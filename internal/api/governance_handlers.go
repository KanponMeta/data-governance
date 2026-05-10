package api

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-chi/chi/v5"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/governance"
	"github.com/kanpon/data-governance/internal/notification"
	"github.com/kanpon/data-governance/internal/platform"
	"github.com/kanpon/data-governance/internal/policy"
)

// init self-registers governance routes via the platform registry (B-03 fix).
func init() {
	platform.RegisterRoutes("governance", MountGovernance)
}

// defaultAssetLookup adapts asset.Default() to governance.AssetLookup.
type defaultAssetLookup struct{}

func (defaultAssetLookup) Get(name string) (*asset.Asset, error) {
	return asset.Default().Get(name)
}

// MountGovernance wires the Phase 5 Plan 05-04 governance REST endpoints
// onto r using the platform-managed dependencies.
//
// The function reads two optional Extra entries off deps:
//   - "policy.yaml" (*policy.YAMLConfig)         — supplied by the start subcommand.
//   - "governance.queue" (notification.JobInserter) — for /governance/submit
//     to enqueue notification jobs. Absent → notifications skipped.
//
// Both are optional: missing yaml degrades the resolver to (builder + owner-fallback);
// missing queue removes notification dispatch but the workflow + state machine still work.
func MountGovernance(r chi.Router, deps platform.MountDeps) {
	if deps.DB == nil || deps.Enforcer == nil {
		return
	}

	var yamlCfg *policy.YAMLConfig
	if deps.Extra != nil {
		if v, ok := deps.Extra["policy.yaml"]; ok {
			if cfg, ok := v.(*policy.YAMLConfig); ok {
				yamlCfg = cfg
			}
		}
	}
	resolver := governance.NewResolver(deps.DB, yamlCfg)
	checker := governance.NewAutoApprovalChecker(deps.DB)

	var queue notification.JobInserter
	if deps.Extra != nil {
		if v, ok := deps.Extra["governance.queue"]; ok {
			if q, ok := v.(notification.JobInserter); ok {
				queue = q
			}
		}
	}
	wf := governance.NewWorkflow(deps.DB, resolver, checker, queue)

	governance.MountGovernance(r, governance.HandlerDeps{
		Workflow:       wf,
		Enforcer:       deps.Enforcer,
		AuthMW:         deps.AuthMW,
		AssetLookup:    defaultAssetLookup{},
		MetadataLookup: defaultMetadataLookup(deps),
	})
}

// defaultMetadataLookup reads (tags, owner) from asset_metadata. It is best-effort —
// any error returns (nil, "", err) and the handler degrades to an empty-tag submit.
func defaultMetadataLookup(deps platform.MountDeps) governance.MetadataLookupFn {
	return func(ctx context.Context, assetName string) ([]string, string, error) {
		var tagsJSON []byte
		var owner string
		err := deps.DB.QueryRowContext(ctx, `
			SELECT COALESCE(tags::text,''), COALESCE(owner,'') FROM asset_metadata
			 WHERE asset = $1 AND column_name IS NULL
			 ORDER BY set_at DESC LIMIT 1
		`, assetName).Scan(&tagsJSON, &owner)
		if err != nil {
			// No metadata row — empty tags + empty owner.
			return nil, "", fmt.Errorf("metadata lookup: %w", err)
		}
		// Parse tags JSON array. Empty / invalid → no tags.
		var tags []string
		if len(tagsJSON) > 2 {
			_ = jsonDecodeArray(tagsJSON, &tags)
		}
		return tags, owner, nil
	}
}

// jsonDecodeArray is a tiny helper to decode a JSON []string. Empty input
// is treated as success with the zero value preserved.
func jsonDecodeArray(raw []byte, out *[]string) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
