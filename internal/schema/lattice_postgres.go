package schema

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	varcharRe = regexp.MustCompile(`^varchar\((\d+)\)$`)
	decimalRe = regexp.MustCompile(`^decimal\((\d+),\s*(\d+)\)$`)
	intRanks  = map[string]int{
		"int16": 1, "int32": 2, "int64": 3,
	}
	floatRanks = map[string]int{
		"float32": 1, "float64": 2,
	}
)

// IsWideningPostgres returns (isWidening, known) for old→new PostgreSQL type transitions.
//   - known=false: cross-family or unrecognized — caller defaults to breaking (D-09 safe default).
//   - known=true && isWidening=true: type widened (non-breaking).
//   - known=true && isWidening=false: type narrowed (breaking).
//
// Examples:
//
//	IsWideningPostgres("int32", "int64")          → (true, true)
//	IsWideningPostgres("int64", "int32")          → (false, true)
//	IsWideningPostgres("varchar(64)", "varchar(255)") → (true, true)
//	IsWideningPostgres("varchar(255)", "text")    → (true, true)
//	IsWideningPostgres("text", "varchar(255)")    → (false, true)
//	IsWideningPostgres("decimal(10,2)", "decimal(10,4)") → (true, true)   // scale up = wider
//	IsWideningPostgres("decimal(10,4)", "decimal(10,2)") → (false, true)  // scale down = narrower
//	IsWideningPostgres("text", "bytea")           → (false, false)        // cross-family
//	IsWideningPostgres("int32", "uuid")           → (false, false)        // cross-family
func IsWideningPostgres(oldType, newType string) (isWidening bool, known bool) {
	old := strings.TrimSpace(oldType)
	new_ := strings.TrimSpace(newType)

	// Identical type: trivially widening (no narrowing risk).
	if old == new_ {
		return true, true
	}

	// Integer family.
	if oldRank, oOK := intRanks[old]; oOK {
		if newRank, nOK := intRanks[new_]; nOK {
			return newRank >= oldRank, true
		}
		return false, false // int → non-int = cross-family
	}

	// Float family.
	if oldRank, oOK := floatRanks[old]; oOK {
		if newRank, nOK := floatRanks[new_]; nOK {
			return newRank >= oldRank, true
		}
		return false, false // float → non-float = cross-family
	}

	// varchar(N) / text family.
	oldVar, oldVarOK := parseVarchar(old)
	newVar, newVarOK := parseVarchar(new_)
	oldIsText := old == "text"
	newIsText := new_ == "text"

	if oldIsText || newIsText || oldVarOK || newVarOK {
		// text → varchar(*) = narrowing (text is unbounded).
		if oldIsText && newVarOK {
			return false, true
		}
		// varchar(*) → text = widening.
		if oldVarOK && newIsText {
			return true, true
		}
		// varchar(N1) → varchar(N2).
		if oldVarOK && newVarOK {
			return newVar >= oldVar, true
		}
		// text → text handled by old == new_ guard above.
		// Anything else in this family (e.g., text → bytea): cross-family.
		return false, false
	}

	// decimal(p,s) family.
	// Widening requires both precision AND scale to be >= (per D-09 table):
	//   decimal(8,2) → decimal(10,2): precision up, scale equal → widening ✓
	//   decimal(10,2) → decimal(10,4): scale up (more fractional digits) → widening ✓
	//   decimal(10,4) → decimal(10,2): scale down → narrowing ✓
	//   decimal(10,4) → decimal(8,2): both down → narrowing ✓
	op, os, oOK := parseDecimal(old)
	np, ns, nOK := parseDecimal(new_)
	if oOK && nOK {
		return (np >= op && ns >= os), true
	}

	// Cross-family (e.g., int32 → uuid, bool → text, decimal → varchar).
	return false, false
}

// parseVarchar extracts the length parameter from "varchar(N)".
// Returns (N, true) on success, (0, false) if the string is not varchar(N).
func parseVarchar(t string) (int, bool) {
	m := varcharRe.FindStringSubmatch(t)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// parseDecimal extracts (precision, scale) from "decimal(p,s)".
// Returns (p, s, true) on success, (0, 0, false) if not a decimal(p,s) string.
func parseDecimal(t string) (precision, scale int, ok bool) {
	m := decimalRe.FindStringSubmatch(t)
	if m == nil {
		return 0, 0, false
	}
	p, err1 := strconv.Atoi(m[1])
	s, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return p, s, true
}
