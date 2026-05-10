package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kanpon/data-governance/internal/asset"
)

// jsonUnmarshalLenient is encoding/json.Unmarshal that tolerates empty input
// (returns success with the zero value preserved). Used for config_json /
// columns JSONB columns that may legitimately be empty.
func jsonUnmarshalLenient(b []byte, v any) error {
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// Decision is the outcome of the 5-check pipeline (D-10).
type Decision int

const (
	// DecisionAutoApproved means all 5 checks passed AND the asset has no PII
	// columns AND RequireHumanReview() is false. The Workflow may transition
	// directly to governance_state='active' and emit governance.auto_approved.
	DecisionAutoApproved Decision = iota + 1
	// DecisionMustHumanReview means all 5 checks passed (or only the PII-presence
	// "soft" rule fired) but the asset still requires human review (PII present
	// OR RequireHumanReview() set). The reviewer pool MUST include the
	// privacy-team role when PII triggers this path.
	DecisionMustHumanReview
	// DecisionBlocked means at least one of the 5 checks rejected the submission.
	// The Workflow still creates the governance_reviews row in status=in_review
	// so the reviewer can see the issue and address it.
	DecisionBlocked
)

// String returns the canonical lower-snake string form of a Decision.
func (d Decision) String() string {
	switch d {
	case DecisionAutoApproved:
		return "auto_approved"
	case DecisionMustHumanReview:
		return "must_human_review"
	case DecisionBlocked:
		return "blocked"
	default:
		return "unknown"
	}
}

// CheckResult is the structured outcome of AutoApprovalChecker.Check.
// Reason is a short machine-readable code for the failing check (or empty
// when Decision == DecisionAutoApproved). FailedChecks lists every check
// that did NOT pass — the Workflow records them in the audit payload so a
// reviewer can see ALL outstanding issues, not just the first one.
type CheckResult struct {
	Decision     Decision
	Reason       string
	FailedChecks []string
}

// AutoApprovalChecker runs the 5-check pipeline (D-10) against the live DB.
// All probes use simple SELECTs against tables managed by other Phase 5
// plans (schema_changes, column_policies, quality_rules, asset_versions).
// Missing tables (e.g. quality_rules absent because Plan 05-05 has not
// landed yet) are treated as "no rules to validate" — fail-open per
// Pitfall #11 ("metadata-failures don't fail data work").
type AutoApprovalChecker struct {
	db *sql.DB
}

// NewAutoApprovalChecker constructs a checker bound to db. db must be the
// platform_app connection so RLS + grants are honoured.
func NewAutoApprovalChecker(db *sql.DB) *AutoApprovalChecker {
	return &AutoApprovalChecker{db: db}
}

// Check runs the pipeline. Returns the decision plus a defensive list of
// failing checks. Errors are returned only for true infra faults (DB down);
// well-formed "blocked" rejects are encoded in CheckResult, never as errors.
//
// Order of checks (D-10):
//
//  1. unack breaking schema_changes for this asset → Blocked
//  2. PII columns without column_policies row     → Blocked
//  3. quality_rules whose Column missing from schema → Blocked (best-effort)
//  4. asset_versions.drift_status='pending'        → Blocked
//  5. PII presence (soft)                          → MustHumanReview
//
// Step 5 is the only "soft" rule — it does NOT block but forces human review.
// asset.RequireHumanReview() ALSO forces the human path even when 1-5 pass.
func (c *AutoApprovalChecker) Check(
	ctx context.Context,
	a *asset.Asset,
	codeHash string,
	owner string,
	tags []string,
) (CheckResult, error) {
	res := CheckResult{Decision: DecisionAutoApproved}

	// 1. Unack breaking schema change.
	hasBreak, err := c.hasUnackBreakingSchemaChange(ctx, a.Name())
	if err != nil {
		return res, fmt.Errorf("auto_approval: schema_changes probe: %w", err)
	}
	if hasBreak {
		res.Decision = DecisionBlocked
		res.Reason = "unacknowledged breaking schema change"
		res.FailedChecks = append(res.FailedChecks, "schema_break_ack")
	}

	// 2. PII columns without column_policies row.
	hasPII, missingPolicy, err := c.piiColumnsCoverage(ctx, a.Name())
	if err != nil {
		return res, fmt.Errorf("auto_approval: column_policies probe: %w", err)
	}
	if missingPolicy {
		res.Decision = DecisionBlocked
		if res.Reason == "" {
			res.Reason = "PII column without policy"
		}
		res.FailedChecks = append(res.FailedChecks, "pii_policy_consistency")
	}

	// 3. quality_rules row references column missing from schema_versions.
	missingCol, err := c.qualityRulesReferenceMissingColumn(ctx, a.Name(), codeHash)
	if err != nil {
		return res, fmt.Errorf("auto_approval: quality_rules probe: %w", err)
	}
	if missingCol != "" {
		res.Decision = DecisionBlocked
		if res.Reason == "" {
			res.Reason = "quality rule references missing column: " + missingCol
		}
		res.FailedChecks = append(res.FailedChecks, "quality_config_sanity")
	}

	// 4. asset_versions drift pending.
	driftPending, err := c.driftPending(ctx, a.Name(), codeHash)
	if err != nil {
		return res, fmt.Errorf("auto_approval: drift probe: %w", err)
	}
	if driftPending {
		res.Decision = DecisionBlocked
		if res.Reason == "" {
			res.Reason = "lineage drift unacknowledged"
		}
		res.FailedChecks = append(res.FailedChecks, "lineage_drift")
	}

	// 5. PII presence — soft rule. Only fires when 1-4 passed.
	if res.Decision == DecisionAutoApproved && hasPII {
		res.Decision = DecisionMustHumanReview
		res.Reason = "PII columns require human review"
		res.FailedChecks = append(res.FailedChecks, "pii_presence")
	}

	// RequireHumanReview override — even when 1-5 pass.
	if a.RequireHumanReview() && res.Decision == DecisionAutoApproved {
		res.Decision = DecisionMustHumanReview
		res.Reason = "Builder.RequireHumanReview() forces human path"
		res.FailedChecks = append(res.FailedChecks, "require_human_review")
	}

	return res, nil
}

// hasUnackBreakingSchemaChange returns true when at least one schema_changes
// row for the asset has change_type ∈ {column_dropped, type_narrowed,
// nullable_removed, pk_changed} AND acknowledged_at IS NULL.
func (c *AutoApprovalChecker) hasUnackBreakingSchemaChange(ctx context.Context, assetName string) (bool, error) {
	var count int
	err := c.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM schema_changes
		 WHERE asset = $1
		   AND change_type IN ('column_dropped','type_narrowed','nullable_removed','pk_changed')
		   AND acknowledged_at IS NULL
	`, assetName).Scan(&count)
	if err != nil {
		// schema_changes table may not exist in some envs (e.g. unit tests w/o phase 4 migration)
		if isUndefinedTable(err) {
			return false, nil
		}
		return false, err
	}
	return count > 0, nil
}

// piiColumnsCoverage returns (hasAnyPIIColumn, somePIIWithoutPolicy).
//
// hasAnyPIIColumn — true when any column on this asset carries the "pii" tag
// in asset_metadata.
// somePIIWithoutPolicy — true when at least one of those PII columns lacks a
// column_policies row (any source, superseded_at IS NULL).
//
// The check uses asset_metadata.tags JSONB array (Phase 4 D-17 pattern).
// Missing tables short-circuit to (false, false).
func (c *AutoApprovalChecker) piiColumnsCoverage(ctx context.Context, assetName string) (bool, bool, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT DISTINCT column_name FROM asset_metadata
		 WHERE asset = $1
		   AND column_name IS NOT NULL
		   AND tags ? 'pii'
	`, assetName)
	if err != nil {
		if isUndefinedTable(err) {
			return false, false, nil
		}
		return false, false, err
	}
	defer rows.Close()

	var piiCols []string
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			return false, false, err
		}
		piiCols = append(piiCols, col)
	}
	if err := rows.Err(); err != nil {
		return false, false, err
	}
	if len(piiCols) == 0 {
		return false, false, nil
	}

	// For each PII column, verify a column_policies row exists.
	for _, col := range piiCols {
		var has bool
		err := c.db.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM column_policies
			 WHERE asset = $1 AND column_name = $2 AND superseded_at IS NULL)
		`, assetName, col).Scan(&has)
		if err != nil {
			if isUndefinedTable(err) {
				// column_policies table missing — be conservative: PII present, missing policy.
				return true, true, nil
			}
			return true, false, err
		}
		if !has {
			return true, true, nil
		}
	}
	return true, false, nil
}

// qualityRulesReferenceMissingColumn returns the first column referenced by
// a quality_rules row that does NOT appear in this asset's latest declared
// schema. Returns empty string when all rules reference present columns.
//
// Implementation note: schema_versions stores column listings as JSONB; the
// check is deferred to the rules' config_json field's "column" key for
// null_check / range_check. sql_assertion rules are skipped (they reference
// arbitrary expressions, not declared columns). Returns "" when
// quality_rules table is missing (Plan 05-05 not yet landed).
func (c *AutoApprovalChecker) qualityRulesReferenceMissingColumn(ctx context.Context, assetName, codeHash string) (string, error) {
	rows, err := c.db.QueryContext(ctx, `
		SELECT rule_type, config_json FROM quality_rules
		 WHERE asset = $1 AND code_hash = $2
		   AND rule_type IN ('null_check','range_check')
	`, assetName, codeHash)
	if err != nil {
		if isUndefinedTable(err) {
			return "", nil
		}
		return "", err
	}
	defer rows.Close()

	type cfg struct {
		Column string `json:"column"`
	}
	var refs []string
	for rows.Next() {
		var rt string
		var raw []byte
		if err := rows.Scan(&rt, &raw); err != nil {
			return "", err
		}
		var c cfg
		if err := jsonUnmarshalLenient(raw, &c); err != nil {
			continue
		}
		if c.Column != "" {
			refs = append(refs, c.Column)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return "", nil
	}

	// Look up the latest declared columns for the asset (Phase 4 schema_versions).
	declared, err := c.declaredColumns(ctx, assetName, codeHash)
	if err != nil {
		// Schema not captured yet — best-effort, skip column-existence check.
		return "", nil
	}
	declaredSet := make(map[string]struct{}, len(declared))
	for _, d := range declared {
		declaredSet[d] = struct{}{}
	}
	for _, r := range refs {
		if _, ok := declaredSet[r]; !ok {
			return r, nil
		}
	}
	return "", nil
}

// declaredColumns returns the column names from the most-recent schema_versions
// row for (asset, code_hash). Returns sql.ErrNoRows-like error wrapping when
// no row exists or the table is missing.
func (c *AutoApprovalChecker) declaredColumns(ctx context.Context, assetName, codeHash string) ([]string, error) {
	var raw []byte
	err := c.db.QueryRowContext(ctx, `
		SELECT columns FROM schema_versions
		 WHERE asset = $1 AND code_hash = $2
		 ORDER BY captured_at DESC
		 LIMIT 1
	`, assetName, codeHash).Scan(&raw)
	if err != nil {
		return nil, err
	}
	type colRow struct {
		Name string `json:"name"`
	}
	var rows []colRow
	if err := jsonUnmarshalLenient(raw, &rows); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out, nil
}

// driftPending returns true when the asset_versions row for (asset, code_hash)
// has drift_status = 'pending'. Missing rows or missing columns short-circuit
// to false (fail-open per Pitfall #11).
func (c *AutoApprovalChecker) driftPending(ctx context.Context, assetName, codeHash string) (bool, error) {
	var s string
	err := c.db.QueryRowContext(ctx, `
		SELECT drift_status FROM asset_versions
		 WHERE asset = $1 AND code_hash = $2
		 ORDER BY created_at DESC
		 LIMIT 1
	`, assetName, codeHash).Scan(&s)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || isUndefinedTable(err) {
			return false, nil
		}
		return false, err
	}
	return s == "pending", nil
}

// isUndefinedTable reports whether err is a Postgres "undefined_table" /
// "undefined_column" error. We match on substring rather than importing pgx
// errcodes — this keeps the package free of pgx imports for testability.
func isUndefinedTable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "undefined_table") ||
		strings.Contains(msg, "undefined_column")
}
