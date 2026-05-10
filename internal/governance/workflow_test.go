package governance_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/governance"
	"github.com/kanpon/data-governance/internal/governance/testharness"
)

// ===== Test scaffolding =====

func noop(_ context.Context, _ asset.AssetIO) (asset.MaterializeResult, error) {
	return asset.MaterializeResult{}, nil
}

// setupWorkflow returns a Workflow + cleanup. Skips the test if the
// testharness postgres container can't be started (environment-restricted).
func setupWorkflow(t *testing.T) (*governance.Workflow, *sql.DB, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("workflow integration tests require Postgres testcontainer; -short skipped")
	}
	db, cleanup := testharness.NewTestPostgres(t)
	resolver := governance.NewResolver(db, nil)
	checker := governance.NewAutoApprovalChecker(db)
	w := governance.NewWorkflow(db, resolver, checker, nil)
	return w, db, cleanup
}

// seedUser inserts a test user and returns its ID.
func seedUser(t *testing.T, db *sql.DB, email string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO "user" (id, email, password_hash, status, created_at)
		VALUES ($1, $2, 'test', 'active', NOW())
	`, id, email)
	require.NoError(t, err)
	return id
}

// seedAssetVersion writes an asset_versions row in 'draft' state.
func seedAssetVersion(t *testing.T, db *sql.DB, assetName, codeHash string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO asset_versions (id, asset, code_hash, drift_status, governance_state, created_at)
		VALUES ($1, $2, $3, 'clean', 'draft', NOW())
	`, id, assetName, codeHash)
	require.NoError(t, err)
	return id
}

// readGovernanceState returns the current asset_versions.governance_state.
func readGovernanceState(t *testing.T, db *sql.DB, assetName, codeHash string) string {
	t.Helper()
	var state string
	err := db.QueryRowContext(context.Background(),
		`SELECT governance_state FROM asset_versions WHERE asset=$1 AND code_hash=$2`,
		assetName, codeHash,
	).Scan(&state)
	require.NoError(t, err)
	return state
}

// readAuditEvents returns the event_type strings written to audit_log
// during this test.
func readAuditEvents(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT event_type FROM audit.audit_log ORDER BY seq ASC
	`)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var et string
		require.NoError(t, rows.Scan(&et))
		out = append(out, et)
	}
	return out
}

// ===== Tests =====

// TestWorkflow_Submit_AutoApprovedPath — clean asset → state goes to active,
// audit_log has governance.auto_approved.
func TestWorkflow_Submit_AutoApprovedPath(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_clean").Connector("snowflake").Materialize(noop).Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())

	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "owner@example.com")
	require.NoError(t, err)
	require.Equal(t, governance.DecisionAutoApproved, res.Decision)
	require.Equal(t, "auto_approved", res.Status)

	// State flipped to active.
	require.Equal(t, "active", readGovernanceState(t, db, a.Name(), a.CodeHash()))
	// Audit log contains governance.auto_approved.
	require.Contains(t, readAuditEvents(t, db), "governance.auto_approved")
}

// TestWorkflow_Submit_HumanReviewPath — RequireHumanReview() → in_review,
// audit has governance.submitted.
func TestWorkflow_Submit_HumanReviewPath(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_pii").Connector("snowflake").
		Materialize(noop).
		Reviewers("privacy-team").
		RequireHumanReview().
		Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())

	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "owner@example.com")
	require.NoError(t, err)
	require.Equal(t, governance.DecisionMustHumanReview, res.Decision)
	require.Equal(t, "in_review", res.Status)
	require.Equal(t, "in_review", readGovernanceState(t, db, a.Name(), a.CodeHash()))
	require.Contains(t, readAuditEvents(t, db), "governance.submitted")
}

// TestWorkflow_Submit_BlockedPath — pending drift → blocked, but row created
// so reviewer can see issue.
func TestWorkflow_Submit_BlockedPath(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_drift").Connector("snowflake").Materialize(noop).Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	// Asset version with drift_status=pending blocks auto-approval.
	id := uuid.New()
	_, err = db.ExecContext(context.Background(), `
		INSERT INTO asset_versions (id, asset, code_hash, drift_status, governance_state, created_at)
		VALUES ($1, $2, $3, 'pending', 'draft', NOW())
	`, id, a.Name(), a.CodeHash())
	require.NoError(t, err)

	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "owner@example.com")
	require.NoError(t, err)
	require.Equal(t, governance.DecisionBlocked, res.Decision)
	require.Equal(t, "in_review", res.Status, "blocked submissions still create the in_review row")
	require.Contains(t, res.Reason, "drift")
}

// TestWorkflow_Approve_HappyPath — submit → approve → state=active,
// audit governance.approved.
func TestWorkflow_Approve_HappyPath(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_approve").Connector("snowflake").
		Materialize(noop).
		Reviewers("team-data-gov").
		RequireHumanReview().
		Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	decider := seedUser(t, db, "gov@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())

	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)
	require.Equal(t, "in_review", res.Status)

	rev, err := w.Approve(context.Background(), res.ReviewID, decider, "looks good")
	require.NoError(t, err)
	require.Equal(t, "approved", rev.Status)
	require.Equal(t, "active", readGovernanceState(t, db, a.Name(), a.CodeHash()))
	require.Contains(t, readAuditEvents(t, db), "governance.approved")
}

// TestWorkflow_Reject_RequiresComment — empty comment → ErrCommentRequired,
// no DB writes.
func TestWorkflow_Reject_RequiresComment(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_reject1").Connector("snowflake").
		Materialize(noop).RequireHumanReview().Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	decider := seedUser(t, db, "gov@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())
	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)

	_, err = w.Reject(context.Background(), res.ReviewID, decider, "")
	require.ErrorIs(t, err, governance.ErrCommentRequired)

	// Whitespace-only comment also fails.
	_, err = w.Reject(context.Background(), res.ReviewID, decider, "   ")
	require.ErrorIs(t, err, governance.ErrCommentRequired)
}

// TestWorkflow_Reject_HappyPath — reject with comment → state=rejected,
// audit governance.rejected.
func TestWorkflow_Reject_HappyPath(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_reject2").Connector("snowflake").
		Materialize(noop).RequireHumanReview().Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	decider := seedUser(t, db, "gov@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())
	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)

	rev, err := w.Reject(context.Background(), res.ReviewID, decider, "missing PII review")
	require.NoError(t, err)
	require.Equal(t, "rejected", rev.Status)
	require.Equal(t, "rejected", readGovernanceState(t, db, a.Name(), a.CodeHash()))
	require.Contains(t, readAuditEvents(t, db), "governance.rejected")
}

// TestWorkflow_ResubmitAfterReject — same code_hash from rejected → state=in_review.
func TestWorkflow_ResubmitAfterReject(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_resub").Connector("snowflake").
		Materialize(noop).RequireHumanReview().Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	decider := seedUser(t, db, "gov@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())
	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)
	_, err = w.Reject(context.Background(), res.ReviewID, decider, "needs more review")
	require.NoError(t, err)
	require.Equal(t, "rejected", readGovernanceState(t, db, a.Name(), a.CodeHash()))

	// Re-submit with same code_hash → fresh in_review.
	res2, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)
	require.Equal(t, "in_review", res2.Status)
	require.Equal(t, "in_review", readGovernanceState(t, db, a.Name(), a.CodeHash()))
	require.NotEqual(t, res.ReviewID, res2.ReviewID, "resubmit creates a new review row")
}

// TestWorkflow_Reassign_RotatesReviewerPool — reassign updates the pool snapshot.
func TestWorkflow_Reassign_RotatesReviewerPool(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_reassign").Connector("snowflake").
		Materialize(noop).Reviewers("old-team").RequireHumanReview().Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	admin := seedUser(t, db, "admin@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())
	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)

	rev, err := w.Reassign(context.Background(), res.ReviewID, []string{"new-team-1", "new-team-2"}, admin)
	require.NoError(t, err)
	require.Equal(t, []string{"new-team-1", "new-team-2"}, rev.ReviewerPool.Roles)
	require.Equal(t, "in_review", rev.Status)
}

// TestWorkflow_Approve_QuorumAll_PartialDoesNotFlip — Quorum(All) with a
// 2-role pool needs 2 approvals; first approve leaves status=in_review.
func TestWorkflow_Approve_QuorumAll_PartialDoesNotFlip(t *testing.T) {
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_quorum").Connector("snowflake").
		Materialize(noop).
		Reviewers("team-a", "team-b").
		Quorum(asset.QuorumAll).
		RequireHumanReview().
		Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@example.com")
	d1 := seedUser(t, db, "approver1@example.com")
	d2 := seedUser(t, db, "approver2@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())
	res, err := w.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)

	// First approval — must NOT flip (quorum=2 needed).
	rev, err := w.Approve(context.Background(), res.ReviewID, d1, "approve 1/2")
	require.NoError(t, err)
	require.Equal(t, "in_review", rev.Status, "partial vote keeps status in_review")
	require.Equal(t, "in_review", readGovernanceState(t, db, a.Name(), a.CodeHash()))

	// Second approval — flips to approved.
	rev2, err := w.Approve(context.Background(), res.ReviewID, d2, "approve 2/2")
	require.NoError(t, err)
	require.Equal(t, "approved", rev2.Status)
	require.Equal(t, "active", readGovernanceState(t, db, a.Name(), a.CodeHash()))
}

// Helper: ensure default time import is used (gofmt-stable test).
var _ = time.Now

// strings is sometimes used.
var _ = strings.Contains
