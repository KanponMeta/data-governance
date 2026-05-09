package quality

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/notification"
)

// Scanner scans schedules for SLA breaches and emits sla.breached events +
// enqueues notification jobs. Designed to run on the scheduler tick (Plan 05-05
// D-20) — one Scan call per tick is enough.
//
// Dedup window: a schedule row's freshness_breach_emitted_at is set to NOW()
// after each emit; the next emit only fires when (NOW - emitted_at) > MaxLag.
// commitSuccess clears freshness_breach_emitted_at on every successful run so
// a recovered SLA emits a fresh breach event the next time it goes stale.
type Scanner struct {
	db     *sql.DB
	queue  notification.JobInserter
	events event.Writer
}

// NewScanner constructs a Scanner. queue may be nil — sla events still go to
// event_log but no notification jobs are enqueued.
func NewScanner(db *sql.DB, queue notification.JobInserter, events event.Writer) *Scanner {
	return &Scanner{db: db, queue: queue, events: events}
}

// breachRow is one schedules row that exceeded its freshness budget.
type breachRow struct {
	asset             string
	maxLagSeconds     int
	lastSucceededAt   sql.NullTime
}

// Scan runs the SLA query and emits one sla.breached event per row.
// Returns the number of breaches emitted (for slog observability) + any DB error.
//
// SQL contract: only rows with freshness_max_lag_seconds NOT NULL participate.
// Two breach predicates are checked:
//  1. last_succeeded_at + max_lag < NOW()        — stale after a previous success
//  2. last_succeeded_at IS NULL AND created_at + max_lag < NOW()
//                                                — never succeeded since creation
//
// Dedup: skip if freshness_breach_emitted_at >= NOW - max_lag.
func (s *Scanner) Scan(ctx context.Context) (int, error) {
	const q = `
SELECT asset_name, freshness_max_lag_seconds, last_succeeded_at
  FROM schedules
 WHERE freshness_max_lag_seconds IS NOT NULL
   AND (
     (last_succeeded_at IS NOT NULL AND last_succeeded_at + interval '1 second' * freshness_max_lag_seconds < NOW())
     OR (last_succeeded_at IS NULL AND created_at + interval '1 second' * freshness_max_lag_seconds < NOW())
   )
   AND (freshness_breach_emitted_at IS NULL
        OR freshness_breach_emitted_at < NOW() - interval '1 second' * freshness_max_lag_seconds)
`
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("freshness.Scan: query: %w", err)
	}
	defer rows.Close()

	var breaches []breachRow
	for rows.Next() {
		var b breachRow
		if err := rows.Scan(&b.asset, &b.maxLagSeconds, &b.lastSucceededAt); err != nil {
			return 0, fmt.Errorf("freshness.Scan: scan: %w", err)
		}
		breaches = append(breaches, b)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("freshness.Scan: rows: %w", err)
	}

	for _, b := range breaches {
		if err := s.emitBreach(ctx, b); err != nil {
			return 0, err
		}
	}
	return len(breaches), nil
}

func (s *Scanner) emitBreach(ctx context.Context, b breachRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("freshness.emitBreach: begin: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	now := time.Now().UTC()
	windowStart := now.Add(-time.Duration(b.maxLagSeconds) * time.Second).Format(time.RFC3339)
	var lastSucceededRendered *string
	if b.lastSucceededAt.Valid {
		s := b.lastSucceededAt.Time.Format(time.RFC3339)
		lastSucceededRendered = &s
	}

	// 1. Update dedup window marker.
	if _, err := tx.ExecContext(ctx,
		`UPDATE schedules SET freshness_breach_emitted_at = NOW() WHERE asset_name = $1`,
		b.asset); err != nil {
		return fmt.Errorf("freshness.emitBreach: update marker: %w", err)
	}

	// 2. Append sla.breached event.
	if err := s.events.Append(ctx, event.Event{
		Type:         event.EventTypeSLABreached,
		ResourceType: "asset",
		ResourceID:   b.asset,
		Payload: event.SLABreachedPayload{
			Asset:             b.asset,
			MaxLagSeconds:     b.maxLagSeconds,
			LastSucceededAt:   lastSucceededRendered,
			BreachWindowStart: windowStart,
		},
	}); err != nil {
		return fmt.Errorf("freshness.emitBreach: append event: %w", err)
	}

	// 3. Enqueue notification job (best-effort — queue may be nil).
	if s.queue != nil {
		args := notification.NotificationDispatchArgs{
			EventType: "sla.breached",
			AssetName: b.asset,
			Payload: map[string]any{
				"asset":               b.asset,
				"max_lag_seconds":     b.maxLagSeconds,
				"breach_window_start": windowStart,
			},
		}
		if err := s.queue.InsertTx(ctx, tx, args); err != nil {
			return fmt.Errorf("freshness.emitBreach: enqueue: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("freshness.emitBreach: commit: %w", err)
	}
	rollback = false
	return nil
}
