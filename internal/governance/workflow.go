package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/notification"
)

// ===== Errors =====

var (
	// ErrCommentRequired is returned by Reject when comment is empty (D-12).
	ErrCommentRequired = errors.New("governance: comment required for reject")
	// ErrReviewNotFound is returned by Approve/Reject/Reassign/Status when the
	// requested review row does not exist (or is no longer in_review for
	// Approve/Reject).
	ErrReviewNotFound = errors.New("governance: review not found")
	// ErrAssetVersionNotFound is returned by Submit when no asset_versions row
	// exists for the supplied (asset, code_hash) pair.
	ErrAssetVersionNotFound = errors.New("governance: asset_versions row not found")
	// ErrAlreadyDecided is returned by Approve/Reject when the review already
	// has decided_at set.
	ErrAlreadyDecided = errors.New("governance: review already decided")
	// ErrSelfApproval is returned by Approve/Reject when the decider is the
	// submitter — four-eyes principle (D-12). A submitter cannot decide on
	// their own review.
	ErrSelfApproval = errors.New("governance: decider cannot be submitter (four-eyes)")
	// ErrDuplicateVote is returned by Approve/Reject when the decider has
	// already voted on this review. Prevents quorum bypass via repeat
	// approvals from the same actor.
	ErrDuplicateVote = errors.New("governance: decider already voted on this review")
)

// ===== Public types =====

// SubmitResult is the structured return from Workflow.Submit. Decision is
// the AutoApprovalChecker outcome; Status is the resulting governance_reviews
// row status (auto_approved or in_review).
type SubmitResult struct {
	ReviewID uuid.UUID
	Status   string
	Pool     ReviewerPool
	Decision Decision
	Reason   string
}

// Review is the read-shape returned by Status / Approve / Reject. Mirrors the
// governance_reviews row.
type Review struct {
	ID                  uuid.UUID
	AssetVersionID      uuid.UUID
	Asset               string
	CodeHash            string
	SubmitterID         uuid.UUID
	SubmittedAt         time.Time
	ReviewerPool        ReviewerPool
	Quorum              int
	RequireHumanReview  bool
	EscalationRoles     []string
	Status              string
	DecidedAt           *time.Time
	DecidedByID         *uuid.UUID
	Comment             string
	SLABreachEmittedAt  *time.Time
	ApprovalsSoFar      int // derived from comment ledger; v1 simplification.
}

// Workflow owns Submit / Approve / Reject / Reassign / Status state-machine
// transitions tied to the audit hash chain. Each method opens its own tx so
// the transition + audit + notification enqueue happen atomically.
type Workflow struct {
	db       *sql.DB
	resolver *Resolver
	checker  *AutoApprovalChecker
	queue    notification.JobInserter
}

// NewWorkflow constructs a Workflow. queue may be nil — the workflow becomes
// notification-less in that case (useful for tests / single-binary mode without
// the in-process queue wired). resolver/checker MUST be non-nil.
func NewWorkflow(db *sql.DB, resolver *Resolver, checker *AutoApprovalChecker, queue notification.JobInserter) *Workflow {
	return &Workflow{db: db, resolver: resolver, checker: checker, queue: queue}
}

// ===== Submit =====

// Submit performs the D-12 lifecycle entry: resolve reviewer pool → run
// AutoApprovalChecker → INSERT governance_reviews row → flip
// asset_versions.governance_state → write audit_log entry → enqueue
// notification job. All of this happens in a single tx.
//
// reviewersExtra appends to the resolved pool (callers that already know
// they want to add reviewers, e.g. PII path adds "privacy-team" automatically).
func (w *Workflow) Submit(
	ctx context.Context,
	assetName string,
	codeHash string,
	submitter uuid.UUID,
	reviewersExtra []string,
	a *asset.Asset, // for routing config — caller provides registered asset
	tags []string, // currently-applied asset+column tags (caller resolves)
	owner string, // asset_metadata.owner for fallback resolver
) (SubmitResult, error) {
	if a == nil {
		return SubmitResult{}, errors.New("governance: Submit requires non-nil asset")
	}

	// 1. Verify asset_versions row exists.
	var assetVersionID uuid.UUID
	err := w.db.QueryRowContext(ctx,
		`SELECT id FROM asset_versions WHERE asset=$1 AND code_hash=$2 ORDER BY created_at DESC LIMIT 1`,
		assetName, codeHash,
	).Scan(&assetVersionID)
	if errors.Is(err, sql.ErrNoRows) {
		return SubmitResult{}, fmt.Errorf("%w: %s code_hash=%s", ErrAssetVersionNotFound, assetName, codeHash)
	}
	if err != nil {
		return SubmitResult{}, fmt.Errorf("governance: lookup asset_versions: %w", err)
	}

	// 2. Resolve reviewer pool.
	pool, err := w.resolver.ResolveReviewers(ctx, a, tags, owner)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("governance: resolve reviewers: %w", err)
	}
	if len(reviewersExtra) > 0 {
		pool.Roles = dedupRoles(append(pool.Roles, reviewersExtra...))
		pool.Source = append(pool.Source, "submit-extra")
	}

	// 3. Run auto-approval pipeline.
	check, err := w.checker.Check(ctx, a, codeHash, owner, tags)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("governance: auto-approval check: %w", err)
	}

	// PII presence path: if the only failure is pii_presence, add privacy-team
	// to reviewer pool automatically (D-10).
	if check.Decision == DecisionMustHumanReview {
		for _, fc := range check.FailedChecks {
			if fc == "pii_presence" {
				pool.Roles = dedupRoles(append(pool.Roles, "privacy-team"))
				pool.Source = append(pool.Source, "pii-auto-add")
				break
			}
		}
	}

	// 4. Decide row status + new asset_versions.governance_state.
	rowStatus := "in_review"
	newGovState := "in_review"
	auditEvent := audit.AuditGovernanceSubmitted
	if check.Decision == DecisionAutoApproved {
		rowStatus = "auto_approved"
		newGovState = "active"
		auditEvent = audit.AuditGovernanceAutoApproved
	}

	// 5. Open tx, INSERT review, flip state, write audit, enqueue notification.
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return SubmitResult{}, fmt.Errorf("governance: begin tx: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	poolJSON, err := json.Marshal(pool)
	if err != nil {
		return SubmitResult{}, fmt.Errorf("governance: marshal reviewer pool: %w", err)
	}
	escJSON, _ := json.Marshal(pool.EscalationRoles)
	if escJSON == nil {
		escJSON = []byte("[]")
	}

	reviewID := uuid.New()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO governance_reviews (
		    id, asset_version_id, asset, code_hash, submitter_id,
		    reviewer_pool_snapshot, quorum, require_human_review, escalation_roles, status
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9::jsonb, $10)
	`, reviewID, assetVersionID, assetName, codeHash, submitter,
		string(poolJSON), normaliseQuorum(pool.Quorum, len(pool.Roles)), a.RequireHumanReview(),
		string(escJSON), rowStatus); err != nil {
		return SubmitResult{}, fmt.Errorf("governance: insert review: %w", err)
	}
	// Flip asset_versions.governance_state.
	if _, err := tx.ExecContext(ctx,
		`UPDATE asset_versions SET governance_state=$1 WHERE id=$2`,
		newGovState, assetVersionID,
	); err != nil {
		return SubmitResult{}, fmt.Errorf("governance: flip state: %w", err)
	}
	// Audit hash-chain entry.
	actorCp := submitter
	auditPayload := map[string]any{
		"review_id":     reviewID.String(),
		"asset":         assetName,
		"code_hash":     codeHash,
		"reviewer_pool": pool,
		"decision":      check.Decision.String(),
		"reason":        check.Reason,
		"failed_checks": check.FailedChecks,
	}
	if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    auditEvent,
		OccurredAt:   time.Now().UTC(),
		ActorID:      &actorCp,
		ResourceType: "governance_review",
		ResourceID:   reviewID.String(),
		Payload:      auditPayload,
	}); err != nil {
		return SubmitResult{}, fmt.Errorf("governance: audit submit: %w", err)
	}

	// Enqueue notification (best-effort — if queue is nil, skip).
	if w.queue != nil {
		args := notification.NotificationDispatchArgs{
			EventType:  string(auditEvent),
			AssetName:  assetName,
			Payload:    auditPayload,
			Recipients: pool.Roles,
			WebhookID:  reviewID.String() + ":submit",
		}
		// Fire-and-forget — failure to enqueue should not block submit.
		_ = w.queue.InsertTx(ctx, tx, args)
	}

	if err := tx.Commit(); err != nil {
		return SubmitResult{}, fmt.Errorf("governance: commit tx: %w", err)
	}
	rollback = false

	return SubmitResult{
		ReviewID: reviewID,
		Status:   rowStatus,
		Pool:     pool,
		Decision: check.Decision,
		Reason:   check.Reason,
	}, nil
}

// ===== Approve / Reject =====

// Approve transitions an in_review row toward approved (or stays in_review
// when quorum > approval count). Comment is optional for approve. Quorum
// logic is v1-simplified: each Approve appends "[role:X approved]" to
// comment and only flips status when ApprovalsSoFar+1 >= effective quorum.
func (w *Workflow) Approve(ctx context.Context, reviewID, decider uuid.UUID, comment string) (Review, error) {
	return w.decide(ctx, reviewID, decider, comment, false)
}

// Reject transitions an in_review row to rejected. Comment is REQUIRED;
// returns ErrCommentRequired when empty (D-12).
func (w *Workflow) Reject(ctx context.Context, reviewID, decider uuid.UUID, comment string) (Review, error) {
	if strings.TrimSpace(comment) == "" {
		return Review{}, ErrCommentRequired
	}
	return w.decide(ctx, reviewID, decider, comment, true)
}

// decide is the shared body of Approve / Reject. reject flag selects the
// terminal status + audit event type.
func (w *Workflow) decide(ctx context.Context, reviewID, decider uuid.UUID, comment string, reject bool) (Review, error) {
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Review{}, fmt.Errorf("governance: begin tx: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	// Lock the row.
	var (
		assetVersionID                                                                                 uuid.UUID
		assetName, codeHash, status, currentComment                                                    string
		submitterID                                                                                    uuid.UUID
		quorum                                                                                         int
		requireHuman                                                                                   bool
		poolJSON                                                                                       []byte
		decidedAt                                                                                      *time.Time
	)
	err = tx.QueryRowContext(ctx, `
		SELECT asset_version_id, asset, code_hash, submitter_id, quorum, require_human_review,
		       reviewer_pool_snapshot, status, COALESCE(comment,''), decided_at
		  FROM governance_reviews
		 WHERE id = $1
		 FOR UPDATE
	`, reviewID).Scan(&assetVersionID, &assetName, &codeHash, &submitterID, &quorum, &requireHuman,
		&poolJSON, &status, &currentComment, &decidedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Review{}, ErrReviewNotFound
	}
	if err != nil {
		return Review{}, fmt.Errorf("governance: lock review: %w", err)
	}
	if decidedAt != nil {
		return Review{}, ErrAlreadyDecided
	}

	// Four-eyes: a submitter cannot decide on their own review. Without this
	// check, a submitter holding the governance permission could self-approve
	// and bypass review entirely (D-12).
	if decider == submitterID {
		return Review{}, ErrSelfApproval
	}

	// Duplicate-vote check: scan the existing comment ledger for a prior vote
	// from this decider. Without this check, a single privileged user could
	// flip status to approved by repeated calls (quorum bypass).
	if hasPriorVoteByDecider(currentComment, decider) {
		return Review{}, ErrDuplicateVote
	}

	var pool ReviewerPool
	_ = json.Unmarshal(poolJSON, &pool)

	// Append a vote line to comment for quorum tracking. The "[approved by ..."
	// or "[rejected by ..." token is what countApprovals scans for at the
	// next decision call (D-09 quorum semantics, simplified for v1).
	voteLine := fmt.Sprintf("[%s by %s] %s", boolStr(reject, "rejected", "approved"), decider.String(), comment)
	combinedComment := strings.TrimSpace(currentComment + "\n" + voteLine)

	terminal := false
	newStatus := "in_review"
	newGovState := "in_review"
	auditEvent := audit.AuditGovernanceApproved

	if reject {
		terminal = true
		newStatus = "rejected"
		newGovState = "rejected"
		auditEvent = audit.AuditGovernanceRejected
	} else {
		// approve — count current approvals.
		approvalsSoFar := countApprovals(currentComment)
		needed := quorum
		if needed == int(asset.QuorumAll) || needed < 0 {
			needed = len(pool.Roles)
			if needed < 1 {
				needed = 1
			}
		}
		if needed < 1 {
			needed = 1
		}
		if approvalsSoFar+1 >= needed {
			terminal = true
			newStatus = "approved"
			newGovState = "active"
		}
	}

	if terminal {
		now := time.Now().UTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE governance_reviews
			   SET status = $1, decided_at = $2, decided_by_id = $3, comment = $4
			 WHERE id = $5
		`, newStatus, now, decider, combinedComment, reviewID); err != nil {
			return Review{}, fmt.Errorf("governance: update review (terminal): %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE asset_versions SET governance_state=$1 WHERE id=$2`,
			newGovState, assetVersionID,
		); err != nil {
			return Review{}, fmt.Errorf("governance: flip state: %w", err)
		}
	} else {
		// partial — record the vote in comment but stay in_review.
		if _, err := tx.ExecContext(ctx, `
			UPDATE governance_reviews SET comment = $1 WHERE id = $2
		`, combinedComment, reviewID); err != nil {
			return Review{}, fmt.Errorf("governance: update review (partial): %w", err)
		}
	}

	// Audit entry — even partial approvals get a hash-chain row so a
	// subsequent decision audit can be reconciled with the prior vote.
	deciderCp := decider
	auditPayload := map[string]any{
		"review_id":     reviewID.String(),
		"asset":         assetName,
		"code_hash":     codeHash,
		"actor_id":      decider.String(),
		"comment":       comment,
		"terminal":      terminal,
		"new_status":    newStatus,
		"reviewer_pool": pool,
	}
	if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    auditEvent,
		OccurredAt:   time.Now().UTC(),
		ActorID:      &deciderCp,
		ResourceType: "governance_review",
		ResourceID:   reviewID.String(),
		Payload:      auditPayload,
	}); err != nil {
		return Review{}, fmt.Errorf("governance: audit decision: %w", err)
	}

	// Notification dispatch to submitter (and, on terminal, all reviewers).
	if w.queue != nil {
		args := notification.NotificationDispatchArgs{
			EventType:  string(auditEvent),
			AssetName:  assetName,
			Payload:    auditPayload,
			Recipients: append([]string{}, submitterID.String()),
			WebhookID:  reviewID.String() + ":decide",
		}
		_ = w.queue.InsertTx(ctx, tx, args)
	}

	if err := tx.Commit(); err != nil {
		return Review{}, fmt.Errorf("governance: commit tx: %w", err)
	}
	rollback = false

	// Return read-shape; re-query for a clean snapshot.
	return w.Get(ctx, reviewID)
}

// ===== Reassign =====

// Reassign rotates the reviewer_pool_snapshot for an in-flight review (Pitfall #12).
// The change is recorded in event_log (governance.reviewer_reassigned) — operational,
// not hash-chain. The next approve/reject decision writes the audit_log entry.
//
// actor MUST be a privileged caller (admin role). The handler enforces via
// RequirePermission("/governance/reviews/*","manage").
func (w *Workflow) Reassign(ctx context.Context, reviewID uuid.UUID, newReviewers []string, actor uuid.UUID) (Review, error) {
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return Review{}, fmt.Errorf("governance: begin tx: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	var (
		decidedAt *time.Time
		oldJSON   []byte
	)
	err = tx.QueryRowContext(ctx, `
		SELECT decided_at, reviewer_pool_snapshot FROM governance_reviews
		 WHERE id=$1 FOR UPDATE
	`, reviewID).Scan(&decidedAt, &oldJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return Review{}, ErrReviewNotFound
	}
	if err != nil {
		return Review{}, fmt.Errorf("governance: lock review: %w", err)
	}
	if decidedAt != nil {
		return Review{}, ErrAlreadyDecided
	}

	var oldPool ReviewerPool
	_ = json.Unmarshal(oldJSON, &oldPool)

	newPool := oldPool
	newPool.Roles = dedupRoles(newReviewers)
	if newPool.Source == nil {
		newPool.Source = []string{}
	}
	newPool.Source = append(newPool.Source, "reassigned-by:"+actor.String())
	newJSON, _ := json.Marshal(newPool)
	if _, err := tx.ExecContext(ctx,
		`UPDATE governance_reviews SET reviewer_pool_snapshot=$1::jsonb WHERE id=$2`,
		string(newJSON), reviewID,
	); err != nil {
		return Review{}, fmt.Errorf("governance: rotate reviewer pool: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Review{}, fmt.Errorf("governance: commit tx: %w", err)
	}
	rollback = false

	// event_log entry (no hash-chain write; this is operational, not access-control).
	// Best-effort: handler is responsible for using the event Writer; CLI emits via slog only.

	return w.Get(ctx, reviewID)
}

// ===== Status / Get =====

// Status returns all reviews for the given asset (latest first). Pass empty
// asset to return ALL reviews (for status CLI without filter).
func (w *Workflow) Status(ctx context.Context, assetName string) ([]Review, error) {
	var rows *sql.Rows
	var err error
	if assetName == "" {
		rows, err = w.db.QueryContext(ctx, statusBaseSQL+` ORDER BY submitted_at DESC LIMIT 200`)
	} else {
		rows, err = w.db.QueryContext(ctx, statusBaseSQL+` WHERE asset = $1 ORDER BY submitted_at DESC LIMIT 200`, assetName)
	}
	if err != nil {
		return nil, fmt.Errorf("governance: status query: %w", err)
	}
	defer rows.Close()
	out := make([]Review, 0, 16)
	for rows.Next() {
		r, err := scanReview(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns the review row by id.
func (w *Workflow) Get(ctx context.Context, reviewID uuid.UUID) (Review, error) {
	row := w.db.QueryRowContext(ctx, statusBaseSQL+` WHERE id = $1`, reviewID)
	r, err := scanReviewRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Review{}, ErrReviewNotFound
	}
	return r, err
}

// ===== helpers =====

const statusBaseSQL = `
	SELECT id, asset_version_id, asset, code_hash, submitter_id, submitted_at,
	       reviewer_pool_snapshot, quorum, require_human_review, escalation_roles,
	       status, decided_at, decided_by_id, COALESCE(comment,''), sla_breach_emitted_at
	  FROM governance_reviews`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanReview(r *sql.Rows) (Review, error) {
	return scanReviewRow(r)
}

func scanReviewRow(s rowScanner) (Review, error) {
	var (
		r            Review
		poolJSON     []byte
		escJSON      []byte
		decidedAt    sql.NullTime
		decidedBy    uuid.NullUUID
		slaEmittedAt sql.NullTime
	)
	if err := s.Scan(
		&r.ID, &r.AssetVersionID, &r.Asset, &r.CodeHash, &r.SubmitterID, &r.SubmittedAt,
		&poolJSON, &r.Quorum, &r.RequireHumanReview, &escJSON,
		&r.Status, &decidedAt, &decidedBy, &r.Comment, &slaEmittedAt,
	); err != nil {
		return Review{}, err
	}
	_ = json.Unmarshal(poolJSON, &r.ReviewerPool)
	_ = json.Unmarshal(escJSON, &r.EscalationRoles)
	if decidedAt.Valid {
		t := decidedAt.Time
		r.DecidedAt = &t
	}
	if decidedBy.Valid {
		u := decidedBy.UUID
		r.DecidedByID = &u
	}
	if slaEmittedAt.Valid {
		t := slaEmittedAt.Time
		r.SLABreachEmittedAt = &t
	}
	r.ApprovalsSoFar = countApprovals(r.Comment)
	return r, nil
}

// countApprovals scans a stored comment for the structured "[approved by <uuid>]"
// token. The token shape is produced exclusively by decide() — free-form reject
// comments containing the substring "approved by" no longer count as approvals
// (CR-03 hardening). Quick & dirty v1: each line whose prefix matches the
// canonical token counts as one vote.
func countApprovals(comment string) int {
	if comment == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(comment, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "[approved by ") {
			count++
		}
	}
	return count
}

// hasPriorVoteByDecider returns true if the comment ledger already contains an
// "[approved by <decider>]" or "[rejected by <decider>]" token for the given
// decider id. Used by decide() to enforce one-vote-per-decider (CR-03).
func hasPriorVoteByDecider(comment string, decider uuid.UUID) bool {
	if comment == "" {
		return false
	}
	approveToken := "[approved by " + decider.String() + "]"
	rejectToken := "[rejected by " + decider.String() + "]"
	for _, line := range strings.Split(comment, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, approveToken) || strings.HasPrefix(trimmed, rejectToken) {
			return true
		}
	}
	return false
}

// normaliseQuorum maps QuorumAll (-1) to len(roles). Used at INSERT time so
// the DB stores a positive integer for downstream queries.
func normaliseQuorum(q, poolSize int) int {
	if q == int(asset.QuorumAll) {
		if poolSize < 1 {
			return 1
		}
		return poolSize
	}
	if q < 1 {
		return 1
	}
	return q
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}
