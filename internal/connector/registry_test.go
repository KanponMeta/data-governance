package connector

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeConnector is a test double that satisfies the Connector interface.
// Its apiVersion field can be set to simulate version mismatches.
type fakeConnector struct {
	apiVersion string
}

func (f *fakeConnector) APIVersion() string { return f.apiVersion }

func (f *fakeConnector) Ping(ctx context.Context, req PingRequest) (PingResponse, error) {
	return PingResponse{}, nil
}

func (f *fakeConnector) Schema(ctx context.Context, req SchemaRequest) (SchemaResponse, error) {
	return SchemaResponse{}, nil
}

func (f *fakeConnector) Read(ctx context.Context, req ReadRequest) (ReadResponse, error) {
	return ReadResponse{}, nil
}

func (f *fakeConnector) Write(ctx context.Context, req WriteRequest) (WriteResponse, error) {
	return WriteResponse{}, nil
}

// --- Test cases ---

func TestRegistry_Register_Success(t *testing.T) {
	reg := NewRegistry()
	c := &fakeConnector{apiVersion: APIVersion}

	err := reg.Register("test-connector", c)

	if err != nil {
		t.Fatalf("Register returned error: %v", err)
	}

	got, err := reg.Get("test-connector")
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if got != c {
		t.Errorf("Get returned %v, want %v", got, c)
	}
}

func TestRegistry_Register_IncompatibleVersion(t *testing.T) {
	reg := NewRegistry()
	c := &fakeConnector{apiVersion: "v99.0.0"}

	err := reg.Register("bad-version", c)

	if !errors.Is(err, ErrIncompatibleVersion) {
		t.Errorf("Register returned %v, want ErrIncompatibleVersion", err)
	}

	_, err = reg.Get("bad-version")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after failed Register returned %v, want ErrNotFound", err)
	}
}

func TestRegistry_Register_AlreadyRegistered(t *testing.T) {
	reg := NewRegistry()
	c := &fakeConnector{apiVersion: APIVersion}

	err1 := reg.Register("duplicate", c)
	if err1 != nil {
		t.Fatalf("first Register returned error: %v", err1)
	}

	err2 := reg.Register("duplicate", c)
	if !errors.Is(err2, ErrAlreadyRegistered) {
		t.Errorf("second Register returned %v, want ErrAlreadyRegistered", err2)
	}
}

func TestRegistry_Get_NotFound(t *testing.T) {
	reg := NewRegistry()

	_, err := reg.Get("nonexistent")

	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Get returned %v, want ErrNotFound", err)
	}
}

func TestRegistry_List_Sorted(t *testing.T) {
	reg := NewRegistry()

	// Register in non-sorted order.
	registerOrder := []string{"zebra", "apple", "mango"}
	for _, name := range registerOrder {
		reg.Register(name, &fakeConnector{apiVersion: APIVersion})
	}

	names := reg.List()

	// Verify sorted.
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("List() = %v, not sorted", names)
		}
	}

	// Verify all names present.
	if len(names) != len(registerOrder) {
		t.Errorf("List() length = %d, want %d", len(names), len(registerOrder))
	}
}

func TestRegistry_Concurrent(t *testing.T) {
	reg := NewRegistry()

	var wg sync.WaitGroup
	const goroutines = 100

	// Spawn goroutines that register unique connectors and list all connectors.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := strings.Repeat("c", idx+1) // unique name each time
			c := &fakeConnector{apiVersion: APIVersion}
			_ = reg.Register(name, c) // ignore error — duplicates possible with high concurrency
			_ = reg.List()
		}(i)
	}

	wg.Wait()

	// If we got here without data races (detected by -race flag), the test passes.
}

func TestRegistry_ConcurrentMixed(t *testing.T) {
	reg := NewRegistry()

	var wg sync.WaitGroup
	const goroutines = 100

	// Pre-register some connectors.
	for i := 0; i < 10; i++ {
		name := strings.Repeat("pre", i+1)
		reg.Register(name, &fakeConnector{apiVersion: APIVersion})
	}

	// Concurrent: half register new connectors, half call List.
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				name := strings.Repeat("new", idx+1)
				reg.Register(name, &fakeConnector{apiVersion: APIVersion})
			} else {
				_ = reg.List()
			}
		}(i)
	}

	wg.Wait()
}
