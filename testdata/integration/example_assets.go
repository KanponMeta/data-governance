// Package integration provides test assets and configuration for Phase 2 integration tests.
// The assets registered here are used by test/integration/e2e_postgres_test.go.
package integration

import (
	"context"
	"time"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
)

// RegisterTestAssets creates users_raw → users_clean using the postgres connector.
// users_raw materializes two seed rows; users_clean reads from users_raw and writes them.
// This exercises the DAG executor's topological ordering (acceptance criterion 1).
func RegisterTestAssets() error {
	if err := asset.New("users_raw").
		Connector("postgres-test").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			rows := []connector.Row{
				{Fields: map[string]any{"id": int64(1), "email": "a@b.com"}},
				{Fields: map[string]any{"id": int64(2), "email": "c@d.com"}},
			}
			n, err := io.Write(ctx, rows)
			return asset.MaterializeResult{RowsWritten: n}, err
		}).
		Register(); err != nil {
		return err
	}

	return asset.New("users_clean").
		Upstream("users_raw").
		Connector("postgres-test").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			upstream, err := io.Read(ctx, "users_raw")
			if err != nil {
				return asset.MaterializeResult{}, err
			}
			n, err := io.Write(ctx, upstream)
			return asset.MaterializeResult{RowsWritten: n}, err
		}).
		Register()
}

// RegisterFailingAsset registers an asset whose Materialize always returns an error.
// Used by TestE2E_PostgresMaterialize_Failure to verify retry event visibility.
func RegisterFailingAsset(name string) error {
	return asset.New(name).
		Connector("postgres-test").
		Retry(asset.RetryPolicy{Max: 2, InitialDelay: 10 * time.Millisecond}).
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, errBoom
		}).
		Register()
}

// errBoom is the sentinel error returned by the failing asset.
var errBoom = boomError("boom: intentional test failure")

type boomError string

func (e boomError) Error() string { return string(e) }
