package openlineage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/storage/ent/enttest"

	_ "github.com/mattn/go-sqlite3"
)

// openTestTranslator creates an in-memory SQLite ent client and a DefaultTranslator.
func openTestTranslator(t *testing.T) *DefaultTranslator {
	t.Helper()
	client := enttest.Open(t, "sqlite3", "file:oltrans?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	return NewDefault(client, "test-ns")
}

// seedSucceededRun inserts a run row in state "succeeded" and returns its ID.
func seedSucceededRun(t *testing.T, tr *DefaultTranslator, asset string, startedAt, finishedAt time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	run, err := tr.ent.Run.Create().
		SetAssetName(asset).
		SetState("succeeded").
		SetTrigger("manual").
		SetQueuedAt(startedAt.Add(-time.Second)).
		SetStartedAt(startedAt).
		SetFinishedAt(finishedAt).
		Save(ctx)
	if err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return run.ID
}

// seedAssetEdge inserts an asset edge.
func seedAssetEdge(t *testing.T, tr *DefaultTranslator, from, to string, firstSeenAt time.Time, supersededAt *time.Time) {
	t.Helper()
	creator := tr.ent.AssetEdge.Create().
		SetFromAsset(from).
		SetToAsset(to).
		SetCodeHashFirst("h1").
		SetCodeHashLatest("h1").
		SetFirstSeenRunID(uuid.New()).
		SetFirstSeenAt(firstSeenAt).
		SetLastSeenRunID(uuid.New()).
		SetLastSeenAt(firstSeenAt)
	if supersededAt != nil {
		creator = creator.SetSupersededAt(*supersededAt)
	}
	if _, err := creator.Save(context.Background()); err != nil {
		t.Fatalf("seed edge %s->%s: %v", from, to, err)
	}
}

// seedColumnEdge inserts a column edge.
func seedColumnEdge(t *testing.T, tr *DefaultTranslator, fromAsset, fromCol, toAsset, toCol string, firstSeenAt time.Time) {
	t.Helper()
	if _, err := tr.ent.ColumnEdge.Create().
		SetFromAsset(fromAsset).
		SetFromColumn(fromCol).
		SetToAsset(toAsset).
		SetToColumn(toCol).
		SetCodeHashFirst("h1").
		SetCodeHashLatest("h1").
		SetFirstSeenRunID(uuid.New()).
		SetFirstSeenAt(firstSeenAt).
		SetLastSeenRunID(uuid.New()).
		SetLastSeenAt(firstSeenAt).
		Save(context.Background()); err != nil {
		t.Fatalf("seed column edge: %v", err)
	}
}

func TestTranslateRun_OK(t *testing.T) {
	tr := openTestTranslator(t)
	now := time.Now().UTC().Truncate(time.Second)
	runID := seedSucceededRun(t, tr, "asset_out", now.Add(-10*time.Second), now)
	// Seed 2 upstream edges.
	seedAssetEdge(t, tr, "asset_in_1", "asset_out", now.Add(-20*time.Second), nil)
	seedAssetEdge(t, tr, "asset_in_2", "asset_out", now.Add(-20*time.Second), nil)

	ev, err := tr.TranslateRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("TranslateRun: %v", err)
	}

	if ev.EventType != "COMPLETE" {
		t.Errorf("EventType = %q, want COMPLETE", ev.EventType)
	}
	if ev.Run.RunID != runID.String() {
		t.Errorf("RunID = %q, want %q", ev.Run.RunID, runID)
	}
	if ev.Job.Name != "asset_out" {
		t.Errorf("Job.Name = %q, want asset_out", ev.Job.Name)
	}
	if len(ev.Inputs) != 2 {
		t.Errorf("len(Inputs) = %d, want 2", len(ev.Inputs))
	}
	if len(ev.Outputs) != 1 {
		t.Errorf("len(Outputs) = %d, want 1", len(ev.Outputs))
	}
	if ev.Outputs[0].Name != "asset_out" {
		t.Errorf("Outputs[0].Name = %q, want asset_out", ev.Outputs[0].Name)
	}
}

func TestTranslateRun_NoColumnEdges(t *testing.T) {
	tr := openTestTranslator(t)
	now := time.Now().UTC().Truncate(time.Second)
	runID := seedSucceededRun(t, tr, "asset_nocol", now.Add(-10*time.Second), now)
	seedAssetEdge(t, tr, "src", "asset_nocol", now.Add(-20*time.Second), nil)

	ev, err := tr.TranslateRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("TranslateRun: %v", err)
	}

	// No column edges: outputs[0].facets should be nil or empty.
	if ev.Outputs[0].Facets != nil {
		// Only a columnLineage key is expected — if present, fields must be populated.
		if cl, ok := ev.Outputs[0].Facets["columnLineage"]; ok {
			t.Errorf("unexpected columnLineage facet with no column edges: %v", cl)
		}
	}
}

func TestTranslateRunPointInTime(t *testing.T) {
	tr := openTestTranslator(t)
	// Run happened at T=100s.
	runStart := time.Now().UTC().Add(-100 * time.Second).Truncate(time.Second)
	runFinish := runStart.Add(5 * time.Second)
	runID := seedSucceededRun(t, tr, "asset_pt", runStart, runFinish)

	// Edge existed before run start.
	edgeStart := runStart.Add(-20 * time.Second)
	// Edge was superseded at T=200s (AFTER the run finished).
	supersededAt := runFinish.Add(100 * time.Second)
	seedAssetEdge(t, tr, "src_pt", "asset_pt", edgeStart, &supersededAt)

	ev, err := tr.TranslateRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("TranslateRun: %v", err)
	}

	// The edge was active at run.StartedAt (superseded_at > run.StartedAt),
	// so it MUST appear in Inputs even though it was retired later.
	if len(ev.Inputs) != 1 {
		t.Errorf("expected 1 input (point-in-time edge), got %d", len(ev.Inputs))
	}
	if len(ev.Inputs) > 0 && ev.Inputs[0].Name != "src_pt" {
		t.Errorf("Input[0].Name = %q, want src_pt", ev.Inputs[0].Name)
	}
}

func TestTranslateAsset_FilterSince(t *testing.T) {
	tr := openTestTranslator(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Two runs: one old, one recent.
	seedSucceededRun(t, tr, "asset_since", now.Add(-2*time.Hour), now.Add(-2*time.Hour+5*time.Second))
	seedSucceededRun(t, tr, "asset_since", now.Add(-30*time.Minute), now.Add(-30*time.Minute+5*time.Second))

	// since = 1 hour ago: only the recent run should be returned.
	since := now.Add(-1 * time.Hour)
	events, err := tr.TranslateAsset(context.Background(), "asset_since", since)
	if err != nil {
		t.Fatalf("TranslateAsset: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("expected 1 event (filtered by since), got %d", len(events))
	}
}

func TestTranslateAsset_OrdersByFinishedAt(t *testing.T) {
	tr := openTestTranslator(t)
	now := time.Now().UTC().Truncate(time.Second)

	// Insert in reverse order.
	seedSucceededRun(t, tr, "asset_ord", now.Add(-30*time.Minute), now.Add(-30*time.Minute+5*time.Second))
	seedSucceededRun(t, tr, "asset_ord", now.Add(-1*time.Hour), now.Add(-1*time.Hour+5*time.Second))

	events, err := tr.TranslateAsset(context.Background(), "asset_ord", time.Time{})
	if err != nil {
		t.Fatalf("TranslateAsset: %v", err)
	}
	if len(events) < 2 {
		t.Fatalf("expected >= 2 events, got %d", len(events))
	}
	// First event should have the earlier finished_at.
	t1, _ := time.Parse(time.RFC3339, events[0].EventTime)
	t2, _ := time.Parse(time.RFC3339, events[1].EventTime)
	if t1.After(t2) {
		t.Errorf("events not ordered by finished_at ASC: t1=%v t2=%v", t1, t2)
	}
}

func TestRunEvent_ProducerAndSchemaURL(t *testing.T) {
	tr := openTestTranslator(t)
	now := time.Now().UTC().Truncate(time.Second)
	runID := seedSucceededRun(t, tr, "asset_prod", now.Add(-10*time.Second), now)

	ev, err := tr.TranslateRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("TranslateRun: %v", err)
	}

	if ev.Producer != Producer {
		t.Errorf("Producer = %q, want %q", ev.Producer, Producer)
	}
	if ev.SchemaURL != SchemaURL {
		t.Errorf("SchemaURL = %q, want %q", ev.SchemaURL, SchemaURL)
	}

	// Verify JSON output has the correct fields.
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if m["producer"] != Producer {
		t.Errorf("JSON producer = %v, want %q", m["producer"], Producer)
	}
	if m["schemaURL"] != SchemaURL {
		t.Errorf("JSON schemaURL = %v, want %q", m["schemaURL"], SchemaURL)
	}
}
