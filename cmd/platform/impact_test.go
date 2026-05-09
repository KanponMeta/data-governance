package main

import (
	"testing"
)

// TestRunImpact_NoAsset verifies that runImpact returns an error when no asset argument is provided.
func TestRunImpact_NoAsset(t *testing.T) {
	err := runImpact([]string{})
	if err == nil {
		t.Fatal("expected error for missing asset argument, got nil")
	}
}

// TestRunImpact_DepthExceeded verifies that runImpact returns a non-nil error containing
// the words "depth" and "25" when --depth exceeds the hard cap (D-14).
// This path executes before any DB connection is made.
func TestRunImpact_DepthExceeded(t *testing.T) {
	err := runImpact([]string{"some_asset", "--depth=99"})
	if err == nil {
		t.Fatal("expected error for depth exceeding MaxDepth (25), got nil")
	}
	msg := err.Error()
	if !contains(msg, "depth") {
		t.Errorf("error message %q should contain 'depth'", msg)
	}
	if !contains(msg, "25") {
		t.Errorf("error message %q should contain '25'", msg)
	}
}

// TestRunImpact_BadDirection verifies that runImpact returns an error for an invalid --direction.
func TestRunImpact_BadDirection(t *testing.T) {
	err := runImpact([]string{"some_asset", "--direction=sideways"})
	if err == nil {
		t.Fatal("expected error for invalid direction, got nil")
	}
}

// TestRunImpact_BadFormat verifies that runImpact returns an error for an unsupported --format.
func TestRunImpact_BadFormat(t *testing.T) {
	err := runImpact([]string{"some_asset", "--format=csv"})
	if err == nil {
		t.Fatal("expected error for invalid format, got nil")
	}
}

// TestRunImpact_BadAsOf verifies that runImpact returns an error for a malformed --as-of value.
func TestRunImpact_BadAsOf(t *testing.T) {
	err := runImpact([]string{"some_asset", "--as-of=not-a-date"})
	if err == nil {
		t.Fatal("expected error for bad --as-of value, got nil")
	}
}

// contains checks if s contains substr (avoids importing strings in test for clarity).
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
