package quality

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/event"
)

// DefaultRuleTimeout is the default per-rule context timeout (Pitfall #10).
// SQLAssertion may set a longer timeout via NewEvaluator's argument.
const DefaultRuleTimeout = 30 * time.Second

// EventTxAppender is the optional event-writer hook that emits inside the
// caller-supplied *sql.Tx so the event row commits atomically with
// quality_results (avoiding the partial-commit window that plain
// event.Writer.Append leaves).
//
// Implementations may simply forward to event.Writer.Append when atomicity
// is not required (e.g., in-memory test writers).
type EventTxAppender interface {
	AppendTx(ctx context.Context, tx *sql.Tx, evt event.Event) error
}

// Dispatch is the optional notification dispatcher hook called once per
// failing rule + once per evaluated run (when worst != "passed"). Nil is OK.
type Dispatch interface {
	OnQualityFailed(ctx context.Context, tx *sql.Tx, runID uuid.UUID, asset, rule string, payload map[string]any) error
}

// Evaluator runs all of an asset's QualityRules inside the executor's
// per-step transaction (Plan 05-05 D-19).
//
//   - Persists one quality_results row per rule.
//   - Emits quality.rule_passed | rule_failed | rule_error events to the same tx.
//   - Updates runs.run_quality_status to the worst outcome.
//   - Optionally dispatches notifications via the configured Dispatch.
type Evaluator struct {
	events     event.Writer
	eventsTx   EventTxAppender // optional — nil falls back to events.Append (post-commit)
	store      *Store
	timeout    time.Duration
	dispatcher Dispatch // optional
}

// NewEvaluator constructs an Evaluator. timeout is the per-rule default; if
// zero, DefaultRuleTimeout (30s) is used.
func NewEvaluator(events event.Writer, store *Store, timeout time.Duration) *Evaluator {
	if timeout <= 0 {
		timeout = DefaultRuleTimeout
	}
	e := &Evaluator{events: events, store: store, timeout: timeout}
	// If the writer also implements EventTxAppender, plumb it for atomicity.
	if tx, ok := events.(EventTxAppender); ok {
		e.eventsTx = tx
	}
	return e
}

// WithDispatcher returns a copy of the Evaluator with the supplied dispatcher
// wired in (Plan 05-05 Task 2 hook). Returning a copy keeps NewEvaluator's
// signature stable.
func (e *Evaluator) WithDispatcher(d Dispatch) *Evaluator {
	cp := *e
	cp.dispatcher = d
	return &cp
}

// Evaluate runs every QualityRule attached to the asset against the supplied
// connector. Outcomes are persisted to quality_results inside the supplied
// tx; runs.run_quality_status is updated to the worst result; per-rule and
// aggregate events are emitted to event_log.
//
// Returns the aggregate worst status: "passed" | "failed" | "error" | "skipped".
func (e *Evaluator) Evaluate(ctx context.Context, tx *sql.Tx, runID uuid.UUID,
	a *asset.Asset, conn connector.Connector, ref connector.AssetRef,
) (string, error) {
	rules := a.QualityRules()
	if len(rules) == 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE runs SET run_quality_status='skipped' WHERE id=$1`, runID); err != nil {
			return "", fmt.Errorf("quality.Evaluate: update run_quality_status (skipped): %w", err)
		}
		return "skipped", nil
	}

	qa, hasQA := conn.(connector.QueryAggregate)
	worst := "passed"

	for _, r := range rules {
		var res asset.QualityResult
		if !hasQA {
			res = asset.QualityResult{
				Status:       "error",
				ErrorMessage: "connector does not support aggregate queries",
			}
		} else {
			ctxRule, cancel := context.WithTimeout(ctx, e.timeout)
			adapter := &queryAdapter{
				qa:      qa,
				ref:     ref,
				table:   connector.QualifiedTable(ref),
				timeout: e.timeout,
			}
			result, err := r.Evaluate(ctxRule, adapter)
			cancel()
			res = result
			if err != nil && res.Status == "" {
				res = asset.QualityResult{Status: "error", ErrorMessage: err.Error()}
			}
			if errors.Is(ctxRule.Err(), context.DeadlineExceeded) && res.Status == "passed" {
				// safety net — if a rule reported pass but ctx already expired,
				// treat as error. Should rarely fire (Evaluate handles its own errors).
				res = asset.QualityResult{Status: "error", ErrorMessage: "rule timeout"}
			}
		}

		// Persist the per-rule row.
		if err := e.store.Persist(ctx, tx, runID, r.Name(), r.Type(), res); err != nil {
			return "", fmt.Errorf("quality.Evaluate: persist %q: %w", r.Name(), err)
		}

		// Emit per-rule event.
		var evType event.EventType
		switch res.Status {
		case "failed":
			evType = event.EventTypeQualityRuleFailed
			if worst != "error" {
				worst = "failed"
			}
		case "error":
			evType = event.EventTypeQualityRuleError
			worst = "error"
		default:
			evType = event.EventTypeQualityRulePassed
		}
		evt := event.Event{
			Type:         evType,
			ResourceType: "run",
			ResourceID:   runID.String(),
			Payload: event.QualityRulePayload{
				Asset:         a.Name(),
				Rule:          r.Name(),
				Type:          r.Type(),
				Status:        res.Status,
				MeasuredValue: res.MeasuredValue,
				Threshold:     res.Threshold,
				Error:         res.ErrorMessage,
			},
		}
		if e.eventsTx != nil {
			if err := e.eventsTx.AppendTx(ctx, tx, evt); err != nil {
				return "", fmt.Errorf("quality.Evaluate: append %s: %w", evType, err)
			}
		} else {
			// Best-effort post-tx append — events are observability, not coordination.
			_ = e.events.Append(ctx, evt)
		}

		// Dispatcher hook for failing rules (Task 2 wiring).
		if res.Status == "failed" && e.dispatcher != nil {
			payload := map[string]any{
				"asset":          a.Name(),
				"rule":           r.Name(),
				"rule_type":      r.Type(),
				"measured_value": res.MeasuredValue,
				"threshold":      res.Threshold,
			}
			if err := e.dispatcher.OnQualityFailed(ctx, tx, runID, a.Name(), r.Name(), payload); err != nil {
				return "", fmt.Errorf("quality.Evaluate: dispatch %q: %w", r.Name(), err)
			}
		}
	}

	// Update aggregate run_quality_status + emit run-level event.
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET run_quality_status=$1 WHERE id=$2`, worst, runID); err != nil {
		return "", fmt.Errorf("quality.Evaluate: update run_quality_status: %w", err)
	}
	evt := event.Event{
		Type:         event.EventTypeQualityRunEvaluated,
		ResourceType: "run",
		ResourceID:   runID.String(),
		Payload: event.QualityRunEvaluatedPayload{
			Asset:     a.Name(),
			Worst:     worst,
			RuleCount: len(rules),
		},
	}
	if e.eventsTx != nil {
		if err := e.eventsTx.AppendTx(ctx, tx, evt); err != nil {
			return "", fmt.Errorf("quality.Evaluate: append run_evaluated: %w", err)
		}
	} else {
		_ = e.events.Append(ctx, evt)
	}
	return worst, nil
}

// queryAdapter bridges connector.QueryAggregate → asset.QualityEvaluator.
type queryAdapter struct {
	qa      connector.QueryAggregate
	ref     connector.AssetRef
	table   string
	timeout time.Duration
}

func (a *queryAdapter) QueryAggregate(ctx context.Context, sqlText string) (connector.AggregateRow, error) {
	return a.qa.QueryAggregate(ctx, a.ref, sqlText)
}

func (a *queryAdapter) AssetTable() string { return a.table }

func (a *queryAdapter) Timeout() time.Duration { return a.timeout }
