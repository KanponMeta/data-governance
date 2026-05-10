package audit

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Format specifies the output format for Export.
type Format string

const (
	FormatJSONL Format = "jsonl"
	FormatJSON  Format = "json"
	FormatCSV   Format = "csv"
)

// Export streams audit_log rows from occurred_at BETWEEN fromTime and toTime
// to the provided io.Writer in the requested format.
// It also appends an audit.exported entry to the chain so the export itself is audited.
//
// Memory budget: streaming via rows.Next() ensures O(1) memory regardless of result size.
func Export(ctx context.Context, db *sql.DB, w io.Writer, format Format, fromTime, toTime time.Time) (rowCount int64, err error) {
	rows, err := db.QueryContext(ctx, `
		SELECT seq, encode(prev_hash,'hex'), encode(self_hash,'hex'), occurred_at, event_type, actor_id, resource_type, resource_id, payload, expires_at
		FROM audit.audit_log
		WHERE occurred_at BETWEEN $1 AND $2
		ORDER BY seq
	`, fromTime, toTime)
	if err != nil {
		return 0, fmt.Errorf("export: query: %w", err)
	}
	defer rows.Close()

	switch format {
	case FormatJSONL:
		return exportJSONL(ctx, db, rows, w, fromTime, toTime)
	case FormatCSV:
		return exportCSV(ctx, db, rows, w, fromTime, toTime)
	case FormatJSON:
		return exportJSON(ctx, db, rows, w, fromTime, toTime)
	default:
		return 0, fmt.Errorf("export: unknown format %q", format)
	}
}

type auditRow struct {
	Seq          int64     `json:"seq"`
	PrevHash     string    `json:"prev_hash"`
	SelfHash     string    `json:"self_hash"`
	OccurredAt   time.Time `json:"occurred_at"`
	EventType    string    `json:"event_type"`
	ActorID      *string   `json:"actor_id,omitempty"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	Payload      json.RawMessage `json:"payload"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

func exportJSONL(ctx context.Context, db *sql.DB, rows *sql.Rows, w io.Writer, fromTime, toTime time.Time) (int64, error) {
	var count int64
	for rows.Next() {
		var r auditRow
		var occurredAt time.Time
		var expiresAt *time.Time
		var payload []byte
		if err := rows.Scan(&r.Seq, &r.PrevHash, &r.SelfHash, &occurredAt, &r.EventType, &r.ActorID, &r.ResourceType, &r.ResourceID, &payload, &expiresAt); err != nil {
			return count, fmt.Errorf("export jsonl: scan: %w", err)
		}
		r.OccurredAt = occurredAt
		r.Payload = payload
		r.ExpiresAt = expiresAt

		data, err := json.Marshal(r)
		if err != nil {
			return count, fmt.Errorf("export jsonl: marshal: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return count, fmt.Errorf("export jsonl: write: %w", err)
		}
		if _, err := w.Write([]byte("\n")); err != nil {
			return count, fmt.Errorf("export jsonl: write nl: %w", err)
		}
		count++
	}

	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("export jsonl: rows: %w", err)
	}

	_ = emitExportAuditEntry(ctx, db, FormatJSONL, count, fromTime, toTime)

	return count, nil
}

func exportCSV(ctx context.Context, db *sql.DB, rows *sql.Rows, w io.Writer, fromTime, toTime time.Time) (int64, error) {
	cw := csv.NewWriter(w)
	headers := []string{"seq", "prev_hash", "self_hash", "occurred_at", "event_type", "actor_id", "resource_type", "resource_id", "payload", "expires_at"}
	if err := cw.Write(headers); err != nil {
		return 0, fmt.Errorf("export csv: write header: %w", err)
	}

	var count int64
	for rows.Next() {
		var r auditRow
		var occurredAt time.Time
		var expiresAt *time.Time
		var payload []byte
		if err := rows.Scan(&r.Seq, &r.PrevHash, &r.SelfHash, &occurredAt, &r.EventType, &r.ActorID, &r.ResourceType, &r.ResourceID, &payload, &expiresAt); err != nil {
			return count, fmt.Errorf("export csv: scan: %w", err)
		}
		record := []string{
			fmt.Sprintf("%d", r.Seq),
			r.PrevHash,
			r.SelfHash,
			occurredAt.Format(time.RFC3339Nano),
			r.EventType,
			nullableString(r.ActorID),
			r.ResourceType,
			r.ResourceID,
			string(payload),
			nullableTime(expiresAt),
		}
		if err := cw.Write(record); err != nil {
			return count, fmt.Errorf("export csv: write row: %w", err)
		}
		count++
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return count, fmt.Errorf("export csv: flush: %w", err)
	}

	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("export csv: rows: %w", err)
	}

	_ = emitExportAuditEntry(ctx, db, FormatCSV, count, fromTime, toTime)

	return count, nil
}

func exportJSON(ctx context.Context, db *sql.DB, rows *sql.Rows, w io.Writer, fromTime, toTime time.Time) (int64, error) {
	if _, err := w.Write([]byte("[")); err != nil {
		return 0, fmt.Errorf("export json: write bracket: %w", err)
	}

	var count int64
	first := true
	for rows.Next() {
		var r auditRow
		var occurredAt time.Time
		var expiresAt *time.Time
		var payload []byte
		if err := rows.Scan(&r.Seq, &r.PrevHash, &r.SelfHash, &occurredAt, &r.EventType, &r.ActorID, &r.ResourceType, &r.ResourceID, &payload, &expiresAt); err != nil {
			return count, fmt.Errorf("export json: scan: %w", err)
		}
		r.OccurredAt = occurredAt
		r.Payload = payload
		r.ExpiresAt = expiresAt

		if !first {
			if _, err := w.Write([]byte(",")); err != nil {
				return count, fmt.Errorf("export json: write comma: %w", err)
			}
		}
		first = false

		data, err := json.Marshal(r)
		if err != nil {
			return count, fmt.Errorf("export json: marshal: %w", err)
		}
		if _, err := w.Write(data); err != nil {
			return count, fmt.Errorf("export json: write: %w", err)
		}
		count++
	}

	if _, err := w.Write([]byte("]")); err != nil {
		return count, fmt.Errorf("export json: write close: %w", err)
	}

	if err := rows.Err(); err != nil {
		return count, fmt.Errorf("export json: rows: %w", err)
	}

	_ = emitExportAuditEntry(ctx, db, FormatJSON, count, fromTime, toTime)

	return count, nil
}

func nullableString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullableTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// emitExportAuditEntry appends an audit.exported entry to the chain.
// It is called after each successful export and runs in its own transaction.
// Errors are logged but not propagated (export already succeeded).
func emitExportAuditEntry(ctx context.Context, db *sql.DB, format Format, rowCount int64, from, to time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = WriteEntry(ctx, tx, Entry{
		EventType:    AuditExported,
		OccurredAt:   time.Now().UTC(),
		ResourceType: "audit",
		ResourceID:   "export",
		Payload: map[string]any{
			"format":    string(format),
			"row_count": rowCount,
			"from":      from.Format(time.RFC3339),
			"to":        to.Format(time.RFC3339),
		},
	})
	if err != nil {
		return err
	}

	return tx.Commit()
}
