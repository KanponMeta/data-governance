package api

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/platform"
)

// init self-registers roles routes via the platform registry (B-03 fix).
func init() {
	platform.RegisterRoutes("roles", MountRoles)
}

func MountRoles(r chi.Router, deps platform.MountDeps) {
	r.Route("/roles", func(r chi.Router) {
		r.Use(deps.AuthMW)
		r.With(auth.RequirePermission(deps.Enforcer, "/users/admin", "manage")).Post("/", createRoleHandler(deps))
		r.With(auth.RequirePermission(deps.Enforcer, "/users/admin", "manage")).Get("/", listRolesHandler(deps))
		r.With(auth.RequirePermission(deps.Enforcer, "/users/admin", "manage")).Delete("/{name}", deleteRoleHandler(deps))
	})
	r.Route("/users/{userID}/roles", func(r chi.Router) {
		r.Use(deps.AuthMW)
		r.With(auth.RequirePermission(deps.Enforcer, "/users/admin", "manage")).Put("/", assignRolesHandler(deps))
		r.With(auth.RequirePermission(deps.Enforcer, "/users/admin", "manage")).Delete("/{role}", revokeRoleHandler(deps))
	})
}

type createRoleRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func createRoleHandler(deps platform.MountDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if req.Name == "" {
			http.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		actor, _ := auth.PrincipalFromContext(r.Context())
		if err := deps.AuthService.CreateRole(r.Context(), actor.UserID, req.Name, req.Description); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"name": req.Name})
	}
}

func listRolesHandler(deps platform.MountDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := deps.DB.QueryContext(r.Context(), `SELECT name, description FROM roles ORDER BY name`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		var roles []map[string]string
		for rows.Next() {
			var name, description string
			if err := rows.Scan(&name, &description); err != nil {
				continue
			}
			roles = append(roles, map[string]string{"name": name, "description": description})
		}
		json.NewEncoder(w).Encode(map[string]any{"roles": roles})
	}
}

func deleteRoleHandler(deps platform.MountDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := chi.URLParam(r, "name")
		actor, _ := auth.PrincipalFromContext(r.Context())
		if err := deps.AuthService.DeleteRole(r.Context(), actor.UserID, name); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type assignRolesRequest struct {
	Role string `json:"role"`
}

func assignRolesHandler(deps platform.MountDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userIDStr := chi.URLParam(r, "userID")
		var req assignRolesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			http.Error(w, "invalid user_id", http.StatusBadRequest)
			return
		}
		actor, _ := auth.PrincipalFromContext(r.Context())
		if err := deps.AuthService.AssignRole(r.Context(), actor.UserID, userID, req.Role); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"user_id": userIDStr, "role": req.Role})
	}
}

func revokeRoleHandler(deps platform.MountDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userIDStr := chi.URLParam(r, "userID")
		role := chi.URLParam(r, "role")
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			http.Error(w, "invalid user_id", http.StatusBadRequest)
			return
		}
		actor, _ := auth.PrincipalFromContext(r.Context())
		if err := deps.AuthService.RevokeRole(r.Context(), actor.UserID, userID, role); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
