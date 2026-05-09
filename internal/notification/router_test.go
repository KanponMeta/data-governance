package notification_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/notification"
)

func TestRouter_ExactMatch(t *testing.T) {
	cfg := &notification.Config{Rules: []notification.RuleConfig{
		{Match: "governance.submitted", Webhook: "https://gov"},
	}}
	r := notification.NewRouter(cfg, []byte("s"), nil)
	chs := r.Route(context.Background(), "governance.submitted")
	require.Len(t, chs, 1)
	require.Equal(t, "webhook", chs[0].Name())
}

func TestRouter_NoMatch_NoChannels(t *testing.T) {
	cfg := &notification.Config{Rules: []notification.RuleConfig{
		{Match: "quality.rule_failed", Webhook: "https://x"},
	}}
	r := notification.NewRouter(cfg, []byte("s"), nil)
	chs := r.Route(context.Background(), "schedule.fired")
	require.Empty(t, chs)
}

func TestRouter_GlobMatch(t *testing.T) {
	cfg := &notification.Config{Rules: []notification.RuleConfig{
		{Match: "governance.*", Webhook: "https://gov"},
	}}
	r := notification.NewRouter(cfg, []byte("s"), nil)
	for _, evt := range []string{"governance.submitted", "governance.approved", "governance.rejected"} {
		chs := r.Route(context.Background(), evt)
		require.Len(t, chs, 1, "expected match for %s", evt)
	}
}

func TestRouter_StarMatchesAll(t *testing.T) {
	cfg := &notification.Config{Rules: []notification.RuleConfig{
		{Match: "*", Webhook: "https://catchall"},
	}}
	r := notification.NewRouter(cfg, []byte("s"), nil)
	require.Len(t, r.Route(context.Background(), "anything.at.all"), 1)
}

func TestRouter_LoadConfig_FromExampleYAML(t *testing.T) {
	// The example yaml ships in configs/notifications.example.yaml.
	abs, err := filepath.Abs("../../configs/notifications.example.yaml")
	require.NoError(t, err)
	cfg, err := notification.LoadConfig(abs)
	require.NoError(t, err)
	require.NotEmpty(t, cfg.Rules)
}

func TestRenderTemplate_Substitutes(t *testing.T) {
	out := notification.RenderTemplate("hello {name}", map[string]string{"name": "ops"})
	require.Equal(t, "hello ops", out)
}

func TestRenderTemplate_LeavesUnknownVarsIntact(t *testing.T) {
	out := notification.RenderTemplate("ping {missing}", nil)
	require.Equal(t, "ping {missing}", out)
}
