package main

import (
	"testing"
)

// TestRunLineageExport_RequiresAsset verifies that runLineageExport returns an error
// when --asset is omitted.
func TestRunLineageExport_RequiresAsset(t *testing.T) {
	err := runLineageExport([]string{
		"--format=openlineage",
	})
	if err == nil {
		t.Fatal("expected error for missing --asset, got nil")
	}
}

// TestRunLineageExport_UnsupportedFormat verifies that runLineageExport returns an error
// containing "unsupported_format" when an unsupported --format is specified (D-18).
func TestRunLineageExport_UnsupportedFormat(t *testing.T) {
	err := runLineageExport([]string{
		"--asset=demo",
		"--format=invalid",
	})
	if err == nil {
		t.Fatal("expected error for unsupported format, got nil")
	}
	if !contains(err.Error(), "unsupported_format") {
		t.Errorf("error message %q should contain 'unsupported_format'", err.Error())
	}
}

// TestRunLineageExport_BadSince verifies that runLineageExport returns an error
// when --since is not a valid RFC3339 timestamp.
func TestRunLineageExport_BadSince(t *testing.T) {
	err := runLineageExport([]string{
		"--asset=demo",
		"--since=not-a-date",
		"--format=openlineage",
	})
	if err == nil {
		t.Fatal("expected error for bad --since value, got nil")
	}
}

// TestDispatchLineage_UnknownSub verifies that dispatchLineage returns an error
// for an unrecognized subcommand.
func TestDispatchLineage_UnknownSub(t *testing.T) {
	err := dispatchLineage([]string{"frobnicate"})
	if err == nil {
		t.Fatal("expected error for unknown lineage subcommand, got nil")
	}
}

// TestDispatchLineage_NoArgs verifies that dispatchLineage returns an error when
// called with no arguments.
func TestDispatchLineage_NoArgs(t *testing.T) {
	err := dispatchLineage([]string{})
	if err == nil {
		t.Fatal("expected error for empty lineage dispatch, got nil")
	}
}
