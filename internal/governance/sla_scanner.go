package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/notification"
)

// SLAScanner is a tick-driven scanner that emits
// audit.AuditGovernanceReviewSLABreached for each review whose
// submitted_at + sla_hours has passed and decided_at is still NULL (D-11).
//
// SLA breaches DO NOT auto-escalate the state — SOC 2 compliance requires
// every approval to be a deliberate human action. The scanner only emits
// the audit entry + dispatches notification jobs to (reviewer_pool ∪ owner ∪
// escalation_roles). sla_breach_emitted_at is set so subsequent ticks do
// not re-emit per breach.
type SLAScanner struct {
	db        *sql.DB
	queue     notification.JobInserter
	SLAHours  int    // configurable; 0 → 48
	OwnerLookup OwnerLookup
}

// OwnerLookup resolves the asset_metadata.owner email for a given asset.
// Pass nil to skip owner-tier notifications.
type OwnerLookup interface {
	Owner(ctx context.Context, assetName string) (string, error)
}

// NewSLAScanner constructs the scanner. queue may be nil (audit-only mode);
// ownerLookup may be nil (skip owner notification tier).
func NewSLAScanner(db *sql.DB, queue notification.JobInserter, slaHours int, ownerLookup OwnerLookup) *SLAScanner {
	if slaHours <= 0 {
		slaHours = 48
	}
	return &SLAScanner{db: db, queue: queue, SLAHours: slaHours, OwnerLookup: ownerLookup}
}

// Scan walks governance_reviews looking for unbreached past-SLA rows. Returns
// the number of breaches emitted in this scan. Errors stop the scan after
// recording the first failure and return what was processed so far.
//
// Each emit happens in its own tx (audit.WriteEntry is hash-chain serialised
// at the sentinel anyway).
func (s *SLAScanner) Scan(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, asset, submitter_id, reviewer_pool_snapshot, escalation_roles, submitted_at, code_hash
		  FROM governance_reviews
		 WHERE decided_at IS NULL
		   AND sla_breach_emitted_at IS NULL
		   AND submitted_at + (interval '1 hour' * $1) < NOW()
	`, s.SLAHours)
	if err != nil {
		return 0, fmt.Errorf("governance: sla scan query: %w", err)
	}
	defer rows.Close()

	type pending struct {
		ID            uuid.UUID
		Asset         string
		SubmitterID   uuid.UUID
		PoolJSON      []byte
		EscJSON       []byte
		SubmittedAt   time.Time
		CodeHash      string
	}
	var queue []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.ID, &p.Asset, &p.SubmitterID, &p.PoolJSON, &p.EscJSON, &p.SubmittedAt, &p.CodeHash); err != nil {
			return 0, fmt.Errorf("governance: sla scan scan row: %w", err)
		}
		queue = append(queue, p)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("governance: sla scan iter: %w", err)
	}

	emitted := 0
	for _, p := range queue {
		var pool ReviewerPool
		_ = json.Unmarshal(p.PoolJSON, &pool)
		var escalationRoles []string
		_ = json.Unmarshal(p.EscJSON, &escalationRoles)
		owner := ""
		if s.OwnerLookup != nil {
			if o, err := s.OwnerLookup.Owner(ctx, p.Asset); err == nil {
				owner = o
			}
		}

		recipients := append([]string{}, pool.Roles...)
		if owner != "" {
			recipients = append(recipients, owner)
		}
		if len(escalationRoles) > 0 {
			recipients = append(recipients, escalationRoles...)
		}
		recipients = dedupRoles(recipients)

		// Open tx, write audit, mark sla_breach_emitted_at, enqueue.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return emitted, fmt.Errorf("governance: sla scan begin tx: %w", err)
		}
		auditPayload := map[string]any{
			"review_id":   p.ID.String(),
			"asset":       p.Asset,
			"code_hash":   p.CodeHash,
			"submitter":   p.SubmitterID.String(),
			"submitted_at": p.SubmittedAt.Format(time.RFC3339),
			"sla_hours":   s.SLAHours,
			"recipients":  recipients,
		}
		if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
			EventType:    audit.AuditGovernanceReviewSLABreached,
			OccurredAt:   time.Now().UTC(),
			ResourceType: "governance_review",
			ResourceID:   p.ID.String(),
			Payload:      auditPayload,
		}); err != nil {
			_ = tx.Rollback()
			return emitted, fmt.Errorf("governance: sla audit %s: %w", p.ID, err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE governance_reviews SET sla_breach_emitted_at = NOW() WHERE id = $1`, p.ID,
		); err != nil {
			_ = tx.Rollback()
			return emitted, fmt.Errorf("governance: sla mark emitted %s: %w", p.ID, err)
		}
		// Build pending notification args pre-commit. Enqueue happens AFTER
		// commit so a rolled-back tx never produces a phantom notification
		// (CR-04). The in-process queue is non-transactional.
		var pendingArgs *notification.NotificationDispatchArgs
		if s.queue != nil {
			pendingArgs = &notification.NotificationDispatchArgs{
				EventType:  string(audit.AuditGovernanceReviewSLABreached),
				AssetName:  p.Asset,
				Payload:    auditPayload,
				Recipients: recipients,
				WebhookID:  p.ID.String() + ":sla-breach",
			}
		}
		if err := tx.Commit(); err != nil {
			return emitted, fmt.Errorf("governance: sla commit %s: %w", p.ID, err)
		}
		// Post-commit enqueue — fire-and-forget; failure does not roll back.
		if pendingArgs != nil {
			_ = s.queue.Insert(ctx, *pendingArgs)
		}
		emitted++
	}
	return emitted, nil
}

// SQLOwnerLookup is the default OwnerLookup that reads asset_metadata.
type SQLOwnerLookup struct{ DB *sql.DB }

// Owner returns the most-recent owner email for assetName from asset_metadata.
func (s *SQLOwnerLookup) Owner(ctx context.Context, assetName string) (string, error) {
	var owner sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT owner FROM asset_metadata
		 WHERE asset = $1 AND column_name IS NULL
		 ORDER BY set_at DESC LIMIT 1
	`, assetName).Scan(&owner)
	if err != nil {
		return "", err
	}
	if owner.Valid {
		return owner.String, nil
	}
	return "", nil
}
