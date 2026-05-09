package notification

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RuleConfig is one rule in notifications.yaml.
type RuleConfig struct {
	Match   string `yaml:"match"`
	Webhook string `yaml:"webhook,omitempty"`
	EmailTo string `yaml:"email_to,omitempty"`
}

// Config is the parsed notifications.yaml top-level shape.
type Config struct {
	Rules []RuleConfig `yaml:"rules"`
}

// LoadConfig reads + parses the YAML file at path. Empty path returns an
// empty Config (no routes — silent OK on every event).
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		return &Config{}, nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("notification.LoadConfig: abs: %w", err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("notification.LoadConfig: read %s: %w", abs, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return nil, fmt.Errorf("notification.LoadConfig: parse %s: %w", abs, err)
	}
	return &cfg, nil
}

// Router resolves a dispatched event_type to the ordered list of channels
// that should deliver it. Cached load + SIGHUP reload is the worker's job
// (see worker.go); Router itself is read-only after construction.
type Router struct {
	rules      []RuleConfig
	secret     []byte
	smtp       *SMTPChannel
	httpClient *http.Client
}

// NewRouter constructs a Router. secret is the HMAC signing key; smtp is the
// shared SMTP channel (nil → email rules silently skipped).
func NewRouter(cfg *Config, secret []byte, smtp *SMTPChannel) *Router {
	if cfg == nil {
		cfg = &Config{}
	}
	return &Router{
		rules:      append([]RuleConfig(nil), cfg.Rules...),
		secret:     secret,
		smtp:       smtp,
		httpClient: &http.Client{Timeout: DefaultWebhookTimeout},
	}
}

// Route returns the ordered list of Channels for the supplied event_type.
// Rules are evaluated top-to-bottom; the first non-empty webhook in a matching
// rule contributes a WebhookChannel; the first non-empty email_to in a matching
// rule contributes the SMTPChannel (router doesn't expand email_to itself —
// the worker resolves {var} placeholders per-payload).
func (r *Router) Route(_ context.Context, eventType string) []Channel {
	var out []Channel
	for _, rule := range r.rules {
		if !matchPattern(rule.Match, eventType) {
			continue
		}
		if rule.Webhook != "" {
			out = append(out, &WebhookChannel{
				URL:     rule.Webhook,
				Secret:  r.secret,
				Timeout: DefaultWebhookTimeout,
				Client:  r.httpClient,
			})
		}
		if rule.EmailTo != "" && r.smtp != nil {
			out = append(out, r.smtp)
		}
	}
	return out
}

// EmailToFor returns the rendered email_to template for the first matching
// rule (so the worker can resolve {var} substitutions).
func (r *Router) EmailToFor(eventType string) string {
	for _, rule := range r.rules {
		if matchPattern(rule.Match, eventType) && rule.EmailTo != "" {
			return rule.EmailTo
		}
	}
	return ""
}

// matchPattern supports exact match, "*" wildcard, and "prefix.*" glob.
func matchPattern(pattern, eventType string) bool {
	if pattern == "*" {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return pattern == eventType
	}
	// Only support trailing ".*" glob (e.g., "governance.*").
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		return strings.HasPrefix(eventType, prefix+".")
	}
	return false
}
