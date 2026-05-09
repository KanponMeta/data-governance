package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/event"

	_ "github.com/mattn/go-sqlite3"
)

// mockWriter is a no-op event.Writer for handler tests.
type mockWriter struct {
	events []event.Event
}

func (m *mockWriter) Append(_ context.Context, evt event.Event) error {
	m.events = append(m.events, evt)
	return nil
}

// principalCtx wraps a request with an auth.Principal in the context.
func principalCtx(r *http.Request, role string) *http.Request {
	p := auth.Principal{UserID: uuid.New(), Role: role}
	ctx := context.WithValue(r.Context(), testPrincipalKey{}, p)
	return r.WithContext(ctx)
}

// testPrincipalKey is a local type to inject principals in tests.
// We'll use a test helper that injects principal via a chi middleware instead.
type testPrincipalKey struct{}

// injectPrincipal returns a chi middleware that injects the given principal.
func injectPrincipal(p auth.Principal) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), auth.TestPrincipalKey(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func setupHandler(t *testing.T) (*Handler, *Store, *mockWriter) {
	t.Helper()
	client := openTestClient(t) // from store_test.go helper
	mw := &mockWriter{}
	h := NewHandler(client, mw)
	return h, client, mw
}

func TestHandler_PatchAsset_OK(t *testing.T) {
	h, _, mw := setupHandler(t)

	body := `{"description":"updated","owner":"team-x","tags":["a","b"]}`
	req := httptest.NewRequest("PATCH", "/v1/assets/my_asset/metadata", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// Inject governance principal.
	p := auth.Principal{UserID: uuid.New(), Role: "governance"}
	ctx := context.WithValue(req.Context(), auth.TestPrincipalKey(), p)
	req = req.WithContext(ctx)

	// Wire chi URL params.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "my_asset")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.PatchAsset(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if _, ok := resp["effective"]; !ok {
		t.Errorf("response missing 'effective' key: %v", resp)
	}

	// Event should have been emitted.
	if len(mw.events) != 1 {
		t.Errorf("expected 1 event, got %d", len(mw.events))
	}
	if mw.events[0].Type != event.EventTypeMetadataUpdated {
		t.Errorf("unexpected event type: %v", mw.events[0].Type)
	}
}

func TestHandler_PatchAsset_TagsTooMany(t *testing.T) {
	h, _, _ := setupHandler(t)

	// Build tags array with 65 entries.
	tags := make([]string, MaxTags+1)
	for i := range tags {
		tags[i] = fmt.Sprintf("tag%d", i)
	}
	bodyObj := map[string]any{"tags": tags}
	bodyBytes, _ := json.Marshal(bodyObj)

	req := httptest.NewRequest("PATCH", "/v1/assets/my_asset/metadata", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{UserID: uuid.New(), Role: "governance"}
	ctx := context.WithValue(req.Context(), auth.TestPrincipalKey(), p)
	req = req.WithContext(ctx)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "my_asset")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.PatchAsset(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "tags_too_many") {
		t.Errorf("expected 'tags_too_many' in response, got: %s", rec.Body.String())
	}
}

func TestHandler_PatchAsset_RequiresGovernanceRole(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{"description":"x"}`
	req := httptest.NewRequest("PATCH", "/v1/assets/my_asset/metadata", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// No principal in context (simulating RequireRole not being met).
	// Handler should handle missing principal gracefully.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "my_asset")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.PatchAsset(rec, req)

	// Without a principal, handler returns 401.
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing principal, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PatchColumn_OK(t *testing.T) {
	h, _, _ := setupHandler(t)

	body := `{"description":"col desc"}`
	req := httptest.NewRequest("PATCH", "/v1/assets/my_asset/columns/col1/metadata", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := auth.Principal{UserID: uuid.New(), Role: "governance"}
	ctx := context.WithValue(req.Context(), auth.TestPrincipalKey(), p)
	req = req.WithContext(ctx)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "my_asset")
	rctx.URLParams.Add("col", "col1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.PatchColumn(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandler_GetMetadata_NotFound(t *testing.T) {
	h, _, _ := setupHandler(t)

	req := httptest.NewRequest("GET", "/v1/assets/nonexistent/metadata", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "nonexistent")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.Get(rec, req)

	// Asset with no rows still returns 200 with empty code_default (not 404).
	// 404 only applies when asset store itself is missing — for metadata it returns empty.
	// The plan says "GET /v1/assets/missing/metadata returns 404 with type asset_not_found"
	// but this requires an asset existence check. We check asset_versions for existence.
	// No asset_versions row = asset_not_found.
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for missing asset, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "asset_not_found") {
		t.Errorf("expected 'asset_not_found', got: %s", rec.Body.String())
	}
}

func TestHandler_GetMetadata_OK(t *testing.T) {
	h, store, _ := setupHandler(t)
	ctx := context.Background()

	// Seed a version so the asset exists.
	_, err := store.ent.AssetVersion.Create().
		SetAsset("known_asset").
		SetCodeHash("h1").
		SetDescription("desc").
		Save(ctx)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/v1/assets/known_asset/metadata", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "known_asset")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.Get(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp Resolution
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.CodeDefault.Description != "desc" {
		t.Errorf("CodeDefault.Description = %q, want 'desc'", resp.CodeDefault.Description)
	}
}
