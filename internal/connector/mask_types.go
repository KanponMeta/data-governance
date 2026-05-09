// Package connector — column masking types shared between connector
// implementations and the policy store. Lives in the connector package
// (not internal/policy) so first-party connectors can import without
// pulling a circular dependency on internal/policy.
//
// Phase 5 plan 05-02 (D-02 / D-04 / D-07).
package connector

// MaskType is the canonical enumeration of column-level masking transforms.
// Values are also the on-wire / on-disk representation (column_policies.mask_type).
type MaskType string

const (
	MaskHash    MaskType = "hash"
	MaskRedact  MaskType = "redact"
	MaskPartial MaskType = "partial"
)

// IsValid reports whether m is one of the three known mask types.
// Used by Builder.ColumnPolicy and the runtime PATCH handler to
// reject unknown masks fail-fast (Pitfall #2).
func (m MaskType) IsValid() bool {
	return m == MaskHash || m == MaskRedact || m == MaskPartial
}

// String returns the on-wire mask name (lowercase ASCII).
func (m MaskType) String() string { return string(m) }

// ColumnPolicy is the connector-facing column policy type — the value
// passed to MaskingProvisioner.ApplyMaskingPolicy.
//
// Note: internal/asset.ColumnPolicy is a separate user-facing struct that
// includes a Reason field; conversion happens at the policy store boundary.
type ColumnPolicy struct {
	// Column is the target column name on the warehouse table.
	Column string
	// MaskType selects the masking transform.
	MaskType MaskType
	// AllowRoles lists warehouse roles permitted to see clear text.
	// Each value is a fully-qualified warehouse role identifier
	// (e.g. "PII_ANALYST" for Snowflake, "group:pii-analyst@example.com"
	// for BigQuery).
	AllowRoles []string
	// PartialReveal: number of leading characters revealed for MaskPartial;
	// 0 means use the connector default (typically 2).
	PartialReveal int
}
