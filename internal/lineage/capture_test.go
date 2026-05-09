//go:build !integration

package lineage_test

import (
	"context"
	"testing"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/lineage"
	"github.com/stretchr/testify/require"
)

func buildTestAsset(t *testing.T, name string, upstreams ...string) *asset.Asset {
	t.Helper()
	b := asset.New(name).
		Connector("test").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, nil
		})
	if len(upstreams) > 0 {
		b = b.Upstream(upstreams...)
	}
	a, err := b.Build()
	require.NoError(t, err)
	return a
}

func TestSyncStaticEdgesNoUpstreams(t *testing.T) {
	// No DB required: an asset with 0 upstreams returns nil immediately.
	w := lineage.NewWriter(nil, nil)
	a := buildTestAsset(t, "source_asset")

	err := w.SyncStaticEdges(context.Background(), a, "abc123")
	require.NoError(t, err, "SyncStaticEdges with 0 upstreams should return nil")
}

func TestSyncStaticEdgesNilAsset(t *testing.T) {
	w := lineage.NewWriter(nil, nil)
	err := w.SyncStaticEdges(context.Background(), nil, "abc123")
	require.Error(t, err, "SyncStaticEdges with nil asset should return error")
}

func TestSyncStaticEdgesEmptyCodeHash(t *testing.T) {
	w := lineage.NewWriter(nil, nil)
	a := buildTestAsset(t, "source_asset")
	err := w.SyncStaticEdges(context.Background(), a, "")
	require.Error(t, err, "SyncStaticEdges with empty codeHash should return error")
}
