package sensor

import (
	"context"
	"errors"
	"log/slog"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
)

// Daemon is the sensor evaluation harness driver (D-05).
//
// Per D-05 sensors share the scheduler subcommand's tick loop — the scheduler
// calls Daemon.RunOnce(ctx) inside its tick after firing due schedules. There
// is no separate sensor goroutine pool; SKIP LOCKED on the sensors table is
// the multi-replica safety primitive.
type Daemon struct {
	// Store is the persistence boundary; only DB() is used (raw *sql.DB).
	Store storage.Storage
	// Registry is the in-process asset registry — evaluateOneSensor uses it
	// to resolve SensorSpec from sensor_name.
	Registry *asset.DefinitionRegistry
	// Events is the audit log writer.
	Events event.Writer
	// DisableAfter overrides AutoDisableThreshold (D-08). 0 → use the package default.
	DisableAfter int
}

// RunOnce drains the queue of currently-due sensors. Designed to be called
// from the scheduler subcommand's tick loop.
//
// Behaviour:
//   - Repeatedly calls evaluateOneSensor until ErrNoDueSensor is returned.
//   - On a context error (cancellation / deadline), returns the ctx error so
//     the scheduler can shut down cleanly.
//   - On a transient DB error from evaluateOneSensor, logs and returns nil so
//     the next tick retries — does NOT propagate, since one bad sensor must
//     not stop the scheduler.
func (d *Daemon) RunOnce(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := evaluateOneSensor(ctx, d.Store, d.Registry, d.Events, d.DisableAfter)
		if errors.Is(err, ErrNoDueSensor) {
			return nil
		}
		if err != nil {
			slog.Error("sensor.evaluate_failed", "error", err)
			return nil
		}
	}
}
