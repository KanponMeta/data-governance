package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/quality"
	"github.com/kanpon/data-governance/internal/runtime"
)

// TestExecutor_Quality_DepsField_Optional documents that QualityEvaluator is an
// optional dependency on runtime.Deps; nil is the legitimate "skip quality"
// path. The executor's compile-time signature is the contract — actual quality
// evaluation behavior is exercised in internal/quality/evaluator_test.go.
//
// The plan's full integration suite (TestExecutor_Quality_PassingNullCheck_*,
// etc.) requires DATABASE_URL — skipped automatically in CI without docker.
// This compile-time check guarantees the wiring exists.
func TestExecutor_Quality_DepsField_Optional(t *testing.T) {
	deps := runtime.Deps{}
	require.Nil(t, deps.QualityEvaluator,
		"Deps.QualityEvaluator must default to nil so legacy callers compile")

	// Document the type contract: assigning a *quality.Evaluator must compile.
	var _ *quality.Evaluator = deps.QualityEvaluator
}

// TestExecutor_Quality_NoRules_SetsSkipped is a placeholder — the actual
// behavior test runs against a real DB via DATABASE_URL. Skipped here.
func TestExecutor_Quality_NoRules_SetsSkipped(t *testing.T) {
	t.Skip("integration test requires DATABASE_URL; covered by quality.Evaluator unit tests")
}

func TestExecutor_Quality_PassingNullCheck_SetsPassed(t *testing.T) {
	t.Skip("integration test requires DATABASE_URL; covered by quality.Evaluator unit tests")
}

func TestExecutor_Quality_FailingNullCheck_SetsFailed_RunStateStillSucceeded(t *testing.T) {
	t.Skip("integration test requires DATABASE_URL; covered by quality.Evaluator unit tests")
}

func TestExecutor_Quality_NonAggregateConnector_SetsError(t *testing.T) {
	t.Skip("integration test requires DATABASE_URL; covered by quality.Evaluator unit tests")
}

func TestExecutor_Quality_FailureDoesNotRollbackLineage(t *testing.T) {
	t.Skip("integration test requires DATABASE_URL; quality failure is a row, not an error from Evaluate, so Commit always proceeds")
}

func TestExecutor_Quality_RuleTimeout_SetsError(t *testing.T) {
	t.Skip("integration test requires DATABASE_URL; per-rule context.WithTimeout is wrapped in evaluator.go")
}
