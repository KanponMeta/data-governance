package policy

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/governance/testharness"
)

// recordingEnqueuer captures EnqueueSync calls so tests can assert the River
// job would have been queued. Production wiring uses the river.Insert path
// inside the same tx; tests substitute this stub.
type recordingEnqueuer struct {
	calls []PolicySyncArgs
}

func (r *recordingEnqueuer) EnqueueSync(_ context.Context, _ *sql.Tx, args PolicySyncArgs) error {
	r.calls = append(r.calls, args)
	return nil
}

// withStore returns a Store wired against a freshly migrated test Postgres,
// plus the underlying *sql.DB and a recording enqueuer for assertions.
func withStore(t *testing.T) (*Store, *sql.DB, *recordingEnqueuer, func()) {
	t.Helper()
	db, cleanup := testharness.NewTestPostgres(t)
	enq := &recordingEnqueuer{}
	store := NewStore(db, enq)
	return store, db, enq, cleanup
}

// TestStore_Apply_Idempotent — calling Apply twice with the same policies
// produces a single active builder row. (Phase 5 D-04 idempotency.)
func TestStore_Apply_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()

	policies := []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash, AllowRoles: []string{"pii-analyst"}},
	}
	require.NoError(t, store.Apply(ctx, tx, "orders", "hash-aaa", nil, policies))
	require.NoError(t, store.Apply(ctx, tx, "orders", "hash-aaa", nil, policies))
	require.NoError(t, tx.Commit())

	rows, err := store.List(ctx, "orders", "builder")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "ssn", rows[0].Column)
	require.Equal(t, connector.MaskHash, rows[0].Mask)
}

// TestStore_Apply_SoftRetiresRemoved — a column dropped from the builder
// declaration is soft-retired (superseded_at set) and emits policy.removed.
func TestStore_Apply_SoftRetiresRemoved(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "hash-1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash, AllowRoles: []string{"pii-analyst"}},
	}))
	require.NoError(t, tx.Commit())

	tx, err = db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "hash-2", nil, []asset.ColumnPolicy{}))
	require.NoError(t, tx.Commit())

	rows, err := store.List(ctx, "orders", "builder")
	require.NoError(t, err)
	require.Len(t, rows, 0, "removed column should not appear in active list")

	// Audit chain should contain a policy.removed entry.
	var removedCount int
	require.NoError(t, db.QueryRow(`
		SELECT COUNT(*) FROM audit.audit_log
		 WHERE event_type='policy.removed' AND resource_id='orders.ssn'
	`).Scan(&removedCount))
	require.GreaterOrEqual(t, removedCount, 1)
}

// TestStore_Patch_RuntimeOverridesBuilder — Resolve() returns the runtime
// row when both layers exist (precedence: runtime > builder).
func TestStore_Patch_RuntimeOverridesBuilder(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "hash-1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash, AllowRoles: []string{"admin"}},
	}))
	require.NoError(t, tx.Commit())

	actor := uuid.New()
	eff, err := store.Patch(ctx, actor, "orders", "ssn", connector.MaskRedact,
		[]string{"analyst"}, "user requested redaction over hash")
	require.NoError(t, err)
	require.Equal(t, "runtime", eff.Source)
	require.Equal(t, connector.MaskRedact, eff.Mask)
	require.Equal(t, []string{"analyst"}, eff.AllowRoles)

	resolved, err := store.Resolve(ctx, "orders", "ssn")
	require.NoError(t, err)
	require.Equal(t, "runtime", resolved.Source)
	require.Equal(t, connector.MaskRedact, resolved.Mask)
}

// TestStore_Patch_WritesAuditEntry — Patch must write a policy.changed
// entry to the hash chain inside the same transaction.
func TestStore_Patch_WritesAuditEntry(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, enq, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	actor := uuid.New()
	_, err := store.Patch(ctx, actor, "orders", "ssn", connector.MaskHash,
		[]string{"pii-analyst"}, "test reason")
	require.NoError(t, err)

	chain := testharness.ReadChain(t, db)
	require.NotEmpty(t, chain)
	last := chain[len(chain)-1]
	require.Equal(t, "policy.changed", last.EventType)
	require.Equal(t, "column_policy", last.ResourceType)
	require.Equal(t, "orders.ssn", last.ResourceID)

	// River sync job must also have been enqueued (recorded by stub).
	require.Len(t, enq.calls, 1)
	require.Equal(t, "orders", enq.calls[0].Asset)
	require.Equal(t, "ssn", enq.calls[0].Column)
}

// TestStore_Patch_RequiresReason — empty reason returns ErrReasonRequired
// with no DB mutation.
func TestStore_Patch_RequiresReason(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, enq, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	_, err := store.Patch(ctx, uuid.New(), "orders", "ssn", connector.MaskHash, nil, "")
	require.ErrorIs(t, err, ErrReasonRequired)

	rows, err := store.List(ctx, "orders", "")
	require.NoError(t, err)
	require.Empty(t, rows)
	require.Empty(t, enq.calls)

	// chain MUST not have any policy.changed entries.
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM audit.audit_log WHERE event_type='policy.changed'`).Scan(&n))
	require.Zero(t, n)
}

// TestStore_Resolve_PrecedenceOrder verifies all four lookup states:
// runtime present, builder present, yaml-default present, none present.
func TestStore_Resolve_PrecedenceOrder(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()

	// (a) none present → ErrPolicyNotFound
	_, err := store.Resolve(ctx, "orders", "ssn")
	require.ErrorIs(t, err, ErrPolicyNotFound)

	// (b) yaml-default only.
	_, err = db.ExecContext(ctx, `
		INSERT INTO column_policies (asset, column_name, mask_type, allow_roles, code_hash_first, code_hash_latest, source, reason, enforcement_mode)
		VALUES ('orders','ssn','redact','[]'::jsonb,'','','yaml-default','yaml-tag:pii','unknown')
	`)
	require.NoError(t, err)
	eff, err := store.Resolve(ctx, "orders", "ssn")
	require.NoError(t, err)
	require.Equal(t, "yaml-default", eff.Source)
	require.Equal(t, connector.MaskRedact, eff.Mask)

	// (c) builder added → builder wins over yaml-default.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "hash-1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash, AllowRoles: []string{"admin"}},
	}))
	require.NoError(t, tx.Commit())
	eff, err = store.Resolve(ctx, "orders", "ssn")
	require.NoError(t, err)
	require.Equal(t, "builder", eff.Source)
	require.Equal(t, connector.MaskHash, eff.Mask)

	// (d) runtime added → runtime wins over both.
	_, err = store.Patch(ctx, uuid.New(), "orders", "ssn", connector.MaskPartial,
		[]string{"analyst"}, "tighten")
	require.NoError(t, err)
	eff, err = store.Resolve(ctx, "orders", "ssn")
	require.NoError(t, err)
	require.Equal(t, "runtime", eff.Source)
	require.Equal(t, connector.MaskPartial, eff.Mask)

	// Latency guard — Resolve must complete quickly.
	deadline := time.Now().Add(2 * time.Second)
	_, err = store.Resolve(ctx, "orders", "ssn")
	require.NoError(t, err)
	require.True(t, time.Now().Before(deadline))
}

// TestStore_SetEnforcementMode and TestStore_SetSyncStatus exercise the
// worker hooks used by the River sync job.
func TestStore_SetEnforcementMode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "hash-1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash, AllowRoles: []string{"admin"}},
	}))
	require.NoError(t, tx.Commit())

	require.NoError(t, store.SetEnforcementMode(ctx, "orders", "ssn", "warehouse-native"))
	rows, err := store.List(ctx, "orders", "builder")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "warehouse-native", rows[0].EnforcementMode)

	require.NoError(t, store.SetSyncStatus(ctx, "orders", "ssn", "synced"))
	rows, err = store.List(ctx, "orders", "builder")
	require.NoError(t, err)
	require.Equal(t, "synced", rows[0].SyncStatus)

	require.Error(t, store.SetEnforcementMode(ctx, "orders", "ssn", "bad-mode"))
	require.Error(t, store.SetSyncStatus(ctx, "orders", "ssn", "bad-status"))
}

// ---- Plan 05-03 (RBAC-05) MaskRulesForAsset coverage ----

// TestStore_MaskRulesForAsset_OnlyInPipelineRows verifies that
// enforcement_mode='in-pipeline' AND 'unknown' rows are returned, while
// 'warehouse-native' rows are excluded (warehouse handles those).
func TestStore_MaskRulesForAsset_OnlyInPipelineRows(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "h-1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash},
		{Column: "email", Mask: connector.MaskRedact},
		{Column: "phone", Mask: connector.MaskPartial, PartialReveal: 2},
	}))
	require.NoError(t, tx.Commit())

	// Mark only ssn as warehouse-native — others stay 'unknown'.
	require.NoError(t, store.SetEnforcementMode(ctx, "orders", "ssn", "warehouse-native"))
	require.NoError(t, store.SetEnforcementMode(ctx, "orders", "email", "in-pipeline"))
	// phone stays at 'unknown' default → still picked.

	rules, err := store.MaskRulesForAsset(ctx, "orders")
	require.NoError(t, err)

	got := map[string]connector.MaskType{}
	for _, r := range rules {
		got[r.Column] = r.Mask
	}
	require.NotContains(t, got, "ssn", "warehouse-native rows MUST be excluded")
	require.Contains(t, got, "email")
	require.Equal(t, connector.MaskRedact, got["email"])
	require.Contains(t, got, "phone", "unknown enforcement_mode rows are included (pending sync)")
}

// TestStore_MaskRulesForAsset_PIIWithoutPolicyFallsBackToRedact —
// columns with column_pii_tags.pii=true and no column_policies row receive
// the default redact rule.
func TestStore_MaskRulesForAsset_PIIWithoutPolicyFallsBackToRedact(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()

	// Seed pii=true with NO column_policy row.
	_, err := db.ExecContext(ctx, `
		INSERT INTO column_pii_tags (asset, column_name, pii, source, set_at)
		VALUES ('events', 'user_email', TRUE, 'upstream', NOW())
	`)
	require.NoError(t, err)

	rules, err := store.MaskRulesForAsset(ctx, "events")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, "user_email", rules[0].Column)
	require.Equal(t, DefaultMaskForPII(), rules[0].Mask)
}

// TestStore_MaskRulesForAsset_WarehouseNativeRowsExcluded — explicit
// guarantee that even if every row is warehouse-native, the rule list is
// empty and the executor will not wrap MaskingIO redundantly.
func TestStore_MaskRulesForAsset_WarehouseNativeRowsExcluded(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "h-1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash},
		{Column: "email", Mask: connector.MaskRedact},
	}))
	require.NoError(t, tx.Commit())

	require.NoError(t, store.SetEnforcementMode(ctx, "orders", "ssn", "warehouse-native"))
	require.NoError(t, store.SetEnforcementMode(ctx, "orders", "email", "warehouse-native"))

	rules, err := store.MaskRulesForAsset(ctx, "orders")
	require.NoError(t, err)
	require.Empty(t, rules, "all rows warehouse-native → empty rule set")
}

// TestStore_MaskRulesForAsset_PIIWithExistingPolicy_NoFallback — when a
// pii column already has an active policy, the fallback path MUST NOT
// double-add a redact rule.
func TestStore_MaskRulesForAsset_PIIWithExistingPolicy_NoFallback(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "events", "h-1", nil, []asset.ColumnPolicy{
		{Column: "user_email", Mask: connector.MaskHash},
	}))
	require.NoError(t, tx.Commit())
	require.NoError(t, store.SetEnforcementMode(ctx, "events", "user_email", "in-pipeline"))

	_, err = db.ExecContext(ctx, `
		INSERT INTO column_pii_tags (asset, column_name, pii, source, set_at)
		VALUES ('events', 'user_email', TRUE, 'upstream', NOW())
	`)
	require.NoError(t, err)

	rules, err := store.MaskRulesForAsset(ctx, "events")
	require.NoError(t, err)
	require.Len(t, rules, 1)
	require.Equal(t, connector.MaskHash, rules[0].Mask,
		"existing policy wins; no double-rule from pii fallback")
}

// TestStore_MaskRulesForAsset_EmptyAsset — empty assetName surfaces an error.
func TestStore_MaskRulesForAsset_EmptyAsset(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, _, _, cleanup := withStore(t)
	defer cleanup()
	_, err := store.MaskRulesForAsset(context.Background(), "")
	require.Error(t, err)
}
