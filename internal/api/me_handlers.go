package api

import (
	"net/http"

	"github.com/kanpon/data-governance/internal/auth"
)

// meHandler returns the authenticated user info for UI.
// GET /v1/me
func meHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := auth.PrincipalFromContext(r.Context())
		if !ok {
			Unauthorized(w, "authentication required")
			return
		}

		info, err := deps.Auth.GetSessionInfo(r.Context(), p.UserID)
		if err != nil {
			InternalServerError(w, err.Error())
			return
		}

		writeJSONResponse(w, http.StatusOK, info)
	}
}