package quality

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/asset"
)

// Store persists quality_results rows and exposes history queries used by the
// Phase 6 trend chart (QUAL-06).
//
// Persist runs inside the executor's per-step *sql.Tx so quality_results
// commits atomically with lineage + schema rows (Plan 05-05 D-19).
type Store struct {
	db *sql.DB
}

// NewStore constructs a Store backed by the supplied *sql.DB. The same DB is
// used for both Persist (which receives an external *sql.Tx) and History.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// Persist inserts one quality_results row inside the supplied tx. The row's
// status is the canonical outcome ("passed" | "failed" | "error"); MeasuredValue
// and Threshold are optional — nil values are persisted as NULL.
func (s *Store) Persist(ctx context.Context, tx *sql.Tx, runID uuid.UUID, ruleName, ruleType string, res asset.QualityResult) error {
	const q = `
INSERT INTO quality_results (run_id, rule_name, rule_type, status, measured_value, threshold, error_message)
VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))
`
	var measured, threshold any
	if res.MeasuredValue != nil {
		measured = *res.MeasuredValue
	}
	if res.Threshold != nil {
		threshold = *res.Threshold
	}
	if _, err := tx.ExecContext(ctx, q, runID, ruleName, ruleType, res.Status, measured, threshold, res.ErrorMessage); err != nil {
		return fmt.Errorf("quality.Store.Persist: %w", err)
	}
	return nil
}

// HistoryRow is one row of quality history for a (asset, rule_name) pair.
type HistoryRow struct {
	RunID         uuid.UUID
	RuleName      string
	RuleType      string
	Status        string
	MeasuredValue *float64
	Threshold     *float64
	EvaluatedAt   time.Time
	ErrorMessage  string
}

// History returns the last `limit` quality_results rows for the given asset
// and rule, ordered by evaluated_at DESC. Used by Phase 6 trend dashboards.
func (s *Store) History(ctx context.Context, assetName, ruleName string, limit int) ([]HistoryRow, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
SELECT qr.run_id, qr.rule_name, qr.rule_type, qr.status, qr.measured_value, qr.threshold, qr.evaluated_at, COALESCE(qr.error_message, '')
  FROM quality_results qr
  JOIN runs r ON r.id = qr.run_id
 WHERE r.asset_name = $1 AND qr.rule_name = $2
 ORDER BY qr.evaluated_at DESC
 LIMIT $3
`
	rows, err := s.db.QueryContext(ctx, q, assetName, ruleName, limit)
	if err != nil {
		return nil, fmt.Errorf("quality.Store.History: %w", err)
	}
	defer rows.Close()
	var out []HistoryRow
	for rows.Next() {
		var r HistoryRow
		var measured, threshold sql.NullFloat64
		if err := rows.Scan(&r.RunID, &r.RuleName, &r.RuleType, &r.Status, &measured, &threshold, &r.EvaluatedAt, &r.ErrorMessage); err != nil {
			return nil, fmt.Errorf("quality.Store.History scan: %w", err)
		}
		if measured.Valid {
			v := measured.Float64
			r.MeasuredValue = &v
		}
		if threshold.Valid {
			v := threshold.Float64
			r.Threshold = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
