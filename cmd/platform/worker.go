package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/concurrency"
	"github.com/kanpon/data-governance/internal/connector"
	conncfg "github.com/kanpon/data-governance/internal/connector/config"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/governance"
	"github.com/kanpon/data-governance/internal/lineage"
	"github.com/kanpon/data-governance/internal/policy"
	"github.com/kanpon/data-governance/internal/run"
	"github.com/kanpon/data-governance/internal/runtime"
	"github.com/kanpon/data-governance/internal/schema"
	"github.com/kanpon/data-governance/internal/storage"
)

// runWorker is the body of the `worker` subcommand. It claims queued runs and
// executes them under runtime.Executor. It also spawns a stale-run reaper goroutine
// that recovers crashed-worker runs (D-14 Option B). Loops until ctx is canceled
// (SIGTERM/INT).
func runWorker() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	deps, err := bootstrap(ctx)
	if err != nil {
		return err
	}
	defer deps.cleanup()

	// Clean up tokens left by previous crashed worker.
	if n, err := deps.pool.ReleaseStale(ctx, 24*time.Hour); err != nil {
		slog.Warn("worker.release_stale_failed", "error", err)
	} else if n > 0 {
		slog.Info("worker.released_stale_tokens", "count", n)
	}

	// Spawn the stale-run reaper goroutine. Recovers runs whose worker died
	// mid-execution. See plan 02-04 <context> for D-14 Option B rationale.
	reaper := &run.StaleRunReaper{
		Store:      deps.store,
		Events:     deps.events,
		StaleAfter: run.DefaultReaperStaleAfter, // 5 minutes
		Interval:   run.DefaultReaperInterval,   // 60 seconds
	}
	var reaperWG sync.WaitGroup
	reaperWG.Add(1)
	go func() {
		defer reaperWG.Done()
		reaper.Run(ctx)
	}()
	defer reaperWG.Wait()

	slog.Info("worker.started", "worker_id", deps.workerID)

	for {
		if ctx.Err() != nil {
			slog.Info("worker.shutdown")
			return nil
		}

		claimed, err := run.ClaimNext(ctx, deps.store, deps.workerID)
		if errors.Is(err, run.ErrNoQueuedRun) {
			// Idle: sleep briefly, then poll.
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return nil
			}
			continue
		}
		if err != nil {
			slog.Error("worker.claim_failed", "error", err)
			continue
		}

		slog.Info("worker.run_claimed",
			"run_id", claimed.ID,
			"asset", claimed.AssetName,
			"priority", claimed.Priority,
			"partition_key", derefString(claimed.PartitionKey),
		)
		execErr := deps.executor.Run(ctx, claimed)
		if execErr != nil {
			slog.Error("worker.run_failed", "run_id", claimed.ID, "error", execErr)
		} else {
			slog.Info("worker.run_succeeded", "run_id", claimed.ID)
		}
	}
}

// derefString returns *s, or "" if s is nil. Helper for slog fields where the
// caller should not log the literal "<nil>" representation of a nil pointer.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// workerDeps bundles the runtime dependencies shared by worker and materialize subcommands.
type workerDeps struct {
	store      storage.Storage
	events     event.Writer
	registry   *asset.DefinitionRegistry
	connectors *connector.Registry
	pool       *concurrency.Pool
	executor   *runtime.Executor
	workerID   string
	cleanup    func()
}

// bootstrap loads config, opens storage, builds the connector registry from config,
// and constructs an Executor. Shared by worker and materialize subcommands.
func bootstrap(ctx context.Context) (*workerDeps, error) {
	cfgPath := os.Getenv("PLATFORM_CONFIG")
	if cfgPath == "" {
		cfgPath = "./config.yaml"
	}
	cfg, err := conncfg.LoadFile(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("worker: load config %q: %w", cfgPath, err)
	}

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return nil, fmt.Errorf("worker: DATABASE_URL is required")
	}
	store, err := storage.NewPostgres(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("worker: open storage: %w", err)
	}

	connReg := connector.NewRegistry()
	if err := newFactoryRegistry().BuildAll(cfg, connReg); err != nil {
		store.Close()
		return nil, fmt.Errorf("worker: build connectors: %w", err)
	}

	capacities := []concurrency.Capacity{
		{Tag: "global", Limit: cfg.Concurrency.DefaultRunTokens},
	}
	// D-13 layer 3 default: 5 concurrent backfill runs unless operator overrides
	// via cfg.Concurrency.Resources["backfill"]. Caps connector saturation when
	// the priority claim has let many backfill rows reach the executor.
	backfillSet := false
	for tag, limit := range cfg.Concurrency.Resources {
		if tag == "backfill" {
			backfillSet = true
		}
		capacities = append(capacities, concurrency.Capacity{Tag: tag, Limit: limit})
	}
	if !backfillSet {
		capacities = append(capacities, concurrency.Capacity{Tag: "backfill", Limit: 5})
	}
	pool := concurrency.NewPool(store, capacities)

	defaultPolicy := asset.RetryPolicy{
		Max:          cfg.Retry.Default.Max,
		InitialDelay: cfg.Retry.Default.InitialDelay,
		MaxDelay:     cfg.Retry.Default.MaxDelay,
		JitterPct:    cfg.Retry.Default.JitterPct,
	}

	writer := event.NewWriter(store)

	workerID := fmt.Sprintf("worker-%s-%d", os.Getenv("HOSTNAME"), os.Getpid())

	// Phase 4 capture writers — wired into the executor's per-step transaction
	// (executor.commitSuccess) and into asset.Default().OnRegister so static
	// upstream edges are materialized at registration time.
	//
	// Phase 5 Plan 05-03 (D-06): the lineage writer's PII propagator runs in
	// the same tx as column_edges UPSERT — zero unmasked window for downstream
	// columns. The policy store is also exposed as MaskRulesProvider so the
	// executor can wrap MaskingIO around AssetIO.Write for non-warehouse
	// connectors (RBAC-05).
	policyStore := policy.NewStore(store.DB(), nil)
	propagator := governance.NewPropagator()
	lineageWriter := lineage.NewWriter(store.DB(), writer).WithPropagator(propagator)
	schemaWriter := schema.NewWriter(writer)
	asset.Default().OnRegister = func(a *asset.Asset) error {
		return lineageWriter.SyncStaticEdges(ctx, a, a.CodeHash())
	}

	exec := runtime.NewExecutor(runtime.Deps{
		Store:             store,
		Events:            writer,
		Registry:          asset.Default(),
		ConnectorReg:      connReg,
		Pool:              pool,
		DefaultPolicy:     defaultPolicy,
		WorkerID:          workerID,
		LineageWriter:     lineageWriter,
		SchemaWriter:      schemaWriter,
		MaskRulesProvider: policyStore,
		// HeartbeatInterval defaults to 30s in NewExecutor — paired with reaper StaleAfter=5m.
	})

	return &workerDeps{
		store:      store,
		events:     writer,
		registry:   asset.Default(),
		connectors: connReg,
		pool:       pool,
		executor:   exec,
		workerID:   workerID,
		cleanup:    func() { store.Close() },
	}, nil
}
