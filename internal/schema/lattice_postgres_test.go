package schema_test

import (
	"testing"

	"github.com/kanpon/data-governance/internal/schema"
	"github.com/stretchr/testify/assert"
)

func TestIsWideningPostgresIntegerFamily(t *testing.T) {
	tests := []struct {
		old, new    string
		wantWiden   bool
		wantKnown   bool
	}{
		// Widening
		{"int16", "int32", true, true},
		{"int16", "int64", true, true},
		{"int32", "int64", true, true},
		// Narrowing
		{"int64", "int32", false, true},
		{"int64", "int16", false, true},
		{"int32", "int16", false, true},
		// Identity (same type)
		{"int32", "int32", true, true},
		{"int64", "int64", true, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.old+"_to_"+tc.new, func(t *testing.T) {
			got, known := schema.IsWideningPostgres(tc.old, tc.new)
			assert.Equal(t, tc.wantKnown, known, "known mismatch")
			assert.Equal(t, tc.wantWiden, got, "isWidening mismatch")
		})
	}
}

func TestIsWideningPostgresFloatFamily(t *testing.T) {
	tests := []struct {
		old, new  string
		wantWiden bool
		wantKnown bool
	}{
		{"float32", "float64", true, true},
		{"float64", "float32", false, true},
		{"float32", "float32", true, true},
		{"float64", "float64", true, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.old+"_to_"+tc.new, func(t *testing.T) {
			got, known := schema.IsWideningPostgres(tc.old, tc.new)
			assert.Equal(t, tc.wantKnown, known)
			assert.Equal(t, tc.wantWiden, got)
		})
	}
}

func TestIsWideningPostgresVarchar(t *testing.T) {
	tests := []struct {
		name      string
		old, new  string
		wantWiden bool
		wantKnown bool
	}{
		{"varchar_up", "varchar(64)", "varchar(255)", true, true},
		{"varchar_down", "varchar(255)", "varchar(64)", false, true},
		{"varchar_equal", "varchar(100)", "varchar(100)", true, true},
		{"varchar_to_text", "varchar(255)", "text", true, true},
		{"text_to_varchar", "text", "varchar(255)", false, true},
		{"text_to_text", "text", "text", true, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, known := schema.IsWideningPostgres(tc.old, tc.new)
			assert.Equal(t, tc.wantKnown, known)
			assert.Equal(t, tc.wantWiden, got)
		})
	}
}

func TestIsWideningPostgresDecimal(t *testing.T) {
	// Widening: both precision AND scale must be >=
	// decimal(p1,s1) -> decimal(p2,s2) widens iff p2>=p1 AND s2>=s1
	tests := []struct {
		name      string
		old, new  string
		wantWiden bool
		wantKnown bool
	}{
		// Precision up, scale equal → widening
		{"precision_up", "decimal(8,2)", "decimal(10,2)", true, true},
		// Precision equal, scale up → widening (more fractional digits)
		{"scale_up", "decimal(10,2)", "decimal(10,4)", true, true},
		// Both up → widening
		{"both_up", "decimal(8,2)", "decimal(10,4)", true, true},
		// Scale down → narrowing
		{"scale_down", "decimal(10,4)", "decimal(10,2)", false, true},
		// Both down → narrowing
		{"both_down", "decimal(10,4)", "decimal(8,2)", false, true},
		// Precision down, scale equal → narrowing
		{"precision_down", "decimal(10,2)", "decimal(8,2)", false, true},
		// Identity
		{"identity", "decimal(10,2)", "decimal(10,2)", true, true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, known := schema.IsWideningPostgres(tc.old, tc.new)
			assert.Equal(t, tc.wantKnown, known)
			assert.Equal(t, tc.wantWiden, got)
		})
	}
}

func TestIsWideningPostgresCrossFamily(t *testing.T) {
	// Cross-family changes are unknown (not in lattice).
	tests := []struct {
		name     string
		old, new string
	}{
		{"text_to_bytea", "text", "bytea"},
		{"int32_to_uuid", "int32", "uuid"},
		{"int32_to_text", "int32", "text"},
		{"float64_to_int64", "float64", "int64"},
		{"decimal_to_varchar", "decimal(10,2)", "varchar(64)"},
		{"bool_to_int32", "bool", "int32"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, known := schema.IsWideningPostgres(tc.old, tc.new)
			assert.False(t, known, "cross-family types should be unknown")
		})
	}
}

func TestIsWideningPostgresIdentity(t *testing.T) {
	// Any type to same type → (true, true)
	types := []string{
		"int32", "int64", "float32", "float64",
		"text", "varchar(100)", "decimal(10,2)",
		"bool", "uuid", "bytea", "timestamptz",
	}
	for _, typ := range types {
		typ := typ
		t.Run(typ, func(t *testing.T) {
			got, known := schema.IsWideningPostgres(typ, typ)
			assert.True(t, known, "identity should be known")
			assert.True(t, got, "identity should be widening (no narrowing risk)")
		})
	}
}
