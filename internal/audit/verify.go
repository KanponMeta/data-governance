package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
)

// Result describes the outcome of a Verify run.
type Result struct {
	OK           bool
	Scanned      int64
	From, To     int64
	MismatchSeq  *int64
	ComputedHash []byte
	StoredHash   []byte
	Err          error
}

// Verify scans audit_log rows from fromSeq to toSeq (inclusive) and recomputes
// each row's self_hash. If any row's stored hash differs from the computed hash,
// Verify returns Result{OK:false, MismatchSeq:<first bad seq>}.
//
// When a mismatch is detected, Verify emits an audit.verify_failed entry to the
// same chain so the tampering event itself is audited (recursive audit).
//
// If fromSeq==0 it is treated as 1 (the sentinel has seq=0 but is not in audit_log).
func Verify(ctx context.Context, db *sql.DB, from, to int64) (Result, error) {
	if from == 0 {
		from = 1
	}
	if to < from {
		to = from
	}

	rows, err := db.QueryContext(ctx, `
		SELECT seq, prev_hash, self_hash, occurred_at, event_type, actor_id, resource_type, resource_id, payload
		FROM audit.audit_log
		WHERE seq BETWEEN $1 AND $2
		ORDER BY seq
	`, from, to)
	if err != nil {
		return Result{Err: fmt.Errorf("verify: query: %w", err)}, nil
	}
	defer rows.Close()

	var scanned int64
	var prevHash []byte

	for rows.Next() {
		var seq int64
		var storedHash, rowPrevHash []byte
		var occurredAtUnixNano int64
		var eventType string
		var actorID *string
		var resourceType, resourceID string
		var payload []byte

		if err := rows.Scan(&seq, &rowPrevHash, &storedHash, &occurredAtUnixNano, &eventType, &actorID, &resourceType, &resourceID, &payload); err != nil {
			return Result{Err: fmt.Errorf("verify: scan row %d: %w", seq, err)}, nil
		}

		// Verify prev_hash linkage for first row in range.
		if scanned == 0 {
			// First row — prev_hash should match sentinel (unless from>1, then it should match the row before 'from').
			var sentinelHash []byte
			if from == 1 {
				err := db.QueryRowContext(ctx, `SELECT self_hash FROM audit.audit_sentinel WHERE id = 1`).Scan(&sentinelHash)
				if err != nil {
					return Result{Err: fmt.Errorf("verify: read sentinel: %w", err)}, nil
				}
			} else {
				// Check the row before 'from' matches this row's prev_hash.
				err := db.QueryRowContext(ctx, `SELECT self_hash FROM audit.audit_log WHERE seq = $1`, from-1).Scan(&sentinelHash)
				if err != nil {
					return Result{Err: fmt.Errorf("verify: read prev hash for seq %d: %w", from-1, err)}, nil
				}
			}
			prevHash = sentinelHash
		}

		// Verify sequence continuity.
		if seq != from+scanned {
			return Result{Err: fmt.Errorf("verify: sequence discontinuity at %d (expected %d)", seq, from+scanned)}, nil
		}

		// Compute expected hash.
		var actorStr any
		if actorID != nil {
			actorStr = *actorID
		}
		computedHash := computeSelfHashFromRow(seq, prevHash, occurredAtUnixNano, eventType, actorStr, resourceType, resourceID, payload)

		if !bytesEqual(computedHash, storedHash) {
			// Emit audit.verify_failed entry (async — don't fail the verify result itself).
			go emitVerifyFailedEntry(ctx, db, seq, storedHash, computedHash)
			return Result{
				OK:           false,
				Scanned:      scanned + 1,
				From:         from,
				To:           to,
				MismatchSeq:  &seq,
				ComputedHash:  computedHash,
				StoredHash:   storedHash,
			}, nil
		}

		prevHash = storedHash
		scanned++
	}

	if err := rows.Err(); err != nil {
		return Result{Err: fmt.Errorf("verify: rows: %w", err)}, nil
	}

	return Result{
		OK:      true,
		Scanned: scanned,
		From:    from,
		To:      from + scanned - 1,
	}, nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// computeSelfHashFromRow recomputes self_hash from raw row fields.
// occurredAtUnixNano is the UnixNano timestamp as an int64 (extracted from occurred_at TIMESTAMPTZ).
func computeSelfHashFromRow(seq int64, prevHash []byte, occurredAtUnixNano int64, eventType string, actorID any, resourceType, resourceID string, payload []byte) []byte {
	buf := make([]byte, 0, 8+len(prevHash)+8+256+16+256+256+8192)

	var seqBytes [8]byte
	binary.BigEndian.PutUint64(seqBytes[:], uint64(seq))
	buf = append(buf, seqBytes[:]...)

	buf = append(buf, prevHash...)

	var tsBytes [8]byte
	binary.BigEndian.PutUint64(tsBytes[:], uint64(occurredAtUnixNano))
	buf = append(buf, tsBytes[:]...)

	buf = append(buf, eventType...)

	if actorID != nil {
		buf = append(buf, actorID.(string)...)
	}

	buf = append(buf, resourceType...)
	buf = append(buf, resourceID...)
	buf = append(buf, payload...)

	h := sha256.New()
	h.Write(buf)
	return h.Sum(nil)
}

// emitVerifyFailedEntry appends an audit.verify_failed entry to the chain.
func emitVerifyFailedEntry(ctx context.Context, db *sql.DB, badSeq int64, storedHash, computedHash []byte) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()

	_, _ = WriteEntry(ctx, tx, Entry{
		EventType:    AuditVerifyFailed,
		ResourceType: "audit",
		ResourceID:   fmt.Sprintf("%d", badSeq),
		Payload: map[string]any{
			"mismatch_seq":  badSeq,
			"stored_hash":   storedHash,
			"computed_hash": computedHash,
		},
	})
	_ = tx.Commit()
}
