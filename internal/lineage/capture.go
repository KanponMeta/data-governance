// Package lineage provides lineage capture writers for asset-level and
// column-level lineage (D-01, D-02, D-04, D-15).
package lineage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/event"
)

// Writer provides SyncStaticEdges (static derivation, D-01) and CaptureRun
// (runtime lineage + drift detection, D-02 + D-04) for the Phase 4 lineage
// capture subsystem.
//
// All SQL is written via *sql.DB / *sql.Tx (raw SQL, following Phase 2
// claim.go precedent — D-16 doesn't require sqlc for write paths).
type Writer struct {
	db     *sql.DB      // for SyncStaticEdges (internal short tx)
	events event.Writer // for appending Phase 4 events to event_log
}

// NewWriter returns a lineage.Writer. db may be nil only when the caller
// guarantees it will only call SyncStaticEdges with assets that have 0
// upstreams (test-only pattern).
func NewWriter(db *sql.DB, events event.Writer) *Writer {
	return &Writer{db: db, events: events}
}

// SyncStaticEdges UPSERTs asset_edges for every declared upstream of a (D-01).
// Uses an internal short transaction. Called from asset.DefinitionRegistry.OnRegister
// hook so every registered asset gets its static lineage graph materialized at
// startup (D-01 static derivation).
//
// For each upstream u in asset.Upstreams():
//   - UPSERT asset_edges (from_asset=u, to_asset=a.Name(), ...)
//     ON CONFLICT (active unique) DO UPDATE SET code_hash_latest, last_seen_at
//
// For edges previously declared but now absent:
//   - UPDATE superseded_at = NOW() (D-15 soft-retire).
//
// Returns nil if no upstreams are declared (no-op, safe to call on source assets).
//
// Guard: returns error if len(upstreams) > 256 (DoS protection, T-04-04-03).
func (w *Writer) SyncStaticEdges(ctx context.Context, a *asset.Asset, codeHash string) error {
	if a == nil || codeHash == "" {
		return fmt.Errorf("lineage: nil asset or empty codeHash")
	}
	ups := a.Upstreams()
	if len(ups) == 0 {
		return nil // source assets have no upstreams; this is a no-op
	}
	if len(ups) > 256 {
		// DoS protection (T-04-04-03): real-world assets have <50 upstreams.
		return fmt.Errorf("lineage: asset %q has %d declared upstreams (max 256); possible misuse", a.Name(), len(ups))
	}

	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("lineage: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()

	// 1. UPSERT each declared upstream → this asset.
	// asset_edges_active_unique is the partial unique index on (from_asset, to_asset)
	// WHERE superseded_at IS NULL — see migration 20260509120000_phase4_lineage_schema.sql.
	const upsertSQL = `
		INSERT INTO asset_edges
			(id, from_asset, to_asset, code_hash_first, code_hash_latest,
			 first_seen_run_id, first_seen_at, last_seen_run_id, last_seen_at)
		VALUES ($1, $2, $3, $4, $4, $5, $6, $5, $6)
		ON CONFLICT ON CONSTRAINT asset_edges_active_unique
		    DO UPDATE SET code_hash_latest = EXCLUDED.code_hash_latest,
		                  last_seen_at     = EXCLUDED.last_seen_at
	`
	for _, u := range ups {
		if _, err := tx.ExecContext(ctx, upsertSQL,
			uuid.New(), u, a.Name(), codeHash, uuid.Nil, now,
		); err != nil {
			return fmt.Errorf("lineage: upsert edge %s→%s: %w", u, a.Name(), err)
		}
	}

	// 2. Soft-retire edges previously declared but no longer in upstreams (D-15).
	retireSQL := `
		UPDATE asset_edges SET superseded_at = $1
		 WHERE to_asset = $2 AND superseded_at IS NULL
		   AND from_asset NOT IN (` + placeholders(len(ups), 3) + `)`
	args := make([]any, 0, 2+len(ups))
	args = append(args, now, a.Name())
	for _, u := range ups {
		args = append(args, u)
	}
	if _, err := tx.ExecContext(ctx, retireSQL, args...); err != nil {
		return fmt.Errorf("lineage: retire stale edges for %s: %w", a.Name(), err)
	}

	return tx.Commit()
}

// CaptureRun is the run-attributed lineage capture hook (D-01 audit trail +
// D-02 column lineage + D-04 platform-driven drift detection).
//
// Called by executor.runStep INSIDE the run-update transaction (D-21 atomicity).
// observedUpstreams is the canonical set of upstream names the user's
// MaterializeFunc actually called io.Read() on this run, derived from the
// executor's trackingIO decorator — never from result.Metadata (D-04: the
// platform observes this, not the user).
//
// Steps:
//  1. Resolve column lineage source (D-02: runtime override wins over builder default).
//  2. UPSERT column_edges rows for each (output, source) pair.
//  3. UPDATE asset_edges last_seen_* for run-attribution (promote uuid.Nil sentinel).
//  4. UPSERT asset_versions for (asset, codeHash) — first run inserts; subsequent runs no-op.
//  5. D-04 drift detection: compare observed vs declared; emit drift_detected + mark pending.
//  6. Emit lineage.captured event with both declared and observed sets.
func (w *Writer) CaptureRun(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	a *asset.Asset, result asset.MaterializeResult, codeHash string,
	observedUpstreams []string) error {

	if a == nil || codeHash == "" {
		return fmt.Errorf("lineage: CaptureRun requires non-nil asset and non-empty codeHash")
	}
	if observedUpstreams == nil {
		observedUpstreams = []string{}
	}

	now := time.Now().UTC()

	// Step 1: Resolve column lineage source (D-02 runtime override wins).
	var cl asset.ColumnLineageMap
	var clSource string
	switch {
	case result.ColumnLineage != nil:
		cl = result.ColumnLineage
		clSource = "runtime"
	case a.ColumnLineage() != nil:
		cl = a.ColumnLineage()
		clSource = "builder_default"
	default:
		clSource = "undeclared"
	}

	// Step 2: UPSERT column_edges (D-13 + D-15 soft-retire).
	// column_edges_active_unique is the partial unique index on
	// (from_asset, from_column, to_asset, to_column) WHERE superseded_at IS NULL
	// AND partition_key IS NULL.
	if cl != nil {
		const colUpsert = `
			INSERT INTO column_edges
				(id, from_asset, from_column, to_asset, to_column,
				 code_hash_first, code_hash_latest,
				 first_seen_run_id, first_seen_at, last_seen_run_id, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, $6, $6, $7, $8, $7, $8)
			ON CONFLICT ON CONSTRAINT column_edges_active_unique
			    DO UPDATE SET code_hash_latest  = EXCLUDED.code_hash_latest,
			                  last_seen_run_id  = EXCLUDED.last_seen_run_id,
			                  last_seen_at      = EXCLUDED.last_seen_at
		`
		for outCol, refs := range cl {
			for _, r := range refs {
				if _, err := tx.ExecContext(ctx, colUpsert,
					uuid.New(), r.Asset, r.Column, a.Name(), outCol, codeHash, runID, now,
				); err != nil {
					return fmt.Errorf("lineage: upsert column edge %s.%s→%s.%s: %w",
						r.Asset, r.Column, a.Name(), outCol, err)
				}
			}
		}
	}

	// Step 3: UPDATE asset_edges last_seen_* for run-attribution.
	// Promotes the uuid.Nil sentinel (inserted by SyncStaticEdges at registration)
	// to the real first_seen_run_id when this is the first run.
	const updRunAttribution = `
		UPDATE asset_edges
		   SET last_seen_run_id = $1, last_seen_at = $2,
		       first_seen_run_id = CASE
		           WHEN first_seen_run_id = '00000000-0000-0000-0000-000000000000'
		           THEN $1
		           ELSE first_seen_run_id
		           END,
		       first_seen_at = CASE
		           WHEN first_seen_run_id = '00000000-0000-0000-0000-000000000000'
		           THEN $2
		           ELSE first_seen_at
		           END
		 WHERE to_asset = $3 AND superseded_at IS NULL
	`
	if _, err := tx.ExecContext(ctx, updRunAttribution, runID, now, a.Name()); err != nil {
		return fmt.Errorf("lineage: update run attribution for %s: %w", a.Name(), err)
	}

	// Step 4: UPSERT asset_versions for (asset, codeHash).
	// First run: INSERT with description/owner/tags/columnLineage and drift_status='clean'.
	// Subsequent runs with same codeHash: ON CONFLICT DO NOTHING (row stays unchanged).
	clJSON, _ := json.Marshal(cl) // nil → "null"
	tagsJSON, _ := json.Marshal(a.Tags())
	const versionUpsert = `
		INSERT INTO asset_versions
			(id, asset, code_hash, description, owner, tags, column_lineage, drift_status, created_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7::jsonb, 'clean', $8)
		ON CONFLICT (asset, code_hash) DO NOTHING
	`
	if _, err := tx.ExecContext(ctx, versionUpsert,
		uuid.New(), a.Name(), codeHash,
		a.Description(), a.Owner(), tagsJSON, clJSON, now,
	); err != nil {
		return fmt.Errorf("lineage: upsert asset_version: %w", err)
	}

	// Step 5: D-04 PLATFORM-DRIVEN drift detection (always runs, no user opt-in).
	// Drift := observed set != declared set (symmetric difference is non-empty).
	drift := !sameSet(observedUpstreams, a.Upstreams())
	if drift {
		// Mark asset_versions.drift_status='pending' atomically in the same tx.
		const markDrift = `UPDATE asset_versions SET drift_status='pending' WHERE asset=$1 AND code_hash=$2`
		if _, err := tx.ExecContext(ctx, markDrift, a.Name(), codeHash); err != nil {
			return fmt.Errorf("lineage: mark drift: %w", err)
		}
		// Compute missing (declared but not Read) and extra (Read but not declared).
		missing, extra := setDiff(a.Upstreams(), observedUpstreams)
		// Emit lineage.drift_detected. The event.Writer uses its own DB connection
		// (not in our tx) — acceptable per D-04 because the canonical drift state is
		// asset_versions.drift_status which IS in our tx.
		if w.events != nil {
			if err := w.events.Append(ctx, event.Event{
				Type:         event.EventTypeLineageDriftDetected,
				ResourceType: "asset",
				ResourceID:   a.Name(),
				Payload: map[string]any{
					"asset":              a.Name(),
					"code_hash":          codeHash,
					"declared_upstreams": a.Upstreams(),
					"observed_upstreams": observedUpstreams,
					"missing":            missing, // declared but never Read
					"extra":              extra,   // Read but not declared
				},
			}); err != nil {
				return fmt.Errorf("lineage: append drift event: %w", err)
			}
		}
	}

	// Step 6: Emit lineage.captured (always; carries full context for operators).
	if w.events != nil {
		if err := w.events.Append(ctx, event.Event{
			Type:         event.EventTypeLineageCaptured,
			ResourceType: "run",
			ResourceID:   runID.String(),
			Payload: map[string]any{
				"asset":                a.Name(),
				"code_hash":            codeHash,
				"declared_upstreams":   a.Upstreams(),
				"observed_upstreams":   observedUpstreams,
				"column_lineage_source": clSource,
				"drift":                drift,
				"run_id":               runID.String(),
			},
		}); err != nil {
			return fmt.Errorf("lineage: append captured event: %w", err)
		}
	}

	return nil
}

// placeholders returns a comma-separated list of $n placeholders starting at start.
// e.g. placeholders(3, 3) returns "$3, $4, $5".
func placeholders(n, start int) string {
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = fmt.Sprintf("$%d", start+i)
	}
	return strings.Join(parts, ", ")
}

// sameSet returns true iff a and b contain exactly the same elements (order-insensitive).
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aCopy := append([]string(nil), a...)
	bCopy := append([]string(nil), b...)
	sort.Strings(aCopy)
	sort.Strings(bCopy)
	for i := range aCopy {
		if aCopy[i] != bCopy[i] {
			return false
		}
	}
	return true
}

// setDiff returns the elements in 'declared' but not in 'observed' (missing)
// and the elements in 'observed' but not in 'declared' (extra).
func setDiff(declared, observed []string) (missing, extra []string) {
	declaredSet := make(map[string]struct{}, len(declared))
	for _, d := range declared {
		declaredSet[d] = struct{}{}
	}
	observedSet := make(map[string]struct{}, len(observed))
	for _, o := range observed {
		observedSet[o] = struct{}{}
	}

	for d := range declaredSet {
		if _, ok := observedSet[d]; !ok {
			missing = append(missing, d)
		}
	}
	for o := range observedSet {
		if _, ok := declaredSet[o]; !ok {
			extra = append(extra, o)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}
