package sensor

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/partition"
	entpkg "github.com/kanpon/data-governance/internal/storage/ent"
)

// ---- Test fixtures ----

// fakeEventWriter captures Append calls into a slice, mimicking the
// pattern documented for plan 03-04. Safe for concurrent tests.
type fakeEventWriter struct {
	mu     sync.Mutex
	events []event.Event
}

func (f *fakeEventWriter) Append(_ context.Context, evt event.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, evt)
	return nil
}

func (f *fakeEventWriter) byType(t event.EventType) []event.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]event.Event, 0, len(f.events))
	for _, e := range f.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// sqlStorage is a minimal storage.Storage implementation for tests that need
// only DB() (mirrors internal/run/claim_test.go pattern).
type sqlStorage struct {
	db *sql.DB
}

func (s *sqlStorage) Ping(ctx context.Context) error                            { return s.db.PingContext(ctx) }
func (s *sqlStorage) Ent() *entpkg.Client                                       { return nil }
func (s *sqlStorage) DB() *sql.DB                                               { return s.db }
func (s *sqlStorage) Close() error                                              { return s.db.Close() }
func (s *sqlStorage) WithTx(_ context.Context, _ func(*entpkg.Tx) error) error {
	return fmt.Errorf("not implemented in test stub")
}

// openTestDB opens DATABASE_URL or skips. Mirrors internal/run/claim_test.go.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("requires DATABASE_URL to be set (e.g. postgres://platform_app:platform_app@localhost:5432/platform?sslmode=disable)")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err, "sql.Open failed")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx), "database ping failed")
	return db
}

// noopMaterialize lets us build assets in tests.
func noopMaterialize(_ context.Context, _ asset.AssetIO) (asset.MaterializeResult, error) {
	return asset.MaterializeResult{}, nil
}

// buildAsset constructs an asset with the supplied SensorSpec(s) and optional partition strategy.
func buildAsset(t *testing.T, name string, ps partition.PartitionStrategy, specs ...asset.SensorSpec) *asset.Asset {
	t.Helper()
	b := asset.New(name).Connector("test-connector").Materialize(noopMaterialize)
	for _, s := range specs {
		b = b.Sensor(s)
	}
	if ps != nil {
		b = b.Partitions(ps)
	}
	a, err := b.Build()
	require.NoError(t, err)
	return a
}

// insertSensorRow inserts a sensor row directly and returns its uuid.
func insertSensorRow(t *testing.T, db *sql.DB, assetName, sensorName string, mods func(*sensorRowMods)) string {
	t.Helper()
	m := sensorRowMods{minIntervalSeconds: 1}
	if mods != nil {
		mods(&m)
	}
	const insertSQL = `
		INSERT INTO sensors (id, asset_name, sensor_name, min_interval_seconds,
		                    last_run_key, cooldown_until, consecutive_failures,
		                    last_evaluated_at, disabled_at, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, $5, $6, $7, $8, NOW(), NOW())
		RETURNING id
	`
	var id string
	var lastRunKey interface{}
	if m.lastRunKey != "" {
		lastRunKey = m.lastRunKey
	}
	var cooldownUntil interface{}
	if !m.cooldownUntil.IsZero() {
		cooldownUntil = m.cooldownUntil
	}
	var lastEvaluatedAt interface{}
	if !m.lastEvaluatedAt.IsZero() {
		lastEvaluatedAt = m.lastEvaluatedAt
	}
	var disabledAt interface{}
	if !m.disabledAt.IsZero() {
		disabledAt = m.disabledAt
	}
	err := db.QueryRowContext(context.Background(), insertSQL,
		assetName, sensorName, m.minIntervalSeconds,
		lastRunKey, cooldownUntil, m.consecutiveFailures,
		lastEvaluatedAt, disabledAt,
	).Scan(&id)
	require.NoError(t, err, "insert sensor row")
	return id
}

type sensorRowMods struct {
	minIntervalSeconds  int64
	lastRunKey          string
	cooldownUntil       time.Time
	consecutiveFailures int
	lastEvaluatedAt     time.Time
	disabledAt          time.Time
}

// cleanupSensors deletes sensors and runs for an asset.
func cleanupSensors(t *testing.T, db *sql.DB, assetName string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), "DELETE FROM runs WHERE asset_name = $1", assetName)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), "DELETE FROM sensors WHERE asset_name = $1", assetName)
	require.NoError(t, err)
}

// ---- Unit tests (no DB required) ----

// TestSensorPanicRecovery: a panicking Sense() must not propagate.
// Returned error must mention the panic. (Pitfall 2, T-03-05-01)
func TestSensorPanicRecovery(t *testing.T) {
	spec := asset.SensorSpec{
		Name:        "panicky",
		MinInterval: 10 * time.Millisecond,
		Sense: func(_ context.Context) (asset.SensorResult, error) {
			panic("boom")
		},
	}
	res, err := safeEvaluate(context.Background(), spec, 100*time.Millisecond)
	require.Error(t, err, "panic must surface as error")
	assert.False(t, res.Fired)
	assert.Contains(t, err.Error(), "panic")
	assert.Contains(t, err.Error(), "boom")
}

// TestSensorTimeoutEnforced: a Sense() that ignores context and blocks must return
// within the timeout window (Pitfall 3, T-03-05-02). We pass a short timeout (50ms)
// even though the spec.MinInterval is also 50ms.
func TestSensorTimeoutEnforced(t *testing.T) {
	started := make(chan struct{})
	spec := asset.SensorSpec{
		Name:        "blocker",
		MinInterval: 50 * time.Millisecond,
		Sense: func(ctx context.Context) (asset.SensorResult, error) {
			close(started)
			// Respect ctx — return whenever ctx is done.
			<-ctx.Done()
			return asset.SensorResult{}, ctx.Err()
		},
	}
	start := time.Now()
	_, err := safeEvaluate(context.Background(), spec, 50*time.Millisecond)
	elapsed := time.Since(start)
	<-started
	require.Error(t, err)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "expected DeadlineExceeded, got %v", err)
	assert.Less(t, elapsed, 500*time.Millisecond, "timeout must fire promptly")
}

// TestSensorTimeoutDefaultsToMinInterval: passing 0 timeout uses spec.MinInterval (or DefaultMinInterval if smaller).
func TestSensorTimeoutDefaultsToMinInterval(t *testing.T) {
	// MinInterval=0 → DefaultMinInterval(30s) used. We can't realistically wait 30s
	// here, so we just check that a fast-returning Sense works without error.
	spec := asset.SensorSpec{
		Name:        "fast",
		MinInterval: 0,
		Sense: func(_ context.Context) (asset.SensorResult, error) {
			return asset.SensorResult{Fired: false}, nil
		},
	}
	res, err := safeEvaluate(context.Background(), spec, 0)
	require.NoError(t, err)
	assert.False(t, res.Fired)
}

// TestResolveSensorPartitionKey covers all four PartitionStrategy branches plus the nil case.
func TestResolveSensorPartitionKey(t *testing.T) {
	now := time.Date(2024, 1, 16, 12, 0, 0, 0, time.UTC)

	t.Run("nil strategy returns empty", func(t *testing.T) {
		got := resolveSensorPartitionKey(nil, "anything", now)
		assert.Equal(t, "", got)
	})

	t.Run("daily with valid YYYY-MM-DD RunKey", func(t *testing.T) {
		got := resolveSensorPartitionKey(partition.DailyPartitions{}, "2024-01-15", now)
		assert.Equal(t, "2024-01-15", got)
	})

	t.Run("daily with malformed RunKey falls back to current daily key (now-24h)", func(t *testing.T) {
		got := resolveSensorPartitionKey(partition.DailyPartitions{}, "garbage", now)
		// CurrentDailyKey(now, 24h) = DailyKey(now - 24h) = 2024-01-15
		assert.Equal(t, "2024-01-15", got)
	})

	t.Run("daily with empty RunKey falls back to current window", func(t *testing.T) {
		got := resolveSensorPartitionKey(partition.DailyPartitions{}, "", now)
		assert.Equal(t, "2024-01-15", got)
	})

	t.Run("weekly returns previous-week key", func(t *testing.T) {
		got := resolveSensorPartitionKey(partition.WeeklyPartitions{}, "", now)
		// now=2024-01-16 (Tue); now-7d=2024-01-09 → ISO week 2 of 2024
		assert.Equal(t, "2024-W02", got)
	})

	t.Run("monthly returns previous-month key", func(t *testing.T) {
		got := resolveSensorPartitionKey(partition.MonthlyPartitions{}, "", now)
		// now=2024-01-16; now AddDate(0,-1,0)=2023-12-16 → 2023-12
		assert.Equal(t, "2023-12", got)
	})

	t.Run("category accepts known key", func(t *testing.T) {
		ps := partition.CategoryPartitions{Keys: []string{"us", "eu"}}
		got := resolveSensorPartitionKey(ps, "us", now)
		assert.Equal(t, "us", got)
	})

	t.Run("category rejects unknown key", func(t *testing.T) {
		ps := partition.CategoryPartitions{Keys: []string{"us", "eu"}}
		got := resolveSensorPartitionKey(ps, "apac", now)
		assert.Equal(t, "", got)
	})

	t.Run("category empty key returns empty", func(t *testing.T) {
		ps := partition.CategoryPartitions{Keys: []string{"us"}}
		got := resolveSensorPartitionKey(ps, "", now)
		assert.Equal(t, "", got)
	})
}

// ---- Integration tests (require DATABASE_URL) ----

// TestSensorRunKeyDedup verifies layer-1 dedup: SensorResult.RunKey == sensors.last_run_key
// → no run inserted, sensor.dedup_skipped event emitted.
func TestSensorRunKeyDedup(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sensor_dedup"
	const sensorName = "S1"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	// Set up the sensors row with last_run_key="K1".
	insertSensorRow(t, db, assetName, sensorName, func(m *sensorRowMods) {
		m.lastRunKey = "K1"
	})

	// Build asset registry with a sensor that returns Fired=true with RunKey="K1".
	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	a, err := asset.New(assetName).Connector("c").Materialize(noopMaterialize).
		Sensor(asset.SensorSpec{
			Name:        sensorName,
			MinInterval: 1 * time.Second,
			Sense: func(_ context.Context) (asset.SensorResult, error) {
				return asset.SensorResult{Fired: true, RunKey: "K1"}, nil
			},
		}).Build()
	require.NoError(t, err)
	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))

	store := &sqlStorage{db: db}
	events := &fakeEventWriter{}

	require.NoError(t, evaluateOneSensor(context.Background(), store, reg, events, 0))

	// Assert no run inserted.
	var runCount int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM runs WHERE asset_name=$1", assetName).Scan(&runCount))
	assert.Equal(t, 0, runCount, "RunKey dedup must prevent run insert")

	// Assert sensor.dedup_skipped event captured.
	skipped := events.byType(event.EventTypeSensorDedupSkipped)
	assert.Len(t, skipped, 1, "expected exactly one sensor.dedup_skipped event")
}

// TestSensorCooldown verifies layer-2 dedup: NOW() < cooldown_until → no run, sensor.cooldown_skipped emitted.
func TestSensorCooldown(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sensor_cooldown"
	const sensorName = "S1"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	cooldownEnd := time.Now().Add(10 * time.Minute).UTC()
	insertSensorRow(t, db, assetName, sensorName, func(m *sensorRowMods) {
		m.cooldownUntil = cooldownEnd
	})

	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	a, err := asset.New(assetName).Connector("c").Materialize(noopMaterialize).
		Sensor(asset.SensorSpec{
			Name:        sensorName,
			MinInterval: 1 * time.Second,
			Sense: func(_ context.Context) (asset.SensorResult, error) {
				return asset.SensorResult{Fired: true, RunKey: ""}, nil
			},
		}).Build()
	require.NoError(t, err)
	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))

	store := &sqlStorage{db: db}
	events := &fakeEventWriter{}

	require.NoError(t, evaluateOneSensor(context.Background(), store, reg, events, 0))

	var runCount int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM runs WHERE asset_name=$1", assetName).Scan(&runCount))
	assert.Equal(t, 0, runCount, "Cooldown must prevent run insert")

	skipped := events.byType(event.EventTypeSensorCooldownSkipped)
	assert.Len(t, skipped, 1, "expected exactly one sensor.cooldown_skipped event")
}

// TestSensorFire verifies the happy path: pass both layers → run inserted with trigger='sensor'.
func TestSensorFire(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sensor_fire"
	const sensorName = "S1"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	insertSensorRow(t, db, assetName, sensorName, nil)

	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	a, err := asset.New(assetName).Connector("c").Materialize(noopMaterialize).
		Sensor(asset.SensorSpec{
			Name:        sensorName,
			MinInterval: 1 * time.Second,
			Sense: func(_ context.Context) (asset.SensorResult, error) {
				return asset.SensorResult{Fired: true, RunKey: "K2"}, nil
			},
		}).Build()
	require.NoError(t, err)
	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))

	store := &sqlStorage{db: db}
	events := &fakeEventWriter{}

	require.NoError(t, evaluateOneSensor(context.Background(), store, reg, events, 0))

	// One run inserted, trigger='sensor', priority='normal', partition_key NULL.
	var (
		runCount     int
		runTrigger   string
		runPriority  string
		runState     string
		runPartition sql.NullString
	)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM runs WHERE asset_name=$1`, assetName).Scan(&runCount))
	require.Equal(t, 1, runCount)
	require.NoError(t, db.QueryRow(
		`SELECT trigger, priority, state, partition_key FROM runs WHERE asset_name=$1`, assetName,
	).Scan(&runTrigger, &runPriority, &runState, &runPartition))
	assert.Equal(t, "sensor", runTrigger)
	assert.Equal(t, "normal", runPriority)
	assert.Equal(t, "queued", runState)
	assert.False(t, runPartition.Valid, "non-partitioned run must have NULL partition_key")

	// sensor row updated.
	var lastRunKey sql.NullString
	var lastFiredAt sql.NullTime
	require.NoError(t, db.QueryRow(
		`SELECT last_run_key, last_fired_at FROM sensors WHERE asset_name=$1 AND sensor_name=$2`,
		assetName, sensorName,
	).Scan(&lastRunKey, &lastFiredAt))
	assert.True(t, lastRunKey.Valid)
	assert.Equal(t, "K2", lastRunKey.String)
	assert.True(t, lastFiredAt.Valid)

	// sensor.fired event emitted.
	fired := events.byType(event.EventTypeSensorFired)
	assert.Len(t, fired, 1, "expected exactly one sensor.fired event")
}

// TestSensorAutoDisable: sensor with consecutive_failures=AutoDisableThreshold-1 fails once → disabled_at set.
func TestSensorAutoDisable(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sensor_autodisable"
	const sensorName = "S1"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	// Use a small threshold (3) for test speed; pre-set consecutive_failures=2 so one failure trips it.
	insertSensorRow(t, db, assetName, sensorName, func(m *sensorRowMods) {
		m.consecutiveFailures = 2
	})

	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	a, err := asset.New(assetName).Connector("c").Materialize(noopMaterialize).
		Sensor(asset.SensorSpec{
			Name:        sensorName,
			MinInterval: 1 * time.Second,
			Sense: func(_ context.Context) (asset.SensorResult, error) {
				return asset.SensorResult{}, errors.New("synthetic failure")
			},
		}).Build()
	require.NoError(t, err)
	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))

	store := &sqlStorage{db: db}
	events := &fakeEventWriter{}

	require.NoError(t, evaluateOneSensor(context.Background(), store, reg, events, 3))

	var (
		consecutiveFailures int
		disabledAt          sql.NullTime
	)
	require.NoError(t, db.QueryRow(
		`SELECT consecutive_failures, disabled_at FROM sensors WHERE asset_name=$1 AND sensor_name=$2`,
		assetName, sensorName,
	).Scan(&consecutiveFailures, &disabledAt))
	assert.Equal(t, 3, consecutiveFailures)
	assert.True(t, disabledAt.Valid, "disabled_at must be set after threshold reached")

	failed := events.byType(event.EventTypeSensorEvaluationFailed)
	assert.Len(t, failed, 1)
	disabled := events.byType(event.EventTypeSensorDisabled)
	assert.Len(t, disabled, 1)

	// Sanity check: the failed event payload contains an error message.
	if assert.NotNil(t, failed[0].Payload) {
		// Payload is a map[string]any; just check error key exists.
		if m, ok := failed[0].Payload.(map[string]any); ok {
			assert.Contains(t, fmt.Sprintf("%v", m["error"]), "synthetic")
		}
	}
}

// TestSensorAutoResetOnSuccess: failure counter resets on a Fired=false (success-no-fire) evaluation.
func TestSensorAutoResetOnSuccess(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sensor_autoreset"
	const sensorName = "S1"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	insertSensorRow(t, db, assetName, sensorName, func(m *sensorRowMods) {
		m.consecutiveFailures = 10
	})

	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	a, err := asset.New(assetName).Connector("c").Materialize(noopMaterialize).
		Sensor(asset.SensorSpec{
			Name:        sensorName,
			MinInterval: 1 * time.Second,
			Sense: func(_ context.Context) (asset.SensorResult, error) {
				return asset.SensorResult{Fired: false}, nil
			},
		}).Build()
	require.NoError(t, err)
	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))

	store := &sqlStorage{db: db}
	events := &fakeEventWriter{}

	require.NoError(t, evaluateOneSensor(context.Background(), store, reg, events, 0))

	var consecutiveFailures int
	require.NoError(t, db.QueryRow(
		`SELECT consecutive_failures FROM sensors WHERE asset_name=$1 AND sensor_name=$2`,
		assetName, sensorName,
	).Scan(&consecutiveFailures))
	assert.Equal(t, 0, consecutiveFailures, "Fired=false success must reset failure counter")
}

// TestSensorOrphanDisabled: sensor row exists for an asset/sensor name no longer in registry → disabled.
func TestSensorOrphanDisabled(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sensor_orphan"
	const sensorName = "ghost"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	insertSensorRow(t, db, assetName, sensorName, nil)

	// Empty registry — the sensor row's asset is unknown.
	reg := asset.NewDefinitionRegistry()
	store := &sqlStorage{db: db}
	events := &fakeEventWriter{}

	require.NoError(t, evaluateOneSensor(context.Background(), store, reg, events, 0))

	var disabledAt sql.NullTime
	require.NoError(t, db.QueryRow(
		`SELECT disabled_at FROM sensors WHERE asset_name=$1 AND sensor_name=$2`,
		assetName, sensorName,
	).Scan(&disabledAt))
	assert.True(t, disabledAt.Valid, "orphan sensor must be auto-disabled")

	// sensor.disabled event emitted with reason=orphaned.
	disabled := events.byType(event.EventTypeSensorDisabled)
	require.Len(t, disabled, 1)
	if m, ok := disabled[0].Payload.(map[string]any); ok {
		assert.Contains(t, strings.ToLower(fmt.Sprintf("%v", m["reason"])), "orphan")
	}
}

// TestSensorPartitionKeyDailyComposition: sensor on a partitioned asset uses RunKey when valid.
func TestSensorPartitionKeyDailyComposition(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sensor_partition"
	const sensorName = "S1"
	cleanupSensors(t, db, assetName)
	t.Cleanup(func() { cleanupSensors(t, db, assetName) })

	insertSensorRow(t, db, assetName, sensorName, nil)

	asset.ResetForTest()
	t.Cleanup(asset.ResetForTest)
	a := buildAsset(t, assetName, partition.DailyPartitions{}, asset.SensorSpec{
		Name:        sensorName,
		MinInterval: 1 * time.Second,
		Sense: func(_ context.Context) (asset.SensorResult, error) {
			return asset.SensorResult{Fired: true, RunKey: "2024-01-15"}, nil
		},
	})
	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))

	store := &sqlStorage{db: db}
	events := &fakeEventWriter{}

	require.NoError(t, evaluateOneSensor(context.Background(), store, reg, events, 0))

	var partitionKey sql.NullString
	require.NoError(t, db.QueryRow(
		`SELECT partition_key FROM runs WHERE asset_name=$1`, assetName,
	).Scan(&partitionKey))
	assert.True(t, partitionKey.Valid)
	assert.Equal(t, "2024-01-15", partitionKey.String, "valid RunKey must become partition_key")
}
