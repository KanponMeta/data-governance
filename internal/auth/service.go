package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"entgo.io/ent/dialect/sql/sqlgraph"
	"github.com/google/uuid"
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
