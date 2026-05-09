package asset

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func makeTestAsset(name string) *Asset {
	return &Asset{
		name:          name,
		connectorName: "test-connector",
		materializeFn: noopMaterialize,
	}
}

// ---- TestDefinitionRegistry ----

func TestRegistry_Register_Success(t *testing.T) {
	t.Cleanup(resetForTest)
	r := NewDefinitionRegistry()
	a := makeTestAsset("users_clean")

	err := r.Register(a)
	require.NoError(t, err)

	got, err := r.Get("users_clean")
	require.NoError(t, err)
	require.Equal(t, a, got)
}

func TestRegistry_Register_AlreadyRegistered(t *testing.T) {
	t.Cleanup(resetForTest)
	r := NewDefinitionRegistry()

	a := makeTestAsset("dup")
	require.NoError(t, r.Register(a))

	err := r.Register(makeTestAsset("dup"))
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrAlreadyRegistered), "expected ErrAlreadyRegistered, got: %v", err)
}

func TestRegistry_Get_NotFound(t *testing.T) {
	t.Cleanup(resetForTest)
	r := NewDefinitionRegistry()

	_, err := r.Get("nonexistent")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrNotFound), "expected ErrNotFound, got: %v", err)
}

func TestRegistry_List_SortedAlphabetically(t *testing.T) {
	t.Cleanup(resetForTest)
	r := NewDefinitionRegistry()

	for _, name := range []string{"zebra", "apple", "mango"} {
		require.NoError(t, r.Register(makeTestAsset(name)))
	}

	names := r.List()
	require.Equal(t, []string{"apple", "mango", "zebra"}, names)
}

func TestRegistry_Register_NilOrEmpty(t *testing.T) {
	t.Cleanup(resetForTest)
	r := NewDefinitionRegistry()

	err := r.Register(nil)
	require.Error(t, err)

	err = r.Register(&Asset{name: ""})
	require.Error(t, err)
}

// ---- TestOnRegisterHook ----

func TestRegistryOnRegisterHookCalled(t *testing.T) {
	t.Cleanup(resetForTest)
	r := NewDefinitionRegistry()

	callCount := 0
	r.OnRegister = func(a *Asset) error {
		callCount++
		return nil
	}

	a := &Asset{
		name:          "hook_test_asset",
		connectorName: "test-connector",
		materializeFn: noopMaterialize,
	}

	err := r.Register(a)
	require.NoError(t, err)
	require.Equal(t, 1, callCount, "OnRegister hook should be called exactly once")
}

func TestRegistryOnRegisterHookErrorPreservesRegistration(t *testing.T) {
	t.Cleanup(resetForTest)
	r := NewDefinitionRegistry()

	wantErr := errors.New("hook error")
	r.OnRegister = func(a *Asset) error {
		return wantErr
	}

	a := &Asset{
		name:          "hook_error_asset",
		connectorName: "test-connector",
		materializeFn: noopMaterialize,
	}

	err := r.Register(a)
	require.ErrorIs(t, err, wantErr, "OnRegister error should propagate to caller")

	// Despite the hook error, in-memory registration happened.
	got, getErr := r.Get("hook_error_asset")
	require.NoError(t, getErr)
	require.Equal(t, a, got, "in-memory registration should survive hook error")
}

func TestRegistryOnRegisterNilHookIsNoOp(t *testing.T) {
	t.Cleanup(resetForTest)
	r := NewDefinitionRegistry()
	// OnRegister is nil by default — existing behaviour must be unchanged.

	a := &Asset{
		name:          "no_hook_asset",
		connectorName: "test-connector",
		materializeFn: noopMaterialize,
	}
	require.NoError(t, r.Register(a))
}

// ---- TestDefault: process-global singleton ----

func TestDefault_ReturnsSameSingleton(t *testing.T) {
	t.Cleanup(resetForTest)

	d1 := Default()
	d2 := Default()
	require.Same(t, d1, d2, "Default() should return the same pointer every call")
}

func TestResetForTest_ClearsRegistry(t *testing.T) {
	t.Cleanup(resetForTest)

	a := makeTestAsset("temp_asset")
	require.NoError(t, Default().Register(a))

	resetForTest()

	_, err := Default().Get("temp_asset")
	require.True(t, errors.Is(err, ErrNotFound), "after resetForTest, asset should not be found")
}
