package backfill_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	entgosql "entgo.io/ent/dialect/sql"
	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/backfill"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/partition"
	"github.com/kanpon/data-governance/internal/storage"
	stent "github.com/kanpon/data-governance/internal/storage/ent"
)

// entStorage wraps both *sql.DB and *stent.Client for the event writer.
type entStorage struct {
	db  *sql.DB
	ent *stent.Client
}

var _ storage.Storage = (*entStorage)(nil)

func (s *entStorage) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *entStorage) DB() *sql.DB                    { return s.db }
func (s *entStorage) Ent() *stent.Client             { return s.ent }
func (s *entStorage) Close() error                   { return s.db.Close() }
func (s *entStorage) WithTx(ctx context.Context, fn func(tx *stent.Tx) error) error {
	return errors.New("WithTx not used by backfill tests")
}

// openTestDB returns a *sql.DB and *stent.Client connected to DATABASE_URL,
// or skips the test if DATABASE_URL is unset.
func openTestDB(t *testing.T) (*sql.DB, *stent.Client) {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed backfill tests")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx))

	entClient := stent.NewClient(stent.Driver(entgosql.OpenDB(dialect.Postgres, db)))

	t.Cleanup(func() {
		entClient.Close()
		db.Close()
	})
	return db, entClient
}

// deleteBackfillData removes any leftover rows from prior test runs.
func deleteBackfillData(t *testing.T, db *sql.DB, assetName string) {
	t.Helper()
	ctx := context.Background()
	_, _ = db.ExecContext(ctx, `DELETE FROM runs WHERE asset_name = $1`, assetName)
	_, _ = db.ExecContext(ctx, `DELETE FROM backfills WHERE asset_name = $1`, assetName)
}

// TestBackfillSubmit — happy path: parse a 7-day daily spec, Submit, assert
// 1 backfills row + 7 runs rows with correct columns + backfill.submitted event.
func TestBackfillSubmit(t *testing.T) {
	db, entClient := openTestDB(t)
	store := &entStorage{db: db, ent: entClient}
	const assetName = "test_backfill_submit_daily"
	deleteBackfillData(t, db, assetName)
	t.Cleanup(func() { deleteBackfillData(t, db, assetName) })

	spec, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-01-01:2024-01-07", 3650)
	require.NoError(t, err)
	require.Len(t, spec.Keys, 7)

	events := event.NewWriter(store)
	id, err := backfill.Submit(context.Background(), store, events, assetName, spec)
	require.NoError(t, err)
	require.NotEqual(t, uuid.Nil, id)

	// 1 backfills row.
	var (
		gotAsset string
		gotSpec  string
		total    int
	)
	err = db.QueryRow(`SELECT asset_name, partition_spec, total_partitions FROM backfills WHERE id = $1`, id).
		Scan(&gotAsset, &gotSpec, &total)
	require.NoError(t, err)
	assert.Equal(t, assetName, gotAsset)
	assert.Equal(t, "2024-01-01:2024-01-07", gotSpec)
	assert.Equal(t, 7, total)

	// 7 runs rows with correct shape.
	var runCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM runs WHERE backfill_id = $1 AND priority = 'backfill' AND trigger = 'backfill'`, id).
		Scan(&runCount)
	require.NoError(t, err)
	assert.Equal(t, 7, runCount)

	// Distinct partition keys covering the 7 days.
	rows, err := db.Query(`SELECT partition_key FROM runs WHERE backfill_id = $1 ORDER BY partition_key`, id)
	require.NoError(t, err)
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		keys = append(keys, k)
	}
	assert.Equal(t, []string{"2024-01-01", "2024-01-02", "2024-01-03", "2024-01-04", "2024-01-05", "2024-01-06", "2024-01-07"}, keys)

	// backfill.submitted event recorded.
	var evtCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE event_type = 'backfill.submitted' AND resource_id = $1`, id.String()).
		Scan(&evtCount)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, evtCount, 1)
}

// TestBackfillSubmitInvalidPriority — Submit rejects unknown priority values.
func TestBackfillSubmitInvalidPriority(t *testing.T) {
	db, entClient := openTestDB(t)
	store := &entStorage{db: db, ent: entClient}
	const assetName = "test_backfill_invalid_priority"
	deleteBackfillData(t, db, assetName)
	t.Cleanup(func() { deleteBackfillData(t, db, assetName) })

	events := event.NewWriter(store)
	_, err := backfill.Submit(context.Background(), store, events, assetName,
		backfill.Spec{Keys: []string{"2024-01-01"}, Priority: "bogus", Source: "2024-01-01"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid priority")
}

// TestBackfillSubmitIdempotentResubmit — second Submit with same spec should
// insert 0 new runs (ON CONFLICT DO NOTHING because all 3 partitions are still in-flight).
func TestBackfillSubmitIdempotentResubmit(t *testing.T) {
	db, entClient := openTestDB(t)
	store := &entStorage{db: db, ent: entClient}
	const assetName = "test_backfill_idempotent"
	deleteBackfillData(t, db, assetName)
	t.Cleanup(func() { deleteBackfillData(t, db, assetName) })

	spec, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-02-01:2024-02-03", 3650)
	require.NoError(t, err)

	events := event.NewWriter(store)
	id1, err := backfill.Submit(context.Background(), store, events, assetName, spec)
	require.NoError(t, err)

	// First submission inserted 3 rows.
	var n1 int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM runs WHERE backfill_id = $1`, id1).Scan(&n1))
	assert.Equal(t, 3, n1)

	id2, err := backfill.Submit(context.Background(), store, events, assetName, spec)
	require.NoError(t, err)
	require.NotEqual(t, id1, id2, "second backfill must have its own id")

	// Second backfill row exists but no runs were created (all 3 partitions still in-flight).
	var n2 int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM runs WHERE backfill_id = $1`, id2).Scan(&n2))
	assert.Equal(t, 0, n2, "ON CONFLICT DO NOTHING must skip in-flight partitions on resubmit")

	// Total runs across both backfills is still 3 — the duplicates were silently dropped.
	var totalRuns int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM runs WHERE asset_name = $1`, assetName).Scan(&totalRuns))
	assert.Equal(t, 3, totalRuns)
}

// TestBackfillStatus — after Submit, GetStatus aggregates queued state count = 7.
func TestBackfillStatus(t *testing.T) {
	db, entClient := openTestDB(t)
	store := &entStorage{db: db, ent: entClient}
	const assetName = "test_backfill_status"
	deleteBackfillData(t, db, assetName)
	t.Cleanup(func() { deleteBackfillData(t, db, assetName) })

	spec, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-03-01:2024-03-07", 3650)
	require.NoError(t, err)

	events := event.NewWriter(store)
	id, err := backfill.Submit(context.Background(), store, events, assetName, spec)
	require.NoError(t, err)

	status, err := backfill.GetStatus(context.Background(), db, id)
	require.NoError(t, err)
	assert.Equal(t, id, status.BackfillID)
	assert.Equal(t, assetName, status.AssetName)
	assert.Equal(t, 7, status.TotalPartitions)
	assert.Equal(t, "2024-03-01:2024-03-07", status.PartitionSpec)
	assert.Equal(t, 7, status.StateCounts["queued"])
}

// TestBackfillTimePartition (validation map) — daily partition backfill creates
// 7 runs with distinct partition keys and per-run rows in the runs table that
// downstream Phase 2 executor lifecycle events will key on by run_id.
func TestBackfillTimePartition(t *testing.T) {
	db, entClient := openTestDB(t)
	store := &entStorage{db: db, ent: entClient}
	const assetName = "test_backfill_time_partition_orch07"
	deleteBackfillData(t, db, assetName)
	t.Cleanup(func() { deleteBackfillData(t, db, assetName) })

	spec, err := backfill.ParsePartitionSpec(partition.DailyPartitions{}, "2024-04-01:2024-04-07", 3650)
	require.NoError(t, err)
	require.Len(t, spec.Keys, 7)

	events := event.NewWriter(store)
	id, err := backfill.Submit(context.Background(), store, events, assetName, spec)
	require.NoError(t, err)

	// Distinct run IDs & distinct partition_keys (7 of each — D-10 + D-11).
	type runRow struct {
		ID  uuid.UUID
		Key string
	}
	rows, err := db.Query(`SELECT id, partition_key FROM runs WHERE backfill_id = $1 ORDER BY partition_key`, id)
	require.NoError(t, err)
	defer rows.Close()
	idSet := map[uuid.UUID]bool{}
	keySet := map[string]bool{}
	var collected []runRow
	for rows.Next() {
		var rr runRow
		var keyVal sql.NullString
		require.NoError(t, rows.Scan(&rr.ID, &keyVal))
		require.True(t, keyVal.Valid)
		rr.Key = keyVal.String
		collected = append(collected, rr)
		idSet[rr.ID] = true
		keySet[rr.Key] = true
	}
	require.Len(t, collected, 7)
	assert.Equal(t, 7, len(idSet), "each backfill run must have a distinct UUID")
	assert.Equal(t, 7, len(keySet), "each backfill run must have a distinct partition_key")

	// Spot-check a partition_key is present via direct lookup.
	var ck string
	err = db.QueryRow(`SELECT partition_key FROM runs WHERE backfill_id = $1 AND partition_key = '2024-04-04'`, id).Scan(&ck)
	require.NoError(t, err)
	assert.Equal(t, "2024-04-04", ck)

	// Each run lives independently — flipping one to 'failed' must not cascade
	// to siblings (independence guarantee, also validated more thoroughly in
	// TestCategoryPartitionIndependence).
	var firstID uuid.UUID
	require.NoError(t, db.QueryRow(`SELECT id FROM runs WHERE backfill_id = $1 ORDER BY partition_key LIMIT 1`, id).Scan(&firstID))
	_, err = db.Exec(`UPDATE runs SET state='failed' WHERE id=$1 AND state='queued'`, firstID)
	require.NoError(t, err)

	status, err := backfill.GetStatus(context.Background(), db, id)
	require.NoError(t, err)
	assert.Equal(t, 6, status.StateCounts["queued"])
	assert.Equal(t, 1, status.StateCounts["failed"])

	// Sanity: total stayed 7 in backfills.total_partitions.
	require.Equal(t, 7, status.TotalPartitions)

	// Sanity: enqueued vs. spec total is captured by the event payload — verify
	// at least one event_log row exists for this backfill.
	var evtCount int
	err = db.QueryRow(`SELECT COUNT(*) FROM event_log WHERE resource_id = $1`, id.String()).Scan(&evtCount)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, evtCount, 1)

	_ = fmt.Sprintf // keep fmt import in case format strings are added later
}
