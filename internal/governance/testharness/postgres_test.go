package testharness

import (
	"testing"
)

func TestPostgresContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping testcontainer in -short mode")
	}

	db, cleanup := NewTestPostgres(t)
	defer cleanup()

	// Verify current_role is platform_app.
	var role string
	if err := db.QueryRow("SELECT current_role").Scan(&role); err != nil {
		t.Fatalf("query current_role: %v", err)
	}
	if role != "platform_app" {
		t.Errorf("current_role = %q, want %q", role, "platform_app")
	}

	// Verify audit schema exists.
	var count int
	if err := db.QueryRow(`
		SELECT 1 FROM information_schema.schemata WHERE schema_name = 'audit'
	`).Scan(&count); err != nil {
		t.Fatalf("query audit schema: %v", err)
	}
	if count != 1 {
		t.Errorf("audit schema count = %d, want 1", count)
	}
}
