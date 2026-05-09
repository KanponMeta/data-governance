package schema_test

import (
	"testing"

	"github.com/kanpon/data-governance/internal/schema"
	"github.com/kanpon/data-governance/internal/schema/schematest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptrStr(s string) *string { return &s }

func TestClassifyAllChangeKinds(t *testing.T) {
	// Table-driven: all 9 ChangeKind values → expected (changeType, isBreaking).
	// For ChangeTypeWidened: we pass a stub lattice that returns (true, true) so the
	// Classify output is "type_widened", false.
	alwaysWideningLattice := func(old, new string) (bool, bool) { return true, true }

	tests := []struct {
		name         string
		change       schema.SchemaChange
		lattice      func(string, string) (bool, bool)
		wantType     string
		wantBreaking bool
	}{
		{
			name:         "column_added",
			change:       schema.SchemaChange{Kind: schema.ChangeColumnAdded, ColumnName: "col1"},
			lattice:      alwaysWideningLattice,
			wantType:     "column_added",
			wantBreaking: false,
		},
		{
			name:         "column_dropped",
			change:       schema.SchemaChange{Kind: schema.ChangeColumnDropped, ColumnName: "col1"},
			lattice:      alwaysWideningLattice,
			wantType:     "column_dropped",
			wantBreaking: true,
		},
		{
			name: "type_widened",
			change: schema.SchemaChange{
				Kind:     schema.ChangeTypeWidened,
				PrevType: ptrStr("int32"),
				NewType:  ptrStr("int64"),
			},
			lattice:      alwaysWideningLattice,
			wantType:     "type_widened",
			wantBreaking: false,
		},
		{
			name:         "nullable_added",
			change:       schema.SchemaChange{Kind: schema.ChangeNullableAdded, ColumnName: "col1"},
			lattice:      alwaysWideningLattice,
			wantType:     "nullable_added",
			wantBreaking: false,
		},
		{
			name:         "nullable_removed",
			change:       schema.SchemaChange{Kind: schema.ChangeNullableRemoved, ColumnName: "col1"},
			lattice:      alwaysWideningLattice,
			wantType:     "nullable_removed",
			wantBreaking: true,
		},
		{
			name:         "pk_changed",
			change:       schema.SchemaChange{Kind: schema.ChangePKChanged},
			lattice:      alwaysWideningLattice,
			wantType:     "pk_changed",
			wantBreaking: true,
		},
		{
			name:         "comment_changed",
			change:       schema.SchemaChange{Kind: schema.ChangeCommentChanged, ColumnName: "col1"},
			lattice:      alwaysWideningLattice,
			wantType:     "comment_changed",
			wantBreaking: false,
		},
		{
			name:         "default_changed",
			change:       schema.SchemaChange{Kind: schema.ChangeDefaultChanged, ColumnName: "col1"},
			lattice:      alwaysWideningLattice,
			wantType:     "default_changed",
			wantBreaking: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			changeType, isBreaking := schema.Classify(tc.change, tc.lattice)
			assert.Equal(t, tc.wantType, changeType)
			assert.Equal(t, tc.wantBreaking, isBreaking)
		})
	}
}

func TestClassifyTypeWideningWithLattice(t *testing.T) {
	// int32 → int64: lattice says widening → type_widened, non-breaking.
	c := schema.SchemaChange{
		Kind:     schema.ChangeTypeWidened,
		PrevType: ptrStr("int32"),
		NewType:  ptrStr("int64"),
	}
	changeType, isBreaking := schema.Classify(c, schema.IsWideningPostgres)
	assert.Equal(t, "type_widened", changeType)
	assert.False(t, isBreaking)
}

func TestClassifyTypeNarrowingWithLattice(t *testing.T) {
	// int64 → int32: lattice says narrowing → type_narrowed, breaking.
	c := schema.SchemaChange{
		Kind:     schema.ChangeTypeWidened,
		PrevType: ptrStr("int64"),
		NewType:  ptrStr("int32"),
	}
	changeType, isBreaking := schema.Classify(c, schema.IsWideningPostgres)
	assert.Equal(t, "type_narrowed", changeType)
	assert.True(t, isBreaking)
}

func TestClassifyOutOfLatticeBreaking(t *testing.T) {
	// text → bytea: cross-family, known=false → type_narrowed, is_breaking=true (D-09 safe default).
	c := schema.SchemaChange{
		Kind:     schema.ChangeTypeWidened,
		PrevType: ptrStr("text"),
		NewType:  ptrStr("bytea"),
	}
	changeType, isBreaking := schema.Classify(c, schema.IsWideningPostgres)
	assert.Equal(t, "type_narrowed", changeType, "out-of-lattice defaults to type_narrowed")
	assert.True(t, isBreaking, "out-of-lattice defaults to breaking")
}

func TestClassifyAllSchemaTestFixtures(t *testing.T) {
	// End-to-end: run DiffPairs through Diff + Classify, assert each fixture
	// classifies to its ExpectedChangeKind + ExpectedIsBreaking.
	pairs := schematest.DiffPairs()
	require.Len(t, pairs, 9)

	for _, tc := range pairs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			prev := schematestToConnector(tc.PrevSchema)
			next := schematestToConnector(tc.NextSchema)

			changes := schema.Diff(prev, next)
			require.NotEmpty(t, changes, "expected at least 1 change for fixture %q", tc.Name)

			// Run Classify on the first change using IsWideningPostgres lattice.
			changeType, isBreaking := schema.Classify(changes[0], schema.IsWideningPostgres)

			assert.Equal(t, tc.ExpectedChangeKind, changeType,
				"fixture %q: changeType mismatch", tc.Name)
			assert.Equal(t, tc.ExpectedIsBreaking, isBreaking,
				"fixture %q: isBreaking mismatch", tc.Name)
		})
	}
}
