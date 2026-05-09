package platform

import (
	"testing"
)

func TestDispatchCommand_UnknownReturns2(t *testing.T) {
	// Save and restore.
	cmdMu.Lock()
	orig := make(map[string]CommandFn)
	for k, v := range commands {
		orig[k] = v
	}
	cmdMu.Unlock()
	defer func() {
		cmdMu.Lock()
		commands = orig
		cmdMu.Unlock()
	}()

	cmdMu.Lock()
	commands = make(map[string]CommandFn)
	cmdMu.Unlock()

	code := DispatchCommand("does-not-exist", nil)
	if code != 2 {
		t.Errorf("expected 2, got %d", code)
	}
}
