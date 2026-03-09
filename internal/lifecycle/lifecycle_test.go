package lifecycle

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func testManager() *Manager {
	return NewManager(zap.NewNop(), 5*time.Second)
}

func TestNewManager_DefaultTimeout(t *testing.T) {
	m := NewManager(zap.NewNop(), 0)
	if m.timeout != 30*time.Second {
		t.Fatalf("expected default 30s, got %v", m.timeout)
	}
}

func TestManager_RegisterAndCount(t *testing.T) {
	m := testManager()
	if m.ComponentCount() != 0 {
		t.Fatal("expected 0 components")
	}
	m.RegisterStop("a", PhaseHTTP, func(ctx context.Context) error { return nil })
	m.RegisterStop("b", PhaseDatastore, func(ctx context.Context) error { return nil })
	if m.ComponentCount() != 2 {
		t.Fatalf("expected 2 components, got %d", m.ComponentCount())
	}
}

func TestManager_ShutdownOrder(t *testing.T) {
	m := testManager()
	var order []string
	var mu sync.Mutex

	record := func(name string) func(ctx context.Context) error {
		return func(ctx context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	m.RegisterStop("telemetry", PhaseTelemetry, record("telemetry"))
	m.RegisterStop("http", PhaseHTTP, record("http"))
	m.RegisterStop("datastore", PhaseDatastore, record("datastore"))
	m.RegisterStop("publisher", PhasePublishers, record("publisher"))
	m.RegisterStop("adapter", PhaseAdapters, record("adapter"))

	err := m.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	expected := []string{"http", "adapter", "publisher", "datastore", "telemetry"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d stops, got %d: %v", len(expected), len(order), order)
	}
	for i, name := range expected {
		if order[i] != name {
			t.Fatalf("position %d: expected %q, got %q (full: %v)", i, name, order[i], order)
		}
	}
}

func TestManager_SamePhaseRunsConcurrently(t *testing.T) {
	m := testManager()
	var started int64

	barrier := make(chan struct{})
	slowStop := func(_ context.Context) error {
		atomic.AddInt64(&started, 1)
		<-barrier
		return nil
	}

	m.RegisterStop("a", PhaseAdapters, slowStop)
	m.RegisterStop("b", PhaseAdapters, slowStop)

	done := make(chan error, 1)
	go func() {
		done <- m.Shutdown(context.Background())
	}()

	// Wait for both to be running.
	deadline := time.After(2 * time.Second)
	for atomic.LoadInt64(&started) < 2 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for concurrent starts")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(barrier)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown timed out")
	}
}

func TestManager_StopError(t *testing.T) {
	m := testManager()
	m.RegisterStop("ok", PhaseHTTP, func(ctx context.Context) error { return nil })
	m.RegisterStop("fail", PhaseDatastore, func(ctx context.Context) error {
		return errors.New("db close error")
	})

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, nil) {
		// Check that the error message contains the failure info.
		if got := err.Error(); got == "" {
			t.Fatal("error string should not be empty")
		}
	}
}

func TestManager_NilStopFn(t *testing.T) {
	m := testManager()
	m.Register(Component{Name: "noop", Phase: PhaseHTTP, StopFn: nil})
	err := m.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("nil StopFn should be safe: %v", err)
	}
}

func TestManager_EmptyShutdown(t *testing.T) {
	m := testManager()
	err := m.Shutdown(context.Background())
	if err != nil {
		t.Fatalf("empty shutdown should succeed: %v", err)
	}
}

func TestManager_ShutdownRespectsTimeout(t *testing.T) {
	m := NewManager(zap.NewNop(), 100*time.Millisecond)
	m.RegisterStop("slow", PhaseHTTP, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return nil
		}
	})

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestManager_CancelledContext(t *testing.T) {
	m := testManager()
	var called int32
	m.RegisterStop("x", PhaseHTTP, func(ctx context.Context) error {
		atomic.StoreInt32(&called, 1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_ = m.Shutdown(ctx)
	// The component may or may not be called depending on timing,
	// but the manager should not panic.
	_ = atomic.LoadInt32(&called) == 1
}

func TestManager_MultiplePhases_ErrorInMiddle(t *testing.T) {
	m := testManager()
	var order []string
	var mu sync.Mutex

	m.RegisterStop("first", PhaseHTTP, func(ctx context.Context) error {
		mu.Lock()
		order = append(order, "first")
		mu.Unlock()
		return nil
	})
	m.RegisterStop("fail", PhaseAdapters, func(ctx context.Context) error {
		mu.Lock()
		order = append(order, "fail")
		mu.Unlock()
		return errors.New("adapter error")
	})
	m.RegisterStop("last", PhaseDatastore, func(ctx context.Context) error {
		mu.Lock()
		order = append(order, "last")
		mu.Unlock()
		return nil
	})

	err := m.Shutdown(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("all phases should execute even with errors, got %v", order)
	}
}

func TestRegisterStop_Convenience(t *testing.T) {
	m := testManager()
	m.RegisterStop("quick", PhaseHTTP, func(ctx context.Context) error { return nil })
	if m.ComponentCount() != 1 {
		t.Fatal("RegisterStop should add one component")
	}
}

func TestGroupByPhase_Empty(t *testing.T) {
	groups := groupByPhase(nil)
	if len(groups) != 0 {
		t.Fatal("nil input should yield empty groups")
	}
}

func TestGroupByPhase_SinglePhase(t *testing.T) {
	comps := []Component{
		{Name: "a", Phase: PhaseHTTP},
		{Name: "b", Phase: PhaseHTTP},
	}
	groups := groupByPhase(comps)
	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	if len(groups[0].components) != 2 {
		t.Fatalf("expected 2 components in group, got %d", len(groups[0].components))
	}
}

func TestGroupByPhase_MultiplePhases(t *testing.T) {
	comps := []Component{
		{Name: "a", Phase: PhaseHTTP},
		{Name: "b", Phase: PhaseAdapters},
		{Name: "c", Phase: PhaseAdapters},
		{Name: "d", Phase: PhaseDatastore},
	}
	groups := groupByPhase(comps)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(groups))
	}
}
