package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is returned by VerifyPassword for any failure mode,
// including malformed hash, mismatched password, or zero-length input. Callers
// MUST NOT distinguish between these cases when responding to the client
// (T-03-04: prevents user enumeration / hash-format leakage).
var ErrInvalidCredentials = errors.New("auth: invalid credentials")

// bcryptCost balances security and CPU. Cost 12 is ~250ms on a 2026 laptop —
// fast enough for an interactive login, slow enough to defeat offline cracking.
const bcryptCost = 12

const (
	minPasswordBytes = 8
	maxPasswordBytes = 72 // bcrypt silently truncates above 72; reject explicitly
)

// HashPassword returns a bcrypt hash of plain. Length is enforced to 8..72
// bytes inclusive; anything else returns a length-error (caller maps to 400).
func HashPassword(plain string) (string, error) {
	if len(plain) < minPasswordBytes {
		return "", fmt.Errorf("auth: password must be at least %d bytes", minPasswordBytes)
	}
	if len(plain) > maxPasswordBytes {
		return "", fmt.Errorf("auth: password must be at most %d bytes", maxPasswordBytes)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("auth: hash failed: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword returns nil if plain matches hash, ErrInvalidCredentials
// otherwise. Never wraps the underlying bcrypt error — that would leak whether
// the hash was malformed vs. the password was wrong.
func VerifyPassword(hash, plain string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}