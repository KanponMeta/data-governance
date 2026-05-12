package api

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/kanpon/data-governance/internal/config"
)

// Run starts the HTTP server and blocks until ctx is cancelled.
// On context cancellation, it initiates a graceful shutdown with a 10-second timeout.
// If deps.ServeSPA is true, non-API routes serve the embedded React SPA.
func Run(ctx context.Context, cfg config.Config, deps Deps) error {
	router := NewRouter(deps)

	// If SPA serving is enabled, wrap with SPA handler for non-API routes.
	var handler http.Handler = router
	if deps.ServeSPA {
		handler = spaHandler(router, deps.StaticAssets)
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Start server in a goroutine.
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.ListenAndServe()
	}()

	// Wait for context cancellation or server error.
	select {
	case <-ctx.Done():
		// Graceful shutdown.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-serverErr:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}

// spaHandler returns an http.Handler that serves the React SPA for non-API routes.
// API routes (starting with /v1, /health, /metrics, /grpc) are handled by the API router.
func spaHandler(apiHandler http.Handler, assets embed.FS) http.Handler {
	// Try to get the 'dist' subdirectory from the embedded FS.
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		// Fall back to serving from root if 'dist' doesn't exist.
		dist = assets
	}

	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API routes are handled by the chi router.
		path := r.URL.Path
		if strings.HasPrefix(path, "/v1") ||
			strings.HasPrefix(path, "/health") ||
			strings.HasPrefix(path, "/metrics") ||
			strings.HasPrefix(path, "/grpc") ||
			strings.HasPrefix(path, "/debug") {
			apiHandler.ServeHTTP(w, r)
			return
		}

		// SPA handles all other routes (client-side routing).
		// For root path, serve index.html.
		if path == "/" {
			r.URL.Path = "/index.html"
		}
		fileServer.ServeHTTP(w, r)
	})
}
