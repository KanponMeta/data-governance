// Package governance implements the Phase 5 Plan 05-04 governance workflow:
// reviewer pool resolution (D-09), 5-check auto-approval pipeline (D-10),
// state-machine transitions (D-12) tied to the audit hash chain, REST + CLI
// surfaces, executor materialization gate (D-08), SLA scanner, and reassign
// safety net (Pitfall #12).
//
// The package's central type is Workflow — a service that owns Submit /
// Approve / Reject / Reassign / Status. ResolveReviewers and
// AutoApprovalChecker are the two pure-logic helpers that Workflow.Submit
// composes; both are exported so tests + future callers can compose them
// independently.
package governance

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/policy"
)

// ReviewerPool is the resolved decision surface for a single review (D-09).
// Source records the provenance of each entry so audit payloads can answer
// "where did this reviewer come from?"  Quorum is the integer interpretation
// of asset.Quorum() — -1 (asset.QuorumAll) is preserved verbatim so the
// approve handler can treat it as "all roles in the pool".
type ReviewerPool struct {
	Roles           []string `json:"roles"`
	Quorum          int      `json:"quorum"`
	RequireHuman    bool     `json:"require_human"`
	EscalationRoles []string `json:"escalation_roles,omitempty"`
	Source          []string `json:"source,omitempty"`
}

// Resolver computes the reviewer pool for an asset by composing three
// independent sources (D-09):
//
//  1. asset.Builder.Reviewers (highest priority)
//  2. policies.yaml tag_reviewer_roles for each tag on the asset
//  3. team_owners table fallback (only when 1 ∪ 2 is empty)
//
// The yaml field may be nil — the resolver simply skips source (2). The db
// field MUST be non-nil when team_owners fallback is required; pass a
// platform_app DB connection.
type Resolver struct {
	db   *sql.DB
	yaml *policy.YAMLConfig
}

// NewResolver constructs a Resolver. yaml may be nil (skips tag-based source).
func NewResolver(db *sql.DB, yaml *policy.YAMLConfig) *Resolver {
	return &Resolver{db: db, yaml: yaml}
}

// ResolveReviewers computes the reviewer pool for the supplied asset.
//
// Inputs:
//   - a:     the asset whose Reviewers/Quorum/RequireHumanReview/EscalationRoles
//            are read as the highest-priority source.
//   - tags:  the union of asset-level + column-level tags (caller responsibility
//            to pass an already-merged list; resolver does not deduplicate the
//            input).
//   - owner: the asset_metadata.owner email used for team_owners fallback;
//            empty string disables fallback.
//
// Returns the merged pool (deduped). Quorum 0 → 1 (D-09 minimum-friction
// default). All sources EXCEPT owner-fallback contribute to (1) ∪ (2);
// owner-fallback fires only when (1) ∪ (2) is empty.
func (r *Resolver) ResolveReviewers(
	ctx context.Context,
	a *asset.Asset,
	tags []string,
	owner string,
) (ReviewerPool, error) {
	pool := ReviewerPool{
		Quorum:          int(a.Quorum()),
		RequireHuman:    a.RequireHumanReview(),
		EscalationRoles: a.EscalationRoles(),
	}
	if pool.Quorum == 0 {
		pool.Quorum = 1
	}

	// Source 1 — Builder.
	if br := a.ReviewerRoles(); len(br) > 0 {
		pool.Roles = append(pool.Roles, br...)
		pool.Source = append(pool.Source, "builder")
	}

	// Source 2 — YAML tag rules.
	if r.yaml != nil && len(r.yaml.TagReviewerRoles) > 0 {
		// Sort tags for deterministic Source ordering.
		sortedTags := append([]string(nil), tags...)
		sort.Strings(sortedTags)
		for _, tag := range sortedTags {
			roles, ok := r.yaml.TagReviewerRoles[tag]
			if !ok || len(roles) == 0 {
				continue
			}
			pool.Roles = append(pool.Roles, roles...)
			pool.Source = append(pool.Source, "yaml-tag:"+tag)
		}
	}

	// Source 3 — owner fallback (only when 1 ∪ 2 is empty).
	if len(pool.Roles) == 0 && owner != "" && r.db != nil {
		var rolesJSON []byte
		err := r.db.QueryRowContext(ctx,
			`SELECT roles FROM team_owners WHERE owner_email=$1`, owner,
		).Scan(&rolesJSON)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// no fallback row — pool stays empty.
		case err != nil:
			return pool, fmt.Errorf("governance: team_owners lookup for %q: %w", owner, err)
		default:
			var roles []string
			if err := json.Unmarshal(rolesJSON, &roles); err != nil {
				return pool, fmt.Errorf("governance: unmarshal team_owners.roles for %q: %w", owner, err)
			}
			if len(roles) > 0 {
				pool.Roles = append(pool.Roles, roles...)
				pool.Source = append(pool.Source, "owner-fallback")
			}
		}
	}

	pool.Roles = dedupRoles(pool.Roles)
	return pool, nil
}

// dedupRoles removes duplicate role names while preserving first-seen order.
// Stable order is essential because reviewer_pool_snapshot is JSONB and any
// downstream comparison (audit hash chain payload, tests) needs determinism.
//
// WR-11: always returns a freshly allocated slice — previously the len<=1
// fast-path returned the input slice directly. Callers that retained the
// returned slice could observe aliasing mutations if a later append on the
// shared backing array fit in spare capacity.
func dedupRoles(in []string) []string {
	out := make([]string, 0, len(in))
	if len(in) == 0 {
		return out
	}
	seen := make(map[string]struct{}, len(in))
	for _, r := range in {
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
