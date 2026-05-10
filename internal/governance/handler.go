package governance

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/casbin/casbin/v2"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/auth"
)

// AssetLookup resolves a registered asset by name. Tests inject a fake;
// production wiring uses asset.Default().Get.
type AssetLookup interface {
	Get(name string) (*asset.Asset, error)
}

// MetadataLookupFn returns the (tags, owner) pair the resolver / checker
// need at submit time. Pass nil to degrade to empty tags and empty owner.
type MetadataLookupFn func(ctx context.Context, assetName string) ([]string, string, error)

// HandlerDeps is the explicit dependency surface for MountGovernance.
type HandlerDeps struct {
	Workflow       *Workflow
	Enforcer       *casbin.Enforcer
	AuthMW         func(http.Handler) http.Handler
	AssetLookup    AssetLookup
	MetadataLookup MetadataLookupFn
}

// MountGovernance wires the governance REST endpoints.
//
// POST /governance/submit                 — engineer role
// POST /governance/reviews/{id}/approve   — governance role
// POST /governance/reviews/{id}/reject    — governance role (comment required)
// POST /governance/reviews/{id}/reassign  — admin (manage)
// GET  /governance/status                 — any authenticated principal
// GET  /governance/status/{asset}         — any authenticated principal
func MountGovernance(r chi.Router, deps HandlerDeps) {
	if deps.Workflow == nil || deps.Enforcer == nil {
		// Cannot mount without workflow + enforcer. Leave routes off so the
		// startup wiring fails fast at first request rather than silently 500.
		return
	}
	r.Route("/governance", func(r chi.Router) {
		if deps.AuthMW != nil {
			r.Use(deps.AuthMW)
		}
		r.With(auth.RequirePermission(deps.Enforcer, "/governance/submit", "write")).
			Post("/submit", submitHandler(deps))
		r.With(auth.RequirePermission(deps.Enforcer, "/governance/reviews/*", "write")).
			Post("/reviews/{id}/approve", approveHandler(deps))
		r.With(auth.RequirePermission(deps.Enforcer, "/governance/reviews/*", "write")).
			Post("/reviews/{id}/reject", rejectHandler(deps))
		r.With(auth.RequirePermission(deps.Enforcer, "/governance/reviews/*", "manage")).
			Post("/reviews/{id}/reassign", reassignHandler(deps))
		r.Get("/status", statusHandler(deps))
		r.Get("/status/{asset}", statusForAssetHandler(deps))
	})
}

// ===== Bodies =====

type submitBody struct {
	Asset          string   `json:"asset"`
	CodeHash       string   `json:"code_hash"`
	ReviewersExtra []string `json:"reviewers_extra,omitempty"`
}

type decideBody struct {
	Comment string `json:"comment"`
}

type reassignBody struct {
	NewReviewers []string `json:"new_reviewers"`
}

// ===== Handlers =====

func submitHandler(deps HandlerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body submitBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "invalid JSON: "+err.Error())
			return
		}
		if body.Asset == "" || body.CodeHash == "" {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "asset and code_hash required")
			return
		}
		if deps.AssetLookup == nil {
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "asset lookup not configured")
			return
		}
		a, err := deps.AssetLookup.Get(body.Asset)
		if err != nil || a == nil {
			writeProblem(w, http.StatusNotFound, "Not Found", "asset not registered: "+body.Asset)
			return
		}
		var tags []string
		var owner string
		if deps.MetadataLookup != nil {
			tags, owner, _ = deps.MetadataLookup(r.Context(), body.Asset)
		}
		submitter := principalUUID(r)
		res, err := deps.Workflow.Submit(r.Context(), body.Asset, body.CodeHash, submitter, body.ReviewersExtra, a, tags, owner)
		if err != nil {
			if errors.Is(err, ErrAssetVersionNotFound) {
				writeProblem(w, http.StatusNotFound, "Not Found", err.Error())
				return
			}
			slog.Error("governance submit failed", "actor", submitter, "asset", body.Asset, "code_hash", body.CodeHash, "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		writeJSON(w, http.StatusCreated, res)
	}
}

func approveHandler(deps HandlerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		reviewID, err := uuid.Parse(idStr)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "invalid review id")
			return
		}
		var body decideBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		decider := principalUUID(r)
		rev, err := deps.Workflow.Approve(r.Context(), reviewID, decider, body.Comment)
		if err != nil {
			handleDecideError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rev)
	}
}

func rejectHandler(deps HandlerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		reviewID, err := uuid.Parse(idStr)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "invalid review id")
			return
		}
		var body decideBody
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Comment) == "" {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "comment is required for reject")
			return
		}
		decider := principalUUID(r)
		rev, err := deps.Workflow.Reject(r.Context(), reviewID, decider, body.Comment)
		if err != nil {
			handleDecideError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rev)
	}
}

func reassignHandler(deps HandlerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := chi.URLParam(r, "id")
		reviewID, err := uuid.Parse(idStr)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "invalid review id")
			return
		}
		var body reassignBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "invalid JSON: "+err.Error())
			return
		}
		if len(body.NewReviewers) == 0 {
			writeProblem(w, http.StatusBadRequest, "Bad Request", "new_reviewers must be non-empty")
			return
		}
		actor := principalUUID(r)
		rev, err := deps.Workflow.Reassign(r.Context(), reviewID, body.NewReviewers, actor)
		if err != nil {
			if errors.Is(err, ErrReviewNotFound) {
				writeProblem(w, http.StatusNotFound, "Not Found", err.Error())
				return
			}
			if errors.Is(err, ErrAlreadyDecided) {
				writeProblem(w, http.StatusConflict, "Conflict", err.Error())
				return
			}
			slog.Error("governance reassign failed", "actor", actor, "review", reviewID, "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		writeJSON(w, http.StatusOK, rev)
	}
}

func statusHandler(deps HandlerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		out, err := deps.Workflow.Status(r.Context(), "")
		if err != nil {
			slog.Error("governance status failed", "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func statusForAssetHandler(deps HandlerDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		assetName := chi.URLParam(r, "asset")
		out, err := deps.Workflow.Status(r.Context(), assetName)
		if err != nil {
			slog.Error("governance status failed", "asset", assetName, "err", err)
			writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// ===== helpers =====

func handleDecideError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrCommentRequired):
		writeProblem(w, http.StatusBadRequest, "Bad Request", err.Error())
	case errors.Is(err, ErrReviewNotFound):
		writeProblem(w, http.StatusNotFound, "Not Found", err.Error())
	case errors.Is(err, ErrAlreadyDecided):
		writeProblem(w, http.StatusConflict, "Conflict", err.Error())
	case errors.Is(err, ErrSelfApproval):
		writeProblem(w, http.StatusForbidden, "Forbidden", err.Error())
	case errors.Is(err, ErrDuplicateVote):
		writeProblem(w, http.StatusConflict, "Conflict", err.Error())
	default:
		slog.Error("governance decide failed", "err", err)
		writeProblem(w, http.StatusInternalServerError, "Internal Server Error", "internal error; see server logs")
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
