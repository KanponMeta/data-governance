package config

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// minSigningKeyBytes enforces HS256 best-practice (key >= hash output size = 32 bytes).
const minSigningKeyBytes = 32

// Config holds all platform runtime settings derived from env vars.
type Config struct {
	HTTPAddr      string
	DatabaseURL   string
	JWTSigningKey []byte
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration
	LogLevel      string
}

// Load reads env vars and validates them. Returns a fully-populated Config or
// an error explaining the first validation failure.
func Load() (Config, error) {
	cfg := Config{
		HTTPAddr: getEnvDefault("PLATFORM_HTTP_ADDR", ":8080"),
		LogLevel: getEnvDefault("PLATFORM_LOG_LEVEL", "info"),
	}

	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("config: DATABASE_URL is required")
	}

	signingKey := os.Getenv("JWT_SIGNING_KEY")
	if len(signingKey) < minSigningKeyBytes {
		return Config{}, fmt.Errorf("config: JWT_SIGNING_KEY must be at least %d bytes (got %d)", minSigningKeyBytes, len(signingKey))
	}
	cfg.JWTSigningKey = []byte(signingKey)

	accessTTLRaw := getEnvDefault("JWT_ACCESS_TTL", "15m")
	d, err := time.ParseDuration(accessTTLRaw)
	if err != nil {
		return Config{}, fmt.Errorf("config: invalid JWT_ACCESS_TTL %q: %w", accessTTLRaw, err)
	}
	cfg.JWTAccessTTL = d

	refreshTTLRaw := getEnvDefault("JWT_REFRESH_TTL", "168h")
	d, err = time.ParseDuration(refreshTTLRaw)
	if err != nil {
		return Config{}, fmt.Errorf("config: invalid JWT_REFRESH_TTL %q: %w", refreshTTLRaw, err)
	}
	cfg.JWTRefreshTTL = d

	return cfg, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}