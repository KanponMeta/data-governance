package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/kanpon/data-governance/internal/api"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/config"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/storage"
)

var version = "0.1.0-phase1"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cmd := "start"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "start":
		if err := runStart(); err != nil {
			slog.Error("platform.start_failed", "error", err)
			os.Exit(1)
		}
	case "migrate":
		if err := runMigrate(); err != nil {
			slog.Error("platform.migrate_failed", "error", err)
			os.Exit(1)
		}
	case "healthcheck":
		runHealthcheck()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		os.Exit(2)
	}
}

func runStart() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	store, err := storage.NewPostgres(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer store.Close()

	writer := event.NewWriter(store)

	issuer := auth.NewTokenIssuer(cfg.JWTSigningKey, cfg.JWTAccessTTL)
	svc := auth.NewService(store, writer, issuer)

	// Emit platform.started event.
	if err := writer.Append(ctx, event.Event{
		Type:         event.EventTypePlatformStarted,
		OccurredAt:   time.Now().UTC(),
		ResourceType: "platform",
		ResourceID:   "self",
		Payload:      map[string]any{"version": version},
	}); err != nil {
		return fmt.Errorf("emit platform.started: %w", err)
	}

	slog.Info("platform.started", "version", version)

	deps := api.Deps{
		Auth:    svc,
		Issuer:  issuer,
		Storage: store,
		Events:  writer,
		Version: version,
	}

	return api.Run(ctx, cfg, deps)
}

// runHealthcheck performs an HTTP GET against /health on localhost.
// It reads PLATFORM_HTTP_ADDR (default ":8080") and forms the URL accordingly.
// Exits 0 if the server responds with 200, exits 1 otherwise.
func runHealthcheck() {
	addr := os.Getenv("PLATFORM_HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	// Convert ":8080" to "127.0.0.1:8080" for the URL.
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}

	url := "http://" + addr + "/health"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		slog.Error("healthcheck.create_request_failed", "error", err)
		os.Exit(1)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Error("healthcheck.request_failed", "error", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		os.Exit(0)
	}
	slog.Error("healthcheck.unhealthy", "status", resp.StatusCode)
	os.Exit(1)
}

// runMigrate shells out to the atlas CLI to apply pending migrations
// (`atlas migrate apply --env <atlasEnv>`, default env=local), then opens
// storage and appends a platform.migration_applied event recording the
// migration in the audit log. The atlas binary must be on PATH; the
// Makefile and CI install it via `curl -sSf https://atlasgo.sh | sh`.
func runMigrate() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	atlasEnv := os.Getenv("ATLAS_ENV")
	if atlasEnv == "" {
		atlasEnv = "local"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Run `atlas migrate apply --env <atlasEnv>`. Atlas reads atlas.hcl
	// and uses the env's `url` (which can reference DATABASE_URL via getenv).
	cmd := exec.CommandContext(ctx, "atlas", "migrate", "apply", "--env", atlasEnv)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ() // pass DATABASE_URL through
	startedAt := time.Now().UTC()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("atlas migrate apply --env %s: %w (is the atlas CLI installed and on PATH?)", atlasEnv, err)
	}
	finishedAt := time.Now().UTC()

	// After successful migration, append the audit event.
	store, err := storage.NewPostgres(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open storage post-migration: %w", err)
	}
	defer store.Close()

	writer := event.NewWriter(store)
	if err := writer.Append(ctx, event.Event{
		Type:         event.EventTypePlatformMigrationApplied,
		OccurredAt:   finishedAt,
		ResourceType: "platform",
		ResourceID:   "migrations",
		Payload: map[string]any{
			"applied_at":  finishedAt,
			"started_at":  startedAt,
			"atlas_env":   atlasEnv,
			"duration_ms": finishedAt.Sub(startedAt).Milliseconds(),
		},
	}); err != nil {
		return fmt.Errorf("emit platform.migration_applied: %w", err)
	}
	slog.Info("platform.migration_applied",
		"atlas_env", atlasEnv,
		"duration_ms", finishedAt.Sub(startedAt).Milliseconds(),
	)
	return nil
}
