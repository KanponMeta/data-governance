package schematest

import "time"

// Column is a local mirror of the connector.Column type planned in D-07.
// Wave 4 (plan 04-05) will replace usages of this type with connector.Column
// once the SchemaDescriber interface ships. The fields here match the D-07
// specification exactly.
//
// Wave-4-replacement note: substitute `schematest.Column` → `connector.Column`
// in all Phase 4 test files once connector.Column is available.
type Column struct {
	Name         string
	Type         string  // connector-normalized: "int64", "varchar(255)", "decimal(10,2)", etc.
	Nullable     bool
	Default      *string // pointer so absence != empty string
	IsPrimaryKey bool
	Comment      string // for META-03 description seeding
}

// Schema is a local mirror of the connector.Schema type planned in D-07.
// Wave 4 (plan 04-05) will replace usages of this type with connector.Schema.
//
// Wave-4-replacement note: substitute `schematest.Schema` → `connector.Schema`
// in all Phase 4 test files once connector.Schema is available.
type Schema struct {
	Columns       []Column
	PrimaryKey    []string  // ordered column names that form the primary key
	RowCountEstim int64     // -1 if the connector cannot supply an estimate
	CapturedAt    time.Time
}

// DiffCase is a single schema diff fixture: the before/after Schema pair plus
// the expected outcome from Wave 4's classifier (META-02).
//
// Each DiffCase covers exactly one ChangeKind value — Wave 4 iterates
// DiffPairs() to build its table-driven tests.
type DiffCase struct {
	Name               string
	PrevSchema         Schema
	NextSchema         Schema
	ExpectedChangeKind string // matches wave-4 change_type enum values (D-09)
	ExpectedIsBreaking bool
}

// ptr is a helper to take the address of a string literal.
func ptr(s string) *string { return &s }

// DiffPairs returns 9 schema diff test cases — one per ChangeKind enum value
// that Wave 4's classifier emits (D-09). Missing a case means a failed Wave 4
// test, so this list is exhaustive.
//
// Breaking-change classification per D-09:
//   - Breaking:     column_dropped, type_narrowed, nullable_removed, pk_changed
//   - Non-breaking: column_added, type_widened, nullable_added, comment_changed,
//                   default_changed
func DiffPairs() []DiffCase {
	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	// Shared base columns used across multiple cases.
	col1Int32 := Column{Name: "c1", Type: "int32", Nullable: false}
	col1Int64 := Column{Name: "c1", Type: "int64", Nullable: false}
	col2 := Column{Name: "c2", Type: "text", Nullable: true}
	col3 := Column{Name: "c3", Type: "varchar(255)", Nullable: true}

	return []DiffCase{
		// (a) column_added — non-breaking: downstream consumers get the new
		// column on read; defaults handle null-on-read for older rows.
		{
			Name: "column_added",
			PrevSchema: Schema{
				Columns:    []Column{col1Int64, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns:    []Column{col1Int64, col2, col3},
				PrimaryKey: []string{"c1"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "column_added",
			ExpectedIsBreaking: false,
		},

		// (b) column_dropped — breaking: downstream consumers referencing c3
		// will fail at query time.
		{
			Name: "column_dropped",
			PrevSchema: Schema{
				Columns:    []Column{col1Int64, col2, col3},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns:    []Column{col1Int64, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "column_dropped",
			ExpectedIsBreaking: true,
		},

		// (c) type_narrowed — breaking: int64 → int32 can truncate values;
		// existing downstream code expecting int64 may overflow or error.
		{
			Name: "type_narrowed",
			PrevSchema: Schema{
				Columns:    []Column{col1Int64, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns:    []Column{col1Int32, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "type_narrowed",
			ExpectedIsBreaking: true,
		},

		// (d) type_widened — non-breaking: int32 → int64 is lossless;
		// existing consumers reading int32 values still work.
		{
			Name: "type_widened",
			PrevSchema: Schema{
				Columns:    []Column{col1Int32, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns:    []Column{col1Int64, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "type_widened",
			ExpectedIsBreaking: false,
		},

		// (e) nullable_added — non-breaking: NOT NULL → NULLABLE; existing
		// consumers already handle non-null values; new nulls are allowed.
		{
			Name: "nullable_added",
			PrevSchema: Schema{
				Columns:    []Column{{Name: "c1", Type: "int64", Nullable: false}, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns:    []Column{{Name: "c1", Type: "int64", Nullable: true}, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "nullable_added",
			ExpectedIsBreaking: false,
		},

		// (f) nullable_removed — breaking: NULLABLE → NOT NULL; existing rows
		// may contain NULLs that violate the new constraint; downstream code
		// that handles null may now receive hard errors.
		{
			Name: "nullable_removed",
			PrevSchema: Schema{
				Columns:    []Column{{Name: "c1", Type: "int64", Nullable: true}, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns:    []Column{{Name: "c1", Type: "int64", Nullable: false}, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "nullable_removed",
			ExpectedIsBreaking: true,
		},

		// (g) pk_changed — breaking: primary key composition changes;
		// downstream joins, dedup logic, and partition pruning break.
		{
			Name: "pk_changed",
			PrevSchema: Schema{
				Columns:    []Column{col1Int64, col2},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns:    []Column{col1Int64, col2},
				PrimaryKey: []string{"c1", "c2"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "pk_changed",
			ExpectedIsBreaking: true,
		},

		// (h) comment_changed — non-breaking: metadata-only; no data or type
		// semantics change; downstream consumers are unaffected.
		{
			Name: "comment_changed",
			PrevSchema: Schema{
				Columns: []Column{
					{Name: "c1", Type: "int64", Nullable: false, Comment: "old comment"},
					col2,
				},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns: []Column{
					{Name: "c1", Type: "int64", Nullable: false, Comment: "new comment"},
					col2,
				},
				PrimaryKey: []string{"c1"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "comment_changed",
			ExpectedIsBreaking: false,
		},

		// (i) default_changed — non-breaking: default value change only affects
		// new rows inserted without an explicit value; existing rows and reads
		// are unaffected.
		{
			Name: "default_changed",
			PrevSchema: Schema{
				Columns: []Column{
					{Name: "c1", Type: "int64", Nullable: false, Default: ptr("0")},
					col2,
				},
				PrimaryKey: []string{"c1"},
				CapturedAt: base,
			},
			NextSchema: Schema{
				Columns: []Column{
					{Name: "c1", Type: "int64", Nullable: false, Default: ptr("1")},
					col2,
				},
				PrimaryKey: []string{"c1"},
				CapturedAt: base.Add(time.Hour),
			},
			ExpectedChangeKind: "default_changed",
			ExpectedIsBreaking: false,
		},
	}
}
