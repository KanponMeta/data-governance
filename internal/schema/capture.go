package schema

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/event"
)

// prevVersionData holds the data read from the latest schema_versions row.
type prevVersionData struct {
	id     uuid.UUID
	hash   string
	schema connector.Schema
}

// Writer provides schema capture (D-05 + D-06 + D-07 + D-08) for the
// Phase 4 data governance platform.
//
// Capture is called by executor.runStep INSIDE the run-update transaction
// (D-21 atomicity). On error from SchemaDescriber: non-fatal (emits
// schema.capture_failed, run continues). All other errors are fatal and
// cause the enclosing transaction to roll back.
type Writer struct {
	events event.Writer
}

// NewWriter returns a schema.Writer. events is the Phase 1 event log writer.
func NewWriter(events event.Writer) *Writer {
	return &Writer{events: events}
}

// Capture is the schema capture hook (D-05 + D-06 + D-08).
//
// Resolution order for schema source:
//  1. conn.(connector.SchemaDescriber).DescribeSchema(ctx, ref) — if available + no error.
//  2. result.Schema — if user's Materialize populated it (D-06 fallback).
//  3. Otherwise: emit schema.captured with {tag: "schema_capture_unsupported"}; return nil.
//
// On error from DescribeSchema (non-fatal per D-08):
//
//	Emit schema.capture_failed with {error}; return nil (run continues).
//
// On successful schema capture:
//  1. Compute schemaHash via HashSchema (Pitfall 5: alphabetical column sort).
//  2. SELECT schema_hash, id FROM schema_versions WHERE asset=$1 ORDER BY captured_at DESC LIMIT 1.
//  3a. If hit AND hash matches: UPDATE last_seen_run_id, last_seen_at; emit schema.unchanged.
//  3b. If miss OR hash differs: INSERT new schema_versions row; emit schema.change_detected.
//  4. Emit schema.captured with {asset, schema_hash, version_id, capture_source}.
//
// Size guards (DoS mitigations):
//   - T-04-04-05: > 10000 columns → schema.capture_failed (malicious DescribeSchema)
//   - T-04-04-07: 0 columns from descriptor → schema.capture_failed (no access)
func (w *Writer) Capture(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	a *asset.Asset, result asset.MaterializeResult,
	conn connector.Connector, ref connector.AssetRef, codeHash string) error {

	// Step 1: Resolve schema source.
	var captured connector.Schema
	var captureSource string

	if d, ok := conn.(connector.SchemaDescriber); ok {
		s, err := d.DescribeSchema(ctx, ref)
		if err != nil {
			slog.Warn("schema.capture_failed", "asset", a.Name(), "error", err)
			if w.events != nil {
				_ = w.events.Append(ctx, event.Event{
					Type:         event.EventTypeSchemaCaptureFailed,
					ResourceType: "run",
					ResourceID:   runID.String(),
					Payload:      map[string]any{"asset": a.Name(), "error": err.Error()},
				})
			}
			return nil // non-fatal per D-08
		}
		// T-04-04-07: empty columns from descriptor = no table access
		if len(s.Columns) == 0 {
			slog.Warn("schema.capture_failed_no_columns", "asset", a.Name())
			if w.events != nil {
				_ = w.events.Append(ctx, event.Event{
					Type:         event.EventTypeSchemaCaptureFailed,
					ResourceType: "run",
					ResourceID:   runID.String(),
					Payload:      map[string]any{"asset": a.Name(), "error": "descriptor returned 0 columns"},
				})
			}
			return nil
		}
		// T-04-04-05: column count DoS guard
		if len(s.Columns) > 10000 {
			slog.Warn("schema.capture_failed_too_many_columns", "asset", a.Name(), "count", len(s.Columns))
			if w.events != nil {
				_ = w.events.Append(ctx, event.Event{
					Type:         event.EventTypeSchemaCaptureFailed,
					ResourceType: "run",
					ResourceID:   runID.String(),
					Payload: map[string]any{
						"asset": a.Name(),
						"error": fmt.Sprintf("descriptor returned %d columns (max 10000)", len(s.Columns)),
					},
				})
			}
			return nil
		}
		captured = s
		captureSource = "descriptor"
	} else if result.Schema != nil {
		captured = *result.Schema
		captureSource = "materialize_result"
	} else {
		// No schema capture available — emit informational event.
		if w.events != nil {
			_ = w.events.Append(ctx, event.Event{
				Type:         event.EventTypeSchemaCaptured,
				ResourceType: "run",
				ResourceID:   runID.String(),
				Payload: map[string]any{
					"asset": a.Name(),
					"tag":   "schema_capture_unsupported",
				},
			})
		}
		return nil
	}

	// Step 2: Compute schema hash + dedup query.
	// tx may be nil in unit tests where no DB writes are performed.
	schemaHash := HashSchema(captured)

	now := time.Now().UTC()
	var versionID uuid.UUID

	if tx != nil {
		// Step 2: Query the latest schema_versions row for this asset.
		// We SELECT schema_data so we can unmarshal prevSchema for Diff (plan 04-05).
		const latestQuery = `
			SELECT id, schema_hash, schema_data FROM schema_versions
			 WHERE asset = $1 ORDER BY captured_at DESC LIMIT 1
		`
		var prev prevVersionData
		var rawSchemaData []byte
		err := tx.QueryRowContext(ctx, latestQuery, a.Name()).Scan(&prev.id, &prev.hash, &rawSchemaData)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("schema: latest version query: %w", err)
		}
		hasPrev := err == nil

		// Unmarshal the previous schema_data if we have a prev row (T-04-05-01 note:
		// json.Unmarshal returns error, not panic; we treat unmarshal failure as missing prev).
		if hasPrev && len(rawSchemaData) > 0 {
			if err := json.Unmarshal(rawSchemaData, &prev.schema); err != nil {
				slog.Warn("schema: failed to unmarshal prev schema_data; diff will be skipped",
					"asset", a.Name(), "error", err)
				hasPrev = false // treat as first capture to avoid diff on bad data
			}
		}
		prev.schema.CapturedAt = time.Time{} // not stored; zero is fine

		if hasPrev && prev.hash == schemaHash {
			// Step 3a: dedup — UPDATE last_seen_*.
			versionID = prev.id
			const upd = `UPDATE schema_versions SET last_seen_run_id=$1, last_seen_at=$2 WHERE id=$3`
			if _, err := tx.ExecContext(ctx, upd, runID, now, prev.id); err != nil {
				return fmt.Errorf("schema: dedup update: %w", err)
			}
			if w.events != nil {
				_ = w.events.Append(ctx, event.Event{
					Type:         event.EventTypeSchemaUnchanged,
					ResourceType: "run",
					ResourceID:   runID.String(),
					Payload: map[string]any{
						"asset":       a.Name(),
						"schema_hash": schemaHash,
						"version_id":  prev.id.String(),
					},
				})
			}
		} else {
			// Step 3b: INSERT new schema_versions row.
			versionID = uuid.New()
			schemaJSON, _ := json.Marshal(captured)
			const ins = `
				INSERT INTO schema_versions
					(id, asset, code_hash, schema_hash, schema_data,
					 captured_at, last_seen_at, last_seen_run_id)
				VALUES ($1, $2, $3, $4, $5::jsonb, $6, $6, $7)
			`
			if _, err := tx.ExecContext(ctx, ins,
				versionID, a.Name(), codeHash, schemaHash, schemaJSON, now, runID,
			); err != nil {
				return fmt.Errorf("schema: insert version: %w", err)
			}

			// Step 3c: Compute diff + write schema_changes rows (plan 04-05).
			// Only diff if we have a valid previous schema to compare against.
			var changes []SchemaChange
			if hasPrev {
				changes = Diff(prev.schema, captured)
			}

			var prevIDPtr *uuid.UUID
			if hasPrev {
				prevIDPtr = &prev.id
			}

			changeIDs, err := WriteSchemaChanges(ctx, tx, runID, a.Name(), codeHash,
				prevIDPtr, versionID, changes)
			if err != nil {
				return fmt.Errorf("schema: write schema_changes: %w", err)
			}

			// Emit schema.change_detected with audit-pointer payload (D-11).
			if w.events != nil {
				payload := map[string]any{
					"asset":              a.Name(),
					"schema_hash":        schemaHash,
					"new_version_id":     versionID.String(),
					"code_hash":          codeHash,
					"schema_changes_ids": uuidsToStrings(changeIDs),
				}
				if hasPrev {
					payload["prev_version_id"] = prev.id.String()
				}
				_ = w.events.Append(ctx, event.Event{
					Type:         event.EventTypeSchemaChangeDetected,
					ResourceType: "run",
					ResourceID:   runID.String(),
					Payload:      payload,
				})
			}
		}
	}

	// Step 4: Emit schema.captured for every successful capture (with or without tx).
	if w.events != nil {
		payload := map[string]any{
			"asset":          a.Name(),
			"schema_hash":    schemaHash,
			"capture_source": captureSource,
		}
		if versionID != uuid.Nil {
			payload["version_id"] = versionID.String()
		}
		_ = w.events.Append(ctx, event.Event{
			Type:         event.EventTypeSchemaCaptured,
			ResourceType: "run",
			ResourceID:   runID.String(),
			Payload:      payload,
		})
	}

	return nil
}
