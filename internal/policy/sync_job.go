package policy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kanpon/data-governance/internal/audit"
	"github.com/kanpon/data-governance/internal/connector"
)

// PolicySyncJobKind is the canonical River job kind name (also used by the
// no-River unit-test path for log filtering).
const PolicySyncJobKind = "policy_sync"

// MaxSyncAttempts is the upper bound for River retries before the worker
// emits masking.sync_failed and surrenders. Matches RESEARCH.md plan
// (3 attempts; exponential backoff is applied by River InsertOpts).
const MaxSyncAttempts = 3

// ConnectorResolver locates the warehouse connector for a given asset name.
// Production wiring delegates to the connector.Registry + asset.Registry
// lookup chain; tests inject a stub that returns a synthetic connector.
type ConnectorResolver interface {
	// ResolveByAsset returns (connector, AssetRef) for the named asset, or
	// (nil, _, error) if the asset is unknown.
	ResolveByAsset(ctx context.Context, assetName string) (connector.Connector, connector.AssetRef, error)
}

// AuditWriter is the abstract audit-chain entry point used by the worker
// when a permanent sync failure must be recorded. Production wiring opens a
// fresh transaction; tests inject a recording stub.
type AuditWriter interface {
	WritePermanentFailure(ctx context.Context, entry audit.Entry) error
}

// PolicySyncWorker is the engine that consumes PolicySyncArgs jobs. It is
// deliberately decoupled from the river runtime so it can be tested
// directly via Work(ctx, args, attempt) — the cmd/platform wiring wraps
// this in a real river.Worker[PolicySyncArgs] in production.
type PolicySyncWorker struct {
	Store      *Store
	Connectors ConnectorResolver
	Audit      AuditWriter
	Logger     *slog.Logger
}

// NewPolicySyncWorker wires a worker. Logger may be nil — the default logger is used.
func NewPolicySyncWorker(store *Store, conns ConnectorResolver, audit AuditWriter) *PolicySyncWorker {
	return &PolicySyncWorker{
		Store:      store,
		Connectors: conns,
		Audit:      audit,
		Logger:     slog.Default(),
	}
}

// Work executes one sync attempt for (asset, column). attempt is the 1-based
// attempt counter River supplies — when attempt == MaxSyncAttempts and the
// connector errors, the worker writes masking.sync_failed before returning.
//
// Returns nil when the underlying ApplyMaskingPolicy succeeds OR the asset's
// connector lacks MaskingProvisioner (in-pipeline path); returns the
// connector / store error otherwise so River can retry.
func (w *PolicySyncWorker) Work(ctx context.Context, args PolicySyncArgs, attempt int) error {
	logger := w.Logger
	if logger == nil {
		logger = slog.Default()
	}
	eff, err := w.Store.Resolve(ctx, args.Asset, args.Column)
	if err != nil {
		if errors.Is(err, ErrPolicyNotFound) {
			logger.Info("policy.sync.skip_no_policy",
				"asset", args.Asset, "column", args.Column)
			return nil
		}
		return fmt.Errorf("policy_sync resolve %s.%s: %w", args.Asset, args.Column, err)
	}

	conn, ref, err := w.Connectors.ResolveByAsset(ctx, eff.Asset)
	if err != nil {
		return fmt.Errorf("policy_sync resolve connector %s: %w", eff.Asset, err)
	}

	mp, ok := conn.(connector.MaskingProvisioner)
	if !ok {
		// Non-warehouse target — Plan 05-03 in-pipeline takes over.
		logger.Info("policy.sync.in_pipeline",
			"asset", eff.Asset, "column", eff.Column,
			"connector", connectorName(ctx, conn))
		_ = w.Store.SetEnforcementMode(ctx, eff.Asset, eff.Column, "in-pipeline")
		_ = w.Store.SetSyncStatus(ctx, eff.Asset, eff.Column, "synced")
		return nil
	}

	policy := connector.ColumnPolicy{
		Column:     eff.Column,
		MaskType:   eff.Mask,
		AllowRoles: eff.AllowRoles,
	}
	_ = w.Store.SetSyncStatus(ctx, eff.Asset, eff.Column, "syncing")
	if err := mp.ApplyMaskingPolicy(ctx, ref, policy); err != nil {
		// Last attempt? Write masking.sync_failed and tag column failed.
		if attempt >= MaxSyncAttempts {
			logger.Error("policy.sync.failed_permanent",
				"asset", eff.Asset, "column", eff.Column,
				"error", err.Error(), "attempt", attempt)
			_ = w.Store.SetSyncStatus(ctx, eff.Asset, eff.Column, "failed")
			if w.Audit != nil {
				_ = w.Audit.WritePermanentFailure(ctx, audit.Entry{
					EventType:    audit.AuditMaskingSyncFailed,
					OccurredAt:   time.Now().UTC(),
					ResourceType: "column_policy",
					ResourceID:   eff.Asset + "." + eff.Column,
					Payload: map[string]any{
						"asset":     eff.Asset,
						"column":    eff.Column,
						"connector": connectorName(ctx, conn),
						"error":     err.Error(),
						"attempts":  attempt,
					},
				})
			}
		}
		return fmt.Errorf("policy_sync apply %s.%s: %w", eff.Asset, eff.Column, err)
	}
	logger.Info("policy.sync.applied",
		"asset", eff.Asset, "column", eff.Column,
		"mask", string(eff.Mask), "connector", connectorName(ctx, conn))
	_ = w.Store.SetEnforcementMode(ctx, eff.Asset, eff.Column, "warehouse-native")
	_ = w.Store.SetSyncStatus(ctx, eff.Asset, eff.Column, "synced")
	return nil
}

// connectorName best-effort returns the connector identity for logging.
// Falls back to "unknown" on any error so the worker keeps running. Honours
// the caller's ctx (WR-08): a Ping issued during shutdown will not stall
// the goroutine waiting on an unresponsive warehouse.
func connectorName(ctx context.Context, c connector.Connector) string {
	if c == nil {
		return "<nil>"
	}
	resp, err := c.Ping(ctx, connector.PingRequest{})
	if err != nil || resp.ConnectorName == "" {
		return "unknown"
	}
	return resp.ConnectorName
}

// ----- Production AuditWriter using *sql.DB + audit.WriteEntry -----

// SQLAuditWriter is the production AuditWriter — opens a *sql.Tx on the
// supplied *sql.DB, writes the entry via audit.WriteEntry, commits. The
// tx is the standard hash-chain mutation path so the masking.sync_failed
// entry chains correctly with all other audit rows.
type SQLAuditWriter struct{ DB *sql.DB }

// WritePermanentFailure opens a tx, writes entry to the audit chain, commits.
func (w *SQLAuditWriter) WritePermanentFailure(ctx context.Context, entry audit.Entry) error {
	if w.DB == nil {
		return errors.New("policy: SQLAuditWriter has nil DB")
	}
	tx, err := w.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := audit.WriteEntry(ctx, tx, entry); err != nil {
		return err
	}
	return tx.Commit()
}
