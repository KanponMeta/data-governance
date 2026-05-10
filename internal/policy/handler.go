package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/casbin/casbin/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/connector"
)

// MountPolicy registers the column-policy REST endpoints on r.
//
// PATCH /assets/{asset}/columns/{column}/policy — runtime override; reason
//   required; protected by RequirePermission("/policies/edit","write").
// DELETE /assets/{asset}/columns/{column}/policy — soft-retire runtime row;
//   protected by RequirePermission("/policies/edit","write").
// GET   /policies/effective/{asset}/{column}    — resolve precedence;
//   readable by any authenticated principal.
// POST  /policies/yaml-reload                    — reload policies.yaml;
//   protected by RequirePermission("/policies/yaml","write").
//
// authMW is the JWT auth middleware (auth.Middleware) — applied to every
// route in the group so handlers always have a Principal.
func MountPolicy(r chi.Router, store *Store, enforcer *casbin.Enforcer, authMW func(http.Handler) http.Handler, yamlLoader func() (*YAMLConfig, error)) {
	r.Route("/assets/{asset}/columns/{column}/policy", func(r chi.Router) {
		if authMW != nil {
			r.Use(authMW)
		}
		r.With(auth.RequirePermission(enforcer, "/policies/edit", "write")).
			Patch("/", patchPolicyHandler(store))
		r.With(auth.RequirePermission(enforcer, "/policies/edit", "write")).
			Delete("/", deletePolicyHandler(store))
	})
	r.Route("/policies", func(r chi.Router) {
		if authMW != nil {
			r.Use(authMW)
		}
		r.Get("/effective/{asset}/{column}", effectiveHandler(store))
		r.With(auth.RequirePermission(enforcer, "/policies/yaml", "write")).
			Post("/yaml-reload", yamlReloadHandler(store, yamlLoader))
	})
}

type patchPolicyBody struct {
	Mask       string   `json:"mask"`
	AllowRoles []string `json:"allow_roles"`
	Reason     string   `json:"reason"`
}

// patchPolicyHandler implements PATCH /assets/{asset}/columns/{column}/policy.
// Reason is REQUIRED — empty reason returns 400 with a problem+json body.
func patchPolicyHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assetName := chi.URLParam(r, "asset")
		column := chi.URLParam(r, "column")
		var body patchPolicyBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "invalid JSON body: "+err.Error())
			return
		}
		if body.Reason == "" {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "reason is required")
			return
		}
		mask := connector.MaskType(body.Mask)
		if !mask.IsValid() {
			writeProblem(w, http.StatusBadRequest, "Bad Request",
				fmt.Sprintf("invalid mask %q (must be hash, redact, or partial)", body.Mask))
			return
		}

		actorID := principalUUID(r)
		eff, err := store.Patch(r.Context(), actorID, assetName, column, mask, body.AllowRoles, body.Reason)
		if err != nil {
			if errors.Is(err, ErrReasonRequired) {
				writeProblem(w, http.StatusBadRequest, "Bad Request", "reason is required")
				return
			}
			slog.Error("policy patch failed", "actor", actorID, "asset", assetName, "column", column, "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		writeJSON(w, http.StatusOK, effectiveDTO(eff))
	}
}

// deletePolicyHandler implements DELETE /assets/{asset}/columns/{column}/policy.
// Soft-retires the active runtime row; returns 404 if no runtime row existed.
func deletePolicyHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assetName := chi.URLParam(r, "asset")
		column := chi.URLParam(r, "column")
		actorID := principalUUID(r)
		var body struct {
			Reason string `json:"reason"`
		}
		// Reason is optional for DELETE — body may be absent.
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := store.Delete(r.Context(), actorID, assetName, column, body.Reason); err != nil {
			if errors.Is(err, ErrPolicyNotFound) {
				writeProblem(w, http.StatusNotFound, "Not Found", "no runtime policy for that column")
				return
			}
			slog.Error("policy delete failed", "actor", actorID, "asset", assetName, "column", column, "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// effectiveHandler implements GET /policies/effective/{asset}/{column}.
// Returns 404 if no policy at any layer.
func effectiveHandler(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assetName := chi.URLParam(r, "asset")
		column := chi.URLParam(r, "column")
		eff, err := store.Resolve(r.Context(), assetName, column)
		if err != nil {
			if errors.Is(err, ErrPolicyNotFound) {
				writeProblem(w, http.StatusNotFound, "Not Found", "no policy for that column")
				return
			}
			slog.Error("policy resolve failed", "asset", assetName, "column", column, "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		writeJSON(w, http.StatusOK, effectiveDTO(eff))
	}
}

// yamlReloadHandler implements POST /policies/yaml-reload.
// Reloads policies.yaml from disk and re-applies tag→mask defaults.
func yamlReloadHandler(store *Store, loader func() (*YAMLConfig, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if loader == nil {
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "yaml loader not configured")
			return
		}
		cfg, err := loader()
		if err != nil {
			slog.Error("policy yaml-reload load failed", "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		actor := principalUUID(r)
		applied, err := store.ApplyYAML(r.Context(), cfg, actor)
		if err != nil {
			slog.Error("policy yaml-reload apply failed", "actor", actor, "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"applied": applied})
	}
}

// EffectiveDTO is the serialised JSON shape of an Effective policy returned
// by the REST handlers. Stable for external clients (governance UI / CLI).
type EffectiveDTO struct {
	Asset           string   `json:"asset"`
	Column          string   `json:"column"`
	Mask            string   `json:"mask"`
	AllowRoles      []string `json:"allow_roles"`
	Source          string   `json:"source"`
	EnforcementMode string   `json:"enforcement_mode"`
	Reason          string   `json:"reason,omitempty"`
}

func effectiveDTO(e Effective) EffectiveDTO {
	roles := e.AllowRoles
	if roles == nil {
		roles = []string{}
	}
	return EffectiveDTO{
		Asset:           e.Asset,
		Column:          e.Column,
		Mask:            string(e.Mask),
		AllowRoles:      roles,
		Source:          e.Source,
		EnforcementMode: e.EnforcementMode,
		Reason:          e.Reason,
	}
}

func principalUUID(r *http.Request) uuid.UUID {
	if p, ok := auth.PrincipalFromContext(r.Context()); ok {
		return p.UserID
	}
	return uuid.Nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":   "about:blank",
		"title":  title,
		"status": status,
		"detail": detail,
	})
}
