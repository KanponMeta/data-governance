package schema

// Classify returns the DB change_type string + is_breaking flag for a single SchemaChange.
// For ChangeTypeWidened (provisional from Diff), this function consults the lattice
// and may rewrite to ChangeTypeNarrowed if the lattice says narrowing.
//
// lattice is a function pointer so future connectors can register their own type
// compatibility (Phase 5+ / per-connector lattices). Callers from Phase 4 pass
// IsWideningPostgres directly.
//
// D-09 classification table:
//
//	column_added     → is_breaking=false
//	column_dropped   → is_breaking=true
//	type_widened     → is_breaking=false  (lattice says widening)
//	type_narrowed    → is_breaking=true   (lattice says narrowing or unknown)
//	nullable_added   → is_breaking=false  (NOT NULL → NULLABLE)
//	nullable_removed → is_breaking=true   (NULLABLE → NOT NULL)
//	pk_changed       → is_breaking=true
//	comment_changed  → is_breaking=false
//	default_changed  → is_breaking=false
func Classify(c SchemaChange, lattice func(old, new string) (isWidening, known bool)) (changeType string, isBreaking bool) {
	switch c.Kind {
	case ChangeColumnAdded:
		return string(ChangeColumnAdded), false

	case ChangeColumnDropped:
		return string(ChangeColumnDropped), true

	case ChangeNullableAdded:
		return string(ChangeNullableAdded), false

	case ChangeNullableRemoved:
		return string(ChangeNullableRemoved), true

	case ChangePKChanged:
		return string(ChangePKChanged), true

	case ChangeCommentChanged:
		return string(ChangeCommentChanged), false

	case ChangeDefaultChanged:
		return string(ChangeDefaultChanged), false

	case ChangeTypeWidened:
		// Diff emitted this provisionally — consult lattice for true direction.
		if c.PrevType == nil || c.NewType == nil {
			// Missing type info: safe default is breaking (D-09).
			return string(ChangeTypeNarrowed), true
		}
		isWidening, known := lattice(*c.PrevType, *c.NewType)
		if !known {
			// Out-of-lattice type change → D-09 safe default: breaking.
			return string(ChangeTypeNarrowed), true
		}
		if isWidening {
			return string(ChangeTypeWidened), false
		}
		return string(ChangeTypeNarrowed), true

	case ChangeTypeNarrowed:
		// Direct narrowing emit (rarely used; Diff emits ChangeTypeWidened provisionally).
		return string(ChangeTypeNarrowed), true
	}

	// Fallback: unknown kind → safe default (breaking).
	return string(c.Kind), true
}
