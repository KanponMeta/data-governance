package sensor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
)

// TestDaemonRunOnceDrains: 3 due sensor rows; RunOnce drains them all in one call.
func TestDaemonRunOnceDrains(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_daemon_drains"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)

	// Build asset with 3 sensors, each Fired=false (so they don't enqueue).
	specs := []asset.SensorSpec{
		{Name: "S1", MinInterval: 1 * time.Second, Sense: func(_ context.Context) (asset.SensorResult, error) { return asset.SensorResult{Fired: false}, nil }},
		{Name: "S2", MinInterval: 1 * time.Second, Sense: func(_ context.Context) (asset.SensorResult, error) { return asset.SensorResult{Fired: false}, nil }},
		{Name: "S3", MinInterval: 1 * time.Second, Sense: func(_ context.Context) (asset.SensorResult, error) { return asset.SensorResult{Fired: false}, nil }},
	}
	a := buildAsset(t, assetName, nil, specs...)
	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))

	// Insert 3 sensor rows that match.
	for _, s := range specs {
		insertSensorRow(t, db, assetName, s.Name, nil)
	}

	store := &sqlStorage{db: db}
	events := &fakeEventWriter{}
	d := &Daemon{Store: store, Registry: reg, Events: events}

	require.NoError(t, d.RunOnce(context.Background()))

	// All 3 sensors should now have last_evaluated_at set.
	rows, err := db.Query(`SELECT sensor_name, last_evaluated_at FROM sensors WHERE asset_name=$1`, assetName)
	require.NoError(t, err)
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name string
		var evaluatedAt *time.Time
		require.NoError(t, rows.Scan(&name, &evaluatedAt))
		assert.NotNil(t, evaluatedAt, "sensor %q must have last_evaluated_at set", name)
		count++
	}
	assert.Equal(t, 3, count, "expected 3 sensor rows updated")
}

// TestRunOnceContextCancellation: a canceled ctx must short-circuit RunOnce.
func TestRunOnceContextCancellation(t *testing.T) {
	store := &sqlStorage{db: nil} // never used because ctx is already cancelled
	reg := asset.NewDefinitionRegistry()
	events := &fakeEventWriter{}
	d := &Daemon{Store: store, Registry: reg, Events: events}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := d.RunOnce(ctx)
	elapsed := time.Since(start)

	assert.True(t, errors.Is(err, context.Canceled), "expected context.Canceled, got %v", err)
	assert.Less(t, elapsed, 100*time.Millisecond, "must return quickly when ctx cancelled")
}

// TestUpsertSensors: registering a SensorSpec → one sensors row inserted; calling again is idempotent.
func TestUpsertSensors(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_upsert_sensors"
	const sensorName = "alpha"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)

	a := buildAsset(t, assetName, nil, asset.SensorSpec{
		Name:        sensorName,
		MinInterval: 30 * time.Second,
		Sense: func(_ context.Context) (asset.SensorResult, error) {
			return asset.SensorResult{}, nil
		},
	})
	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))

	store := &sqlStorage{db: db}

	require.NoError(t, UpsertSensors(context.Background(), store, reg))

	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sensors WHERE asset_name=$1 AND sensor_name=$2`, assetName, sensorName).Scan(&count))
	assert.Equal(t, 1, count, "first upsert must insert one row")

	// Second call should be a no-op.
	require.NoError(t, UpsertSensors(context.Background(), store, reg))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sensors WHERE asset_name=$1 AND sensor_name=$2`, assetName, sensorName).Scan(&count))
	assert.Equal(t, 1, count, "second upsert must be idempotent (still 1 row)")

	// MinInterval should be 30 (seconds).
	var minIvl int64
	require.NoError(t, db.QueryRow(`SELECT min_interval_seconds FROM sensors WHERE asset_name=$1 AND sensor_name=$2`, assetName, sensorName).Scan(&minIvl))
	assert.Equal(t, int64(30), minIvl)
}

// TestUpsertSensorsMinIntervalUpdate: changing the registry's MinInterval propagates.
func TestUpsertSensorsMinIntervalUpdate(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_upsert_min_interval"
	const sensorName = "beta"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)

	build := func(reg *asset.DefinitionRegistry, minIvl time.Duration) {
		a := buildAsset(t, assetName, nil, asset.SensorSpec{
			Name:        sensorName,
			MinInterval: minIvl,
			Sense: func(_ context.Context) (asset.SensorResult, error) {
				return asset.SensorResult{}, nil
			},
		})
		require.NoError(t, reg.Register(a))
	}

	store := &sqlStorage{db: db}

	reg1 := asset.NewDefinitionRegistry()
	build(reg1, 30*time.Second)
	require.NoError(t, UpsertSensors(context.Background(), store, reg1))

	var minIvl int64
	require.NoError(t, db.QueryRow(`SELECT min_interval_seconds FROM sensors WHERE asset_name=$1 AND sensor_name=$2`, assetName, sensorName).Scan(&minIvl))
	assert.Equal(t, int64(30), minIvl)

	// New registry with same asset/sensor name but different MinInterval.
	asset.ResetForTest()
	reg2 := asset.NewDefinitionRegistry()
	build(reg2, 60*time.Second)
	require.NoError(t, UpsertSensors(context.Background(), store, reg2))
	require.NoError(t, db.QueryRow(`SELECT min_interval_seconds FROM sensors WHERE asset_name=$1 AND sensor_name=$2`, assetName, sensorName).Scan(&minIvl))
	assert.Equal(t, int64(60), minIvl, "min_interval_seconds must be updated to new value")

	// Still only one row.
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sensors WHERE asset_name=$1 AND sensor_name=$2`, assetName, sensorName).Scan(&count))
	assert.Equal(t, 1, count)
}
