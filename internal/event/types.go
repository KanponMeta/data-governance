package event

import "time"

// EventType enumerates the Phase 1 event types per CONTEXT D-10.
// Phase 2 adds `run.*`; Phase 5 will add `governance.*`.
type EventType string

const (
	EventTypeUserRegistered           EventType = "user.registered"
	EventTypeUserInvited              EventType = "user.invited"
	EventTypeAuthLogin                EventType = "auth.login"
	EventTypeAuthLogout               EventType = "auth.logout"
	EventTypeAuthTokenExpired         EventType = "auth.token_expired"
	EventTypePlatformStarted          EventType = "platform.started"
	EventTypePlatformMigrationApplied EventType = "platform.migration_applied"

	// Phase 2 (D-18) — run lifecycle events.
	EventTypeRunQueued               EventType = "run.queued"
	EventTypeRunStarted              EventType = "run.started"
	EventTypeRunStepStarted          EventType = "run.step.started"
	EventTypeRunStepSucceeded        EventType = "run.step.succeeded"
	EventTypeRunStepFailed           EventType = "run.step.failed"
	EventTypeRunStepRetryScheduled   EventType = "run.step.retry_scheduled" // D-18 (plan 02-03)
	EventTypeRunSucceeded            EventType = "run.succeeded"
	EventTypeRunFailed               EventType = "run.failed"
	EventTypeRunCanceled             EventType = "run.canceled"

	// Phase 3 (D-17) — schedule lifecycle events.
	EventTypeScheduleFired   EventType = "schedule.fired"
	EventTypeScheduleMissed  EventType = "schedule.missed"
	EventTypeSchedulePaused  EventType = "schedule.paused"
	EventTypeScheduleResumed EventType = "schedule.resumed"

	// Phase 3 (D-17) — sensor lifecycle events.
	EventTypeSensorEvaluated        EventType = "sensor.evaluated"
	EventTypeSensorFired            EventType = "sensor.fired"
	EventTypeSensorEvaluationFailed EventType = "sensor.evaluation_failed"
	EventTypeSensorDisabled         EventType = "sensor.disabled"
	EventTypeSensorCooldownSkipped  EventType = "sensor.cooldown_skipped"
	EventTypeSensorDedupSkipped     EventType = "sensor.dedup_skipped"

	// Phase 3 (D-17) — backfill lifecycle events.
	EventTypeBackfillSubmitted   EventType = "backfill.submitted"
	EventTypeBackfillRunEnqueued EventType = "backfill.run_enqueued"
	EventTypeBackfillCompleted   EventType = "backfill.completed"

	// Phase 4 (D-21) — lineage capture events.
	EventTypeLineageCaptured      EventType = "lineage.captured"
	EventTypeLineageDriftDetected EventType = "lineage.drift_detected"

	// Phase 4 (D-21) — schema capture events.
	EventTypeSchemaCaptured          EventType = "schema.captured"
	EventTypeSchemaUnchanged         EventType = "schema.unchanged"
	EventTypeSchemaChangeDetected    EventType = "schema.change_detected"
	EventTypeSchemaCaptureFailed     EventType = "schema.capture_failed"
	EventTypeSchemaBreakAcknowledged EventType = "schema.break_acknowledged"

	// Phase 4 (D-21) — metadata mutation event (assets and columns share the type).
	EventTypeMetadataUpdated EventType = "metadata.updated"

	// Phase 5 (Plan 05-05) — quality rule lifecycle events.
	EventTypeQualityRulePassed    EventType = "quality.rule_passed"
	EventTypeQualityRuleFailed    EventType = "quality.rule_failed"
	EventTypeQualityRuleError     EventType = "quality.rule_error"
	EventTypeQualityRunEvaluated  EventType = "quality.run_evaluated"

	// Phase 5 (Plan 05-05) — SLA / freshness events.
	EventTypeSLABreached EventType = "sla.breached"
	EventTypeSLARecovered EventType = "sla.recovered"

	// Phase 5 (Plan 05-05) — notification dispatch events.
	EventTypeNotificationDispatched     EventType = "notification.dispatched"
	EventTypeNotificationDispatchFailed EventType = "notification.dispatch_failed"

	// Phase 5 (Plan 05-04) — governance event_log subset (D-23).
	// governance.materialization_blocked is emitted to event_log on EVERY blocked
	// materialization (high-volume) and ALSO to the audit_log hash-chain (access
	// control event). reviewer_reassigned is event_log only — operational, not
	// hash-chain (audit log captures the next decision).
	EventTypeGovernanceMaterializationBlocked EventType = "governance.materialization_blocked"
	EventTypeGovernanceReviewerReassigned     EventType = "governance.reviewer_reassigned"
)

// AllKnownTypes returns the complete set of valid EventType values including Phase 2 run.* types.
// Used by the writer to reject unknown types before they reach the DB.
func AllKnownTypes() []EventType {
	return []EventType{
		EventTypeUserRegistered,
		EventTypeUserInvited,
		EventTypeAuthLogin,
		EventTypeAuthLogout,
		EventTypeAuthTokenExpired,
		EventTypePlatformStarted,
		EventTypePlatformMigrationApplied,
		// Phase 2 run lifecycle events
		EventTypeRunQueued,
		EventTypeRunStarted,
		EventTypeRunStepStarted,
		EventTypeRunStepSucceeded,
		EventTypeRunStepFailed,
		EventTypeRunStepRetryScheduled,
		EventTypeRunSucceeded,
		EventTypeRunFailed,
		EventTypeRunCanceled,
		// Phase 3 schedule events (D-17)
		EventTypeScheduleFired,
		EventTypeScheduleMissed,
		EventTypeSchedulePaused,
		EventTypeScheduleResumed,
		// Phase 3 sensor events (D-17)
		EventTypeSensorEvaluated,
		EventTypeSensorFired,
		EventTypeSensorEvaluationFailed,
		EventTypeSensorDisabled,
		EventTypeSensorCooldownSkipped,
		EventTypeSensorDedupSkipped,
		// Phase 3 backfill events (D-17)
		EventTypeBackfillSubmitted,
		EventTypeBackfillRunEnqueued,
		EventTypeBackfillCompleted,
		// Phase 4 lineage events (D-21)
		EventTypeLineageCaptured,
		EventTypeLineageDriftDetected,
		// Phase 4 schema events (D-21)
		EventTypeSchemaCaptured,
		EventTypeSchemaUnchanged,
		EventTypeSchemaChangeDetected,
		EventTypeSchemaCaptureFailed,
		EventTypeSchemaBreakAcknowledged,
		// Phase 4 metadata event (D-21)
		EventTypeMetadataUpdated,
		// Phase 5 quality + SLA + notification events (Plan 05-05)
		EventTypeQualityRulePassed,
		EventTypeQualityRuleFailed,
		EventTypeQualityRuleError,
		EventTypeQualityRunEvaluated,
		EventTypeSLABreached,
		EventTypeSLARecovered,
		EventTypeNotificationDispatched,
		EventTypeNotificationDispatchFailed,
		// Phase 5 governance event_log subset (Plan 05-04, D-23)
		EventTypeGovernanceMaterializationBlocked,
		EventTypeGovernanceReviewerReassigned,
	}
}

// AllPhase1Types returns the Phase 1 event types.
// Deprecated: use AllKnownTypes() which includes Phase 2+ types. Kept for backwards compatibility.
func AllPhase1Types() []EventType {
	return []EventType{
		EventTypeUserRegistered,
		EventTypeUserInvited,
		EventTypeAuthLogin,
		EventTypeAuthLogout,
		EventTypeAuthTokenExpired,
		EventTypePlatformStarted,
		EventTypePlatformMigrationApplied,
	}
}

// ===== Phase 1 Typed payloads =====

type UserRegisteredPayload struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
}

type UserInvitedPayload struct {
	InviteID  string    `json:"invite_id"`
	Email     string    `json:"email"`
	InvitedBy string    `json:"invited_by"`
	ExpiresAt time.Time `json:"expires_at"`
}

type AuthLoginPayload struct {
	UserID    string `json:"user_id"`
	UserAgent string `json:"user_agent,omitempty"`
	RemoteIP  string `json:"remote_ip,omitempty"`
}

type AuthLogoutPayload struct {
	UserID string `json:"user_id"`
}

type AuthTokenExpiredPayload struct {
	UserID string `json:"user_id"`
}

type PlatformStartedPayload struct {
	Version string `json:"version"`
}

type PlatformMigrationAppliedPayload struct {
	AppliedAt time.Time `json:"applied_at"`
	Version   string    `json:"version,omitempty"`
}

// ===== Phase 2 Typed payloads (D-18) =====

type RunQueuedPayload struct {
	AssetName string `json:"asset_name"`
	Trigger   string `json:"trigger"`
}

type RunStartedPayload struct {
	AssetName string `json:"asset_name"`
	ClaimedBy string `json:"claimed_by"`
}

type RunStepStartedPayload struct {
	AssetName string `json:"asset_name"`
	TopoOrder int    `json:"topo_order"`
	Attempt   int    `json:"attempt"`
}

type RunStepSucceededPayload struct {
	AssetName   string         `json:"asset_name"`
	RowsWritten int64          `json:"rows_written"`
	DurationMs  int64          `json:"duration_ms"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type RunStepFailedPayload struct {
	AssetName  string `json:"asset_name"`
	Attempt    int    `json:"attempt"`
	Error      string `json:"error"`
	DurationMs int64  `json:"duration_ms"`
}

type RunSucceededPayload struct {
	AssetName  string `json:"asset_name"`
	DurationMs int64  `json:"duration_ms"`
}

type RunFailedPayload struct {
	AssetName  string `json:"asset_name"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error"`
}

type RunCanceledPayload struct {
	AssetName string `json:"asset_name"`
	Reason    string `json:"reason"`
}

// RunStepRetryScheduledPayload is the payload for EventTypeRunStepRetryScheduled (D-18, plan 02-03).
// It records each retry attempt for the audit trail — satisfies acceptance criterion 2.
type RunStepRetryScheduledPayload struct {
	AssetName   string    `json:"asset_name"`
	Attempt     int       `json:"attempt"`      // attempt number that just FAILED (1-indexed)
	NextAttempt int       `json:"next_attempt"`
	ScheduledAt time.Time `json:"scheduled_at"` // when the retry will run
	DelayMs     int64     `json:"delay_ms"`
	Error       string    `json:"error"`
}

// ===== Phase 4 Typed payloads (D-17, D-21) =====

// MetadataUpdatedPayload is the typed payload for metadata.updated events (D-17, D-21).
// The event_type constant EventTypeMetadataUpdated is defined in plan 04-02 task 3.
type MetadataUpdatedPayload struct {
	Asset       string   `json:"asset"`
	Column      *string  `json:"column,omitempty"`
	ActorID     string   `json:"actor_id"`
	BeforeDesc  string   `json:"before_description,omitempty"`
	BeforeOwner string   `json:"before_owner,omitempty"`
	BeforeTags  []string `json:"before_tags,omitempty"`
	AfterDesc   string   `json:"after_description,omitempty"`
	AfterOwner  string   `json:"after_owner,omitempty"`
	AfterTags   []string `json:"after_tags,omitempty"`
	Merge       bool     `json:"merge"`
}

// ===== Phase 5 Typed payloads (Plan 05-05) =====

// QualityRulePayload is emitted for every quality.rule_passed | rule_failed | rule_error event.
type QualityRulePayload struct {
	Asset         string   `json:"asset"`
	Rule          string   `json:"rule"`
	Type          string   `json:"type"`
	Status        string   `json:"status"`
	MeasuredValue *float64 `json:"measured_value,omitempty"`
	Threshold     *float64 `json:"threshold,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// QualityRunEvaluatedPayload is emitted once per evaluated run after all rules ran.
type QualityRunEvaluatedPayload struct {
	Asset     string `json:"asset"`
	Worst     string `json:"worst"` // passed | failed | error | skipped
	RuleCount int    `json:"rule_count"`
}

// SLABreachedPayload reports an asset whose last_succeeded_at exceeded its freshness budget.
type SLABreachedPayload struct {
	Asset            string  `json:"asset"`
	MaxLagSeconds    int     `json:"max_lag_seconds"`
	LastSucceededAt  *string `json:"last_succeeded_at,omitempty"`
	BreachWindowStart string `json:"breach_window_start"`
}

// SLARecoveredPayload reports the first successful materialize after a breach.
type SLARecoveredPayload struct {
	Asset string `json:"asset"`
}

// NotificationDispatchedPayload reports a successful channel send.
type NotificationDispatchedPayload struct {
	Channel   string `json:"channel"`
	EventType string `json:"event_type"`
	Asset     string `json:"asset,omitempty"`
}

// NotificationDispatchFailedPayload reports a permanent failure for a channel send.
type NotificationDispatchFailedPayload struct {
	Channel   string `json:"channel,omitempty"`
	EventType string `json:"event_type"`
	Asset     string `json:"asset,omitempty"`
	Error     string `json:"error"`
}

// ===== Phase 5 Plan 05-04 typed payloads =====

// GovernanceMaterializationBlockedPayload reports a step refused by the
// executor's governance gate (D-08). Emitted to event_log on every blocked
// run + to audit_log (hash-chain) once per blocked attempt.
type GovernanceMaterializationBlockedPayload struct {
	Asset        string `json:"asset"`
	CurrentState string `json:"current_state"`
	CodeHash     string `json:"code_hash"`
	RunID        string `json:"run_id,omitempty"`
}

// GovernanceReviewerReassignedPayload reports a reviewer pool rotation on an
// in-flight review (Pitfall #12). Operational event_log only — the next
// approve/reject writes the hash-chain entry.
type GovernanceReviewerReassignedPayload struct {
	ReviewID     string   `json:"review_id"`
	Asset        string   `json:"asset"`
	OldReviewers []string `json:"old_reviewers"`
	NewReviewers []string `json:"new_reviewers"`
	ActorID      string   `json:"actor_id"`
}
