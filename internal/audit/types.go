// Package audit provides the hash-chain audit log infrastructure for Phase 5.
//
// Hash-chain protocol: each entry's self_hash = SHA-256(seq || prev_hash ||
// ts || event_type || actor || resource_type || resource_id || JCS(payload)).
// The chain is initialized with a sentinel row (seq=0, self_hash=32 zero bytes).
// RLS + REVOKE prevent platform_app from UPDATE/DELETE rows in audit.audit_log.
package audit

import (
	"time"

	"github.com/google/uuid"
)

// AuditEventType identifies the class of governance event being recorded.
type AuditEventType string

// All defined audit event types (D-13/D-14/D-15/D-16/D-23).
const (
	AuditPolicyChanged                  AuditEventType = "policy.changed"
	AuditPolicyRemoved                  AuditEventType = "policy.removed"
	AuditMaskingSyncFailed              AuditEventType = "masking.sync_failed"
	AuditMaskingSyncDriftDetected       AuditEventType = "masking.sync_drift_detected"
	AuditRoleCreated                    AuditEventType = "role.created"
	AuditRoleDeleted                    AuditEventType = "role.deleted"
	AuditRoleAssigned                   AuditEventType = "role.assigned"
	AuditRoleRevoked                    AuditEventType = "role.revoked"
	AuditGovernanceSubmitted            AuditEventType = "governance.submitted"
	AuditGovernanceApproved             AuditEventType = "governance.approved"
	AuditGovernanceRejected             AuditEventType = "governance.rejected"
	AuditGovernanceAutoApproved         AuditEventType = "governance.auto_approved"
	AuditGovernanceReviewSLABreached    AuditEventType = "governance.review_sla_breached"
	AuditGovernanceMaterializationBlocked AuditEventType = "governance.materialization_blocked"
	AuditGovernanceReviewerReassigned  AuditEventType = "governance.reviewer_reassigned"
	AuditExported                       AuditEventType = "audit.exported"
	AuditVerifyFailed                   AuditEventType = "audit.verify_failed"
	AuditMetadataTagOverridden          AuditEventType = "metadata.tag_overridden"
)

// AllAuditEventTypes returns all defined audit event types in declaration order.
func AllAuditEventTypes() []AuditEventType {
	return []AuditEventType{
		AuditPolicyChanged,
		AuditPolicyRemoved,
		AuditMaskingSyncFailed,
		AuditMaskingSyncDriftDetected,
		AuditRoleCreated,
		AuditRoleDeleted,
		AuditRoleAssigned,
		AuditRoleRevoked,
		AuditGovernanceSubmitted,
		AuditGovernanceApproved,
		AuditGovernanceRejected,
		AuditGovernanceAutoApproved,
		AuditGovernanceReviewSLABreached,
		AuditGovernanceMaterializationBlocked,
		AuditGovernanceReviewerReassigned,
		AuditExported,
		AuditVerifyFailed,
		AuditMetadataTagOverridden,
	}
}

// Entry describes a single audit log record to be written to the hash chain.
type Entry struct {
	EventType    AuditEventType
	OccurredAt   time.Time
	ActorID      *uuid.UUID
	ResourceType string
	ResourceID   string
	Payload      any
	ExpiresAt    *time.Time
}
