//go:build !integration

package schema_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
	"github.com/kanpon/data-governance/internal/event"
	"github.com/kanpon/data-governance/internal/schema"
	"github.com/stretchr/testify/require"
)

// --- Test infrastructure ---

// recordingEventWriter captures event types without a DB.
type recordingEventWriter struct {
	mu    sync.Mutex
	calls []event.Event
}

func (r *recordingEventWriter) Append(_ context.Context, evt event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, evt)
	return nil
}

func (r *recordingEventWriter) lastType() event.EventType {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return ""
	}
	return r.calls[len(r.calls)-1].Type
}

func (r *recordingEventWriter) hasType(t event.EventType) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c.Type == t {
			return true
		}
	}
	return false
}

// noSchemaDescriber is a connector that does NOT implement SchemaDescriber.
type noSchemaDescriber struct{}

func (c *noSchemaDescriber) APIVersion() string { return connector.APIVersion }
func (c *noSchemaDescriber) Ping(_ context.Context, _ connector.PingRequest) (connector.PingResponse, error) {
	return connector.PingResponse{}, nil
}
func (c *noSchemaDescriber) Schema(_ context.Context, _ connector.SchemaRequest) (connector.SchemaResponse, error) {
	return connector.SchemaResponse{}, nil
}
func (c *noSchemaDescriber) Read(_ context.Context, _ connector.ReadRequest) (connector.ReadResponse, error) {
	return connector.ReadResponse{}, nil
}
func (c *noSchemaDescriber) Write(_ context.Context, _ connector.WriteRequest) (connector.WriteResponse, error) {
	return connector.WriteResponse{}, nil
}

// errSchemaDescriber implements SchemaDescriber but returns an error.
type errSchemaDescriber struct {
	noSchemaDescriber
	err error
}

func (e *errSchemaDescriber) DescribeSchema(_ context.Context, _ connector.AssetRef) (connector.Schema, error) {
	return connector.Schema{}, e.err
}

func buildTestAsset(t *testing.T, name string) *asset.Asset {
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

// --- Unit tests ---

func TestCaptureUnsupported(t *testing.T) {
	// Connector does NOT implement SchemaDescriber AND result.Schema is nil.
	// Expected: emit schema.captured with tag=schema_capture_unsupported; no DB writes; return nil.
	rec := &recordingEventWriter{}
	w := schema.NewWriter(rec)

	a := buildTestAsset(t, "test_asset")
	result := asset.MaterializeResult{} // Schema = nil
	conn := &noSchemaDescriber{}
	ref := connector.AssetRef{Identifier: "test_asset"}

	err := w.Capture(context.Background(), nil, uuid.New(), a, result, conn, ref, "codehash123")
	require.NoError(t, err, "Capture should return nil when schema capture is unsupported")

	require.True(t, rec.hasType(event.EventTypeSchemaCaptured),
		"must emit schema.captured with unsupported tag")
}

func TestCaptureDescriberError(t *testing.T) {
	// Connector implements SchemaDescriber but returns an error.
	// Expected: emit schema.capture_failed, return nil (non-fatal).
	rec := &recordingEventWriter{}
	w := schema.NewWriter(rec)

	a := buildTestAsset(t, "test_asset")
	result := asset.MaterializeResult{}
	conn := &errSchemaDescriber{err: errors.New("connection timeout")}
	ref := connector.AssetRef{Identifier: "test_asset"}

	err := w.Capture(context.Background(), nil, uuid.New(), a, result, conn, ref, "codehash123")
	require.NoError(t, err, "Capture should return nil when DescribeSchema errors (non-fatal)")

	require.True(t, rec.hasType(event.EventTypeSchemaCaptureFailed),
		"must emit schema.capture_failed when DescribeSchema returns error")
}

func TestCaptureUnsupportedTagPayload(t *testing.T) {
	// Verify payload contains the expected tag.
	rec := &recordingEventWriter{}
	w := schema.NewWriter(rec)

	a := buildTestAsset(t, "asset_x")
	result := asset.MaterializeResult{}
	conn := &noSchemaDescriber{}
	ref := connector.AssetRef{Identifier: "asset_x"}

	require.NoError(t, w.Capture(context.Background(), nil, uuid.New(), a, result, conn, ref, "hash1"))
	require.Equal(t, event.EventTypeSchemaCaptured, rec.lastType())

	// The last call's payload should have tag = schema_capture_unsupported.
	last := rec.calls[len(rec.calls)-1]
	payload, ok := last.Payload.(map[string]any)
	require.True(t, ok, "payload should be a map")
	require.Equal(t, "schema_capture_unsupported", payload["tag"],
		"payload tag must be schema_capture_unsupported")
}
