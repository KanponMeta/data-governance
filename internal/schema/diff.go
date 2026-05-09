package schema

import (
	"sort"

	"github.com/kanpon/data-governance/internal/connector"
)

// ChangeKind enumerates the granular schema-change types written to schema_changes.change_type
// (matches the DB CHECK constraint from plan 04-02 hand-managed appendix).
type ChangeKind string

const (
	ChangeColumnAdded     ChangeKind = "column_added"
	ChangeColumnDropped   ChangeKind = "column_dropped"
	ChangeTypeNarrowed    ChangeKind = "type_narrowed"
	ChangeTypeWidened     ChangeKind = "type_widened"
	ChangeNullableAdded   ChangeKind = "nullable_added"   // NOT NULL → NULLABLE (non-breaking)
	ChangeNullableRemoved ChangeKind = "nullable_removed" // NULLABLE → NOT NULL (BREAKING)
	ChangePKChanged       ChangeKind = "pk_changed"
	ChangeCommentChanged  ChangeKind = "comment_changed"
	ChangeDefaultChanged  ChangeKind = "default_changed"
)

// SchemaChange is the diff record. ColumnName is empty for PK-level changes.
type SchemaChange struct {
	Kind         ChangeKind
	ColumnName   string  // "" for PK-level
	PrevType     *string
	NewType      *string
	PrevNullable *bool
	NewNullable  *bool
	PrevDefault  *string
	NewDefault   *string
	PrevComment  *string
	NewComment   *string
}

// Diff returns the ordered list of changes between prev and next schemas.
// Iteration order: column_added → column_dropped → in-place attribute changes
// (ordered by column name) → pk_changed (if any).
//
// Renames are detected as drop+add — no heuristic rename detection
// (per CONTEXT.md deferred ideas).
func Diff(prev, next connector.Schema) []SchemaChange {
	var changes []SchemaChange

	prevByName := make(map[string]connector.SchemaColumn, len(prev.Columns))
	for _, c := range prev.Columns {
		prevByName[c.Name] = c
	}
	nextByName := make(map[string]connector.SchemaColumn, len(next.Columns))
	for _, c := range next.Columns {
		nextByName[c.Name] = c
	}

	// Sort names for deterministic output ordering.
	nextNames := sortedKeys(nextByName)
	prevNames := sortedKeys(prevByName)

	// 1) column_added: in next but not in prev.
	for _, name := range nextNames {
		if _, in := prevByName[name]; !in {
			nc := nextByName[name]
			t := nc.Type
			nullable := nc.Nullable
			changes = append(changes, SchemaChange{
				Kind:        ChangeColumnAdded,
				ColumnName:  name,
				NewType:     &t,
				NewNullable: &nullable,
				NewDefault:  nc.Default,
			})
		}
	}

	// 2) column_dropped: in prev but not in next.
	for _, name := range prevNames {
		if _, in := nextByName[name]; !in {
			pc := prevByName[name]
			t := pc.Type
			nullable := pc.Nullable
			changes = append(changes, SchemaChange{
				Kind:        ChangeColumnDropped,
				ColumnName:  name,
				PrevType:    &t,
				PrevNullable: &nullable,
				PrevDefault: pc.Default,
			})
		}
	}

	// 3) In-place attribute changes (per column, in name order).
	for _, name := range nextNames {
		pc, in := prevByName[name]
		if !in {
			continue
		}
		nc := nextByName[name]

		// Type change → emit type_widened provisionally. Classify will resolve
		// to type_narrowed if the lattice says narrowing (D-09).
		if pc.Type != nc.Type {
			pt, nt := pc.Type, nc.Type
			changes = append(changes, SchemaChange{
				Kind:       ChangeTypeWidened, // provisional — Classify resolves
				ColumnName: name,
				PrevType:   &pt,
				NewType:    &nt,
			})
		}

		// Nullable change.
		if pc.Nullable != nc.Nullable {
			pn, nn := pc.Nullable, nc.Nullable
			kind := ChangeNullableAdded
			if !nc.Nullable {
				kind = ChangeNullableRemoved // NULLABLE → NOT NULL
			}
			changes = append(changes, SchemaChange{
				Kind:         kind,
				ColumnName:   name,
				PrevNullable: &pn,
				NewNullable:  &nn,
			})
		}

		// Default change.
		if !ptrStringEqual(pc.Default, nc.Default) {
			changes = append(changes, SchemaChange{
				Kind:        ChangeDefaultChanged,
				ColumnName:  name,
				PrevDefault: pc.Default,
				NewDefault:  nc.Default,
			})
		}

		// Comment change.
		if pc.Comment != nc.Comment {
			pcm, ncm := pc.Comment, nc.Comment
			changes = append(changes, SchemaChange{
				Kind:        ChangeCommentChanged,
				ColumnName:  name,
				PrevComment: &pcm,
				NewComment:  &ncm,
			})
		}
	}

	// 4) PK change.
	if !stringSliceEqual(prev.PrimaryKey, next.PrimaryKey) {
		changes = append(changes, SchemaChange{Kind: ChangePKChanged})
	}

	return changes
}

// sortedKeys returns the map keys in sorted order for deterministic output.
func sortedKeys(m map[string]connector.SchemaColumn) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ptrStringEqual returns true if both pointers are nil, or both point to equal strings.
func ptrStringEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// stringSliceEqual returns true if both slices contain the same elements in the same order.
func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
