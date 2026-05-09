package testharness

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

// SeedGenesisAudit inserts the genesis sentinel row into audit.audit_sentinel
// (seq=0, self_hash=32 zero bytes). It is idempotent — subsequent calls are no-ops.
func SeedGenesisAudit(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO audit.audit_sentinel (id, seq, self_hash)
		VALUES (1, 0, decode('0000000000000000000000000000000000000000000000000000000000000000','hex'))
		ON CONFLICT DO NOTHING
	`)
	if err != nil {
		t.Fatalf("SeedGenesisAudit: %v", err)
	}
}

// ChainSnapshot captures a single row from audit.audit_log for testing assertions.
type ChainSnapshot struct {
	Seq          int64
	PrevHash     []byte
	SelfHash     []byte
	OccurredAt   time.Time
	EventType    string
	Actor        *uuid.UUID
	ResourceType string
	ResourceID   string
	Payload      []byte
}

// ReadChain returns all audit_log rows ordered by seq ascending.
func ReadChain(t *testing.T, db *sql.DB) []ChainSnapshot {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT seq, prev_hash, self_hash, occurred_at, event_type, actor_id, resource_type, resource_id, payload
		FROM audit.audit_log ORDER BY seq
	`)
	if err != nil {
		t.Fatalf("ReadChain: query: %v", err)
	}
	defer rows.Close()

	var snapshots []ChainSnapshot
	for rows.Next() {
		var s ChainSnapshot
		var actorID *uuid.UUID
		var payload []byte
		if err := rows.Scan(&s.Seq, &s.PrevHash, &s.SelfHash, &s.OccurredAt, &s.EventType, &actorID, &s.ResourceType, &s.ResourceID, &payload); err != nil {
			t.Fatalf("ReadChain: scan: %v", err)
		}
		s.Actor = actorID
		s.Payload = payload
		snapshots = append(snapshots, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("ReadChain: rows err: %v", err)
	}
	return snapshots
}

// TamperRow updates a row's payload bypassing RLS (used to simulate tampering).
// It connects as postgres superuser directly to test tamper detection.
func TamperRow(t *testing.T, db *sql.DB, seq int64, newPayload string) {
	t.Helper()
	// Bypass RLS by using SET ROLE postgres.
	_, err := db.ExecContext(context.Background(), `
		SET ROLE postgres;
		UPDATE audit.audit_log SET payload = $1 WHERE seq = $2;
		RESET ROLE;
	`, json.RawMessage(newPayload), seq)
	if err != nil {
		t.Fatalf("TamperRow: %v", err)
	}
}

// InsertAuditEntryForTest is a helper that writes a raw audit entry for test purposes.
// It uses raw SQL and bypasses the hash chain — use only in test code where the hash
// chain is not being tested. For integration tests that cover the hash chain,
// use audit.WriteEntry after Task 1a creates that package.
func InsertAuditEntryForTest(t *testing.T, db *sql.DB, eventType, resourceType, resourceID string, payload map[string]any) int64 {
	t.Helper()
	ctx := context.Background()

	// Get previous hash from sentinel.
	var prevHash []byte
	var seq int64
	err := db.QueryRowContext(ctx, `
		SELECT seq, self_hash FROM audit.audit_sentinel WHERE id = 1
	`).Scan(&seq, &prevHash)
	if err != nil {
		t.Fatalf("InsertAuditEntryForTest: read sentinel: %v", err)
	}

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("InsertAuditEntryForTest: marshal payload: %v", err)
	}

	// Self-hash placeholder (not a real hash — tests that care about hash integrity
	// should use audit.WriteEntry from the audit package after Task 1a).
	selfHash := make([]byte, 32)

	newSeq := seq + 1
	now := time.Now().UTC()
	_, err = db.ExecContext(ctx, `
		INSERT INTO audit.audit_log (prev_hash, self_hash, occurred_at, event_type, resource_type, resource_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, prevHash, selfHash, now, eventType, resourceType, resourceID, payloadJSON)
	if err != nil {
		t.Fatalf("InsertAuditEntryForTest: insert: %v", err)
	}

	// Update sentinel.
	_, err = db.ExecContext(ctx, `
		UPDATE audit.audit_sentinel SET seq = $1, self_hash = $2 WHERE id = 1
	`, newSeq, selfHash)
	if err != nil {
		t.Fatalf("InsertAuditEntryForTest: update sentinel: %v", err)
	}

	return newSeq
}
