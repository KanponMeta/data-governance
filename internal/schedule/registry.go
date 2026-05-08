package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/storage"
)

// UpsertSchedules reconciles asset.DefinitionRegistry.All() with the schedules
// table. For each asset whose Schedule() != nil:
//   - INSERT a new row with next_fire_at = parsed.Next(now), or
//   - UPDATE the existing row when cron_expr changed (recomputing next_fire_at
//     from now — the previous schedule is invalid by definition).
//
// Idempotent across daemon restarts. Called once at daemon start (Open
// Question 3 default). Schedules whose registry counterpart was removed are
// left in place — operator must clean up explicitly (Phase 6 pause/disable).
//
// Cron expressions are revalidated here as defense-in-depth (T-03-04-01) —
// asset.Builder.Build() already validates at registration, but the daemon
// must not crash on a corrupt expr that somehow reached the table.
func UpsertSchedules(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error {
	db := store.DB()
	for _, name := range reg.List() {
		a, err := reg.Get(name)
		if err != nil {
			return fmt.Errorf("schedule.upsert: get %q: %w", name, err)
		}
		sp := a.Schedule()
		if sp == nil {
			continue
		}
		sched, err := cronParser.Parse(sp.CronExpr)
		if err != nil {
			return fmt.Errorf("schedule.upsert: registry asset %q has invalid cron %q: %w", name, sp.CronExpr, err)
		}
		nextFire := sched.Next(time.Now().UTC())

		// SERIALIZABLE isolation prevents the TOCTOU race between two replicas
		// both SELECTing "not found" and both INSERTing. One transaction will
		// abort with a serialisation failure (the reviewer can retry).
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
		if err != nil {
			return fmt.Errorf("schedule.upsert: begin tx for %q: %w", name, err)
		}
		defer func() { _ = tx.Rollback() }()

		const selectSQL = `SELECT id, cron_expr FROM schedules WHERE asset_name = $1 LIMIT 1`
		var existingID, existingCron string
		err = tx.QueryRowContext(ctx, selectSQL, name).Scan(&existingID, &existingCron)
		switch {
		case err == nil:
			if existingCron == sp.CronExpr {
				if err := tx.Commit(); err != nil {
					return fmt.Errorf("schedule.upsert: commit (unchanged) for %q: %w", name, err)
				}
				continue // unchanged — no UPDATE needed
			}
			const updateSQL = `
				UPDATE schedules
				   SET cron_expr = $1, next_fire_at = $2, updated_at = NOW()
				 WHERE id = $3::uuid
			`
			if _, err := tx.ExecContext(ctx, updateSQL, sp.CronExpr, nextFire, existingID); err != nil {
				return fmt.Errorf("schedule.upsert: update %q: %w", name, err)
			}
		case errors.Is(err, sql.ErrNoRows):
			const insertSQL = `
				INSERT INTO schedules (id, asset_name, cron_expr, next_fire_at, created_at, updated_at)
				VALUES (gen_random_uuid(), $1, $2, $3, NOW(), NOW())
			`
			if _, err := tx.ExecContext(ctx, insertSQL, name, sp.CronExpr, nextFire); err != nil {
				return fmt.Errorf("schedule.upsert: insert %q: %w", name, err)
			}
		default:
			return fmt.Errorf("schedule.upsert: select existing for %q: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("schedule.upsert: commit for %q: %w", name, err)
		}
	}
	return nil
}
