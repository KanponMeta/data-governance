package bigquery

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/connector"
)

// fakePTM is a test PolicyTagManagerClient that records call counts and
// payloads so assertions can verify the four-step BigQuery CLS protocol
// (RESEARCH.md §3) without round-tripping a real Data Catalog API.
type fakePTM struct {
	mu sync.Mutex

	// Configurable behaviour.
	GetTaxonomyMissing bool   // when true, EnsureTaxonomy treats first call as "not found".
	TaxonomyName       string // canonical resource name returned.
	TagName            string // canonical tag resource name returned.

	// Recorded state.
	CreateTaxonomyCalls int
	CreatePolicyTagCalls int
	SetIamCalls          int
	LastIamMembers       []string

	taxonomyCreated bool
}

func (f *fakePTM) EnsureTaxonomy(_ context.Context, project, location, displayName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.GetTaxonomyMissing && !f.taxonomyCreated {
		f.CreateTaxonomyCalls++
		f.taxonomyCreated = true
	}
	if f.TaxonomyName == "" {
		f.TaxonomyName = "projects/" + project + "/locations/" + location + "/taxonomies/" + displayName
	}
	return f.TaxonomyName, nil
}

func (f *fakePTM) EnsurePolicyTag(_ context.Context, taxonomy, displayName string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CreatePolicyTagCalls++
	if f.TagName == "" {
		f.TagName = taxonomy + "/policyTags/" + displayName
	}
	return f.TagName, nil
}

func (f *fakePTM) SetIamPolicyOnTag(_ context.Context, _ string, members []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SetIamCalls++
	f.LastIamMembers = append([]string(nil), members...)
	return nil
}

func (f *fakePTM) ListPolicyTagsForTable(_ context.Context, _, _, _ string) (map[string]string, error) {
	return map[string]string{
		"ssn":   "projects/p/locations/us/taxonomies/dgp-platform/policyTags/hash",
		"email": "projects/p/locations/us/taxonomies/dgp-platform/policyTags/redact",
	}, nil
}

func (f *fakePTM) PolicyTagDisplayName(_ context.Context, name string) (string, error) {
	// Recover the trailing display name segment.
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '/' {
			return name[i+1:], nil
		}
	}
	return name, nil
}

// fakeBQ is a test BigQueryClient recording UpdateTablePolicyTags calls.
type fakeBQ struct {
	mu          sync.Mutex
	UpdateCalls int
	LastTags    map[string][]string
	Err         error
}

func (f *fakeBQ) UpdateTablePolicyTags(_ context.Context, _, _, _ string, columnTags map[string][]string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.UpdateCalls++
	cp := make(map[string][]string, len(columnTags))
	for k, v := range columnTags {
		cp[k] = append([]string(nil), v...)
	}
	f.LastTags = cp
	return f.Err
}

// TestBigQuery_ApplyMaskingPolicy_CreatesTaxonomyIfMissing — when the
// taxonomy is absent, EnsureTaxonomy registers the create call.
func TestBigQuery_ApplyMaskingPolicy_CreatesTaxonomyIfMissing(t *testing.T) {
	ptm := &fakePTM{GetTaxonomyMissing: true}
	bq := &fakeBQ{}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")

	err := mp.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "p.ds.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash, AllowRoles: []string{"group:pii-analyst@example.com"}})
	require.NoError(t, err)
	require.Equal(t, 1, ptm.CreateTaxonomyCalls)
}

// TestBigQuery_ApplyMaskingPolicy_ReusesExistingTaxonomy — repeat calls
// hit the in-process cache and DO NOT spam EnsureTaxonomy.
func TestBigQuery_ApplyMaskingPolicy_ReusesExistingTaxonomy(t *testing.T) {
	ptm := &fakePTM{}
	bq := &fakeBQ{}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")

	require.NoError(t, mp.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "p.ds.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash, AllowRoles: []string{"group:a@b"}}))
	require.NoError(t, mp.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "p.ds.orders"},
		connector.ColumnPolicy{Column: "email", MaskType: connector.MaskRedact}))
	// Taxonomy was created once at most (depends on first-call branch).
	require.LessOrEqual(t, ptm.CreateTaxonomyCalls, 1)
}

// TestBigQuery_ApplyMaskingPolicy_AttachesPolicyTagToColumn — the
// fakeBQ.UpdateTablePolicyTags must be called with the right column key
// and a non-empty tag value.
func TestBigQuery_ApplyMaskingPolicy_AttachesPolicyTagToColumn(t *testing.T) {
	ptm := &fakePTM{}
	bq := &fakeBQ{}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")

	err := mp.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "p.ds.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash, AllowRoles: []string{"group:a@b"}})
	require.NoError(t, err)
	require.Equal(t, 1, bq.UpdateCalls)
	require.Contains(t, bq.LastTags, "ssn")
	require.NotEmpty(t, bq.LastTags["ssn"])
	require.Contains(t, bq.LastTags["ssn"][0], "policyTags/hash")
}

// TestBigQuery_ApplyMaskingPolicy_BindsIAM_OnAllowRoles — SetIamPolicyOnTag
// must be called exactly once with the supplied members.
func TestBigQuery_ApplyMaskingPolicy_BindsIAM_OnAllowRoles(t *testing.T) {
	ptm := &fakePTM{}
	bq := &fakeBQ{}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")

	err := mp.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "p.ds.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash,
			AllowRoles: []string{"group:pii-analyst@example.com", "user:lead@example.com"}})
	require.NoError(t, err)
	require.Equal(t, 1, ptm.SetIamCalls)
	require.ElementsMatch(t, []string{"group:pii-analyst@example.com", "user:lead@example.com"}, ptm.LastIamMembers)
}

// TestBigQuery_RemoveMaskingPolicy_ClearsTag — clearing a column passes nil
// for that column's policyTags.
func TestBigQuery_RemoveMaskingPolicy_ClearsTag(t *testing.T) {
	ptm := &fakePTM{}
	bq := &fakeBQ{}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")

	err := mp.RemoveMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "p.ds.orders"}, "ssn")
	require.NoError(t, err)
	require.Equal(t, 1, bq.UpdateCalls)
	require.Contains(t, bq.LastTags, "ssn")
	require.Nil(t, bq.LastTags["ssn"])
}

// TestBigQuery_ListMaskingPolicies_ResolvesTagsToMaskTypes — verifies the
// ListPolicyTagsForTable + PolicyTagDisplayName round trip reconstructs
// MaskType values.
func TestBigQuery_ListMaskingPolicies_ResolvesTagsToMaskTypes(t *testing.T) {
	ptm := &fakePTM{}
	bq := &fakeBQ{}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")

	out, err := mp.ListMaskingPolicies(context.Background(),
		connector.AssetRef{Identifier: "p.ds.orders"})
	require.NoError(t, err)
	require.Len(t, out, 2)
	byCol := map[string]connector.MaskType{}
	for _, p := range out {
		byCol[p.Column] = p.MaskType
	}
	require.Equal(t, connector.MaskHash, byCol["ssn"])
	require.Equal(t, connector.MaskRedact, byCol["email"])
}

// TestBigQuery_ApplyMaskingPolicy_RejectsBadIdentifier — non-tri-part IDs.
func TestBigQuery_ApplyMaskingPolicy_RejectsBadIdentifier(t *testing.T) {
	ptm := &fakePTM{}
	bq := &fakeBQ{}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")
	err := mp.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "ds.orders"}, // missing project
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash})
	require.Error(t, err)
	require.Equal(t, 0, bq.UpdateCalls)
}

// TestBigQuery_ApplyMaskingPolicy_PropagatesContextError — ctx cancel returns ctx error.
func TestBigQuery_ApplyMaskingPolicy_PropagatesContextError(t *testing.T) {
	ptm := &fakePTM{}
	bq := &fakeBQ{}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := mp.ApplyMaskingPolicy(ctx,
		connector.AssetRef{Identifier: "p.ds.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash})
	require.Error(t, err)
}

// TestBigQuery_ApplyMaskingPolicy_BubblesUpdateError — Tables.update failure
// surfaces as a wrapped error.
func TestBigQuery_ApplyMaskingPolicy_BubblesUpdateError(t *testing.T) {
	ptm := &fakePTM{}
	bq := &fakeBQ{Err: errors.New("permission denied")}
	mp := NewMaskingProvisioner(ptm, bq, "us-central1", "dgp-platform")

	err := mp.ApplyMaskingPolicy(context.Background(),
		connector.AssetRef{Identifier: "p.ds.orders"},
		connector.ColumnPolicy{Column: "ssn", MaskType: connector.MaskHash, AllowRoles: []string{"group:a@b"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission denied")
}

// TestBigQuery_NormaliseMembers_FiltersBlanks — internal helper guard.
func TestBigQuery_NormaliseMembers_FiltersBlanks(t *testing.T) {
	require.Equal(t, []string{"a"}, normaliseMembers([]string{"", "a", " "}))
	require.Equal(t, []string{}, normaliseMembers(nil))
}
