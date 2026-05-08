package backfill

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Status describes the aggregated execution state of a backfill submission.
// Counts are GROUP BY runs.state for the runs created by this backfill.
type Status struct {
	BackfillID      uuid.UUID
	AssetName       string
	PartitionSpec   string
	TotalPartitions int
	SubmittedAt     time.Time
	CompletedAt     *time.Time
	StateCounts     map[string]int // state -> count (queued|starting|running|succeeded|failed|canceled)
}

// GetStatus aggregates the runs in a backfill by state. Returns the backfills
// row header + a map[state]count produced via SELECT state, COUNT(*) GROUP BY state.
//
// Total in the backfills row reflects the operator's submitted intent
// (len(spec.Keys)). Sum of state counts may be smaller if Submit's
// ON CONFLICT DO NOTHING skipped some keys already in-flight; this is
// intentional and visible to operators inspecting status output.
func GetStatus(ctx context.Context, db *sql.DB, backfillID uuid.UUID) (*Status, error) {
	const headerSQL = `
		SELECT asset_name, partition_spec, total_partitions, submitted_at, completed_at
		FROM backfills WHERE id = $1
	`
	s := &Status{BackfillID: backfillID, StateCounts: map[string]int{}}
	var completed sql.NullTime
	if err := db.QueryRowContext(ctx, headerSQL, backfillID).Scan(
		&s.AssetName, &s.PartitionSpec, &s.TotalPartitions, &s.SubmittedAt, &completed,
	); err != nil {
		return nil, fmt.Errorf("backfill.GetStatus: select backfill: %w", err)
	}
	if completed.Valid {
		t := completed.Time
		s.CompletedAt = &t
	}

	const countsSQL = `SELECT state, COUNT(*) FROM runs WHERE backfill_id = $1 GROUP BY state`
	rows, err := db.QueryContext(ctx, countsSQL, backfillID)
	if err != nil {
		return nil, fmt.Errorf("backfill.GetStatus: select state counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("backfill.GetStatus: scan: %w", err)
		}
		s.StateCounts[state] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("backfill.GetStatus: rows.Err: %w", err)
	}
	return s, nil
}
