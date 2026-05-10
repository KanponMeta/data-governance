package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/event"
)

// principalKey is a private context key type to avoid collisions.
type principalKey struct{}

// Principal describes the authenticated caller extracted from a JWT.
//
// Role is the primary role (back-compat); Roles is the full active set used
// by RequirePermission for RBAC enforcement. WR-01: a user assigned both
// data-engineer and governance must have BOTH roles considered when checking
// (role, obj, act); previous code only checked Role and silently denied.
type Principal struct {
	UserID uuid.UUID
	Role   string
	Roles  []string
}

// PrincipalFromContext retrieves the Principal from ctx, if present.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// ContextWithPrincipal returns ctx with the given Principal injected.
// Intended for use in tests that need to simulate an authenticated caller
// without going through the full JWT middleware.
func ContextWithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// TestPrincipalKey returns the context key used by ContextWithPrincipal.
// Only intended for test helpers that need to set the key directly.
func TestPrincipalKey() any {
	return principalKey{}
}

// Middleware returns a chi-compatible HTTP middleware that validates Bearer
// tokens using the supplied TokenIssuer. On success it injects a Principal
// into the request context. On failure it writes a RFC 7807 problem+json
// response and (for expired tokens) appends an auth.token_expired event.
//
// The middleware intentionally does NOT import internal/api to avoid a import
// cycle. The inline problem+json response is ~6 lines and matches what
// internal/api/problem.go produces.
func Middleware(issuer *TokenIssuer, events event.Writer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			if auth == "" {
				writeUnauthorized(w, "missing authorization header")
				return
			}

			// Authorization: Bearer <token>
			idx := strings.Index(auth, " ")
			if idx < 0 || !strings.EqualFold(auth[:idx], "bearer") {
				writeUnauthorized(w, "invalid authorization header format")
				return
			}
			token := strings.TrimSpace(auth[idx+1:])
			if token == "" {
				writeUnauthorized(w, "missing bearer token")
				return
			}

			claims, err := issuer.Verify(token)
			if err != nil {
				if err == ErrTokenExpired {
					// Record the expired token event with the actor's identity.
					if claims != nil && claims.UserID != (uuid.UUID{}) {
						events.Append(r.Context(), event.Event{
							Type:         event.EventTypeAuthTokenExpired,
							OccurredAt:   time.Now().UTC(),
							ResourceType: "user",
							ResourceID:   claims.UserID.String(),
							ActorID:      &claims.UserID,
							Payload:      event.AuthTokenExpiredPayload{UserID: claims.UserID.String()},
						})
					}
					writeUnauthorized(w, "token has expired")
					return
				}
				// Other errors (tampered, wrong algo, malformed) — do NOT emit event.
				writeUnauthorized(w, "invalid token")
				return
			}

			ctx := context.WithValue(r.Context(), principalKey{}, Principal{
				UserID: claims.UserID,
				Role:   claims.Role,
				Roles:  claims.Roles,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole returns a middleware that checks the authenticated Principal's
// role. It passes the request through if the role matches, otherwise responds
// with 403 Forbidden using RFC 7807 problem+json.
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFromContext(r.Context())
			if !ok {
				// Should not happen if auth.Middleware is applied first.
				writeUnauthorized(w, "authentication required")
				return
			}
			if p.Role != role {
				writeForbidden(w, "insufficient permissions")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequirePermission returns a chi-compatible middleware that enforces
// Casbin RBAC policy: (role, obj, act) must be permitted.
// It extracts ALL active roles from the authenticated Principal's JWT
// (Principal.Role primary + Principal.Roles set) and checks each against
// the Casbin enforcer. Passes if ANY role matches; 403 otherwise.
//
// WR-01: previous code only checked Role and ignored Roles, so a user
// assigned multiple roles was silently denied access permitted by their
// non-primary roles.
func RequirePermission(enforcer *casbin.Enforcer, obj, act string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFromContext(r.Context())
			if !ok {
				writeUnauthorized(w, "authentication required")
				return
			}
			// Build the de-duplicated role set: primary Role plus the full
			// Roles list. Empty role values are skipped to avoid spurious
			// "role:" enforcement against the empty role identifier.
			seen := make(map[string]struct{}, 1+len(p.Roles))
			roles := make([]string, 0, 1+len(p.Roles))
			if p.Role != "" {
				seen[p.Role] = struct{}{}
				roles = append(roles, p.Role)
			}
			for _, extraRole := range p.Roles {
				if extraRole == "" {
					continue
				}
				if _, dup := seen[extraRole]; dup {
					continue
				}
				seen[extraRole] = struct{}{}
				roles = append(roles, extraRole)
			}
			for _, role := range roles {
				allowed, err := enforcer.Enforce("role:"+role, obj, act)
				if err != nil {
					writeInternalError(w, "policy check failed")
					return
				}
				if allowed {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeForbidden(w, "insufficient permissions for "+obj+" "+act)
		})
	}
}

func writeInternalError(w http.ResponseWriter, detail string) {
	problem := map[string]any{
		"type":   "about:blank",
		"title":  "Internal Server Error",
		"status": http.StatusInternalServerError,
		"detail": detail,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusInternalServerError)
	if enc := json.NewEncoder(w); enc != nil {
		_ = enc.Encode(problem)
	}
}

// writeUnauthorized writes a RFC 7807 problem+json 401 response.
// This is duplicated from internal/api/problem.go to avoid an import cycle.
func writeUnauthorized(w http.ResponseWriter, detail string) {
	problem := map[string]any{
		"type":   "about:blank",
		"title":  "Unauthorized",
		"status": http.StatusUnauthorized,
		"detail": detail,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnauthorized)
	if enc := json.NewEncoder(w); enc != nil {
		_ = enc.Encode(problem)
	}
}

// writeForbidden writes a RFC 7807 problem+json 403 response.
func writeForbidden(w http.ResponseWriter, detail string) {
	problem := map[string]any{
		"type":   "about:blank",
		"title":  "Forbidden",
		"status": http.StatusForbidden,
		"detail": detail,
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusForbidden)
	if enc := json.NewEncoder(w); enc != nil {
		_ = enc.Encode(problem)
	}
}
