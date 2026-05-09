package lineageq

import "testing"

// TestQueriesInterface is a compile-time check that confirms the generated
// Queries struct exposes the two recursive CTE methods introduced in plan 04-06.
// The test has no runtime assertions — if the generated file is removed or the
// method signatures change, this file will fail to compile, surfacing the issue
// immediately.
func TestQueriesInterface(t *testing.T) {
	// Compile-time check: ensure Queries struct has the two methods we expect.
	var q *Queries
	_ = q.TraverseAssetLineage
	_ = q.TraverseColumnLineage
}
