// Package runtime provides the execution engine hooks for Phase 5 governance
// integration points. Downstream plans (05-02..05-05) register PreStep, PostSchema,
// and IOWrap hooks here instead of editing executor.go directly (B-03 fix).
package runtime

import (
	"context"
	"database/sql"
	"sort"
	"sync"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
)

// PreStepHook is called before each asset step executes.
// Return an error to halt the run with the error as the failure reason.
type PreStepHook func(ctx context.Context, runID uuid.UUID, a *asset.Asset, deps Deps) error

// PostSchemaHook is called inside the per-step commit transaction,
// after schema capture, before tx.Commit. Use for quality evaluation,
// additional audit entries, or governance checks.
type PostSchemaHook func(ctx context.Context, tx *sql.Tx, runID uuid.UUID, a *asset.Asset, conn connector.Connector, ref connector.AssetRef, deps Deps) error

// IOWrapHook wraps the AssetIO before NewTrackingIO is called.
// Use for masking IO, encryption, or other transformation layers.
// The hook receives the raw io and the asset/connector and returns the wrapped io.
type IOWrapHook func(io asset.AssetIO, a *asset.Asset, conn connector.Connector, deps Deps) asset.AssetIO

type namedPreStep    struct{ Name string; Hook PreStepHook }
type namedPostSchema struct{ Name string; Hook PostSchemaHook }
type namedIOWrap     struct{ Name string; Hook IOWrapHook }

var (
	hooksMu     sync.Mutex
	pre         = []namedPreStep{}
	postSchema  = []namedPostSchema{}
	ioWrap      = []namedIOWrap{}
)

// RegisterPreStep registers a PreStepHook. Hooks are called in Name order.
func RegisterPreStep(name string, h PreStepHook) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	pre = append(pre, namedPreStep{name, h})
	sort.Slice(pre, func(i, j int) bool { return pre[i].Name < pre[j].Name })
}

// RegisterPostSchema registers a PostSchemaHook. Hooks are called in Name order.
func RegisterPostSchema(name string, h PostSchemaHook) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	postSchema = append(postSchema, namedPostSchema{name, h})
	sort.Slice(postSchema, func(i, j int) bool { return postSchema[i].Name < postSchema[j].Name })
}

// RegisterIOWrap registers an IOWrapHook. Hooks are folded in Name order.
func RegisterIOWrap(name string, h IOWrapHook) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	ioWrap = append(ioWrap, namedIOWrap{name, h})
	sort.Slice(ioWrap, func(i, j int) bool { return ioWrap[i].Name < ioWrap[j].Name })
}

// PreStepHooks returns a snapshot of registered PreStepHooks in call order.
func PreStepHooks() []namedPreStep {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	out := make([]namedPreStep, len(pre))
	copy(out, pre)
	return out
}

// PostSchemaHooks returns a snapshot of registered PostSchemaHooks in call order.
func PostSchemaHooks() []namedPostSchema {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	out := make([]namedPostSchema, len(postSchema))
	copy(out, postSchema)
	return out
}

// IOWrapHooks returns a snapshot of registered IOWrapHooks in call order.
func IOWrapHooks() []namedIOWrap {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	out := make([]namedIOWrap, len(ioWrap))
	copy(out, ioWrap)
	return out
}
