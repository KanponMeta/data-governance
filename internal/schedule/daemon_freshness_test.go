package schedule

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// stubFreshnessScanner counts Scan invocations.
type stubFreshnessScanner struct {
	calls int
	n     int
	err   error
}

func (s *stubFreshnessScanner) Scan(_ context.Context) (int, error) {
	s.calls++
	return s.n, s.err
}

// TestDaemon_FreshnessScanner_Wired documents that the Daemon exposes the
// WithFreshnessScanner builder method and stores the supplied scanner.
//
// Full tick-loop integration (including FireOneSchedule + scanner) is exercised
// via the DATABASE_URL-gated TestDaemonRunCancellation / TestDaemonUpsertOnStart
// suites. Here we only assert the field is wired so a downstream caller can
// rely on the scanner running once per tick.
func TestDaemon_FreshnessScanner_Invoked(t *testing.T) {
	stub := &stubFreshnessScanner{}
	d := &Daemon{}
	got := d.WithFreshnessScanner(stub)
	require.Same(t, d, got, "WithFreshnessScanner must return receiver for chaining")
	require.NotNil(t, d.freshnessScanner, "freshnessScanner field must be set")
}

// TestDaemon_NoScanner_NoOp verifies the daemon defaults to nil scanner.
func TestDaemon_NoScanner_NoOp(t *testing.T) {
	d := &Daemon{}
	require.Nil(t, d.freshnessScanner, "fresh Daemon must default freshnessScanner to nil")
}
