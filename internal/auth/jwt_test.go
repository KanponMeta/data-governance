package auth

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestTokenIssuer_Issue(t *testing.T) {
	issuer := NewTokenIssuer([]byte("12345678901234567890123456789012"), time.Minute)

	t.Run("Issue returns non-empty token and correct expiry", func(t *testing.T) {
		userID := uuid.New()
		token, expiresAt, err := issuer.Issue(userID, "admin")
		if err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		if token == "" {
			t.Fatal("Issue() returned empty token")
		}
		// expiresAt should be approximately now + accessTTL
		expectedExpiry := time.Now().Add(time.Minute)
		if expiresAt.Before(expectedExpiry.Add(-time.Second)) || expiresAt.After(expectedExpiry.Add(time.Second)) {
			t.Errorf("expiresAt = %v, want approximately %v", expiresAt, expectedExpiry)
		}
	})

	t.Run("Issue sets all required RegisteredClaims", func(t *testing.T) {
		userID := uuid.New()
		token, _, err := issuer.Issue(userID, "admin")
		if err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		claims, err := issuer.Verify(token)
		if err != nil {
			t.Fatalf("Verify() error = %v", err)
		}
		if claims.Issuer != jwtIssuer {
			t.Errorf("Issuer = %q, want %q", claims.Issuer, jwtIssuer)
		}
		if claims.Subject != userID.String() {
			t.Errorf("Subject = %q, want %q", claims.Subject, userID.String())
		}
		if claims.Role != "admin" {
			t.Errorf("Role = %q, want %q", claims.Role, "admin")
		}
		if claims.ID == "" {
			t.Error("ID (jti) is empty, expected non-empty UUID")
		}
		if claims.IssuedAt == nil {
			t.Error("IssuedAt is nil")
		}
		if claims.NotBefore == nil {
			t.Error("NotBefore is nil")
		}
		if claims.ExpiresAt == nil {
			t.Error("ExpiresAt is nil")
		}
	})
}

func TestTokenIssuer_Verify(t *testing.T) {
	issuer := NewTokenIssuer([]byte("12345678901234567890123456789012"), time.Minute)

	t.Run("Verify returns correct Claims for valid token", func(t *testing.T) {
		userID := uuid.New()
		token, _, err := issuer.Issue(userID, "admin")
		if err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		claims, err := issuer.Verify(token)
		if err != nil {
			t.Errorf("Verify() error = %v", err)
		}
		if claims.UserID != userID {
			t.Errorf("UserID = %v, want %v", claims.UserID, userID)
		}
		if claims.Role != "admin" {
			t.Errorf("Role = %q, want %q", claims.Role, "admin")
		}
	})

	t.Run("Verify rejects token signed with different key", func(t *testing.T) {
		userID := uuid.New()
		token, _, err := issuer.Issue(userID, "admin")
		if err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		// Mutate one byte of the token
		mutated := token[:len(token)-5] + "XXXXX"
		_, err = issuer.Verify(mutated)
		if err == nil {
			t.Fatal("Verify() expected error for mutated token, got nil")
		}
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("Verify rejects expired token but still returns claims with UserID", func(t *testing.T) {
		// Create issuer with -1s TTL so token is already expired
		expiredIssuer := NewTokenIssuer([]byte("12345678901234567890123456789012"), -time.Second)
		userID := uuid.New()
		token, _, err := expiredIssuer.Issue(userID, "admin")
		if err != nil {
			t.Fatalf("Issue() error = %v", err)
		}
		claims, err := issuer.Verify(token)
		if !errors.Is(err, ErrTokenExpired) {
			t.Errorf("Verify() error = %v, want ErrTokenExpired", err)
		}
		if claims == nil {
			t.Fatal("Verify() returned nil claims on expired token")
		}
		if claims.UserID != userID {
			t.Errorf("claims.UserID = %v, want %v (should be populated for audit)", claims.UserID, userID)
		}
	})

	t.Run("Verify rejects alg:none token (algorithm confusion attack)", func(t *testing.T) {
		forged := jwt.NewWithClaims(jwt.SigningMethodNone, &Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    jwtIssuer,
				Subject:   uuid.NewString(),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			},
			Role: "admin",
		})
		raw, err := forged.SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("Failed to create alg:none token: %v", err)
		}
		_, err = issuer.Verify(raw)
		if err == nil {
			t.Fatal("Verify() expected error for alg:none token, got nil")
		}
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("Verify rejects non-HS256 algorithm (HS512)", func(t *testing.T) {
		// Create a token with HS512
		claims := &Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    jwtIssuer,
				Subject:   uuid.NewString(),
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			},
			Role: "admin",
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodHS512, claims)
		signed, err := tok.SignedString([]byte("12345678901234567890123456789012"))
		if err != nil {
			t.Fatalf("Failed to create HS512 token: %v", err)
		}
		_, err = issuer.Verify(signed)
		if err == nil {
			t.Fatal("Verify() expected error for HS512 token, got nil")
		}
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})

	t.Run("Verify rejects malformed subject (not a UUID)", func(t *testing.T) {
		claims := &Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Issuer:    jwtIssuer,
				Subject:   "not-a-uuid",
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
			},
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := tok.SignedString([]byte("12345678901234567890123456789012"))
		if err != nil {
			t.Fatalf("Failed to create token: %v", err)
		}
		_, err = issuer.Verify(signed)
		if err == nil {
			t.Fatal("Verify() expected error for non-UUID subject, got nil")
		}
		if !errors.Is(err, ErrInvalidToken) {
			t.Errorf("Verify() error = %v, want ErrInvalidToken", err)
		}
	})
}