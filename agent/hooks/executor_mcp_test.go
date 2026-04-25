package hooks

import (
	"context"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Test event with tool support
// ---------------------------------------------------------------------------

// testToolEvent is a minimal Event implementation that supports ToolName and
// ToolInput for MCP executor tests.
type testToolEvent struct {
	payload   map[string]any
	toolName  string
	toolInput map[string]any
}

func (e *testToolEvent) EventName() string         { return "Test" }
func (e *testToolEvent) Payload() map[string]any   { return e.payload }
func (e *testToolEvent) ToolName() string          { return e.toolName }
func (e *testToolEvent) ToolInput() map[string]any { return e.toolInput }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestMCPExecutor_Type(t *testing.T) {
	e := NewMCPExecutor(nil)
	if got := e.Type(); got != "mcp_tool" {
		t.Errorf("Type() = %q, want %q", got, "mcp_tool")
	}
}

func TestMCPExecutor_Success(t *testing.T) {
	e := NewMCPExecutor(func(ctx context.Context, serverName, toolName string, input map[string]any) (map[string]any, bool, error) {
		if serverName != "myserver" {
			t.Errorf("serverName = %q, want %q", serverName, "myserver")
		}
		if toolName != "mytool" {
			t.Errorf("toolName = %q, want %q", toolName, "mytool")
		}
		return map[string]any{"result": "ok"}, false, nil
	})

	def := &HookDef{
		Type:   "mcp_tool",
		Server: "myserver",
		Tool:   "mytool",
		Input:  map[string]any{"key": "value"},
	}
	event := &testToolEvent{
		payload:   map[string]any{"session_id": "sess-1"},
		toolName:  "bash",
		toolInput: map[string]any{"command": "ls"},
	}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
}

func TestMCPExecutor_IsError(t *testing.T) {
	e := NewMCPExecutor(func(ctx context.Context, serverName, toolName string, input map[string]any) (map[string]any, bool, error) {
		return map[string]any{"error": "something went wrong"}, true, nil
	})

	def := &HookDef{
		Type:   "mcp_tool",
		Server: "srv",
		Tool:   "tool",
	}
	event := &testToolEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	// isError=true → non-blocking, Decision="allow".
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
	if !strings.Contains(result.Reason, "something went wrong") {
		t.Errorf("Reason = %q, want to contain 'something went wrong'", result.Reason)
	}
}

func TestMCPExecutor_NilCallTool(t *testing.T) {
	e := NewMCPExecutor(nil)

	def := &HookDef{Type: "mcp_tool", Server: "srv", Tool: "tool"}
	event := &testToolEvent{payload: map[string]any{}}

	_, err := e.Execute(context.Background(), def, event)
	if err == nil {
		t.Fatal("Execute() expected error for nil callTool, got nil")
	}
	if !strings.Contains(err.Error(), "callTool is nil") {
		t.Errorf("error = %q, want to contain 'callTool is nil'", err.Error())
	}
}

func TestMCPExecutor_InterpolateInput(t *testing.T) {
	e := NewMCPExecutor(func(ctx context.Context, serverName, toolName string, input map[string]any) (map[string]any, bool, error) {
		// Verify that interpolation happened correctly.
		if v, ok := input["path"].(string); !ok || v != "/some/path" {
			t.Errorf("input[\"path\"] = %v, want %q", input["path"], "/some/path")
		}
		if v, ok := input["session"].(string); !ok || v != "sess-42" {
			t.Errorf("input[\"session\"] = %v, want %q", input["session"], "sess-42")
		}
		if v, ok := input["tool"].(string); !ok || v != "bash" {
			t.Errorf("input[\"tool\"] = %v, want %q", input["tool"], "bash")
		}
		return map[string]any{}, false, nil
	})

	def := &HookDef{
		Type:   "mcp_tool",
		Server: "srv",
		Tool:   "tool",
		Input: map[string]any{
			"path":    "${tool_input.path}",
			"session": "${session_id}",
			"tool":    "${tool_name}",
		},
	}
	event := &testToolEvent{
		payload:   map[string]any{"session_id": "sess-42"},
		toolName:  "bash",
		toolInput: map[string]any{"path": "/some/path"},
	}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
}

func TestMCPExecutor_ContextCancel(t *testing.T) {
	e := NewMCPExecutor(func(ctx context.Context, serverName, toolName string, input map[string]any) (map[string]any, bool, error) {
		<-ctx.Done()
		return nil, false, ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	def := &HookDef{Type: "mcp_tool", Server: "srv", Tool: "tool"}
	event := &testToolEvent{payload: map[string]any{}}

	_, err := e.Execute(ctx, def, event)
	if err == nil {
		t.Fatal("Execute() expected error for cancelled context, got nil")
	}
}

func TestMCPExecutor_WithDecision(t *testing.T) {
	e := NewMCPExecutor(func(ctx context.Context, serverName, toolName string, input map[string]any) (map[string]any, bool, error) {
		return map[string]any{
			"decision":     "deny",
			"reason":       "forbidden tool",
			"context":      "security policy violation",
			"updatedInput": map[string]any{"cmd": "safe_command"},
		}, false, nil
	})

	def := &HookDef{Type: "mcp_tool", Server: "srv", Tool: "tool"}
	event := &testToolEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.Decision != "deny" {
		t.Errorf("Decision = %q, want %q", result.Decision, "deny")
	}
	if result.Reason != "forbidden tool" {
		t.Errorf("Reason = %q, want %q", result.Reason, "forbidden tool")
	}
	if result.Context != "security policy violation" {
		t.Errorf("Context = %q, want %q", result.Context, "security policy violation")
	}
	if result.UpdatedInput == nil || result.UpdatedInput["cmd"] != "safe_command" {
		t.Errorf("UpdatedInput = %v, want {cmd: safe_command}", result.UpdatedInput)
	}
}

// ---------------------------------------------------------------------------
// interpolateInput unit tests
// ---------------------------------------------------------------------------

func TestInterpolateInput_ToolInputPrefix(t *testing.T) {
	input := map[string]any{
		"arg": "${tool_input.path}",
	}
	event := &testToolEvent{
		payload:   map[string]any{},
		toolInput: map[string]any{"path": "/tmp/file.txt"},
	}
	result := interpolateInput(input, event)
	if result["arg"] != "/tmp/file.txt" {
		t.Errorf("arg = %v, want %q", result["arg"], "/tmp/file.txt")
	}
}

func TestInterpolateInput_NilInput(t *testing.T) {
	event := &testToolEvent{payload: map[string]any{}}
	result := interpolateInput(nil, event)
	if result != nil {
		t.Errorf("interpolateInput(nil, _) = %v, want nil", result)
	}
}

func TestInterpolateInput_NonStringPreserved(t *testing.T) {
	input := map[string]any{
		"count": 42,
		"flag":  true,
	}
	event := &testToolEvent{payload: map[string]any{}}
	result := interpolateInput(input, event)
	if result["count"] != 42 {
		t.Errorf("count = %v, want 42", result["count"])
	}
	if result["flag"] != true {
		t.Errorf("flag = %v, want true", result["flag"])
	}
}

func TestInterpolateInput_UnresolvedVariable(t *testing.T) {
	input := map[string]any{
		"arg": "${unknown_var}",
	}
	event := &testToolEvent{payload: map[string]any{}}
	result := interpolateInput(input, event)
	if result["arg"] != "${unknown_var}" {
		t.Errorf("arg = %v, want original ${unknown_var}", result["arg"])
	}
}

func TestInterpolateInput_PayloadFallback(t *testing.T) {
	input := map[string]any{
		"sid": "${session_id}",
	}
	event := &testToolEvent{
		payload:   map[string]any{"session_id": "abc-123"},
		toolInput: map[string]any{},
	}
	result := interpolateInput(input, event)
	if result["sid"] != "abc-123" {
		t.Errorf("sid = %v, want %q", result["sid"], "abc-123")
	}
}
