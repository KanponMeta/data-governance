// BigQuery Column-Level Security (CLS) provisioner — implements
// connector.MaskingProvisioner for the Phase 5 plan 05-02 column-level
// access control feature (D-04, RBAC-03/04).
//
// Wire protocol per RESEARCH.md §3:
//
//   1. Ensure Data Catalog Taxonomy "<TaxonomyDisplayName>" exists in
//      project+location with ActivatedPolicyTypes containing
//      FINE_GRAINED_ACCESS_CONTROL (Pitfall #5).
//   2. Ensure a PolicyTag with display_name == string(MaskType) exists
//      under the taxonomy (Pitfall #5: 1 column → 1 tag).
//   3. SetIamPolicy on the policy_tag with role
//      "roles/datacatalog.fineGrainedReader" + members from AllowRoles.
//   4. Tables.Update column metadata with the new policy_tag attached.
//
// Implementation notes:
//   - The Data Catalog client surface area is large; we abstract behind
//     PolicyTagManagerClient + BigQueryClient interfaces so the real
//     google client and the testharness BigQuery mock both satisfy them
//     without leaking BigQuery types into the rest of the platform.
//   - Errors from the warehouse round-trip are returned verbatim; the River
//     sync worker (internal/policy/sync_job.go) handles retries.
package bigquery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/kanpon/data-governance/internal/connector"
)

// PolicyTagManagerClient abstracts the cloud.google.com/go/datacatalog
// PolicyTagManager client surface that matters for column-level security.
// Supplying a small interface (rather than the concrete *datacatalog.PolicyTagManagerClient)
// lets the testharness BigQuery mock implement these methods directly.
//
// All operations MUST honour ctx — Apply / Remove must abort within the
// supplied timeout. Methods may return wrapped gRPC errors.
type PolicyTagManagerClient interface {
	// EnsureTaxonomy returns the resource name (full path) of a Taxonomy
	// with the given display name and FINE_GRAINED_ACCESS_CONTROL activated.
	// Implementations create the taxonomy if it does not exist.
	EnsureTaxonomy(ctx context.Context, projectID, location, displayName string) (string, error)

	// EnsurePolicyTag returns the resource name of a PolicyTag with the
	// given display name under taxonomyName. Created on demand.
	EnsurePolicyTag(ctx context.Context, taxonomyName, displayName string) (string, error)

	// SetIamPolicyOnTag binds members (e.g. "group:pii-analyst@example.com")
	// to roles/datacatalog.fineGrainedReader on the supplied policy tag.
	// The bindings REPLACE any existing bindings.
	SetIamPolicyOnTag(ctx context.Context, policyTagName string, members []string) error

	// ListPolicyTagsForTable returns a map of column → policy_tag_name
	// currently attached to the named BigQuery table. Used by
	// ListMaskingPolicies to recover masking state.
	ListPolicyTagsForTable(ctx context.Context, projectID, datasetID, tableID string) (map[string]string, error)

	// PolicyTagDisplayName returns the display name of a policy tag given
	// its resource name. Used by ListMaskingPolicies to derive MaskType
	// from the attached tag.
	PolicyTagDisplayName(ctx context.Context, policyTagName string) (string, error)
}

// BigQueryClient abstracts the cloud.google.com/go/bigquery Tables.Update
// surface that we use for attaching policy tags to columns.
type BigQueryClient interface {
	// UpdateTablePolicyTags sets each column's policyTags attribute.
	// Pass an empty []string for a column to clear its tags.
	UpdateTablePolicyTags(ctx context.Context, projectID, datasetID, tableID string, columnTags map[string][]string) error
}

// MaskingProvisioner is the BigQuery-side implementation. It is constructed
// separately from the BigQuery connector so unit tests can inject mocks
// without touching the live BigQuery client.
//
// TaxonomyDisplayName defaults to "dgp-platform" if empty.
type MaskingProvisioner struct {
	Ptm                 PolicyTagManagerClient
	Bq                  BigQueryClient
	Location            string
	TaxonomyDisplayName string
	mu                  sync.Mutex // guards the lazily-cached taxonomy / tag names
	taxonomyCache       string
	tagCache            map[connector.MaskType]string
}

// Compile-time interface assertion.
var _ connector.MaskingProvisioner = (*MaskingProvisioner)(nil)

// NewMaskingProvisioner builds a MaskingProvisioner. Either ptm or bq may
// be nil for partial-fixture testing; the corresponding methods will return
// a clear error if invoked.
func NewMaskingProvisioner(ptm PolicyTagManagerClient, bq BigQueryClient, location, taxonomy string) *MaskingProvisioner {
	if taxonomy == "" {
		taxonomy = "dgp-platform"
	}
	return &MaskingProvisioner{
		Ptm:                 ptm,
		Bq:                  bq,
		Location:            location,
		TaxonomyDisplayName: taxonomy,
		tagCache:            map[connector.MaskType]string{},
	}
}

// ApplyMaskingPolicy installs the column-level security tag on (project, dataset, table, column).
// Steps follow RESEARCH.md §3 ordering.
//
// AssetRef format: ref.Identifier carries "PROJECT.DATASET.TABLE" (matching the
// Phase 4 BigQuery connector convention).
func (m *MaskingProvisioner) ApplyMaskingPolicy(ctx context.Context, ref connector.AssetRef, policy connector.ColumnPolicy) error {
	if m.Ptm == nil || m.Bq == nil {
		return errors.New("bigquery: MaskingProvisioner missing PolicyTagManagerClient or BigQueryClient")
	}
	if !policy.MaskType.IsValid() {
		return fmt.Errorf("bigquery: invalid mask type %q", policy.MaskType)
	}
	if policy.Column == "" {
		return errors.New("bigquery: ColumnPolicy.Column required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	project, dataset, table, err := splitTriIdentifier(ref.Identifier)
	if err != nil {
		return err
	}

	// 1. Ensure taxonomy.
	taxonomy, err := m.ensureTaxonomy(ctx, project)
	if err != nil {
		return fmt.Errorf("bigquery: ensure taxonomy: %w", err)
	}

	// 2. Ensure policy tag.
	tagName, err := m.ensurePolicyTag(ctx, taxonomy, policy.MaskType)
	if err != nil {
		return fmt.Errorf("bigquery: ensure policy tag: %w", err)
	}

	// 3. Set IAM bindings.
	members := normaliseMembers(policy.AllowRoles)
	if err := m.Ptm.SetIamPolicyOnTag(ctx, tagName, members); err != nil {
		return fmt.Errorf("bigquery: set IAM on tag: %w", err)
	}

	// 4. Attach tag to column via Tables.update.
	if err := m.Bq.UpdateTablePolicyTags(ctx, project, dataset, table,
		map[string][]string{policy.Column: {tagName}}); err != nil {
		return fmt.Errorf("bigquery: update table policy tags: %w", err)
	}
	return nil
}

// RemoveMaskingPolicy clears the policy tag from the column. Must succeed
// even if the column has no tag attached (Pitfall #4).
func (m *MaskingProvisioner) RemoveMaskingPolicy(ctx context.Context, ref connector.AssetRef, column string) error {
	if m.Bq == nil {
		return errors.New("bigquery: MaskingProvisioner missing BigQueryClient")
	}
	if column == "" {
		return errors.New("bigquery: column required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	project, dataset, table, err := splitTriIdentifier(ref.Identifier)
	if err != nil {
		return err
	}
	return m.Bq.UpdateTablePolicyTags(ctx, project, dataset, table,
		map[string][]string{column: nil})
}

// ListMaskingPolicies returns the current column → MaskType mapping for the
// asset by reading Tables.get and resolving each tag's display name back to
// a MaskType.
func (m *MaskingProvisioner) ListMaskingPolicies(ctx context.Context, ref connector.AssetRef) ([]connector.ColumnPolicy, error) {
	if m.Bq == nil || m.Ptm == nil {
		return nil, errors.New("bigquery: MaskingProvisioner not fully wired")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	project, dataset, table, err := splitTriIdentifier(ref.Identifier)
	if err != nil {
		return nil, err
	}

	// One ListPolicyTagsForTable lookup is sufficient — every tagged column is
	// returned as a single entry (Pitfall #5: 1 column = 1 tag).
	tags, err := m.Ptm.ListPolicyTagsForTable(ctx, project, dataset, table)
	if err != nil {
		return nil, fmt.Errorf("bigquery: list policy tags: %w", err)
	}
	out := make([]connector.ColumnPolicy, 0, len(tags))
	for col, tagName := range tags {
		dn, err := m.Ptm.PolicyTagDisplayName(ctx, tagName)
		if err != nil {
			// Skip tags we can't resolve rather than fail the whole list —
			// reconciler will flag them as drift.
			continue
		}
		mt := connector.MaskType(strings.ToLower(strings.TrimSpace(dn)))
		if !mt.IsValid() {
			continue
		}
		out = append(out, connector.ColumnPolicy{Column: col, MaskType: mt})
	}
	return out, nil
}

// ensureTaxonomy caches the resource name of the taxonomy after the first
// successful EnsureTaxonomy. The cache is OK to keep for the process
// lifetime — taxonomies are rarely deleted.
func (m *MaskingProvisioner) ensureTaxonomy(ctx context.Context, projectID string) (string, error) {
	m.mu.Lock()
	cached := m.taxonomyCache
	m.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	name, err := m.Ptm.EnsureTaxonomy(ctx, projectID, m.Location, m.TaxonomyDisplayName)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.taxonomyCache = name
	m.mu.Unlock()
	return name, nil
}

// ensurePolicyTag caches per-MaskType tag resource names.
func (m *MaskingProvisioner) ensurePolicyTag(ctx context.Context, taxonomy string, mt connector.MaskType) (string, error) {
	m.mu.Lock()
	cached := m.tagCache[mt]
	m.mu.Unlock()
	if cached != "" {
		return cached, nil
	}
	name, err := m.Ptm.EnsurePolicyTag(ctx, taxonomy, string(mt))
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.tagCache[mt] = name
	m.mu.Unlock()
	return name, nil
}

// splitTriIdentifier splits "PROJECT.DATASET.TABLE" into three parts.
func splitTriIdentifier(id string) (string, string, string, error) {
	parts := strings.Split(id, ".")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("bigquery: identifier %q must be PROJECT.DATASET.TABLE", id)
	}
	for _, p := range parts {
		if p == "" {
			return "", "", "", fmt.Errorf("bigquery: identifier %q has empty part", id)
		}
	}
	return parts[0], parts[1], parts[2], nil
}

// normaliseMembers passes through IAM members. Empty roles become an
// empty slice so the IAM binding clears any previously-applied members.
func normaliseMembers(roles []string) []string {
	if roles == nil {
		return []string{}
	}
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// IAMRoleColumnReader is the canonical IAM role used for column-level reader
// permissions on a BigQuery policy tag. Re-exported so callers don't have
// to hard-code the string in tests.
const IAMRoleColumnReader = "roles/datacatalog.fineGrainedReader"
