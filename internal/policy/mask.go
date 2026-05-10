// Mask functions implement the in-pipeline masking transforms used when a
// connector does NOT implement connector.MaskingProvisioner (Phase 5
// RBAC-05). The asset.MaskingIO decorator (Plan 05-03 Task 2) calls
// policy.Apply on every row before delegating to the inner AssetIO.Write.
//
// Three transforms are supported, matching the warehouse-native catalogue
// in connector.MaskType:
//
//   - hash    — hex(HMAC-SHA256(salt, value)). Salt comes from the
//               MASK_HASH_SALT env var; in production (GOV_ENV=prod) an
//               empty salt is rejected with ErrMaskSaltMissing.
//   - redact  — always returns "***" (no leakage of length).
//   - partial — reveals leading and trailing characters and replaces the
//               middle with '*' (default reveal=2). Short values fall
//               back to redact to avoid revealing the entire string.
//
// All functions are pure Go — no warehouse round-trips.
package policy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/kanpon/data-governance/internal/connector"
)

// ErrMaskSaltMissing is returned by Salt() when the deployment runs with
// GOV_ENV=prod but MASK_HASH_SALT is empty. Hash-mask callers MUST surface
// this error to the executor — silent fallback to an empty salt would let
// an attacker rebuild a rainbow table from the deployment's hash output.
var ErrMaskSaltMissing = errors.New("policy: MASK_HASH_SALT env var is empty in prod mode")

var (
	saltOnce    sync.Once
	cachedSalt  string
	cachedErr   error
)

// Salt returns the deployment-wide HMAC salt or ErrMaskSaltMissing in
// production mode. The value is memoised after the first read so test code
// must be careful: tests that mutate the env should use ResetSaltForTest
// (test-only helper) to flush the cache.
func Salt() (string, error) {
	saltOnce.Do(func() {
		cachedSalt = os.Getenv("MASK_HASH_SALT")
		if cachedSalt == "" && os.Getenv("GOV_ENV") == "prod" {
			cachedErr = ErrMaskSaltMissing
		}
	})
	return cachedSalt, cachedErr
}

// ResetSaltForTest flushes the memoised salt so subsequent Salt() calls
// re-read MASK_HASH_SALT / GOV_ENV. Test-only helper — production code
// must NOT call this.
func ResetSaltForTest() {
	saltOnce = sync.Once{}
	cachedSalt = ""
	cachedErr = nil
}

// ApplyHash returns the deterministic HMAC-SHA256 hex digest of value using
// the deployment salt. In dev mode (no GOV_ENV) an empty salt is permitted
// and used as the HMAC key — useful for repeatable local testing but
// INSECURE for production.
func ApplyHash(value string) (string, error) {
	salt, err := Salt()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// ApplyRedact unconditionally returns "***" — no length leakage.
func ApplyRedact(_ string) string { return "***" }

// ApplyPartial reveals reveal leading and reveal trailing characters and
// replaces the middle with '*'. If the value is too short to reveal both
// sides without overlap (rune-count <= 2*reveal+1), the entire value is
// redacted. reveal <= 0 is normalised to 2.
//
// Indexes by rune (not byte) so multi-byte UTF-8 sequences are not split
// mid-rune; previously, a 3-byte character with reveal=2 could expose
// 2 bytes of a 3-byte rune, producing invalid UTF-8 and partially leaking
// the supposedly-masked character (WR-06).
func ApplyPartial(value string, reveal int) string {
	if reveal <= 0 {
		reveal = 2
	}
	runes := []rune(value)
	if len(runes) <= 2*reveal+1 {
		return ApplyRedact(value)
	}
	mid := strings.Repeat("*", len(runes)-2*reveal)
	return string(runes[:reveal]) + mid + string(runes[len(runes)-reveal:])
}

// Apply dispatches to the right ApplyXxx based on the supplied MaskType.
// reveal is consulted only for MaskPartial. Unknown mask types pass the
// value through unchanged — callers (asset.MaskingIO) treat any unknown
// MaskType as a no-op so a future extension does not break running pipelines.
func Apply(mt connector.MaskType, value string, reveal int) (string, error) {
	switch mt {
	case connector.MaskHash:
		return ApplyHash(value)
	case connector.MaskRedact:
		return ApplyRedact(value), nil
	case connector.MaskPartial:
		return ApplyPartial(value, reveal), nil
	default:
		return value, nil
	}
}

// DefaultMaskForPII returns the safe-default MaskType applied when a column
// carries pii=true (after propagation) but has no column_policy row. v1
// hardcodes redact — the configurable knob lands in plan 06.
func DefaultMaskForPII() connector.MaskType { return connector.MaskRedact }
