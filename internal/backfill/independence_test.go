package backfill_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/backfill"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/partition"
)

// TestCategoryPartitionIndependence (validation map / D-16 / ORCH-08) — proves
// that one partition's failure does NOT propagate to sibling partitions in the
// same backfill. Each partition is its own runs row; the database imposes no
// shared state tying partition fates together.
//
// Test scenario: 3-category backfill (us, eu, apac). After Submit, manually
// flip the `us` run to 'failed'. Assert eu + apac remain in 'queued' state and
// GetStatus reports {queued: 2, failed: 1}.
//
// This test only validates the database-level independence guarantee — full
// executor + retry-policy exercise belongs to a downstream e2e test that reuses
// the worker subcommand. The independence claim of D-16 is precisely about
// per-partition isolation at the runs-table level.
func TestCategoryPartitionIndependence(t *testing.T) {
	db, entClient := openTestDB(t)
	store := &entStorage{db: db, ent: entClient}
	const assetName = "test_category_partition_independence_orch08"
	deleteBackfillData(t, db, assetName)
	t.Cleanup(func() { deleteBackfillData(t, db, assetName) })

	strategy := partition.CategoryPartitions{Keys: []string{"us", "eu", "apac"}}
	spec, err := backfill.ParsePartitionSpec(strategy, "us,eu,apac", backfill.DefaultMaxPartitions)
	require.NoError(t, err)
	require.Equal(t, []string{"us", "eu", "apac"}, spec.Keys)

	events := event.NewWriter(store)
	id, err := backfill.Submit(context.Background(), store, events, assetName, spec)
	require.NoError(t, err)

	// Sanity: 3 runs queued.
	var queuedBefore int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM runs WHERE backfill_id = $1 AND state = 'queued'`, id).Scan(&queuedBefore))
	require.Equal(t, 3, queuedBefore)

	// Flip the 'us' partition to failed via direct SQL.
	res, err := db.Exec(`UPDATE runs SET state = 'failed' WHERE backfill_id = $1 AND partition_key = 'us' AND state = 'queued'`, id)
	require.NoError(t, err)
	affected, _ := res.RowsAffected()
	require.Equal(t, int64(1), affected, "exactly one 'us' run should transition to failed")

	// eu + apac remain queued — D-16 independence guarantee.
	var (
		euState   string
		apacState string
	)
	require.NoError(t, db.QueryRow(`SELECT state FROM runs WHERE backfill_id = $1 AND partition_key = 'eu'`, id).Scan(&euState))
	require.NoError(t, db.QueryRow(`SELECT state FROM runs WHERE backfill_id = $1 AND partition_key = 'apac'`, id).Scan(&apacState))
	assert.Equal(t, "queued", euState, "eu partition must remain queued (D-16 independence)")
	assert.Equal(t, "queued", apacState, "apac partition must remain queued (D-16 independence)")

	// GetStatus reports {queued: 2, failed: 1}.
	status, err := backfill.GetStatus(context.Background(), db, id)
	require.NoError(t, err)
	assert.Equal(t, 2, status.StateCounts["queued"])
	assert.Equal(t, 1, status.StateCounts["failed"])
	assert.Equal(t, 3, status.TotalPartitions)
}
