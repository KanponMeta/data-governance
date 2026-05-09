// Snowflake Dynamic Data Masking (DDM) provisioner — implements
// connector.MaskingProvisioner for the Phase 5 plan 05-02 column-level
// access control feature (D-04, RBAC-03/04).
//
// Wire protocol per RESEARCH.md §2:
//
//	CREATE OR REPLACE MASKING POLICY "<db>"."<schema>"."dgp_mask_<table>_<column>"
//	    AS (val VARIANT) RETURNS VARIANT ->
//	    CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, ARRAY_CONSTRUCT(<roles>))
//	         THEN val ELSE TO_VARIANT(<mask-body>) END;
//	ALTER TABLE "<db>"."<schema>"."<table>"
//	    ALTER COLUMN "<column>" SET MASKING POLICY "<db>"."<schema>"."dgp_mask_<table>_<column>";
//
// Mask bodies:
//   - hash    : SHA2_HEX(TO_VARCHAR(val), 256)
//   - redact  : '***'
//   - partial : LEFT(TO_VARCHAR(val),2) || REPEAT('*', GREATEST(LENGTH(TO_VARCHAR(val))-4,0)) || RIGHT(TO_VARCHAR(val),2)
//
// Identifier quoting: every reference is fully-qualified
// "<db>"."<schema>"."<table>" or ..."<column>" so name collisions across
// databases never reach the warehouse (Pitfall #2).
//
// Asset identifier shape: connector.AssetRef.Identifier carries
// "DB.SCHEMA.TABLE" (matching the existing Snowflake connector convention).
package snowflake

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/kanpon/data-governance/internal/connector"
)

// Compile-time assertion that Snowflake satisfies MaskingProvisioner.
var _ connector.MaskingProvisioner = (*Snowflake)(nil)

// maskPolicyNamePrefix is the canonical prefix used by all DGP-managed
// masking policies. The suffix is "<table>_<column>" so the policy name is
// uniquely derivable from the (table, column) pair without lookup.
const maskPolicyNamePrefix = "dgp_mask_"

// snowflake DDL templates — exported for test assertions.
const (
	bodyTplHash    = `CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, ARRAY_CONSTRUCT(%s)) THEN val ELSE TO_VARIANT(SHA2_HEX(TO_VARCHAR(val), 256)) END`
	bodyTplRedact  = `CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, ARRAY_CONSTRUCT(%s)) THEN val ELSE TO_VARIANT('***') END`
	bodyTplPartial = `CASE WHEN ARRAY_CONTAINS(CURRENT_ROLE()::variant, ARRAY_CONSTRUCT(%s)) THEN val ELSE TO_VARIANT(LEFT(TO_VARCHAR(val),2) || REPEAT('*', GREATEST(LENGTH(TO_VARCHAR(val))-4, 0)) || RIGHT(TO_VARCHAR(val),2)) END`
)

// ApplyMaskingPolicy issues CREATE OR REPLACE MASKING POLICY + ALTER TABLE
// SET MASKING POLICY. Idempotent (CREATE OR REPLACE).
func (s *Snowflake) ApplyMaskingPolicy(ctx context.Context, ref connector.AssetRef, policy connector.ColumnPolicy) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !policy.MaskType.IsValid() {
		return fmt.Errorf("snowflake: invalid mask type %q", policy.MaskType)
	}
	db, schema, table, err := splitTriIdentifier(ref.Identifier)
	if err != nil {
		return err
	}
	if policy.Column == "" {
		return errors.New("snowflake: ColumnPolicy.Column required")
	}

	policyName := maskPolicyNamePrefix + table + "_" + policy.Column
	body := buildMaskBody(policy.MaskType, policy.AllowRoles)

	createDDL := fmt.Sprintf(
		`CREATE OR REPLACE MASKING POLICY %s.%s.%s AS (val VARIANT) RETURNS VARIANT -> %s`,
		quote(db), quote(schema), quote(policyName), body,
	)
	if _, err := s.db.ExecContext(ctx, createDDL); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("snowflake: create masking policy: %w", err)
	}

	alterDDL := fmt.Sprintf(
		`ALTER TABLE %s.%s.%s ALTER COLUMN %s SET MASKING POLICY %s.%s.%s`,
		quote(db), quote(schema), quote(table),
		quote(policy.Column),
		quote(db), quote(schema), quote(policyName),
	)
	if _, err := s.db.ExecContext(ctx, alterDDL); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("snowflake: alter set masking policy: %w", err)
	}
	return nil
}

// RemoveMaskingPolicy issues UNSET MASKING POLICY + DROP MASKING POLICY IF EXISTS.
func (s *Snowflake) RemoveMaskingPolicy(ctx context.Context, ref connector.AssetRef, column string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db, schema, table, err := splitTriIdentifier(ref.Identifier)
	if err != nil {
		return err
	}
	if column == "" {
		return errors.New("snowflake: column required")
	}
	policyName := maskPolicyNamePrefix + table + "_" + column

	unsetDDL := fmt.Sprintf(
		`ALTER TABLE %s.%s.%s ALTER COLUMN %s UNSET MASKING POLICY`,
		quote(db), quote(schema), quote(table), quote(column),
	)
	if _, err := s.db.ExecContext(ctx, unsetDDL); err != nil {
		// Don't fail if the column had no policy attached — Pitfall #4.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		// Continue to drop attempt below.
	}

	dropDDL := fmt.Sprintf(
		`DROP MASKING POLICY IF EXISTS %s.%s.%s`,
		quote(db), quote(schema), quote(policyName),
	)
	if _, err := s.db.ExecContext(ctx, dropDDL); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("snowflake: drop masking policy: %w", err)
	}
	return nil
}

// ListMaskingPolicies queries INFORMATION_SCHEMA.MASKING_POLICIES (via the
// account_usage view) for policies whose name begins with maskPolicyNamePrefix.
// Each policy name is parsed back to (table, column) and the body is parsed
// to recover the MaskType.
func (s *Snowflake) ListMaskingPolicies(ctx context.Context, ref connector.AssetRef) ([]connector.ColumnPolicy, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	db, schema, table, err := splitTriIdentifier(ref.Identifier)
	if err != nil {
		return nil, err
	}
	prefix := maskPolicyNamePrefix + table + "_"

	q := `SELECT POLICY_NAME, POLICY_BODY
	        FROM ` + quote(db) + `.INFORMATION_SCHEMA.MASKING_POLICIES
	       WHERE POLICY_SCHEMA = ?
	         AND POLICY_NAME LIKE ?`
	rows, err := s.db.QueryContext(ctx, q, schema, prefix+"%")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("snowflake: list masking policies: %w", err)
	}
	defer rows.Close()

	var out []connector.ColumnPolicy
	for rows.Next() {
		var name, body string
		if err := rows.Scan(&name, &body); err != nil {
			return nil, fmt.Errorf("snowflake: list scan: %w", err)
		}
		col := strings.TrimPrefix(name, prefix)
		if col == "" {
			continue
		}
		mt := parseMaskFromBody(body)
		roles := parseRolesFromBody(body)
		out = append(out, connector.ColumnPolicy{
			Column:     col,
			MaskType:   mt,
			AllowRoles: roles,
		})
	}
	return out, rows.Err()
}

// quote returns a Snowflake double-quoted identifier. Embedded double quotes
// are escaped per Snowflake DDL grammar.
func quote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// splitTriIdentifier splits "DB.SCHEMA.TABLE" into three parts. Any other
// shape returns an error — masking requires a tri-part identifier so the
// fully-qualified policy name is unambiguous (Pitfall #2).
func splitTriIdentifier(id string) (string, string, string, error) {
	parts := strings.Split(id, ".")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("snowflake: identifier %q must be DB.SCHEMA.TABLE", id)
	}
	for _, p := range parts {
		if p == "" {
			return "", "", "", fmt.Errorf("snowflake: identifier %q has empty part", id)
		}
	}
	return parts[0], parts[1], parts[2], nil
}

// buildMaskBody builds the CASE WHEN body using the per-MaskType template.
// AllowRoles values are wrapped in single-quoted Snowflake string literals;
// existing single quotes are doubled. Empty AllowRoles → ARRAY_CONSTRUCT()
// which makes the policy mask everyone.
func buildMaskBody(mt connector.MaskType, roles []string) string {
	tmpl := bodyTplRedact
	switch mt {
	case connector.MaskHash:
		tmpl = bodyTplHash
	case connector.MaskRedact:
		tmpl = bodyTplRedact
	case connector.MaskPartial:
		tmpl = bodyTplPartial
	}
	literals := make([]string, len(roles))
	for i, r := range roles {
		literals[i] = `'` + strings.ReplaceAll(r, `'`, `''`) + `'`
	}
	return fmt.Sprintf(tmpl, strings.Join(literals, ","))
}

// parseMaskFromBody reverses buildMaskBody — uses substring matching since
// the body is the CASE WHEN ... THEN val ELSE TO_VARIANT(...) END string.
func parseMaskFromBody(body string) connector.MaskType {
	switch {
	case strings.Contains(body, "SHA2_HEX"):
		return connector.MaskHash
	case strings.Contains(body, "REPEAT('*'"):
		return connector.MaskPartial
	case strings.Contains(body, "'***'"):
		return connector.MaskRedact
	default:
		return connector.MaskRedact
	}
}

// rolesPattern matches single-quoted role literals inside ARRAY_CONSTRUCT(...).
var rolesPattern = regexp.MustCompile(`ARRAY_CONSTRUCT\(([^)]*)\)`)

func parseRolesFromBody(body string) []string {
	m := rolesPattern.FindStringSubmatch(body)
	if len(m) < 2 || strings.TrimSpace(m[1]) == "" {
		return nil
	}
	parts := strings.Split(m[1], ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if strings.HasPrefix(p, "'") && strings.HasSuffix(p, "'") {
			p = strings.TrimSuffix(strings.TrimPrefix(p, "'"), "'")
			p = strings.ReplaceAll(p, "''", "'")
		}
		out = append(out, p)
	}
	return out
}

// Compile-time hint — *sql.DB is what underpins ApplyMaskingPolicy.
var _ = (*sql.DB)(nil)
