// Package openlineage provides an in-house OpenLineage RunEvent translator (D-18).
// Zero external runtime dependencies — ThijsKoot/openlineage-go is NOT used
// (LOW credibility flag in CLAUDE.md; the RunEvent shape is simple enough to own).
// Output conforms to https://openlineage.io/spec/2-0-2/RunEvent.json.
package openlineage

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/storage/ent"
	"github.com/kanpon/data-governance/internal/storage/ent/assetedge"
	"github.com/kanpon/data-governance/internal/storage/ent/columnedge"
	runpkg "github.com/kanpon/data-governance/internal/storage/ent/run"
)

const (
	// Producer is the OpenLineage producer URI for this platform.
	Producer = "https://github.com/kanpon/data-governance"
	// SchemaURL is the OpenLineage RunEvent schema URI this output conforms to.
	SchemaURL = "https://openlineage.io/spec/2-0-2/RunEvent.json"
)

// RunEvent is an OpenLineage RunEvent (D-18 minimum required fields).
// Defined inline — no external OL dependency.
type RunEvent struct {
	EventType string      `json:"eventType"`  // always "COMPLETE" for past successful runs
	EventTime string      `json:"eventTime"`  // RFC 3339 UTC
	Run       OLRun       `json:"run"`
	Job       OLJob       `json:"job"`
	Inputs    []OLDataset `json:"inputs"`
	Outputs   []OLDataset `json:"outputs"`
	Producer  string      `json:"producer"`   // Producer constant
	SchemaURL string      `json:"schemaURL"`  // SchemaURL constant
}

// OLRun is the OpenLineage Run facet holder.
type OLRun struct {
	RunID  string         `json:"runId"`
	Facets map[string]any `json:"facets,omitempty"`
}

// OLJob is the OpenLineage Job identifier.
type OLJob struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Facets    map[string]any `json:"facets,omitempty"`
}

// OLDataset is an OpenLineage Dataset identifier (input or output).
type OLDataset struct {
	Namespace string         `json:"namespace"`
	Name      string         `json:"name"`
	Facets    map[string]any `json:"facets,omitempty"`
}

// Translator is the interface for converting internal runs to OpenLineage events.
// Defined as an interface for testability; DefaultTranslator is the production implementation.
type Translator interface {
	TranslateRun(ctx context.Context, runID uuid.UUID) (RunEvent, error)
	TranslateAsset(ctx context.Context, asset string, since time.Time) ([]RunEvent, error)
}

// DefaultTranslator is the production implementation of Translator.
// It queries the ent client for run and edge data.
type DefaultTranslator struct {
	ent       *ent.Client
	Namespace string // e.g. "platform.local"
}

// NewDefault creates a DefaultTranslator with the given ent client and namespace.
// If ns is empty, "platform" is used.
func NewDefault(c *ent.Client, ns string) *DefaultTranslator {
	if ns == "" {
		ns = "platform"
	}
	return &DefaultTranslator{ent: c, Namespace: ns}
}

// TranslateRun converts a single succeeded run to a RunEvent.
// Uses point-in-time edge predicates (D-15): edges are selected as of run.StartedAt,
// so edges retired after run completion still appear in the export for that run.
func (t *DefaultTranslator) TranslateRun(ctx context.Context, runID uuid.UUID) (RunEvent, error) {
	run, err := t.ent.Run.Query().Where(runpkg.IDEQ(runID)).Only(ctx)
	if err != nil {
		return RunEvent{}, fmt.Errorf("openlineage: load run %s: %w", runID, err)
	}
	if run.State != "succeeded" {
		return RunEvent{}, fmt.Errorf("openlineage: run %s state=%s, only succeeded runs export as COMPLETE", runID, run.State)
	}
	if run.FinishedAt == nil {
		return RunEvent{}, fmt.Errorf("openlineage: run %s has nil finished_at", runID)
	}

	// Point-in-time predicate (D-15): edges visible at run.StartedAt.
	// An edge retired between run completion and export STILL appears in this run's RunEvent.
	var runStarted time.Time
	if run.StartedAt != nil {
		runStarted = *run.StartedAt
	} else {
		runStarted = run.QueuedAt
	}

	// Inputs: upstream asset edges as of run start time.
	edges, err := t.ent.AssetEdge.Query().
		Where(
			assetedge.ToAssetEQ(run.AssetName),
			assetedge.FirstSeenAtLTE(runStarted),
			assetedge.Or(
				assetedge.SupersededAtIsNil(),
				assetedge.SupersededAtGT(runStarted),
			),
		).
		All(ctx)
	if err != nil {
		return RunEvent{}, fmt.Errorf("openlineage: load edges for run %s: %w", runID, err)
	}

	inputs := make([]OLDataset, 0, len(edges))
	for _, e := range edges {
		inputs = append(inputs, OLDataset{Namespace: t.Namespace, Name: e.FromAsset})
	}

	// Outputs: the run's asset is always the single output.
	outputs := []OLDataset{{Namespace: t.Namespace, Name: run.AssetName}}

	// columnLineage facet (D-18): column edges grouped by to_column.
	// Same point-in-time predicate as asset edges.
	colEdges, err := t.ent.ColumnEdge.Query().
		Where(
			columnedge.ToAssetEQ(run.AssetName),
			columnedge.FirstSeenAtLTE(runStarted),
			columnedge.Or(
				columnedge.SupersededAtIsNil(),
				columnedge.SupersededAtGT(runStarted),
			),
		).
		All(ctx)
	if err != nil {
		return RunEvent{}, fmt.Errorf("openlineage: load column edges for run %s: %w", runID, err)
	}

	if len(colEdges) > 0 {
		fields := map[string]any{}
		for _, ce := range colEdges {
			refs, _ := fields[ce.ToColumn].([]map[string]any)
			refs = append(refs, map[string]any{
				"namespace": t.Namespace,
				"name":      ce.FromAsset,
				"field":     ce.FromColumn,
			})
			fields[ce.ToColumn] = refs
		}
		outputs[0].Facets = map[string]any{
			"columnLineage": map[string]any{"fields": fields},
		}
	}

	return RunEvent{
		EventType: "COMPLETE",
		EventTime: run.FinishedAt.UTC().Format(time.RFC3339),
		Run:       OLRun{RunID: runID.String()},
		Job:       OLJob{Namespace: t.Namespace, Name: run.AssetName},
		Inputs:    inputs,
		Outputs:   outputs,
		Producer:  Producer,
		SchemaURL: SchemaURL,
	}, nil
}

// TranslateAsset returns RunEvents for all succeeded runs of the given asset
// with finished_at >= since, ordered by finished_at ASC.
// Individual run errors are skipped (logged implicitly by callers).
func (t *DefaultTranslator) TranslateAsset(ctx context.Context, asset string, since time.Time) ([]RunEvent, error) {
	q := t.ent.Run.Query().
		Where(
			runpkg.AssetNameEQ(asset),
			runpkg.StateEQ("succeeded"),
		)
	if !since.IsZero() {
		q = q.Where(runpkg.FinishedAtGTE(since))
	}
	runs, err := q.Order(runpkg.ByFinishedAt()).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("openlineage: list runs for %s: %w", asset, err)
	}

	out := make([]RunEvent, 0, len(runs))
	for _, r := range runs {
		ev, err := t.TranslateRun(ctx, r.ID)
		if err != nil {
			// Skip individual run failures; caller sees partial results.
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}
