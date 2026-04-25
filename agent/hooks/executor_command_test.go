package hooks

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// testEvent is a minimal Event implementation for testing.
// ---------------------------------------------------------------------------

type testEvent struct {
	payload map[string]any
}

func (e *testEvent) EventName() string         { return "Test" }
func (e *testEvent) Payload() map[string]any   { return e.payload }
func (e *testEvent) ToolName() string          { return "" }
func (e *testEvent) ToolInput() map[string]any { return nil }

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCommandExecutor_Type(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")
	if got := e.Type(); got != "command" {
		t.Errorf("Type() = %q, want %q", got, "command")
	}
}

func TestCommandExecutor_Success(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")

	// Command that outputs valid JSON.
	jsonOut := `{"decision":"deny","reason":"forbidden","context":"extra info"}`
	def := &HookDef{
		Type:    "command",
		Command: "echo '" + jsonOut + "'",
		Timeout: 5,
	}
	event := &testEvent{payload: map[string]any{"session_id": "sess-123"}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Decision != "deny" {
		t.Errorf("Decision = %q, want %q", result.Decision, "deny")
	}
	if result.Reason != "forbidden" {
		t.Errorf("Reason = %q, want %q", result.Reason, "forbidden")
	}
	if result.Context != "extra info" {
		t.Errorf("Context = %q, want %q", result.Context, "extra info")
	}
}

func TestCommandExecutor_SuccessPlainText(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")

	// Command that outputs non-JSON plain text.
	def := &HookDef{
		Type:    "command",
		Command: "echo 'hello world'",
		Timeout: 5,
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
	// Plain text should be in Context.
	if result.Context != "hello world" {
		t.Errorf("Context = %q, want %q", result.Context, "hello world")
	}
}

func TestCommandExecutor_BlockExit2(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")

	def := &HookDef{
		Type:    "command",
		Command: "echo 'blocked' >&2; exit 2",
		Timeout: 5,
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", result.ExitCode)
	}
	if result.Decision != "deny" {
		t.Errorf("Decision = %q, want %q", result.Decision, "deny")
	}
	if !strings.Contains(result.Reason, "blocked") {
		t.Errorf("Reason = %q, want to contain 'blocked'", result.Reason)
	}
}

func TestCommandExecutor_NonBlockError(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")

	def := &HookDef{
		Type:    "command",
		Command: "echo 'something went wrong' >&2; exit 1",
		Timeout: 5,
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", result.ExitCode)
	}
	// Non-blocking: decision is "allow".
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
	if !strings.Contains(result.Stderr, "something went wrong") {
		t.Errorf("Stderr = %q, want to contain 'something went wrong'", result.Stderr)
	}
}

func TestCommandExecutor_Timeout(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")

	def := &HookDef{
		Type:    "command",
		Command: "sleep 10",
		Timeout: 1, // 1 second timeout
	}
	event := &testEvent{payload: map[string]any{}}

	start := time.Now()
	_, err := e.Execute(context.Background(), def, event)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Execute() expected error for timeout, got nil")
	}
	// Should timeout within roughly 2 seconds (1s timeout + overhead).
	if elapsed > 5*time.Second {
		t.Errorf("Execute() took %v, should have timed out within ~1s", elapsed)
	}
}

func TestCommandExecutor_DefaultTimeout(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")

	// No Timeout set in def → should default to 30s.
	// We verify by running a fast command (should succeed within default timeout).
	def := &HookDef{
		Type:    "command",
		Command: "echo ok",
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
}

func TestCommandExecutor_EnvironmentVars(t *testing.T) {
	e := NewCommandExecutor("/custom/home", "/custom/project")

	def := &HookDef{
		Type:    "command",
		Command: "echo \"HOME=$XBOT_HOME PROJECT=$XBOT_PROJECT_DIR SESSION=$XBOT_SESSION_ID\"",
		Timeout: 5,
	}
	event := &testEvent{payload: map[string]any{
		"session_id": "sess-abc-123",
	}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}

	wantHome := "HOME=/custom/home"
	wantProject := "PROJECT=/custom/project"
	wantSession := "SESSION=sess-abc-123"

	if !strings.Contains(result.Stdout, wantHome) {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, wantHome)
	}
	if !strings.Contains(result.Stdout, wantProject) {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, wantProject)
	}
	if !strings.Contains(result.Stdout, wantSession) {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, wantSession)
	}
}

func TestCommandExecutor_EnvironmentVarsNoSession(t *testing.T) {
	e := NewCommandExecutor("/custom/home", "/custom/project")

	def := &HookDef{
		Type:    "command",
		Command: "echo \"SESSION=${XBOT_SESSION_ID:-unset}\"",
		Timeout: 5,
	}
	// No session_id in payload.
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if !strings.Contains(result.Stdout, "unset") {
		t.Errorf("Stdout = %q, want to contain 'unset'", result.Stdout)
	}
}

func TestCommandExecutor_StdinPayload(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")

	def := &HookDef{
		Type:    "command",
		Command: "cat",
		Timeout: 5,
	}
	event := &testEvent{payload: map[string]any{
		"session_id":      "sess-xyz",
		"hook_event_name": "Test",
	}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}

	// Stdout should be the JSON-encoded payload that was piped to stdin.
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Stdout)), &got); err != nil {
		t.Fatalf("stdout is not valid JSON: %q, error: %v", result.Stdout, err)
	}
	if got["session_id"] != "sess-xyz" {
		t.Errorf("stdin payload session_id = %v, want %q", got["session_id"], "sess-xyz")
	}
	if got["hook_event_name"] != "Test" {
		t.Errorf("stdin payload hook_event_name = %v, want %q", got["hook_event_name"], "Test")
	}
}

func TestCommandExecutor_SuccessWithUpdatedInput(t *testing.T) {
	e := NewCommandExecutor("/tmp/xbot", "/tmp/project")

	jsonOut := `{"decision":"allow","updatedInput":{"path":"/new/path","force":true}}`
	def := &HookDef{
		Type:    "command",
		Command: "echo '" + jsonOut + "'",
		Timeout: 5,
	}
	event := &testEvent{payload: map[string]any{}}

	result, err := e.Execute(context.Background(), def, event)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if result.Decision != "allow" {
		t.Errorf("Decision = %q, want %q", result.Decision, "allow")
	}
	if result.UpdatedInput == nil {
		t.Fatal("UpdatedInput is nil, want non-nil")
	}
	if result.UpdatedInput["path"] != "/new/path" {
		t.Errorf("UpdatedInput[path] = %v, want %q", result.UpdatedInput["path"], "/new/path")
	}
	if result.UpdatedInput["force"] != true {
		t.Errorf("UpdatedInput[force] = %v, want true", result.UpdatedInput["force"])
	}
}
