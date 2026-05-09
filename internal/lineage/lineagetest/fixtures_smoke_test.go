package lineagetest

import "testing"

// TestSmoke_StaticEdgeFixtures verifies that StaticEdgeFixtures returns a
// non-empty slice (sanity check — no DB required).
func TestSmoke_StaticEdgeFixtures(t *testing.T) {
	cases := StaticEdgeFixtures()
	if len(cases) == 0 {
		t.Fatal("StaticEdgeFixtures returned empty slice")
	}
}

// TestSmoke_ColumnLineageFixtures verifies that ColumnLineageFixtures returns
// a non-empty slice (sanity check — no DB required).
func TestSmoke_ColumnLineageFixtures(t *testing.T) {
	cases := ColumnLineageFixtures()
	if len(cases) == 0 {
		t.Fatal("ColumnLineageFixtures returned empty slice")
	}
}
