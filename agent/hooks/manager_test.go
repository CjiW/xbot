package hooks

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// managerTestEvent is a flexible Event implementation for manager tests.
// It uses a different name from managerTestEvent (used in executor_command_test.go)
// to avoid redeclaration conflicts within the same package.
type managerTestEvent struct {
	mName      string
	mToolName  string
	mToolInput map[string]any
	mPayload   map[string]any
}

func (e *managerTestEvent) EventName() string         { return e.mName }
func (e *managerTestEvent) ToolName() string          { return e.mToolName }
func (e *managerTestEvent) ToolInput() map[string]any { return e.mToolInput }
func (e *managerTestEvent) Payload() map[string]any {
	if e.mPayload != nil {
		return e.mPayload
	}
	return map[string]any{"hook_event_name": e.mName}
}

// newTestManager creates a Manager with no config files (empty dirs).
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	tmpDir := t.TempDir()
	m, err := NewManager(tmpDir, "")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestManager_NewManager_NoConfig verifies that creating a Manager without
// any configuration files succeeds without error.
func TestManager_NewManager_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := NewManager(tmpDir, "")
	if err != nil {
		t.Fatalf("NewManager should not fail without config files: %v", err)
	}
	if m == nil {
		t.Fatal("Manager should not be nil")
	}
	if m.config == nil {
		t.Fatal("config should not be nil")
	}
	if len(m.config.Hooks) != 0 {
		t.Fatalf("expected empty hooks, got %d", len(m.config.Hooks))
	}
}

// TestManager_Emit_NoHandlers verifies that emitting an event with no
// matching handlers returns Allow.
func TestManager_Emit_NoHandlers(t *testing.T) {
	m := newTestManager(t)
	evt := &managerTestEvent{mName: "SessionStart"}

	dec, err := m.Emit(context.Background(), evt)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if dec.Action != Allow {
		t.Fatalf("expected Allow, got %s", dec.Action)
	}
}

// TestManager_Emit_BuiltinAllow verifies that a builtin callback returning
// allow produces an Allow decision.
func TestManager_Emit_BuiltinAllow(t *testing.T) {
	m := newTestManager(t)
	m.RegisterBuiltin(&CallbackHook{
		Name: "test-allow",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			return &Result{Decision: "allow"}, nil
		},
	})

	evt := &managerTestEvent{mName: "SessionStart"}
	dec, err := m.Emit(context.Background(), evt)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if dec.Action != Allow {
		t.Fatalf("expected Allow, got %s", dec.Action)
	}
}

// TestManager_Emit_BuiltinDeny verifies that a builtin callback returning
// deny produces a Deny decision.
func TestManager_Emit_BuiltinDeny(t *testing.T) {
	m := newTestManager(t)
	m.RegisterBuiltin(&CallbackHook{
		Name: "test-deny",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			return &Result{Decision: "deny", Reason: "forbidden"}, nil
		},
	})

	evt := &managerTestEvent{mName: "SessionStart"}
	dec, err := m.Emit(context.Background(), evt)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if dec.Action != Deny {
		t.Fatalf("expected Deny, got %s", dec.Action)
	}
	if dec.Reason != "forbidden" {
		t.Fatalf("expected reason 'forbidden', got %q", dec.Reason)
	}
}

// TestManager_Emit_DecisionPriority verifies that deny takes priority over
// allow in the aggregated decision.
func TestManager_Emit_DecisionPriority(t *testing.T) {
	m := newTestManager(t)

	m.RegisterBuiltin(&CallbackHook{
		Name: "hook-allow",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			return &Result{Decision: "allow", Reason: "ok"}, nil
		},
	})
	m.RegisterBuiltin(&CallbackHook{
		Name: "hook-deny",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			return &Result{Decision: "deny", Reason: "nope"}, nil
		},
	})

	evt := &managerTestEvent{mName: "SessionStart"}
	dec, err := m.Emit(context.Background(), evt)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if dec.Action != Deny {
		t.Fatalf("expected Deny (deny > allow), got %s", dec.Action)
	}
	// Reasons should be concatenated with "; ".
	if dec.Reason != "ok; nope" {
		t.Fatalf("expected concatenated reason 'ok; nope', got %q", dec.Reason)
	}
}

// TestManager_Emit_MaxHandlers verifies that at most 10 handlers execute.
func TestManager_Emit_MaxHandlers(t *testing.T) {
	m := newTestManager(t)

	var executed atomic.Int32
	for i := 0; i < 15; i++ {
		idx := i
		m.RegisterBuiltin(&CallbackHook{
			Name: fmt.Sprintf("hook-%d", idx),
			Fn: func(ctx context.Context, event Event) (*Result, error) {
				executed.Add(1)
				return &Result{Decision: "allow"}, nil
			},
		})
	}

	evt := &managerTestEvent{mName: "SessionStart"}
	dec, err := m.Emit(context.Background(), evt)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if dec.Action != Allow {
		t.Fatalf("expected Allow, got %s", dec.Action)
	}
	if got := executed.Load(); got != 10 {
		t.Fatalf("expected exactly 10 handler executions, got %d", got)
	}
}

// TestManager_Emit_ConcurrentSafety verifies that concurrent Emit calls
// do not race.
func TestManager_Emit_ConcurrentSafety(t *testing.T) {
	m := newTestManager(t)
	m.RegisterBuiltin(&CallbackHook{
		Name: "concurrent-hook",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			return &Result{Decision: "allow"}, nil
		},
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			evt := &managerTestEvent{mName: "SessionStart"}
			dec, err := m.Emit(context.Background(), evt)
			if err != nil {
				t.Errorf("Emit: %v", err)
				return
			}
			if dec.Action != Allow {
				t.Errorf("expected Allow, got %s", dec.Action)
			}
		}()
	}
	wg.Wait()
}

// TestManager_Emit_AsyncHandler verifies that async handlers do not block
// Emit and their results are not aggregated.
func TestManager_Emit_AsyncHandler(t *testing.T) {
	m := newTestManager(t)

	// Add a slow async handler via config.
	m.mu.Lock()
	m.config.Hooks["SessionStart"] = []EventGroup{
		{
			Matcher: "",
			Hooks: []HookDef{
				{
					Type:    "command",
					Command: "sleep 10",
					Async:   true,
				},
			},
		},
	}
	m.config.EnableCommandHooks = true
	m.mu.Unlock()

	// Add a fast builtin that completes immediately.
	m.RegisterBuiltin(&CallbackHook{
		Name: "fast-hook",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			return &Result{Decision: "allow", Reason: "fast"}, nil
		},
	})

	evt := &managerTestEvent{mName: "SessionStart"}

	start := time.Now()
	dec, err := m.Emit(context.Background(), evt)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if dec.Action != Allow {
		t.Fatalf("expected Allow, got %s", dec.Action)
	}
	// Emit should return quickly — the async handler should not block.
	if elapsed > 5*time.Second {
		t.Fatalf("Emit blocked for %v; async handler should not block", elapsed)
	}
}

// TestManager_RegisterExecutor verifies that a custom executor can be
// registered and used.
func TestManager_RegisterExecutor(t *testing.T) {
	m := newTestManager(t)

	called := false
	m.RegisterExecutor(&testExecutor{
		typeVal: "custom",
		executeFn: func(ctx context.Context, def *HookDef, event Event) (*Result, error) {
			called = true
			return &Result{Decision: "deny", Reason: "custom says no"}, nil
		},
	})

	// Wire a config hook that uses the custom executor.
	m.mu.Lock()
	m.config.Hooks["SessionStart"] = []EventGroup{
		{
			Matcher: "",
			Hooks: []HookDef{
				{Type: "custom"},
			},
		},
	}
	m.mu.Unlock()

	evt := &managerTestEvent{mName: "SessionStart"}
	dec, err := m.Emit(context.Background(), evt)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	if !called {
		t.Fatal("custom executor was not called")
	}
	if dec.Action != Deny {
		t.Fatalf("expected Deny, got %s", dec.Action)
	}
}

// TestManager_GetBuiltin verifies retrieving a registered builtin by name.
func TestManager_GetBuiltin(t *testing.T) {
	m := newTestManager(t)

	// Not found.
	if b := m.GetBuiltin("nope"); b != nil {
		t.Fatal("expected nil for unknown builtin")
	}

	// Register and find.
	hook := &CallbackHook{Name: "my-hook", Fn: nil}
	m.RegisterBuiltin(hook)
	if b := m.GetBuiltin("my-hook"); b != hook {
		t.Fatal("expected to find the registered builtin")
	}
}

// TestManager_ReloadConfig verifies that configuration can be reloaded.
func TestManager_ReloadConfig(t *testing.T) {
	m := newTestManager(t)

	// Initial state — no hooks.
	if len(m.config.Hooks) != 0 {
		t.Fatal("expected no hooks initially")
	}

	// Reload should succeed even without files.
	if err := m.ReloadConfig(); err != nil {
		t.Fatalf("ReloadConfig: %v", err)
	}
}

// TestManager_Emit_CommandDisabled verifies that command-type hooks are
// skipped when EnableCommandHooks is false.
func TestManager_Emit_CommandDisabled(t *testing.T) {
	m := newTestManager(t)

	// Add a command hook in config but leave EnableCommandHooks=false (default).
	m.mu.Lock()
	m.config.Hooks["PreToolUse"] = []EventGroup{
		{
			Matcher: "Shell",
			Hooks: []HookDef{
				{
					Type:    "command",
					Command: "echo denied",
				},
			},
		},
	}
	// EnableCommandHooks stays false (default)
	m.mu.Unlock()

	evt := &managerTestEvent{mName: "PreToolUse", mToolName: "Shell"}
	dec, err := m.Emit(context.Background(), evt)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}
	// Command is disabled — no handler fires → Allow.
	if dec.Action != Allow {
		t.Fatalf("expected Allow (command disabled), got %s", dec.Action)
	}
}

// ---------------------------------------------------------------------------
// Stub executor for tests
// ---------------------------------------------------------------------------

// testExecutor is a test double for the Executor interface.
type testExecutor struct {
	typeVal   string
	executeFn func(ctx context.Context, def *HookDef, event Event) (*Result, error)
}

func (e *testExecutor) Type() string {
	if e.typeVal != "" {
		return e.typeVal
	}
	return "test"
}

func (e *testExecutor) Execute(ctx context.Context, def *HookDef, event Event) (*Result, error) {
	if e.executeFn != nil {
		return e.executeFn(ctx, def, event)
	}
	return &Result{Decision: "allow"}, nil
}
