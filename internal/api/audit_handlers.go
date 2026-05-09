package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/platform"
)

// init self-registers audit routes via the platform registry (B-03 fix).
func init() {
	platform.RegisterRoutes("audit", MountAudit)
}

func MountAudit(r chi.Router, deps platform.MountDeps) {
	r.Route("/audit", func(r chi.Router) {
		r.Use(deps.AuthMW)
		r.With(auth.RequirePermission(deps.Enforcer, "/audit/export", "read")).Get("/export", handleExport(deps.DB))
		r.With(auth.RequirePermission(deps.Enforcer, "/audit/verify", "read")).Get("/verify", handleVerify(deps.DB))
	})
}

func handleExport(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		format := r.URL.Query().Get("format")
		if format == "" {
			format = string(audit.FormatJSONL)
		}
		fromStr := r.URL.Query().Get("from")
		toStr := r.URL.Query().Get("to")

		from := time.Now().AddDate(0, 0, -30) // default: last 30 days
		to := time.Now()
		if fromStr != "" {
			if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
				from = t
			}
		}
		if toStr != "" {
			if t, err := time.Parse(time.RFC3339, toStr); err == nil {
				to = t
			}
		}

		af := audit.Format(format)
		switch af {
		case audit.FormatJSONL:
			w.Header().Set("Content-Type", "application/x-ndjson")
		case audit.FormatCSV:
			w.Header().Set("Content-Type", "text/csv")
		case audit.FormatJSON:
			w.Header().Set("Content-Type", "application/json")
		default:
			http.Error(w, "unsupported format", http.StatusBadRequest)
			return
		}

		_, err := audit.Export(r.Context(), db, w, af, from, to)
		if err != nil {
			// Headers may already be sent — log only.
			_ = err
		}
	}
}

func handleVerify(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		result, err := audit.Verify(r.Context(), db, 1, 0)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
			return
		}
		if !result.OK {
			w.WriteHeader(http.StatusUnprocessableEntity)
			json.NewEncoder(w).Encode(map[string]any{
				"ok":           false,
				"scanned":      result.Scanned,
				"mismatch_seq": result.MismatchSeq,
				"stored_hash":   result.StoredHash,
				"computed_hash": result.ComputedHash,
			})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"ok":      true,
			"scanned": result.Scanned,
		})
	}
}
