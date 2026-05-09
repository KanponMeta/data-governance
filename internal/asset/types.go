package asset

import "github.com/kanpon/data-governance/internal/connector"

// ColumnRef declares a column-level lineage source (D-02).
type ColumnRef struct {
	Asset  string `json:"asset"`
	Column string `json:"column"`
}

// ColumnLineageMap maps output column name → source column references.
// Wave 4's lineage writer reads this from the builder default OR
// MaterializeResult.ColumnLineage (runtime override wins per D-02).
type ColumnLineageMap map[string][]ColumnRef

// ColumnMeta is the per-column declaration produced by Builder.Column(name).
type ColumnMeta struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// ---- Phase 5 additions (D-02, D-04, D-07; RBAC-03/04) ----

// MaskType aliases the connector mask enum so user code can write
// asset.MaskHash directly without importing internal/connector.
type MaskType = connector.MaskType

const (
	MaskHash    = connector.MaskHash
	MaskRedact  = connector.MaskRedact
	MaskPartial = connector.MaskPartial
)

// ColumnPolicy is the user-facing column-level masking declaration attached
// via Builder.ColumnPolicy. Reason is populated by the REST PATCH path
// (runtime overrides) — for builder defaults it is the empty string.
//
// Per Phase 5 D-02 the (sorted) ColumnPolicies slice is part of the asset's
// code_hash, so changing a builder mask creates a new asset_versions row.
type ColumnPolicy struct {
	Column        string             `json:"column"`
	Mask          connector.MaskType `json:"mask"`
	AllowRoles    []string           `json:"allow_roles,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	PartialReveal int                `json:"partial_reveal,omitempty"`
}
