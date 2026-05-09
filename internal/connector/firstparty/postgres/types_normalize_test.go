package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

// TestNormalizePostgresType covers all PostgreSQL type mappings from 04-RESEARCH.md §6.
func TestNormalizePostgresType(t *testing.T) {
	tests := []struct {
		name         string
		rawType      string
		charMaxLen   *int
		numPrecision *int
		numScale     *int
		want         string
	}{
		// Integer types
		{"smallint", "smallint", nil, nil, nil, "int16"},
		{"integer", "integer", nil, nil, nil, "int32"},
		{"bigint", "bigint", nil, nil, nil, "int64"},
		// Floating point
		{"real", "real", nil, nil, nil, "float32"},
		{"double precision", "double precision", nil, nil, nil, "float64"},
		// Boolean
		{"boolean", "boolean", nil, nil, nil, "bool"},
		// Text
		{"text", "text", nil, nil, nil, "text"},
		// Character varying with length
		{"character varying with len", "character varying", intPtr(255), nil, nil, "varchar(255)"},
		// Character varying without length
		{"character varying without len", "character varying", nil, nil, nil, "varchar"},
		// Character with length
		{"character with len", "character", intPtr(10), nil, nil, "char(10)"},
		// Numeric with precision + scale
		{"numeric(10,2)", "numeric", nil, intPtr(10), intPtr(2), "decimal(10,2)"},
		// Numeric with precision only
		{"numeric(p)", "numeric", nil, intPtr(18), nil, "decimal(18)"},
		// Numeric without precision or scale
		{"numeric bare", "numeric", nil, nil, nil, "decimal"},
		// Timestamps
		{"timestamp with time zone", "timestamp with time zone", nil, nil, nil, "timestamptz"},
		{"timestamp without time zone", "timestamp without time zone", nil, nil, nil, "timestamp"},
		// Date
		{"date", "date", nil, nil, nil, "date"},
		// JSON types
		{"jsonb", "jsonb", nil, nil, nil, "jsonb"},
		{"json", "json", nil, nil, nil, "json"},
		// UUID
		{"uuid", "uuid", nil, nil, nil, "uuid"},
		// Bytea
		{"bytea", "bytea", nil, nil, nil, "bytea"},
		// Unknown type — falls through with "?:" prefix
		{"unknown foo", "foo", nil, nil, nil, "?:foo"},
		// Case insensitivity
		{"BIGINT uppercase", "BIGINT", nil, nil, nil, "int64"},
		{"Text mixed case", "Text", nil, nil, nil, "text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizePostgresType(tt.rawType, tt.charMaxLen, tt.numPrecision, tt.numScale)
			require.Equal(t, tt.want, got)
		})
	}
}
