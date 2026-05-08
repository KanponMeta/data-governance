package main_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchedulerGracefulShutdown spawns the platform binary in scheduler mode,
// sends SIGTERM after 1s, and asserts:
//   - process exits with code 0 within 5s
//   - stdout contains "scheduler.started" and "scheduler.shutdown" log lines
//
// Validates D-05 graceful shutdown semantics — current tick completes, no new ticks,
// daemon exits cleanly on operator signal.
func TestSchedulerGracefulShutdown(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires DATABASE_URL")
	}
	// Build the platform binary into a temp dir.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "platform")
	// The test runs from cmd/platform/; repo root is two levels up.
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err, "resolve repo root")

	buildCmd := exec.Command("go", "build", "-o", bin, "./cmd/platform")
	buildCmd.Dir = repoRoot
	buildCmd.Env = os.Environ()
	buildOut, buildErr := buildCmd.CombinedOutput()
	require.NoError(t, buildErr, "go build failed: %s", string(buildOut))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run ./platform scheduler with a short interval to trigger tick logs quickly.
	cmd := exec.CommandContext(ctx, bin, "scheduler")
	cmd.Env = append(os.Environ(),
		"PLATFORM_SCHEDULER_INTERVAL=100ms",
		"PLATFORM_SCHEDULER_SHUTDOWN_TIMEOUT=2s",
	)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	require.NoError(t, cmd.Start())

	// Wait 1s, then send SIGTERM.
	time.Sleep(1 * time.Second)
	require.NoError(t, cmd.Process.Signal(syscall.SIGTERM))

	// Wait up to 5s for graceful exit.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		assert.NoError(t, err, "scheduler exited with non-zero code; output: %s", out.String())
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("scheduler did not shut down within 5s after SIGTERM; output: %s", out.String())
	}

	output := out.String()
	assert.True(t, strings.Contains(output, "scheduler.started"),
		"expected 'scheduler.started' log line, got: %s", output)
	assert.True(t, strings.Contains(output, "scheduler.shutdown"),
		"expected 'scheduler.shutdown' log line, got: %s", output)
}
