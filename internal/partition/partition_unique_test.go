package partition_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openTestDB opens a database connection from DATABASE_URL or skips the test.
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

// TestPartitionUniqueConstraint exercises the partial unique index
// run_partition_inflight_unique on (asset_name, partition_key) WHERE
// state IN ('queued','starting','running') AND partition_key IS NOT NULL
// (D-10 + Pitfall 7).
//
// Behaviors verified:
//  1. Two queued rows with same (asset, partition_key) — second INSERT fails.
//  2. After the first run reaches a terminal state ('succeeded'), re-INSERT
//     with the same (asset, partition_key) succeeds — partial index ignores
//     terminal states.
//  3. Two queued rows with partition_key = NULL succeed — NULL is not unique
//     under the partial index predicate (partition_key IS NOT NULL).
//  4. queued + running for the same partition both fail — both states are
//     in-flight and the partial index covers both.
func TestPartitionUniqueConstraint(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const assetName = "test_partition_unique"
	ctx := context.Background()

	// Best-effort cleanup before and after.
	_, _ = db.ExecContext(ctx, `DELETE FROM runs WHERE asset_name = $1`, assetName)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM runs WHERE asset_name = $1`, assetName)
	})

	insert := func(state, partitionKey string) error {
		var pk interface{} = partitionKey
		if partitionKey == "" {
			pk = nil
		}
		_, err := db.ExecContext(ctx,
			`INSERT INTO runs (id, asset_name, state, trigger, queued_at, priority, partition_key)
			 VALUES (gen_random_uuid(), $1, $2, 'manual', NOW(), 'normal', $3)`,
			assetName, state, pk)
		return err
	}

	// 1. Two queued rows with same (asset, partition_key) — second must fail.
	require.NoError(t, insert("queued", "2024-01-01"),
		"first queued INSERT for partition 2024-01-01 must succeed")
	err := insert("queued", "2024-01-01")
	assert.Error(t, err,
		"second queued INSERT for same partition must fail unique constraint (D-10 in-flight uniqueness)")

	// 2. Mark first run succeeded — re-INSERT should now succeed
	//    (partial index excludes terminal states).
	_, err = db.ExecContext(ctx,
		`UPDATE runs SET state = 'succeeded', finished_at = NOW()
		 WHERE asset_name = $1 AND partition_key = $2`,
		assetName, "2024-01-01")
	require.NoError(t, err)
	assert.NoError(t, insert("queued", "2024-01-01"),
		"INSERT for re-run after terminal state must succeed (state IN ('queued','starting','running') predicate ignores 'succeeded')")

	// 3. Two queued rows with partition_key = NULL — both must succeed
	//    (partial index predicate has AND partition_key IS NOT NULL).
	require.NoError(t, insert("queued", ""),
		"first NULL-partition queued INSERT must succeed")
	assert.NoError(t, insert("queued", ""),
		"second NULL-partition queued INSERT must succeed (NULL excluded from partial unique)")

	// 4. queued + running for same partition both fail — both states are
	//    in-flight under the partial index.
	require.NoError(t, insert("queued", "2024-02-01"))
	err = insert("running", "2024-02-01")
	assert.Error(t, err,
		"INSERT 'running' alongside 'queued' for same partition must fail "+
			"(state IN ('queued','starting','running') predicate covers both)")
}
