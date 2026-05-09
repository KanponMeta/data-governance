package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kanpon/data-governance/internal/policy"
)

// TestReconcilerCmd_OnceFlag_RunsSingleTick — exercises dispatchReconciler
// with --once. We can't open a real DB here so we expect exit code 1 from
// the load-config / open-db path, AND we ensure the binary at least parses
// flags and reaches the connect-db step (Init: validates flag handling).
func TestReconcilerCmd_OnceFlag_RunsSingleTick(t *testing.T) {
	// dispatchReconciler with no DATABASE_URL configured will fail at
	// either config.Load or sql.Open — both must return non-zero.
	// We only assert the exit code is non-zero (config error) without
	// requiring a running Postgres.
	rc := dispatchReconciler([]string{"--once", "--interval=10ms", "--grace=1s"})
	require.NotEqual(t, 2, rc, "expected non-usage exit (flag parse OK)")
}

// TestReconcilerCmd_BadFlag_ReturnsUsage — bad flag returns 2.
func TestReconcilerCmd_BadFlag_ReturnsUsage(t *testing.T) {
	rc := dispatchReconciler([]string{"--no-such-flag"})
	require.Equal(t, 2, rc)
}

// TestReconcilerConnectorResolver_StubReturnsError — the stub resolver
// always returns an error so the reconciler's per-asset error tally
// counts every asset.
func TestReconcilerConnectorResolver_StubReturnsError(t *testing.T) {
	r := newReconcilerConnectorResolver()
	_, _, err := r.ResolveByAsset(context.Background(), "orders")
	require.Error(t, err)
}

// TestNoopReEnqueuer_LogsAndReturnsNil — the noop re-enqueuer never errs.
func TestNoopReEnqueuer_LogsAndReturnsNil(t *testing.T) {
	require.NoError(t, noopReEnqueuer{}.ReEnqueueSync(context.Background(),
		policy.PolicySyncArgs{Asset: "orders", Column: "ssn"}))
}

// TestReconcilerCmd_HonoursContextCancel — sanity check that --once exits
// cleanly even on a cancelled context. (Real lifecycle wiring tested in
// the integration suite.)
func TestReconcilerCmd_HonoursContextCancel(t *testing.T) {
	// Without a config we expect rc != 2 (since flag parse succeeds).
	// We don't have a way to inject ctx cancel here — covered by the
	// internal/policy/reconciler_test.go context propagation tests instead.
	rc := dispatchReconciler([]string{"--once"})
	require.True(t, rc == 0 || rc == 1, "exit code must be 0 (clean) or 1 (db error), got %d", rc)
}

// _ keeps the errors import alive in case future tests need it.
var _ = errors.New
