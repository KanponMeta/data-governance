package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/kanpon/data-governance/internal/config"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/platform"
	"github.com/kanpon/data-governance/internal/policy"
)

// init self-registers the reconciler subcommand via the platform registry (B-03 fix).
func init() {
	platform.RegisterCommand("reconciler", dispatchReconciler)
}

// dispatchReconciler is the entry point for `./platform reconciler`.
//
//	platform reconciler [--interval=15m] [--grace=5m] [--once]
func dispatchReconciler(args []string) int {
	fs := flag.NewFlagSet("reconciler", flag.ContinueOnError)
	interval := fs.Duration("interval", 15*time.Minute, "scan interval")
	grace := fs.Duration("grace", 5*time.Minute, "grace period after policy push")
	once := fs.Bool("once", false, "run a single tick then exit (CI usage)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconciler: load config: %v\n", err)
		return 1
	}

	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconciler: open db: %v\n", err)
		return 1
	}
	defer db.Close()

	store := policy.NewStore(db, nil)
	resolver := newReconcilerConnectorResolver()
	auditWriter := &policy.SQLAuditWriter{DB: db}
	rec := policy.NewReconciler(store, resolver, auditWriter, &noopReEnqueuer{})
	rec.GracePeriod = *grace

	logger := slog.Default()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runOnce := func() {
		t0 := time.Now()
		report, err := rec.Tick(ctx)
		if err != nil {
			logger.Error("reconciler.tick_failed", "error", err.Error())
			return
		}
		logger.Info("reconciler.tick_done",
			"scanned", report.Scanned, "drifts", report.Drifts, "pushed", report.Pushed,
			"duration_ms", time.Since(t0).Milliseconds(),
			"errors", len(report.Errors))
	}

	if *once {
		runOnce()
		return 0
	}

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	logger.Info("reconciler.start", "interval", interval.String(), "grace", grace.String())
	runOnce()
	for {
		select {
		case <-ctx.Done():
			logger.Info("reconciler.stop", "reason", ctx.Err().Error())
			return 0
		case <-ticker.C:
			runOnce()
		}
	}
}

// noopReEnqueuer is the placeholder ReEnqueuer used until River wiring lands.
// It logs the request without enqueueing — sufficient for v1 reconciler
// observability (drift is also written to the hash-chain audit log).
type noopReEnqueuer struct{}

func (noopReEnqueuer) ReEnqueueSync(_ context.Context, args policy.PolicySyncArgs) error {
	slog.Default().Info("reconciler.would_reenqueue",
		"asset", args.Asset, "column", args.Column, "reason", args.Reason)
	return nil
}

// reconcilerConnectorResolver is the production ConnectorResolver — looks up
// the asset in the global asset.Registry, gets its connector name, then
// fetches the connector from connector.Registry. It is a stub for v1: the
// resolver is wired during the start subcommand; the reconciler subcommand
// uses a no-op resolver so it does not require the full asset/runtime
// initialisation. Future plans (05-03+) will plug in the real resolver.
type reconcilerConnectorResolver struct{}

func newReconcilerConnectorResolver() *reconcilerConnectorResolver {
	return &reconcilerConnectorResolver{}
}

// ResolveByAsset returns ErrNoConnector for the v1 stub. Reconciler treats
// this as a non-fatal error per asset and continues — drifts are reported
// via the audit chain regardless.
func (r *reconcilerConnectorResolver) ResolveByAsset(_ context.Context, asset string) (connector.Connector, connector.AssetRef, error) {
	return nil, connector.AssetRef{}, fmt.Errorf("reconciler: connector resolver stub — asset=%q not wired (Phase 5 plan 05-03 supplies real resolver)", asset)
}
