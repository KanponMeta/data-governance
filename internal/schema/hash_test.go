package schema_test

import (
	"sync"
	"testing"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/schema"
	"github.com/stretchr/testify/require"
)

func makeTestSchema() connector.Schema {
	return connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Nullable: false, IsPrimaryKey: true},
			{Name: "name", Type: "varchar(255)", Nullable: true},
			{Name: "created_at", Type: "timestamptz", Nullable: false},
		},
		PrimaryKey:    []string{"id"},
		RowCountEstim: 1000,
		CapturedAt:    time.Now(),
	}
}

func TestHashSchemaDeterministic(t *testing.T) {
	s := makeTestSchema()
	h0 := schema.HashSchema(s)
	for i := 0; i < 100; i++ {
		require.Equal(t, h0, schema.HashSchema(s), "hash must be stable across repeated calls")
	}
}

func TestHashSchemaConcurrent(t *testing.T) {
	s := makeTestSchema()
	expected := schema.HashSchema(s)

	var wg sync.WaitGroup
	const n = 50
	results := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = schema.HashSchema(s)
		}(i)
	}
	wg.Wait()

	for i, got := range results {
		require.Equal(t, expected, got, "goroutine %d produced different hash", i)
	}
}

func TestHashSchemaColumnOrderIndependent(t *testing.T) {
	s1 := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "a", Type: "int64"},
			{Name: "b", Type: "text"},
			{Name: "c", Type: "bool"},
		},
		PrimaryKey: []string{"a"},
	}
	s2 := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "c", Type: "bool"},
			{Name: "b", Type: "text"},
			{Name: "a", Type: "int64"},
		},
		PrimaryKey: []string{"a"},
	}
	require.Equal(t, schema.HashSchema(s1), schema.HashSchema(s2),
		"schemas with same columns in different order must produce the same hash (Pitfall 5)")
}

func TestHashSchemaPKOrderSensitive(t *testing.T) {
	s1 := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "a", Type: "int64", IsPrimaryKey: true},
			{Name: "b", Type: "int64", IsPrimaryKey: true},
		},
		PrimaryKey: []string{"a", "b"},
	}
	s2 := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "a", Type: "int64", IsPrimaryKey: true},
			{Name: "b", Type: "int64", IsPrimaryKey: true},
		},
		PrimaryKey: []string{"b", "a"},
	}
	require.NotEqual(t, schema.HashSchema(s1), schema.HashSchema(s2),
		"PK column order is meaningful — different order must produce different hash")
}

func TestHashSchemaIgnoresVolatileFields(t *testing.T) {
	base := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Nullable: false},
		},
		PrimaryKey:    []string{"id"},
		RowCountEstim: 100,
		CapturedAt:    time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	different := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Nullable: false},
		},
		PrimaryKey:    []string{"id"},
		RowCountEstim: 999999, // different row count
		CapturedAt:    time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC), // different timestamp
	}
	require.Equal(t, schema.HashSchema(base), schema.HashSchema(different),
		"RowCountEstim and CapturedAt must not affect the hash")
}

func TestHashSchemaIgnoresComment(t *testing.T) {
	s1 := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Comment: ""},
		},
		PrimaryKey: []string{"id"},
	}
	s2 := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Comment: "the primary key"},
		},
		PrimaryKey: []string{"id"},
	}
	require.Equal(t, schema.HashSchema(s1), schema.HashSchema(s2),
		"Comment changes must not affect the schema hash (D-09: comment changes are non-breaking; Wave 5 reads schema_data JSONB)")
}

func TestHashSchemaSensitiveFields(t *testing.T) {
	base := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Nullable: false},
			{Name: "name", Type: "varchar(255)", Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}
	baseHash := schema.HashSchema(base)

	cases := []struct {
		name   string
		schema connector.Schema
	}{
		{
			name: "add_column",
			schema: connector.Schema{
				Columns: []connector.SchemaColumn{
					{Name: "id", Type: "int64", Nullable: false},
					{Name: "name", Type: "varchar(255)", Nullable: true},
					{Name: "new_col", Type: "text", Nullable: true},
				},
				PrimaryKey: []string{"id"},
			},
		},
		{
			name: "drop_column",
			schema: connector.Schema{
				Columns: []connector.SchemaColumn{
					{Name: "id", Type: "int64", Nullable: false},
				},
				PrimaryKey: []string{"id"},
			},
		},
		{
			name: "change_type",
			schema: connector.Schema{
				Columns: []connector.SchemaColumn{
					{Name: "id", Type: "int64", Nullable: false},
					{Name: "name", Type: "text", Nullable: true}, // varchar→text
				},
				PrimaryKey: []string{"id"},
			},
		},
		{
			name: "change_nullable",
			schema: connector.Schema{
				Columns: []connector.SchemaColumn{
					{Name: "id", Type: "int64", Nullable: false},
					{Name: "name", Type: "varchar(255)", Nullable: false}, // true→false
				},
				PrimaryKey: []string{"id"},
			},
		},
		{
			name: "change_default",
			schema: connector.Schema{
				Columns: []connector.SchemaColumn{
					{Name: "id", Type: "int64", Nullable: false},
					func() connector.SchemaColumn {
						d := "empty"
						return connector.SchemaColumn{Name: "name", Type: "varchar(255)", Nullable: true, Default: &d}
					}(),
				},
				PrimaryKey: []string{"id"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := schema.HashSchema(tc.schema)
			require.NotEqual(t, baseHash, got, "schema change %q must produce a different hash", tc.name)
		})
	}
}
