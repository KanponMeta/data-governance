package connector_test

import (
	"testing"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchemaTypeShape asserts the D-07 Schema and SchemaColumn zero values are well-defined
// and that the pointer Default field correctly disambiguates "no default" from empty string.
func TestSchemaTypeShape(t *testing.T) {
	t.Parallel()

	t.Run("Schema zero value", func(t *testing.T) {
		t.Parallel()
		s := connector.Schema{}
		assert.Empty(t, s.Columns, "zero Schema.Columns should be nil/empty")
		assert.Empty(t, s.PrimaryKey, "zero Schema.PrimaryKey should be nil/empty")
		assert.Equal(t, int64(0), s.RowCountEstim, "zero Schema.RowCountEstim should be 0")
		assert.True(t, s.CapturedAt.IsZero(), "zero Schema.CapturedAt should be zero time")
	})

	t.Run("SchemaColumn without default", func(t *testing.T) {
		t.Parallel()
		c := connector.SchemaColumn{
			Name:         "x",
			Type:         "int64",
			Nullable:     false,
			IsPrimaryKey: true,
		}
		assert.Equal(t, "x", c.Name)
		assert.Equal(t, "int64", c.Type)
		assert.False(t, c.Nullable)
		assert.True(t, c.IsPrimaryKey)
		assert.Nil(t, c.Default, "Default should be nil when no default is set")
		assert.Equal(t, "", c.Comment, "Comment should be empty string when unset")
	})

	t.Run("SchemaColumn with default", func(t *testing.T) {
		t.Parallel()
		def := "0"
		c2 := connector.SchemaColumn{Name: "y", Default: &def}
		require.NotNil(t, c2.Default, "Default should not be nil when set")
		assert.Equal(t, "0", *c2.Default)
	})

	t.Run("SchemaColumn with empty-string default", func(t *testing.T) {
		t.Parallel()
		// pointer distinguishes "no default" (nil) from "default is empty string" (&"")
		empty := ""
		c3 := connector.SchemaColumn{Name: "z", Default: &empty}
		require.NotNil(t, c3.Default, "Default pointer must be non-nil even for empty string default")
		assert.Equal(t, "", *c3.Default, "Default value should be the empty string")
	})

	t.Run("Legacy types still present in connector package", func(t *testing.T) {
		t.Parallel()
		// Ensure Phase 1 CONN-08 frozen types compile alongside new types.
		_ = connector.Column{Name: "col", RawType: "text", Nullable: true}
		_ = connector.SchemaResponse{Columns: []connector.Column{}, CapturedAt: connector.Schema{}.CapturedAt}
	})
}
