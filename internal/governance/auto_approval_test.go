package governance

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
)

// helper to set up an AutoApprovalChecker and a sqlmock with the standard
// "asset has neither schema_changes nor PII columns nor quality_rules nor
// drift_pending" baseline. Per-test overrides can adjust before calling.
func newCheckerHappyPath(t *testing.T) (*AutoApprovalChecker, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)

	checker := NewAutoApprovalChecker(db)
	cleanup := func() { _ = db.Close() }
	return checker, mock, cleanup
}

// expectAllPassQueries seeds the 4 baseline queries to clean state.
func expectAllPassQueries(mock sqlmock.Sqlmock, asset, codeHash string) {
	// 1. schema_changes — no rows
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM schema_changes`).
		WithArgs(asset).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// 2. PII columns — no rows
	mock.ExpectQuery(`SELECT DISTINCT column_name FROM asset_metadata`).
		WithArgs(asset).
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
	// 3. quality_rules — no rows
	mock.ExpectQuery(`SELECT rule_type, config_json FROM quality_rules`).
		WithArgs(asset, codeHash).
		WillReturnRows(sqlmock.NewRows([]string{"rule_type", "config_json"}))
	// 4. drift_status — clean
	mock.ExpectQuery(`SELECT drift_status FROM asset_versions`).
		WithArgs(asset, codeHash).
		WillReturnRows(sqlmock.NewRows([]string{"drift_status"}).AddRow("clean"))
}

// ---- All checks pass → DecisionAutoApproved ----

func TestAutoApproval_AllPass_Approves(t *testing.T) {
	checker, mock, cleanup := newCheckerHappyPath(t)
	defer cleanup()

	a, err := asset.New("orders").Connector("c").Materialize(noop).Build()
	require.NoError(t, err)
	expectAllPassQueries(mock, a.Name(), a.CodeHash())

	res, err := checker.Check(context.Background(), a, a.CodeHash(), "owner@example.com", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionAutoApproved, res.Decision)
	require.Empty(t, res.FailedChecks)
}

// ---- 1: unack breaking schema change ----

func TestAutoApproval_UnacknowledgedSchemaBreak_Blocks(t *testing.T) {
	checker, mock, cleanup := newCheckerHappyPath(t)
	defer cleanup()

	a, err := asset.New("orders").Connector("c").Materialize(noop).Build()
	require.NoError(t, err)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM schema_changes`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery(`SELECT DISTINCT column_name FROM asset_metadata`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
	mock.ExpectQuery(`SELECT rule_type, config_json FROM quality_rules`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"rule_type", "config_json"}))
	mock.ExpectQuery(`SELECT drift_status FROM asset_versions`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"drift_status"}).AddRow("clean"))

	res, err := checker.Check(context.Background(), a, a.CodeHash(), "owner@example.com", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionBlocked, res.Decision)
	require.Contains(t, res.FailedChecks, "schema_break_ack")
}

// ---- 2: PII column without policy ----

func TestAutoApproval_PIIWithoutPolicy_Blocks(t *testing.T) {
	checker, mock, cleanup := newCheckerHappyPath(t)
	defer cleanup()

	a, err := asset.New("orders").Connector("c").Materialize(noop).Build()
	require.NoError(t, err)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM schema_changes`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	// PII column "ssn" exists
	mock.ExpectQuery(`SELECT DISTINCT column_name FROM asset_metadata`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("ssn"))
	// No column_policies row → blocks
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs(a.Name(), "ssn").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`SELECT rule_type, config_json FROM quality_rules`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"rule_type", "config_json"}))
	mock.ExpectQuery(`SELECT drift_status FROM asset_versions`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"drift_status"}).AddRow("clean"))

	res, err := checker.Check(context.Background(), a, a.CodeHash(), "owner@example.com", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionBlocked, res.Decision)
	require.Contains(t, res.FailedChecks, "pii_policy_consistency")
}

// ---- 3: quality_rules references missing column ----

func TestAutoApproval_BrokenQualityRule_Blocks(t *testing.T) {
	checker, mock, cleanup := newCheckerHappyPath(t)
	defer cleanup()

	a, err := asset.New("orders").Connector("c").Materialize(noop).Build()
	require.NoError(t, err)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM schema_changes`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT column_name FROM asset_metadata`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
	// quality rule references column "missing"
	mock.ExpectQuery(`SELECT rule_type, config_json FROM quality_rules`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"rule_type", "config_json"}).
			AddRow("null_check", []byte(`{"column":"missing","max_rate":0.01}`)))
	// schema_versions has column "id" only
	mock.ExpectQuery(`SELECT columns FROM schema_versions`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"columns"}).
			AddRow([]byte(`[{"name":"id"}]`)))
	mock.ExpectQuery(`SELECT drift_status FROM asset_versions`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"drift_status"}).AddRow("clean"))

	res, err := checker.Check(context.Background(), a, a.CodeHash(), "owner@example.com", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionBlocked, res.Decision)
	require.Contains(t, res.FailedChecks, "quality_config_sanity")
}

// ---- 4: lineage drift pending ----

func TestAutoApproval_LineageDriftPending_Blocks(t *testing.T) {
	checker, mock, cleanup := newCheckerHappyPath(t)
	defer cleanup()

	a, err := asset.New("orders").Connector("c").Materialize(noop).Build()
	require.NoError(t, err)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM schema_changes`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT column_name FROM asset_metadata`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}))
	mock.ExpectQuery(`SELECT rule_type, config_json FROM quality_rules`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"rule_type", "config_json"}))
	mock.ExpectQuery(`SELECT drift_status FROM asset_versions`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"drift_status"}).AddRow("pending"))

	res, err := checker.Check(context.Background(), a, a.CodeHash(), "owner@example.com", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionBlocked, res.Decision)
	require.Contains(t, res.FailedChecks, "lineage_drift")
}

// ---- 5: PII present (with policy) → MustHumanReview ----

func TestAutoApproval_PIIPresent_RequiresHuman(t *testing.T) {
	checker, mock, cleanup := newCheckerHappyPath(t)
	defer cleanup()

	a, err := asset.New("orders").Connector("c").Materialize(noop).Build()
	require.NoError(t, err)

	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM schema_changes`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT DISTINCT column_name FROM asset_metadata`).
		WithArgs(a.Name()).
		WillReturnRows(sqlmock.NewRows([]string{"column_name"}).AddRow("ssn"))
	mock.ExpectQuery(`SELECT EXISTS`).
		WithArgs(a.Name(), "ssn").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`SELECT rule_type, config_json FROM quality_rules`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"rule_type", "config_json"}))
	mock.ExpectQuery(`SELECT drift_status FROM asset_versions`).
		WithArgs(a.Name(), a.CodeHash()).
		WillReturnRows(sqlmock.NewRows([]string{"drift_status"}).AddRow("clean"))

	res, err := checker.Check(context.Background(), a, a.CodeHash(), "owner@example.com", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionMustHumanReview, res.Decision)
	require.Contains(t, res.FailedChecks, "pii_presence")
}

// ---- RequireHumanReview() forces human path even when all checks pass ----

func TestAutoApproval_RequireHumanReview_ForcesHuman_EvenWhenAllPass(t *testing.T) {
	checker, mock, cleanup := newCheckerHappyPath(t)
	defer cleanup()

	a, err := asset.New("orders").
		Connector("c").
		Materialize(noop).
		RequireHumanReview().
		Build()
	require.NoError(t, err)
	expectAllPassQueries(mock, a.Name(), a.CodeHash())

	res, err := checker.Check(context.Background(), a, a.CodeHash(), "owner@example.com", nil)
	require.NoError(t, err)
	require.Equal(t, DecisionMustHumanReview, res.Decision)
	require.Contains(t, res.FailedChecks, "require_human_review")
}
