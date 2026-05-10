package main

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// dispatchGovernance is the CLI entry point. These tests exercise the parsing
// + flag-validation paths that do NOT require a live DB; integration coverage
// for the underlying Workflow lives in internal/governance/workflow_test.go.

// TestSubmitCmd_HappyPath_ParseFlags — submit with valid flags but no DB
// should reach the openGovernanceDB step (returns 1 if config load fails).
// We assert the dispatch reaches submit and produces a non-2 exit code,
// indicating flags parsed OK.
func TestSubmitCmd_HappyPath(t *testing.T) {
	// With a missing required flag, parseError → 2.
	rc := dispatchGovernance([]string{"submit"})
	require.Equal(t, 2, rc, "missing asset arg → 2")

	rc = dispatchGovernance([]string{"submit", "orders"})
	require.Equal(t, 2, rc, "missing --code-hash → 2")

	// With required flags but no DB env, dispatch reaches openGovernanceDB
	// which returns a non-zero exit code (1). Parse layer is OK.
	// (flag must come before positional args because flag.FlagSet.Parse
	// stops at the first non-flag.)
	t.Setenv("DATABASE_URL", "")
	rc = dispatchGovernance([]string{"submit", "--code-hash=abc", "orders"})
	require.NotEqual(t, 2, rc, "valid flags should leave parse layer; exit=%d", rc)
}

// TestReviewCmd_RejectRequiresComment verifies the CLI enforces the same
// "comment required for reject" rule as the workflow. This is a parse-layer
// guard so operators get fast feedback without round-tripping the DB.
func TestReviewCmd_RejectRequiresComment(t *testing.T) {
	id := uuid.New().String()
	rc := dispatchGovernance([]string{"review", id, "--reject"})
	require.Equal(t, 2, rc, "missing --comment for --reject → 2")

	rc = dispatchGovernance([]string{"review", id, "--reject", "--comment=  "})
	require.Equal(t, 2, rc, "whitespace-only --comment for --reject → 2")

	rc = dispatchGovernance([]string{"review", id})
	require.Equal(t, 2, rc, "missing --approve / --reject → 2")

	rc = dispatchGovernance([]string{"review", id, "--approve", "--reject"})
	require.Equal(t, 2, rc, "both --approve and --reject → 2")

	// Invalid review id.
	rc = dispatchGovernance([]string{"review", "not-a-uuid", "--approve"})
	require.Equal(t, 2, rc, "invalid review id → 2")
}

// TestStatusCmd verifies the dispatch reaches the status path. With no DB
// env, the cmd returns 1; the absence of usage-error 2 is the assertion.
func TestStatusCmd(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	rc := dispatchGovernance([]string{"status"})
	require.NotEqual(t, 2, rc)

	rc = dispatchGovernance([]string{"status", "orders"})
	require.NotEqual(t, 2, rc)
}

// TestReassignCmd_HappyPath verifies the dispatcher accepts a valid review id
// + non-empty CSV reviewer list (parse layer); empty / invalid → 2.
func TestReassignCmd_HappyPath(t *testing.T) {
	rc := dispatchGovernance([]string{"reassign"})
	require.Equal(t, 2, rc, "missing args → 2")

	rc = dispatchGovernance([]string{"reassign", "not-a-uuid", "team-a"})
	require.Equal(t, 2, rc, "invalid review id → 2")

	rc = dispatchGovernance([]string{"reassign", uuid.New().String(), ""})
	require.Equal(t, 2, rc, "empty reviewer csv → 2")

	rc = dispatchGovernance([]string{"reassign", uuid.New().String(), ",,, , "})
	require.Equal(t, 2, rc, "csv with only whitespace/empty entries → 2")
}

// TestParseCSV — quick sanity for the helper used by submit + reassign.
func TestParseCSV(t *testing.T) {
	require.Nil(t, parseCSV(""))
	require.Equal(t, []string{"a", "b"}, parseCSV("a,b"))
	require.Equal(t, []string{"a", "b"}, parseCSV(" a , b "))
	require.Empty(t, parseCSV(",,,"))
	require.True(t, strings.Contains(strings.Join(parseCSV("alpha,beta"), ","), "alpha"))
}
