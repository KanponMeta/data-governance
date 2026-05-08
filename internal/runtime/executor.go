// Package runtime provides the end-to-end run executor for the data governance
// platform (plans 02-03 through 02-05).
//
// The Executor is the heart of the execution engine: it reads queued runs from
// the database (via run.ClaimNext), resolves the asset dependency graph (via
// internal/dag), executes each step under the global concurrency token pool
// (via internal/concurrency), applies per-asset retry policy (via internal/retry),
// and emits run.* events for every state transition.
//
// Per-run heartbeat goroutine: Each Executor.Run spawns a goroutine that ticks
// runs.last_heartbeat = NOW() every HeartbeatInterval (default 30s). Plan 02-04's
// stale-run reaper detects workers that stopped ticking and resets their runs back
// to 'queued' — this is the D-14 Option B crash-recovery path. A 10x safety margin
// between the tick rate (30s) and the staleness threshold (5m) means transient
// delays cannot trigger spurious re-queues.
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/concurrency"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/dag"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/retry"
	"github.com/kanpon/data-governance/internal/run"
	"github.com/kanpon/data-governance/internal/storage"
)

// Deps bundles the Executor's dependencies. Plan 02-04 wires these from
// cmd/platform/main.go subcommands.
type Deps struct {
	Store             storage.Storage
	Events            event.Writer
	Registry          *asset.DefinitionRegistry
	ConnectorReg      *connector.Registry
	Pool              *concurrency.Pool
	DefaultPolicy     asset.RetryPolicy
	WorkerID          string
	StepTimeout       time.Duration // per-step ctx timeout; default 30m
	HeartbeatInterval time.Duration // tick rate for runs.last_heartbeat; default 30s
}

// Executor runs claimed runs end-to-end. Create with NewExecutor.
type Executor struct {
	deps Deps
}

// NewExecutor creates an Executor with the supplied dependencies. Zero-value
// StepTimeout is defaulted to 30 minutes; zero-value HeartbeatInterval to 30 seconds.
func NewExecutor(deps Deps) *Executor {
	if deps.StepTimeout == 0 {
		deps.StepTimeout = 30 * time.Minute
	}
	if deps.HeartbeatInterval == 0 {
		deps.HeartbeatInterval = 30 * time.Second
	}
	return &Executor{deps: deps}
}

// Run executes the run identified by runID targeting assetName. The run MUST already
// be in state 'starting' (post-claim via run.ClaimNext). Run is responsible for:
//   - Transitioning starting → running → succeeded/failed
//   - Building and resolving the DAG of transitive upstream assets
//   - Executing each step in topological order under the concurrency token pool
//   - Applying per-asset retry policy for business faults
//   - Writing run.* events for every step lifecycle transition
//   - Ticking runs.last_heartbeat every HeartbeatInterval via the heartbeat goroutine
//
// Infrastructure faults (worker crash, OOM) are handled by plan 02-04's stale-run
// reaper, NOT by this function — see D-14 Option B.
func (e *Executor) Run(ctx context.Context, runID uuid.UUID, assetName string) error {
	// Spawn the heartbeat goroutine for the duration of this run.
	// It exits when hbCancel is called (deferred below) or when ctx is canceled.
	// sync.WaitGroup ensures the goroutine has fully exited before Run returns.
	hbCtx, hbCancel := context.WithCancel(ctx)
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		e.heartbeatLoop(hbCtx, runID)
	}()
	defer func() {
		hbCancel()
		hbWG.Wait()
	}()

	// 1. Look up target asset, build transitive subgraph, resolve topological order.
	target, err := e.deps.Registry.Get(assetName)
	if err != nil {
		return fmt.Errorf("executor: resolve target asset %q: %w", assetName, err)
	}
	graph, err := buildSubgraph(e.deps.Registry, target)
	if err != nil {
		return fmt.Errorf("executor: build dag: %w", err)
	}
	order, err := graph.TopologicalOrder()
	if err != nil {
		return fmt.Errorf("executor: topo order: %w", err)
	}

	// 2. Transition starting → running, emit run.started.
	if err := e.transition(ctx, runID, run.StateStarting, run.StateRunning); err != nil {
		return err
	}
	e.appendEvent(ctx, runID, event.EventTypeRunStarted, event.RunStartedPayload{
		AssetName: assetName,
		ClaimedBy: e.deps.WorkerID,
	})

	// 3. Execute steps in topological order.
	for i, name := range order {
		stepAsset, _ := e.deps.Registry.Get(name)
		if err := e.runStep(ctx, runID, stepAsset, i); err != nil {
			// Step failed terminally (retries exhausted or unretryable).
			_ = e.transition(ctx, runID, run.StateRunning, run.StateFailed)
			e.appendEvent(ctx, runID, event.EventTypeRunFailed, event.RunFailedPayload{
				AssetName: assetName,
				Error:     err.Error(),
			})
			_ = e.deps.Pool.ReleaseAll(ctx, runID)
			return err
		}
	}

	// 4. All steps succeeded.
	_ = e.transition(ctx, runID, run.StateRunning, run.StateSucceeded)
	e.appendEvent(ctx, runID, event.EventTypeRunSucceeded, event.RunSucceededPayload{
		AssetName: assetName,
	})
	_ = e.deps.Pool.ReleaseAll(ctx, runID)
	return nil
}

// heartbeatLoop ticks runs.last_heartbeat = NOW() every HeartbeatInterval until ctx
// is canceled. The first tick is immediate so even very short runs leave a heartbeat
// trail for the reaper.
//
// Plan 02-04's reaper (5-minute staleness threshold) reads runs.last_heartbeat to
// detect crashed workers. With a 30s tick (default) and a 5m threshold, there is a
// 10x safety margin against scheduler hiccups.
func (e *Executor) heartbeatLoop(ctx context.Context, runID uuid.UUID) {
	// Immediate first tick.
	if err := run.Heartbeat(ctx, e.deps.Store, runID); err != nil {
		slog.Warn("executor.heartbeat_failed", "run_id", runID, "error", err)
	}
	t := time.NewTicker(e.deps.HeartbeatInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := run.Heartbeat(ctx, e.deps.Store, runID); err != nil {
				slog.Warn("executor.heartbeat_failed", "run_id", runID, "error", err)
			}
		}
	}
}

// runStep executes a single asset step with retry logic and concurrency token
// management. It loops until the step succeeds or all retries are exhausted.
func (e *Executor) runStep(ctx context.Context, runID uuid.UUID, a *asset.Asset, topoOrder int) error {
	policy := a.RetryPolicy()
	if policy.IsZero() {
		policy = e.deps.DefaultPolicy
	}

	for attempt := 1; ; attempt++ {
		// Acquire concurrency tokens for the global tag + each declared resource.
		// SINGLE pool, multiple checkouts (D-16). Release at end of this attempt.
		var acquired []string
		releaseAcquired := func() {
			for _, tag := range acquired {
				_ = e.deps.Pool.Release(ctx, runID, tag)
			}
		}

		// Global token (run-level cap).
		if err := e.deps.Pool.Acquire(ctx, runID, a.Name(), "global", 1); err != nil {
			releaseAcquired()
			if !retry.ShouldRetry(attempt, policy) {
				e.appendEvent(ctx, runID, event.EventTypeRunStepFailed, event.RunStepFailedPayload{
					AssetName: a.Name(), Attempt: attempt, Error: err.Error(),
				})
				return fmt.Errorf("executor: step %q failed to acquire global token (retries exhausted): %w", a.Name(), err)
			}
			e.scheduleRetry(ctx, runID, a, attempt, err, policy)
			continue
		}
		acquired = append(acquired, "global")

		// Resource-level tokens per asset.Resources() declaration.
		var resourceErr error
		for _, res := range a.Resources() {
			if err := e.deps.Pool.Acquire(ctx, runID, a.Name(), res.Name, res.Weight); err != nil {
				resourceErr = err
				break
			}
			acquired = append(acquired, res.Name)
		}
		if resourceErr != nil {
			releaseAcquired()
			if !retry.ShouldRetry(attempt, policy) {
				e.appendEvent(ctx, runID, event.EventTypeRunStepFailed, event.RunStepFailedPayload{
					AssetName: a.Name(), Attempt: attempt, Error: resourceErr.Error(),
				})
				return fmt.Errorf("executor: step %q failed to acquire resource token (retries exhausted): %w", a.Name(), resourceErr)
			}
			e.scheduleRetry(ctx, runID, a, attempt, resourceErr, policy)
			continue
		}

		// Emit step started event.
		e.appendEvent(ctx, runID, event.EventTypeRunStepStarted, event.RunStepStartedPayload{
			AssetName: a.Name(), TopoOrder: topoOrder, Attempt: attempt,
		})

		// Execute the user function with panic recovery + per-step timeout.
		stepCtx, cancel := context.WithTimeout(ctx, e.deps.StepTimeout)
		startedAt := time.Now().UTC()
		io := asset.NewAssetIO(a, e) // executor implements asset.ConnectorResolver
		result, runErr := safeMaterialize(stepCtx, a.MaterializeFn(), io)
		cancel()
		releaseAcquired()
		durationMs := time.Since(startedAt).Milliseconds()

		if runErr == nil {
			e.appendEvent(ctx, runID, event.EventTypeRunStepSucceeded, event.RunStepSucceededPayload{
				AssetName:   a.Name(),
				RowsWritten: result.RowsWritten,
				DurationMs:  durationMs,
				Metadata:    result.Metadata,
			})
			return nil
		}

		// Step failed — emit failure event.
		e.appendEvent(ctx, runID, event.EventTypeRunStepFailed, event.RunStepFailedPayload{
			AssetName: a.Name(), Attempt: attempt, Error: runErr.Error(),
			DurationMs: durationMs,
		})

		if !retry.ShouldRetry(attempt, policy) {
			return fmt.Errorf("executor: step %q exhausted retries (last err: %w)", a.Name(), runErr)
		}
		e.scheduleRetry(ctx, runID, a, attempt, runErr, policy)
		// Loop continues for attempt+1.
	}
}

// safeMaterialize wraps the user's MaterializeFunc in panic recovery (T-02-03-01).
// A panic is captured and returned as a formatted error; the recovered value is
// included in the error message so the event payload carries it.
func safeMaterialize(ctx context.Context, fn asset.MaterializeFunc, io asset.AssetIO) (result asset.MaterializeResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("materialize panic: %v", r)
		}
	}()
	return fn(ctx, io)
}

// scheduleRetry writes a run.step.retry_scheduled event and sleeps for the computed
// delay before returning control to the retry loop.
func (e *Executor) scheduleRetry(
	ctx context.Context, runID uuid.UUID, a *asset.Asset,
	attempt int, runErr error, policy asset.RetryPolicy,
) {
	delay := retry.Schedule(attempt, policy)
	scheduledAt := time.Now().UTC().Add(delay)
	e.appendEvent(ctx, runID, event.EventTypeRunStepRetryScheduled, event.RunStepRetryScheduledPayload{
		AssetName:   a.Name(),
		Attempt:     attempt,
		NextAttempt: attempt + 1,
		ScheduledAt: scheduledAt,
		DelayMs:     delay.Milliseconds(),
		Error:       runErr.Error(),
	})
	if delay > 0 {
		slog.Info("executor.retry_scheduled",
			"run_id", runID, "asset", a.Name(),
			"attempt", attempt, "delay_ms", delay.Milliseconds())
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return
		}
	}
}

// Resolve implements asset.ConnectorResolver. The executor is the resolver that
// AssetIO uses to look up the connector for a given asset name.
func (e *Executor) Resolve(assetName string) (connector.Connector, connector.AssetRef, error) {
	a, err := e.deps.Registry.Get(assetName)
	if err != nil {
		return nil, connector.AssetRef{}, fmt.Errorf("executor: resolve asset %q: %w", assetName, err)
	}
	c, err := e.deps.ConnectorReg.Get(a.ConnectorName())
	if err != nil {
		return nil, connector.AssetRef{}, fmt.Errorf("executor: get connector %q for asset %q: %w", a.ConnectorName(), assetName, err)
	}
	return c, connector.AssetRef{Identifier: a.Name()}, nil
}

// transition validates the FSM edge and applies it to the runs table.
func (e *Executor) transition(ctx context.Context, runID uuid.UUID, from, to run.State) error {
	if err := run.Transition(from, to); err != nil {
		return err
	}
	const sql = `UPDATE runs SET state = $1, started_at = COALESCE(started_at, NOW()) WHERE id = $2 AND state = $3`
	_, err := e.deps.Store.DB().ExecContext(ctx, sql, string(to), runID, string(from))
	return err
}

// appendEvent writes an event to the event log; logs a warning on error but does
// not fail the run (events are observability, not coordination).
func (e *Executor) appendEvent(ctx context.Context, runID uuid.UUID, evtType event.EventType, payload any) {
	if err := e.deps.Events.Append(ctx, event.Event{
		Type:         evtType,
		ResourceType: "run",
		ResourceID:   runID.String(),
		Payload:      payload,
	}); err != nil {
		slog.Error("executor.event_append_failed", "type", evtType, "run_id", runID, "error", err)
	}
}

// buildSubgraph constructs a dag.Graph containing the target asset plus all of its
// transitive upstream assets. The graph is used solely for topological ordering;
// it does not touch the global DefinitionRegistry (no side effects).
func buildSubgraph(reg *asset.DefinitionRegistry, target *asset.Asset) (*dag.Graph, error) {
	var assets []*asset.Asset
	seen := make(map[string]struct{})

	var walk func(*asset.Asset) error
	walk = func(a *asset.Asset) error {
		if _, ok := seen[a.Name()]; ok {
			return nil
		}
		seen[a.Name()] = struct{}{}
		assets = append(assets, a)
		for _, up := range a.Upstreams() {
			upAsset, err := reg.Get(up)
			if err != nil {
				return fmt.Errorf("buildSubgraph: missing upstream %q of %q: %w", up, a.Name(), err)
			}
			if err := walk(upAsset); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(target); err != nil {
		return nil, err
	}
	return dag.BuildDAG(assets)
}
