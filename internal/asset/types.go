package asset

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
