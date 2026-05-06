package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Run("valid config with all env vars", func(t *testing.T) {
		t.Setenv("JWT_SIGNING_KEY", "this-is-a-32-byte-secret-key-here")
		t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
		t.Setenv("JWT_ACCESS_TTL", "15m")
		t.Setenv("JWT_REFRESH_TTL", "168h")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.HTTPAddr != ":8080" {
			t.Errorf("HTTPAddr = %q, want %q", cfg.HTTPAddr, ":8080")
		}
		if cfg.JWTAccessTTL != 15*time.Minute {
			t.Errorf("JWTAccessTTL = %v, want %v", cfg.JWTAccessTTL, 15*time.Minute)
		}
		if cfg.JWTRefreshTTL != 168*time.Hour {
			t.Errorf("JWTRefreshTTL = %v, want %v", cfg.JWTRefreshTTL, 168*time.Hour)
		}
		if cfg.LogLevel != "info" {
			t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "info")
		}
	})

	t.Run("empty JWT_SIGNING_KEY returns error", func(t *testing.T) {
		t.Setenv("JWT_SIGNING_KEY", "")
		t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")

		_, err := Load()
		if err == nil {
			t.Fatal("Load() expected error, got nil")
		}
		if err != nil {
			if !contains(err.Error(), "JWT_SIGNING_KEY") {
				t.Errorf("error = %q, want to contain %q", err.Error(), "JWT_SIGNING_KEY")
			}
			if !contains(err.Error(), "0") {
				t.Errorf("error = %q, want to contain %q (actual byte length)", err.Error(), "0")
			}
		}
	})

	t.Run("31-byte JWT_SIGNING_KEY returns error", func(t *testing.T) {
		// Pure ASCII: 31 bytes exactly
		t.Setenv("JWT_SIGNING_KEY", "abcdefghijklmnopqrstuvwxyz12345")
		t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")

		_, err := Load()
		if err == nil {
			t.Fatal("Load() expected error, got nil")
		}
		if err != nil {
			if !contains(err.Error(), "32") {
				t.Errorf("error = %q, want to contain %q", err.Error(), "32")
			}
			if !contains(err.Error(), "31") {
				t.Errorf("error = %q, want to contain %q (actual byte length)", err.Error(), "31")
			}
		}
	})

	t.Run("32-byte JWT_SIGNING_KEY succeeds (boundary)", func(t *testing.T) {
		// Pure ASCII: exactly 32 bytes
		t.Setenv("JWT_SIGNING_KEY", "12345678901234567890123456789012")
		t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")

		_, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v, want nil for 32-byte key", err)
		}
	})

	t.Run("empty DATABASE_URL returns error", func(t *testing.T) {
		t.Setenv("JWT_SIGNING_KEY", "this-is-a-32-byte-secret-key-here")
		t.Setenv("DATABASE_URL", "")

		_, err := Load()
		if err == nil {
			t.Fatal("Load() expected error, got nil")
		}
		if err != nil {
			if !contains(err.Error(), "DATABASE_URL") {
				t.Errorf("error = %q, want to contain %q", err.Error(), "DATABASE_URL")
			}
		}
	})

	t.Run("custom JWT_ACCESS_TTL parsed correctly", func(t *testing.T) {
		t.Setenv("JWT_SIGNING_KEY", "this-is-a-32-byte-secret-key-here")
		t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
		t.Setenv("JWT_ACCESS_TTL", "5m")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.JWTAccessTTL != 5*time.Minute {
			t.Errorf("JWTAccessTTL = %v, want %v", cfg.JWTAccessTTL, 5*time.Minute)
		}
	})

	t.Run("malformed JWT_ACCESS_TTL returns error", func(t *testing.T) {
		t.Setenv("JWT_SIGNING_KEY", "this-is-a-32-byte-secret-key-here")
		t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
		t.Setenv("JWT_ACCESS_TTL", "not-a-duration")

		_, err := Load()
		if err == nil {
			t.Fatal("Load() expected error, got nil")
		}
	})

	t.Run("PLATFORM_LOG_LEVEL debug overrides default", func(t *testing.T) {
		t.Setenv("JWT_SIGNING_KEY", "this-is-a-32-byte-secret-key-here")
		t.Setenv("DATABASE_URL", "postgres://user:pass@localhost/db")
		t.Setenv("PLATFORM_LOG_LEVEL", "debug")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if cfg.LogLevel != "debug" {
			t.Errorf("LogLevel = %q, want %q", cfg.LogLevel, "debug")
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}