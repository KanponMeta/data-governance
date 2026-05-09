package schema_test

import (
	"testing"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/schema"
	"github.com/kanpon/data-governance/internal/schema/schematest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// schematestToConnector converts a schematest.Schema to connector.Schema
// so we can run DiffPairs fixtures through schema.Diff.
func schematestToConnector(s schematest.Schema) connector.Schema {
	cols := make([]connector.SchemaColumn, len(s.Columns))
	for i, c := range s.Columns {
		cols[i] = connector.SchemaColumn{
			Name:         c.Name,
			Type:         c.Type,
			Nullable:     c.Nullable,
			Default:      c.Default,
			IsPrimaryKey: c.IsPrimaryKey,
			Comment:      c.Comment,
		}
	}
	return connector.Schema{
		Columns:       cols,
		PrimaryKey:    s.PrimaryKey,
		RowCountEstim: s.RowCountEstim,
		CapturedAt:    s.CapturedAt,
	}
}

func TestDiffEmpty(t *testing.T) {
	// prev == next → 0 changes
	s := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Nullable: false},
			{Name: "name", Type: "text", Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}
	changes := schema.Diff(s, s)
	assert.Len(t, changes, 0, "identical schemas should produce no changes")
}

func TestDiffSchemaTestFixtures(t *testing.T) {
	// Table-driven over schematest.DiffPairs() — 9 cases.
	// Each case has exactly ONE change; assert the first change has the expected Kind.
	// Note: type cases emit ChangeTypeWidened provisionally — Classify resolves direction.
	pairs := schematest.DiffPairs()
	require.Len(t, pairs, 9, "expected 9 DiffPairs cases")

	for _, tc := range pairs {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			prev := schematestToConnector(tc.PrevSchema)
			next := schematestToConnector(tc.NextSchema)
			changes := schema.Diff(prev, next)
			require.NotEmpty(t, changes, "expected at least 1 change for fixture %q", tc.Name)

			// For type changes, Diff emits ChangeTypeWidened provisionally (Classify resolves).
			// All other kinds emit directly.
			firstKind := string(changes[0].Kind)
			expectedKind := tc.ExpectedChangeKind

			// Handle provisional type kind: Diff always emits ChangeTypeWidened for type changes.
			// The final kind (type_narrowed vs type_widened) is determined by Classify.
			if expectedKind == "type_narrowed" || expectedKind == "type_widened" {
				assert.Equal(t, "type_widened", firstKind,
					"type changes should be provisionally ChangeTypeWidened from Diff (Classify resolves)")
			} else {
				assert.Equal(t, expectedKind, firstKind,
					"fixture %q: expected ChangeKind %q", tc.Name, expectedKind)
			}
		})
	}
}

func TestDiffMultipleChangesPerColumn(t *testing.T) {
	// A column whose type AND nullable both change should emit 2 changes.
	prev := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "amount", Type: "int32", Nullable: false},
		},
		PrimaryKey: []string{},
	}
	next := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "amount", Type: "int64", Nullable: true},
		},
		PrimaryKey: []string{},
	}
	changes := schema.Diff(prev, next)
	require.Len(t, changes, 2, "expected 2 changes: type + nullable")

	kinds := make(map[schema.ChangeKind]bool)
	for _, c := range changes {
		kinds[c.Kind] = true
	}
	assert.True(t, kinds[schema.ChangeTypeWidened], "should have type change")
	assert.True(t, kinds[schema.ChangeNullableAdded], "should have nullable added")
}

func TestDiffColumnAdded(t *testing.T) {
	prev := connector.Schema{
		Columns:    []connector.SchemaColumn{{Name: "a", Type: "int64", Nullable: false}},
		PrimaryKey: []string{"a"},
	}
	next := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "a", Type: "int64", Nullable: false},
			{Name: "b", Type: "text", Nullable: true},
		},
		PrimaryKey: []string{"a"},
	}
	changes := schema.Diff(prev, next)
	require.Len(t, changes, 1)
	assert.Equal(t, schema.ChangeColumnAdded, changes[0].Kind)
	assert.Equal(t, "b", changes[0].ColumnName)
	require.NotNil(t, changes[0].NewType)
	assert.Equal(t, "text", *changes[0].NewType)
}

func TestDiffColumnDropped(t *testing.T) {
	prev := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "a", Type: "int64", Nullable: false},
			{Name: "b", Type: "text", Nullable: true},
		},
		PrimaryKey: []string{"a"},
	}
	next := connector.Schema{
		Columns:    []connector.SchemaColumn{{Name: "a", Type: "int64", Nullable: false}},
		PrimaryKey: []string{"a"},
	}
	changes := schema.Diff(prev, next)
	require.Len(t, changes, 1)
	assert.Equal(t, schema.ChangeColumnDropped, changes[0].Kind)
	assert.Equal(t, "b", changes[0].ColumnName)
	require.NotNil(t, changes[0].PrevType)
	assert.Equal(t, "text", *changes[0].PrevType)
}

func TestDiffPKChanged(t *testing.T) {
	prev := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "a", Type: "int64", Nullable: false},
			{Name: "b", Type: "text", Nullable: true},
		},
		PrimaryKey: []string{"a"},
	}
	next := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "a", Type: "int64", Nullable: false},
			{Name: "b", Type: "text", Nullable: true},
		},
		PrimaryKey: []string{"a", "b"},
	}
	changes := schema.Diff(prev, next)
	require.Len(t, changes, 1)
	assert.Equal(t, schema.ChangePKChanged, changes[0].Kind)
	assert.Empty(t, changes[0].ColumnName, "PK-level changes have no column name")
}

func TestDiffRenameIsDropAdd(t *testing.T) {
	// Per plan: rename = drop + add (no heuristic detection).
	prev := connector.Schema{
		Columns:    []connector.SchemaColumn{{Name: "old_name", Type: "int32", Nullable: false}},
		PrimaryKey: []string{"old_name"},
	}
	next := connector.Schema{
		Columns:    []connector.SchemaColumn{{Name: "new_name", Type: "int32", Nullable: false}},
		PrimaryKey: []string{"new_name"},
	}
	changes := schema.Diff(prev, next)
	// Expect: column_added(new_name) + column_dropped(old_name) + pk_changed
	kinds := make(map[schema.ChangeKind]int)
	for _, c := range changes {
		kinds[c.Kind]++
	}
	assert.Equal(t, 1, kinds[schema.ChangeColumnAdded], "should detect new_name as added")
	assert.Equal(t, 1, kinds[schema.ChangeColumnDropped], "should detect old_name as dropped")
	assert.Equal(t, 1, kinds[schema.ChangePKChanged], "PK also changed")
}
