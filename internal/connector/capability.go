// Package-level optional capability interfaces. Connectors implement these
// additional interfaces to opt into Phase 4+ features. The base Connector
// interface in connector.go is FROZEN (CONN-08).
//
// Runtime detection pattern (used by Wave 4 schema writer):
//
//	if d, ok := conn.(connector.SchemaDescriber); ok {
//	    schema, err := d.DescribeSchema(ctx, ref)
//	    // ... use schema ...
//	} else {
//	    // Fall back to MaterializeResult.Schema or tag schema_capture_unsupported.
//	}
package connector

import "context"

// SchemaDescriber is an optional capability interface (Phase 4 D-05/D-06).
//
// Connectors that can introspect their output table after a successful
// materialization implement this. The PostgreSQL connector queries
// information_schema.columns; future BigQuery/Snowflake connectors will
// query their own equivalents; Kafka/REST connectors legitimately CANNOT
// and should NOT implement this interface — the type assertion will return
// ok=false and Wave 4's schema writer will tag the asset
// schema_capture_unsupported (no alert noise).
//
// Errors from DescribeSchema are non-fatal (D-08): Wave 4's writer logs
// the error, emits a schema.capture_failed event_log row, and lets the
// asset materialization succeed.
//
// Phase 5 will add MaskingCapability following the same separate-interface
// pattern.
type SchemaDescriber interface {
	// DescribeSchema returns the current Schema of the asset's output table as
	// observed from the warehouse. Called after a successful materialization.
	// Errors are non-fatal (D-08: schema capture failure emits event, run succeeds).
	DescribeSchema(ctx context.Context, ref AssetRef) (Schema, error)
}
