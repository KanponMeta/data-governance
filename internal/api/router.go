package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/lineage/openlineage"
	lineageq "github.com/kanpon/data-governance/internal/lineage/queries"
	"github.com/kanpon/data-governance/internal/storage"
	"github.com/kanpon/data-governance/internal/storage/ent"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Deps holds all the dependencies required to wire up the HTTP router.
type Deps struct {
	Auth    *auth.Service
	Issuer  *auth.TokenIssuer
	Storage storage.Storage
	Events  event.Writer
	Version string

	// Phase 4 additions (04-07):
	// Ent is the ent client for schema-ack mutation and metadata store.
	Ent *ent.Client
	// LineageDB is the raw DB connection passed to impact.Analyze (sqlc DBTX interface).
	// *pgxpool.Pool satisfies lineageq.DBTX.
	LineageDB lineageq.DBTX
	// OLTranslator is the OpenLineage export translator interface.
	OLTranslator openlineage.Translator
}

// NewRouter returns a chi router with all routes mounted and middleware applied.
func NewRouter(deps Deps) http.Handler {
	r := chi.NewRouter()

	// Global middleware: request ID, real IP, structured logging, recoverer, timeout.
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(requestLogger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	// Body limit: 1 MB max.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			next.ServeHTTP(w, r)
		})
	})

	// Public routes (no auth required).
	r.Route("/v1/auth", func(r chi.Router) {
		r.Post("/register", (&authHandler{svc: deps.Auth}).register)
		r.Post("/login", (&authHandler{svc: deps.Auth}).login)
		r.Post("/accept-invite", (&authHandler{svc: deps.Auth}).acceptInvite)
	})

	// Protected routes (JWT required).
	r.Group(func(r chi.Router) {
		r.Use(auth.Middleware(deps.Issuer, deps.Events))

		// Admin-only: POST /v1/auth/invites
		r.Route("/v1/auth/invites", func(r chi.Router) {
			r.Use(auth.RequireRole("admin"))
			r.Post("/", (&authHandler{svc: deps.Auth}).invite)
		})

		// Phase 4 (D-19, D-20, LINE-06): impact analysis (any authenticated user).
		r.Get("/v1/lineage/impact", impactHandler(deps))

		// Phase 4 (D-18, LINE-01): OpenLineage export (any authenticated user).
		r.Get("/v1/lineage/export", exportLineageHandler(deps))

		// Phase 4 (META-05, D-12): schema-change timeline (any authenticated user).
		r.Get("/v1/schema/changes", listSchemaChanges(deps))

		// Phase 4 (META-03, D-17): asset/column metadata read (any authenticated user).
		if deps.Ent != nil {
			mh := metadataHandler(deps)
			r.Get("/v1/assets/{name}/metadata", mh.Get)

			// Phase 4 governance-only writes — D-10 ack, D-17 PATCH metadata.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole("governance"))
				r.Post("/v1/schema/changes/{id}/ack", ackSchemaChange(deps))
				r.Patch("/v1/assets/{name}/metadata", mh.PatchAsset)
				r.Patch("/v1/assets/{name}/columns/{col}/metadata", mh.PatchColumn)
			})
		}
	})

	// Health, readiness, and metrics endpoints.
	r.Get("/health", Health(deps.Version))
	r.Get("/ready", Ready(deps))
	r.Handle("/metrics", promhttp.Handler())

	// gRPC stub placeholder (Phase 2 will replace with connect-go handlers).
	r.Mount("/grpc", NewGRPCMux())

	return r
}

// requestLogger is a structured logging middleware for chi.
// It logs method, path, status, duration_ms, and request_id; redacts Authorization header.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)

		next.ServeHTTP(ww, r)

		duration := time.Since(start).Milliseconds()

		// Redact Authorization header value.
		authHdr := r.Header.Get("Authorization")
		if authHdr != "" {
			if idx := strings.Index(authHdr, " "); idx >= 0 {
				authHdr = authHdr[:idx+1] + "[REDACTED]"
			} else {
				authHdr = "[REDACTED]"
			}
		}

		slog.Info("http_request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"duration_ms", duration,
			"request_id", middleware.GetReqID(r.Context()),
			"remote_ip", r.RemoteAddr,
			"authorization", authHdr,
		)
	})
}
