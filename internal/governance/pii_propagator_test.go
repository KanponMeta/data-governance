package governance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/governance"
	"github.com/kanpon/data-governance/internal/governance/testharness"
)

// withPropagator boots a fresh test Postgres + a Propagator for assertions.
func withPropagator(t *testing.T) (*governance.Propagator, *sql.DB, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	db, cleanup := testharness.NewTestPostgres(t)
	return governance.NewPropagator(), db, cleanup
}

// seedColumnEdge inserts a single active column_edges row.
func seedColumnEdge(t *testing.T, db *sql.DB, fromAsset, fromCol, toAsset, toCol string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO column_edges
		    (id, from_asset, from_column, to_asset, to_column,
		     code_hash_first, code_hash_latest,
		     first_seen_run_id, first_seen_at, last_seen_run_id, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, 'h-aaa', 'h-aaa', $6, NOW(), $6, NOW())
	`, uuid.New(), fromAsset, fromCol, toAsset, toCol, uuid.New())
	require.NoError(t, err)
}

// seedPIITag marks a column as pii=true via the upstream path.
func seedPIITag(t *testing.T, db *sql.DB, asset, col string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO column_pii_tags (asset, column_name, pii, source, set_at)
		VALUES ($1, $2, TRUE, 'upstream', NOW())
		ON CONFLICT (asset, column_name) DO UPDATE
		    SET pii = TRUE, source = 'upstream', set_at = NOW()
	`, asset, col)
	require.NoError(t, err)
}

// readPII returns the pii flag and source for (asset, column), or (false, "")
// when the row does not exist.
func readPII(t *testing.T, db *sql.DB, assetName, col string) (bool, string) {
	t.Helper()
	var pii bool
	var source string
	err := db.QueryRowContext(context.Background(), `
		SELECT pii, source FROM column_pii_tags WHERE asset = $1 AND column_name = $2
	`, assetName, col).Scan(&pii, &source)
	if err == sql.ErrNoRows {
		return false, ""
	}
	require.NoError(t, err)
	return pii, source
}

// audit chain genesis — the test Postgres migration sentinel row already
// exists; auditCount returns the number of rows of a given type.
func auditCountByType(t *testing.T, db *sql.DB, eventType audit.AuditEventType) int {
	t.Helper()
	var n int
	err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit.audit_log WHERE event_type = $1`,
		string(eventType)).Scan(&n)
	require.NoError(t, err)
	return n
}

// TestPropagate_UnionRule_AnyUpstreamPII — fixture: column_edge from
// users.ssn (pii=true) to orders.customer_ssn → after Propagate,
// orders.customer_ssn carries pii=true.
func TestPropagate_UnionRule_AnyUpstreamPII(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	// Upstream column users.ssn is pii.
	seedPIITag(t, db, "users", "ssn")
	seedColumnEdge(t, db, "users", "ssn", "orders", "customer_ssn")

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	require.NoError(t, p.Propagate(ctx, tx, uuid.New(),
		[]governance.ColumnRef{{Asset: "orders", Column: "customer_ssn"}}, nil))
	require.NoError(t, tx.Commit())

	pii, source := readPII(t, db, "orders", "customer_ssn")
	require.True(t, pii, "downstream pii=true must be inherited via union rule")
	require.Equal(t, "upstream", source)
}

// TestPropagate_NoUpstreamPII_NoChange — no upstream is pii → asset_metadata
// for the output is unchanged (no row is created).
func TestPropagate_NoUpstreamPII_NoChange(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	seedColumnEdge(t, db, "users", "name", "orders", "customer_name")

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	require.NoError(t, p.Propagate(ctx, tx, uuid.New(),
		[]governance.ColumnRef{{Asset: "orders", Column: "customer_name"}}, nil))
	require.NoError(t, tx.Commit())

	pii, source := readPII(t, db, "orders", "customer_name")
	require.False(t, pii)
	require.Empty(t, source, "no row should be created when upstream is not pii")
}

// TestPropagate_OverrideStopsPropagation — override on
// orders_anon.hashed_ssn with Reason="hashed via Sha-256" → output does NOT
// receive pii=true even when upstream orders.ssn is pii.
func TestPropagate_OverrideStopsPropagation(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	seedPIITag(t, db, "orders", "ssn")
	seedColumnEdge(t, db, "orders", "ssn", "orders_anon", "hashed_ssn")

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	overrides := []asset.ColumnTagOverride{{
		Asset:  "orders_anon",
		Column: "hashed_ssn",
		Override: asset.TagOverride{
			Remove: "pii",
			Reason: "hashed via SHA-256 inside materialize; not reversible",
		},
	}}
	require.NoError(t, p.Propagate(ctx, tx, uuid.New(),
		[]governance.ColumnRef{{Asset: "orders_anon", Column: "hashed_ssn"}},
		overrides))
	require.NoError(t, tx.Commit())

	pii, source := readPII(t, db, "orders_anon", "hashed_ssn")
	require.False(t, pii, "override Remove='pii' must stop propagation")
	require.Equal(t, "override", source)
}

// TestPropagate_OverrideEmitsAuditOnce — first run emits
// metadata.tag_overridden audit row; second run with the same override
// does NOT emit a new audit row.
func TestPropagate_OverrideEmitsAuditOnce(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	overrides := []asset.ColumnTagOverride{{
		Asset:  "orders_anon",
		Column: "hashed_ssn",
		Override: asset.TagOverride{
			Remove: "pii",
			Reason: "hashed via SHA-256 inside materialize",
		},
	}}

	ctx := context.Background()
	before := auditCountByType(t, db, audit.AuditMetadataTagOverridden)

	// First propagation — should emit one audit entry.
	tx1, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, p.Propagate(ctx, tx1, uuid.New(), nil, overrides))
	require.NoError(t, tx1.Commit())

	mid := auditCountByType(t, db, audit.AuditMetadataTagOverridden)
	require.Equal(t, before+1, mid, "first override must emit a single audit entry")

	// Second propagation — same override; must NOT emit another audit entry.
	tx2, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, p.Propagate(ctx, tx2, uuid.New(), nil, overrides))
	require.NoError(t, tx2.Commit())

	after := auditCountByType(t, db, audit.AuditMetadataTagOverridden)
	require.Equal(t, mid, after, "re-applying the same override must be idempotent")
}

// TestPropagate_SameTxGuarantee — assert the propagator only writes via the
// supplied tx. We open a tx but rollback (without commit) and verify NO row
// reaches column_pii_tags or audit_log after the rollback.
func TestPropagate_SameTxGuarantee(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	seedPIITag(t, db, "users", "ssn")
	seedColumnEdge(t, db, "users", "ssn", "orders", "customer_ssn")

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)

	require.NoError(t, p.Propagate(ctx, tx, uuid.New(),
		[]governance.ColumnRef{{Asset: "orders", Column: "customer_ssn"}}, nil))

	// Roll back instead of commit. If Propagate had used a separate
	// connection, the row would survive.
	require.NoError(t, tx.Rollback())

	pii, _ := readPII(t, db, "orders", "customer_ssn")
	require.False(t, pii, "propagator writes MUST be inside the caller's tx (rollback erased them)")
}

// TestPropagate_RespectsCanceledContext — canceling the parent ctx returns
// a context error rather than completing the SQL.
func TestPropagate_RespectsCanceledContext(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	seedPIITag(t, db, "users", "ssn")
	seedColumnEdge(t, db, "users", "ssn", "orders", "customer_ssn")

	ctx, cancel := context.WithCancel(context.Background())
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	cancel()

	err = p.Propagate(ctx, tx, uuid.New(),
		[]governance.ColumnRef{{Asset: "orders", Column: "customer_ssn"}}, nil)
	require.Error(t, err)
}

// TestPropagate_MultipleUpstreamsUnion — output has 3 upstream cols; 1 is
// pii, 2 are not → output gets pii=true (union).
func TestPropagate_MultipleUpstreamsUnion(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	seedColumnEdge(t, db, "users", "name", "report", "out_col")
	seedColumnEdge(t, db, "users", "ssn", "report", "out_col")
	seedColumnEdge(t, db, "users", "email", "report", "out_col")
	seedPIITag(t, db, "users", "ssn") // only one of the three is pii

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	require.NoError(t, p.Propagate(ctx, tx, uuid.New(),
		[]governance.ColumnRef{{Asset: "report", Column: "out_col"}}, nil))
	require.NoError(t, tx.Commit())

	pii, source := readPII(t, db, "report", "out_col")
	require.True(t, pii, "union rule: any upstream pii=true → output pii=true")
	require.Equal(t, "upstream", source)

	// Verify the propagated_from JSON contains the pii upstream.
	var propagated string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT propagated_from::text FROM column_pii_tags WHERE asset = 'report' AND column_name = 'out_col'`,
	).Scan(&propagated))
	var refs []map[string]string
	require.NoError(t, json.Unmarshal([]byte(propagated), &refs))
	require.Len(t, refs, 1)
	require.Equal(t, "users", refs[0]["asset"])
	require.Equal(t, "ssn", refs[0]["column"])
}

// TestPropagate_OverrideWinsOverUpstream — even when upstream says pii=true,
// an override Remove="pii" stops the inheritance and writes source=override.
func TestPropagate_OverrideWinsOverUpstream(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	seedPIITag(t, db, "users", "ssn")
	seedColumnEdge(t, db, "users", "ssn", "orders_anon", "ssn_hash")

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	overrides := []asset.ColumnTagOverride{{
		Asset:  "orders_anon",
		Column: "ssn_hash",
		Override: asset.TagOverride{
			Remove: "pii",
			Reason: "irreversible HMAC at materialize time",
		},
	}}
	require.NoError(t, p.Propagate(ctx, tx, uuid.New(),
		[]governance.ColumnRef{{Asset: "orders_anon", Column: "ssn_hash"}},
		overrides))
	require.NoError(t, tx.Commit())

	pii, source := readPII(t, db, "orders_anon", "ssn_hash")
	require.False(t, pii)
	require.Equal(t, "override", source)
}

// TestPropagate_NilPropagator_GuardClause — calling Propagate with a nil
// tx returns an explicit error rather than panicking. (Defence-in-depth.)
func TestPropagate_NilTxReturnsError(t *testing.T) {
	p := governance.NewPropagator()
	err := p.Propagate(context.Background(), nil, uuid.New(), nil, nil)
	require.Error(t, err)
}

// TestPropagate_OccurredAtIsRecent — first override audit entry must have
// occurred_at within ~1 minute of NOW(), enforcing real wall-clock time.
func TestPropagate_OccurredAtIsRecent(t *testing.T) {
	p, db, cleanup := withPropagator(t)
	defer cleanup()

	overrides := []asset.ColumnTagOverride{{
		Asset:  "x",
		Column: "y",
		Override: asset.TagOverride{Add: "pii", Reason: "manually flagged for review"},
	}}

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, p.Propagate(ctx, tx, uuid.New(), nil, overrides))
	require.NoError(t, tx.Commit())

	var occurredAt time.Time
	require.NoError(t, db.QueryRowContext(ctx, `
		SELECT occurred_at FROM audit.audit_log
		 WHERE event_type = $1 AND resource_id = 'x.y'
		 ORDER BY seq DESC LIMIT 1
	`, string(audit.AuditMetadataTagOverridden)).Scan(&occurredAt))
	require.WithinDuration(t, time.Now().UTC(), occurredAt, time.Minute)
}
