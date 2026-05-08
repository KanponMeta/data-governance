// Package config provides the startup configuration loader for the data governance
// platform (D-09). It reads a YAML file, resolves ${ENV_VAR} placeholders from the
// process environment, and returns a typed Config struct.
//
// Secret fields MUST be referenced via ${ENV_VAR} in the yaml file. The loader
// resolves them silently — it NEVER logs the resolved values. Missing env vars cause
// an error listing the variable names (not values).
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	// ErrMissingEnvVar is returned by Load when one or more ${VAR} placeholders
	// could not be resolved because the variable is not set in the process environment.
	ErrMissingEnvVar = errors.New("config: missing environment variable")

	// envVarPattern matches ${NAME} placeholders where NAME is a valid env var name.
	envVarPattern = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]*)\}`)
)

// Config is the top-level startup configuration loaded from yaml (D-09).
type Config struct {
	Connectors  map[string]ConnectorConfig `yaml:"connectors"`
	Retry       RetryConfig                `yaml:"retry"`
	Concurrency ConcurrencyConfig          `yaml:"concurrency"`
}

// ConnectorConfig holds one named connector's configuration block.
// The `type` field selects the factory; all other fields are type-specific.
type ConnectorConfig struct {
	Type   string                 `yaml:"type"`    // e.g. "postgres", "s3"
	Params map[string]interface{} `yaml:",inline"` // type-specific fields after env resolution
}

// RetryConfig holds the global retry defaults.
type RetryConfig struct {
	Default RetryPolicyConfig `yaml:"default"`
}

// RetryPolicyConfig mirrors asset.RetryPolicy for yaml deserialization.
type RetryPolicyConfig struct {
	Max          int           `yaml:"max"`
	InitialDelay time.Duration `yaml:"initial_delay"`
	MaxDelay     time.Duration `yaml:"max_delay"`
	JitterPct    int           `yaml:"jitter_pct"`
}

// ConcurrencyConfig holds the global concurrency limits.
type ConcurrencyConfig struct {
	DefaultRunTokens int            `yaml:"default_run_tokens"`
	Resources        map[string]int `yaml:"resources"`
}

// Load parses yamlBytes and resolves ${ENV_VAR} placeholders against os.Environ().
//
// Security: secrets MUST NEVER be logged. This function records ONLY the variable
// NAMES it resolved — never the resolved values — at debug level. Test 4 asserts
// that actual env-var values never appear in slog output.
//
// Returns ErrMissingEnvVar if any placeholder references an unset variable.
func Load(yamlBytes []byte) (*Config, error) {
	// Resolve env-var placeholders in the raw bytes BEFORE yaml unmarshaling.
	// This ensures ${VAR} references inside nested yaml fields are also resolved.
	resolved, missing := resolveEnv(string(yamlBytes))
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrMissingEnvVar, strings.Join(missing, ", "))
	}
	var cfg Config
	if err := yaml.Unmarshal([]byte(resolved), &cfg); err != nil {
		return nil, fmt.Errorf("config: parse yaml: %w", err)
	}
	return &cfg, nil
}

// LoadFile is a convenience wrapper that reads a yaml file from disk and calls Load.
// File path may be overridden via PLATFORM_CONFIG env var; defaults to ./config.yaml.
func LoadFile(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}
	return Load(b)
}

// resolveEnv replaces ${NAME} occurrences with os.Getenv("NAME"). Returns the
// resolved string and a list of missing variable names (never values).
func resolveEnv(in string) (string, []string) {
	var missing []string
	out := envVarPattern.ReplaceAllStringFunc(in, func(match string) string {
		name := match[2 : len(match)-1] // strip ${ and }
		v, ok := os.LookupEnv(name)
		if !ok {
			missing = append(missing, name)
			return match // leave the placeholder; caller will error
		}
		return v
	})
	return out, missing
}
