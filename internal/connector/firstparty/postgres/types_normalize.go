package postgres

import (
	"fmt"
	"strings"
)

// normalizePostgresType maps PostgreSQL native data_type values to the
// platform-normalized type names used in connector.SchemaColumn.Type (D-07).
//
// Mapping table is the authoritative source from 04-RESEARCH.md §6.
// Adding a new mapping requires both updating this function and adding a
// test case in types_normalize_test.go.
//
// Caller supplies:
//   - rawType: information_schema.columns.data_type (e.g. "character varying", "numeric")
//   - charMaxLen: information_schema.columns.character_maximum_length (nil for non-string types)
//   - numPrecision: information_schema.columns.numeric_precision (nil for non-numeric types)
//   - numScale: information_schema.columns.numeric_scale (nil for non-numeric types)
//
// Unrecognized types fall through to the rawType verbatim with a leading "?:"
// marker so Wave 4's diff classifier defaults them to "needs_review" / breaking
// (Pitfall 5 + D-09 safe default for out-of-lattice changes).
func normalizePostgresType(rawType string, charMaxLen, numPrecision, numScale *int) string {
	switch strings.ToLower(rawType) {
	case "smallint":
		return "int16"
	case "integer":
		return "int32"
	case "bigint":
		return "int64"
	case "real":
		return "float32"
	case "double precision":
		return "float64"
	case "boolean":
		return "bool"
	case "text":
		return "text"
	case "character varying":
		if charMaxLen != nil {
			return fmt.Sprintf("varchar(%d)", *charMaxLen)
		}
		return "varchar"
	case "character":
		if charMaxLen != nil {
			return fmt.Sprintf("char(%d)", *charMaxLen)
		}
		return "char"
	case "numeric":
		if numPrecision != nil && numScale != nil {
			return fmt.Sprintf("decimal(%d,%d)", *numPrecision, *numScale)
		}
		if numPrecision != nil {
			return fmt.Sprintf("decimal(%d)", *numPrecision)
		}
		return "decimal"
	case "timestamp with time zone":
		return "timestamptz"
	case "timestamp without time zone":
		return "timestamp"
	case "date":
		return "date"
	case "jsonb":
		return "jsonb"
	case "json":
		return "json"
	case "uuid":
		return "uuid"
	case "bytea":
		return "bytea"
	default:
		return "?:" + rawType
	}
}
