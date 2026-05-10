// Package policy implements the Phase 5 column-level access policy store
// (D-02 / D-04 / D-07; RBAC-03/04). Three policy layers are persisted into
// the column_policies temporal table:
//
//   - source='builder'      — declared via asset.Builder.ColumnPolicy; written
//     at registration time / on every code_hash change. Resolve() precedence: 2.
//   - source='runtime'      — REST PATCH /assets/.../columns/.../policy override.
//     Resolve() precedence: 1 (highest).
//   - source='yaml-default' — tag→mask defaults loaded from policies.yaml.
//     Resolve() precedence: 3 (lowest).
//
// All mutations write a policy.changed (or policy.removed) entry to the
// hash-chain audit log inside the same transaction as the column_policies
// write — atomicity guarantee per Phase 5 D-13.
//
// Patch additionally enqueues a River PolicySyncArgs job for the Snowflake /
// BigQuery worker (internal/policy/sync_job.go) — the enqueue happens via the
// same *sql.Tx so an audit/store rollback also rolls back the queued job.
//
// Storage choice: allow_roles uses JSONB (matching Phase 4's asset_metadata.tags
// pattern) rather than TEXT[], so pgx encodes via the stdlib json.Marshal path
// without depending on lib/pq.
package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/connector"
)

// Sentinel errors.
var (
	// ErrReasonRequired is returned by Patch when the supplied reason is empty.
	// Per RBAC-03 every runtime override must carry a non-empty reason which
	// the audit chain captures alongside the before/after diff.
	ErrReasonRequired = errors.New("policy: reason required for runtime override")

	// ErrInvalidMask is returned by Patch / Apply when an unknown MaskType is
	// supplied. Connectors only support hash / redact / partial.
	ErrInvalidMask = errors.New("policy: invalid mask type")

	// ErrPolicyNotFound is returned by Resolve when neither a runtime,
	// builder, nor yaml-default row exists for (asset, column).
	ErrPolicyNotFound = errors.New("policy: not found")
)

// Store is the policy CRUD facade backed by Postgres. Patch opens its own
// transaction (audit + sync enqueue MUST be atomic with the column_policies
// write); Apply takes the caller's transaction so it can compose with the
// asset_versions / lineage_writer commit boundary.
type Store struct {
	db *sql.DB
	// enqueueSyncJob is an optional hook called inside Patch's transaction to
	// queue a River PolicySyncArgs job. The signature decouples this package
	// from the river runtime so tests can supply a no-op or a recorder.
	// Production wiring (cmd/platform) plugs in the real client.
	enqueueSyncJob SyncEnqueuer
}

// SyncEnqueuer is the abstract interface for enqueueing a sync job from
// inside an arbitrary *sql.Tx. The concrete River-backed implementation
// lives in cmd/platform; tests pass a recording stub.
//
// Implementations MUST honour the supplied tx so that an audit/store rollback
// also rolls back the queued job (transactional enqueue is one of River's
// headline guarantees).
type SyncEnqueuer interface {
	EnqueueSync(ctx context.Context, tx *sql.Tx, args PolicySyncArgs) error
}

// noopEnqueuer is the default SyncEnqueuer used when none is supplied —
// handy for unit tests that exercise Patch without booting a River client.
type noopEnqueuer struct{}

func (noopEnqueuer) EnqueueSync(_ context.Context, _ *sql.Tx, _ PolicySyncArgs) error { return nil }

// NewStore returns a Store that writes column_policies via db and audit
// entries via the same transaction. enqueuer may be nil — a no-op is used.
func NewStore(db *sql.DB, enqueuer SyncEnqueuer) *Store {
	if enqueuer == nil {
		enqueuer = noopEnqueuer{}
	}
	return &Store{db: db, enqueueSyncJob: enqueuer}
}

// PolicySyncArgs is exported from this package so the River worker
// (internal/policy/sync_job.go) can register its job kind. Defining it here
// avoids an import cycle between the worker and the store.
type PolicySyncArgs struct {
	Asset  string `json:"asset"`
	Column string `json:"column"`
	Reason string `json:"reason,omitempty"`
}

// Effective is the resolved active policy returned by Resolve.
type Effective struct {
	Asset           string
	Column          string
	Mask            connector.MaskType
	AllowRoles      []string
	Source          string // builder | runtime | yaml-default
	CodeHashLatest  string
	EnforcementMode string
	Reason          string
}

// Active is one row of the column_policies active list (List).
type Active struct {
	Asset           string
	Column          string
	Mask            connector.MaskType
	AllowRoles      []string
	Source          string
	CodeHashFirst   string
	CodeHashLatest  string
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	Reason          string
	EnforcementMode string
	SyncStatus      string
}

// rolesJSON marshals a []string (possibly nil) to a JSONB-compatible
// byte slice. nil → "[]" so the column NOT NULL DEFAULT '[]' is preserved.
func rolesJSON(roles []string) ([]byte, error) {
	if roles == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(roles)
}

// scanRoles decodes a JSONB column into []string. Empty / null → nil slice.
func scanRoles(b []byte) ([]string, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("policy: scan allow_roles: %w", err)
	}
	return out, nil
}

// Apply syncs builder-default ColumnPolicies for the given asset+code_hash.
// Idempotent: re-applying the same policies is a no-op (LAST_SEEN_AT bump).
// Removed columns (present previously, absent now) are soft-retired with
// superseded_at = NOW() and emit policy.removed audit entries.
//
// The caller controls the transaction — typically the lineage_writer hook
// in Phase 4's CaptureRun path passes its tx so column_policies, audit_log,
// and asset_versions all commit/rollback as one.
func (s *Store) Apply(ctx context.Context, tx *sql.Tx, assetName, codeHash string, runID *uuid.UUID, policies []asset.ColumnPolicy) error {
	if assetName == "" {
		return errors.New("policy: assetName required")
	}
	if codeHash == "" {
		return errors.New("policy: codeHash required")
	}
	for _, p := range policies {
		if !p.Mask.IsValid() {
			return fmt.Errorf("%w: %q", ErrInvalidMask, p.Mask)
		}
	}

	// 1. Read existing active builder rows for this asset.
	rows, err := tx.QueryContext(ctx, `
		SELECT column_name, mask_type, allow_roles, code_hash_first, code_hash_latest
		  FROM column_policies
		 WHERE asset = $1 AND source = 'builder' AND superseded_at IS NULL
	`, assetName)
	if err != nil {
		return fmt.Errorf("policy: apply read existing: %w", err)
	}
	type existing struct {
		mask           string
		allowRoles     []string
		codeHashFirst  string
		codeHashLatest string
	}
	have := map[string]existing{}
	for rows.Next() {
		var col, mask, cf, cl string
		var rolesBytes []byte
		if err := rows.Scan(&col, &mask, &rolesBytes, &cf, &cl); err != nil {
			rows.Close()
			return fmt.Errorf("policy: apply scan existing: %w", err)
		}
		roles, err := scanRoles(rolesBytes)
		if err != nil {
			rows.Close()
			return err
		}
		have[col] = existing{mask: mask, allowRoles: roles, codeHashFirst: cf, codeHashLatest: cl}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("policy: apply iter: %w", err)
	}
	rows.Close()

	// 2. Build the want set keyed by column.
	want := map[string]asset.ColumnPolicy{}
	for _, p := range policies {
		want[p.Column] = p
	}

	// 3. Detect removed columns (have - want) and soft-retire them.
	for col, ex := range have {
		if _, stillThere := want[col]; stillThere {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE column_policies SET superseded_at = NOW()
			 WHERE asset = $1 AND column_name = $2 AND source = 'builder' AND superseded_at IS NULL
		`, assetName, col); err != nil {
			return fmt.Errorf("policy: apply soft-retire %s: %w", col, err)
		}
		if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
			EventType:    audit.AuditPolicyRemoved,
			OccurredAt:   time.Now().UTC(),
			ResourceType: "column_policy",
			ResourceID:   assetName + "." + col,
			Payload: map[string]any{
				"asset":      assetName,
				"column":     col,
				"source":     "builder",
				"prior_mask": ex.mask,
				"reason":     "removed-from-builder",
			},
		}); err != nil {
			return fmt.Errorf("policy: apply audit removed %s: %w", col, err)
		}
	}

	// 4. UPSERT each desired policy.
	for col, p := range want {
		ex, hadIt := have[col]
		newRoles := append([]string(nil), p.AllowRoles...)
		if hadIt && ex.mask == string(p.Mask) && stringSlicesEqual(ex.allowRoles, newRoles) {
			// (a) idempotent reapplication.
			if _, err := tx.ExecContext(ctx, `
				UPDATE column_policies SET last_seen_at = NOW(), code_hash_latest = $1
				 WHERE asset = $2 AND column_name = $3 AND source = 'builder' AND superseded_at IS NULL
			`, codeHash, assetName, col); err != nil {
				return fmt.Errorf("policy: apply touch %s: %w", col, err)
			}
			continue
		}
		// (b) insert new row, soft-retire prior if any.
		if hadIt {
			if _, err := tx.ExecContext(ctx, `
				UPDATE column_policies SET superseded_at = NOW()
				 WHERE asset = $1 AND column_name = $2 AND source = 'builder' AND superseded_at IS NULL
			`, assetName, col); err != nil {
				return fmt.Errorf("policy: apply soft-retire-replace %s: %w", col, err)
			}
		}
		var firstRunID any
		if runID != nil {
			firstRunID = *runID
		}
		codeFirst := codeHash
		if hadIt {
			codeFirst = ex.codeHashFirst
		}
		rolesB, err := rolesJSON(newRoles)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO column_policies (
			    asset, column_name, mask_type, allow_roles,
			    code_hash_first, code_hash_latest, first_seen_run_id,
			    source, reason, enforcement_mode
			) VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, 'builder', '', 'unknown')
		`, assetName, col, string(p.Mask), string(rolesB), codeFirst, codeHash, firstRunID); err != nil {
			return fmt.Errorf("policy: apply insert %s: %w", col, err)
		}
		// Emit policy.changed for every (re)applied builder row.
		before := map[string]any{}
		if hadIt {
			before = map[string]any{"mask": ex.mask, "allow_roles": ex.allowRoles}
		}
		if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
			EventType:    audit.AuditPolicyChanged,
			OccurredAt:   time.Now().UTC(),
			ResourceType: "column_policy",
			ResourceID:   assetName + "." + col,
			Payload: map[string]any{
				"asset":   assetName,
				"column":  col,
				"source":  "builder",
				"before":  before,
				"after":   map[string]any{"mask": string(p.Mask), "allow_roles": newRoles},
				"reason":  "builder-default",
			},
		}); err != nil {
			return fmt.Errorf("policy: apply audit changed %s: %w", col, err)
		}
	}
	return nil
}

// Patch applies a runtime override for (asset, column). It opens its own
// transaction, soft-retires any prior runtime row, inserts the new one,
// writes a policy.changed audit entry, enqueues a River sync job, and commits.
// Returns the post-mutation effective policy.
func (s *Store) Patch(ctx context.Context, actorID uuid.UUID, assetName, column string, mask connector.MaskType, allowRoles []string, reason string) (Effective, error) {
	if reason == "" {
		return Effective{}, ErrReasonRequired
	}
	if !mask.IsValid() {
		return Effective{}, fmt.Errorf("%w: %q", ErrInvalidMask, mask)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Effective{}, fmt.Errorf("policy: patch begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Read existing active runtime row for diff context.
	var beforeMask string
	var beforeRolesB []byte
	hadPrior := true
	err = tx.QueryRowContext(ctx, `
		SELECT mask_type, allow_roles
		  FROM column_policies
		 WHERE asset = $1 AND column_name = $2 AND source = 'runtime' AND superseded_at IS NULL
	`, assetName, column).Scan(&beforeMask, &beforeRolesB)
	if errors.Is(err, sql.ErrNoRows) {
		hadPrior = false
	} else if err != nil {
		return Effective{}, fmt.Errorf("policy: patch read prior: %w", err)
	}
	beforeRoles, err := scanRoles(beforeRolesB)
	if err != nil {
		return Effective{}, err
	}

	// 2. Resolve a code_hash anchor for the row. Use the asset's most recent
	//    asset_versions row if any; otherwise empty string is acceptable.
	var codeHashLatest sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT code_hash FROM asset_versions WHERE asset = $1 ORDER BY created_at DESC LIMIT 1
	`, assetName).Scan(&codeHashLatest); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Effective{}, fmt.Errorf("policy: patch read asset_version: %w", err)
	}
	codeAnchor := ""
	if codeHashLatest.Valid {
		codeAnchor = codeHashLatest.String
	}

	// 3. Soft-retire prior runtime row.
	if hadPrior {
		if _, err := tx.ExecContext(ctx, `
			UPDATE column_policies SET superseded_at = NOW()
			 WHERE asset = $1 AND column_name = $2 AND source = 'runtime' AND superseded_at IS NULL
		`, assetName, column); err != nil {
			return Effective{}, fmt.Errorf("policy: patch retire prior: %w", err)
		}
	}

	// 4. Insert new runtime row.
	roles := append([]string(nil), allowRoles...)
	rolesB, err := rolesJSON(roles)
	if err != nil {
		return Effective{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO column_policies (
		    asset, column_name, mask_type, allow_roles,
		    code_hash_first, code_hash_latest,
		    source, reason, enforcement_mode, sync_status, created_by_id
		) VALUES ($1, $2, $3, $4::jsonb, $5, $5, 'runtime', $6, 'unknown', 'pending', $7)
	`, assetName, column, string(mask), string(rolesB), codeAnchor, reason, actorID); err != nil {
		return Effective{}, fmt.Errorf("policy: patch insert: %w", err)
	}

	// 5. Recompute effective policy AFTER the insert (within tx so it sees the new row).
	eff, err := s.resolveTx(ctx, tx, assetName, column)
	if err != nil {
		return Effective{}, fmt.Errorf("policy: patch resolve: %w", err)
	}

	// 6. Write hash-chain audit entry.
	beforePayload := map[string]any{}
	if hadPrior {
		beforePayload = map[string]any{"mask": beforeMask, "allow_roles": beforeRoles}
	}
	if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    audit.AuditPolicyChanged,
		OccurredAt:   time.Now().UTC(),
		ActorID:      &actorID,
		ResourceType: "column_policy",
		ResourceID:   assetName + "." + column,
		Payload: map[string]any{
			"asset":   assetName,
			"column":  column,
			"source":  "runtime",
			"before":  beforePayload,
			"after":   map[string]any{"mask": string(mask), "allow_roles": roles},
			"reason":  reason,
		},
	}); err != nil {
		return Effective{}, fmt.Errorf("policy: patch audit: %w", err)
	}

	// 7. Enqueue River sync job inside the same tx.
	if err := s.enqueueSyncJob.EnqueueSync(ctx, tx, PolicySyncArgs{Asset: assetName, Column: column, Reason: reason}); err != nil {
		return Effective{}, fmt.Errorf("policy: patch enqueue: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Effective{}, fmt.Errorf("policy: patch commit: %w", err)
	}
	return eff, nil
}

// Delete soft-retires the active runtime row for (asset, column) and writes
// a policy.removed audit entry. It does NOT remove builder/yaml-default
// rows — those are always derived from upstream sources.
func (s *Store) Delete(ctx context.Context, actorID uuid.UUID, assetName, column, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("policy: delete begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE column_policies SET superseded_at = NOW()
		 WHERE asset = $1 AND column_name = $2 AND source = 'runtime' AND superseded_at IS NULL
	`, assetName, column)
	if err != nil {
		return fmt.Errorf("policy: delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPolicyNotFound
	}
	if _, err := audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    audit.AuditPolicyRemoved,
		OccurredAt:   time.Now().UTC(),
		ActorID:      &actorID,
		ResourceType: "column_policy",
		ResourceID:   assetName + "." + column,
		Payload: map[string]any{
			"asset":  assetName,
			"column": column,
			"source": "runtime",
			"reason": reason,
		},
	}); err != nil {
		return fmt.Errorf("policy: delete audit: %w", err)
	}
	if err := s.enqueueSyncJob.EnqueueSync(ctx, tx, PolicySyncArgs{Asset: assetName, Column: column, Reason: "delete"}); err != nil {
		return fmt.Errorf("policy: delete enqueue: %w", err)
	}
	return tx.Commit()
}

// Resolve returns the effective policy via COALESCE precedence:
// runtime > builder > yaml-default. Returns ErrPolicyNotFound when no row
// exists at any layer.
func (s *Store) Resolve(ctx context.Context, assetName, column string) (Effective, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Effective{}, fmt.Errorf("policy: resolve begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	return s.resolveTx(ctx, tx, assetName, column)
}

// resolveTx is the inside-transaction Resolve used by Patch / Resolve.
// Precedence implemented via three SELECTs returning the first non-empty.
func (s *Store) resolveTx(ctx context.Context, tx *sql.Tx, assetName, column string) (Effective, error) {
	for _, src := range []string{"runtime", "builder", "yaml-default"} {
		var (
			mask    string
			rolesB  []byte
			codeLat string
			mode    string
			reason  string
		)
		err := tx.QueryRowContext(ctx, `
			SELECT mask_type, allow_roles, code_hash_latest, enforcement_mode, reason
			  FROM column_policies
			 WHERE asset = $1 AND column_name = $2 AND source = $3 AND superseded_at IS NULL
			 ORDER BY first_seen_at DESC
			 LIMIT 1
		`, assetName, column, src).Scan(&mask, &rolesB, &codeLat, &mode, &reason)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return Effective{}, err
		}
		roles, err := scanRoles(rolesB)
		if err != nil {
			return Effective{}, err
		}
		return Effective{
			Asset:           assetName,
			Column:          column,
			Mask:            connector.MaskType(mask),
			AllowRoles:      roles,
			Source:          src,
			CodeHashLatest:  codeLat,
			EnforcementMode: mode,
			Reason:          reason,
		}, nil
	}
	return Effective{}, ErrPolicyNotFound
}

// List enumerates active policy rows for assetName. If sourceFilter is empty,
// returns all sources; otherwise only rows with that source.
func (s *Store) List(ctx context.Context, assetName, sourceFilter string) ([]Active, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT asset, column_name, mask_type, allow_roles, source,
		       code_hash_first, code_hash_latest, first_seen_at, last_seen_at,
		       reason, enforcement_mode, sync_status
		  FROM column_policies
		 WHERE asset = $1 AND superseded_at IS NULL
		   AND ($2 = '' OR source = $2)
		 ORDER BY column_name, source
	`, assetName, sourceFilter)
	if err != nil {
		return nil, fmt.Errorf("policy: list: %w", err)
	}
	defer rows.Close()
	var out []Active
	for rows.Next() {
		var a Active
		var maskStr string
		var rolesB []byte
		if err := rows.Scan(&a.Asset, &a.Column, &maskStr, &rolesB,
			&a.Source, &a.CodeHashFirst, &a.CodeHashLatest, &a.FirstSeenAt, &a.LastSeenAt,
			&a.Reason, &a.EnforcementMode, &a.SyncStatus); err != nil {
			return nil, fmt.Errorf("policy: list scan: %w", err)
		}
		a.Mask = connector.MaskType(maskStr)
		roles, err := scanRoles(rolesB)
		if err != nil {
			return nil, err
		}
		a.AllowRoles = roles
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetEnforcementMode updates the enforcement_mode for the active row at the
// given (asset, column) — called by the River sync worker after a successful
// Apply (warehouse-native) or when no MaskingProvisioner exists (in-pipeline).
//
// Updates ALL active rows (runtime/builder/yaml-default) so the reconciler
// reads consistent state across sources.
func (s *Store) SetEnforcementMode(ctx context.Context, assetName, column, mode string) error {
	switch mode {
	case "warehouse-native", "in-pipeline", "unknown":
	default:
		return fmt.Errorf("policy: invalid enforcement mode %q", mode)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE column_policies SET enforcement_mode = $1
		 WHERE asset = $2 AND column_name = $3 AND superseded_at IS NULL
	`, mode, assetName, column); err != nil {
		return fmt.Errorf("policy: set enforcement: %w", err)
	}
	return nil
}

// SetSyncStatus updates sync_status for the (asset, column) active rows.
// Status values: pending|syncing|synced|failed.
func (s *Store) SetSyncStatus(ctx context.Context, assetName, column, status string) error {
	switch status {
	case "pending", "syncing", "synced", "failed":
	default:
		return fmt.Errorf("policy: invalid sync_status %q", status)
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE column_policies SET sync_status = $1
		 WHERE asset = $2 AND column_name = $3 AND superseded_at IS NULL
	`, status, assetName, column); err != nil {
		return fmt.Errorf("policy: set sync_status: %w", err)
	}
	return nil
}

// ListAllAssets returns the distinct asset names that have at least one
// active column_policies row. Used by the reconciler to drive its scan loop.
func (s *Store) ListAllAssets(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT asset FROM column_policies WHERE superseded_at IS NULL ORDER BY asset
	`)
	if err != nil {
		return nil, fmt.Errorf("policy: list assets: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// stringSlicesEqual compares two []string for equality without sorting
// (caller should normalise if order is irrelevant). Used by Apply to detect
// idempotent re-applications.
func stringSlicesEqual(a, b []string) bool {
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

// ===== Phase 5 Plan 05-03 (RBAC-05) — MaskRulesForAsset =====

// MaskRule is the policy-store view of a single in-pipeline mask directive.
// It is shaped identically to asset.MaskRule but lives here to avoid
// introducing a circular import (asset.io_masking imports policy.Apply,
// so policy cannot return asset types).
//
// Callers (the executor) translate []policy.MaskRule into []asset.MaskRule
// before constructing the MaskingIO decorator.
type MaskRule struct {
	Column string
	Mask   connector.MaskType
	Reveal int
}

// MaskRulesForAsset returns the active mask rules the executor should apply
// in-pipeline to the asset's outbound rows. Two row classes contribute:
//
//  1. column_policies rows with enforcement_mode IN ('in-pipeline','unknown')
//     AND superseded_at IS NULL — the policy store has decided the column
//     needs masking. We pick the highest-precedence active row per column
//     (runtime > builder > yaml-default) so the source-precedence semantic
//     mirrors Resolve().
//  2. Columns with column_pii_tags.pii=TRUE that have NO active column_policy
//     row — the safe-default fallback path emits a slog WARN and applies
//     DefaultMaskForPII() (redact in v1).
//
// Behaviour for warehouse-native rows: enforcement_mode='warehouse-native'
// rows are excluded — the warehouse handles them and adding an in-pipeline
// pass would double-mask.
func (s *Store) MaskRulesForAsset(ctx context.Context, assetName string) ([]MaskRule, error) {
	if assetName == "" {
		return nil, errors.New("policy: assetName required")
	}

	// 1. Pick the highest-precedence active in-pipeline / unknown row per column.
	//    Postgres lateral-join would be more elegant but the table is small;
	//    a CTE that ranks by source precedence keeps the SQL portable.
	const inPipelineSQL = `
		WITH ranked AS (
		    SELECT column_name, mask_type, allow_roles, source, enforcement_mode,
		           CASE source
		               WHEN 'runtime'      THEN 1
		               WHEN 'builder'      THEN 2
		               WHEN 'yaml-default' THEN 3
		               ELSE 9
		           END AS rank
		      FROM column_policies
		     WHERE asset = $1
		       AND superseded_at IS NULL
		       AND enforcement_mode IN ('in-pipeline','unknown')
		),
		picked AS (
		    SELECT DISTINCT ON (column_name) column_name, mask_type
		      FROM ranked
		     ORDER BY column_name, rank
		)
		SELECT column_name, mask_type FROM picked
	`
	rows, err := s.db.QueryContext(ctx, inPipelineSQL, assetName)
	if err != nil {
		return nil, fmt.Errorf("policy: mask rules in-pipeline: %w", err)
	}
	defer rows.Close()

	out := make([]MaskRule, 0)
	covered := map[string]struct{}{}
	for rows.Next() {
		var col, mask string
		if err := rows.Scan(&col, &mask); err != nil {
			return nil, fmt.Errorf("policy: mask rules scan: %w", err)
		}
		mt := connector.MaskType(mask)
		if !mt.IsValid() {
			continue
		}
		out = append(out, MaskRule{Column: col, Mask: mt})
		covered[col] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("policy: mask rules iter: %w", err)
	}

	// 2. PII fallback: pii=true columns with no covering policy.
	const piiFallbackSQL = `
		SELECT t.column_name
		  FROM column_pii_tags t
		 WHERE t.asset = $1
		   AND t.pii = TRUE
		   AND NOT EXISTS (
		       SELECT 1 FROM column_policies cp
		        WHERE cp.asset = t.asset
		          AND cp.column_name = t.column_name
		          AND cp.superseded_at IS NULL
		   )
	`
	piiRows, err := s.db.QueryContext(ctx, piiFallbackSQL, assetName)
	if err != nil {
		return nil, fmt.Errorf("policy: pii fallback: %w", err)
	}
	defer piiRows.Close()
	for piiRows.Next() {
		var col string
		if err := piiRows.Scan(&col); err != nil {
			return nil, fmt.Errorf("policy: pii fallback scan: %w", err)
		}
		if _, alreadyCovered := covered[col]; alreadyCovered {
			continue
		}
		// slog.WARN: the column is pii but has no policy — applying default
		// redact. Operators see this in production logs so missing policies
		// are observable.
		slog.Warn("pii column without policy",
			"asset", assetName,
			"column", col,
			"fallback_mask", string(DefaultMaskForPII()),
		)
		out = append(out, MaskRule{Column: col, Mask: DefaultMaskForPII()})
	}
	if err := piiRows.Err(); err != nil {
		return nil, fmt.Errorf("policy: pii fallback iter: %w", err)
	}

	return out, nil
}
