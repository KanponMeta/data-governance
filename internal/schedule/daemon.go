package schedule

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
)

// DefaultInterval is the default tick interval for Daemon.run when Interval == 0.
const DefaultInterval = 30 * time.Second

// jitterMaxMs is the upper bound (exclusive) for the per-tick jitter added on
// top of Interval. Mitigates thundering-herd across multiple replicas.
const jitterMaxMs = 5000

// FreshnessScanner is the optional Phase 5 SLA scanner hook (Plan 05-05 D-20).
// Implementations live in internal/quality.Scanner. Daemon calls Scan on each
// tick after sensor / schedule evaluation; nil = skip.
type FreshnessScanner interface {
	Scan(ctx context.Context) (int, error)
}

// Daemon wraps the package-internal tick loop driver. The exported fields are
// the runtime dependencies; Interval defaults to DefaultInterval when zero.
//
// Production callers (cmd/platform/scheduler.go in plan 03-06) do NOT use
// this struct — they drive their own tick loop and call FireOneSchedule
// directly so they can interleave sensor evaluation per D-05. Daemon and its
// unexported run() method exist solely as a self-contained test surface for
// loop behavior in isolation (TestDaemonRunCancellation, TestDaemonUpsertOnStart).
type Daemon struct {
	Store    storage.Storage
	Registry *asset.DefinitionRegistry
	Events   event.Writer
	Interval time.Duration

	// Phase 5 Plan 05-05 (D-20) — optional FreshnessSLA scanner. nil = skip.
	// When non-nil, Scan(ctx) is called on each tick after schedule firing.
	freshnessScanner FreshnessScanner
}

// WithFreshnessScanner attaches a FreshnessScanner; idempotent.
func (d *Daemon) WithFreshnessScanner(s FreshnessScanner) *Daemon {
	d.freshnessScanner = s
	return d
}

// run executes the tick loop until ctx is canceled. UNEXPORTED — production
// code drives its own tick loop and calls FireOneSchedule directly. This
// method exists for package-internal tests in daemon_test.go.
//
// At start, run calls UpsertSchedules to reconcile the registry with the
// schedules table. Errors are logged but non-fatal — the daemon can still
// fire whatever rows already exist.
//
// The very first tick runs immediately on start (so a daemon restart picks
// up missed windows without waiting one full Interval). Subsequent ticks
// fire at Interval + jitter[0..jitterMaxMs).
func (d *Daemon) run(ctx context.Context) error {
	if d.Interval == 0 {
		d.Interval = DefaultInterval
	}
	if err := UpsertSchedules(ctx, d.Store, d.Registry); err != nil {
		slog.Error("schedule.upsert_failed", "error", err)
		// Continue — daemon can still fire whatever rows already exist.
	}

	// Run one tick immediately on start to handle any missed windows.
	d.tick(ctx)

	for {
		jitter := time.Duration(rand.Int64N(jitterMaxMs)) * time.Millisecond
		select {
		case <-time.After(d.Interval + jitter):
			d.tick(ctx)
		case <-ctx.Done():
			slog.Info("schedule.shutdown")
			return ctx.Err()
		}
	}
}

// tick fires due schedules until none remain (or ctx canceled, or fire error).
// Unexported — called only by run() and by package-internal tests.
//
// On any FireOneSchedule error other than ErrNoDueSchedule, tick logs and
// returns; the next tick retries. This back-off prevents a flaky schedule row
// (e.g. partition_key conflict) from busy-looping the daemon.
//
// Phase 5 Plan 05-05 (D-20): after schedule firing completes, the freshness
// scanner runs (if configured) so SLA breaches surface within one tick of
// becoming stale.
func (d *Daemon) tick(ctx context.Context) {
	now := time.Now().UTC()
	for {
		if ctx.Err() != nil {
			return
		}
		err := FireOneSchedule(ctx, d.Store, d.Registry, d.Events, now)
		if errors.Is(err, ErrNoDueSchedule) {
			break
		}
		if err != nil {
			slog.Error("schedule.fire_failed", "error", err)
			return
		}
	}
	// Phase 5 freshness scan — runs once per tick.
	if d.freshnessScanner != nil {
		if n, err := d.freshnessScanner.Scan(ctx); err != nil {
			slog.Error("freshness scan", "err", err)
		} else if n > 0 {
			slog.Info("freshness breaches emitted", "count", n)
		}
	}
}
