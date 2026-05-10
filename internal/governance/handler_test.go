package governance_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/auth"
	"github.com/kanpon/data-governance/internal/governance"
)

// minimalEnforcer returns a Casbin enforcer with the minimum policy set the
// governance handler tests need (data-engineer + governance + admin policies).
// allowed=false means no policies are seeded → 403 path exercised.
func minimalEnforcer(t *testing.T, allowed bool) *casbin.Enforcer {
	t.Helper()
	cfg := `
[request_definition]
r = sub, obj, act
[policy_definition]
p = sub, obj, act
[role_definition]
g = _, _
[policy_effect]
e = some(where (p.eft == allow))
[matchers]
m = (g(r.sub, p.sub) || r.sub == p.sub) && keyMatch(r.obj, p.obj) && r.act == p.act
`
	mdl, err := model.NewModelFromString(cfg)
	require.NoError(t, err)
	e, err := casbin.NewEnforcer(mdl)
	require.NoError(t, err)
	if allowed {
		_, _ = e.AddPolicy("role:data-engineer", "/governance/submit", "write")
		_, _ = e.AddPolicy("role:governance", "/governance/reviews/*", "write")
		_, _ = e.AddPolicy("role:admin", "/governance/reviews/*", "manage")
	}
	return e
}

// fakePrincipalMW injects a Principal with the given role.
func fakePrincipalMW(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.ContextWithPrincipal(r.Context(), auth.Principal{
				UserID: uuid.New(),
				Role:   role,
			})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// fakeAssetLookup satisfies governance.AssetLookup with a single asset.
type fakeAssetLookup struct{ a *asset.Asset }

func (f *fakeAssetLookup) Get(name string) (*asset.Asset, error) {
	if f.a == nil || f.a.Name() != name {
		return nil, nil
	}
	return f.a, nil
}

// ===== Tests =====

// TestSubmitHandler_201_AutoApprovedPath
func TestSubmitHandler_201_AutoApprovedPath(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_clean_h").Connector("c").Materialize(noop).Build()
	require.NoError(t, err)
	_ = seedUser(t, db, "engineer-h@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())

	r := chi.NewRouter()
	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow:    w,
		Enforcer:    minimalEnforcer(t, true),
		AuthMW:      fakePrincipalMW("data-engineer"),
		AssetLookup: &fakeAssetLookup{a: a},
	})

	body, _ := json.Marshal(map[string]any{"asset": a.Name(), "code_hash": a.CodeHash()})
	req := httptest.NewRequest(http.MethodPost, "/governance/submit", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var res governance.SubmitResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, "auto_approved", res.Status)
}

// TestSubmitHandler_201_HumanReviewPath
func TestSubmitHandler_201_HumanReviewPath(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	w, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_human_h").Connector("c").Materialize(noop).RequireHumanReview().Build()
	require.NoError(t, err)
	_ = seedUser(t, db, "engineer-h2@example.com")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())

	r := chi.NewRouter()
	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow:    w,
		Enforcer:    minimalEnforcer(t, true),
		AuthMW:      fakePrincipalMW("data-engineer"),
		AssetLookup: &fakeAssetLookup{a: a},
	})

	body, _ := json.Marshal(map[string]any{"asset": a.Name(), "code_hash": a.CodeHash()})
	req := httptest.NewRequest(http.MethodPost, "/governance/submit", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
	var res governance.SubmitResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &res))
	require.Equal(t, "in_review", res.Status)
}

// TestSubmitHandler_403_OnInsufficientRole
func TestSubmitHandler_403_OnInsufficientRole(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	w, _, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_403").Connector("c").Materialize(noop).Build()
	require.NoError(t, err)

	r := chi.NewRouter()
	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow:    w,
		Enforcer:    minimalEnforcer(t, true),
		AuthMW:      fakePrincipalMW("viewer"), // viewer has no policy
		AssetLookup: &fakeAssetLookup{a: a},
	})

	body, _ := json.Marshal(map[string]any{"asset": a.Name(), "code_hash": a.CodeHash()})
	req := httptest.NewRequest(http.MethodPost, "/governance/submit", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

// TestRejectHandler_400_OnEmptyComment — most important fast-feedback test.
// This test does NOT touch Postgres because the handler validates the comment
// BEFORE invoking the workflow.
func TestRejectHandler_400_OnEmptyComment(t *testing.T) {
	r := chi.NewRouter()
	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow: governance.NewWorkflow(nil, governance.NewResolver(nil, nil), nil, nil),
		Enforcer: minimalEnforcer(t, true),
		AuthMW:   fakePrincipalMW("governance"),
	})
	body, _ := json.Marshal(map[string]any{"comment": ""})
	req := httptest.NewRequest(http.MethodPost,
		"/governance/reviews/"+uuid.New().String()+"/reject",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "comment is required")
}

// TestApproveHandler_200_FlipsToActive
func TestApproveHandler_200_FlipsToActive(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	wf, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_approve_h").Connector("c").
		Materialize(noop).RequireHumanReview().Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@x")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())
	res, err := wf.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)

	r := chi.NewRouter()
	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow: wf,
		Enforcer: minimalEnforcer(t, true),
		AuthMW:   fakePrincipalMW("governance"),
	})

	body, _ := json.Marshal(map[string]any{"comment": "ok"})
	req := httptest.NewRequest(http.MethodPost,
		"/governance/reviews/"+res.ReviewID.String()+"/approve",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "active", readGovernanceState(t, db, a.Name(), a.CodeHash()))
}

// TestRejectHandler_200_FlipsToRejected
func TestRejectHandler_200_FlipsToRejected(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	wf, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_reject_h").Connector("c").
		Materialize(noop).RequireHumanReview().Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@y")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())
	res, err := wf.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)

	r := chi.NewRouter()
	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow: wf,
		Enforcer: minimalEnforcer(t, true),
		AuthMW:   fakePrincipalMW("governance"),
	})
	body, _ := json.Marshal(map[string]any{"comment": "fix the schema"})
	req := httptest.NewRequest(http.MethodPost,
		"/governance/reviews/"+res.ReviewID.String()+"/reject",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Equal(t, "rejected", readGovernanceState(t, db, a.Name(), a.CodeHash()))
}

// TestReassignHandler_200
func TestReassignHandler_200(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	wf, db, cleanup := setupWorkflow(t)
	defer cleanup()
	a, err := asset.New("orders_re_h").Connector("c").
		Materialize(noop).Reviewers("old-team").RequireHumanReview().Build()
	require.NoError(t, err)
	submitter := seedUser(t, db, "engineer@z")
	seedAssetVersion(t, db, a.Name(), a.CodeHash())
	res, err := wf.Submit(context.Background(), a.Name(), a.CodeHash(), submitter, nil, a, nil, "")
	require.NoError(t, err)

	r := chi.NewRouter()
	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow: wf,
		Enforcer: minimalEnforcer(t, true),
		AuthMW:   fakePrincipalMW("admin"),
	})
	body, _ := json.Marshal(map[string]any{"new_reviewers": []string{"team-x", "team-y"}})
	req := httptest.NewRequest(http.MethodPost,
		"/governance/reviews/"+res.ReviewID.String()+"/reassign",
		bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
}

// TestStatusHandler_200
func TestStatusHandler_200(t *testing.T) {
	if testing.Short() {
		t.Skip("requires postgres testcontainer")
	}
	wf, _, cleanup := setupWorkflow(t)
	defer cleanup()

	r := chi.NewRouter()
	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow: wf,
		Enforcer: minimalEnforcer(t, true),
		AuthMW:   fakePrincipalMW("data-engineer"),
	})
	req := httptest.NewRequest(http.MethodGet, "/governance/status", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}
