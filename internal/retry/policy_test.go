package retry_test

import (
	"testing"
	"time"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/retry"
)

// basePolicy is a non-zero policy for general test use.
var basePolicy = asset.RetryPolicy{
	Max:          5,
	InitialDelay: 30 * time.Second,
	MaxDelay:     5 * time.Minute,
	JitterPct:    0, // no jitter — deterministic
}

// TestSchedule_ExponentialBackoff verifies attempt 1 = InitialDelay,
// attempt 2 = 2×InitialDelay, caps at MaxDelay.
func TestSchedule_ExponentialBackoff(t *testing.T) {
	policy := asset.RetryPolicy{
		Max:          10,
		InitialDelay: 30 * time.Second,
		MaxDelay:     5 * time.Minute,
		JitterPct:    0,
	}

	cases := []struct {
		attempt  int
		expected time.Duration
	}{
		{1, 30 * time.Second},           // 30s * 2^0 = 30s
		{2, 60 * time.Second},           // 30s * 2^1 = 60s
		{3, 120 * time.Second},          // 30s * 2^2 = 120s
		{4, 240 * time.Second},          // 30s * 2^3 = 240s
		{5, 5 * time.Minute},            // 30s * 2^4 = 480s, capped at 300s
		{6, 5 * time.Minute},            // still capped
	}

	for _, tc := range cases {
		got := retry.Schedule(tc.attempt, policy)
		if got != tc.expected {
			t.Errorf("Schedule(attempt=%d): got %v, want %v", tc.attempt, got, tc.expected)
		}
	}
}

// TestSchedule_Jitter verifies that JitterPct=25 keeps all 100 trials within ±25%.
func TestSchedule_Jitter(t *testing.T) {
	policy := asset.RetryPolicy{
		Max:          10,
		InitialDelay: 30 * time.Second,
		MaxDelay:     5 * time.Minute,
		JitterPct:    25,
	}

	unjittered := 30 * time.Second // attempt=1, no jitter
	lo := time.Duration(float64(unjittered) * 0.75)
	hi := time.Duration(float64(unjittered) * 1.25)

	for i := 0; i < 100; i++ {
		got := retry.Schedule(1, policy)
		if got < lo || got > hi {
			t.Errorf("trial %d: Schedule returned %v which is outside [%v, %v]", i, got, lo, hi)
		}
	}
}

// TestShouldRetry_ExhaustsWhenAttemptGTEMax verifies ShouldRetry returns false when
// attempt >= policy.Max.
func TestShouldRetry_ExhaustsWhenAttemptGTEMax(t *testing.T) {
	policy := asset.RetryPolicy{Max: 3, InitialDelay: 1 * time.Second}

	// attempt < Max → should retry
	for _, attempt := range []int{1, 2} {
		if !retry.ShouldRetry(attempt, policy) {
			t.Errorf("ShouldRetry(attempt=%d, Max=3): expected true, got false", attempt)
		}
	}

	// attempt >= Max → should not retry
	for _, attempt := range []int{3, 4, 5} {
		if retry.ShouldRetry(attempt, policy) {
			t.Errorf("ShouldRetry(attempt=%d, Max=3): expected false, got true", attempt)
		}
	}
}

// TestShouldRetry_ZeroPolicy verifies that a zero policy never retries.
func TestShouldRetry_ZeroPolicy(t *testing.T) {
	zero := asset.RetryPolicy{} // zero value
	if !zero.IsZero() {
		t.Fatal("RetryPolicy{}.IsZero() should be true")
	}
	if retry.ShouldRetry(1, zero) {
		t.Fatal("ShouldRetry(1, zero) should be false for zero policy")
	}
}

// TestSchedule_ZeroPolicy returns 0 for zero policy.
func TestSchedule_ZeroPolicy(t *testing.T) {
	zero := asset.RetryPolicy{}
	if d := retry.Schedule(1, zero); d != 0 {
		t.Fatalf("Schedule(1, zero) should return 0, got %v", d)
	}
}

// TestSchedule_AttemptLessThan1 returns 0 for attempt < 1.
func TestSchedule_AttemptLessThan1(t *testing.T) {
	if d := retry.Schedule(0, basePolicy); d != 0 {
		t.Fatalf("Schedule(0, ...) should return 0, got %v", d)
	}
	if d := retry.Schedule(-1, basePolicy); d != 0 {
		t.Fatalf("Schedule(-1, ...) should return 0, got %v", d)
	}
}
