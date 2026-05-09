package asset

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/kanpon/data-governance/internal/connector"
	"github.com/stretchr/testify/require"
)

// fakeInnerIO is a test AssetIO that records Write calls and returns fixed Read rows.
type fakeInnerIO struct {
	mu          sync.Mutex
	readRows    []connector.Row
	readErr     error
	writeRows   []connector.Row
	writeResult int64
	writeErr    error
	pk          string
}

func (f *fakeInnerIO) Read(_ context.Context, _ string) ([]connector.Row, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.readRows, nil
}

func (f *fakeInnerIO) Write(_ context.Context, rows []connector.Row) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeRows = append(f.writeRows, rows...)
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.writeResult, nil
}

func (f *fakeInnerIO) PartitionKey() string { return f.pk }

func TestTrackingIORecordsRead(t *testing.T) {
	inner := &fakeInnerIO{}
	tracker := NewTrackingIO(inner)

	ctx := context.Background()
	_, _ = tracker.Read(ctx, "a")
	_, _ = tracker.Read(ctx, "b")
	_, _ = tracker.Read(ctx, "a") // duplicate — should be deduplicated

	got := tracker.Observed()
	require.Equal(t, []string{"a", "b"}, got, "Observed() should return sorted, deduplicated upstream names")
}

func TestTrackingIORecordsOnError(t *testing.T) {
	inner := &fakeInnerIO{readErr: ErrUnknownUpstream}
	tracker := NewTrackingIO(inner)

	ctx := context.Background()
	_, err := tracker.Read(ctx, "upstream_x")
	require.Error(t, err, "Read should still return the inner error")
	require.True(t, errors.Is(err, ErrUnknownUpstream))

	// Even on error, the attempted upstream name should be recorded.
	got := tracker.Observed()
	require.Equal(t, []string{"upstream_x"}, got, "Observed() should record the upstream even on read error")
}

func TestTrackingIOEmpty(t *testing.T) {
	inner := &fakeInnerIO{}
	tracker := NewTrackingIO(inner)

	// Never call Read.
	got := tracker.Observed()
	require.NotNil(t, got, "Observed() should return non-nil slice even when empty")
	require.Len(t, got, 0, "Observed() should return empty slice when Read was never called")
}

func TestTrackingIOConcurrent(t *testing.T) {
	inner := &fakeInnerIO{}
	tracker := NewTrackingIO(inner)

	ctx := context.Background()
	var wg sync.WaitGroup
	const n = 50

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("u_%d", idx)
			_, _ = tracker.Read(ctx, name)
		}(i)
	}
	wg.Wait()

	got := tracker.Observed()
	require.Len(t, got, n, "Observed() should contain exactly %d unique upstream names", n)
	require.True(t, sort.StringsAreSorted(got), "Observed() should return sorted slice")
}

func TestTrackingIOWritePassThrough(t *testing.T) {
	inner := &fakeInnerIO{writeResult: 42}
	tracker := NewTrackingIO(inner)

	rows := []connector.Row{{Fields: map[string]any{"k": "v"}}}
	ctx := context.Background()
	n, err := tracker.Write(ctx, rows)
	require.NoError(t, err)
	require.Equal(t, int64(42), n, "Write should return inner's row count")
	require.Equal(t, rows, inner.writeRows, "Write should delegate rows to inner")
}

func TestTrackingIOPartitionKeyPassThrough(t *testing.T) {
	inner := &fakeInnerIO{pk: "p1"}
	tracker := NewTrackingIO(inner)

	require.Equal(t, "p1", tracker.PartitionKey(), "PartitionKey() should delegate to inner")
}
