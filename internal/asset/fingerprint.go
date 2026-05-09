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
//   - FreshnessSLA (operational config — D-20 explicitly excluded; changing the SLA
//     does not change the data shape).
//
// Phase 5 Plan 05-05 additions: QualityRules ARE part of the fingerprint (D-08
// governance reset semantics — changing a rule reseats the asset version).
type assetFingerprint struct {
	Name          string                       `json:"name"`
	Upstreams     []string                     `json:"upstreams"`
	Description   string                       `json:"description,omitempty"`
	Owner         string                       `json:"owner,omitempty"`
	Tags          []string                     `json:"tags,omitempty"`
	Columns       []ColumnMeta                 `json:"columns,omitempty"`
	ColumnLineage map[string][]ColumnRef       `json:"column_lineage,omitempty"`
	QualityRules  []qualityRuleFingerprintItem `json:"quality_rules,omitempty"`
}

// qualityRuleFingerprintItem is the canonical encoding of a QualityRule in the
// fingerprint. We use Name + Type + ConfigJSON (raw bytes) so two assertions
// with the same SQL but different predicates produce different fingerprints.
type qualityRuleFingerprintItem struct {
	Name   string          `json:"name"`
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config"`
}

// ComputeCodeHash returns the SHA-256 hex of the canonical JSON fingerprint of
// the asset definition (D-03). Determinism guarantees:
//   - encoding/json marshals struct fields in declaration order.
//   - encoding/json marshals map[string]T keys in sorted order (Go 1.12+).
//   - This function pre-sorts Upstreams, Tags, Columns (by Name), each Column's Tags,
//     each ColumnLineage value's []ColumnRef (by Asset+Column), and QualityRules
//     (by Name).
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

	// Phase 5 Plan 05-05: include QualityRules sorted by Name.
	var qr []qualityRuleFingerprintItem
	if len(a.qualityRules) > 0 {
		qr = make([]qualityRuleFingerprintItem, 0, len(a.qualityRules))
		for _, r := range a.qualityRules {
			cfg, err := r.ConfigJSON()
			if err != nil {
				panic(fmt.Sprintf("asset: QualityRule %q ConfigJSON: %v", r.Name(), err))
			}
			qr = append(qr, qualityRuleFingerprintItem{
				Name:   r.Name(),
				Type:   r.Type(),
				Config: json.RawMessage(cfg),
			})
		}
		sort.Slice(qr, func(i, j int) bool { return qr[i].Name < qr[j].Name })
	}

	fp := assetFingerprint{
		Name:          a.name,
		Upstreams:     ups,
		Description:   a.description,
		Owner:         a.owner,
		Tags:          tags,
		Columns:       cols,
		ColumnLineage: cl,
		QualityRules:  qr,
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
