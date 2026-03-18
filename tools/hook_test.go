package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// --- Mock Hooks ---

type mockHook struct {
	name         string
	preCalls     []mockCall
	postCalls    []mockCall
	preErr       error
	panicInPre   bool
	panicInPost  bool
	mu           sync.Mutex
}

type mockCall struct {
	toolName string
	args     string
}

func (h *mockHook) Name() string { return h.name }

func (h *mockHook) PreToolUse(_ context.Context, toolName string, args string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.preCalls = append(h.preCalls, mockCall{toolName, args})
	if h.panicInPre {
		panic("mock panic in PreToolUse")
	}
	return h.preErr
}

func (h *mockHook) PostToolUse(_ context.Context, toolName string, args string, _ *ToolResult, _ error, _ time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.postCalls = append(h.postCalls, mockCall{toolName, args})
	if h.panicInPost {
		panic("mock panic in PostToolUse")
	}
}

func (h *mockHook) preCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.preCalls)
}

func (h *mockHook) postCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.postCalls)
}

// --- Tests ---

func TestHookChain_NewHookChain(t *testing.T) {
	hc := NewHookChain()
	if hc == nil {
		t.Fatal("NewHookChain returned nil")
	}

	h1 := &mockHook{name: "a"}
	h2 := &mockHook{name: "b"}
	hc = NewHookChain(h1, nil, h2)
	if len(hc.hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hc.hooks))
	}
}

func TestHookChain_Use(t *testing.T) {
	hc := NewHookChain()

	h1 := &mockHook{name: "alpha"}
	hc.Use(h1)

	if len(hc.hooks) != 1 {
		t.Fatalf("expected 1 hook, got %d", len(hc.hooks))
	}

	// Use with same name should replace
	h2 := &mockHook{name: "alpha"}
	hc.Use(h2)
	if len(hc.hooks) != 1 {
		t.Fatalf("expected 1 hook after replace, got %d", len(hc.hooks))
	}
	if hc.hooks[0] != h2 {
		t.Fatal("expected replaced hook")
	}

	// Use with different name should append
	h3 := &mockHook{name: "beta"}
	hc.Use(h3)
	if len(hc.hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d", len(hc.hooks))
	}
}

func TestHookChain_Remove(t *testing.T) {
	h1 := &mockHook{name: "alpha"}
	h2 := &mockHook{name: "beta"}
	hc := NewHookChain(h1, h2)

	hc.Remove("alpha")
	if len(hc.hooks) != 1 {
		t.Fatalf("expected 1 hook after remove, got %d", len(hc.hooks))
	}
	if hc.hooks[0].Name() != "beta" {
		t.Fatal("expected beta hook remaining")
	}

	// Remove non-existent should be no-op
	hc.Remove("gamma")
	if len(hc.hooks) != 1 {
		t.Fatal("remove non-existent should be no-op")
	}
}

func TestHookChain_RunPre_Normal(t *testing.T) {
	h1 := &mockHook{name: "first"}
	h2 := &mockHook{name: "second"}
	hc := NewHookChain(h1, h2)

	ctx := context.Background()
	err := hc.RunPre(ctx, "TestTool", `{"key":"val"}`)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if h1.preCallCount() != 1 {
		t.Fatal("first hook not called")
	}
	if h2.preCallCount() != 1 {
		t.Fatal("second hook not called")
	}
}

func TestHookChain_RunPre_Block(t *testing.T) {
	h1 := &mockHook{name: "pass"}
	h2 := &mockHook{name: "block", preErr: errors.New("blocked")}
	h3 := &mockHook{name: "after"}
	hc := NewHookChain(h1, h2, h3)

	ctx := context.Background()
	err := hc.RunPre(ctx, "TestTool", "")
	if err == nil {
		t.Fatal("expected error from blocking hook")
	}
	if err.Error() != "blocked" {
		t.Fatalf("expected 'blocked', got %v", err)
	}
	// All hooks should still be called
	if h1.preCallCount() != 1 {
		t.Fatal("first hook not called")
	}
	if h2.preCallCount() != 1 {
		t.Fatal("blocking hook not called")
	}
	if h3.preCallCount() != 1 {
		t.Fatal("after hook not called (should run even after block)")
	}
}

func TestHookChain_RunPre_PanicRecover(t *testing.T) {
	h1 := &mockHook{name: "panic", panicInPre: true}
	h2 := &mockHook{name: "after-panic"}
	hc := NewHookChain(h1, h2)

	ctx := context.Background()
	err := hc.RunPre(ctx, "TestTool", "")
	if err != nil {
		t.Fatalf("expected no error after panic recovery, got %v", err)
	}
	// Second hook should still run
	if h2.preCallCount() != 1 {
		t.Fatal("hook after panic should still run")
	}
}

func TestHookChain_RunPost_Normal(t *testing.T) {
	h1 := &mockHook{name: "first"}
	h2 := &mockHook{name: "second"}
	hc := NewHookChain(h1, h2)

	ctx := context.Background()
	result := &ToolResult{Summary: "ok"}
	hc.RunPost(ctx, "TestTool", `{"key":"val"}`, result, nil, 10*time.Millisecond)

	if h1.postCallCount() != 1 {
		t.Fatal("first hook PostToolUse not called")
	}
	if h2.postCallCount() != 1 {
		t.Fatal("second hook PostToolUse not called")
	}
}

func TestHookChain_RunPost_WithError(t *testing.T) {
	h1 := &mockHook{name: "log"}
	hc := NewHookChain(h1)

	ctx := context.Background()
	hc.RunPost(ctx, "TestTool", "", nil, errors.New("boom"), 5*time.Millisecond)

	if h1.postCallCount() != 1 {
		t.Fatal("PostToolUse not called on error")
	}
}

func TestHookChain_RunPost_PanicRecover(t *testing.T) {
	h1 := &mockHook{name: "panic", panicInPost: true}
	h2 := &mockHook{name: "after-panic"}
	hc := NewHookChain(h1, h2)

	ctx := context.Background()
	// Should not panic
	hc.RunPost(ctx, "TestTool", "", nil, nil, 0)

	if h2.postCallCount() != 1 {
		t.Fatal("hook after panic should still run")
	}
}

func TestHookChain_RunPost_AlwaysAll(t *testing.T) {
	// PostToolUse must run all hooks even when there's an error
	h1 := &mockHook{name: "a"}
	h2 := &mockHook{name: "b"}
	h3 := &mockHook{name: "c"}
	hc := NewHookChain(h1, h2, h3)

	ctx := context.Background()
	hc.RunPost(ctx, "TestTool", "", nil, fmt.Errorf("some error"), 0)

	if h1.postCallCount() != 1 || h2.postCallCount() != 1 || h3.postCallCount() != 1 {
		t.Fatal("not all PostToolUse hooks ran")
	}
}

func TestHookChain_ConcurrentSafety(t *testing.T) {
	hc := NewHookChain()

	// Concurrently add hooks
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hc.Use(&mockHook{name: fmt.Sprintf("hook-%d", i)})
		}(i)
	}
	wg.Wait()

	if len(hc.hooks) != 100 {
		t.Fatalf("expected 100 hooks, got %d", len(hc.hooks))
	}

	// Concurrently run pre hooks
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = hc.RunPre(context.Background(), "TestTool", "")
		}()
	}
	wg.Wait()

	// Concurrently run post hooks
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hc.RunPost(context.Background(), "TestTool", "", nil, nil, time.Millisecond)
		}()
	}
	wg.Wait()

	// Concurrently add/remove
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hc.Remove(fmt.Sprintf("hook-%d", i))
		}(i)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			hc.Use(&mockHook{name: fmt.Sprintf("new-%d", i)})
		}(i)
	}
	wg.Wait()
}

func TestHookChain_NilHooks(t *testing.T) {
	hc := NewHookChain(nil, nil, &mockHook{name: "exists"}, nil)
	if len(hc.hooks) != 1 {
		t.Fatalf("expected 1 non-nil hook, got %d", len(hc.hooks))
	}
}
