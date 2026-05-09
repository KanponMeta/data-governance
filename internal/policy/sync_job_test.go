package policy

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/connector"
)

// fakeProvisionerConnector is a connector.Connector that ALSO satisfies
// connector.MaskingProvisioner. Use it to drive the warehouse-native path.
type fakeProvisionerConnector struct {
	name string
	mu   sync.Mutex

	ApplyErr     error
	ApplyCalls   int
	LastPolicy   connector.ColumnPolicy
	RemoveCalls  int
	ListResponse []connector.ColumnPolicy
}

var _ connector.Connector = (*fakeProvisionerConnector)(nil)
var _ connector.MaskingProvisioner = (*fakeProvisionerConnector)(nil)

func (f *fakeProvisionerConnector) APIVersion() string { return connector.APIVersion }
func (f *fakeProvisionerConnector) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{ConnectorName: f.name, APIVersion: connector.APIVersion}, nil
}
func (f *fakeProvisionerConnector) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (f *fakeProvisionerConnector) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{}, nil
}
func (f *fakeProvisionerConnector) Write(_ context.Context, _ connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{}, nil
}
func (f *fakeProvisionerConnector) ApplyMaskingPolicy(_ context.Context, _ connector.AssetRef, p connector.ColumnPolicy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ApplyCalls++
	f.LastPolicy = p
	return f.ApplyErr
}
func (f *fakeProvisionerConnector) RemoveMaskingPolicy(_ context.Context, _ connector.AssetRef, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.RemoveCalls++
	return nil
}
func (f *fakeProvisionerConnector) ListMaskingPolicies(_ context.Context, _ connector.AssetRef) ([]connector.ColumnPolicy, error) {
	return f.ListResponse, nil
}

// fakeNonProvisionerConnector implements connector.Connector but NOT
// MaskingProvisioner — drives the in-pipeline path.
type fakeNonProvisionerConnector struct{}

var _ connector.Connector = (*fakeNonProvisionerConnector)(nil)

func (fakeNonProvisionerConnector) APIVersion() string { return connector.APIVersion }
func (fakeNonProvisionerConnector) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{ConnectorName: "kafka", APIVersion: connector.APIVersion}, nil
}
func (fakeNonProvisionerConnector) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (fakeNonProvisionerConnector) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{}, nil
}
func (fakeNonProvisionerConnector) Write(_ context.Context, _ connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{}, nil
}

// fixedResolver returns a fixed (connector, ref) for any asset.
type fixedResolver struct {
	conn connector.Connector
	ref  connector.AssetRef
	err  error
}

func (r *fixedResolver) ResolveByAsset(_ context.Context, _ string) (connector.Connector, connector.AssetRef, error) {
	return r.conn, r.ref, r.err
}

// recordingAudit captures permanent-failure entries.
type recordingAudit struct {
	mu      sync.Mutex
	Entries []audit.Entry
}

func (r *recordingAudit) WritePermanentFailure(_ context.Context, e audit.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Entries = append(r.Entries, e)
	return nil
}

// TestPolicySyncWorker_AppliesAndUpdatesEnforcementMode — happy path.
func TestPolicySyncWorker_AppliesAndUpdatesEnforcementMode(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()
	ctx := context.Background()

	// Seed a builder policy so Resolve returns something.
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "h1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash, AllowRoles: []string{"PII_ANALYST"}},
	}))
	require.NoError(t, tx.Commit())

	conn := &fakeProvisionerConnector{name: "snowflake"}
	w := NewPolicySyncWorker(store, &fixedResolver{conn: conn, ref: connector.AssetRef{Identifier: "DB.SCH.orders"}}, &recordingAudit{})

	require.NoError(t, w.Work(ctx, PolicySyncArgs{Asset: "orders", Column: "ssn"}, 1))
	require.Equal(t, 1, conn.ApplyCalls)
	require.Equal(t, connector.MaskHash, conn.LastPolicy.MaskType)

	rows, err := store.List(ctx, "orders", "")
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	require.Equal(t, "warehouse-native", rows[0].EnforcementMode)
	require.Equal(t, "synced", rows[0].SyncStatus)
}

// TestPolicySyncWorker_NonProvisionerConnector_SetsInPipeline — connector without
// MaskingProvisioner sets enforcement_mode=in-pipeline + sync_status=synced.
func TestPolicySyncWorker_NonProvisionerConnector_SetsInPipeline(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "h1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash},
	}))
	require.NoError(t, tx.Commit())

	w := NewPolicySyncWorker(store, &fixedResolver{conn: fakeNonProvisionerConnector{}, ref: connector.AssetRef{Identifier: "topic.events"}}, &recordingAudit{})
	require.NoError(t, w.Work(ctx, PolicySyncArgs{Asset: "orders", Column: "ssn"}, 1))

	rows, err := store.List(ctx, "orders", "")
	require.NoError(t, err)
	require.Equal(t, "in-pipeline", rows[0].EnforcementMode)
	require.Equal(t, "synced", rows[0].SyncStatus)
}

// TestPolicySyncWorker_PermanentFailure_WritesAuditAndDispatchesAlert —
// after MaxSyncAttempts the worker writes a masking.sync_failed audit entry.
func TestPolicySyncWorker_PermanentFailure_WritesAuditAndDispatchesAlert(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, db, _, cleanup := withStore(t)
	defer cleanup()
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	require.NoError(t, store.Apply(ctx, tx, "orders", "h1", nil, []asset.ColumnPolicy{
		{Column: "ssn", Mask: connector.MaskHash},
	}))
	require.NoError(t, tx.Commit())

	conn := &fakeProvisionerConnector{name: "snowflake", ApplyErr: errors.New("permission denied")}
	rec := &recordingAudit{}
	w := NewPolicySyncWorker(store, &fixedResolver{conn: conn, ref: connector.AssetRef{Identifier: "DB.SCH.orders"}}, rec)

	// Last attempt — should record audit + return error.
	err = w.Work(ctx, PolicySyncArgs{Asset: "orders", Column: "ssn"}, MaxSyncAttempts)
	require.Error(t, err)
	require.Len(t, rec.Entries, 1)
	require.Equal(t, audit.AuditMaskingSyncFailed, rec.Entries[0].EventType)
	require.Equal(t, "orders.ssn", rec.Entries[0].ResourceID)

	// Earlier attempts must NOT record audit (only return error).
	rec.Entries = nil
	err = w.Work(ctx, PolicySyncArgs{Asset: "orders", Column: "ssn"}, 1)
	require.Error(t, err)
	require.Empty(t, rec.Entries)
}

// TestPolicySyncWorker_RespectsContextCancel — cancelled ctx propagates.
func TestPolicySyncWorker_RespectsContextCancel(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, _, _, cleanup := withStore(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	w := NewPolicySyncWorker(store,
		&fixedResolver{conn: &fakeProvisionerConnector{name: "snowflake"}, ref: connector.AssetRef{Identifier: "DB.SCH.orders"}},
		&recordingAudit{},
	)
	err := w.Work(ctx, PolicySyncArgs{Asset: "orders", Column: "ssn"}, 1)
	require.Error(t, err)
}

// TestPolicySyncWorker_NoPolicy_ReturnsNoError — Resolve returning ErrPolicyNotFound
// should be treated as a benign no-op (an in-flight delete for example).
func TestPolicySyncWorker_NoPolicy_ReturnsNoError(t *testing.T) {
	if testing.Short() {
		t.Skip("requires testcontainers")
	}
	store, _, _, cleanup := withStore(t)
	defer cleanup()
	ctx := context.Background()

	w := NewPolicySyncWorker(store,
		&fixedResolver{conn: &fakeProvisionerConnector{name: "snowflake"}, ref: connector.AssetRef{Identifier: "DB.SCH.orders"}},
		&recordingAudit{},
	)
	err := w.Work(ctx, PolicySyncArgs{Asset: "orders", Column: "no-such-column"}, 1)
	require.NoError(t, err)
}
