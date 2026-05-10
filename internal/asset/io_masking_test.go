package asset_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/asset"
	"github.com/kanpon/data-governance/internal/connector"
)

// fakeIO records calls so tests can assert what reached the inner AssetIO.
type fakeIO struct {
	mu          sync.Mutex
	writeCalls  int
	lastRows    []connector.Row
	readCalls   int
	readReturn  []connector.Row
	partition   string
	writeErr    error
	rowsWritten int64
}

func (f *fakeIO) Read(_ context.Context, _ string) ([]connector.Row, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readCalls++
	return f.readReturn, nil
}
func (f *fakeIO) Write(_ context.Context, rows []connector.Row) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writeCalls++
	cp := make([]connector.Row, len(rows))
	for i, r := range rows {
		fields := make(map[string]any, len(r.Fields))
		for k, v := range r.Fields {
			fields[k] = v
		}
		cp[i] = connector.Row{Fields: fields}
	}
	f.lastRows = cp
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return f.rowsWritten, nil
}
func (f *fakeIO) PartitionKey() string { return f.partition }

// fakeApply lets tests record the (mt, value, reveal) triples MaskingIO
// dispatched, and substitute deterministic outputs.
type fakeApply struct {
	mu    sync.Mutex
	calls []struct {
		Mask   connector.MaskType
		Value  string
		Reveal int
	}
	transform func(mt connector.MaskType, v string, reveal int) (string, error)
}

func (f *fakeApply) Apply(mt connector.MaskType, v string, reveal int) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, struct {
		Mask   connector.MaskType
		Value  string
		Reveal int
	}{mt, v, reveal})
	f.mu.Unlock()
	if f.transform != nil {
		return f.transform(mt, v, reveal)
	}
	return v, nil
}

// TestMaskingIO_NoRules_PassesThrough — 0 rules means Write is a no-op
// pass-through; the apply func is never invoked.
func TestMaskingIO_NoRules_PassesThrough(t *testing.T) {
	inner := &fakeIO{rowsWritten: 5}
	apply := &fakeApply{}
	io := asset.NewMaskingIO(inner, "orders", nil, apply.Apply)

	rows := []connector.Row{{Fields: map[string]any{"id": 1, "name": "alice"}}}
	n, err := io.Write(context.Background(), rows)
	require.NoError(t, err)
	require.Equal(t, int64(5), n)
	require.Equal(t, 1, inner.writeCalls)
	require.Empty(t, apply.calls, "apply MUST NOT be called when there are no rules")
	require.Equal(t, "alice", inner.lastRows[0].Fields["name"], "value passed through unchanged")
}

// TestMaskingIO_HashesSSNColumn — one rule on column "ssn"; inner.Write
// receives the row with ssn replaced.
func TestMaskingIO_HashesSSNColumn(t *testing.T) {
	inner := &fakeIO{rowsWritten: 1}
	apply := &fakeApply{
		transform: func(_ connector.MaskType, _ string, _ int) (string, error) {
			return "deadbeef", nil
		},
	}
	io := asset.NewMaskingIO(inner, "orders", []asset.MaskRule{
		{Column: "ssn", Mask: connector.MaskHash},
	}, apply.Apply)

	rows := []connector.Row{{Fields: map[string]any{"id": 1, "ssn": "123-45-6789"}}}
	_, err := io.Write(context.Background(), rows)
	require.NoError(t, err)

	require.Len(t, apply.calls, 1)
	require.Equal(t, connector.MaskHash, apply.calls[0].Mask)
	require.Equal(t, "123-45-6789", apply.calls[0].Value)

	require.Equal(t, "deadbeef", inner.lastRows[0].Fields["ssn"])
	require.Equal(t, 1, inner.lastRows[0].Fields["id"], "non-rule column unchanged")
}

// TestMaskingIO_RedactsEmail — Mask=Redact replaces with "***".
func TestMaskingIO_RedactsEmail(t *testing.T) {
	inner := &fakeIO{rowsWritten: 1}
	apply := &fakeApply{
		transform: func(_ connector.MaskType, _ string, _ int) (string, error) {
			return "***", nil
		},
	}
	io := asset.NewMaskingIO(inner, "users", []asset.MaskRule{
		{Column: "email", Mask: connector.MaskRedact},
	}, apply.Apply)

	rows := []connector.Row{{Fields: map[string]any{"email": "alice@example.com"}}}
	_, err := io.Write(context.Background(), rows)
	require.NoError(t, err)
	require.Equal(t, "***", inner.lastRows[0].Fields["email"])
}

// TestMaskingIO_PartialEmail_Reveals2 — partial reveal=2 produces
// "al*************om".
func TestMaskingIO_PartialEmail_Reveals2(t *testing.T) {
	inner := &fakeIO{rowsWritten: 1}
	apply := &fakeApply{
		transform: func(_ connector.MaskType, v string, reveal int) (string, error) {
			if reveal == 0 {
				reveal = 2
			}
			return v[:reveal] + strings.Repeat("*", len(v)-2*reveal) + v[len(v)-reveal:], nil
		},
	}
	io := asset.NewMaskingIO(inner, "users", []asset.MaskRule{
		{Column: "email", Mask: connector.MaskPartial, Reveal: 2},
	}, apply.Apply)

	rows := []connector.Row{{Fields: map[string]any{"email": "alice@example.com"}}}
	_, err := io.Write(context.Background(), rows)
	require.NoError(t, err)
	require.Equal(t, "al*************om", inner.lastRows[0].Fields["email"])
	require.Equal(t, 2, apply.calls[0].Reveal, "Reveal must be plumbed through")
}

// TestMaskingIO_PreservesNonRuleColumns — id (not in rules) passes through
// unchanged and unmasked.
func TestMaskingIO_PreservesNonRuleColumns(t *testing.T) {
	inner := &fakeIO{rowsWritten: 1}
	apply := &fakeApply{
		transform: func(_ connector.MaskType, _ string, _ int) (string, error) { return "X", nil },
	}
	io := asset.NewMaskingIO(inner, "orders", []asset.MaskRule{
		{Column: "ssn", Mask: connector.MaskHash},
	}, apply.Apply)

	rows := []connector.Row{{Fields: map[string]any{"id": 42, "ssn": "secret"}}}
	_, err := io.Write(context.Background(), rows)
	require.NoError(t, err)
	require.Equal(t, 42, inner.lastRows[0].Fields["id"])
	require.Equal(t, "X", inner.lastRows[0].Fields["ssn"])
}

// TestMaskingIO_SkipsNonStringValues — non-string column values pass
// through unchanged in v1 (Apply is not called for them).
func TestMaskingIO_SkipsNonStringValues(t *testing.T) {
	inner := &fakeIO{rowsWritten: 1}
	apply := &fakeApply{
		transform: func(_ connector.MaskType, _ string, _ int) (string, error) { return "***", nil },
	}
	io := asset.NewMaskingIO(inner, "orders", []asset.MaskRule{
		{Column: "amount", Mask: connector.MaskRedact},
	}, apply.Apply)

	rows := []connector.Row{{Fields: map[string]any{"amount": 1000}}}
	_, err := io.Write(context.Background(), rows)
	require.NoError(t, err)
	require.Empty(t, apply.calls, "Apply must NOT be called for non-string values in v1")
	require.Equal(t, 1000, inner.lastRows[0].Fields["amount"])
}

// TestMaskingIO_ReadAndPartitionKey_PassThrough — Read and PartitionKey
// are not transformed by MaskingIO.
func TestMaskingIO_ReadAndPartitionKey_PassThrough(t *testing.T) {
	inner := &fakeIO{readReturn: []connector.Row{{Fields: map[string]any{"x": "y"}}}, partition: "2024-05-01"}
	apply := &fakeApply{}
	io := asset.NewMaskingIO(inner, "asset", []asset.MaskRule{{Column: "any", Mask: connector.MaskHash}}, apply.Apply)

	rows, err := io.Read(context.Background(), "upstream")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "2024-05-01", io.PartitionKey())
	require.Empty(t, apply.calls, "Apply MUST NOT be called on the Read path")
}

// TestMaskingIO_Concurrent_Write — 10 goroutines × 10 Write calls each;
// run with -race to detect data races.
func TestMaskingIO_Concurrent_Write(t *testing.T) {
	inner := &fakeIO{rowsWritten: 1}
	apply := &fakeApply{
		transform: func(_ connector.MaskType, v string, _ int) (string, error) { return v + "-masked", nil },
	}
	io := asset.NewMaskingIO(inner, "orders", []asset.MaskRule{
		{Column: "ssn", Mask: connector.MaskHash},
	}, apply.Apply)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(_ int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				rows := []connector.Row{{Fields: map[string]any{"ssn": "secret"}}}
				_, _ = io.Write(context.Background(), rows)
			}
		}(i)
	}
	wg.Wait()
	require.Equal(t, 100, inner.writeCalls)
}

// TestMaskingIO_PropagatesApplyError — when the apply func returns an
// error, MaskingIO surfaces it instead of writing.
func TestMaskingIO_PropagatesApplyError(t *testing.T) {
	inner := &fakeIO{rowsWritten: 1}
	apply := &fakeApply{
		transform: func(_ connector.MaskType, _ string, _ int) (string, error) {
			return "", errors.New("salt-missing")
		},
	}
	io := asset.NewMaskingIO(inner, "orders", []asset.MaskRule{{Column: "ssn", Mask: connector.MaskHash}}, apply.Apply)

	_, err := io.Write(context.Background(), []connector.Row{{Fields: map[string]any{"ssn": "x"}}})
	require.Error(t, err)
	require.Equal(t, 0, inner.writeCalls, "inner.Write MUST NOT be invoked on apply error")
}

// TestMaskingIO_NilApply_ReturnsError — calling NewMaskingIO without an
// apply function fails the first Write loudly rather than silently passing
// values through unmasked.
func TestMaskingIO_NilApply_ReturnsError(t *testing.T) {
	inner := &fakeIO{rowsWritten: 1}
	io := asset.NewMaskingIO(inner, "orders", []asset.MaskRule{{Column: "ssn", Mask: connector.MaskHash}}, nil)

	_, err := io.Write(context.Background(), []connector.Row{{Fields: map[string]any{"ssn": "x"}}})
	require.Error(t, err)
}
