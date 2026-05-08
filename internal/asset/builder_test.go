package asset

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/partition"
)

// ---- Helpers ----

// resolverFunc is a ConnectorResolver that delegates to a function.
type resolverFunc func(assetName string) (connector.Connector, connector.AssetRef, error)

func (f resolverFunc) Resolve(assetName string) (connector.Connector, connector.AssetRef, error) {
	return f(assetName)
}

// fakeConnectorForBuilder satisfies connector.Connector for builder / io tests.
type fakeConnectorForBuilder struct{}

func (f *fakeConnectorForBuilder) APIVersion() string { return connector.APIVersion }
func (f *fakeConnectorForBuilder) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{}, nil
}
func (f *fakeConnectorForBuilder) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (f *fakeConnectorForBuilder) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{Rows: []connector.Row{{Fields: map[string]any{"id": 1}}}}, nil
}
func (f *fakeConnectorForBuilder) Write(_ context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{RowsWritten: int64(len(req.Rows))}, nil
}

// ---- Test 1: Full builder chain registers successfully ----

func TestBuilder_FullChain_Register(t *testing.T) {
	t.Cleanup(resetForTest)

	err := New("users_clean").
		Upstream("users_raw").
		Connector("postgres-prod").
		Materialize(noopMaterialize).
		Register()

	require.NoError(t, err)

	got, err := Default().Get("users_clean")
	require.NoError(t, err)
	require.Equal(t, "users_clean", got.Name())
	require.Equal(t, []string{"users_raw"}, got.Upstreams())
	require.Equal(t, "postgres-prod", got.ConnectorName())
}

// ---- Test 2: Variadic Upstream ----

func TestBuilder_Upstream_Variadic(t *testing.T) {
	t.Cleanup(resetForTest)

	a, err := New("a").
		Upstream("b", "c", "d").
		Connector("c1").
		Materialize(noopMaterialize).
		Build()

	require.NoError(t, err)
	require.Equal(t, []string{"b", "c", "d"}, a.Upstreams())
}

// ---- Test 3: Method chaining is order-independent ----

func TestBuilder_Chain_OrderIndependent(t *testing.T) {
	t.Cleanup(resetForTest)

	policy := RetryPolicy{Max: 3, InitialDelay: time.Second}

	err := New("a").
		Materialize(noopMaterialize).
		Upstream("b").
		Connector("c").
		Retry(policy).
		Resource("r1", 2).
		Register()

	require.NoError(t, err)

	got, err := Default().Get("a")
	require.NoError(t, err)
	require.Equal(t, policy, got.RetryPolicy())
	require.Equal(t, []Resource{{Name: "r1", Weight: 2}}, got.Resources())
}

// ---- Test 4: Resource weight 0 defaults to 1 ----

func TestBuilder_Resource_ZeroWeightDefaultsToOne(t *testing.T) {
	t.Cleanup(resetForTest)

	a, err := New("x").
		Connector("c").
		Materialize(noopMaterialize).
		Resource("postgres-prod", 0).
		Build()

	require.NoError(t, err)
	require.Equal(t, 1, a.Resources()[0].Weight)
}

// ---- Test 5: Missing Materialize returns ErrMissingMaterialize ----

func TestBuilder_MissingMaterialize_Error(t *testing.T) {
	t.Cleanup(resetForTest)

	err := New("x").Connector("c").Register()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMissingMaterialize), "expected ErrMissingMaterialize, got: %v", err)
}

// ---- Test 6: Missing Connector returns ErrMissingConnector ----

func TestBuilder_MissingConnector_Error(t *testing.T) {
	t.Cleanup(resetForTest)

	err := New("x").Materialize(noopMaterialize).Register()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrMissingConnector), "expected ErrMissingConnector, got: %v", err)
}

// ---- Test 7: AssetIO interface — Read checks declared upstreams ----

func TestAssetIO_Read_UndeclaredUpstream_Rejected(t *testing.T) {
	a, err := New("clean").Connector("pg").Materialize(noopMaterialize).Upstream("raw").Build()
	require.NoError(t, err)

	resolver := resolverFunc(func(name string) (connector.Connector, connector.AssetRef, error) {
		return &fakeConnectorForBuilder{}, connector.AssetRef{Identifier: name}, nil
	})
	io := NewAssetIO(a, resolver)

	// Declared upstream — should succeed
	rows, readErr := io.Read(context.Background(), "raw")
	require.NoError(t, readErr)
	require.Len(t, rows, 1)

	// Undeclared upstream — should fail with ErrUnknownUpstream
	_, readErr = io.Read(context.Background(), "sneaky")
	require.Error(t, readErr)
	require.True(t, errors.Is(readErr, ErrUnknownUpstream), "expected ErrUnknownUpstream, got: %v", readErr)
}

func TestAssetIO_Write_DelegatesToConnector(t *testing.T) {
	a, err := New("clean").Connector("pg").Materialize(noopMaterialize).Build()
	require.NoError(t, err)

	resolver := resolverFunc(func(name string) (connector.Connector, connector.AssetRef, error) {
		return &fakeConnectorForBuilder{}, connector.AssetRef{Identifier: name}, nil
	})
	io := NewAssetIO(a, resolver)

	rows := []connector.Row{{Fields: map[string]any{"x": 1}}, {Fields: map[string]any{"x": 2}}}
	written, err := io.Write(context.Background(), rows)
	require.NoError(t, err)
	require.Equal(t, int64(2), written)
}

// ---- Tests 9-11: Build() ----

func TestBuilder_Build_ReturnsAssetWithoutRegistering(t *testing.T) {
	t.Cleanup(resetForTest)

	a, err := New("isolated").
		Connector("c").
		Materialize(noopMaterialize).
		Build()

	require.NoError(t, err)
	require.NotNil(t, a)
	require.Equal(t, "isolated", a.Name())

	// Crucially: Build() must NOT register into the global Default() registry
	_, getErr := Default().Get("isolated")
	require.True(t, errors.Is(getErr, ErrNotFound), "Build() must not register in Default(), got: %v", getErr)
}

func TestBuilder_Build_ValidationErrors(t *testing.T) {
	t.Cleanup(resetForTest)

	// Empty name
	a, err := New("").Connector("c").Materialize(noopMaterialize).Build()
	require.Nil(t, a)
	require.True(t, errors.Is(err, ErrEmptyName), "expected ErrEmptyName for empty name, got: %v", err)

	// Missing Materialize
	a, err = New("x").Connector("c").Build()
	require.Nil(t, a)
	require.True(t, errors.Is(err, ErrMissingMaterialize), "expected ErrMissingMaterialize, got: %v", err)

	// Missing Connector
	a, err = New("x").Materialize(noopMaterialize).Build()
	require.Nil(t, a)
	require.True(t, errors.Is(err, ErrMissingConnector), "expected ErrMissingConnector, got: %v", err)
}

func TestBuilder_Build_AndRegister_AreEquivalent(t *testing.T) {
	t.Cleanup(resetForTest)

	policy := RetryPolicy{Max: 2, InitialDelay: time.Second}
	fnA := noopMaterialize

	// Build path
	built, err := New("eq_test").
		Upstream("upstream1").
		Connector("pg").
		Materialize(fnA).
		Retry(policy).
		Resource("r1", 3).
		Build()
	require.NoError(t, err)

	// Register path (fresh name so no collision)
	err = New("eq_test_reg").
		Upstream("upstream1").
		Connector("pg").
		Materialize(fnA).
		Retry(policy).
		Resource("r1", 3).
		Register()
	require.NoError(t, err)

	registered, err := Default().Get("eq_test_reg")
	require.NoError(t, err)

	// Compare field by field (excluding name since they're different)
	require.Equal(t, built.Upstreams(), registered.Upstreams())
	require.Equal(t, built.ConnectorName(), registered.ConnectorName())
	require.Equal(t, built.RetryPolicy(), registered.RetryPolicy())
	require.Equal(t, built.Resources(), registered.Resources())
}

// ---- Register called twice with same name returns ErrAlreadyRegistered ----

func TestBuilder_Register_Duplicate_Error(t *testing.T) {
	t.Cleanup(resetForTest)

	err1 := New("dup").Connector("c").Materialize(noopMaterialize).Register()
	require.NoError(t, err1)

	err2 := New("dup").Connector("c").Materialize(noopMaterialize).Register()
	require.True(t, errors.Is(err2, ErrAlreadyRegistered), "expected ErrAlreadyRegistered, got: %v", err2)
}

// ---- Phase 3 — Schedule / Sensor / Partitions DSL extensions ----
// Cover D-03 (cron parser-only fail-fast), D-06 (SensorSpec types), D-09 (.Partitions),
// D-11 (UTC keys), D-12 (orthogonal composition).

func noopSense(ctx context.Context) (SensorResult, error) { return SensorResult{Fired: false}, nil }

// TestScheduleAccepted — valid cron expression accepted; ScheduleSpec readable via Asset.Schedule().
func TestScheduleAccepted(t *testing.T) {
	a, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Schedule("0 0 * * *").
		Build()
	require.NoError(t, err)
	require.NotNil(t, a.Schedule())
	require.Equal(t, "0 0 * * *", a.Schedule().CronExpr)
}

// TestScheduleInvalidCron — invalid expression rejected at Build() (Pitfall 1, D-03).
// Satisfies VALIDATION.md TestScheduleInvalidCron requirement.
func TestScheduleInvalidCron(t *testing.T) {
	_, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Schedule("not a valid cron").
		Build()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrInvalidCron),
		"expected ErrInvalidCron, got: %v", err)
}

// TestScheduleEvery — descriptor-form expressions (@every 30s) accepted.
func TestScheduleEvery(t *testing.T) {
	a, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Schedule("@every 30s").
		Build()
	require.NoError(t, err)
	require.Equal(t, "@every 30s", a.Schedule().CronExpr)
}

// TestSensorAccepted — single SensorSpec accepted; Asset.Sensors() returns slice of len 1.
func TestSensorAccepted(t *testing.T) {
	a, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Sensor(SensorSpec{Name: "s1", MinInterval: 30 * time.Second, Sense: noopSense}).
		Build()
	require.NoError(t, err)
	require.Len(t, a.Sensors(), 1)
	require.Equal(t, "s1", a.Sensors()[0].Name)
}

// TestSensorEmptyName — sensor with empty Name rejected (D-06).
func TestSensorEmptyName(t *testing.T) {
	_, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Sensor(SensorSpec{Sense: noopSense}).
		Build()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSensorNameRequired),
		"expected ErrSensorNameRequired, got: %v", err)
}

// TestSensorNilSense — sensor with nil Sense rejected (D-06).
func TestSensorNilSense(t *testing.T) {
	_, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Sensor(SensorSpec{Name: "s1"}).
		Build()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSensorFuncRequired),
		"expected ErrSensorFuncRequired, got: %v", err)
}

// TestSensorNegativeMinInterval — negative MinInterval rejected (T-03-02-03 mitigation).
func TestSensorNegativeMinInterval(t *testing.T) {
	_, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Sensor(SensorSpec{Name: "s1", MinInterval: -1 * time.Second, Sense: noopSense}).
		Build()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSensorMinIntervalNegative),
		"expected ErrSensorMinIntervalNegative, got: %v", err)
}

// TestPartitionsDailyAccepted — DailyPartitions strategy accepted; Asset.Partitions() returns it.
func TestPartitionsDailyAccepted(t *testing.T) {
	a, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Partitions(partition.DailyPartitions{}).
		Build()
	require.NoError(t, err)
	require.NotNil(t, a.Partitions())
	require.Equal(t, "daily", a.Partitions().Kind())
}

// TestPartitionsCategoryInvalidKey — Category key with '/' rejected at Build (Pitfall 4).
func TestPartitionsCategoryInvalidKey(t *testing.T) {
	_, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Partitions(partition.CategoryPartitions{Keys: []string{"us/east"}}).
		Build()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPartitionInvalidKey),
		"expected ErrPartitionInvalidKey, got: %v", err)
}

// TestPartitionsCategoryOversizeKey — Category key >128 chars rejected.
func TestPartitionsCategoryOversizeKey(t *testing.T) {
	_, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Partitions(partition.CategoryPartitions{Keys: []string{strings.Repeat("x", 129)}}).
		Build()
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrPartitionInvalidKey),
		"expected ErrPartitionInvalidKey for oversize, got: %v", err)
}

// TestPartitionsLastWins — successive .Partitions() calls overwrite (last wins, no error).
func TestPartitionsLastWins(t *testing.T) {
	a, err := New("foo").
		Connector("c").
		Materialize(noopMaterialize).
		Partitions(partition.DailyPartitions{}).
		Partitions(partition.WeeklyPartitions{}).
		Build()
	require.NoError(t, err)
	require.Equal(t, "weekly", a.Partitions().Kind())
}

// TestOrthogonalComposition — D-12: method order is irrelevant. Building with two
// different chain orders must yield Assets with equivalent Schedule/Sensors/Partitions.
func TestOrthogonalComposition(t *testing.T) {
	t.Cleanup(resetForTest)

	spec := SensorSpec{Name: "s1", MinInterval: 30 * time.Second, Sense: noopSense}

	a1, err := New("a1").
		Connector("c").
		Materialize(noopMaterialize).
		Schedule("0 0 * * *").
		Sensor(spec).
		Partitions(partition.DailyPartitions{}).
		Build()
	require.NoError(t, err)

	a2, err := New("a2").
		Connector("c").
		Materialize(noopMaterialize).
		Partitions(partition.DailyPartitions{}).
		Sensor(spec).
		Schedule("0 0 * * *").
		Build()
	require.NoError(t, err)

	require.Equal(t, a1.Schedule().CronExpr, a2.Schedule().CronExpr)
	require.Equal(t, len(a1.Sensors()), len(a2.Sensors()))
	require.Equal(t, a1.Sensors()[0].Name, a2.Sensors()[0].Name)
	require.Equal(t, a1.Partitions().Kind(), a2.Partitions().Kind())
}
