package asset

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
)

// assetFingerprint is the canonical struct hashed for D-03.
// Field order in this struct IS the stable ordering — encoding/json marshals
// struct fields in declaration order, not sorted. Slices and map values must
// be pre-sorted by ComputeCodeHash for deterministic output.
//
// Excluded from the fingerprint by design (D-03):
//   - MaterializeFn (Go function values are not hashable; D-04 covers implementation drift)
//   - ConnectorName (declaration vs. binding — moving an asset between connectors should
//     not invalidate downstream lineage/schema rows)
//   - RetryPolicy (orchestration concern; same data shape regardless of retry settings)
//   - Schedule / Sensors / Partitions (orchestration concerns, not data-shape concerns)
type assetFingerprint struct {
	Name          string               `json:"name"`
	Upstreams     []string             `json:"upstreams"`
	Description   string               `json:"description,omitempty"`
	Owner         string               `json:"owner,omitempty"`
	Tags          []string             `json:"tags,omitempty"`
	Columns       []ColumnMeta         `json:"columns,omitempty"`
	ColumnLineage map[string][]ColumnRef `json:"column_lineage,omitempty"`
}

// ComputeCodeHash returns the SHA-256 hex of the canonical JSON fingerprint of
// the asset definition (D-03). Determinism guarantees:
//   - encoding/json marshals struct fields in declaration order.
//   - encoding/json marshals map[string]T keys in sorted order (Go 1.12+).
//   - This function pre-sorts Upstreams, Tags, Columns (by Name), each Column's Tags,
//     and each ColumnLineage value's []ColumnRef (by Asset+Column).
//
// Returns empty string for a nil asset (defensive).
func ComputeCodeHash(a *Asset) string {
	if a == nil {
		return ""
	}

	// Pre-sort each variable-length collection to canonicalize.
	ups := append([]string(nil), a.upstreams...)
	sort.Strings(ups)

	tags := append([]string(nil), a.tags...)
	sort.Strings(tags)

	// Sort columns by Name; sort each column's Tags within.
	cols := append([]ColumnMeta(nil), a.columns...)
	sort.Slice(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })
	for i := range cols {
		t := append([]string(nil), cols[i].Tags...)
		sort.Strings(t)
		cols[i].Tags = t
	}

	// Canonicalize ColumnLineage: sort each value slice by (Asset, Column).
	// Map keys are auto-sorted by encoding/json (Go 1.12+).
	var cl map[string][]ColumnRef
	if a.columnLineage != nil {
		cl = make(map[string][]ColumnRef, len(a.columnLineage))
		for k, refs := range a.columnLineage {
			sorted := append([]ColumnRef(nil), refs...)
			sort.Slice(sorted, func(i, j int) bool {
				if sorted[i].Asset != sorted[j].Asset {
					return sorted[i].Asset < sorted[j].Asset
				}
				return sorted[i].Column < sorted[j].Column
			})
			cl[k] = sorted
		}
	}

	fp := assetFingerprint{
		Name:          a.name,
		Upstreams:     ups,
		Description:   a.description,
		Owner:         a.owner,
		Tags:          tags,
		Columns:       cols,
		ColumnLineage: cl,
	}

	b, err := json.Marshal(fp)
	if err != nil {
		// Marshaling a struct with primitive + slice + map[string][]struct cannot fail
		// in encoding/json. Panic indicates a programmer error (e.g., adding an unsupported type).
		panic(fmt.Sprintf("asset: fingerprint marshal: %v", err))
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}
