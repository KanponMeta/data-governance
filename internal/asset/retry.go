package asset

import "time"

// RetryPolicy is per-asset retry configuration (D-15). InitialDelay grows
// exponentially up to MaxDelay; JitterPct (0..100) randomizes each delay.
type RetryPolicy struct {
	Max          int
	InitialDelay time.Duration
	MaxDelay     time.Duration
	JitterPct    int
}

// IsZero reports whether the policy is unset and the platform default should apply.
func (r RetryPolicy) IsZero() bool {
	return r.Max == 0 && r.InitialDelay == 0 && r.MaxDelay == 0 && r.JitterPct == 0
}

// DefaultRetryPolicy returns the zero-value policy used as the engine fallback when
// the asset omits Retry(...) and the platform-level default in startup config also unset.
func DefaultRetryPolicy() RetryPolicy { return RetryPolicy{} }
