package schedule

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/partition"
	entpkg "github.com/kanpon/data-governance/internal/storage/ent"
)

// fakeEventWriter captures Append calls into an in-memory slice for assertions.
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
	out := []event.Event{}
	for _, e := range f.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

// sqlOnlyStorage is a minimal storage.Storage that exposes only DB(). The
// schedule package only needs DB() — Ent() / WithTx() are not used by
// FireOneSchedule or UpsertSchedules.
type sqlOnlyStorage struct {
	db *sql.DB
}

func (s *sqlOnlyStorage) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *sqlOnlyStorage) Ent() *entpkg.Client            { return nil }
func (s *sqlOnlyStorage) DB() *sql.DB                    { return s.db }
func (s *sqlOnlyStorage) Close() error                   { return s.db.Close() }
func (s *sqlOnlyStorage) WithTx(_ context.Context, _ func(*entpkg.Tx) error) error {
	return fmt.Errorf("not implemented in test stub")
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("requires DATABASE_URL")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx))
	return db
}

// cleanupSchedule deletes a schedule row + any runs for the given asset.
func cleanupSchedule(t *testing.T, db *sql.DB, assetName string) {
	t.Helper()
	_, _ = db.ExecContext(context.Background(),
		`DELETE FROM runs WHERE asset_name = $1`, assetName)
	_, _ = db.ExecContext(context.Background(),
		`DELETE FROM schedules WHERE asset_name = $1`, assetName)
}

// insertSchedule inserts a schedules row and returns its id.
func insertSchedule(t *testing.T, db *sql.DB, assetName, cronExpr string, lastFireAt sql.NullTime, nextFireAt time.Time) string {
	t.Helper()
	var id string
	const sqlText = `
		INSERT INTO schedules (id, asset_name, cron_expr, last_fire_at, next_fire_at, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, NOW(), NOW())
		RETURNING id
	`
	err := db.QueryRowContext(context.Background(), sqlText, assetName, cronExpr, lastFireAt, nextFireAt).Scan(&id)
	require.NoError(t, err)
	return id
}

// nopMaterialize is a stub MaterializeFunc — fire-time tests do not actually
// execute Materialize, but Build() requires the function be non-nil.
func nopMaterialize(_ context.Context, _ asset.AssetIO) (asset.MaterializeResult, error) {
	return asset.MaterializeResult{RowsWritten: 0}, nil
}

// buildAssetReg constructs a one-asset DefinitionRegistry. Used by tests that
// need reg.Get(assetName) to resolve partition strategy.
func buildAssetReg(t *testing.T, b *asset.Builder) *asset.DefinitionRegistry {
	t.Helper()
	a, err := b.Build()
	require.NoError(t, err)
	r := asset.NewDefinitionRegistry()
	require.NoError(t, r.Register(a))
	return r
}

// ----- Tests -----

// TestSchedulerFiresDueRow proves the end-to-end fire path: a due schedule
// row, scanned by FireOneSchedule, atomically yields a queued runs row and an
// updated schedule (last_fire_at != NULL, next_fire_at > NOW()).
func TestSchedulerFiresDueRow(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sched_fires_due_row"
	cleanupSchedule(t, db, assetName)
	t.Cleanup(func() { cleanupSchedule(t, db, assetName) })

	// Schedule was due 1 minute ago.
	dueAt := time.Now().UTC().Add(-1 * time.Minute)
	schedID := insertSchedule(t, db, assetName, "@every 30s", sql.NullTime{}, dueAt)

	reg := buildAssetReg(t, asset.New(assetName).
		Connector("dummy").
		Materialize(nopMaterialize))
	store := &sqlOnlyStorage{db: db}
	ev := &fakeEventWriter{}

	now := time.Now().UTC()
	require.NoError(t, FireOneSchedule(context.Background(), store, reg, ev, now))

	// Check the runs row.
	var (
		state   string
		trigger string
		prio    string
		pk      sql.NullString
	)
	err := db.QueryRowContext(context.Background(),
		`SELECT state, trigger, priority, partition_key FROM runs WHERE asset_name = $1`, assetName,
	).Scan(&state, &trigger, &prio, &pk)
	require.NoError(t, err)
	assert.Equal(t, "queued", state)
	assert.Equal(t, "schedule", trigger)
	assert.Equal(t, "normal", prio)
	assert.False(t, pk.Valid, "partition_key must be NULL for non-partitioned asset")

	// Check the schedule row was updated.
	var (
		lastFire sql.NullTime
		nextFire sql.NullTime
	)
	err = db.QueryRowContext(context.Background(),
		`SELECT last_fire_at, next_fire_at FROM schedules WHERE id = $1`, schedID,
	).Scan(&lastFire, &nextFire)
	require.NoError(t, err)
	assert.True(t, lastFire.Valid, "last_fire_at must be set after fire")
	require.True(t, nextFire.Valid)
	assert.True(t, nextFire.Time.After(now), "next_fire_at must be > now after fire")

	// Check the schedule.fired event.
	fired := ev.byType(event.EventTypeScheduleFired)
	require.Len(t, fired, 1)
	assert.Equal(t, schedID, fired[0].ResourceID)

	// No missed event (lastFireAt was NULL — first-registration suppression).
	assert.Empty(t, ev.byType(event.EventTypeScheduleMissed))
}

// TestSchedulerFireWithDailyPartition proves that a fire for an asset with
// .Partitions(daily) inserts a runs row with partition_key = CurrentDailyKey(now, 24h).
func TestSchedulerFireWithDailyPartition(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sched_fire_daily_partition"
	cleanupSchedule(t, db, assetName)
	t.Cleanup(func() { cleanupSchedule(t, db, assetName) })

	dueAt := time.Now().UTC().Add(-1 * time.Minute)
	insertSchedule(t, db, assetName, "@every 1h", sql.NullTime{}, dueAt)

	reg := buildAssetReg(t, asset.New(assetName).
		Connector("dummy").
		Materialize(nopMaterialize).
		Partitions(partition.DailyPartitions{}))
	store := &sqlOnlyStorage{db: db}
	ev := &fakeEventWriter{}

	now := time.Now().UTC()
	require.NoError(t, FireOneSchedule(context.Background(), store, reg, ev, now))

	var pk sql.NullString
	err := db.QueryRowContext(context.Background(),
		`SELECT partition_key FROM runs WHERE asset_name = $1`, assetName,
	).Scan(&pk)
	require.NoError(t, err)
	require.True(t, pk.Valid, "partition_key must be set for partitioned asset")
	expected := partition.CurrentDailyKey(now, 24*time.Hour)
	assert.Equal(t, expected, pk.String)
}

// TestSchedulerFireMissedWindow proves the LatestOnly recovery path: when a
// schedule has missed multiple windows, FireOneSchedule fires only the most
// recent and emits schedule.missed with skipped_count.
func TestSchedulerFireMissedWindow(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sched_fire_missed_window"
	cleanupSchedule(t, db, assetName)
	t.Cleanup(func() { cleanupSchedule(t, db, assetName) })

	// Hourly schedule. last_fire_at = 4 hours ago — three windows skipped.
	now := time.Now().UTC()
	lastFire := now.Add(-4 * time.Hour).Truncate(time.Hour)
	dueAt := now.Add(-1 * time.Minute)
	insertSchedule(t, db, assetName, "0 * * * *",
		sql.NullTime{Time: lastFire, Valid: true}, dueAt)

	reg := buildAssetReg(t, asset.New(assetName).
		Connector("dummy").
		Materialize(nopMaterialize))
	store := &sqlOnlyStorage{db: db}
	ev := &fakeEventWriter{}

	require.NoError(t, FireOneSchedule(context.Background(), store, reg, ev, now))

	// Exactly one runs row inserted (LatestOnly).
	var n int
	err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM runs WHERE asset_name = $1`, assetName,
	).Scan(&n)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "LatestOnly: only the most recent missed window must produce a run")

	// schedule.fired emitted.
	assert.Len(t, ev.byType(event.EventTypeScheduleFired), 1)

	// schedule.missed emitted with skipped_count > 0.
	missed := ev.byType(event.EventTypeScheduleMissed)
	require.Len(t, missed, 1)
	pl, ok := missed[0].Payload.(map[string]any)
	require.True(t, ok, "payload must be map[string]any")
	skipped, ok := pl["skipped_count"].(int)
	require.True(t, ok, "skipped_count must be int")
	assert.GreaterOrEqual(t, skipped, 2, "at least two windows skipped (4-hour gap, hourly cron)")
}

// TestSchedulerNoDueRows proves FireOneSchedule returns ErrNoDueSchedule when
// no schedules row is due.
func TestSchedulerNoDueRows(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sched_no_due_rows"
	cleanupSchedule(t, db, assetName)
	t.Cleanup(func() { cleanupSchedule(t, db, assetName) })

	// Insert a schedule that is NOT yet due (next_fire_at in the future).
	insertSchedule(t, db, assetName, "@every 30s", sql.NullTime{}, time.Now().UTC().Add(1*time.Hour))

	reg := buildAssetReg(t, asset.New(assetName).
		Connector("dummy").
		Materialize(nopMaterialize))
	store := &sqlOnlyStorage{db: db}
	ev := &fakeEventWriter{}

	err := FireOneSchedule(context.Background(), store, reg, ev, time.Now().UTC())
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoDueSchedule), "expected ErrNoDueSchedule; got %v", err)
}

// TestSchedulerSkipLocked proves the SKIP LOCKED multi-replica safety: with a
// single due schedule row, two parallel FireOneSchedule calls produce exactly
// one fire and one ErrNoDueSchedule.
func TestSchedulerSkipLocked(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_sched_skip_locked"
	cleanupSchedule(t, db, assetName)
	t.Cleanup(func() { cleanupSchedule(t, db, assetName) })

	dueAt := time.Now().UTC().Add(-1 * time.Minute)
	insertSchedule(t, db, assetName, "@every 30s", sql.NullTime{}, dueAt)

	reg := buildAssetReg(t, asset.New(assetName).
		Connector("dummy").
		Materialize(nopMaterialize))
	store := &sqlOnlyStorage{db: db}

	var (
		fires    atomic.Int32
		noDues   atomic.Int32
		wg       sync.WaitGroup
	)
	const workers = 8
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			ev := &fakeEventWriter{}
			err := FireOneSchedule(context.Background(), store, reg, ev, time.Now().UTC())
			switch {
			case err == nil:
				fires.Add(1)
			case errors.Is(err, ErrNoDueSchedule):
				noDues.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(1), fires.Load(), "exactly one goroutine must win the fire")
	assert.Equal(t, int32(workers-1), noDues.Load(), "the rest must observe ErrNoDueSchedule")
}

// ----- compile-time guard: AssetIO field used to silence unused-import in some build modes -----
var _ connector.Row // ensure connector import is consumed even when nopMaterialize is unused at runtime
