package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// ErrInvalidToken is returned for any verification failure other than expiry:
// bad signature, wrong algorithm, malformed token, malformed claims.
var ErrInvalidToken = errors.New("auth: invalid token")

// ErrTokenExpired is returned by Verify when the only failure is expiry. The
// returned Claims is still populated so middleware can record actor_id for an
// auth.token_expired event.
var ErrTokenExpired = errors.New("auth: token expired")

const (
	jwtIssuer    = "data-governance-platform"
	jwtAlgorithm = "HS256"
)

// Claims extends jwt.RegisteredClaims with platform-specific fields.
// UserID is populated from RegisteredClaims.Subject after parse; the json:"-"
// tag keeps it from being serialized into the token (Subject is the source of truth).
type Claims struct {
	jwt.RegisteredClaims
	UserID uuid.UUID `json:"-"`
	Role   string    `json:"role"`
}

// TokenIssuer signs and verifies access tokens. Refresh-token rotation is
// deferred to a later phase (D-CLAUDE-DISCRETION in CONTEXT.md).
type TokenIssuer struct {
	signingKey []byte
	accessTTL  time.Duration
	now        func() time.Time
}

// NewTokenIssuer constructs a TokenIssuer. signingKey MUST be >= 32 bytes
// (config.Load enforces this); accessTTL MAY be negative for testing expired tokens.
func NewTokenIssuer(signingKey []byte, accessTTL time.Duration) *TokenIssuer {
	return &TokenIssuer{
		signingKey: signingKey,
		accessTTL:  accessTTL,
		now:        time.Now,
	}
}

// Issue mints a signed JWT for userID with the given role. Returns the token
// string and the absolute expiry time (UTC).
func (t *TokenIssuer) Issue(userID uuid.UUID, role string) (string, time.Time, error) {
	now := t.now().UTC()
	expiresAt := now.Add(t.accessTTL)
	claims := &Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   userID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			ID:        uuid.NewString(),
		},
		UserID: userID,
		Role:   role,
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(t.signingKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: sign failed: %v", ErrInvalidToken, err)
	}
	return signed, expiresAt, nil
}

// Verify parses raw, validates signature with HS256-only allowlist, and
// returns the typed Claims. On expiry returns (claims, ErrTokenExpired);
// on any other failure returns (nil, ErrInvalidToken-wrapped).
func (t *TokenIssuer) Verify(raw string) (*Claims, error) {
	claims := &Claims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwtAlgorithm}),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithIssuedAt(),
	)
	tok, err := parser.ParseWithClaims(raw, claims, func(_ *jwt.Token) (interface{}, error) {
		return t.signingKey, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			// Populate UserID from Subject so caller can audit who's expired.
			if id, parseErr := uuid.Parse(claims.Subject); parseErr == nil {
				claims.UserID = id
			}
			return claims, ErrTokenExpired
		}
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	if !tok.Valid {
		return nil, fmt.Errorf("%w: token marked invalid by parser", ErrInvalidToken)
	}
	id, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, fmt.Errorf("%w: subject is not a uuid: %v", ErrInvalidToken, err)
	}
	claims.UserID = id
	return claims, nil
}