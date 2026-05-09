package schematest

import "testing"

// TestSmoke_DiffPairs verifies that DiffPairs returns exactly 9 cases —
// one for each ChangeKind enum value Wave 4 will classify (D-09).
// No DB required.
func TestSmoke_DiffPairs(t *testing.T) {
	pairs := DiffPairs()
	const wantCount = 9
	if len(pairs) != wantCount {
		t.Fatalf("DiffPairs returned %d cases; want %d (one per ChangeKind)", len(pairs), wantCount)
	}
}
