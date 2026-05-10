package runtime

import (
	"context"

	"github.com/kanpon/data-governance/internal/asset"
)

// MaybeWrapMaskingIOForTest exposes the internal helper that decides
// whether to wrap an AssetIO with MaskingIO. Used by executor_mask_test.go
// to drive the decision logic without booting a full run lifecycle.
func (e *Executor) MaybeWrapMaskingIOForTest(ctx context.Context, assetName string, inner asset.AssetIO) (asset.AssetIO, error) {
	return e.maybeWrapMaskingIO(ctx, assetName, inner)
}
