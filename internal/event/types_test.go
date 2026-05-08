package event

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestAllPhase3EventTypes asserts the 13 Phase 3 (D-17) EventType constants
// hold the verbatim string values defined in 03-CONTEXT.md, and that
// AllKnownTypes() includes every one of them. Existing Phase 1+2 types must
// also remain present (regression guard).
func TestAllPhase3EventTypes(t *testing.T) {
	t.Parallel()

	// 1. Verify each Phase 3 constant has the exact string value from D-17.
	cases := []struct {
		name string
		got  EventType
		want string
	}{
		// Schedule (4)
		{"EventTypeScheduleFired", EventTypeScheduleFired, "schedule.fired"},
		{"EventTypeScheduleMissed", EventTypeScheduleMissed, "schedule.missed"},
		{"EventTypeSchedulePaused", EventTypeSchedulePaused, "schedule.paused"},
		{"EventTypeScheduleResumed", EventTypeScheduleResumed, "schedule.resumed"},
		// Sensor (6)
		{"EventTypeSensorEvaluated", EventTypeSensorEvaluated, "sensor.evaluated"},
		{"EventTypeSensorFired", EventTypeSensorFired, "sensor.fired"},
		{"EventTypeSensorEvaluationFailed", EventTypeSensorEvaluationFailed, "sensor.evaluation_failed"},
		{"EventTypeSensorDisabled", EventTypeSensorDisabled, "sensor.disabled"},
		{"EventTypeSensorCooldownSkipped", EventTypeSensorCooldownSkipped, "sensor.cooldown_skipped"},
		{"EventTypeSensorDedupSkipped", EventTypeSensorDedupSkipped, "sensor.dedup_skipped"},
		// Backfill (3)
		{"EventTypeBackfillSubmitted", EventTypeBackfillSubmitted, "backfill.submitted"},
		{"EventTypeBackfillRunEnqueued", EventTypeBackfillRunEnqueued, "backfill.run_enqueued"},
		{"EventTypeBackfillCompleted", EventTypeBackfillCompleted, "backfill.completed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, string(tc.got),
				"Phase 3 D-17 string for %s must be %q (got %q)",
				tc.name, tc.want, string(tc.got))
		})
	}

	// 2. Verify AllKnownTypes() includes every Phase 3 type (membership check).
	known := AllKnownTypes()
	knownSet := make(map[EventType]bool, len(known))
	for _, et := range known {
		knownSet[et] = true
	}
	for _, tc := range cases {
		assert.True(t, knownSet[tc.got],
			"AllKnownTypes() must include Phase 3 type %s (%q)", tc.name, tc.want)
	}

	// 3. Phase 1 (7) + Phase 2 (9) = 16 baseline + 13 Phase 3 = 29 minimum.
	// Use >= so adding more types in the interim does not break this guard.
	assert.GreaterOrEqual(t, len(known), 29,
		"AllKnownTypes() must contain at least 16 Phase 1+2 + 13 Phase 3 = 29 entries")

	// 4. Regression: a representative Phase 1 + Phase 2 type must still be present
	// (defense against accidental Phase 3 replace-all).
	assert.True(t, knownSet[EventTypeUserRegistered],
		"AllKnownTypes() must still include Phase 1 user.registered after Phase 3 additions")
	assert.True(t, knownSet[EventTypeRunQueued],
		"AllKnownTypes() must still include Phase 2 run.queued after Phase 3 additions")
}
