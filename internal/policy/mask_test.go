package policy_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/policy"
)

// TestApplyHash_Deterministic verifies repeated calls with the same value
// + same salt return the same digest (HMAC-SHA256 determinism).
func TestApplyHash_Deterministic(t *testing.T) {
	policy.ResetSaltForTest()
	t.Setenv("MASK_HASH_SALT", "deterministic-salt")
	t.Setenv("GOV_ENV", "")

	first, err := policy.ApplyHash("alice@example.com")
	require.NoError(t, err)

	for i := 0; i < 99; i++ {
		got, err := policy.ApplyHash("alice@example.com")
		require.NoError(t, err)
		require.Equal(t, first, got, "iter %d: HMAC-SHA256 must be deterministic", i)
	}
}

// TestApplyHash_DifferentValuesDifferentHashes verifies distinct inputs
// produce distinct digests.
func TestApplyHash_DifferentValuesDifferentHashes(t *testing.T) {
	policy.ResetSaltForTest()
	t.Setenv("MASK_HASH_SALT", "salt1")
	t.Setenv("GOV_ENV", "")

	a, err := policy.ApplyHash("alice")
	require.NoError(t, err)
	b, err := policy.ApplyHash("bob")
	require.NoError(t, err)

	require.NotEqual(t, a, b)
	require.Len(t, a, 64, "hex(SHA-256) is 64 hex chars")
}

// TestApplyHash_RequiresSaltInProd — GOV_ENV=prod + empty MASK_HASH_SALT
// surfaces ErrMaskSaltMissing.
func TestApplyHash_RequiresSaltInProd(t *testing.T) {
	policy.ResetSaltForTest()
	t.Setenv("MASK_HASH_SALT", "")
	t.Setenv("GOV_ENV", "prod")

	_, err := policy.ApplyHash("any-value")
	require.ErrorIs(t, err, policy.ErrMaskSaltMissing)
}

// TestApplyHash_NoErrInDev — without GOV_ENV, an empty salt is permitted
// (insecure but useful for repeatable local testing).
func TestApplyHash_NoErrInDev(t *testing.T) {
	policy.ResetSaltForTest()
	t.Setenv("MASK_HASH_SALT", "")
	t.Setenv("GOV_ENV", "")

	got, err := policy.ApplyHash("any-value")
	require.NoError(t, err)
	require.Len(t, got, 64)
}

// TestApplyRedact_AlwaysReturnsThreeStars — irrespective of input length.
func TestApplyRedact_AlwaysReturnsThreeStars(t *testing.T) {
	cases := []string{"", "a", "ab", "abcdefghij", strings.Repeat("x", 1024)}
	for _, in := range cases {
		require.Equal(t, "***", policy.ApplyRedact(in), "input=%q", in)
	}
}

// TestApplyPartial_ShortValue_FullyRedacted — input shorter than 2*reveal+1
// is fully redacted to avoid revealing the entire string.
func TestApplyPartial_ShortValue_FullyRedacted(t *testing.T) {
	require.Equal(t, "***", policy.ApplyPartial("abc", 2))
	require.Equal(t, "***", policy.ApplyPartial("abcd", 2))
	require.Equal(t, "***", policy.ApplyPartial("abcde", 2))
}

// TestApplyPartial_LongValue_Reveal2 — input "alice@example.com" with reveal=2
// → "al" + ('*' x 13) + "om" = "al*************om".
func TestApplyPartial_LongValue_Reveal2(t *testing.T) {
	got := policy.ApplyPartial("alice@example.com", 2)
	require.Equal(t, "al*************om", got)
	require.Len(t, got, len("alice@example.com"))
}

// TestApplyPartial_DefaultRevealOnZero — reveal=0 falls back to 2.
func TestApplyPartial_DefaultRevealOnZero(t *testing.T) {
	got := policy.ApplyPartial("hello-world", 0)
	require.Equal(t, "he*******ld", got)
}

// TestApply_DispatchByMaskType — table-driven over the three mask types.
func TestApply_DispatchByMaskType(t *testing.T) {
	policy.ResetSaltForTest()
	t.Setenv("MASK_HASH_SALT", "deterministic-salt")
	t.Setenv("GOV_ENV", "")

	cases := []struct {
		name    string
		mt      connector.MaskType
		value   string
		reveal  int
		want    string
		isHex   bool
	}{
		{"hash", connector.MaskHash, "ssn-1234", 0, "", true},
		{"redact", connector.MaskRedact, "anything", 0, "***", false},
		{"partial-reveal2", connector.MaskPartial, "alice@example.com", 2, "al*************om", false},
		{"partial-default", connector.MaskPartial, "alice@example.com", 0, "al*************om", false},
		{"unknown-passthrough", connector.MaskType("blowfish"), "passthru", 0, "passthru", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := policy.Apply(tc.mt, tc.value, tc.reveal)
			require.NoError(t, err)
			if tc.isHex {
				require.Len(t, got, 64)
			} else {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

// TestDefaultMaskForPII_IsRedact verifies the v1 fallback is redact.
func TestDefaultMaskForPII_IsRedact(t *testing.T) {
	require.Equal(t, connector.MaskRedact, policy.DefaultMaskForPII())
}
