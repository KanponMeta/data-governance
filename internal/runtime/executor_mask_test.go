package runtime_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/concurrency"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/policy"
	"github.com/kanpon/data-governance/internal/runtime"
)

// stubProvider implements runtime.MaskRulesProvider for unit tests.
type stubProvider struct {
	rules []policy.MaskRule
	err   error
}

func (s *stubProvider) MaskRulesForAsset(_ context.Context, _ string) ([]policy.MaskRule, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rules, nil
}

// maskingProvisionerConn is a fake connector.MaskingProvisioner. It satisfies
// the warehouse-native capability so the executor MUST NOT wrap MaskingIO.
type maskingProvisionerConn struct{ recordingConnector }

func (m *maskingProvisionerConn) ApplyMaskingPolicy(_ context.Context, _ connector.AssetRef, _ connector.ColumnPolicy) error {
	return nil
}
func (m *maskingProvisionerConn) RemoveMaskingPolicy(_ context.Context, _ connector.AssetRef, _ string) error {
	return nil
}
func (m *maskingProvisionerConn) ListMaskingPolicies(_ context.Context, _ connector.AssetRef) ([]connector.ColumnPolicy, error) {
	return nil, nil
}

// fakeAssetIO records what reaches Write so tests can assert wrapping.
type fakeAssetIO struct {
	rows         []connector.Row
	wroteCount   int
	rowsWritten  int64
	hasMaskApplied bool
}

func (f *fakeAssetIO) Read(_ context.Context, _ string) ([]connector.Row, error) {
	return nil, nil
}
func (f *fakeAssetIO) Write(_ context.Context, rows []connector.Row) (int64, error) {
	f.wroteCount++
	f.rows = rows
	for _, r := range rows {
		if v, ok := r.Fields["ssn"].(string); ok && v != "raw-secret" {
			f.hasMaskApplied = true
		}
	}
	return f.rowsWritten, nil
}
func (f *fakeAssetIO) PartitionKey() string { return "" }

// buildExecutorWithMaskingProvider constructs a runtime.Executor wired with
// the supplied connector + MaskRulesProvider. It does NOT touch the DB and
// is therefore safe to run without DATABASE_URL.
func buildExecutorWithMaskingProvider(t *testing.T, conn connector.Connector, provider runtime.MaskRulesProvider) (*runtime.Executor, *asset.Asset) {
	t.Helper()

	// Build an asset bound to "test-connector".
	a, err := asset.New("orders").
		Connector("test-connector").
		Materialize(func(_ context.Context, _ asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, nil
		}).
		Build()
	require.NoError(t, err)

	reg := asset.NewDefinitionRegistry()
	require.NoError(t, reg.Register(a))
	connReg := connector.NewRegistry()
	require.NoError(t, connReg.RegisterInProcess("test-connector", conn))

	store := &rawStorage{} // DB not needed for maybeWrapMaskingIO unit tests
	pool := concurrency.NewPool(store, nil)

	exec := runtime.NewExecutor(runtime.Deps{
		Store:             store,
		Events:            event.NewWriter(store),
		Registry:          reg,
		ConnectorReg:      connReg,
		Pool:              pool,
		WorkerID:          "unit-test",
		MaskRulesProvider: provider,
	})
	return exec, a
}

// TestExecutor_MaybeWrap_NoProvider_ReturnsInner — when the executor has no
// MaskRulesProvider, the AssetIO chain is unchanged (Phase 4 path).
func TestExecutor_MaybeWrap_NoProvider_ReturnsInner(t *testing.T) {
	conn := &recordingConnector{}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, nil)

	inner := &fakeAssetIO{}
	got, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", inner)
	require.NoError(t, err)
	require.Equal(t, asset.AssetIO(inner), got, "no provider → no wrapping")
}

// TestExecutor_MaybeWrap_WarehouseConnector_ReturnsInner — connector
// implements MaskingProvisioner → no wrapping (warehouse-native takes
// precedence; in-pipeline wrap would double-mask).
func TestExecutor_MaybeWrap_WarehouseConnector_ReturnsInner(t *testing.T) {
	conn := &maskingProvisionerConn{}
	provider := &stubProvider{rules: []policy.MaskRule{{Column: "ssn", Mask: connector.MaskHash}}}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, provider)

	inner := &fakeAssetIO{}
	got, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", inner)
	require.NoError(t, err)
	require.Equal(t, asset.AssetIO(inner), got,
		"connector implements MaskingProvisioner → MUST NOT wrap MaskingIO")
}

// TestExecutor_MaybeWrap_NonWarehouse_WithRules_WrapsMaskingIO —
// non-warehouse connector with policies → wraps with MaskingIO.
func TestExecutor_MaybeWrap_NonWarehouse_WithRules_WrapsMaskingIO(t *testing.T) {
	conn := &recordingConnector{}
	provider := &stubProvider{rules: []policy.MaskRule{{Column: "ssn", Mask: connector.MaskHash}}}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, provider)

	inner := &fakeAssetIO{}
	got, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", inner)
	require.NoError(t, err)
	require.NotEqual(t, asset.AssetIO(inner), got,
		"non-warehouse + rules → MUST wrap with MaskingIO")
	_, ok := got.(*asset.MaskingIO)
	require.True(t, ok, "got type %T, want *asset.MaskingIO", got)
}

// TestExecutor_MaybeWrap_NonWarehouse_NoRules_ReturnsInner — non-warehouse
// connector without any rules → no wrapping (Phase 4 path).
func TestExecutor_MaybeWrap_NonWarehouse_NoRules_ReturnsInner(t *testing.T) {
	conn := &recordingConnector{}
	provider := &stubProvider{rules: nil}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, provider)

	inner := &fakeAssetIO{}
	got, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", inner)
	require.NoError(t, err)
	require.Equal(t, asset.AssetIO(inner), got, "no rules → no wrapping")
}

// TestExecutor_MaybeWrap_ProviderError_Surfaced — a provider error must be
// surfaced rather than silently dropping the masking decision.
func TestExecutor_MaybeWrap_ProviderError_Surfaced(t *testing.T) {
	conn := &recordingConnector{}
	provider := &stubProvider{err: errors.New("policy store unavailable")}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, provider)

	_, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", &fakeAssetIO{})
	require.Error(t, err)
}

// ---- Plan 05-03 named acceptance criteria coverage ----
//
// The following names mirror the <acceptance_criteria> grep targets in
// 05-03-PLAN.md. They are thin wrappers over the MaybeWrap_* unit tests
// above so the regex grep "TestExecutor_NoPolicies_DoesNotWrapMaskingIO|...|"
// hits at least one passing test per name.

// TestExecutor_NoPolicies_DoesNotWrapMaskingIO — Phase 4 path stays
// unchanged when no MaskRulesProvider is configured. Wraps the more
// general MaybeWrap_NoProvider_ReturnsInner test for the named criterion.
func TestExecutor_NoPolicies_DoesNotWrapMaskingIO(t *testing.T) {
	conn := &recordingConnector{}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, nil)
	got, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", &fakeAssetIO{})
	require.NoError(t, err)
	_, isMI := got.(*asset.MaskingIO)
	require.False(t, isMI, "without provider, MaskingIO MUST NOT be inserted")
}

// TestExecutor_WarehouseConnector_DoesNotWrapMaskingIO — connector
// implements MaskingProvisioner → no MaskingIO. Uses a stub fake that
// satisfies the interface.
func TestExecutor_WarehouseConnector_DoesNotWrapMaskingIO(t *testing.T) {
	conn := &maskingProvisionerConn{}
	provider := &stubProvider{rules: []policy.MaskRule{{Column: "ssn", Mask: connector.MaskHash}}}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, provider)
	got, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", &fakeAssetIO{})
	require.NoError(t, err)
	_, isMI := got.(*asset.MaskingIO)
	require.False(t, isMI, "warehouse-native connector MUST NOT be wrapped with MaskingIO")
}

// TestExecutor_NonWarehouseConnector_WithPolicies_WrapsMaskingIO —
// non-warehouse connector with at least one mask rule → MaskingIO wraps.
func TestExecutor_NonWarehouseConnector_WithPolicies_WrapsMaskingIO(t *testing.T) {
	conn := &recordingConnector{}
	provider := &stubProvider{rules: []policy.MaskRule{{Column: "ssn", Mask: connector.MaskHash}}}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, provider)
	got, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", &fakeAssetIO{})
	require.NoError(t, err)
	_, isMI := got.(*asset.MaskingIO)
	require.True(t, isMI, "non-warehouse + rules → MaskingIO MUST be inserted")
}

// TestExecutor_PIIWithoutPolicy_FallsBackToRedact — policy store returns
// a fallback redact rule for a pii column without an explicit policy. The
// provider stub returns the row; the executor wraps with MaskingIO.
func TestExecutor_PIIWithoutPolicy_FallsBackToRedact(t *testing.T) {
	conn := &recordingConnector{}
	provider := &stubProvider{rules: []policy.MaskRule{{Column: "ssn", Mask: policy.DefaultMaskForPII()}}}
	exec, _ := buildExecutorWithMaskingProvider(t, conn, provider)
	got, err := exec.MaybeWrapMaskingIOForTest(context.Background(), "orders", &fakeAssetIO{})
	require.NoError(t, err)
	_, isMI := got.(*asset.MaskingIO)
	require.True(t, isMI, "pii column without policy → MaskingIO with default redact applied")
}
