package policy

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/connector"
)

// Reconciler is the drift detection daemon. It periodically scans every
// asset that has at least one active column_policies row, fetches the
// current warehouse state via MaskingProvisioner.ListMaskingPolicies, and
// emits masking.sync_drift_detected entries for any (column, mask)
// mismatch — automatically re-enqueueing a PolicySyncArgs job to converge.
//
// GracePeriod (default 5min) skips columns whose last_seen_at is recent;
// this avoids false drift during BigQuery IAM propagation windows
// (Pitfall #4).
type Reconciler struct {
	Store       *Store
	Connectors  ConnectorResolver
	GracePeriod time.Duration
	Audit       AuditWriter
	// ReEnqueue is the hook the reconciler calls when it detects drift —
	// production wiring plugs in the same SyncEnqueuer used by the Store
	// so the reconciler-issued job uses the standard transactional path.
	// Tests inject a recorder.
	ReEnqueue ReEnqueuer
	Logger    *slog.Logger
	// Now is overridable for deterministic testing of the grace period.
	Now func() time.Time
}

// ReEnqueuer is the abstract sync re-enqueue interface used by the reconciler.
// It is intentionally distinct from SyncEnqueuer so the reconciler can run
// outside any existing transaction (River.Insert without a tx).
type ReEnqueuer interface {
	ReEnqueueSync(ctx context.Context, args PolicySyncArgs) error
}

// NewReconciler wires a Reconciler with sane defaults.
func NewReconciler(store *Store, conns ConnectorResolver, audit AuditWriter, re ReEnqueuer) *Reconciler {
	return &Reconciler{
		Store:       store,
		Connectors:  conns,
		GracePeriod: 5 * time.Minute,
		Audit:       audit,
		ReEnqueue:   re,
		Logger:      slog.Default(),
		Now:         time.Now,
	}
}

// Report aggregates the outcome of one Tick().
type Report struct {
	Scanned int      // assets scanned
	Drifts  int      // drift entries emitted
	Pushed  int      // re-enqueued sync jobs
	Errors  []string // non-fatal per-asset errors
}

// Tick executes one reconciliation pass. Iterates every asset returned by
// Store.ListAllAssets, fetches actual + expected, diffs, emits
// masking.sync_drift_detected, re-enqueues sync.
//
// Tick is safe to invoke concurrently (the underlying connector + store
// methods are safe for concurrent use). Production wiring schedules Tick
// via a 15-minute time.Ticker (cmd/platform/reconciler.go).
func (r *Reconciler) Tick(ctx context.Context) (Report, error) {
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Logger == nil {
		r.Logger = slog.Default()
	}
	rep := Report{}
	assets, err := r.Store.ListAllAssets(ctx)
	if err != nil {
		return rep, fmt.Errorf("reconciler: list assets: %w", err)
	}
	rep.Scanned = len(assets)

	for _, a := range assets {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		if err := r.tickAsset(ctx, a, &rep); err != nil {
			rep.Errors = append(rep.Errors, fmt.Sprintf("%s: %v", a, err))
			r.Logger.Warn("policy.reconciler.asset_error", "asset", a, "error", err.Error())
		}
	}
	return rep, nil
}

func (r *Reconciler) tickAsset(ctx context.Context, assetName string, rep *Report) error {
	conn, ref, err := r.Connectors.ResolveByAsset(ctx, assetName)
	if err != nil {
		return fmt.Errorf("connector resolve: %w", err)
	}
	mp, ok := conn.(connector.MaskingProvisioner)
	if !ok {
		// In-pipeline path; reconciler doesn't apply.
		return nil
	}
	actual, err := mp.ListMaskingPolicies(ctx, ref)
	if err != nil {
		return fmt.Errorf("list masking policies: %w", err)
	}
	expected, err := r.Store.List(ctx, assetName, "")
	if err != nil {
		return fmt.Errorf("list expected: %w", err)
	}

	// Build a map: column → effective Active row (use precedence-first
	// shortcut: Active rows include all sources; pick highest precedence
	// per column when multiple exist).
	expectedByCol := map[string]Active{}
	priority := map[string]int{"runtime": 3, "builder": 2, "yaml-default": 1}
	for _, e := range expected {
		if cur, has := expectedByCol[e.Column]; has {
			if priority[e.Source] <= priority[cur.Source] {
				continue
			}
		}
		expectedByCol[e.Column] = e
	}

	actualByCol := map[string]connector.MaskType{}
	for _, p := range actual {
		actualByCol[p.Column] = p.MaskType
	}

	now := r.Now()

	// Detect MISSING: expected but not actual (or wrong mask).
	for col, exp := range expectedByCol {
		// Apply the grace period — recent changes are ignored.
		if now.Sub(exp.LastSeenAt) < r.GracePeriod {
			continue
		}
		actMask, exists := actualByCol[col]
		if !exists || actMask != exp.Mask {
			r.recordDrift(ctx, assetName, col, exp.Mask, actMask, exists, rep)
		}
	}

	// Detect EXTRA: actual but not expected.
	for col, mt := range actualByCol {
		if _, has := expectedByCol[col]; has {
			continue
		}
		r.recordDrift(ctx, assetName, col, "", mt, true, rep)
	}
	return nil
}

func (r *Reconciler) recordDrift(ctx context.Context, assetName, col string, expected, actual connector.MaskType, actualExists bool, rep *Report) {
	rep.Drifts++
	r.Logger.Warn("policy.reconciler.drift_detected",
		"asset", assetName, "column", col,
		"expected", string(expected), "actual", string(actual),
		"actual_exists", actualExists)

	if r.Audit != nil {
		_ = r.Audit.WritePermanentFailure(ctx, audit.Entry{
			EventType:    audit.AuditMaskingSyncDriftDetected,
			OccurredAt:   time.Now().UTC(),
			ResourceType: "column_policy",
			ResourceID:   assetName + "." + col,
			Payload: map[string]any{
				"asset":         assetName,
				"column":        col,
				"expected":      string(expected),
				"actual":        string(actual),
				"actual_exists": actualExists,
			},
		})
	}
	if r.ReEnqueue != nil {
		if err := r.ReEnqueue.ReEnqueueSync(ctx, PolicySyncArgs{
			Asset: assetName, Column: col, Reason: "reconciler-drift",
		}); err != nil {
			r.Logger.Warn("policy.reconciler.reenqueue_failed",
				"asset", assetName, "column", col, "error", err.Error())
			return
		}
		rep.Pushed++
	}
}
