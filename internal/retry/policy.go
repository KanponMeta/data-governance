// Package retry implements exponential backoff with optional jitter for the
// execution engine's business-fault retry path (D-14 Option B, D-15).
//
// Infrastructure-fault recovery (worker crash, OOM) is handled separately by
// plan 02-04's stale-run reaper via runs.last_heartbeat — NOT by retry.Schedule.
package retry

import (
	"math"
	"math/rand/v2"
	"time"

	"github.com/kanpon/data-governance/internal/asset"
)

// Schedule returns the next delay for the given attempt number (1-indexed) under the
// supplied policy. Implements exponential backoff with optional jitter (D-15).
//
//	delay = min(policy.InitialDelay * 2^(attempt-1), policy.MaxDelay)
//	jittered = delay * (1 + rand[-jitter, +jitter])
//
// Attempt 1 uses InitialDelay, attempt 2 uses 2×InitialDelay, etc.
// Returns 0 when policy.IsZero() (no retry configured) or attempt < 1.
func Schedule(attempt int, policy asset.RetryPolicy) time.Duration {
	if policy.IsZero() || attempt < 1 {
		return 0
	}
	// 1-indexed attempt: first retry is attempt=1, uses InitialDelay.
	exp := math.Pow(2, float64(attempt-1))
	delay := time.Duration(float64(policy.InitialDelay) * exp)
	if policy.MaxDelay > 0 && delay > policy.MaxDelay {
		delay = policy.MaxDelay
	}
	if policy.JitterPct > 0 {
		// Random factor in [-jitter, +jitter].
		j := float64(policy.JitterPct) / 100.0
		factor := 1.0 + (rand.Float64()*2-1)*j
		delay = time.Duration(float64(delay) * factor)
		if delay < 0 {
			delay = 0
		}
	}
	return delay
}

// ShouldRetry reports whether another attempt is permitted under the policy.
// attempt is the 1-indexed count of attempts ALREADY MADE (1 = first attempt completed).
//
// Returns false when policy.IsZero() (no retry configured) or attempt >= policy.Max.
func ShouldRetry(attempt int, policy asset.RetryPolicy) bool {
	if policy.IsZero() {
		return false
	}
	return attempt < policy.Max
}
