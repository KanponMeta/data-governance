package audit

import (
	"time"
)

// RetentionConfig describes the retention policy for audit log entries.
// v1 ships with the expires_at column and audit_purge role provisioned;
// the actual purge mechanism is deferred to v1.x.
type RetentionConfig struct {
	// DefaultDays is the default retention in days for events without a
	// per-event-type override. nil means infinite retention.
	DefaultDays *int
	// PerEventType overrides the default retention for specific event types.
	PerEventType map[AuditEventType]int
}

// ExpiresAt returns the expiration timestamp for a given event type and
// occurrence time, applying the configured retention policy.
// Returns nil if the event should not expire.
func (c RetentionConfig) ExpiresAt(eventType AuditEventType, occurredAt time.Time) *time.Time {
	days := c.defaultDays()
	if override, ok := c.PerEventType[eventType]; ok {
		days = override
	}
	if days == 0 {
		return nil // infinite
	}
	expires := occurredAt.AddDate(0, 0, days)
	return &expires
}

func (c RetentionConfig) defaultDays() int {
	if c.DefaultDays == nil {
		return 0 // infinite by default
	}
	return *c.DefaultDays
}
