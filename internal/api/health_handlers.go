package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Health returns an HTTP handler that responds with {"status":"ok","version":version}
// It never touches the database.
func Health(version string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"version": version,
		})
	}
}

// Ready returns an HTTP handler that checks whether the database is reachable.
// It calls deps.Storage.Ping with a 2-second timeout; returns 200 on success,
// 503 problem+json on failure.
func Ready(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := deps.Storage.Ping(ctx); err != nil {
			ServiceUnavailable(w, "database not reachable: "+err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "ok",
		})
	}
}
