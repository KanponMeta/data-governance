package main

import (
	"testing"
)

// TestRunSchemaAckBreak_RequiresReason verifies that runSchemaAckBreak returns an error
// when --reason is omitted (D-10: no silent acks).
func TestRunSchemaAckBreak_RequiresReason(t *testing.T) {
	err := runSchemaAckBreak([]string{
		"some_asset",
		"00000000-0000-0000-0000-000000000001",
		"--actor=00000000-0000-0000-0000-000000000002",
		// --reason intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error when --reason is omitted, got nil")
	}
}

// TestRunSchemaAckBreak_RequiresActor verifies that runSchemaAckBreak returns an error
// when --actor is omitted.
func TestRunSchemaAckBreak_RequiresActor(t *testing.T) {
	err := runSchemaAckBreak([]string{
		"some_asset",
		"00000000-0000-0000-0000-000000000001",
		`--reason=intentional drop`,
		// --actor intentionally omitted
	})
	if err == nil {
		t.Fatal("expected error when --actor is omitted, got nil")
	}
}

// TestRunSchemaAckBreak_BadChangeID verifies that runSchemaAckBreak returns an error
// when the change_id positional argument is not a valid UUID.
func TestRunSchemaAckBreak_BadChangeID(t *testing.T) {
	err := runSchemaAckBreak([]string{
		"some_asset",
		"not-a-uuid",
		`--reason=intentional`,
		"--actor=00000000-0000-0000-0000-000000000002",
	})
	if err == nil {
		t.Fatal("expected error for non-UUID change_id, got nil")
	}
}

// TestRunSchemaAckBreak_MissingPositionalArgs verifies that runSchemaAckBreak returns
// an error when asset or change_id positional args are missing.
func TestRunSchemaAckBreak_MissingPositionalArgs(t *testing.T) {
	err := runSchemaAckBreak([]string{
		"--reason=intentional",
		"--actor=00000000-0000-0000-0000-000000000002",
	})
	if err == nil {
		t.Fatal("expected error for missing positional args, got nil")
	}
}

// TestRunSchemaDiff_RequiresFlags verifies that runSchemaDiff returns an error
// when required flags (--asset, --from, --to) are missing.
func TestRunSchemaDiff_RequiresFlags(t *testing.T) {
	// All flags missing.
	err := runSchemaDiff([]string{})
	if err == nil {
		t.Fatal("expected error for missing --asset/--from/--to flags, got nil")
	}
}

// TestRunSchemaDiff_RequiresFrom verifies that runSchemaDiff returns an error
// when --from is omitted.
func TestRunSchemaDiff_RequiresFrom(t *testing.T) {
	err := runSchemaDiff([]string{
		"--asset=my_asset",
		// --from omitted
		"--to=00000000-0000-0000-0000-000000000002",
	})
	if err == nil {
		t.Fatal("expected error for missing --from flag, got nil")
	}
}

// TestDispatchSchema_UnknownSub verifies that dispatchSchema returns an error
// for an unrecognized subcommand.
func TestDispatchSchema_UnknownSub(t *testing.T) {
	err := dispatchSchema([]string{"frobnicate"})
	if err == nil {
		t.Fatal("expected error for unknown schema subcommand, got nil")
	}
}

// TestDispatchSchema_NoArgs verifies that dispatchSchema returns an error when
// called with no arguments.
func TestDispatchSchema_NoArgs(t *testing.T) {
	err := dispatchSchema([]string{})
	if err == nil {
		t.Fatal("expected error for empty schema dispatch, got nil")
	}
}
