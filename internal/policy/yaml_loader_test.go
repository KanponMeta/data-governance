package policy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/connector"
)

// TestLoadYAML_ParsesValid checks LoadYAML accepts a well-formed file
// and returns a populated YAMLConfig.
func TestLoadYAML_ParsesValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policies.yaml")
	body := `
tag_mask_defaults:
  pii: hash
  internal_only: redact
  partial_email: partial

tag_reviewer_roles:
  pii: [privacy-team, governance]
  financial: [finance-leads]
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	cfg, err := LoadYAML(path)
	require.NoError(t, err)
	require.Equal(t, connector.MaskHash, cfg.TagMaskDefaults["pii"])
	require.Equal(t, connector.MaskRedact, cfg.TagMaskDefaults["internal_only"])
	require.Equal(t, connector.MaskPartial, cfg.TagMaskDefaults["partial_email"])
	require.Equal(t, []string{"privacy-team", "governance"}, cfg.TagReviewerRoles["pii"])
}

// TestLoadYAML_RejectsInvalidMask returns ErrYAMLValidation when a mask is
// not one of hash|redact|partial.
func TestLoadYAML_RejectsInvalidMask(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policies.yaml")
	body := `
tag_mask_defaults:
  pii: blowfish
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	_, err := LoadYAML(path)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrYAMLValidation)
}

// TestLoadYAML_EmptyFile returns an empty config (no error).
func TestLoadYAML_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policies.yaml")
	require.NoError(t, os.WriteFile(path, []byte(""), 0o600))
	cfg, err := LoadYAML(path)
	require.NoError(t, err)
	require.Empty(t, cfg.TagMaskDefaults)
	require.Empty(t, cfg.TagReviewerRoles)
}

// TestLoadYAML_MissingFile surfaces the os.Open error.
func TestLoadYAML_MissingFile(t *testing.T) {
	_, err := LoadYAML("/no/such/path.yaml")
	require.Error(t, err)
}
