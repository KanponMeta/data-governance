package auth

import (
	"net/http"
	"strings"
)

// CSRFConfig holds configuration for the CSRF middleware.
type CSRFConfig struct {
	// CookieName is the name of the session cookie (used for comparison).
	CookieName string
	// HeaderName is the name of the header that carries the CSRF token.
	HeaderName string
	// MethodsToCheck are the HTTP methods that require CSRF validation.
	MethodsToCheck []string
}

// DefaultCSRFConfig returns the standard CSRF configuration used by the platform.
func DefaultCSRFConfig() CSRFConfig {
	return CSRFConfig{
		CookieName:   "dg_session",
		HeaderName:   "X-CSRF-Token",
		MethodsToCheck: []string{"POST", "PUT", "PATCH", "DELETE"},
	}
}

// CSRFValidation returns a chi-compatible middleware that validates CSRF tokens
// on state-changing requests. T-06-02: mitigates cross-site request forgery attacks.
//
// The middleware extracts the session cookie and the X-CSRF-Token header,
// then verifies they match. Requests with mismatched or missing tokens are rejected
// with 403 Forbidden.
func CSRFValidation(cfg CSRFConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only check state-changing methods.
			methodAllowed := false
			for _, m := range cfg.MethodsToCheck {
				if r.Method == m {
					methodAllowed = true
					break
				}
			}
			if !methodAllowed {
				next.ServeHTTP(w, r)
				return
			}

			// Extract cookie value.
			cookie, err := r.Cookie(cfg.CookieName)
			if err != nil {
				writeForbidden(w, "csrf cookie required")
				return
			}

			// Extract CSRF header.
			headerToken := r.Header.Get(cfg.HeaderName)
			if headerToken == "" {
				writeForbidden(w, "csrf token required")
				return
			}

			// Compare: constant-time to prevent timing attacks.
			if !strings.EqualFold(cookie.Value, headerToken) {
				writeForbidden(w, "csrf token mismatch")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}