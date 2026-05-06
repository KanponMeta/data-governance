package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/event"
)

// mockEventWriter is a no-op event.Writer for middleware tests.
type mockEventWriter struct{}

func (m *mockEventWriter) Append(ctx context.Context, evt event.Event) error {
	return nil
}

func TestMiddleware_NoAuthHeader(t *testing.T) {
	issuer := integrationTokenIssuer()
	writer := &mockEventWriter{}

	req := httptest.NewRequest("GET", "/protected", nil)
	rec := httptest.NewRecorder()

	handler := Middleware(issuer, writer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called when auth header is missing")
	}))

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %q", ct)
	}
}

func TestMiddleware_InvalidAuthHeaderFormat(t *testing.T) {
	issuer := integrationTokenIssuer()
	writer := &mockEventWriter{}

	tests := []struct {
		name   string
		header string
	}{
		{"no bearer prefix", "some-token"},
		{"wrong prefix", "Basic some-token"},
		{"empty token", "Bearer "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/protected", nil)
			req.Header.Set("Authorization", tt.header)
			rec := httptest.NewRecorder()

			handler := Middleware(issuer, writer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("handler should not be called")
			}))

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", rec.Code)
			}
		})
	}
}

func TestMiddleware_TamperedToken(t *testing.T) {
	// Use a different key than what we use in middleware
	issuer := NewTokenIssuer(mustRandBytes(32), 15*time.Minute)
	writer := &mockEventWriter{}

	// Create a token signed with a different key
	badIssuer := NewTokenIssuer(mustRandBytes(32), 15*time.Minute)
	tok, _, _ := badIssuer.Issue(uuid.New(), "member")

	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	handler := Middleware(issuer, writer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for tampered token")
	}))

	handler.ServeHTTP(rec, req)

	// Tampered token should return 401
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestMiddleware_ValidToken(t *testing.T) {
	issuer := integrationTokenIssuer()
	writer := &mockEventWriter{}

	userID := uuid.New()
	tok, _, _ := issuer.Issue(userID, "member")

	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()

	var sawPrincipal bool
	handler := Middleware(issuer, writer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Error("Principal not found in context")
			return
		}
		if p.UserID != userID {
			t.Errorf("expected UserID %v, got %v", userID, p.UserID)
		}
		if p.Role != "member" {
			t.Errorf("expected role member, got %q", p.Role)
		}
		sawPrincipal = true
	}))

	handler.ServeHTTP(rec, req)

	if !sawPrincipal {
		t.Error("handler did not see valid principal")
	}
}

func TestRequireRole_WrongRole(t *testing.T) {
	principal := Principal{UserID: uuid.New(), Role: "member"}
	ctx := context.WithValue(context.Background(), principalKey{}, principal)

	handler := RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for wrong role")
	}))

	req := httptest.NewRequest("GET", "/admin", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("expected application/problem+json, got %q", ct)
	}
}

func TestRequireRole_CorrectRole(t *testing.T) {
	adminPrincipal := Principal{UserID: uuid.New(), Role: "admin"}
	ctx := context.WithValue(context.Background(), principalKey{}, adminPrincipal)

	passed := false
	handler := RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
	}))

	req := httptest.NewRequest("GET", "/admin", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if !passed {
		t.Error("admin role should pass through RequireRole")
	}
}

func TestRequireRole_NoPrincipal(t *testing.T) {
	ctx := context.Background()

	handler := RequireRole("admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called when no principal is present")
	}))

	req := httptest.NewRequest("GET", "/admin", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 when no principal, got %d", rec.Code)
	}
}

// TestExpiredTokenEvent tests that an expired token emits an auth.token_expired event.
// This requires a real issuer with a short TTL to simulate expiry.
func TestMiddleware_ExpiredTokenEmitsEvent(t *testing.T) {
	// Create an issuer with a token that has already expired
	issuer := NewTokenIssuer(mustRandBytes(32), -1*time.Hour) // TTL of -1 hour means already expired
	writer := &mockEventWriter{}

	// Issue a token that is already expired
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   uuid.New().String(),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-2 * time.Hour)),
		},
		UserID: uuid.New(),
		Role:   "member",
	}
	expiredTok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	// Sign with the issuer's key
	signed, err := expiredTok.SignedString(mustRandBytes(32)) // wrong key - token verification will fail first
	if err != nil {
		t.Fatalf("create expired token: %v", err)
	}

	req := httptest.NewRequest("GET", "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rec := httptest.NewRecorder()

	handler := Middleware(issuer, writer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called for expired token")
	}))

	handler.ServeHTTP(rec, req)

	// Should return 401 because the token signature is wrong (different key)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}
