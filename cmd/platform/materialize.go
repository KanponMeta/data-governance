package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"
)

// runMaterialize is the body of the `materialize <asset>` subcommand.
// Default mode: synchronous — insert queued run, poll until terminal state, exit with status code.
// --detach mode: insert queued run, print UUID, exit 0 immediately.
//
// JWT authentication (Phase 1 reuse): PLATFORM_SERVICE_TOKEN is validated using
// PLATFORM_JWT_SIGNING_KEY. If PLATFORM_NO_AUTH=1 is set, auth is bypassed (dev only).
func runMaterialize(args []string) error {
	fs := flag.NewFlagSet("materialize", flag.ExitOnError)
	detach := fs.Bool("detach", false, "return immediately after queueing the run")
	timeout := fs.Duration("timeout", 30*time.Minute, "synchronous-mode max wait")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: platform materialize [--detach] <asset>")
	}
	assetName := fs.Arg(0)

	ctx, cancel := context.WithTimeout(context.Background(), *timeout+30*time.Second)
	defer cancel()

	// JWT authentication (Phase 1 reuse). If PLATFORM_SERVICE_TOKEN is empty AND
	// PLATFORM_NO_AUTH is not "1", we refuse to proceed.
	// Note: auth.ParseToken does not exist as a standalone function; we use
	// auth.TokenIssuer.Verify with the JWT_SIGNING_KEY from env (T-02-04-01).
	if os.Getenv("PLATFORM_NO_AUTH") != "1" {
		tok := os.Getenv("PLATFORM_SERVICE_TOKEN")
		if tok == "" {
			return fmt.Errorf("materialize: PLATFORM_SERVICE_TOKEN is required (set PLATFORM_NO_AUTH=1 to bypass for local dev)")
		}
		signingKey := os.Getenv("PLATFORM_JWT_SIGNING_KEY")
		if signingKey == "" {
			return fmt.Errorf("materialize: PLATFORM_JWT_SIGNING_KEY is required for token validation")
		}
		issuer := auth.NewTokenIssuer([]byte(signingKey), 0)
		if _, err := issuer.Verify(tok); err != nil {
			return fmt.Errorf("materialize: invalid PLATFORM_SERVICE_TOKEN: %w", err)
		}
	}

	deps, err := bootstrap(ctx)
	if err != nil {
		return err
	}
	defer deps.cleanup()

	if _, err := deps.registry.Get(assetName); err != nil {
		return fmt.Errorf("materialize: asset %q not registered: %w", assetName, err)
	}

	// Insert queued run; emit run.queued event.
	runID := uuid.New()
	const insertSQL = `
		INSERT INTO runs (id, asset_name, state, trigger, queued_at)
		VALUES ($1, $2, 'queued', 'manual', NOW())
	`
	if _, err := deps.store.DB().ExecContext(ctx, insertSQL, runID, assetName); err != nil {
		return fmt.Errorf("materialize: insert run: %w", err)
	}
	if err := deps.events.Append(ctx, event.Event{
		Type:         event.EventTypeRunQueued,
		ResourceType: "run",
		ResourceID:   runID.String(),
		Payload:      event.RunQueuedPayload{AssetName: assetName, Trigger: "manual"},
	}); err != nil {
		slog.Warn("materialize.event_append_failed", "error", err)
	}

	if *detach {
		fmt.Println(runID.String())
		return nil
	}

	// Synchronous: poll runs.state until terminal.
	slog.Info("materialize.queued", "run_id", runID, "asset", assetName)
	return waitForRun(ctx, deps, runID, *timeout)
}

// waitForRun polls runs.state every 500ms until the run reaches a terminal state
// or the timeout is exceeded. Exits with a non-nil error if the run fails or times out.
func waitForRun(ctx context.Context, deps *workerDeps, runID uuid.UUID, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	const stateSQL = `SELECT state, COALESCE(error_message, '') FROM runs WHERE id = $1`
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state, errMsg string
		if err := deps.store.DB().QueryRowContext(ctx, stateSQL, runID).Scan(&state, &errMsg); err != nil {
			return fmt.Errorf("materialize: poll state: %w", err)
		}
		switch state {
		case "succeeded":
			fmt.Printf("run %s succeeded\n", runID)
			return nil
		case "failed":
			fmt.Fprintf(os.Stderr, "run %s failed: %s\n", runID, errMsg)
			return fmt.Errorf("materialize: run failed (id=%s)", runID)
		case "canceled":
			return fmt.Errorf("materialize: run canceled (id=%s)", runID)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("materialize: timeout waiting for run %s (last state=%s)", runID, state)
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
