// Package governance implements Phase 5 governance primitives that span
// multiple subsystems (lineage + audit + policy). The PII propagator
// (Plan 05-03 D-06) is the first inhabitant: it walks column_edges
// inside the lineage_writer transaction, applies the union rule
// ("any upstream pii=true → output pii=true"), and emits the
// metadata.tag_overridden audit entry the first time an explicit
// override is observed.
package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/audit"
)

// ColumnRef is a (asset, column) pair the propagator iterates.
// It mirrors asset.ColumnRef but lives here to avoid importing the
// asset package inside SQL-builder code paths that don't otherwise need it.
type ColumnRef struct {
	Asset  string
	Column string
}

// Propagator walks column_edges synchronously within the caller's *sql.Tx
// to enforce two governance invariants on every materialization:
//
//  1. Union rule — if ANY upstream column referenced by an active edge
//     to (asset, column) carries pii=true, the output column inherits
//     pii=true. Stored in column_pii_tags with source='upstream'.
//  2. Override path — if the asset declares
//     Builder.Column(c).TagOverride(asset.TagOverride{Remove:"pii", Reason:"..."}),
//     propagation is suppressed for that column AND a metadata.tag_overridden
//     audit entry is appended (first time only — subsequent re-runs are
//     idempotent, gated by column_pii_tags.pii_override_audit_seq).
//
// All writes happen on the caller-supplied *sql.Tx — the propagator NEVER
// opens a new connection or new transaction. If Propagate returns an error,
// the lineage_writer rollback rolls back column_edges UPSERT plus any
// partial column_pii_tags writes, so the post-rollback database is
// indistinguishable from "this materialization never happened".
type Propagator struct{}

// NewPropagator constructs a Propagator. There is no per-instance state —
// the type exists so callers can mock the contract in tests via an
// interface rather than a free function.
func NewPropagator() *Propagator { return &Propagator{} }

// Propagate runs the two invariants for the supplied output columns.
//
//   - tx is the lineage_writer's open transaction (post-column_edges UPSERT).
//   - runID is the run that triggered the materialization; it ends up on
//     audit payloads and column_pii_tags.source_run_id.
//   - outputColumns is the set of columns this asset wrote on this run.
//   - overrides is the list of builder-declared TagOverride directives for
//     this asset (typically asset.TagOverrides()).
//
// outputColumns may be empty (an asset that ran but declared no column
// lineage); in that case the function still walks overrides because an
// override may set pii without requiring a lineage edge.
func (p *Propagator) Propagate(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	outputColumns []ColumnRef, overrides []asset.ColumnTagOverride) error {

	if tx == nil {
		return fmt.Errorf("governance: Propagate requires non-nil tx")
	}

	// Index overrides by (asset, column) for O(1) lookup.
	overrideByKey := make(map[string]asset.ColumnTagOverride, len(overrides))
	for _, o := range overrides {
		overrideByKey[o.Asset+"."+o.Column] = o
	}

	// Walk overrides first so even output columns that don't appear in
	// outputColumns get their override row written (an asset may declare an
	// override for a column whose lineage was provided in a previous run).
	processed := make(map[string]struct{}, len(outputColumns)+len(overrides))

	// 1. Process explicit overrides.
	for _, o := range overrides {
		key := o.Asset + "." + o.Column
		processed[key] = struct{}{}
		if err := p.applyOverride(ctx, tx, runID, o); err != nil {
			return err
		}
	}

	// 2. Process output columns that were not the subject of an override —
	// the BFS-union check looks at active column_edges and column_pii_tags.
	for _, c := range outputColumns {
		key := c.Asset + "." + c.Column
		if _, alreadyProcessed := processed[key]; alreadyProcessed {
			continue
		}
		if err := p.propagateUnion(ctx, tx, runID, c); err != nil {
			return err
		}
	}

	return nil
}

// applyOverride writes (or updates) the column_pii_tags row driven by an
// explicit Builder.TagOverride. The first time an override is observed, an
// audit_log entry is written and its sequence stored on the row to
// short-circuit subsequent re-runs.
func (p *Propagator) applyOverride(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	o asset.ColumnTagOverride) error {

	// Read current row (if any) to discover prior pii state and prior audit seq.
	var (
		havePrior      bool
		priorPII       bool
		priorAuditSeq  sql.NullInt64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT pii, pii_override_audit_seq
		  FROM column_pii_tags
		 WHERE asset = $1 AND column_name = $2
	`, o.Asset, o.Column).Scan(&priorPII, &priorAuditSeq)
	switch {
	case err == sql.ErrNoRows:
		havePrior = false
	case err != nil:
		return fmt.Errorf("governance: read existing override row: %w", err)
	default:
		havePrior = true
	}

	// Determine the new pii flag based on Remove / Add semantics.
	// Remove="pii" → pii=false; Add="pii" → pii=true; otherwise carry forward.
	newPII := priorPII
	if o.Override.Remove == "pii" {
		newPII = false
	}
	if o.Override.Add == "pii" {
		newPII = true
	}

	// Decide whether to emit a new audit entry. We emit on FIRST observation
	// of the override row (no prior_audit_seq AND prior pii state differs
	// from the override target — i.e., the override actually changed
	// something). Re-runs with the same code_hash see the seq populated and
	// skip the audit emission.
	emitAudit := !priorAuditSeq.Valid

	// Carry forward prior audit_seq when we don't emit; otherwise it stays
	// NULL so we set it after WriteEntry below.
	var newAuditSeq sql.NullInt64
	if priorAuditSeq.Valid {
		newAuditSeq = priorAuditSeq
	}

	// 3. UPSERT (asset, column_name) — primary-key-based.
	if !havePrior {
		// INSERT
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO column_pii_tags
			    (asset, column_name, pii, source, source_run_id,
			     override_reason, override_actor_id, pii_override_audit_seq,
			     propagated_from, set_at, set_by)
			VALUES ($1, $2, $3, 'override', $4, $5, NULL, $6, '[]'::jsonb, NOW(), NULL)
		`, o.Asset, o.Column, newPII, runID, o.Override.Reason, newAuditSeq); err != nil {
			return fmt.Errorf("governance: insert override row: %w", err)
		}
	} else {
		// UPDATE — the same row can be re-applied across runs.
		if _, err := tx.ExecContext(ctx, `
			UPDATE column_pii_tags
			   SET pii = $3,
			       source = 'override',
			       source_run_id = $4,
			       override_reason = $5,
			       set_at = NOW()
			 WHERE asset = $1 AND column_name = $2
		`, o.Asset, o.Column, newPII, runID, o.Override.Reason); err != nil {
			return fmt.Errorf("governance: update override row: %w", err)
		}
	}

	if !emitAudit {
		return nil
	}

	// 4. Write metadata.tag_overridden to the audit chain.
	payload := map[string]any{
		"asset":       o.Asset,
		"column":      o.Column,
		"removed_tag": o.Override.Remove,
		"added_tag":   o.Override.Add,
		"reason":      o.Override.Reason,
		"run_id":      runID.String(),
	}
	seq, err := audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    audit.AuditMetadataTagOverridden,
		OccurredAt:   time.Now().UTC(),
		ActorID:      nil, // builder declaration → system actor (NULL); REST PATCH would set actor
		ResourceType: "column",
		ResourceID:   o.Asset + "." + o.Column,
		Payload:      payload,
	})
	if err != nil {
		return fmt.Errorf("governance: write audit override: %w", err)
	}

	// 5. Persist the audit seq so future re-runs are idempotent.
	if _, err := tx.ExecContext(ctx, `
		UPDATE column_pii_tags
		   SET pii_override_audit_seq = $1
		 WHERE asset = $2 AND column_name = $3
	`, seq, o.Asset, o.Column); err != nil {
		return fmt.Errorf("governance: persist override audit seq: %w", err)
	}
	return nil
}

// propagateUnion runs the BFS-of-depth-1 union rule for one output column:
// SELECT EXISTS (any upstream column edge to (asset, column) where the
// upstream column carries pii=true). When yes, write/update column_pii_tags
// with source='upstream' and propagated_from = JSON list of contributing
// upstream refs (operator visibility).
func (p *Propagator) propagateUnion(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	c ColumnRef) error {

	// Collect every active upstream column edge into (asset, column) and
	// determine which of those uppstream columns are flagged pii=true.
	rows, err := tx.QueryContext(ctx, `
		SELECT ce.from_asset, ce.from_column, COALESCE(t.pii, FALSE) AS upstream_pii
		  FROM column_edges ce
		  LEFT JOIN column_pii_tags t
		    ON t.asset = ce.from_asset
		   AND t.column_name = ce.from_column
		 WHERE ce.to_asset = $1
		   AND ce.to_column = $2
		   AND ce.superseded_at IS NULL
	`, c.Asset, c.Column)
	if err != nil {
		return fmt.Errorf("governance: read upstreams for %s.%s: %w", c.Asset, c.Column, err)
	}
	defer rows.Close()

	type upstream struct {
		Asset  string `json:"asset"`
		Column string `json:"column"`
	}
	var contributingUpstreams []upstream
	anyPII := false
	for rows.Next() {
		var fa, fc string
		var pii bool
		if err := rows.Scan(&fa, &fc, &pii); err != nil {
			return fmt.Errorf("governance: scan upstream row: %w", err)
		}
		if pii {
			anyPII = true
			contributingUpstreams = append(contributingUpstreams, upstream{Asset: fa, Column: fc})
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("governance: iterate upstreams: %w", err)
	}

	if !anyPII {
		// No upstream is pii — leave column_pii_tags untouched. (Existing
		// override or upstream rows from earlier runs survive.)
		return nil
	}

	// Sort upstream list deterministically so the persisted JSON is stable
	// across runs — useful for asserting test fixtures + audit reads.
	sort.Slice(contributingUpstreams, func(i, j int) bool {
		if contributingUpstreams[i].Asset != contributingUpstreams[j].Asset {
			return contributingUpstreams[i].Asset < contributingUpstreams[j].Asset
		}
		return contributingUpstreams[i].Column < contributingUpstreams[j].Column
	})

	propagatedJSON, err := json.Marshal(contributingUpstreams)
	if err != nil {
		return fmt.Errorf("governance: marshal propagated_from: %w", err)
	}

	// Read existing row to determine whether to INSERT or UPDATE. We MUST
	// NOT overwrite an override row (source='override') — overrides win.
	var (
		havePrior  bool
		priorSrc   string
	)
	err = tx.QueryRowContext(ctx, `
		SELECT source FROM column_pii_tags
		 WHERE asset = $1 AND column_name = $2
	`, c.Asset, c.Column).Scan(&priorSrc)
	switch {
	case err == sql.ErrNoRows:
		havePrior = false
	case err != nil:
		return fmt.Errorf("governance: read prior tag row: %w", err)
	default:
		havePrior = true
	}

	if havePrior && priorSrc == "override" {
		// Override wins — do nothing.
		return nil
	}

	if !havePrior {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO column_pii_tags
			    (asset, column_name, pii, source, source_run_id,
			     propagated_from, set_at)
			VALUES ($1, $2, TRUE, 'upstream', $3, $4::jsonb, NOW())
		`, c.Asset, c.Column, runID, string(propagatedJSON)); err != nil {
			return fmt.Errorf("governance: insert upstream pii row: %w", err)
		}
	} else {
		if _, err := tx.ExecContext(ctx, `
			UPDATE column_pii_tags
			   SET pii = TRUE,
			       source = 'upstream',
			       source_run_id = $3,
			       propagated_from = $4::jsonb,
			       set_at = NOW()
			 WHERE asset = $1 AND column_name = $2
		`, c.Asset, c.Column, runID, string(propagatedJSON)); err != nil {
			return fmt.Errorf("governance: update upstream pii row: %w", err)
		}
	}
	return nil
}
