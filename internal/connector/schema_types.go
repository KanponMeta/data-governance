// Phase 4 (D-07) — rich Schema capture shape used by the new SchemaDescriber
// optional capability interface (introduced in Wave 2 plan 04-03).
//
// The legacy connector.Column and connector.SchemaResponse stay frozen
// (Phase 1 CONN-08) and continue to be returned by the legacy Schema()
// method. SchemaDescriber.DescribeSchema() returns this richer Schema struct.
//
// These types live in the connector package — not internal/schema/ —
// because asset.MaterializeResult.Schema needs to reference Schema, and
// asset already imports connector. Putting Schema in internal/schema would
// create asset -> schema -> connector -> asset, breaking the build (Pitfall 4
// in 04-RESEARCH.md).
package connector

import "time"

// Schema is the rich D-07 schema capture (distinct from SchemaResponse).
// Columns are stored alphabetically by Name for stable hash dedup (D-08, Pitfall 5).
type Schema struct {
	Columns       []SchemaColumn
	PrimaryKey    []string  // ordered by user/connector intent
	RowCountEstim int64     // -1 if connector cannot supply
	CapturedAt    time.Time
}

// SchemaColumn is the per-column shape returned by SchemaDescriber.
// Distinct from connector.Column (Phase 1 thin shape) — adds Default,
// IsPrimaryKey, Comment, normalized Type.
type SchemaColumn struct {
	Name         string
	Type         string  // normalized: "int64" | "varchar(255)" | "decimal(10,2)" | "timestamptz" | ...
	Nullable     bool
	Default      *string // nil = no default; pointer disambiguates "no default" from empty string
	IsPrimaryKey bool
	Comment      string // pulled from PG col_description() / equivalent
}
