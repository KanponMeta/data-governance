package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// WriteEntry writes a new audit_log row and advances the sentinel atomically
// within the provided transaction. It implements the hash-chain protocol:
//
//	self_hash = SHA-256(
//	  encodeUint64(seq) ||
//	  prev_hash ||
//	  encodeUint64(occurredAt.UnixNano()) ||
//	  event_type ||
//	  actor_id ||
//	  resource_type ||
//	  resource_id ||
//	  JCS(payload)
//	)
//
// The sentinel row is locked with SELECT ... FOR UPDATE to serialize all
// concurrent writers. If the transaction commits, the caller is responsible
// for emitting the event to the chain; on error the tx rolls back and the
// chain is untouched.
//
// Returns the assigned sequence number and any error.
func WriteEntry(ctx context.Context, tx *sql.Tx, e Entry) (seq int64, err error) {
	// 1. Lock the sentinel row and read current state.
	var prevSeq int64
	var prevHash []byte
	err = tx.QueryRowContext(ctx, `
		SELECT seq, self_hash FROM audit.audit_sentinel WHERE id = 1 FOR UPDATE
	`).Scan(&prevSeq, &prevHash)
	if err != nil {
		return 0, fmt.Errorf("write_entry: lock sentinel: %w", err)
	}

	// 2. Prepare canonical payload bytes.
	payloadBytes, err := CanonicalJSON(e.Payload)
	if err != nil {
		return 0, fmt.Errorf("write_entry: canonical payload: %w", err)
	}

	// 3. Resolve occurredAt BEFORE hashing — the hash MUST use the same
	// timestamp value that is persisted to audit_log.occurred_at, otherwise
	// Verify will recompute a hash that does not match the stored row.
	occurredAt := e.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now().UTC()
	}

	// 4. Compute self_hash using the resolved occurredAt.
	seq = prevSeq + 1
	selfHash := computeSelfHash(seq, prevHash, occurredAt, e.EventType, e.ActorID, e.ResourceType, e.ResourceID, payloadBytes)

	// 5. Insert the audit row.
	var actorID any = nil
	if e.ActorID != nil {
		actorID = *e.ActorID
	}
	var expiresAt any = nil
	if e.ExpiresAt != nil {
		expiresAt = *e.ExpiresAt
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit.audit_log (prev_hash, self_hash, occurred_at, event_type, actor_id, resource_type, resource_id, payload, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, prevHash, selfHash, occurredAt, string(e.EventType), actorID, e.ResourceType, e.ResourceID, payloadBytes, expiresAt)
	if err != nil {
		return 0, fmt.Errorf("write_entry: insert audit_log: %w", err)
	}

	// 6. Advance sentinel.
	_, err = tx.ExecContext(ctx, `
		UPDATE audit.audit_sentinel SET seq = $1, self_hash = $2 WHERE id = 1
	`, seq, selfHash)
	if err != nil {
		return 0, fmt.Errorf("write_entry: update sentinel: %w", err)
	}

	return seq, nil
}

// computeSelfHash computes the SHA-256 hash for an audit entry.
// Format: seq(be64) || prev_hash || occurredAtUnixNano(be64) || eventType || actorID || resourceType || resourceID || canonicalPayload
func computeSelfHash(seq int64, prevHash []byte, occurredAt time.Time, eventType AuditEventType, actorID *uuid.UUID, resourceType, resourceID string, canonicalPayload []byte) []byte {
	// Pre-allocate worst-case size: 8 + 32 + 8 + 256 + 16 + 256 + 256 + 8192
	buf := make([]byte, 0, 8+len(prevHash)+8+256+16+256+256+8192)

	// seq as big-endian uint64.
	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], uint64(seq))
	buf = append(buf, seqBytes[:]...)

	// prev_hash.
	buf = append(buf, prevHash...)

	// occurredAt.UnixNano() as big-endian uint64.
	var tsBytes [8]byte
	binary.BigEndian.PutUint64(tsBytes[:], uint64(occurredAt.UnixNano()))
	buf = append(buf, tsBytes[:]...)

	// event_type.
	buf = append(buf, eventType...)

	// actor_id (empty string if nil).
	if actorID != nil {
		buf = append(buf, actorID.String()...)
	}

	// resource_type.
	buf = append(buf, resourceType...)

	// resource_id.
	buf = append(buf, resourceID...)

	// canonical payload.
	buf = append(buf, canonicalPayload...)

	h := sha256.New()
	h.Write(buf)
	return h.Sum(nil)
}
