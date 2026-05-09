package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/lineage/impact"
	"github.com/kanpon/data-governance/internal/lineage/openlineage"

	_ "github.com/mattn/go-sqlite3"
)

// mockTranslator is a testable Translator implementation.
type mockTranslator struct {
	runEvents   []openlineage.RunEvent
	returnError error
}

func (m *mockTranslator) TranslateRun(_ context.Context, _ uuid.UUID) (openlineage.RunEvent, error) {
	if m.returnError != nil {
		return openlineage.RunEvent{}, m.returnError
	}
	if len(m.runEvents) > 0 {
		return m.runEvents[0], nil
	}
	return openlineage.RunEvent{}, nil
}

func (m *mockTranslator) TranslateAsset(_ context.Context, _ string, _ time.Time) ([]openlineage.RunEvent, error) {
	if m.returnError != nil {
		return nil, m.returnError
	}
	return m.runEvents, nil
}

// mockImpactDB implements lineageq.DBTX by always returning empty results.
// Tests that call Analyze with real DB will use a real connection (integration);
// tests focusing on handler validation use the mock path that never reaches DB.
type mockDB struct{}

func (m *mockDB) Exec(_ context.Context, _ string, _ ...interface{}) (interface{}, error) {
	panic("mockDB.Exec should not be called")
}

func lineageDepsWithMockTranslator(events []openlineage.RunEvent) Deps {
	return Deps{
		OLTranslator: &mockTranslator{runEvents: events},
		LineageDB:    nil, // unused for export tests
	}
}

func TestImpact_AssetRequired(t *testing.T) {
	deps := Deps{}
	req := httptest.NewRequest("GET", "/v1/lineage/impact", nil)
	rec := httptest.NewRecorder()
	impactHandler(deps)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "asset_required") {
		t.Errorf("expected 'asset_required': %s", rec.Body.String())
	}
}

func TestImpact_DepthExceeded(t *testing.T) {
	deps := Deps{}
	req := httptest.NewRequest("GET", "/v1/lineage/impact?asset=A&depth=99", nil)
	rec := httptest.NewRecorder()
	impactHandler(deps)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "depth_exceeded") {
		t.Errorf("expected 'depth_exceeded': %s", rec.Body.String())
	}
}

func TestImpact_InvalidDirection(t *testing.T) {
	deps := Deps{}
	req := httptest.NewRequest("GET", "/v1/lineage/impact?asset=A&direction=sideways", nil)
	rec := httptest.NewRecorder()
	impactHandler(deps)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_direction") {
		t.Errorf("expected 'invalid_direction': %s", rec.Body.String())
	}
}

func TestImpact_InvalidAssetName(t *testing.T) {
	deps := Deps{}
	// Asset name with characters outside the allowed set (spaces, special chars).
	// URL-encode the query parameter to ensure the full string is received.
	req := httptest.NewRequest("GET", "/v1/lineage/impact?asset=bad%20asset%21", nil)
	rec := httptest.NewRecorder()
	impactHandler(deps)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_asset_name") {
		t.Errorf("expected 'invalid_asset_name': %s", rec.Body.String())
	}
}

func TestImpact_OK(t *testing.T) {
	// Impact handler validation tests only — we verify the handler correctly passes
	// validation (returns non-400). The actual Analyze call needs a real pgx DB;
	// we use integration tests for that. Here we confirm valid params pass all guards.

	// We test with depth=26 which is still caught at the handler level (not Analyze).
	// depth=25 is valid at handler, passes to Analyze, which would panic with nil DB.
	// Use depth=26 to test the handler's own depth cap (returns 400).
	deps := Deps{}
	req := httptest.NewRequest("GET", "/v1/lineage/impact?asset=valid_asset&direction=downstream&depth=26", nil)
	rec := httptest.NewRecorder()
	impactHandler(deps)(rec, req)

	// depth=26 > MaxDepth(25) → 400 depth_exceeded.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for depth=26, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "depth_exceeded") {
		t.Errorf("expected 'depth_exceeded': %s", rec.Body.String())
	}

	// Test that depth=25 (at the cap) passes handler validation.
	// With nil LineageDB, handler returns 503 (not 400) — validation passed.
	req2 := httptest.NewRequest("GET", "/v1/lineage/impact?asset=valid_asset&direction=downstream&depth=25", nil)
	rec2 := httptest.NewRecorder()
	impactHandler(deps)(rec2, req2)

	// Should not be 400 (handler validation passed). 503 = nil DB sentinel.
	if rec2.Code == http.StatusBadRequest {
		t.Errorf("got unexpected 400 for valid params (depth=25): %s", rec2.Body.String())
	}
	if rec2.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for nil LineageDB, got %d", rec2.Code)
	}
}

func TestExport_UnsupportedFormat(t *testing.T) {
	deps := lineageDepsWithMockTranslator(nil)
	req := httptest.NewRequest("GET", "/v1/lineage/export?asset=A&format=invalid", nil)
	rec := httptest.NewRecorder()
	exportLineageHandler(deps)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unsupported_format") {
		t.Errorf("expected 'unsupported_format': %s", rec.Body.String())
	}
}

func TestExport_AssetRequired(t *testing.T) {
	deps := lineageDepsWithMockTranslator(nil)
	req := httptest.NewRequest("GET", "/v1/lineage/export?format=openlineage", nil)
	rec := httptest.NewRecorder()
	exportLineageHandler(deps)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "asset_required") {
		t.Errorf("expected 'asset_required': %s", rec.Body.String())
	}
}

func TestExport_OK(t *testing.T) {
	events := []openlineage.RunEvent{
		{
			EventType: "COMPLETE",
			EventTime: time.Now().UTC().Format(time.RFC3339),
			Run:       openlineage.OLRun{RunID: uuid.New().String()},
			Job:       openlineage.OLJob{Namespace: "test", Name: "asset_a"},
			Inputs:    []openlineage.OLDataset{{Namespace: "test", Name: "src"}},
			Outputs:   []openlineage.OLDataset{{Namespace: "test", Name: "asset_a"}},
			Producer:  openlineage.Producer,
			SchemaURL: openlineage.SchemaURL,
		},
	}
	deps := lineageDepsWithMockTranslator(events)
	req := httptest.NewRequest("GET", "/v1/lineage/export?asset=asset_a&since=2026-01-01T00:00:00Z&format=openlineage", nil)
	rec := httptest.NewRecorder()
	exportLineageHandler(deps)(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var result []openlineage.RunEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("expected 1 event, got %d", len(result))
	}
	if result[0].EventType != "COMPLETE" {
		t.Errorf("EventType = %q, want COMPLETE", result[0].EventType)
	}
	if result[0].Producer != openlineage.Producer {
		t.Errorf("Producer = %q, want %q", result[0].Producer, openlineage.Producer)
	}
}

// Compile-time assertion that impact.MaxDepth constant is accessible.
var _ = impact.MaxDepth
