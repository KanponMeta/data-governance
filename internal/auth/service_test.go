package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
)

// integrationDatabaseURL returns the integration test database URL or empty if not set.
func integrationDatabaseURL() string {
	return os.Getenv("INTEGRATION_DATABASE_URL")
}

func skipIfNoIntegrationDB(t *testing.T) {
	if integrationDatabaseURL() == "" {
		t.Skip("INTEGRATION_DATABASE_URL not set, skipping integration test")
	}
}

// mustRandBytes returns n random bytes or panics.
func mustRandBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

// setupIntegrationStorage returns a Storage connected to the integration DB.
func setupIntegrationStorage(t *testing.T) storage.Storage {
	skipIfNoIntegrationDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := storage.NewPostgres(ctx, integrationDatabaseURL())
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	// Clean up users and invite_tokens before each test.
	store.Ent().User.Delete().Exec(ctx)
	store.Ent().InviteToken.Delete().Exec(ctx)
	return store
}

// setupIntegrationEventWriter returns an event.Writer backed by the integration DB.
func setupIntegrationEventWriter(t *testing.T, store storage.Storage) event.Writer {
	skipIfNoIntegrationDB(t)
	return event.NewWriter(store)
}

// integrationTokenIssuer returns a TokenIssuer with a fixed key valid for 15 minutes.
func integrationTokenIssuer() *TokenIssuer {
	return NewTokenIssuer(mustRandBytes(32), 15*time.Minute)
}

// TestService_Register_BootstrapAdmin tests that the first user gets role=admin.
func TestService_Register_BootstrapAdmin(t *testing.T) {
	store := setupIntegrationStorage(t)
	writer := setupIntegrationEventWriter(t, store)
	issuer := integrationTokenIssuer()
	svc := NewService(store, writer, issuer)

	ctx := context.Background()

	// When User table is empty, first user gets role=admin
	in := RegisterInput{Email: "admin@example.com", Password: "password123"}
	out, err := svc.Register(ctx, in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if out.Role != "admin" {
		t.Errorf("Register: first user expected role admin, got %q", out.Role)
	}
}

// TestService_Register_SecondUserMember tests that subsequent users get role=member.
func TestService_Register_SecondUserMember(t *testing.T) {
	store := setupIntegrationStorage(t)
	writer := setupIntegrationEventWriter(t, store)
	issuer := integrationTokenIssuer()
	svc := NewService(store, writer, issuer)

	ctx := context.Background()

	// First user is admin
	_, err := svc.Register(ctx, RegisterInput{Email: "admin2@example.com", Password: "password123"})
	if err != nil {
		t.Fatalf("Register admin: %v", err)
	}

	// Second user is member
	out, err := svc.Register(ctx, RegisterInput{Email: "member@example.com", Password: "password123"})
	if err != nil {
		t.Fatalf("Register member: %v", err)
	}
	if out.Role != "member" {
		t.Errorf("Register: second user expected role member, got %q", out.Role)
	}
}

// TestService_Register_DuplicateEmail tests that duplicate email returns ErrEmailAlreadyUsed.
func TestService_Register_DuplicateEmail(t *testing.T) {
	store := setupIntegrationStorage(t)
	writer := setupIntegrationEventWriter(t, store)
	issuer := integrationTokenIssuer()
	svc := NewService(store, writer, issuer)

	ctx := context.Background()

	in := RegisterInput{Email: "dup@example.com", Password: "password123"}
	_, err := svc.Register(ctx, in)
	if err != nil {
		t.Fatalf("Register first: %v", err)
	}

	_, err = svc.Register(ctx, in)
	if err != ErrEmailAlreadyUsed {
		t.Errorf("Register duplicate: expected ErrEmailAlreadyUsed, got %v", err)
	}
}

// TestService_Login tests successful login and invalid credential handling.
func TestService_Login(t *testing.T) {
	store := setupIntegrationStorage(t)
	writer := setupIntegrationEventWriter(t, store)
	issuer := integrationTokenIssuer()
	svc := NewService(store, writer, issuer)

	ctx := context.Background()

	// Register a user first
	regIn := RegisterInput{Email: "login@example.com", Password: "password123"}
	_, err := svc.Register(ctx, regIn)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Test: Login with correct credentials
	loginOut, err := svc.Login(ctx, "login@example.com", "password123", "test-agent", "127.0.0.1")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if loginOut.Token == "" {
		t.Error("Login: Token should not be empty")
	}
	if loginOut.UserID == (uuid.UUID{}) {
		t.Error("Login: UserID should not be zero")
	}

	// Verify the token is valid
	claims, err := issuer.Verify(loginOut.Token)
	if err != nil {
		t.Errorf("Login: issued token failed verification: %v", err)
	}
	if claims.UserID != loginOut.UserID {
		t.Errorf("Login: token claims userID mismatch")
	}

	// Test: Wrong password returns ErrInvalidCredentials
	_, err = svc.Login(ctx, "login@example.com", "wrongpassword", "test-agent", "127.0.0.1")
	if err != ErrInvalidCredentials {
		t.Errorf("Login wrong password: expected ErrInvalidCredentials, got %v", err)
	}

	// Test: Non-existent email returns ErrInvalidCredentials
	_, err = svc.Login(ctx, "nonexistent@example.com", "password123", "test-agent", "127.0.0.1")
	if err != ErrInvalidCredentials {
		t.Errorf("Login missing email: expected ErrInvalidCredentials, got %v", err)
	}
}

// TestService_Invite tests invite token creation and storage.
func TestService_Invite(t *testing.T) {
	store := setupIntegrationStorage(t)
	writer := setupIntegrationEventWriter(t, store)
	issuer := integrationTokenIssuer()
	svc := NewService(store, writer, issuer)

	ctx := context.Background()

	// Register admin
	adminOut, err := svc.Register(ctx, RegisterInput{Email: "invite-admin@example.com", Password: "password123"})
	if err != nil {
		t.Fatalf("Register admin: %v", err)
	}

	// Test: Invite creates token with 72h expiry
	inviteOut, err := svc.Invite(ctx, adminOut.UserID, "invitee@example.com")
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}
	if inviteOut.InviteID == (uuid.UUID{}) {
		t.Error("Invite: InviteID should not be zero")
	}
	if inviteOut.RawToken == "" {
		t.Error("Invite: RawToken should not be empty")
	}
	if inviteOut.ExpiresAt.Before(time.Now().Add(71 * time.Hour)) {
		t.Errorf("Invite: ExpiresAt should be at least 71h from now")
	}

	// Verify token hash is stored correctly
	token, err := store.Ent().InviteToken.Query().Only(ctx)
	if err != nil {
		t.Fatalf("Query invite token: %v", err)
	}
	if token.Email != "invitee@example.com" {
		t.Errorf("Invite: expected email invitee@example.com, got %q", token.Email)
	}
}

// TestService_AcceptInvite tests invite acceptance flow.
func TestService_AcceptInvite(t *testing.T) {
	store := setupIntegrationStorage(t)
	writer := setupIntegrationEventWriter(t, store)
	issuer := integrationTokenIssuer()
	svc := NewService(store, writer, issuer)

	ctx := context.Background()

	// Register admin
	adminOut, err := svc.Register(ctx, RegisterInput{Email: "accept-admin@example.com", Password: "password123"})
	if err != nil {
		t.Fatalf("Register admin: %v", err)
	}

	// Create invite
	inviteOut, err := svc.Invite(ctx, adminOut.UserID, "newuser@example.com")
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}

	// Test: AcceptInvite creates user
	acceptIn := AcceptInviteInput{RawToken: inviteOut.RawToken, Password: "newuserpass123"}
	acceptOut, err := svc.AcceptInvite(ctx, acceptIn)
	if err != nil {
		t.Fatalf("AcceptInvite: %v", err)
	}
	if acceptOut.UserID == (uuid.UUID{}) {
		t.Error("AcceptInvite: UserID should not be zero")
	}

	// Test: Calling again with same token returns ErrInviteAlreadyUsed
	_, err = svc.AcceptInvite(ctx, acceptIn)
	if err != ErrInviteAlreadyUsed {
		t.Errorf("AcceptInvite twice: expected ErrInviteAlreadyUsed, got %v", err)
	}
}

// TestService_AcceptInvite_Expired tests that expired invites are rejected.
func TestService_AcceptInvite_Expired(t *testing.T) {
	store := setupIntegrationStorage(t)
	writer := setupIntegrationEventWriter(t, store)
	// Use a negative TTL issuer to create already-expired tokens
	issuer := NewTokenIssuer(mustRandBytes(32), -1*time.Hour)
	svc := NewService(store, writer, issuer)

	ctx := context.Background()

	// Register admin
	adminOut, err := svc.Register(ctx, RegisterInput{Email: "expired-admin@example.com", Password: "password123"})
	if err != nil {
		t.Fatalf("Register admin: %v", err)
	}

	// Create invite (expires immediately since issuer TTL is -1h)
	inviteOut, err := svc.Invite(ctx, adminOut.UserID, "expireduser@example.com")
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}

	// Token should be expired
	acceptIn := AcceptInviteInput{RawToken: inviteOut.RawToken, Password: "newuserpass123"}
	_, err = svc.AcceptInvite(ctx, acceptIn)
	if err != ErrInviteExpired {
		t.Errorf("AcceptInvite expired: expected ErrInviteExpired, got %v", err)
	}
}

// TestService_AcceptInvite_NotFound tests that non-existent tokens are rejected.
func TestService_AcceptInvite_NotFound(t *testing.T) {
	store := setupIntegrationStorage(t)
	writer := setupIntegrationEventWriter(t, store)
	issuer := integrationTokenIssuer()
	svc := NewService(store, writer, issuer)

	ctx := context.Background()

	// Test: Non-existent token returns ErrInviteNotFound
	randomToken := base64.RawURLEncoding.EncodeToString(mustRandBytes(32))
	acceptIn := AcceptInviteInput{RawToken: randomToken, Password: "newuserpass123"}
	_, err := svc.AcceptInvite(ctx, acceptIn)
	if err != ErrInviteNotFound {
		t.Errorf("AcceptInvite not found: expected ErrInviteNotFound, got %v", err)
	}
}
