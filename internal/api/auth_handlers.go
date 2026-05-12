package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kanpon/data-governance/internal/auth"
)

// authHandler handles HTTP requests for authentication operations.
type authHandler struct {
	svc *auth.Service
}

// RegisterRequest is the POST /v1/auth/register request body.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// RegisterResponse is the POST /v1/auth/register response body.
type RegisterResponse struct {
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

// LoginRequest is the POST /v1/auth/login request body.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is the POST /v1/auth/login response body.
type LoginResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresAt   string `json:"expires_at"`
	UserID      string `json:"user_id"`
	Role        string `json:"role"`
}

// InviteRequest is the POST /v1/auth/invites request body.
type InviteRequest struct {
	Email string `json:"email"`
}

// InviteResponse is the POST /v1/auth/invites response body.
type InviteResponse struct {
	InviteID string `json:"invite_id"`
	Token    string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// AcceptInviteRequest is the POST /v1/auth/accept-invite request body.
type AcceptInviteRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// AcceptInviteResponse is the POST /v1/auth/accept-invite response body.
type AcceptInviteResponse struct {
	UserID string `json:"user_id"`
}

// registerHandler handles POST /v1/auth/register.
// @Summary Register new user
// @Description Create a new user account. The first user becomes admin, subsequent users become member.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body RegisterRequest true "registration credentials"
// @Success 201 {object} RegisterResponse
// @Failure 400 {object} APIError
// @Failure 409 {object} APIError
// @Router /v1/auth/register [post]
func (h *authHandler) register(w http.ResponseWriter, r *http.Request) {
	var body RegisterRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	out, err := h.svc.Register(r.Context(), auth.RegisterInput{
		Email:    body.Email,
		Password: body.Password,
	})
	if err != nil {
		switch err {
		case auth.ErrEmailAlreadyUsed:
			Conflict(w, "email is already registered")
		default:
			// HashPassword validation errors come back as different errors.
			// Map them all to 400 Bad Request.
			BadRequest(w, err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(RegisterResponse{
		UserID: out.UserID.String(),
		Role:   out.Role,
	})
}

// loginHandler handles POST /v1/auth/login.
// @Summary User login
// @Description Authenticate user with email and password. Returns JWT token, sets session cookie, and returns CSRF token header.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body LoginRequest true "login credentials"
// @Success 200 {object} LoginResponse
// @Failure 400 {object} APIError
// @Failure 401 {object} APIError
// @Router /v1/auth/login [post]
func (h *authHandler) login(w http.ResponseWriter, r *http.Request) {
	var body LoginRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	out, err := h.svc.Login(r.Context(), body.Email, body.Password, r.UserAgent(), r.RemoteAddr)
	if err != nil {
		// T-05-01: Identical response for missing email and wrong password.
		// This prevents user enumeration attacks.
		Unauthorized(w, "Invalid credentials")
		return
	}

	// D-25: Set httpOnly Secure session cookie for UI.
	// Cookie name: dg_session, SameSite=Strict, Path=/, Max-Age matching JWT TTL.
	cookie := http.Cookie{
		Name:     "dg_session",
		Value:    out.Token,
		Path:     "/",
		MaxAge:   int(time.Since(out.ExpiresAt).Seconds()),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
	http.SetCookie(w, &cookie)

	// D-23: Return CSRF token in response header for state-changing requests.
	csrfToken := out.Token // CSRF token derived from JWT for simplicity
	w.Header().Set("X-CSRF-Token", csrfToken)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(LoginResponse{
		AccessToken: out.Token,
		TokenType:   "Bearer",
		ExpiresAt:   out.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
		UserID:      out.UserID.String(),
		Role:        out.Role,
	})
}

// inviteHandler handles POST /v1/auth/invites (admin only).
// @Summary Generate invite token
// @Description Create a single-use invite token for a new user. Requires admin role.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body InviteRequest true "invite email"
// @Success 201 {object} InviteResponse
// @Failure 400 {object} APIError
// @Failure 401 {object} APIError
// @Failure 403 {object} APIError
// @Failure 500 {object} APIError
// @Router /v1/auth/invites [post]
func (h *authHandler) invite(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.PrincipalFromContext(r.Context())
	if !ok {
		Unauthorized(w, "authentication required")
		return
	}

	var body InviteRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	out, err := h.svc.Invite(r.Context(), p.UserID, body.Email)
	if err != nil {
		InternalServerError(w, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(InviteResponse{
		InviteID:  out.InviteID.String(),
		Token:     out.RawToken,
		ExpiresAt: out.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"),
	})
}

// acceptInviteHandler handles POST /v1/auth/accept-invite.
// @Summary Accept invite
// @Description Consume an invite token to create a new member account.
// @Tags auth
// @Accept json
// @Produce json
// @Param request body AcceptInviteRequest true "invite token and password"
// @Success 201 {object} AcceptInviteResponse
// @Failure 400 {object} APIError
// @Failure 404 {object} APIError
// @Failure 410 {object} APIError
// @Failure 409 {object} APIError
// @Router /v1/auth/accept-invite [post]
func (h *authHandler) acceptInvite(w http.ResponseWriter, r *http.Request) {
	var body AcceptInviteRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	out, err := h.svc.AcceptInvite(r.Context(), auth.AcceptInviteInput{
		RawToken: body.Token,
		Password: body.Password,
	})
	if err != nil {
		switch err {
		case auth.ErrInviteNotFound:
			NotFound(w, "invite not found")
		case auth.ErrInviteExpired:
			Gone(w, "invite has expired")
		case auth.ErrInviteAlreadyUsed:
			Conflict(w, "invite has already been used")
		default:
			BadRequest(w, err.Error())
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(AcceptInviteResponse{
		UserID: out.UserID.String(),
	})
}

// decodeJSON decodes the request body as JSON with DisallowUnknownFields.
// Returns false if decoding fails, in which case a 400 problem+json has
// already been written.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		BadRequest(w, "Invalid request body")
		return false
	}
	return true
}
