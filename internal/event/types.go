package event

import "time"

// EventType enumerates the Phase 1 event types per CONTEXT D-10.
// Phase 2 will add `run.*`; Phase 5 will add `governance.*`.
type EventType string

const (
	EventTypeUserRegistered             EventType = "user.registered"
	EventTypeUserInvited                EventType = "user.invited"
	EventTypeAuthLogin                  EventType = "auth.login"
	EventTypeAuthLogout                 EventType = "auth.logout"
	EventTypeAuthTokenExpired           EventType = "auth.token_expired"
	EventTypePlatformStarted            EventType = "platform.started"
	EventTypePlatformMigrationApplied   EventType = "platform.migration_applied"
)

// AllPhase1Types returns the set of valid EventType values for Phase 1.
// Used by the writer to reject unknown types before they reach the DB.
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

// ===== Typed payloads =====

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
