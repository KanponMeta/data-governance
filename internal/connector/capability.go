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

// QueryAggregate is an optional capability (Phase 5 D-19). Connectors that
// support aggregate SQL queries implement this for quality rule evaluation.
// Connectors without it (e.g., file-only S3/GCS/HDFS) cause quality rules to
// be marked status='error' with reason 'connector does not support aggregate queries'.
//
// Callers MUST always wrap the call with a strict context.WithTimeout
// (default 30s for NullCheck/RangeCheck, 60s for SQLAssertion — Pitfall #10
// from RESEARCH §651-653) so a long warehouse query never blocks the
// per-step executor transaction forever.
type QueryAggregate interface {
	QueryAggregate(ctx context.Context, ref AssetRef, sql string) (AggregateRow, error)
}

// AggregateRow is a single-row result from QueryAggregate. Values are positional
// and aligned with Columns. Returns at most one row (the connector is responsible
// for issuing aggregate SQL that yields a single tuple).
type AggregateRow struct {
	Columns []string
	Values  []any
}

// QualifiedTable returns the fully-qualified table reference for use in
// quality rule SQL substitution (${asset}). The default implementation
// uses the AssetRef.Identifier as-is; connectors that need extra qualification
// (database/schema) should override the substitution at the call site.
func QualifiedTable(ref AssetRef) string {
	if ref.Identifier == "" {
		return ""
	}
	return ref.Identifier
}
