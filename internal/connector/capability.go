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
// Phase 5 adds MaskingProvisioner following the same separate-interface
// pattern.
type SchemaDescriber interface {
	// DescribeSchema returns the current Schema of the asset's output table as
	// observed from the warehouse. Called after a successful materialization.
	// Errors are non-fatal (D-08: schema capture failure emits event, run succeeds).
	DescribeSchema(ctx context.Context, ref AssetRef) (Schema, error)
}

// MaskingProvisioner is an optional capability interface (Phase 5 D-04, RBAC-03/04).
//
// Connectors that can install warehouse-native column masking — Snowflake DDM,
// BigQuery CLS via Data Catalog policy tags — implement this. Connectors that
// cannot (Postgres-as-source, Kafka, REST) MUST NOT implement this; the
// runtime detection pattern in internal/policy/sync_job.go will then mark
// the column as enforcement_mode='in-pipeline' and rely on plan 05-03 for
// pipeline-side masking.
//
// All three methods MUST be safe to call concurrently and MUST honour the
// supplied ctx — Apply/Remove may run for several seconds against the
// warehouse so context cancellation must abort the in-flight DDL.
type MaskingProvisioner interface {
	// ApplyMaskingPolicy installs or replaces the masking policy for the
	// column referenced by ref+policy.Column. Implementations should be
	// idempotent: re-applying the same policy must converge without error.
	ApplyMaskingPolicy(ctx context.Context, ref AssetRef, policy ColumnPolicy) error

	// RemoveMaskingPolicy detaches and removes any masking policy currently
	// applied to (ref, column). MUST NOT error if no policy exists — that
	// case must succeed (Pitfall #4: reconciler retries can otherwise stall).
	RemoveMaskingPolicy(ctx context.Context, ref AssetRef, column string) error

	// ListMaskingPolicies returns the masking policies currently installed
	// on the warehouse for the asset referenced by ref. Used by the Phase 5
	// reconciler (internal/policy/reconciler.go) to detect drift between
	// expected and actual warehouse state.
	ListMaskingPolicies(ctx context.Context, ref AssetRef) ([]ColumnPolicy, error)
}
