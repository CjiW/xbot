package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xbot/bus"
	"xbot/llm"
	"xbot/tools"
)

// mockSandbox is a test double for the Sandbox interface.
type mockSandbox struct {
	name      string
	workspace string
}

func (m *mockSandbox) Name() string              { return m.name }
func (m *mockSandbox) Workspace(_ string) string { return m.workspace }
func (m *mockSandbox) Exec(_ context.Context, _ tools.ExecSpec) (*tools.ExecResult, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockSandbox) ReadFile(_ context.Context, _ string, _ string) ([]byte, error) {
	return nil, os.ErrNotExist
}
func (m *mockSandbox) WriteFile(_ context.Context, _ string, _ []byte, _ os.FileMode, _ string) error {
	return nil
}
func (m *mockSandbox) Stat(_ context.Context, _ string, _ string) (*tools.SandboxFileInfo, error) {
	return nil, os.ErrNotExist
}
func (m *mockSandbox) ReadDir(_ context.Context, _ string, _ string) ([]tools.DirEntry, error) {
	return nil, os.ErrNotExist
}
func (m *mockSandbox) MkdirAll(_ context.Context, _ string, _ os.FileMode, _ string) error {
	return nil
}
func (m *mockSandbox) Remove(_ context.Context, _ string, _ string) error    { return os.ErrNotExist }
func (m *mockSandbox) RemoveAll(_ context.Context, _ string, _ string) error { return nil }
func (m *mockSandbox) DownloadFile(_ context.Context, _ string, _ string, _ string) error {
	return nil
}
func (m *mockSandbox) GetShell(_ string, _ string) (string, error) { return "/bin/bash", nil }
func (m *mockSandbox) Close() error                                { return nil }
func (m *mockSandbox) CloseForUser(_ string) error                 { return nil }
func (m *mockSandbox) IsExporting(_ string) bool                   { return false }
func (m *mockSandbox) ExportAndImport(_ string) error              { return nil }

// --- Mock LLM ---

type mockLLM struct {
	responses []llm.LLMResponse
	callCount int
	calls     []mockLLMCall
}

type mockLLMCall struct {
	Model    string
	Messages []llm.ChatMessage
	Tools    []llm.ToolDefinition
}

func (m *mockLLM) Generate(_ context.Context, model string, messages []llm.ChatMessage, toolDefs []llm.ToolDefinition, thinkingMode string) (*llm.LLMResponse, error) {
	m.calls = append(m.calls, mockLLMCall{Model: model, Messages: messages, Tools: toolDefs})
	if m.callCount >= len(m.responses) {
		return nil, fmt.Errorf("no more mock responses (call %d)", m.callCount)
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return &resp, nil
}

func (m *mockLLM) ListModels() []string {
	return []string{"test-model"}
}

// --- Mock Tool ---

type mockTool struct {
	name     string
	result   *tools.ToolResult
	err      error
	execFunc func(ctx *tools.ToolContext, input string) (*tools.ToolResult, error)
}

func (t *mockTool) Name() string                { return t.name }
func (t *mockTool) Description() string         { return "mock tool" }
func (t *mockTool) Parameters() []llm.ToolParam { return nil }
func (t *mockTool) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	if t.execFunc != nil {
		return t.execFunc(ctx, input)
	}
	return t.result, t.err
}

// --- Helper ---

func newTestRegistry(tt ...*mockTool) *tools.Registry {
	r := tools.NewRegistry()
	for _, t := range tt {
		r.Register(t)
	}
	return r
}

func baseMessages() []llm.ChatMessage {
	return []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("Hello"),
	}
}

// --- Tests ---

func TestRun_BasicConversation(t *testing.T) {
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{Content: "Hello! How can I help?"},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test-model",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		Channel:   "test",
		ChatID:    "chat1",
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if out.Content != "Hello! How can I help?" {
		t.Errorf("content = %q, want %q", out.Content, "Hello! How can I help?")
	}
	if len(out.ToolsUsed) != 0 {
		t.Errorf("toolsUsed = %v, want empty", out.ToolsUsed)
	}
	if out.Channel != "test" {
		t.Errorf("channel = %q, want %q", out.Channel, "test")
	}
}

func TestRun_SingleToolCall(t *testing.T) {
	shellTool := &mockTool{
		name:   "Shell",
		result: tools.NewResult("command output"),
	}

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				Content:      "",
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{"command":"ls"}`},
				},
			},
			{Content: "Here are the files."},
		},
	}

	reg := newTestRegistry(shellTool)

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     reg,
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return shellTool.Execute(nil, tc.Arguments)
		},
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if out.Content != "Here are the files." {
		t.Errorf("content = %q", out.Content)
	}
	if len(out.ToolsUsed) != 1 || out.ToolsUsed[0] != "Shell" {
		t.Errorf("toolsUsed = %v, want [Shell]", out.ToolsUsed)
	}
}

func TestRun_MultiToolCallLoop(t *testing.T) {
	callCount := 0
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Read", Arguments: `{"path":"a.go"}`},
				},
			},
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc2", Name: "FileReplace", Arguments: `{"path":"a.go"}`},
				},
			},
			{Content: "Done editing."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			callCount++
			return tools.NewResult("ok"), nil
		},
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if out.Content != "Done editing." {
		t.Errorf("content = %q", out.Content)
	}
	if callCount != 2 {
		t.Errorf("tool call count = %d, want 2", callCount)
	}
	if len(out.ToolsUsed) != 2 {
		t.Errorf("toolsUsed = %v, want 2 items", out.ToolsUsed)
	}
}

func TestRun_MaxIterations(t *testing.T) {
	// LLM always returns tool calls, never a final response
	mock := &mockLLM{
		responses: make([]llm.LLMResponse, 10),
	}
	for i := range mock.responses {
		mock.responses[i] = llm.LLMResponse{
			FinishReason: llm.FinishReasonToolCalls,
			ToolCalls: []llm.ToolCall{
				{ID: fmt.Sprintf("tc%d", i), Name: "Shell", Arguments: `{}`},
			},
		}
	}

	out := Run(context.Background(), RunConfig{
		LLMClient:     mock,
		Model:         "test",
		Tools:         newTestRegistry(),
		Messages:      baseMessages(),
		AgentID:       "main",
		MaxIterations: 3,
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return tools.NewResult("ok"), nil
		},
	})

	if !strings.Contains(out.Content, "最大迭代次数") {
		t.Errorf("expected max iterations message, got %q", out.Content)
	}
	if mock.callCount != 3 {
		t.Errorf("LLM call count = %d, want 3", mock.callCount)
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	mock := &mockLLM{
		responses: []llm.LLMResponse{},
	}
	// Generate will fail because context is cancelled
	out := Run(ctx, RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
	})

	if out.Error == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !strings.Contains(out.Content, "cancelled") {
		t.Errorf("content = %q, expected cancellation message", out.Content)
	}
}

func TestRun_LLMError_GracefulDegradation(t *testing.T) {
	// First call succeeds with tool call + content, second call fails
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				Content:      "Let me check...",
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{}`},
				},
			},
			// Second call will fail (no more responses)
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return tools.NewResult("ok"), nil
		},
	})

	// Should return partial content with error warning appended
	if out.Content == "" || !strings.HasPrefix(out.Content, "Let me check...") {
		t.Errorf("content = %q, want partial result with warning", out.Content)
	}
	if !strings.Contains(out.Content, "⚠️ LLM 调用失败") {
		t.Errorf("content = %q, want partial result to contain warning", out.Content)
	}
}

func TestRun_LLMError_NoPartialResult(t *testing.T) {
	mock := &mockLLM{
		responses: []llm.LLMResponse{}, // immediate failure
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
	})

	if out.Error == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(out.Error, ErrLLMGenerate) {
		t.Errorf("error = %v, want ErrLLMGenerate", out.Error)
	}
	// Content should also contain user-friendly error message
	if out.Content == "" || !strings.Contains(out.Content, "❌ LLM 服务调用失败") {
		t.Errorf("content = %q, want user-friendly error message", out.Content)
	}
}

func TestRun_ProgressNotification(t *testing.T) {
	var notifications []string

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{"command":"ls"}`},
				},
			},
			{Content: "Done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ProgressNotifier: func(lines []string) {
			notifications = append(notifications, lines...)
		},
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return tools.NewResult("ok"), nil
		},
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if len(notifications) == 0 {
		t.Error("expected progress notifications")
	}
	// Should have at least the tool progress notification
	found := false
	for _, n := range notifications {
		if strings.Contains(n, "Shell") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no Shell tool notification found in: %v", notifications)
	}
}

func TestRun_WaitingUser(t *testing.T) {
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "CardCreate", Arguments: `{}`},
				},
			},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return tools.NewResultWithUserResponse("Card sent, waiting for user"), nil
		},
	})

	if !out.WaitingUser {
		t.Error("expected WaitingUser = true")
	}
	if out.Content != "" {
		t.Errorf("content should be empty when waiting for user, got %q", out.Content)
	}
}

func TestRun_ReadWriteSplit(t *testing.T) {
	var execOrder []string
	var mu = make(chan struct{}, 1)

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Read", Arguments: `{"path":"a.go"}`},
					{ID: "tc2", Name: "Read", Arguments: `{"path":"b.go"}`},
					{ID: "tc3", Name: "FileReplace", Arguments: `{"path":"c.go"}`},
				},
			},
			{Content: "Done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient:            mock,
		Model:                "test",
		Tools:                newTestRegistry(),
		Messages:             baseMessages(),
		AgentID:              "main",
		EnableReadWriteSplit: true,
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			mu <- struct{}{}
			execOrder = append(execOrder, tc.Name)
			<-mu
			return tools.NewResult("ok"), nil
		},
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	// Edit should come after both Reads
	editIdx := -1
	for i, name := range execOrder {
		if name == "FileReplace" {
			editIdx = i
		}
	}
	if editIdx < 2 {
		t.Errorf("FileReplace executed at index %d, expected after reads. Order: %v", editIdx, execOrder)
	}
}

func TestRun_ToolError(t *testing.T) {
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{}`},
				},
			},
			{Content: "I see the error, let me try differently."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return nil, fmt.Errorf("permission denied")
		},
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	// LLM should receive the error and respond
	if out.Content != "I see the error, let me try differently." {
		t.Errorf("content = %q", out.Content)
	}

	// Verify the error was passed to LLM in tool message
	if len(mock.calls) < 2 {
		t.Fatal("expected at least 2 LLM calls")
	}
	lastCall := mock.calls[1]
	lastMsg := lastCall.Messages[len(lastCall.Messages)-1]
	if !strings.Contains(lastMsg.Content, "permission denied") {
		t.Errorf("tool error not in LLM messages: %q", lastMsg.Content)
	}
}

func TestRun_OAuthHandler(t *testing.T) {
	oauthErr := fmt.Errorf("token needed")

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "FeishuAPI", Arguments: `{}`},
				},
			},
			{Content: "OAuth handled."},
		},
	}

	var oauthCalled bool
	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			return nil, oauthErr
		},
		OAuthHandler: func(ctx context.Context, tc llm.ToolCall, execErr error) (string, bool) {
			oauthCalled = true
			return "Please authorize via link", true
		},
	})

	if !oauthCalled {
		t.Error("OAuthHandler was not called")
	}
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
}

func TestRun_SystemMessageAssert(t *testing.T) {
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{Content: "ok"},
		},
	}

	// No system message
	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  []llm.ChatMessage{llm.NewUserMessage("Hello")},
		AgentID:   "main",
	})

	if out.Error == nil {
		t.Error("expected error for missing system message")
	}
	if !strings.Contains(out.Error.Error(), "system message") {
		t.Errorf("error = %v, expected system message assertion", out.Error)
	}
}

func TestRun_ThinkBlockStripping(t *testing.T) {
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{Content: "<think>internal reasoning</think>Hello user!"},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
	})

	if strings.Contains(out.Content, "think") {
		t.Errorf("think block not stripped: %q", out.Content)
	}
	if !strings.Contains(out.Content, "Hello user!") {
		t.Errorf("content = %q, expected 'Hello user!'", out.Content)
	}
}

func TestRun_DefaultToolExecutor(t *testing.T) {
	shellTool := &mockTool{
		name:   "Shell",
		result: tools.NewResult("default executor output"),
	}

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{}`},
				},
			},
			{Content: "Got it."},
		},
	}

	// No ToolExecutor set — should use defaultToolExecutor
	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(shellTool),
		Messages:  baseMessages(),
		AgentID:   "main",
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if out.Content != "Got it." {
		t.Errorf("content = %q", out.Content)
	}
}

func TestRun_DefaultToolExecutor_InheritsWorkspace(t *testing.T) {
	// Verify that defaultToolExecutor passes workspace/sandbox fields to ToolContext
	var capturedCtx *tools.ToolContext
	captureTool := &mockTool{
		name: "CaptureTool",
		execFunc: func(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
			capturedCtx = ctx
			return tools.NewResult("captured"), nil
		},
	}

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "CaptureTool", Arguments: `{}`},
				},
			},
			{Content: "Done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(captureTool),
		Messages:  baseMessages(),
		AgentID:   "sub/code-reviewer",
		Channel:   "feishu",
		ChatID:    "oc_test",
		SenderID:  "ou_test",

		// Workspace fields (simulating SubAgent inheriting from parent)
		WorkingDir:    "/work",
		WorkspaceRoot: "/work/users/ou_test",
		Sandbox:       &mockSandbox{name: "docker", workspace: "/workspace"},
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		ReadOnlyRoots: []string{"/work/.xbot/skills"},
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		SkillsDirs: []string{"/work/.xbot/skills"},
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		AgentsDir:        "/work/.xbot/agents",
		MCPConfigPath:    "/work/users/ou_test/mcp.json",
		GlobalMCPConfig:  "/work/mcp.json",
		DataDir:          "/work",
		SandboxEnabled:   true,
		PreferredSandbox: "docker",
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if capturedCtx == nil {
		t.Fatal("tool was not called")
	}

	// Verify workspace fields propagated to ToolContext
	if capturedCtx.WorkingDir != "/work" {
		t.Errorf("WorkingDir = %q", capturedCtx.WorkingDir)
	}
	if capturedCtx.WorkspaceRoot != "/work/users/ou_test" {
		t.Errorf("WorkspaceRoot = %q", capturedCtx.WorkspaceRoot)
	}
	if capturedCtx.Sandbox == nil || capturedCtx.Sandbox.Workspace("test-user") != "/workspace" {
		t.Errorf("Sandbox.Workspace(\"test-user\") = %q", capturedCtx.Sandbox.Workspace("test-user"))
	}
	if !capturedCtx.SandboxEnabled {
		t.Error("SandboxEnabled should be true")
	}
	if capturedCtx.PreferredSandbox != "docker" {
		t.Errorf("PreferredSandbox = %q", capturedCtx.PreferredSandbox)
	}
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	if len(capturedCtx.SkillsDirs) != 1 || capturedCtx.SkillsDirs[0] != "/work/.xbot/skills" {
		t.Errorf("SkillsDirs = %v", capturedCtx.SkillsDirs)
	}
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	if capturedCtx.AgentsDir != "/work/.xbot/agents" {
		t.Errorf("AgentsDir = %q", capturedCtx.AgentsDir)
	}
	if capturedCtx.MCPConfigPath != "/work/users/ou_test/mcp.json" {
		t.Errorf("MCPConfigPath = %q", capturedCtx.MCPConfigPath)
	}
	if capturedCtx.GlobalMCPConfigPath != "/work/mcp.json" {
		t.Errorf("GlobalMCPConfigPath = %q", capturedCtx.GlobalMCPConfigPath)
	}
	if capturedCtx.DataDir != "/work" {
		t.Errorf("DataDir = %q", capturedCtx.DataDir)
	}
	if capturedCtx.AgentID != "sub/code-reviewer" {
		t.Errorf("AgentID = %q", capturedCtx.AgentID)
	}
}

func TestRun_DefaultToolExecutor_UnknownTool(t *testing.T) {
	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "NonExistent", Arguments: `{}`},
				},
			},
			{Content: "I see the error."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(), // empty registry
		Messages:  baseMessages(),
		AgentID:   "main",
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	// LLM should receive the "unknown tool" error and respond
	if out.Content != "I see the error." {
		t.Errorf("content = %q", out.Content)
	}
}

func TestRun_SessionFinalSentCallback(t *testing.T) {
	var finalSent int32

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "CardCreate", Arguments: `{}`},
					{ID: "tc2", Name: "Shell", Arguments: `{}`},
				},
			},
			{Content: "Done."},
		},
	}

	var notifyCount int
	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ProgressNotifier: func(lines []string) {
			notifyCount++
		},
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			if tc.Name == "CardCreate" {
				atomic.StoreInt32(&finalSent, 1)
			}
			return tools.NewResult("ok"), nil
		},
		SessionFinalSentCallback: func() bool {
			return atomic.LoadInt32(&finalSent) == 1
		},
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	// After CardCreate sets finalSent, progress notifications should stop
	// We can't easily verify the exact count, but the test should not panic
}

func TestRun_MultipleToolCallsInOneResponse(t *testing.T) {
	var toolNames []string

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Read", Arguments: `{"path":"a.go"}`},
					{ID: "tc2", Name: "Grep", Arguments: `{"pattern":"TODO"}`},
					{ID: "tc3", Name: "Shell", Arguments: `{"command":"ls"}`},
				},
			},
			{Content: "All done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		ToolExecutor: func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
			toolNames = append(toolNames, tc.Name)
			return tools.NewResult("ok"), nil
		},
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if len(toolNames) != 3 {
		t.Errorf("executed %d tools, want 3", len(toolNames))
	}
	if len(out.ToolsUsed) != 3 {
		t.Errorf("toolsUsed = %v, want 3 items", out.ToolsUsed)
	}
}

// --- CallChain Tests (preserved from original) ---

func TestCallChain_CanSpawn(t *testing.T) {
	tests := []struct {
		name     string
		chain    []string
		target   string
		maxDepth int
		wantErr  bool
	}{
		{
			name:     "normal spawn from main",
			chain:    []string{"main"},
			target:   "code-reviewer",
			maxDepth: 6,
			wantErr:  false,
		},
		{
			name:     "depth 2 spawn",
			chain:    []string{"main", "main/code-reviewer"},
			target:   "explorer",
			maxDepth: 6,
			wantErr:  false,
		},
		{
			name:     "max depth reached (old default 3)",
			chain:    []string{"main", "main/a", "main/a/b"},
			target:   "c",
			maxDepth: 3,
			wantErr:  true,
		},
		{
			name:     "max depth reached (new default 6)",
			chain:    []string{"main", "main/a", "main/a/b", "main/a/b/c", "main/a/b/c/d", "main/a/b/c/d/e"},
			target:   "f",
			maxDepth: 6,
			wantErr:  true,
		},
		{
			name:     "within new default depth 6",
			chain:    []string{"main", "main/a", "main/a/b", "main/a/b/c", "main/a/b/c/d"},
			target:   "e",
			maxDepth: 6,
			wantErr:  false,
		},
		{
			name:     "circular call",
			chain:    []string{"main", "main/code-reviewer"},
			target:   "code-reviewer",
			maxDepth: 6,
			wantErr:  true,
		},
		{
			name:     "zero maxDepth uses default",
			chain:    []string{"main", "main/a", "main/a/b"},
			target:   "c",
			maxDepth: 0, // should use DefaultMaxSubAgentDepth (6)
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := &CallChain{Chain: tt.chain}
			err := cc.CanSpawn(tt.target, tt.maxDepth)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestCallChain_Spawn(t *testing.T) {
	cc := &CallChain{Chain: []string{"main"}}
	child := cc.Spawn("code-reviewer")

	if len(child.Chain) != 2 {
		t.Fatalf("expected chain length 2, got %d", len(child.Chain))
	}
	if child.Chain[0] != "main" {
		t.Errorf("chain[0] = %q, want %q", child.Chain[0], "main")
	}
	if child.Chain[1] != "main/code-reviewer" {
		t.Errorf("chain[1] = %q, want %q", child.Chain[1], "main/code-reviewer")
	}

	// Original should be unchanged
	if len(cc.Chain) != 1 {
		t.Errorf("original chain modified: %v", cc.Chain)
	}
}

func TestCallChain_Context(t *testing.T) {
	ctx := context.Background()

	// Default chain
	cc := CallChainFromContext(ctx)
	if cc.Current() != "main" {
		t.Errorf("default Current() = %q, want %q", cc.Current(), "main")
	}
	if cc.Depth() != 1 {
		t.Errorf("default Depth() = %d, want 1", cc.Depth())
	}

	// Inject chain
	custom := &CallChain{Chain: []string{"main", "main/cr"}}
	ctx = WithCallChain(ctx, custom)
	got := CallChainFromContext(ctx)
	if got.Current() != "main/cr" {
		t.Errorf("Current() = %q, want %q", got.Current(), "main/cr")
	}
	if got.Depth() != 2 {
		t.Errorf("Depth() = %d, want 2", got.Depth())
	}
}

func TestSpawnAgentAdapter(t *testing.T) {
	var capturedMsg bus.InboundMessage

	adapter := &spawnAgentAdapter{
		spawnFn: func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			capturedMsg = msg
			return &bus.OutboundMessage{
				Content: "task completed",
			}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_xxx",
		senderID: "ou_xxx",
	}

	parentCtx := &tools.ToolContext{
		Ctx:        context.Background(),
		SenderID:   "ou_xxx",
		SenderName: "Test User",
		ChatID:     "oc_xxx",
	}

	result, err := adapter.RunSubAgent(parentCtx, "review this code", "You are a code reviewer.", []string{"Shell", "Read"}, tools.SubAgentCapabilities{
		Memory:      true,
		SendMessage: true,
	}, "code-reviewer", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "task completed" {
		t.Errorf("result = %q, want %q", result, "task completed")
	}

	// Verify InboundMessage was constructed correctly
	if capturedMsg.Channel != "agent" {
		t.Errorf("Channel = %q, want %q", capturedMsg.Channel, "agent")
	}
	if capturedMsg.Content != "review this code" {
		t.Errorf("Content = %q, want %q", capturedMsg.Content, "review this code")
	}
	if capturedMsg.ParentAgentID != "main" {
		t.Errorf("ParentAgentID = %q, want %q", capturedMsg.ParentAgentID, "main")
	}
	if capturedMsg.SystemPrompt != "You are a code reviewer." {
		t.Errorf("SystemPrompt = %q", capturedMsg.SystemPrompt)
	}
	if len(capturedMsg.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v, want [Shell Read]", capturedMsg.AllowedTools)
	}
	if !capturedMsg.IsFromAgent() {
		t.Error("expected IsFromAgent() = true")
	}
	if capturedMsg.OriginChannel() != "feishu" {
		t.Errorf("OriginChannel() = %q, want %q", capturedMsg.OriginChannel(), "feishu")
	}
	if capturedMsg.OriginChatID() != "oc_xxx" {
		t.Errorf("OriginChatID() = %q, want %q", capturedMsg.OriginChatID(), "oc_xxx")
	}
	if capturedMsg.OriginSenderID() != "ou_xxx" {
		t.Errorf("OriginSenderID() = %q, want %q", capturedMsg.OriginSenderID(), "ou_xxx")
	}

	// Verify unified addressing
	if !capturedMsg.From.IsIM() {
		t.Errorf("From should be IM address, got %v", capturedMsg.From)
	}
	if !capturedMsg.To.IsAgent() {
		t.Errorf("To should be Agent address, got %v", capturedMsg.To)
	}

	// Verify capabilities are propagated
	if !capturedMsg.Capabilities["memory"] {
		t.Error("expected capabilities[memory] = true")
	}
	if !capturedMsg.Capabilities["send_message"] {
		t.Error("expected capabilities[send_message] = true")
	}
	// SpawnAgent=false was explicitly set, should be preserved in map
	if capturedMsg.Capabilities["spawn_agent"] {
		t.Error("expected capabilities[spawn_agent] = false (explicitly set)")
	}
}

func TestSpawnAgentAdapter_ErrorPropagation(t *testing.T) {
	adapter := &spawnAgentAdapter{
		spawnFn: func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return &bus.OutboundMessage{
				Content: "partial result",
				Error:   context.Canceled,
			}, nil
		},
		parentID: "main",
		channel:  "feishu",
		chatID:   "oc_xxx",
		senderID: "ou_xxx",
	}

	parentCtx := &tools.ToolContext{
		Ctx: context.Background(),
	}

	result, err := adapter.RunSubAgent(parentCtx, "task", "", nil, tools.SubAgentCapabilities{}, "test-role", "")
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if result != "partial result" {
		t.Errorf("result = %q, want %q", result, "partial result")
	}
}

func TestBuildToolContext(t *testing.T) {
	called := false
	injectCalled := false
	cfg := &RunConfig{
		AgentID:    "main",
		Channel:    "feishu",
		ChatID:     "oc_xxx",
		SenderID:   "ou_xxx",
		SenderName: "Test",
		SendFunc: func(ch, cid, content string, _ ...map[string]string) error {
			return nil
		},
		InjectInbound: func(ch, cid, sid, content string) {
			injectCalled = true
		},
		SpawnAgent: func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			called = true
			return &bus.OutboundMessage{Content: "ok"}, nil
		},

		// 工作区 & 沙箱
		WorkingDir:    "/work",
		WorkspaceRoot: "/work/users/ou_xxx",
		Sandbox:       &mockSandbox{name: "docker", workspace: "/workspace"},
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		ReadOnlyRoots: []string{"/work/.xbot/skills"},
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		SkillsDirs: []string{"/work/.xbot/skills"},
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		AgentsDir:        "/work/.xbot/agents",
		MCPConfigPath:    "/work/users/ou_xxx/mcp.json",
		GlobalMCPConfig:  "/work/mcp.json",
		DataDir:          "/work",
		SandboxEnabled:   true,
		PreferredSandbox: "docker",

		Tools: tools.NewRegistry(),
	}

	tc := buildToolContext(context.Background(), cfg)

	// 基本字段
	if tc.AgentID != "main" {
		t.Errorf("AgentID = %q", tc.AgentID)
	}
	if tc.Channel != "feishu" {
		t.Errorf("Channel = %q", tc.Channel)
	}
	if tc.Manager == nil {
		t.Fatal("Manager should not be nil when SpawnAgent is set")
	}

	// 工作区 & 沙箱字段
	if tc.WorkingDir != "/work" {
		t.Errorf("WorkingDir = %q, want /work", tc.WorkingDir)
	}
	if tc.WorkspaceRoot != "/work/users/ou_xxx" {
		t.Errorf("WorkspaceRoot = %q", tc.WorkspaceRoot)
	}
	if tc.Sandbox == nil || tc.Sandbox.Workspace("test-user") != "/workspace" {
		t.Errorf("Sandbox.Workspace(\"test-user\") = %q", tc.Sandbox.Workspace("test-user"))
	}
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	if len(tc.ReadOnlyRoots) != 1 || tc.ReadOnlyRoots[0] != "/work/.xbot/skills" {
		t.Errorf("ReadOnlyRoots = %v", tc.ReadOnlyRoots)
	}
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	if len(tc.SkillsDirs) != 1 || tc.SkillsDirs[0] != "/work/.xbot/skills" {
		t.Errorf("SkillsDirs = %v", tc.SkillsDirs)
	}
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	if tc.AgentsDir != "/work/.xbot/agents" {
		t.Errorf("AgentsDir = %q", tc.AgentsDir)
	}
	if tc.MCPConfigPath != "/work/users/ou_xxx/mcp.json" {
		t.Errorf("MCPConfigPath = %q", tc.MCPConfigPath)
	}
	if tc.GlobalMCPConfigPath != "/work/mcp.json" {
		t.Errorf("GlobalMCPConfigPath = %q", tc.GlobalMCPConfigPath)
	}
	if tc.DataDir != "/work" {
		t.Errorf("DataDir = %q", tc.DataDir)
	}
	if !tc.SandboxEnabled {
		t.Error("SandboxEnabled should be true")
	}
	if tc.PreferredSandbox != "docker" {
		t.Errorf("PreferredSandbox = %q", tc.PreferredSandbox)
	}

	// InjectInbound
	if tc.InjectInbound == nil {
		t.Fatal("InjectInbound should not be nil")
	}
	tc.InjectInbound("", "", "", "")
	if !injectCalled {
		t.Error("InjectInbound was not called")
	}

	// Registry
	if tc.Registry == nil {
		t.Fatal("Registry should not be nil")
	}

	// Verify Manager works
	_, _ = tc.Manager.RunSubAgent(tc, "test", "prompt", nil, tools.SubAgentCapabilities{}, "test", "")
	if !called {
		t.Error("SpawnAgent was not called through Manager")
	}
}

func TestBuildToolContext_WithExtras(t *testing.T) {
	cfg := &RunConfig{
		AgentID: "main",
		ToolContextExtras: &ToolContextExtras{
			TenantID: 42,
		},
	}

	tc := buildToolContext(context.Background(), cfg)
	if tc.TenantID != 42 {
		t.Errorf("TenantID = %d, want 42", tc.TenantID)
	}
}

func TestBuildToolContext_NilExtras(t *testing.T) {
	cfg := &RunConfig{
		AgentID: "main",
	}

	tc := buildToolContext(context.Background(), cfg)
	if tc.TenantID != 0 {
		t.Errorf("TenantID = %d, want 0", tc.TenantID)
	}
	if tc.Manager != nil {
		t.Error("Manager should be nil when SpawnAgent is nil")
	}
}

// --- Hook Tests ---

func TestRun_WithHookChain_PreAndPost(t *testing.T) {
	preHook := &toolsMockHook{name: "pre-post-check"}
	hc := tools.NewHookChain(preHook, tools.NewLoggingHook(), tools.NewTimingHook())

	shellTool := &mockTool{
		name:   "Shell",
		result: tools.NewResult("output"),
	}

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{"cmd":"ls"}`},
				},
			},
			{Content: "Done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(shellTool),
		Messages:  baseMessages(),
		AgentID:   "main",
		HookChain: hc,
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if out.Content != "Done." {
		t.Errorf("content = %q", out.Content)
	}

	if preHook.preCallCount() != 1 {
		t.Errorf("expected 1 pre call, got %d", preHook.preCallCount())
	}
	if preHook.postCallCount() != 1 {
		t.Errorf("expected 1 post call, got %d", preHook.postCallCount())
	}
}

func TestRun_WithHookChain_PreBlocks(t *testing.T) {
	blockHook := &toolsMockHook{name: "blocker", preErr: errors.New("access denied")}
	hc := tools.NewHookChain(blockHook)

	shellTool := &mockTool{
		name:   "Shell",
		result: tools.NewResult("should not execute"),
	}

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{}`},
				},
			},
			{Content: "Done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(shellTool),
		Messages:  baseMessages(),
		AgentID:   "main",
		HookChain: hc,
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	// The tool should have been blocked, but the LLM gets another turn
	if blockHook.preCallCount() != 1 {
		t.Errorf("expected 1 pre call, got %d", blockHook.preCallCount())
	}
}

func TestRun_WithHookChain_Nil(t *testing.T) {
	// Nil HookChain should work fine (no hooks run)
	shellTool := &mockTool{
		name:   "Shell",
		result: tools.NewResult("output"),
	}

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{}`},
				},
			},
			{Content: "Done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(shellTool),
		Messages:  baseMessages(),
		AgentID:   "main",
		HookChain: nil,
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
}

func TestRun_WithHookChain_TimingHookCollects(t *testing.T) {
	timingHook := tools.NewTimingHook()
	hc := tools.NewHookChain(timingHook)

	shellTool := &mockTool{
		name:   "Shell",
		result: tools.NewResult("output"),
	}

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{}`},
				},
			},
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc2", Name: "Shell", Arguments: `{}`},
				},
			},
			{Content: "Done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(shellTool),
		Messages:  baseMessages(),
		AgentID:   "main",
		HookChain: hc,
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}

	stats := timingHook.Stats()
	shellStats, ok := stats["Shell"]
	if !ok {
		t.Fatal("expected Shell timing stats")
	}
	if shellStats.Count != 2 {
		t.Fatalf("expected 2 Shell calls, got %d", shellStats.Count)
	}
}

func TestRun_WithHookChain_HookPanics(t *testing.T) {
	panicHook := &toolsMockHook{name: "panicker", panicInPre: true}
	postHook := &toolsMockHook{name: "post-check"}
	hc := tools.NewHookChain(panicHook, postHook, tools.NewLoggingHook())

	shellTool := &mockTool{
		name:   "Shell",
		result: tools.NewResult("output"),
	}

	mock := &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Shell", Arguments: `{}`},
				},
			},
			{Content: "Done."},
		},
	}

	out := Run(context.Background(), RunConfig{
		LLMClient: mock,
		Model:     "test",
		Tools:     newTestRegistry(shellTool),
		Messages:  baseMessages(),
		AgentID:   "main",
		HookChain: hc,
	})

	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	// post-check hook should still have run
	if postHook.postCallCount() != 1 {
		t.Errorf("expected 1 post call despite panic, got %d", postHook.postCallCount())
	}
}

// toolsMockHook is a mock ToolHook for testing in agent package.
// (Cannot use tools package mockHook directly due to unexported fields.)
type toolsMockHook struct {
	name        string
	preCalls    int
	postCalls   int
	preErr      error
	panicInPre  bool
	panicInPost bool
	mu          sync.Mutex
}

func (h *toolsMockHook) Name() string { return h.name }

func (h *toolsMockHook) PreToolUse(_ context.Context, toolName string, args string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.preCalls++
	if h.panicInPre {
		panic("test panic in PreToolUse")
	}
	return h.preErr
}

func (h *toolsMockHook) PostToolUse(_ context.Context, toolName string, args string, result *tools.ToolResult, err error, elapsed time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.postCalls++
	if h.panicInPost {
		panic("test panic in PostToolUse")
	}
}

func (h *toolsMockHook) preCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.preCalls
}

func (h *toolsMockHook) postCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.postCalls
}

// TestBuildToolContext_SubAgentCdPersists verifies that Cd tool's directory change
// persists across subsequent buildToolContext calls for SubAgent (no session).
// This was a bug: buildToolContext is called per tool execution, and the old closure
// only set tc.CurrentDir on the old ToolContext, which was discarded after each call.
func TestBuildToolContext_SubAgentCdPersists(t *testing.T) {
	// Simulate SubAgent: no Session, InitialCWD set
	cfg := &RunConfig{
		AgentID:    "main/code-reviewer",
		Channel:    "feishu",
		ChatID:     "oc_xxx",
		SenderID:   "ou_xxx",
		WorkingDir: "/work",
		InitialCWD: "/work",
		Tools:      tools.NewRegistry(),
	}

	// First buildToolContext — simulates Cd call
	tc1 := buildToolContext(context.Background(), cfg)
	if tc1.CurrentDir != "/work" {
		t.Fatalf("initial CurrentDir = %q, want /work", tc1.CurrentDir)
	}

	// Simulate Cd tool calling SetCurrentDir
	if tc1.SetCurrentDir == nil {
		t.Fatal("SetCurrentDir should not be nil for SubAgent with InitialCWD")
	}
	tc1.SetCurrentDir("/work/project/src")

	// Second buildToolContext — simulates next tool call (e.g., Read)
	tc2 := buildToolContext(context.Background(), cfg)

	// BUG: before the fix, this was "/work" because the closure only updated
	// the old tc1.CurrentDir, not cfg.InitialCWD which buildToolContext re-reads.
	if tc2.CurrentDir != "/work/project/src" {
		t.Errorf("CurrentDir after Cd = %q, want /work/project/src (Cd change was lost)", tc2.CurrentDir)
	}

	// Third call — verify it persists across multiple calls
	tc3 := buildToolContext(context.Background(), cfg)
	if tc3.CurrentDir != "/work/project/src" {
		t.Errorf("CurrentDir on third call = %q, want /work/project/src", tc3.CurrentDir)
	}
}

// TestRun_LLMSemaphore_NoLeakAcrossIterations verifies that the per-tenant LLM
// semaphore is released after each LLM call, not deferred to Run() exit.
// Before the fix, defer inside the for-loop caused slots to accumulate, deadlocking
// after <capacity> iterations.
func TestRun_LLMSemaphore_NoLeakAcrossIterations(t *testing.T) {
	const semCapacity = 2
	const toolIterations = 5 // more iterations than semaphore capacity

	// Build mock LLM responses: toolIterations rounds of tool calls + final text reply
	var responses []llm.LLMResponse
	for i := 0; i < toolIterations; i++ {
		responses = append(responses, llm.LLMResponse{
			FinishReason: llm.FinishReasonToolCalls,
			ToolCalls: []llm.ToolCall{{
				ID:        fmt.Sprintf("call_%d", i),
				Name:      "Echo",
				Arguments: fmt.Sprintf(`{"msg":"iter%d"}`, i),
			}},
		})
	}
	responses = append(responses, llm.LLMResponse{Content: "done"})

	mock := &mockLLM{responses: responses}

	sem := make(chan struct{}, semCapacity)
	var acquireCount atomic.Int32

	semAcquire := func() func() {
		acquireCount.Add(1)
		sem <- struct{}{}
		return func() { <-sem }
	}

	echoTool := &mockTool{
		name:   "Echo",
		result: &tools.ToolResult{Summary: "ok"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out := Run(ctx, RunConfig{
		LLMClient:     mock,
		Model:         "test-model",
		Tools:         newTestRegistry(echoTool),
		Messages:      baseMessages(),
		AgentID:       "main",
		Channel:       "test",
		ChatID:        "chat1",
		LLMSemAcquire: semAcquire,
	})

	if ctx.Err() != nil {
		t.Fatal("Run deadlocked on LLM semaphore (timed out)")
	}
	if out.Error != nil {
		t.Fatalf("unexpected error: %v", out.Error)
	}
	if out.Content != "done" {
		t.Errorf("content = %q, want %q", out.Content, "done")
	}
	total := int(acquireCount.Load())
	if total != toolIterations+1 {
		t.Errorf("semaphore acquired %d times, want %d", total, toolIterations+1)
	}
	// Verify semaphore is fully released
	if len(sem) != 0 {
		t.Errorf("semaphore has %d slots still held, want 0", len(sem))
	}
}
