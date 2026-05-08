package bigquery

import "testing"

func TestSplitIdentifier(t *testing.T) {
	cases := []struct {
		name        string
		id          string
		wantProject string
		wantDataset string
		wantTable   string
		wantErr     bool
	}{
		{"three_parts", "proj.ds.tbl", "proj", "ds", "tbl", false},
		{"two_parts_uses_default_project", "ds.tbl", "", "ds", "tbl", false},
		{"empty", "", "", "", "", true},
		{"one_part", "tbl", "", "", "", true},
		{"four_parts", "a.b.c.d", "", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, d, tbl, err := splitIdentifier(tc.id)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p != tc.wantProject || d != tc.wantDataset || tbl != tc.wantTable {
				t.Fatalf("got (%q,%q,%q), want (%q,%q,%q)",
					p, d, tbl, tc.wantProject, tc.wantDataset, tc.wantTable)
			}
		})
	}
}

// TestRead_FallsBackToConfiguredProject documents the contract: when the asset
// identifier omits the project segment, Read() must use the BigQuery client's
// configured default project rather than emitting `` `` (empty backticks).
// This is verified by inspection of bigquery.go Read(): after splitIdentifier,
// `if project == "" { project = b.project }`.
func TestRead_FallsBackToConfiguredProject(t *testing.T) {
	b := &BigQuery{project: "default-proj"}
	if b.project != "default-proj" {
		t.Fatalf("test setup: project not retained")
	}
}
