package api

import (
	"github.com/go-chi/chi/v5"

	"github.com/kanpon/data-governance/internal/platform"
	"github.com/kanpon/data-governance/internal/policy"
)

// init self-registers policy routes via the platform registry (B-03 fix).
// MountPolicy is the bridge between platform.MountDeps and the
// internal/policy.MountPolicy signature.
func init() {
	platform.RegisterRoutes("policy", MountPolicy)
}

// MountPolicy mounts the column-policy REST endpoints (Phase 5 plan 05-02).
// It adapts platform.MountDeps to the policy.Store + casbin enforcer that
// internal/policy.MountPolicy requires.
//
// The yamlLoader hook is left nil here — production wiring registers a
// platform-specific loader via deps.Extra["policy.yaml_loader"]. When
// nil the /policies/yaml-reload route returns 500 with a clear message.
func MountPolicy(r chi.Router, deps platform.MountDeps) {
	store := policy.NewStore(deps.DB, nil)

	var loader func() (*policy.YAMLConfig, error)
	if deps.Extra != nil {
		if v, ok := deps.Extra["policy.yaml_loader"]; ok {
			if fn, ok := v.(func() (*policy.YAMLConfig, error)); ok {
				loader = fn
			}
		}
	}

	policy.MountPolicy(r, store, deps.Enforcer, deps.AuthMW, loader)
}
