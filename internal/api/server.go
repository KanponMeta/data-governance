package api

import (
	"context"
	"net/http"
	"time"

	"github.com/kanpon/data-governance/internal/config"
)

// Run starts the HTTP server and blocks until ctx is cancelled.
// On context cancellation, it initiates a graceful shutdown with a 10-second timeout.
func Run(ctx context.Context, cfg config.Config, deps Deps) error {
	router := NewRouter(deps)

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
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
