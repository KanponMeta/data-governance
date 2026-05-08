package asset

import (
	"context"
	"fmt"

	"github.com/kanpon/data-governance/internal/connector"
)

// AssetIO is the user-facing IO contract (D-04). User Materialize functions call
// io.Read(upstreamName) to read upstream rows and io.Write(rows) to write the
// asset's own rows — connector resolution and pooling live behind this interface.
type AssetIO interface {
	// Read reads the rows of the named upstream asset using its bound connector.
	// Returns ErrUnknownUpstream if the upstream is not declared in the asset's Upstreams().
	Read(ctx context.Context, upstream string) ([]connector.Row, error)

	// Write writes rows to the asset's own connector target.
	// Returns the connector-reported RowsWritten count.
	Write(ctx context.Context, rows []connector.Row) (int64, error)
}

// ErrUnknownUpstream is returned by AssetIO.Read when the upstream name
// was not declared in the asset's Upstreams() list (T-02-01-05 mitigation).
var ErrUnknownUpstream = fmt.Errorf("asset: upstream not declared")

// ConnectorResolver maps an asset name to the connector instance that
// materializes it, plus the AssetRef for that asset. Plan 02-03 implements
// this against connector.Registry + the startup config.
type ConnectorResolver interface {
	Resolve(assetName string) (connector.Connector, connector.AssetRef, error)
}

// NewAssetIO constructs the runtime AssetIO for an asset run. The DAG executor
// (plan 02-02) builds one AssetIO per step and passes it to MaterializeFunc.
func NewAssetIO(self *Asset, resolver ConnectorResolver) AssetIO {
	return &assetIO{self: self, resolver: resolver}
}

type assetIO struct {
	self     *Asset
	resolver ConnectorResolver
}

// Read enforces that the user reads only declared upstreams (catches typos at
// runtime) and delegates to the connector via the resolver (T-02-01-05).
func (io *assetIO) Read(ctx context.Context, upstream string) ([]connector.Row, error) {
	declared := false
	for _, u := range io.self.upstreams {
		if u == upstream {
			declared = true
			break
		}
	}
	if !declared {
		return nil, fmt.Errorf("%w: %q (declared: %v)", ErrUnknownUpstream, upstream, io.self.upstreams)
	}
	c, ref, err := io.resolver.Resolve(upstream)
	if err != nil {
		return nil, fmt.Errorf("asset: resolve upstream %q: %w", upstream, err)
	}
	resp, err := c.Read(ctx, connector.ReadRequest{Asset: ref})
	if err != nil {
		return nil, fmt.Errorf("asset: connector read %q: %w", upstream, err)
	}
	return resp.Rows, nil
}

// Write delegates to the asset's own connector via the resolver.
func (io *assetIO) Write(ctx context.Context, rows []connector.Row) (int64, error) {
	c, ref, err := io.resolver.Resolve(io.self.name)
	if err != nil {
		return 0, fmt.Errorf("asset: resolve self %q: %w", io.self.name, err)
	}
	resp, err := c.Write(ctx, connector.WriteRequest{Asset: ref, Rows: rows})
	if err != nil {
		return 0, fmt.Errorf("asset: connector write %q: %w", io.self.name, err)
	}
	return resp.RowsWritten, nil
}
