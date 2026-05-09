// Package schema provides schema capture and content-addressable versioning
// for the Phase 4 data governance platform (D-05, D-06, D-07, D-08).
package schema

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kanpon/data-governance/internal/connector"
)

// HashSchema returns a stable SHA-256 hex of the canonical JSON of s (D-08 + Pitfall 5).
//
// Excluded from the hash by design:
//   - RowCountEstim: volatile; row count changes constantly without schema change
//   - CapturedAt: always different for each run
//   - Comment: comment-only changes are non-breaking (D-09); Wave 5's diff
//     classifier reads schema_data JSONB to detect comment changes separately
//
// Columns are sorted ALPHABETICALLY BY NAME before hashing — the PostgreSQL
// information_schema.columns order is ordinal_position, which can drift if a
// user does ALTER TABLE DROP COLUMN; ALTER TABLE ADD COLUMN. Alphabetical
// sort canonicalizes (Pitfall 5).
//
// PrimaryKey list IS preserved in original order — composite PK column order
// is meaningful (PK on (a, b) is different from PK on (b, a)).
func HashSchema(s connector.Schema) string {
	type canonicalCol struct {
		Name         string  `json:"name"`
		Type         string  `json:"type"`
		Nullable     bool    `json:"nullable"`
		Default      *string `json:"default,omitempty"`
		IsPrimaryKey bool    `json:"is_pk"`
		// Comment intentionally OMITTED — comment changes are non-breaking
		// (D-09) and should not invalidate the schema_hash. Wave 5's diff
		// classifier reads the schema_data JSONB to detect comment changes.
	}

	cols := make([]canonicalCol, 0, len(s.Columns))
	for _, c := range s.Columns {
		cols = append(cols, canonicalCol{
			Name:         c.Name,
			Type:         c.Type,
			Nullable:     c.Nullable,
			Default:      c.Default,
			IsPrimaryKey: c.IsPrimaryKey,
		})
	}
	sort.Slice(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })

	type canonicalSchema struct {
		Columns    []canonicalCol `json:"columns"`
		PrimaryKey []string       `json:"pk"`
	}

	pkCopy := append([]string(nil), s.PrimaryKey...) // preserve original PK order
	cs := canonicalSchema{Columns: cols, PrimaryKey: pkCopy}

	b, err := json.Marshal(cs)
	if err != nil {
		// json.Marshal only fails on non-marshalable types (channels, funcs, etc.).
		// canonicalSchema contains only basic types, so this is unreachable in practice.
		panic(fmt.Sprintf("schema: hash marshal: %v", err))
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}
