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
	io := NewAssetIO(a, resolver, "")

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
	io := NewAssetIO(a, resolver, "")

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

// ---- Phase 4 Builder DSL extensions ----

// TestBuilderColumnLineage exercises the full Phase 4 chain from 04-RESEARCH.md.
func TestBuilderColumnLineage(t *testing.T) {
	a, err := New("orders").
		Connector("postgres-prod").
		Materialize(noopMaterialize).
		Description("Daily orders fact table").
		Owner("team-data@example.com").
		Tags("finance", "pii").
		Column("user_id").Description("FK users.id").Tags("pii").And().
		Column("total").Description("USD cents").And().
		ColumnLineage(ColumnLineageMap{
			"user_id": {{Asset: "payments", Column: "payer_id"}},
		}).
		Build()

	require.NoError(t, err)
	require.NotNil(t, a)
	require.Equal(t, "Daily orders fact table", a.Description())
	require.Equal(t, "team-data@example.com", a.Owner())
	require.Equal(t, []string{"finance", "pii"}, a.Tags())
	cols := a.Columns()
	require.Len(t, cols, 2)
	require.Equal(t, "user_id", cols[0].Name)
	require.Equal(t, "FK users.id", cols[0].Description)
	require.Equal(t, []string{"pii"}, cols[0].Tags)
	require.Equal(t, "total", cols[1].Name)
	require.Equal(t, "USD cents", cols[1].Description)
	cl := a.ColumnLineage()
	require.NotNil(t, cl)
	require.Len(t, cl["user_id"], 1)
	require.Equal(t, "payments", cl["user_id"][0].Asset)
	require.Equal(t, "payer_id", cl["user_id"][0].Column)
}

// TestBuilderColumnLineageEmpty — calling no Phase 4 methods leaves all zero.
func TestBuilderColumnLineageEmpty(t *testing.T) {
	a, err := New("bare").
		Connector("c").
		Materialize(noopMaterialize).
		Build()
	require.NoError(t, err)
	require.Equal(t, "", a.Description())
	require.Equal(t, "", a.Owner())
	require.Nil(t, a.Tags())
	require.Nil(t, a.Columns())
	require.Nil(t, a.ColumnLineage())
}

// TestBuilderColumnLineageDefensiveCopy — mutating the slice passed to Tags does not affect the asset.
func TestBuilderColumnLineageDefensiveCopy(t *testing.T) {
	tags := []string{"finance", "pii"}
	a, err := New("copy_test").
		Connector("c").
		Materialize(noopMaterialize).
		Tags(tags...).
		Build()
	require.NoError(t, err)
	// Mutate original slice
	tags[0] = "mutated"
	// Asset's tags must be unchanged
	require.Equal(t, "finance", a.Tags()[0])
}

// TestMaterializeResultBackwardCompat — existing zero-value construction works.
func TestMaterializeResultBackwardCompat(t *testing.T) {
	r := MaterializeResult{
		RowsWritten: 42,
		Metadata:    map[string]any{"x": 1},
	}
	require.Equal(t, int64(42), r.RowsWritten)
	require.Equal(t, 1, r.Metadata["x"])
	require.Nil(t, r.ColumnLineage)
	require.Nil(t, r.Schema)
}

// TestBuilderPhase23Unchanged — Phase 2/3 chain still works correctly.
func TestBuilderPhase23Unchanged(t *testing.T) {
	t.Cleanup(resetForTest)

	a, err := New("phase23_compat").
		Upstream("upstream_a").
		Connector("pg").
		Materialize(noopMaterialize).
		Schedule("@daily").
		Build()
	require.NoError(t, err)
	require.Equal(t, "phase23_compat", a.Name())
	require.Equal(t, []string{"upstream_a"}, a.Upstreams())
	require.Equal(t, "pg", a.ConnectorName())
	require.NotNil(t, a.Schedule())
	require.Equal(t, "@daily", a.Schedule().CronExpr)
}

// TestBuilderColumnLineageMapDefensiveCopy — mutating the ColumnLineageMap after
// passing to ColumnLineage() does not affect the asset's stored value.
func TestBuilderColumnLineageMapDefensiveCopy(t *testing.T) {
	cl := ColumnLineageMap{
		"out_col": {{Asset: "src", Column: "in_col"}},
	}
	a, err := New("defensecopy").
		Connector("c").
		Materialize(noopMaterialize).
		ColumnLineage(cl).
		Build()
	require.NoError(t, err)
	// Mutate original map
	cl["out_col"][0] = ColumnRef{Asset: "evil", Column: "bad"}
	// Asset must be unchanged
	stored := a.ColumnLineage()
	require.Equal(t, "src", stored["out_col"][0].Asset)
	require.Equal(t, "in_col", stored["out_col"][0].Column)
}

// ---- Phase 5 (D-02 / D-04 / RBAC-03) — ColumnPolicy builder tests ----

// TestBuilder_ColumnPolicy_Chainable verifies multiple ColumnPolicy calls
// accumulate onto Asset.ColumnPolicies and survive Build().
func TestBuilder_ColumnPolicy_Chainable(t *testing.T) {
	a, err := New("orders_chain").
		Connector("snowflake").
		Materialize(noopMaterialize).
		ColumnPolicy(ColumnPolicy{Column: "ssn", Mask: MaskHash, AllowRoles: []string{"pii-analyst"}}).
		ColumnPolicy(ColumnPolicy{Column: "email", Mask: MaskPartial, PartialReveal: 3}).
		Build()
	require.NoError(t, err)
	cps := a.ColumnPolicies()
	require.Len(t, cps, 2)
	require.Equal(t, "ssn", cps[0].Column)
	require.Equal(t, MaskHash, cps[0].Mask)
	require.Equal(t, []string{"pii-analyst"}, cps[0].AllowRoles)
	require.Equal(t, "email", cps[1].Column)
	require.Equal(t, MaskPartial, cps[1].Mask)
	require.Equal(t, 3, cps[1].PartialReveal)
}

// TestBuilder_ColumnPolicy_DuplicateColumnFails verifies that declaring the
// same Column twice causes Build() to return ErrColumnPolicyDuplicateColumn.
func TestBuilder_ColumnPolicy_DuplicateColumnFails(t *testing.T) {
	_, err := New("orders_dup").
		Connector("snowflake").
		Materialize(noopMaterialize).
		ColumnPolicy(ColumnPolicy{Column: "ssn", Mask: MaskHash}).
		ColumnPolicy(ColumnPolicy{Column: "ssn", Mask: MaskRedact}).
		Build()
	require.Error(t, err)
	require.ErrorIs(t, err, ErrColumnPolicyDuplicateColumn)
}

// TestBuilder_ColumnPolicy_AffectsCodeHash verifies the ColumnPolicy slice
// participates in code_hash so a builder mask change forces a new
// asset_versions row (Phase 5 D-02). Reordering AllowRoles or ColumnPolicy
// declarations must NOT change the hash (canonical sort).
func TestBuilder_ColumnPolicy_AffectsCodeHash(t *testing.T) {
	plain, err := New("orders_h1").
		Connector("snowflake").
		Materialize(noopMaterialize).
		Build()
	require.NoError(t, err)
	masked, err := New("orders_h1").
		Connector("snowflake").
		Materialize(noopMaterialize).
		ColumnPolicy(ColumnPolicy{Column: "ssn", Mask: MaskHash, AllowRoles: []string{"pii-analyst"}}).
		Build()
	require.NoError(t, err)
	require.NotEqual(t, plain.CodeHash(), masked.CodeHash(),
		"adding a ColumnPolicy must change code_hash (D-02)")

	// Reordering AllowRoles must not change the hash (canonical sort).
	roleA, err := New("orders_h1").
		Connector("snowflake").
		Materialize(noopMaterialize).
		ColumnPolicy(ColumnPolicy{Column: "ssn", Mask: MaskHash, AllowRoles: []string{"a", "b"}}).
		Build()
	require.NoError(t, err)
	roleB, err := New("orders_h1").
		Connector("snowflake").
		Materialize(noopMaterialize).
		ColumnPolicy(ColumnPolicy{Column: "ssn", Mask: MaskHash, AllowRoles: []string{"b", "a"}}).
		Build()
	require.NoError(t, err)
	require.Equal(t, roleA.CodeHash(), roleB.CodeHash(),
		"AllowRoles ordering must not affect the hash (canonical sort)")
}

// TestBuilder_ColumnPolicy_InvalidMask verifies that an invalid Mask surfaces
// as ErrColumnPolicyInvalidMask at Build() time.
func TestBuilder_ColumnPolicy_InvalidMask(t *testing.T) {
	_, err := New("orders_invalidmask").
		Connector("snowflake").
		Materialize(noopMaterialize).
		ColumnPolicy(ColumnPolicy{Column: "ssn", Mask: "blowfish"}).
		Build()
	require.Error(t, err)
	require.ErrorIs(t, err, ErrColumnPolicyInvalidMask)
}

// TestBuilder_ColumnPolicy_MissingColumn verifies that an empty Column surfaces
// as ErrColumnPolicyMissingColumn at Build() time.
func TestBuilder_ColumnPolicy_MissingColumn(t *testing.T) {
	_, err := New("orders_emptycol").
		Connector("snowflake").
		Materialize(noopMaterialize).
		ColumnPolicy(ColumnPolicy{Column: "", Mask: MaskHash}).
		Build()
	require.Error(t, err)
	require.ErrorIs(t, err, ErrColumnPolicyMissingColumn)
}

// ---- Phase 5 Plan 05-04 — governance routing DSL tests ----

// TestBuilder_Reviewers_Accumulate verifies multiple Reviewers calls append.
func TestBuilder_Reviewers_Accumulate(t *testing.T) {
	a, err := New("orders_rev").
		Connector("snowflake").
		Materialize(noopMaterialize).
		Reviewers("team-data-gov").
		Reviewers("privacy-team", "compliance").
		Build()
	require.NoError(t, err)
	require.Equal(t, []string{"team-data-gov", "privacy-team", "compliance"}, a.ReviewerRoles())
}

// TestBuilder_Quorum_DefaultIs1 verifies Quorum default + custom.
// Note: Builder zero value is Quorum(0) — workflow treats this as Quorum1.
func TestBuilder_Quorum_DefaultIs1(t *testing.T) {
	def, err := New("a").Connector("c").Materialize(noopMaterialize).Build()
	require.NoError(t, err)
	require.Equal(t, Quorum(0), def.Quorum(),
		"Quorum() returns 0 when not set; workflow normalises 0→Quorum1")

	q2, err := New("b").Connector("c").Materialize(noopMaterialize).Quorum(Quorum2).Build()
	require.NoError(t, err)
	require.Equal(t, Quorum2, q2.Quorum())

	all, err := New("c").Connector("c").Materialize(noopMaterialize).Quorum(QuorumAll).Build()
	require.NoError(t, err)
	require.Equal(t, QuorumAll, all.Quorum())
}

// TestBuilder_RequireHumanReview_Toggles verifies the toggle method.
func TestBuilder_RequireHumanReview_Toggles(t *testing.T) {
	off, err := New("a").Connector("c").Materialize(noopMaterialize).Build()
	require.NoError(t, err)
	require.False(t, off.RequireHumanReview())

	on, err := New("b").Connector("c").Materialize(noopMaterialize).RequireHumanReview().Build()
	require.NoError(t, err)
	require.True(t, on.RequireHumanReview())
}

// TestBuilder_EscalationRoles_Accumulate verifies multiple EscalationRoles append.
func TestBuilder_EscalationRoles_Accumulate(t *testing.T) {
	a, err := New("a").
		Connector("c").
		Materialize(noopMaterialize).
		EscalationRoles("director").
		EscalationRoles("cto", "ciso").
		Build()
	require.NoError(t, err)
	require.Equal(t, []string{"director", "cto", "ciso"}, a.EscalationRoles())
}

// TestBuilder_GovernanceConfig_NotInCodeHash verifies that all four governance
// routing knobs are excluded from code_hash. Two assets identical except for
// reviewer pool / quorum / RequireHumanReview / escalation roles must have
// identical code_hash so a routing change does NOT reseat the asset version.
func TestBuilder_GovernanceConfig_NotInCodeHash(t *testing.T) {
	plain, err := New("orders_gov").
		Connector("snowflake").
		Materialize(noopMaterialize).
		Build()
	require.NoError(t, err)

	configured, err := New("orders_gov").
		Connector("snowflake").
		Materialize(noopMaterialize).
		Reviewers("team-data-gov", "privacy-team").
		Quorum(Quorum2).
		RequireHumanReview().
		EscalationRoles("director", "cto").
		Build()
	require.NoError(t, err)

	require.Equal(t, plain.CodeHash(), configured.CodeHash(),
		"governance routing config (Reviewers/Quorum/RequireHumanReview/EscalationRoles) must NOT change code_hash")
	require.NotEmpty(t, plain.CodeHash(), "code_hash must be populated by Build()")
}
