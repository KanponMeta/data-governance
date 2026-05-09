//go:build integration

package schema_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/runtime/executortest"
	"github.com/kanpon/data-governance/internal/schema"
	"github.com/kanpon/data-governance/internal/schema/schematest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Integration test helpers (duplicated from non-integration test since build tags separate them) ---

type intgRecordingEventWriter struct {
	mu    sync.Mutex
	calls []event.Event
}

func (r *intgRecordingEventWriter) Append(_ context.Context, evt event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, evt)
	return nil
}

func (r *intgRecordingEventWriter) hasType(t event.EventType) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c.Type == t {
			return true
		}
	}
	return false
}

func (r *intgRecordingEventWriter) callsOfType(t event.EventType) []event.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []event.Event
	for _, c := range r.calls {
		if c.Type == t {
			out = append(out, c)
		}
	}
	return out
}

// intgNoSchemaDescriber is a connector that does NOT implement SchemaDescriber.
type intgNoSchemaDescriber struct{}

func (c *intgNoSchemaDescriber) APIVersion() string { return connector.APIVersion }
func (c *intgNoSchemaDescriber) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{}, nil
}
func (c *intgNoSchemaDescriber) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (c *intgNoSchemaDescriber) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{}, nil
}
func (c *intgNoSchemaDescriber) Write(_ context.Context, _ connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{}, nil
}

// intgSchemaDescriberConn is a connector that implements SchemaDescriber.
type intgSchemaDescriberConn struct {
	intgNoSchemaDescriber
	s connector.Schema
}

func (c *intgSchemaDescriberConn) DescribeSchema(_ context.Context, _ connector.AssetRef) (connector.Schema, error) {
	return c.s, nil
}

func intgBuildTestAsset(t *testing.T, name string) *asset.Asset {
	t.Helper()
	a, err := asset.New(name).
		Connector("test").
		Materialize(func(ctx context.Context, io asset.AssetIO) (asset.MaterializeResult, error) {
			return asset.MaterializeResult{}, nil
		}).
		Build()
	require.NoError(t, err)
	return a
}

// insertBaseSchemaVersion inserts a schema_version row to satisfy FK for schema_changes.
func insertBaseSchemaVersion(t *testing.T, ctx context.Context, c *executortest.Phase4Container,
	assetName string, runID uuid.UUID) uuid.UUID {
	t.Helper()
	vID := uuid.New()
	_, err := c.DB.ExecContext(ctx, `
		INSERT INTO schema_versions
			(id, asset, code_hash, schema_hash, schema_data, captured_at, last_seen_at, last_seen_run_id)
		VALUES ($1, $2, $3, $4, '{}', $5, $5, $6)`,
		vID, assetName, "codehash1", "schemahash1", time.Now().UTC(), runID)
	require.NoError(t, err)
	return vID
}

// --- Tests ---

func TestWriteSchemaChangesAllKinds(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)

	runID := uuid.New()
	assetName := "test_asset_all_kinds"

	// Insert a schema_version to reference as new_version_id.
	newVersionID := insertBaseSchemaVersion(t, ctx, c, assetName, runID)

	// Build one SchemaChange per kind using schematest DiffPairs.
	pairs := schematest.DiffPairs()
	require.Len(t, pairs, 9)

	changes := make([]schema.SchemaChange, 0, len(pairs))
	for _, p := range pairs {
		prev := schematestToConnector(p.PrevSchema)
		next := schematestToConnector(p.NextSchema)
		diff := schema.Diff(prev, next)
		require.NotEmpty(t, diff, "expected diff for %q", p.Name)
		changes = append(changes, diff[0])
	}

	tx, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)

	ids, err := schema.WriteSchemaChanges(ctx, tx, runID, assetName, "codehash1",
		nil, newVersionID, changes)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	// Verify we got 9 IDs back.
	assert.Len(t, ids, 9, "expected 9 inserted change IDs")

	// Verify the rows are in the DB.
	var count int
	require.NoError(t, c.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_changes WHERE asset=$1`, assetName).Scan(&count))
	assert.Equal(t, 9, count)
}

func TestWriteSchemaChangesIsBreakingFlags(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)

	// D-09 breaking changes: column_dropped, type_narrowed, nullable_removed, pk_changed.
	// Non-breaking: column_added, type_widened, nullable_added, comment_changed, default_changed.
	expectedBreaking := map[string]bool{
		"column_dropped":   true,
		"type_narrowed":    true,
		"nullable_removed": true,
		"pk_changed":       true,
		"column_added":     false,
		"type_widened":     false,
		"nullable_added":   false,
		"comment_changed":  false,
		"default_changed":  false,
	}

	runID := uuid.New()
	assetName := "test_asset_breaking"
	newVersionID := insertBaseSchemaVersion(t, ctx, c, assetName, runID)

	pairs := schematest.DiffPairs()
	for _, p := range pairs {
		prev := schematestToConnector(p.PrevSchema)
		next := schematestToConnector(p.NextSchema)
		diff := schema.Diff(prev, next)
		require.NotEmpty(t, diff)

		tx, err := c.DB.BeginTx(ctx, nil)
		require.NoError(t, err)

		_, err = schema.WriteSchemaChanges(ctx, tx, runID, assetName, "codehash1",
			nil, newVersionID, diff[:1])
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
	}

	// Read all schema_changes and verify is_breaking matches expected.
	rows, err := c.DB.QueryContext(ctx,
		`SELECT change_type, is_breaking FROM schema_changes WHERE asset=$1`, assetName)
	require.NoError(t, err)
	defer rows.Close()

	for rows.Next() {
		var changeType string
		var isBreaking bool
		require.NoError(t, rows.Scan(&changeType, &isBreaking))

		want, known := expectedBreaking[changeType]
		if known {
			assert.Equal(t, want, isBreaking,
				"change_type=%s is_breaking mismatch", changeType)
		}
	}
	require.NoError(t, rows.Err())
}

func TestWriteSchemaChangesPKChange(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)

	runID := uuid.New()
	assetName := "test_asset_pk"
	newVersionID := insertBaseSchemaVersion(t, ctx, c, assetName, runID)

	// PK changed — column_name should be NULL in DB.
	pkChange := schema.SchemaChange{Kind: schema.ChangePKChanged}

	tx, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)

	ids, err := schema.WriteSchemaChanges(ctx, tx, runID, assetName, "codehash1",
		nil, newVersionID, []schema.SchemaChange{pkChange})
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.Len(t, ids, 1)

	var changeType string
	var columnName *string
	var isBreaking bool
	err = c.DB.QueryRowContext(ctx,
		`SELECT change_type, column_name, is_breaking FROM schema_changes WHERE id=$1`,
		ids[0]).Scan(&changeType, &columnName, &isBreaking)
	require.NoError(t, err)

	assert.Equal(t, "pk_changed", changeType)
	assert.Nil(t, columnName, "pk_changed should have NULL column_name")
	assert.True(t, isBreaking)
}

func TestWriteSchemaChangesAtomicity(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)

	runID := uuid.New()
	assetName := "test_asset_atomic"
	newVersionID := insertBaseSchemaVersion(t, ctx, c, assetName, runID)

	changes := []schema.SchemaChange{
		{Kind: schema.ChangeColumnAdded, ColumnName: "new_col"},
	}

	tx, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)

	_, err = schema.WriteSchemaChanges(ctx, tx, runID, assetName, "codehash1",
		nil, newVersionID, changes)
	require.NoError(t, err)

	// ROLLBACK — changes should not be persisted.
	require.NoError(t, tx.Rollback())

	var count int
	require.NoError(t, c.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_changes WHERE asset=$1`, assetName).Scan(&count))
	assert.Equal(t, 0, count, "rolled-back tx should leave 0 rows")
}

func TestWriteSchemaChangesPrevVersionID(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)

	runID := uuid.New()
	assetName := "test_asset_prev"

	prevVersionID := insertBaseSchemaVersion(t, ctx, c, assetName, runID)
	newVersionID := insertBaseSchemaVersion(t, ctx, c, assetName, runID)

	changes := []schema.SchemaChange{
		{Kind: schema.ChangeColumnAdded, ColumnName: "extra"},
	}

	tx, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)

	ids, err := schema.WriteSchemaChanges(ctx, tx, runID, assetName, "codehash1",
		&prevVersionID, newVersionID, changes)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	require.Len(t, ids, 1)

	var gotPrevVersionID *string
	err = c.DB.QueryRowContext(ctx,
		`SELECT prev_version_id::text FROM schema_changes WHERE id=$1`, ids[0]).
		Scan(&gotPrevVersionID)
	require.NoError(t, err)
	require.NotNil(t, gotPrevVersionID)
	assert.Equal(t, prevVersionID.String(), *gotPrevVersionID)
}

func TestCaptureWithDiff(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)

	rec := &intgRecordingEventWriter{}
	w := schema.NewWriter(rec)

	// Schema A — initial capture.
	schemaA := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Nullable: false, IsPrimaryKey: true},
			{Name: "foo", Type: "text", Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	a := intgBuildTestAsset(t, "capture_diff_asset")

	// First capture: Schema A → schema_versions has 1 row, schema_changes has 0 rows.
	tx1, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = w.Capture(ctx, tx1, uuid.New(), a, asset.MaterializeResult{},
		&intgSchemaDescriberConn{s: schemaA},
		connector.AssetRef{Identifier: "capture_diff_asset"}, "code_v1")
	require.NoError(t, err)
	require.NoError(t, tx1.Commit())

	// Verify: 1 schema_versions row, 0 schema_changes rows.
	var vCount, cCount int
	require.NoError(t, c.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_versions WHERE asset='capture_diff_asset'`).Scan(&vCount))
	require.NoError(t, c.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_changes WHERE asset='capture_diff_asset'`).Scan(&cCount))
	assert.Equal(t, 1, vCount, "first capture: 1 schema_version row")
	assert.Equal(t, 0, cCount, "first capture: 0 schema_changes rows (no prev)")

	// Schema B = Schema A + new column "extra" + dropped column "foo".
	schemaB := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "extra", Type: "varchar(100)", Nullable: true},
			{Name: "id", Type: "int64", Nullable: false, IsPrimaryKey: true},
		},
		PrimaryKey: []string{"id"},
	}

	// Second capture: Schema B → schema_versions has 2 rows, schema_changes has 2 rows.
	tx2, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = w.Capture(ctx, tx2, uuid.New(), a, asset.MaterializeResult{},
		&intgSchemaDescriberConn{s: schemaB},
		connector.AssetRef{Identifier: "capture_diff_asset"}, "code_v2")
	require.NoError(t, err)
	require.NoError(t, tx2.Commit())

	require.NoError(t, c.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_versions WHERE asset='capture_diff_asset'`).Scan(&vCount))
	require.NoError(t, c.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_changes WHERE asset='capture_diff_asset'`).Scan(&cCount))
	assert.Equal(t, 2, vCount, "second capture: 2 schema_version rows")
	assert.Equal(t, 2, cCount, "second capture: 2 schema_changes rows (added+dropped)")

	// Verify the SECOND schema.change_detected event (from second capture) has
	// non-empty schema_changes_ids. The first event is emitted on initial capture
	// (no prev) and legitimately has empty schema_changes_ids.
	changeDetectedEvents := rec.callsOfType(event.EventTypeSchemaChangeDetected)
	require.GreaterOrEqual(t, len(changeDetectedEvents), 2,
		"must have at least 2 schema.change_detected events (first=initial, second=diff)")

	// The last event is from the second capture (with diff).
	lastEvent := changeDetectedEvents[len(changeDetectedEvents)-1]
	payload, ok := lastEvent.Payload.(map[string]any)
	require.True(t, ok)
	ids, hasIDs := payload["schema_changes_ids"]
	assert.True(t, hasIDs, "schema.change_detected must have schema_changes_ids key")
	assert.NotEmpty(t, ids, "schema_changes_ids should not be empty on diff capture")
}

func TestCaptureWithDedupAfterChange(t *testing.T) {
	ctx := context.Background()
	c := executortest.StartPhase4Container(ctx, t)
	c.Reset(ctx, t)

	rec := &intgRecordingEventWriter{}
	w := schema.NewWriter(rec)

	schemaA := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Nullable: false},
		},
		PrimaryKey: []string{"id"},
	}
	schemaB := connector.Schema{
		Columns: []connector.SchemaColumn{
			{Name: "id", Type: "int64", Nullable: false},
			{Name: "new_col", Type: "text", Nullable: true},
		},
		PrimaryKey: []string{"id"},
	}

	a := intgBuildTestAsset(t, "dedup_after_change_asset")

	// First capture: Schema A.
	tx1, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = w.Capture(ctx, tx1, uuid.New(), a, asset.MaterializeResult{},
		&intgSchemaDescriberConn{s: schemaA},
		connector.AssetRef{Identifier: "dedup_after_change_asset"}, "code1")
	require.NoError(t, err)
	require.NoError(t, tx1.Commit())

	// Second capture: Schema B (adds new_col).
	tx2, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = w.Capture(ctx, tx2, uuid.New(), a, asset.MaterializeResult{},
		&intgSchemaDescriberConn{s: schemaB},
		connector.AssetRef{Identifier: "dedup_after_change_asset"}, "code2")
	require.NoError(t, err)
	require.NoError(t, tx2.Commit())

	// Third capture: Schema B again (dedup).
	tx3, err := c.DB.BeginTx(ctx, nil)
	require.NoError(t, err)
	err = w.Capture(ctx, tx3, uuid.New(), a, asset.MaterializeResult{},
		&intgSchemaDescriberConn{s: schemaB},
		connector.AssetRef{Identifier: "dedup_after_change_asset"}, "code3")
	require.NoError(t, err)
	require.NoError(t, tx3.Commit())

	// Verify: schema_versions still has 2 rows (dedup = no new insert).
	var vCount, cCount int
	require.NoError(t, c.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_versions WHERE asset='dedup_after_change_asset'`).Scan(&vCount))
	require.NoError(t, c.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_changes WHERE asset='dedup_after_change_asset'`).Scan(&cCount))
	assert.Equal(t, 2, vCount, "dedup: 3rd capture should not add a new schema_version row")
	assert.Equal(t, 1, cCount, "dedup: 3rd capture should not add new schema_changes rows")

	// schema.unchanged should be emitted on third capture.
	assert.True(t, rec.hasType(event.EventTypeSchemaUnchanged),
		"schema.unchanged must be emitted on dedup")
}
