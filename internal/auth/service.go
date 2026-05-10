package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/casbin/casbin/v2"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
	"github.com/kanpon/data-governance/internal/storage/ent"
	"github.com/kanpon/data-governance/internal/storage/ent/invitetoken"
	"github.com/kanpon/data-governance/internal/storage/ent/user"
)

// Sentinel errors for service operations.
var (
	ErrEmailAlreadyUsed  = errors.New("auth: email already used")
	ErrInviteExpired     = errors.New("auth: invite expired")
	ErrInviteAlreadyUsed = errors.New("auth: invite already used")
	ErrInviteNotFound    = errors.New("auth: invite not found")
)

// Input/output types for service operations.

type RegisterInput struct {
	Email    string
	Password string
}

type RegisterOutput struct {
	UserID uuid.UUID
	Role   string
}

type LoginOutput struct {
	Token     string
	ExpiresAt time.Time
	UserID    uuid.UUID
	Role      string
}

type InviteOutput struct {
	InviteID  uuid.UUID
	RawToken  string
	ExpiresAt time.Time
}

type AcceptInviteInput struct {
	RawToken string
	Password string
}

type AcceptInviteOutput struct {
	UserID uuid.UUID
}

// Service provides authentication domain operations.
type Service struct {
	store     storage.Storage
	events    event.Writer
	issuer    *TokenIssuer
	enforcer  *casbin.Enforcer
	clock     func() time.Time
	inviteTTL time.Duration
}

// NewService constructs a Service with the supplied dependencies.
// Defaults: clock=time.Now, inviteTTL=72h.
func NewService(store storage.Storage, events event.Writer, issuer *TokenIssuer) *Service {
	return &Service{
		store:     store,
		events:    events,
		issuer:    issuer,
		clock:     time.Now,
		inviteTTL: 72 * time.Hour,
	}
}

// SetEnforcer sets the Casbin enforcer for RBAC policy management.
func (s *Service) SetEnforcer(e *casbin.Enforcer) { s.enforcer = e }

// Register creates a new user account. The first user in the system is given
// role=admin; subsequent registrations default to role=member. Emits
// user.registered after the transaction commits.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*RegisterOutput, error) {
	if _, err := mail.ParseAddress(in.Email); err != nil {
		return nil, fmt.Errorf("auth: invalid email: %w", err)
	}

	passwordHash, err := HashPassword(in.Password)
	if err != nil {
		return nil, err // caller maps to 400
	}

	var out *RegisterOutput
	err = s.store.WithTx(ctx, func(tx *ent.Tx) error {
		count, err := tx.User.Query().Count(ctx)
		if err != nil {
			return fmt.Errorf("count users: %w", err)
		}

		role := user.RoleMember
		if count == 0 {
			role = user.RoleAdmin
		}

		u, err := tx.User.Create().
			SetEmail(in.Email).
			SetPasswordHash(passwordHash).
			SetRole(role).
			SetStatus(user.StatusActive).
			Save(ctx)
		if err != nil {
			if sqlgraph.IsConstraintError(err) {
				return ErrEmailAlreadyUsed
			}
			return fmt.Errorf("create user: %w", err)
		}

		out = &RegisterOutput{UserID: u.ID, Role: string(u.Role)}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Emit event after commit.
	s.events.Append(ctx, event.Event{
		Type:         event.EventTypeUserRegistered,
		OccurredAt:   s.clock().UTC(),
		ResourceType: "user",
		ResourceID:   out.UserID.String(),
		ActorID:      &out.UserID,
		Payload: event.UserRegisteredPayload{
			UserID: out.UserID.String(),
			Email:  in.Email,
		},
	})

	return out, nil
}

// Login authenticates a user by email and password. On success returns a JWT
// and emits auth.login with the actor's ID. On failure returns
// ErrInvalidCredentials with no event emitted (prevents user enumeration).
func (s *Service) Login(ctx context.Context, email, password, userAgent, remoteIP string) (*LoginOutput, error) {
	u, err := s.store.Ent().User.Query().Where(user.Email(email)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("query user: %w", err)
	}

	if err := VerifyPassword(u.PasswordHash, password); err != nil {
		// Wrong password — no event emitted (T-05-01).
		return nil, ErrInvalidCredentials
	}

	token, expiresAt, err := s.issuer.Issue(u.ID, string(u.Role))
	if err != nil {
		return nil, fmt.Errorf("issue token: %w", err)
	}

	s.events.Append(ctx, event.Event{
		Type:         event.EventTypeAuthLogin,
		OccurredAt:   s.clock().UTC(),
		ResourceType: "user",
		ResourceID:   u.ID.String(),
		ActorID:      &u.ID,
		Payload: event.AuthLoginPayload{
			UserID:    u.ID.String(),
			UserAgent: userAgent,
			RemoteIP:  remoteIP,
		},
	})

	return &LoginOutput{
		Token:     token,
		ExpiresAt: expiresAt,
		UserID:    u.ID,
		Role:      string(u.Role),
	}, nil
}

// Invite generates a single-use invite token for email. The token is returned
// exactly once as RawToken; only its sha256 hash is stored. Emits user.invited.
func (s *Service) Invite(ctx context.Context, adminID uuid.UUID, email string) (*InviteOutput, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate token: %w", err)
	}
	rawToken := base64.RawURLEncoding.EncodeToString(raw)

	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])

	expiresAt := s.clock().Add(s.inviteTTL)

	tok, err := s.store.Ent().InviteToken.Create().
		SetTokenHash(tokenHash).
		SetEmail(email).
		SetInvitedBy(adminID).
		SetExpiresAt(expiresAt).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("create invite token: %w", err)
	}

	s.events.Append(ctx, event.Event{
		Type:         event.EventTypeUserInvited,
		OccurredAt:   s.clock().UTC(),
		ResourceType: "invite_token",
		ResourceID:   tok.ID.String(),
		ActorID:      &adminID,
		Payload: event.UserInvitedPayload{
			InviteID:  tok.ID.String(),
			Email:     email,
			InvitedBy: adminID.String(),
			ExpiresAt: expiresAt,
		},
	})

	return &InviteOutput{
		InviteID:  tok.ID,
		RawToken:  rawToken,
		ExpiresAt: expiresAt,
	}, nil
}

// AcceptInvite consumes a raw invite token to create a new member account.
// The operation is atomic: token lookup, user creation, and accepted_at update
// all occur within a single transaction. Emits user.registered.
func (s *Service) AcceptInvite(ctx context.Context, in AcceptInviteInput) (*AcceptInviteOutput, error) {
	tokenHash := sha256Hash(in.RawToken)

	var out *AcceptInviteOutput
	err := s.store.WithTx(ctx, func(tx *ent.Tx) error {
		tok, err := tx.InviteToken.Query().
			Where(invitetoken.TokenHash(tokenHash)).
			Only(ctx)
		if err != nil {
			if ent.IsNotFound(err) {
				return ErrInviteNotFound
			}
			return fmt.Errorf("query invite token: %w", err)
		}

		if tok.AcceptedAt != nil {
			return ErrInviteAlreadyUsed
		}
		if tok.ExpiresAt.Before(s.clock()) {
			return ErrInviteExpired
		}

		passwordHash, err := HashPassword(in.Password)
		if err != nil {
			return err
		}

		u, err := tx.User.Create().
			SetEmail(tok.Email).
			SetPasswordHash(passwordHash).
			SetRole(user.RoleMember).
			SetStatus(user.StatusActive).
			Save(ctx)
		if err != nil {
			if sqlgraph.IsConstraintError(err) {
				return ErrEmailAlreadyUsed
			}
			return fmt.Errorf("create user: %w", err)
		}

		now := s.clock()
		if _, err := tx.InviteToken.UpdateOne(tok).SetAcceptedAt(now).Save(ctx); err != nil {
			return fmt.Errorf("mark token accepted: %w", err)
		}

		out = &AcceptInviteOutput{UserID: u.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.events.Append(ctx, event.Event{
		Type:         event.EventTypeUserRegistered,
		OccurredAt:   s.clock().UTC(),
		ResourceType: "user",
		ResourceID:   out.UserID.String(),
		ActorID:      &out.UserID,
		Payload: event.UserRegisteredPayload{
			UserID: out.UserID.String(),
			Email:  "", // not readily available post-invite
		},
	})

	return out, nil
}

// sha256Hash returns the hex-encoded sha256 sum of data.
func sha256Hash(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// ---- Role management (RBAC-01/RBAC-02) ----

// CreateRole creates a new named role. The caller must have admin role.
// Emits role.created audit entry inside the same transaction.
func (s *Service) CreateRole(ctx context.Context, actor uuid.UUID, name, description string) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("create_role: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Insert role.
	res, err := tx.ExecContext(ctx, `
		INSERT INTO roles (name, description, created_by_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (name) DO NOTHING
	`, name, description, actor)
	if err != nil {
		return fmt.Errorf("create_role: insert: %w", err)
	}

	// WR-10: only emit audit when a row was actually created. Previously
	// every call appended a role.created entry even when ON CONFLICT
	// DO NOTHING short-circuited the insert, making the audit chain
	// indistinguishable between a true create and a no-op replay.
	rows, raErr := res.RowsAffected()
	if raErr != nil {
		return fmt.Errorf("create_role: rows affected: %w", raErr)
	}
	if rows == 0 {
		// Role already exists — no-op; commit to release the tx and return.
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("create_role: commit (no-op): %w", err)
		}
		return nil
	}

	// Audit entry inside same tx.
	_, err = audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    audit.AuditRoleCreated,
		OccurredAt:   s.clock().UTC(),
		ActorID:      &actor,
		ResourceType: "role",
		ResourceID:   name,
		Payload:      map[string]any{"name": name, "description": description},
	})
	if err != nil {
		return fmt.Errorf("create_role: audit write: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("create_role: commit: %w", err)
	}
	return nil
}

// DeleteRole deletes a named role. The caller must have admin role.
// Emits role.deleted audit entry inside the same transaction.
func (s *Service) DeleteRole(ctx context.Context, actor uuid.UUID, name string) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete_role: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Remove role assignments first (soft-delete via revoked_at would be better,
	// but for simplicity we hard-delete).
	_, err = tx.ExecContext(ctx, `DELETE FROM role_assignments WHERE role_name = $1`, name)
	if err != nil {
		return fmt.Errorf("delete_role: remove assignments: %w", err)
	}

	// Remove Casbin policy entries for this role.
	if s.enforcer != nil {
		_, _ = s.enforcer.RemoveFilteredNamedGroupingPolicy("g", 0, "role:"+name)
	}

	// Delete role.
	_, err = tx.ExecContext(ctx, `DELETE FROM roles WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("delete_role: delete: %w", err)
	}

	// Audit entry.
	_, err = audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    audit.AuditRoleDeleted,
		OccurredAt:   s.clock().UTC(),
		ActorID:      &actor,
		ResourceType: "role",
		ResourceID:   name,
		Payload:      map[string]any{"name": name},
	})
	if err != nil {
		return fmt.Errorf("delete_role: audit write: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete_role: commit: %w", err)
	}
	return nil
}

// AssignRole assigns a role to a user. The caller must have admin role.
// Emits role.assigned audit entry and updates Casbin policy in same transaction.
// Note: the Casbin adapter writes to casbin_rule through its own connection,
// so the policy update is not in the same DB transaction as the role_assignment.
// This is acceptable since RolesForUser (the JWT source) reads from role_assignments,
// not from Casbin.
func (s *Service) AssignRole(ctx context.Context, actor, user uuid.UUID, role string) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("assign_role: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Insert role assignment.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO role_assignments (user_id, role_name, granted_by_id)
		VALUES ($1, $2, $3)
		ON CONFLICT DO NOTHING
	`, user, role, actor)
	if err != nil {
		return fmt.Errorf("assign_role: insert assignment: %w", err)
	}

	// Get user email for Casbin grouping policy.
	var email string
	err = tx.QueryRowContext(ctx, `SELECT email FROM "user" WHERE id = $1`, user).Scan(&email)
	if err != nil {
		return fmt.Errorf("assign_role: get user email: %w", err)
	}

	// Update Casbin policy (uses the enforcer's own adapter connection).
	if s.enforcer != nil {
		_, _ = s.enforcer.AddGroupingPolicy(email, "role:"+role)
	}

	// Audit entry.
	_, err = audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    audit.AuditRoleAssigned,
		OccurredAt:   s.clock().UTC(),
		ActorID:      &actor,
		ResourceType: "user",
		ResourceID:   user.String(),
		Payload:      map[string]any{"role": role, "user_id": user.String()},
	})
	if err != nil {
		return fmt.Errorf("assign_role: audit write: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("assign_role: commit: %w", err)
	}
	return nil
}

// RevokeRole revokes a role from a user. The caller must have admin role.
// Emits role.revoked audit entry inside same transaction.
func (s *Service) RevokeRole(ctx context.Context, actor, user uuid.UUID, role string) error {
	tx, err := s.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("revoke_role: begin tx: %w", err)
	}
	defer tx.Rollback()

	// Soft revoke: set revoked_at.
	result, err := tx.ExecContext(ctx, `
		UPDATE role_assignments
		SET revoked_at = NOW(), revoked_by_id = $1
		WHERE user_id = $2 AND role_name = $3 AND revoked_at IS NULL
	`, actor, user, role)
	if err != nil {
		return fmt.Errorf("revoke_role: update: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return errors.New("revoke_role: no active assignment found")
	}

	// Remove from Casbin grouping policy.
	var email string
	err = tx.QueryRowContext(ctx, `SELECT email FROM "user" WHERE id = $1`, user).Scan(&email)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("revoke_role: get user email: %w", err)
	}
	if s.enforcer != nil && email != "" {
		_, _ = s.enforcer.RemoveGroupingPolicy(email, "role:"+role)
	}

	// Audit entry.
	_, err = audit.WriteEntry(ctx, tx, audit.Entry{
		EventType:    audit.AuditRoleRevoked,
		OccurredAt:   s.clock().UTC(),
		ActorID:      &actor,
		ResourceType: "user",
		ResourceID:   user.String(),
		Payload:      map[string]any{"role": role, "user_id": user.String()},
	})
	if err != nil {
		return fmt.Errorf("revoke_role: audit write: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("revoke_role: commit: %w", err)
	}
	return nil
}

// RolesForUser returns the list of active (non-revoked) roles for a user.
func (s *Service) RolesForUser(ctx context.Context, user uuid.UUID) ([]string, error) {
	rows, err := s.store.DB().QueryContext(ctx, `
		SELECT role_name FROM role_assignments
		WHERE user_id = $1 AND revoked_at IS NULL
	`, user)
	if err != nil {
		return nil, fmt.Errorf("roles_for_user: query: %w", err)
	}
	defer rows.Close()

	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, fmt.Errorf("roles_for_user: scan: %w", err)
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}
