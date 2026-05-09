package lineagetest

// ExpectedEdge represents a directed edge from one asset to another.
type ExpectedEdge struct {
	From string
	To   string
}

// StaticEdgeCase is a test fixture for asset-level lineage capture.
// It pairs an asset definition (with declared upstreams) against the
// expected asset_edges rows the lineage writer should produce.
//
// Used by: internal/lineage/*_test.go (Wave 3 LINE-01 unit tests).
type StaticEdgeCase struct {
	Name          string
	Asset         string
	Upstreams     []string
	ExpectedEdges []ExpectedEdge
}

// StaticEdgeFixtures returns the canonical set of asset-level lineage test
// cases that Wave 3's lineage writer must satisfy. Each case maps an asset
// + declared upstreams to the expected asset_edges rows.
//
// Cases:
//  (a) Single asset, 0 upstreams — writer produces 0 edges (no self-edge).
//  (b) Single asset, 1 upstream — writer produces exactly 1 edge u1→self.
//  (c) Single asset, 3 upstreams — writer produces 3 edges (fan-in).
//  (d) Diamond: target with 2 upstreams that share a common ancestor —
//      writer emits only the directly-declared edges; transitivity is the
//      traversal layer's responsibility, not the write layer.
func StaticEdgeFixtures() []StaticEdgeCase {
	return []StaticEdgeCase{
		{
			Name:          "no_upstreams",
			Asset:         "asset_self",
			Upstreams:     nil,
			ExpectedEdges: []ExpectedEdge{},
		},
		{
			Name:      "single_upstream",
			Asset:     "asset_self",
			Upstreams: []string{"u1"},
			ExpectedEdges: []ExpectedEdge{
				{From: "u1", To: "asset_self"},
			},
		},
		{
			Name:      "three_upstreams",
			Asset:     "asset_self",
			Upstreams: []string{"u1", "u2", "u3"},
			ExpectedEdges: []ExpectedEdge{
				{From: "u1", To: "asset_self"},
				{From: "u2", To: "asset_self"},
				{From: "u3", To: "asset_self"},
			},
		},
		{
			// Diamond: target depends on left and right; both depend on root.
			// asset_root → asset_left → asset_target
			// asset_root → asset_right → asset_target
			// Writer only emits the directly-declared edges (left→target,
			// right→target). Transitivity (root→target via left/right) is
			// computed at query time — the writer does not expand transitively.
			Name:      "diamond",
			Asset:     "asset_target",
			Upstreams: []string{"asset_left", "asset_right"},
			ExpectedEdges: []ExpectedEdge{
				{From: "asset_left", To: "asset_target"},
				{From: "asset_right", To: "asset_target"},
			},
		},
	}
}

// ColumnRef identifies a single column on an upstream asset.
type ColumnRef struct {
	Asset  string
	Column string
}

// ColumnEdgeRow represents an expected row in the column_edges table.
type ColumnEdgeRow struct {
	FromAsset  string
	FromColumn string
	ToAsset    string
	ToColumn   string
}

// ColumnLineageCase is a test fixture for column-level lineage capture.
// It pairs a column lineage declaration (output column → []ColumnRef) with
// the expected column_edges rows the lineage writer should produce.
//
// Used by: internal/lineage/*_test.go (Wave 3 LINE-02 unit tests).
type ColumnLineageCase struct {
	Name          string
	Asset         string
	ColumnLineage map[string][]ColumnRef
	ExpectedRows  []ColumnEdgeRow
}

// ColumnLineageFixtures returns the canonical column-level lineage test cases.
//
// Cases:
//  (a) Single output column derived from one input — 1 column_edge row.
//  (b) Fan-in: single output column derived from 2 inputs — 2 rows.
//  (c) Fan-out: 2 output columns each derived from the same input — 2 rows.
func ColumnLineageFixtures() []ColumnLineageCase {
	return []ColumnLineageCase{
		{
			Name:  "single_column",
			Asset: "asset_out",
			ColumnLineage: map[string][]ColumnRef{
				"out_col": {{Asset: "asset_in", Column: "in_col"}},
			},
			ExpectedRows: []ColumnEdgeRow{
				{FromAsset: "asset_in", FromColumn: "in_col", ToAsset: "asset_out", ToColumn: "out_col"},
			},
		},
		{
			Name:  "fan_in_two_to_one",
			Asset: "asset_out",
			ColumnLineage: map[string][]ColumnRef{
				"out_col": {
					{Asset: "asset_a", Column: "col_a"},
					{Asset: "asset_b", Column: "col_b"},
				},
			},
			ExpectedRows: []ColumnEdgeRow{
				{FromAsset: "asset_a", FromColumn: "col_a", ToAsset: "asset_out", ToColumn: "out_col"},
				{FromAsset: "asset_b", FromColumn: "col_b", ToAsset: "asset_out", ToColumn: "out_col"},
			},
		},
		{
			Name:  "fan_out_one_to_two",
			Asset: "asset_out",
			ColumnLineage: map[string][]ColumnRef{
				"out_col_1": {{Asset: "asset_in", Column: "src_col"}},
				"out_col_2": {{Asset: "asset_in", Column: "src_col"}},
			},
			ExpectedRows: []ColumnEdgeRow{
				{FromAsset: "asset_in", FromColumn: "src_col", ToAsset: "asset_out", ToColumn: "out_col_1"},
				{FromAsset: "asset_in", FromColumn: "src_col", ToAsset: "asset_out", ToColumn: "out_col_2"},
			},
		},
	}
}
