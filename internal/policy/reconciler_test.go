package policy

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
)

// recordingReEnqueuer captures ReEnqueueSync calls.
type recordingReEnqueuer struct {
	mu    sync.Mutex
	Calls []PolicySyncArgs
}

func (r *recordingReEnqueuer) ReEnqueueSync(_ context.Context, args PolicySyncArgs) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Calls = append(r.Calls, args)
	return nil
}

// reconcilerFixture is a populated test setup: store + DB + recording stubs.
type reconcilerFixture struct {
	store     *Store
	conn      *fakeProvisionerConnector
	resolver  *fixedResolver
	audit     *recordingAudit
	reenq     *recordingReEnqueuer
	cleanup   func()
	now       time.Time
}

func newReconcilerFixture(t *testing.T) *reconcilerFixture {
	t.Helper()
	store, _, _, cleanup := withStore(t)
	conn := &fakeProvisionerConnector{name: "snowflake"}
	return &reconcilerFixture{
		store:    store,
		conn:     conn,
		resolver: &fixedResolver{conn: conn, ref: connector.AssetRef{Identifier: "DB.SCH.orders"}},
		audit:    &recordingAudit{},
		reenq:    &recordingReEnqueuer{},
		cleanup:  cleanup,
		now:      time.Now(),
	}
}

func (f *reconcilerFixture) reconciler() *Reconciler {
	r := NewReconciler(f.store, f.resolver, f.audit, f.reenq)
	r.Now = func() time.Time { return f.now }
	return r
}

// seedActiveBuilder writes a single builder row for (asset, column) with
// last_seen_at = NOW() - 1 hour so the grace period cannot mask it.
func (f *reconcilerFixture) seedActiveBuilder(t *testing.T, assetName, column string, mask connector.MaskType) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, f.store.Apply(ctx, tx, assetName, "h1", nil, []asset.ColumnPolicy{
		{Column: column, Mask: mask},
	}))
	require.NoError(t, tx.Commit())
	// Backdate last_seen_at past the grace period.
	_, err = f.store.db.ExecContext(ctx, `
		UPDATE column_policies SET last_seen_at = NOW() - INTERVAL '1 hour'
		 WHERE asset = $1 AND column_name = $2 AND superseded_at IS NULL
	`, assetName, column)
	require.NoError(t, err)
}

// TestReconciler_NoDriftWhenAligned — actual matches expected → 0 drifts.
func TestReconciler_NoDriftWhenAligned(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	f := newReconcilerFixture(t)
	defer f.cleanup()

	f.seedActiveBuilder(t, "orders", "ssn", connector.MaskHash)
	f.conn.ListResponse = []connector.ColumnPolicy{
		{Column: "ssn", MaskType: connector.MaskHash},
	}

	rep, err := f.reconciler().Tick(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, rep.Scanned)
	require.Equal(t, 0, rep.Drifts)
	require.Empty(t, f.reenq.Calls)
	require.Empty(t, f.audit.Entries)
}

// TestReconciler_DriftEmitsAuditAndReEnqueues — actual missing → drift.
func TestReconciler_DriftEmitsAuditAndReEnqueues(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	f := newReconcilerFixture(t)
	defer f.cleanup()

	f.seedActiveBuilder(t, "orders", "ssn", connector.MaskHash)
	f.conn.ListResponse = []connector.ColumnPolicy{} // missing on warehouse

	rep, err := f.reconciler().Tick(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, rep.Drifts)
	require.Equal(t, 1, rep.Pushed)
	require.Len(t, f.reenq.Calls, 1)
	require.Equal(t, "orders", f.reenq.Calls[0].Asset)
	require.Equal(t, "ssn", f.reenq.Calls[0].Column)
	require.Len(t, f.audit.Entries, 1)
}

// TestReconciler_DriftOnMaskMismatch — actual has different mask → drift.
func TestReconciler_DriftOnMaskMismatch(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	f := newReconcilerFixture(t)
	defer f.cleanup()

	f.seedActiveBuilder(t, "orders", "ssn", connector.MaskHash)
	f.conn.ListResponse = []connector.ColumnPolicy{
		{Column: "ssn", MaskType: connector.MaskRedact}, // wrong mask
	}

	rep, err := f.reconciler().Tick(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, rep.Drifts)
	require.Equal(t, 1, rep.Pushed)
}

// TestReconciler_GracePeriodSkipsRecentChanges — column updated within
// GracePeriod is NOT flagged as drift.
func TestReconciler_GracePeriodSkipsRecentChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	f := newReconcilerFixture(t)
	defer f.cleanup()

	// Seed with a recent last_seen_at (default Apply uses NOW()).
	ctx := context.Background()
	tx, err := f.store.db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, f.store.Apply(ctx, tx, "orders", "h1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash},
	}))
	require.NoError(t, tx.Commit())
	f.conn.ListResponse = []connector.ColumnPolicy{} // missing on warehouse

	rep, err := f.reconciler().Tick(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, rep.Drifts, "recent change should be inside grace period")
	require.Empty(t, f.reenq.Calls)
}

// TestReconciler_NonProvisionerConnector_SkippedWithoutError — non-provisioner
// connector is silently skipped.
func TestReconciler_NonProvisionerConnector_SkippedWithoutError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	f := newReconcilerFixture(t)
	defer f.cleanup()

	f.seedActiveBuilder(t, "orders", "ssn", connector.MaskHash)
	f.resolver.conn = fakeNonProvisionerConnector{}

	rep, err := f.reconciler().Tick(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, rep.Drifts)
	require.Empty(t, f.reenq.Calls)
}

// TestReconciler_DriftOnExtraTag — actual has tag the platform doesn't expect.
func TestReconciler_DriftOnExtraTag(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	f := newReconcilerFixture(t)
	defer f.cleanup()

	// No seeded policy for orders — but reconciler scans only assets in store.
	// To exercise the EXTRA detection we must seed at least one active row.
	// Seed a different column so the extra tag on "ssn" is unexpected.
	f.seedActiveBuilder(t, "orders", "email", connector.MaskRedact)
	f.conn.ListResponse = []connector.ColumnPolicy{
		{Column: "email", MaskType: connector.MaskRedact},
		{Column: "ssn", MaskType: connector.MaskHash}, // extra
	}

	rep, err := f.reconciler().Tick(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, rep.Drifts) // only the extra ssn
}
