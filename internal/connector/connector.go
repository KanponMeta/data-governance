// Package connector defines the versioned connector interface that third-party
// data platform integrations must implement to register as connectors.
//
// Phase 1 scope (D-01): In-process connectors only. The interface below is
// consumed directly by the platform. Phase 2 will add a go-plugin subprocess
// transport layer that wraps these types with generated protobuf adapters.
//
// Stability commitment: This package is FROZEN at v1.0.0 once Phase 1 ships.
// The APIVersion constant and all method signatures are guaranteed stable.
// Breaking changes require data_governance.connector.v2.
package connector

import (
	"context"
	"time"
)

// Connector is the interface that all data platform connectors must implement.
// It is the single, stable ABI contract for Phase 1.
//
// Implementors MUST ensure APIVersion() returns exactly connector.APIVersion.
// The Registry will reject connectors that return a mismatched version string.
type Connector interface {
	// APIVersion returns the connector ABI version, e.g. "v1.0.0".
	// The returned value MUST equal connector.APIVersion or registration fails.
	APIVersion() string

	// Ping returns the connector's identity and capabilities.
	Ping(ctx context.Context, req PingRequest) (PingResponse, error)

	// Schema returns the column definitions for the given asset.
	Schema(ctx context.Context, req SchemaRequest) (SchemaResponse, error)

	// Read returns rows from the given asset, up to the requested limit.
	// An empty or zero-value req.Limit means "no limit".
	Read(ctx context.Context, req ReadRequest) (ReadResponse, error)

	// Write persists rows to the given asset.
	// The connector SHOULD respect IdempotencyKey to deduplicate repeated writes.
	Write(ctx context.Context, req WriteRequest) (WriteResponse, error)
}

// ===== Request/Response types =====

// Capabilities describes optional features a connector may support.
type Capabilities struct {
	SupportsSchemaCapture  bool
	SupportsColumnMasking  bool
	SupportsPartitioning   bool
}

type PingRequest struct{}

type PingResponse struct {
	APIVersion       string
	ConnectorName    string
	ConnectorVersion string
	Capabilities     Capabilities
}

type AssetRef struct {
	Identifier string
	Config     map[string]string
}

type SchemaRequest struct {
	Asset AssetRef
}

type Column struct {
	Name     string
	RawType  string
	Nullable bool
}

type SchemaResponse struct {
	Columns    []Column
	CapturedAt time.Time
}

type ReadRequest struct {
	Asset AssetRef
	Limit int64
}

type Row struct {
	Fields map[string]any
}

type ReadResponse struct {
	Rows []Row
}

type WriteRequest struct {
	Asset          AssetRef
	Rows           []Row
	IdempotencyKey string
}

type WriteResponse struct {
	RowsWritten int64
}
