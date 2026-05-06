// Package example_inproc provides a reference implementation of a third-party-style
// connector that lives outside the platform's internal packages.
//
// This package exists to prove that the connector.Connector interface is a clean
// public boundary consumable by third parties. As such, it imports ONLY:
//
//	github.com/kanpon/data-governance/internal/connector
//
// It does NOT import any other github.com/kanpon/data-governance/* package.
// If you add an import from another internal package, you break the boundary.
package example_inproc

import (
	"context"
	"time"

	"github.com/kanpon/data-governance/internal/connector"
)

// Compile-time assertion: PostgresStub satisfies the connector.Connector interface.
var _ connector.Connector = (*PostgresStub)(nil)

// PostgresStub is a minimal reference connector that wraps a PostgreSQL data source.
// It is intended as a proof-of-concept; production connectors should implement
// proper connection pooling, error handling, and schema discovery.
type PostgresStub struct{}

// NewPostgresStub returns a new PostgresStub connector.
func NewPostgresStub() *PostgresStub {
	return &PostgresStub{}
}

// APIVersion returns the connector ABI version.
func (c *PostgresStub) APIVersion() string {
	return connector.APIVersion
}

// Ping returns the connector identity and capabilities.
func (c *PostgresStub) Ping(ctx context.Context, req connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{
		APIVersion:       connector.APIVersion,
		ConnectorName:    "postgres-stub",
		ConnectorVersion: "0.1.0-phase1",
		Capabilities: connector.Capabilities{
			SupportsSchemaCapture: true,
		},
	}, nil
}

// Schema returns a canned schema with a single "id uuid not null" column.
// A real connector would query the information_schema.
func (c *PostgresStub) Schema(ctx context.Context, req connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{
		Columns: []connector.Column{
			{Name: "id", RawType: "uuid", Nullable: false},
		},
		CapturedAt: time.Now().UTC(),
	}, nil
}

// Read returns an empty row set. A real connector would execute a SQL query.
func (c *PostgresStub) Read(ctx context.Context, req connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{Rows: []connector.Row{}}, nil
}

// Write returns the number of rows written. A real connector would execute COPY or INSERT.
func (c *PostgresStub) Write(ctx context.Context, req connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{
		RowsWritten: int64(len(req.Rows)),
	}, nil
}
