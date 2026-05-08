package sensor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/storage"
)

// UpsertSensors reconciles asset.DefinitionRegistry → sensors table.
// Idempotent: identical specs produce no UPDATE, changed MinInterval is
// propagated. Removed sensors are NOT deleted from the table — they are
// left to be evaluated and orphan-disabled by evaluateOneSensor (consistent
// with the schedules behaviour from plan 03-04).
//
// Called from the scheduler subcommand (plan 03-06) at startup so the in-memory
// registry definitions get a sensors row each. Safe to call repeatedly.
func UpsertSensors(ctx context.Context, store storage.Storage, reg *asset.DefinitionRegistry) error {
	db := store.DB()
	for _, name := range reg.List() {
		a, err := reg.Get(name)
		if err != nil || a == nil {
			continue
		}
		for _, spec := range a.Sensors() {
			if err := upsertOneSensor(ctx, db, a.Name(), spec); err != nil {
				return fmt.Errorf("sensor.upsert(%s/%s): %w", a.Name(), spec.Name, err)
			}
		}
	}
	return nil
}

// upsertOneSensor SELECTs the existing row (if any), UPDATEs MinInterval when
// changed, otherwise INSERTs a fresh row with default zero values.
func upsertOneSensor(ctx context.Context, db *sql.DB, assetName string, spec asset.SensorSpec) error {
	minIntervalSec := int64(spec.MinInterval.Seconds())
	if minIntervalSec <= 0 {
		minIntervalSec = int64(DefaultMinInterval.Seconds())
	}

	const selectSQL = `SELECT id, min_interval_seconds FROM sensors WHERE asset_name=$1 AND sensor_name=$2 LIMIT 1`
	var (
		id             string
		existingMinIvl int64
	)
	err := db.QueryRowContext(ctx, selectSQL, assetName, spec.Name).Scan(&id, &existingMinIvl)
	if err == nil {
		if existingMinIvl == minIntervalSec {
			return nil // unchanged
		}
		const updateSQL = `UPDATE sensors SET min_interval_seconds=$1, updated_at=NOW() WHERE id=$2`
		_, err = db.ExecContext(ctx, updateSQL, minIntervalSec, id)
		return err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	const insertSQL = `
		INSERT INTO sensors (id, asset_name, sensor_name, min_interval_seconds, consecutive_failures, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 0, NOW(), NOW())
	`
	_, err = db.ExecContext(ctx, insertSQL, assetName, spec.Name, minIntervalSec)
	return err
}
