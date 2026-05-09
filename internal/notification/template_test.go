package notification_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/notification"
)

// TestRenderTemplate_StringReplaceAll documents that the template engine is
// strings.ReplaceAll-backed (per CONTEXT D-21 minimal templating).
func TestRenderTemplate_StringReplaceAll(t *testing.T) {
	tpl := "{a} and {b} and {a}"
	out := notification.RenderTemplate(tpl, map[string]string{"a": "1", "b": "2"})
	require.Equal(t, "1 and 2 and 1", out)
	require.False(t, strings.Contains(out, "{a}"))
}
