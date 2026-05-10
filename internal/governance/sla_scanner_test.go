package governance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/governance"
	"github.com/kanpon/data-governance/internal/notification"
)

// fakeQueue records every InsertTx / Insert call for assertion.
type fakeQueue struct {
	mu      sync.Mutex
	inserts []notification.NotificationDispatchArgs
}

func (q *fakeQueue) Insert(_ context.Context, args notification.NotificationDispatchArgs) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inserts = append(q.inserts, args)
	return nil
}
func (q *fakeQueue) InsertTx(_ context.Context, _ *sql.Tx, args notification.NotificationDispatchArgs) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.inserts = append(q.inserts, args)
	return nil
}

// fakeOwnerLookup returns a fixed owner string.
type fakeOwnerLookup struct{ owner string }

func (f *fakeOwnerLookup) Owner(_ context.Context, _ string) (string, error) {
	return f.owner, nil
}

// seedReviewRow inserts a governance_reviews row with submitted_at offset.
// hoursAgo > 0 means "submitted N hours ago"; 0 means "now".
func seedReviewRow(t *testing.T, db *sql.DB, assetName string, hoursAgo int, escalationRoles []string) uuid.UUID {
	t.Helper()
	submitter := seedUser(t, db, "engineer-sla@example.com-"+uuid.NewString())
	avID := seedAssetVersion(t, db, assetName, "code-"+uuid.NewString())
	id := uuid.New()
	pool := governance.ReviewerPool{Roles: []string{"team-a"}}
	poolJSON, _ := json.Marshal(pool)
	escJSON, _ := json.Marshal(escalationRoles)
	if escJSON == nil {
		escJSON = []byte("[]")
	}
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO governance_reviews (
			id, asset_version_id, asset, code_hash, submitter_id,
			submitted_at, reviewer_pool_snapshot, quorum, require_human_review,
			escalation_roles, status
		) VALUES ($1, $2, $3, 'code-x', $4,
			NOW() - (interval '1 hour' * $5),
			$6::jsonb, 1, true, $7::jsonb, 'in_review')
	`, id, avID, assetName, submitter, hoursAgo, string(poolJSON), string(escJSON))
	require.NoError(t, err)
	return id
}

// TestSLAScanner_NoBreaches_WhenAllRecent — recent reviews are not breached.
func TestSLAScanner_NoBreaches_WhenAllRecent(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	_, db, cleanup := setupWorkflow(t)
	defer cleanup()
	_ = seedReviewRow(t, db, "asset-recent", 0, nil)
	q := &fakeQueue{}
	scanner := governance.NewSLAScanner(db, q, 48, nil)
	n, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, n)
	require.Empty(t, q.inserts)
}

// TestSLAScanner_OneBreachAfterSLA — a row submitted 49h ago breaches a 48h SLA.
func TestSLAScanner_OneBreachAfterSLA(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	_, db, cleanup := setupWorkflow(t)
	defer cleanup()
	id := seedReviewRow(t, db, "asset-old", 49, nil)
	q := &fakeQueue{}
	scanner := governance.NewSLAScanner(db, q, 48, nil)
	n, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// sla_breach_emitted_at is now set.
	var marked sql.NullTime
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT sla_breach_emitted_at FROM governance_reviews WHERE id=$1`, id,
	).Scan(&marked))
	require.True(t, marked.Valid)

	// Audit log received the breach event.
	require.Contains(t, readAuditEvents(t, db), "governance.review_sla_breached")
}

// TestSLAScanner_DoesNotReEmit — second scan returns 0 because
// sla_breach_emitted_at is now set.
func TestSLAScanner_DoesNotReEmit(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	_, db, cleanup := setupWorkflow(t)
	defer cleanup()
	_ = seedReviewRow(t, db, "asset-once", 49, nil)
	q := &fakeQueue{}
	scanner := governance.NewSLAScanner(db, q, 48, nil)
	first, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, first)

	second, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, second, "scanner must not re-emit per breach")
}

// TestSLAScanner_NotifiesReviewersAndOwner — the queue.InsertTx call carries
// reviewer pool roles + owner email + escalation roles.
func TestSLAScanner_NotifiesReviewersAndOwner(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	_, db, cleanup := setupWorkflow(t)
	defer cleanup()
	_ = seedReviewRow(t, db, "asset-notify", 49, []string{"director"})
	q := &fakeQueue{}
	owner := &fakeOwnerLookup{owner: "owner@example.com"}
	scanner := governance.NewSLAScanner(db, q, 48, owner)
	n, err := scanner.Scan(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Len(t, q.inserts, 1)
	rec := q.inserts[0].Recipients
	require.Contains(t, rec, "team-a")
	require.Contains(t, rec, "owner@example.com")
	require.Contains(t, rec, "director")
}
