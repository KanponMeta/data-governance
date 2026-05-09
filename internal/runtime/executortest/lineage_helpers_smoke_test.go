//go:build integration

package executortest

import (
	"context"
	"testing"
)

// TestSmoke_StartPhase4Container verifies:
//  1. The container starts and the DB is reachable.
//  2. The "platform" database is active.
//  3. The connection is non-empty (current_user not empty).
//  4. The Phase 1 event_log table is present (migrations applied).
//
// Run with: go test ./internal/runtime/executortest/... -tags=integration -run Smoke -count=1 -timeout 180s
// Requires Docker. Set CI_NO_DOCKER=1 to skip.
func TestSmoke_StartPhase4Container(t *testing.T) {
	ctx := context.Background()
	c := StartPhase4Container(ctx, t)

	// 1. Ping succeeds.
	if err := c.DB.PingContext(ctx); err != nil {
		t.Fatalf("Ping failed: %v", err)
	}

	// 2. Connected to the "platform" database.
	var dbName string
	if err := c.DB.QueryRowContext(ctx, "SELECT current_database()").Scan(&dbName); err != nil {
		t.Fatalf("SELECT current_database() failed: %v", err)
	}
	if dbName != "platform" {
		t.Errorf("expected current_database()=%q; got %q", "platform", dbName)
	}

	// 3. Current user is non-empty.
	var currentUser string
	if err := c.DB.QueryRowContext(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatalf("SELECT current_user failed: %v", err)
	}
	if currentUser == "" {
		t.Error("current_user is empty")
	}

	// 4. Phase 1 event_log table exists and has 0 rows after a fresh migration.
	var count int
	if err := c.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM event_log").Scan(&count); err != nil {
		t.Fatalf("SELECT COUNT(*) FROM event_log failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows in event_log after fresh migration; got %d", count)
	}
}
