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

	// Phase 2 (D-18) — run lifecycle events. retry_scheduled is added in plan 02-03.
	EventTypeRunQueued        EventType = "run.queued"
	EventTypeRunStarted       EventType = "run.started"
	EventTypeRunStepStarted   EventType = "run.step.started"
	EventTypeRunStepSucceeded EventType = "run.step.succeeded"
	EventTypeRunStepFailed    EventType = "run.step.failed"
	EventTypeRunSucceeded     EventType = "run.succeeded"
	EventTypeRunFailed        EventType = "run.failed"
	EventTypeRunCanceled      EventType = "run.canceled"
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
		EventTypeRunSucceeded,
		EventTypeRunFailed,
		EventTypeRunCanceled,
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
