package schedule

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
)

// TestDaemonRunCancellation ensures the unexported run() loop returns
// promptly when its context is canceled. The test exercises the loop in
// isolation (same-package access) — production code uses FireOneSchedule
// directly per W3 resolution.
func TestDaemonRunCancellation(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	store := &sqlOnlyStorage{db: db}
	ev := &fakeEventWriter{}
	reg := asset.NewDefinitionRegistry()

	d := &Daemon{
		Store:    store,
		Registry: reg,
		Events:   ev,
		Interval: 10 * time.Millisecond,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled),
			"run must return context.Canceled when ctx is canceled, got: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("run did not return within 2 seconds of cancellation")
	}
}

// TestDaemonUpsertOnStart proves the daemon calls UpsertSchedules on start —
// after a brief run, the schedules table contains a row for the registered
// asset.
func TestDaemonUpsertOnStart(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_daemon_upsert_on_start"
	cleanupSchedule(t, db, assetName)
	t.Cleanup(func() { cleanupSchedule(t, db, assetName) })

	reg := buildAssetReg(t, asset.New(assetName).
		Connector("dummy").
		Materialize(nopMaterialize).
		Schedule("@every 1m"))

	store := &sqlOnlyStorage{db: db}
	ev := &fakeEventWriter{}
	d := &Daemon{
		Store:    store,
		Registry: reg,
		Events:   ev,
		Interval: 100 * time.Millisecond, // long enough not to trigger second tick during the test
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- d.run(ctx) }()

	// Allow run() to finish UpsertSchedules + the immediate first tick.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	var cron string
	err := db.QueryRowContext(context.Background(),
		`SELECT cron_expr FROM schedules WHERE asset_name = $1`, assetName,
	).Scan(&cron)
	require.NoError(t, err, "schedules row must exist after daemon start")
	assert.Equal(t, "@every 1m", cron)

	// last_fire_at should be set because the first tick fires the immediately-due schedule.
	var lf sql.NullTime
	err = db.QueryRowContext(context.Background(),
		`SELECT last_fire_at FROM schedules WHERE asset_name = $1`, assetName,
	).Scan(&lf)
	require.NoError(t, err)
	assert.True(t, lf.Valid, "first tick should have fired the schedule")
}
