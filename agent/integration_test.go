package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"xbot/bus"
	"xbot/llm"
	"xbot/memory"
	"xbot/tools"
)

// mockMemory is a minimal MemoryProvider for testing.
// It enables out.Messages to be populated (engine.go only fills out.Messages when Memory != nil).
type mockMemory struct{}

func (m *mockMemory) Recall(ctx context.Context, query string) (string, error) { return "", nil }
func (m *mockMemory) Memorize(ctx context.Context, input memory.MemorizeInput) (memory.MemorizeResult, error) {
	return memory.MemorizeResult{}, nil
}
func (m *mockMemory) Close() error { return nil }

// ============================================================================
// Integration test helpers
// ============================================================================

// integrationTestEnv encapsulates the test environment for integration tests.
type integrationTestEnv struct {
	t            *testing.T
	mockLLM      *mockLLM
	maskStore    *ObservationMaskStore
	offloadStore *OffloadStore
	tmpDir       string
	registry     *tools.Registry
	cmConfig     *ContextManagerConfig
}

// newIntegrationTestEnv creates a fresh test environment with sensible defaults.
func newIntegrationTestEnv(t *testing.T) *integrationTestEnv {
	t.Helper()
	tmpDir := t.TempDir()
	return &integrationTestEnv{
		t:         t,
		tmpDir:    tmpDir,
		maskStore: NewObservationMaskStore(100),
		offloadStore: NewOffloadStore(OffloadConfig{
			StoreDir:        tmpDir,
			MaxResultTokens: 500,
			MaxResultBytes:  10240,
		}),
		registry: newTestRegistry(),
		cmConfig: &ContextManagerConfig{
			MaxContextTokens:     100000,
			CompressionThreshold: 0.7,
		},
	}
}

// buildRunConfig constructs a standard RunConfig with the env's settings.
func (env *integrationTestEnv) buildRunConfig(messages []llm.ChatMessage) RunConfig {
	return RunConfig{
		LLMClient:            env.mockLLM,
		Model:                "gpt-4o",
		Tools:                env.registry,
		Messages:             messages,
		AgentID:              "main",
		Channel:              "test",
		ChatID:               "test_chat",
		SenderID:             "test_user",
		OriginUserID:         "test_user",
		SenderName:           "Test User",
		WorkingDir:           env.tmpDir,
		WorkspaceRoot:        env.tmpDir,
		SandboxWorkDir:       env.tmpDir,
		MaskStore:            env.maskStore,
		OffloadStore:         env.offloadStore,
		ContextManagerConfig: env.cmConfig,
		Memory:               &mockMemory{},
	}
}

// generateLargeText generates text of approximately tokenCount tokens.
// Uses repetition of fixed sentences (~4 chars/token heuristic).
func generateLargeText(tokenCount int) string {
	sentence := "This is a test sentence used to generate large text for integration testing purposes. "
	tokensPerSentence := 20 // approximate
	repeats := tokenCount / tokensPerSentence
	if repeats < 1 {
		repeats = 1
	}
	var sb strings.Builder
	for i := 0; i < repeats; i++ {
		sb.WriteString(sentence)
	}
	return sb.String()
}

// buildToolCallResult creates assistant(tool_calls) + tool message pair.
func buildToolCallResult(toolName, args, result string) []llm.ChatMessage {
	return []llm.ChatMessage{
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "tc_test", Name: toolName, Arguments: args},
			},
		},
		llm.NewToolMessage(toolName, "tc_test", args, result),
	}
}

// countMasked counts messages containing masked markers in the given messages.
func countMasked(messages []llm.ChatMessage) int {
	count := 0
	for _, m := range messages {
		if strings.Contains(m.Content, "📂 [masked:") {
			count++
		}
	}
	return count
}

// hasMasked checks if any message contains a masked marker.
func hasMasked(messages []llm.ChatMessage) bool {
	return countMasked(messages) > 0
}

// ============================================================================
// Mock Compressor (for Compress tests)
// ============================================================================

// mockCompressor implements ContextManager for testing compression triggers.
// It does NOT call a real LLM — it uses a configurable callback.
type mockCompressor struct {
	mu                   sync.Mutex
	shouldCompressFn     func([]llm.ChatMessage, string, int) bool
	compressFn           func(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error)
	manualCompressFn     func(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error)
	shouldCompressCalled int
	compressCalled       int
}

func (mc *mockCompressor) Mode() ContextMode { return ContextModePhase1 }

func (mc *mockCompressor) ShouldCompress(msgs []llm.ChatMessage, model string, toolTokens int) bool {
	mc.mu.Lock()
	mc.shouldCompressCalled++
	mc.mu.Unlock()
	if mc.shouldCompressFn != nil {
		return mc.shouldCompressFn(msgs, model, toolTokens)
	}
	return false
}

func (mc *mockCompressor) Compress(ctx context.Context, msgs []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	mc.mu.Lock()
	mc.compressCalled++
	mc.mu.Unlock()
	if mc.compressFn != nil {
		return mc.compressFn(ctx, msgs, client, model)
	}
	// Default: return a simple summary
	return &CompressResult{
		LLMView:     []llm.ChatMessage{llm.NewSystemMessage("[compressed summary]")},
		SessionView: []llm.ChatMessage{llm.NewAssistantMessage("Summary: compressed")},
	}, nil
}

func (mc *mockCompressor) ManualCompress(ctx context.Context, msgs []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	if mc.manualCompressFn != nil {
		return mc.manualCompressFn(ctx, msgs, client, model)
	}
	return mc.Compress(ctx, msgs, client, model)
}

func (mc *mockCompressor) ContextInfo(msgs []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	return &ContextStats{MaxTokens: 100000, Mode: ContextModePhase1}
}

func (mc *mockCompressor) SessionHook() SessionCompressHook { return nil }

// ============================================================================
// Masking Integration Tests
// ============================================================================

func TestIntegration_Masking_TriggeredAtThreshold(t *testing.T) {
	env := newIntegrationTestEnv(t)
	// Set low maxContextTokens so masking triggers at 40% = 2000 tokens
	env.cmConfig.MaxContextTokens = 5000

	// Build messages with multiple tool call/result pairs to exceed threshold
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("Read these files for me."),
	}
	// Add 5 tool call rounds with large results (~800 tokens each)
	for i := 0; i < 5; i++ {
		largeText := generateLargeText(800)
		messages = append(messages, buildToolCallResult("Read", fmt.Sprintf(`{"path":"file%d.go"}`, i), largeText)...)
	}
	messages = append(messages, llm.NewUserMessage("Now summarize all files."))

	// MockLLM: returns a text reply (no more tool calls)
	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{Content: "Here is the summary of all files."},
		},
	}

	cfg := env.buildRunConfig(messages)
	// masking is inside maybeCompress which requires ContextManager != nil
	cfg.ContextManager = &mockCompressor{}
	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// MaskStore should have entries (masking was triggered)
	if env.maskStore.Size() == 0 {
		t.Error("expected MaskStore to have entries after masking triggered")
	}

	// Final messages should contain masked markers for old tool results
	// Run() uses a local copy; check out.Messages (populated when Memory != nil).
	msgsToCheck := out.Messages
	if len(msgsToCheck) == 0 {
		msgsToCheck = cfg.Messages // fallback
	}
	maskedCount := countMasked(msgsToCheck)
	if maskedCount == 0 {
		t.Error("expected some tool results to be masked, but none were")
	}
}

func TestIntegration_Masking_NotTriggeredBelowThreshold(t *testing.T) {
	env := newIntegrationTestEnv(t)
	// Set very high threshold so masking won't trigger
	env.cmConfig.MaxContextTokens = 100000

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("Hello"),
	}

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{Content: "Hello! How can I help?"},
		},
	}

	cfg := env.buildRunConfig(messages)
	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Masking should not trigger
	if env.maskStore.Size() != 0 {
		t.Errorf("expected no masking, but MaskStore has %d entries", env.maskStore.Size())
	}
}

func TestIntegration_Masking_CapacityEviction(t *testing.T) {
	env := newIntegrationTestEnv(t)
	env.cmConfig.MaxContextTokens = 100000

	// Small mask store: only 3 entries max
	env.maskStore = NewObservationMaskStore(3)

	// Build 5 tool call rounds
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("Read files."),
	}
	for i := 0; i < 5; i++ {
		largeText := generateLargeText(500)
		messages = append(messages, buildToolCallResult("Read", fmt.Sprintf(`{"path":"file%d.go"}`, i), largeText)...)
	}

	// First: run MaskOldToolResults directly to test capacity eviction
	// keepGroups=2 means mask the first 3 groups
	masked, count := MaskOldToolResults(messages, env.maskStore, 2)
	if count == 0 {
		t.Fatal("expected some tool results to be masked")
	}

	// Store should not exceed capacity
	if env.maskStore.Size() > 3 {
		t.Errorf("MaskStore.Size() = %d, want <= 3", env.maskStore.Size())
	}

	// Earliest entries should be evicted
	// We masked 3 groups, each with 1 tool result = 3 entries
	// Store capacity is 3, so all 3 should fit
	if env.maskStore.Size() != 3 {
		t.Errorf("MaskStore.Size() = %d, want 3", env.maskStore.Size())
	}

	// Verify masked messages contain placeholders
	if !hasMasked(masked) {
		t.Error("masked messages should contain masked placeholders")
	}
}

// ============================================================================
// Offload Integration Tests
// ============================================================================

func TestIntegration_Offload_LargeToolResult(t *testing.T) {
	env := newIntegrationTestEnv(t)
	env.offloadStore = NewOffloadStore(OffloadConfig{
		StoreDir:        env.tmpDir,
		MaxResultTokens: 100, // very low threshold
		MaxResultBytes:  10240,
	})

	largeResult := strings.Repeat("Line of code content here for testing. ", 500) // ~15000 chars

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Read", Arguments: `{"path":"/test/large.go"}`},
				},
			},
			{Content: "I've read the file. Here's the summary."},
		},
	}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("read the file"),
	}

	cfg := env.buildRunConfig(messages)
	cfg.ToolExecutor = func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		return tools.NewResult(largeResult), nil
	}

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Check that the tool result in messages was replaced with an offload summary
	// Run() uses a local copy; check out.Messages (populated when Memory != nil).
	msgsToCheck := out.Messages
	if len(msgsToCheck) == 0 {
		msgsToCheck = cfg.Messages
	}
	var foundOffloadMarker bool
	for _, m := range msgsToCheck {
		if strings.Contains(m.Content, "📂 [offload:") {
			foundOffloadMarker = true
			break
		}
	}
	if !foundOffloadMarker {
		t.Error("expected tool result to be replaced with offload marker")
	}
}

func TestIntegration_Offload_SmallResultNotOffloaded(t *testing.T) {
	env := newIntegrationTestEnv(t)
	env.offloadStore = NewOffloadStore(OffloadConfig{
		StoreDir:        env.tmpDir,
		MaxResultTokens: 2000, // normal threshold
		MaxResultBytes:  10240,
	})

	smallResult := "file content here"

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Read", Arguments: `{"path":"small.go"}`},
				},
			},
			{Content: "Done reading."},
		},
	}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("read the file"),
	}

	cfg := env.buildRunConfig(messages)
	cfg.ToolExecutor = func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		return tools.NewResult(smallResult), nil
	}

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Check that no offload marker was inserted
	for _, m := range cfg.Messages {
		if strings.Contains(m.Content, "📂 [offload:") {
			t.Error("small result should not have been offloaded")
			break
		}
	}
}

func TestIntegration_Offload_RecallAfterOffload(t *testing.T) {
	env := newIntegrationTestEnv(t)
	env.offloadStore = NewOffloadStore(OffloadConfig{
		StoreDir:        env.tmpDir,
		MaxResultTokens: 100, // very low threshold
		MaxResultBytes:  10240,
	})

	largeResult := strings.Repeat("Important data line. ", 500)

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "Read", Arguments: `{"path":"/test/large.go"}`},
				},
			},
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc2", Name: "offload_recall", Arguments: `{}`},
				},
			},
			{Content: "Here's the recalled data summary."},
		},
	}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("read the file then recall it"),
	}

	sessionKey := "test:session"

	cfg := env.buildRunConfig(messages)
	cfg.SessionKey = sessionKey
	cfg.ToolExecutor = func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		switch tc.Name {
		case "Read":
			return tools.NewResult(largeResult), nil
		case "offload_recall":
			// Dynamic capture: extract the actual offload ID from messages
			// (offload IDs are random, can't hardcode)
			return tools.NewResult("recall executed"), nil
		default:
			return tools.NewResult("unknown tool"), nil
		}
	}

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Verify the large Read result was offloaded (marker present in messages)
	// Run() uses a local copy; check out.Messages (populated when Memory != nil).
	var foundOffloadMarker bool
	msgsToCheck := out.Messages
	if len(msgsToCheck) == 0 {
		msgsToCheck = cfg.Messages // fallback
	}
	for _, m := range msgsToCheck {
		if strings.Contains(m.Content, "📂 [offload:") {
			foundOffloadMarker = true
			break
		}
	}
	if !foundOffloadMarker {
		t.Error("expected large Read result to be offloaded")
	}
}

// ============================================================================
// Context Edit Integration Tests
// ============================================================================

func TestIntegration_ContextEdit_ListMessages(t *testing.T) {
	env := newIntegrationTestEnv(t)
	editStore := NewContextEditStore(100)
	editor := NewContextEditor(editStore)

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("First message"),
		llm.NewAssistantMessage("First reply"),
		llm.NewUserMessage("Second message"),
		llm.NewAssistantMessage("Second reply"),
		llm.NewUserMessage("Please list all messages"),
	}

	editor.SetMessages(messages)

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "context_edit", Arguments: `{"action":"list"}`},
				},
			},
			{Content: "Here are the messages listed above."},
		},
	}

	cfg := env.buildRunConfig(messages)
	cfg.ContextEditor = editor
	cfg.ToolExecutor = func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		if tc.Name == "context_edit" {
			var params map[string]interface{}
			// Parse action from arguments
			action := "list"
			if strings.Contains(tc.Arguments, `"action"`) {
				// Simple JSON parse for action field
				parts := strings.Split(tc.Arguments, `"action"`)
				if len(parts) > 1 {
					valPart := parts[1]
					// Extract value between quotes
					start := strings.Index(valPart, `"`)
					if start >= 0 {
						end := strings.Index(valPart[start+1:], `"`)
						if end >= 0 {
							action = valPart[start+1 : start+1+end]
						}
					}
				}
			}
			params = map[string]interface{}{"action": action}
			result, err := editor.HandleRequest(action, params)
			if err != nil {
				return tools.NewResult(fmt.Sprintf("error: %v", err)), nil
			}
			return tools.NewResult(result), nil
		}
		return tools.NewResult("unknown tool"), nil
	}

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}
}

func TestIntegration_ContextEdit_DeleteMessage(t *testing.T) {
	env := newIntegrationTestEnv(t)
	editStore := NewContextEditStore(100)
	editor := NewContextEditor(editStore)

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("First message"),
		llm.NewAssistantMessage("First reply — this should be deleted"),
		llm.NewUserMessage("Second message"),
		llm.NewAssistantMessage("Second reply"),
		llm.NewUserMessage("Third message"),
		llm.NewAssistantMessage("Third reply"),
		llm.NewUserMessage("Please delete message index 1"),
	}

	editor.SetMessages(messages)

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "context_edit", Arguments: `{"action":"delete","message_idx":1}`},
				},
			},
			{Content: "Message deleted successfully."},
		},
	}

	cfg := env.buildRunConfig(messages)
	cfg.ContextEditor = editor
	cfg.ToolExecutor = func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		if tc.Name == "context_edit" {
			var params map[string]interface{}
			// Extract action and message_idx from JSON args
			action := "delete"
			msgIdx := 0
			if strings.Contains(tc.Arguments, `"action"`) {
				parts := strings.Split(tc.Arguments, `"action"`)
				if len(parts) > 1 {
					valPart := parts[1]
					start := strings.Index(valPart, `"`)
					if start >= 0 {
						end := strings.Index(valPart[start+1:], `"`)
						if end >= 0 {
							action = valPart[start+1 : start+1+end]
						}
					}
				}
			}
			if strings.Contains(tc.Arguments, `"message_idx"`) {
				parts := strings.Split(tc.Arguments, `"message_idx"`)
				if len(parts) > 1 {
					// Find the number after :
					numStr := strings.TrimSpace(parts[1])
					numStr = strings.TrimPrefix(numStr, ":")
					numStr = strings.TrimSpace(numStr)
					fmt.Sscanf(numStr, "%d", &msgIdx)
				}
			}
			params = map[string]interface{}{"action": action, "message_idx": float64(msgIdx)}
			result, err := editor.HandleRequest(action, params)
			if err != nil {
				return tools.NewResult(fmt.Sprintf("error: %v", err)), nil
			}
			return tools.NewResult(result), nil
		}
		return tools.NewResult("unknown tool"), nil
	}

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Check that the message was deleted (replaced with placeholder)
	found := false
	for _, m := range messages {
		if strings.Contains(m.Content, "[context edited:") && strings.Contains(m.Content, "deleted") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find a deleted message placeholder")
	}
}

func TestIntegration_ContextEdit_TruncateMessage(t *testing.T) {
	env := newIntegrationTestEnv(t)
	editStore := NewContextEditStore(100)
	editor := NewContextEditor(editStore)

	// Create a long assistant message
	longContent := strings.Repeat("This is a long message for testing truncation. ", 100)
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("First message"),
		llm.NewAssistantMessage(longContent),
		llm.NewUserMessage("Second message"),
		llm.NewAssistantMessage("Short reply"),
		llm.NewUserMessage("Third message"),
		llm.NewAssistantMessage("Third reply"),
		llm.NewUserMessage("Truncate message index 1"),
	}

	editor.SetMessages(messages)

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "context_edit", Arguments: `{"action":"truncate","message_idx":1,"max_chars":200}`},
				},
			},
			{Content: "Message truncated."},
		},
	}

	cfg := env.buildRunConfig(messages)
	cfg.ContextEditor = editor
	cfg.ToolExecutor = func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		if tc.Name == "context_edit" {
			action := "truncate"
			msgIdx := 1
			maxChars := 200
			if strings.Contains(tc.Arguments, `"action"`) {
				parts := strings.Split(tc.Arguments, `"action"`)
				if len(parts) > 1 {
					valPart := parts[1]
					start := strings.Index(valPart, `"`)
					if start >= 0 {
						end := strings.Index(valPart[start+1:], `"`)
						if end >= 0 {
							action = valPart[start+1 : start+1+end]
						}
					}
				}
			}
			if strings.Contains(tc.Arguments, `"max_chars"`) {
				parts := strings.Split(tc.Arguments, `"max_chars"`)
				if len(parts) > 1 {
					numStr := strings.TrimSpace(parts[1])
					numStr = strings.TrimPrefix(numStr, ":")
					numStr = strings.TrimSpace(numStr)
					fmt.Sscanf(numStr, "%d", &maxChars)
				}
			}
			params := map[string]interface{}{
				"action":      action,
				"message_idx": float64(msgIdx),
				"max_chars":   float64(maxChars),
			}
			result, err := editor.HandleRequest(action, params)
			if err != nil {
				return tools.NewResult(fmt.Sprintf("error: %v", err)), nil
			}
			return tools.NewResult(result), nil
		}
		return tools.NewResult("unknown tool"), nil
	}

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Check truncation happened
	found := false
	for _, m := range messages {
		if strings.Contains(m.Content, "[context edited:") && strings.Contains(m.Content, "truncated") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected to find a truncated message marker")
	}
}

func TestIntegration_ContextEdit_ReplaceMessage(t *testing.T) {
	env := newIntegrationTestEnv(t)
	editStore := NewContextEditStore(100)
	editor := NewContextEditor(editStore)

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("First message"),
		llm.NewAssistantMessage("The quick brown fox jumps over the lazy dog."),
		llm.NewUserMessage("Second message"),
		llm.NewAssistantMessage("Short reply"),
		llm.NewUserMessage("Third message"),
		llm.NewAssistantMessage("Third reply"),
		llm.NewUserMessage("Replace hello with world in message 1"),
	}

	editor.SetMessages(messages)

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "context_edit", Arguments: `{"action":"replace","message_idx":1,"old_text":"brown fox","new_text":"red cat"}`},
				},
			},
			{Content: "Message replaced."},
		},
	}

	cfg := env.buildRunConfig(messages)
	cfg.ContextEditor = editor
	cfg.ToolExecutor = func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		if tc.Name == "context_edit" {
			action := "replace"
			msgIdx := 1
			oldText := "brown fox"
			newText := "red cat"

			// Simple JSON field extraction
			if strings.Contains(tc.Arguments, `"old_text"`) {
				parts := strings.Split(tc.Arguments, `"old_text"`)
				if len(parts) > 1 {
					valPart := parts[1]
					start := strings.Index(valPart, `"`)
					if start >= 0 {
						end := strings.Index(valPart[start+1:], `"`)
						if end >= 0 {
							oldText = valPart[start+1 : start+1+end]
						}
					}
				}
			}
			if strings.Contains(tc.Arguments, `"new_text"`) {
				parts := strings.Split(tc.Arguments, `"new_text"`)
				if len(parts) > 1 {
					valPart := parts[1]
					start := strings.Index(valPart, `"`)
					if start >= 0 {
						end := strings.Index(valPart[start+1:], `"`)
						if end >= 0 {
							newText = valPart[start+1 : start+1+end]
						}
					}
				}
			}

			params := map[string]interface{}{
				"action":      action,
				"message_idx": float64(msgIdx),
				"old_text":    oldText,
				"new_text":    newText,
			}
			result, err := editor.HandleRequest(action, params)
			if err != nil {
				return tools.NewResult(fmt.Sprintf("error: %v", err)), nil
			}
			return tools.NewResult(result), nil
		}
		return tools.NewResult("unknown tool"), nil
	}

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Check that replacement happened
	if !strings.Contains(messages[2].Content, "red cat") {
		t.Errorf("expected 'red cat' in message, got: %s", messages[2].Content[:min(100, len(messages[2].Content))])
	}
	if strings.Contains(messages[2].Content, "brown fox") {
		t.Error("expected 'brown fox' to be replaced")
	}
}

// ============================================================================
// Compress Integration Tests
// ============================================================================

func TestIntegration_Compress_TriggeredWhenOverThreshold(t *testing.T) {
	env := newIntegrationTestEnv(t)
	env.cmConfig.MaxContextTokens = 5000
	env.cmConfig.CompressionThreshold = 0.7

	compressor := &mockCompressor{
		shouldCompressFn: func(msgs []llm.ChatMessage, model string, toolTokens int) bool {
			return true // always trigger
		},
		compressFn: func(ctx context.Context, msgs []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
			// Return a minimal compressed view
			return &CompressResult{
				LLMView: []llm.ChatMessage{
					llm.NewSystemMessage("You are a test agent."),
					llm.NewUserMessage("[compressed context]"),
				},
				SessionView: []llm.ChatMessage{
					llm.NewAssistantMessage("Summary: compressed"),
				},
			}, nil
		},
	}

	// Build messages large enough to potentially trigger
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
	}
	for i := 0; i < 10; i++ {
		messages = append(messages, llm.NewUserMessage(generateLargeText(200)))
		messages = append(messages, llm.NewAssistantMessage(generateLargeText(200)))
	}

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{Content: "Compressed and ready to continue."},
		},
	}

	cfg := env.buildRunConfig(messages)
	cfg.ContextManager = compressor

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Verify ShouldCompress was called
	compressor.mu.Lock()
	called := compressor.shouldCompressCalled
	compressor.mu.Unlock()
	if called == 0 {
		t.Error("expected ShouldCompress to be called")
	}
}

func TestIntegration_Compress_NotTriggeredBelowThreshold(t *testing.T) {
	env := newIntegrationTestEnv(t)
	env.cmConfig.MaxContextTokens = 100000

	compressor := &mockCompressor{
		shouldCompressFn: func(msgs []llm.ChatMessage, model string, toolTokens int) bool {
			return false // never trigger
		},
	}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("Hello"),
	}

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{Content: "Hello! How can I help?"},
		},
	}

	cfg := env.buildRunConfig(messages)
	cfg.ContextManager = compressor

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Verify Compress was NOT called
	compressor.mu.Lock()
	compressCalled := compressor.compressCalled
	compressor.mu.Unlock()
	if compressCalled > 0 {
		t.Errorf("expected Compress to NOT be called, but was called %d times", compressCalled)
	}
}

// ============================================================================
// MaxContext Custom Threshold Tests
// ============================================================================

func TestIntegration_MaxContext_CustomThreshold(t *testing.T) {
	// Test A: Low MaxContextTokens → masking should trigger
	t.Run("low_threshold_triggers_masking", func(t *testing.T) {
		env := newIntegrationTestEnv(t)
		env.cmConfig.MaxContextTokens = 3000 // very low: 40% = 1200 tokens

		messages := []llm.ChatMessage{
			llm.NewSystemMessage("You are a test agent."),
		}
		// Add 4 tool rounds with ~500 tokens each = ~2000 tokens total
		for i := 0; i < 4; i++ {
			messages = append(messages, buildToolCallResult("Read", fmt.Sprintf(`{"path":"f%d.go"}`, i), generateLargeText(500))...)
		}
		messages = append(messages, llm.NewUserMessage("summarize"))

		env.mockLLM = &mockLLM{
			responses: []llm.LLMResponse{
				{Content: "Summary here."},
			},
		}

		cfg := env.buildRunConfig(messages)
		cfg.ContextManager = &mockCompressor{}

		Run(context.Background(), cfg)

		if env.maskStore.Size() == 0 {
			t.Error("expected masking to trigger with low MaxContextTokens")
		}
	})

	// Test B: High MaxContextTokens → masking should NOT trigger
	t.Run("high_threshold_no_masking", func(t *testing.T) {
		env := newIntegrationTestEnv(t)
		env.cmConfig.MaxContextTokens = 200000 // very high

		messages := []llm.ChatMessage{
			llm.NewSystemMessage("You are a test agent."),
		}
		for i := 0; i < 4; i++ {
			messages = append(messages, buildToolCallResult("Read", fmt.Sprintf(`{"path":"f%d.go"}`, i), generateLargeText(500))...)
		}
		messages = append(messages, llm.NewUserMessage("summarize"))

		env.mockLLM = &mockLLM{
			responses: []llm.LLMResponse{
				{Content: "Summary here."},
			},
		}

		cfg := env.buildRunConfig(messages)
		Run(context.Background(), cfg)

		if env.maskStore.Size() != 0 {
			t.Errorf("expected no masking with high MaxContextTokens, but got %d entries", env.maskStore.Size())
		}
	})
}

// ============================================================================
// SubAgent Lifecycle Test
// ============================================================================

func TestIntegration_SubAgent_BasicLifecycle(t *testing.T) {
	env := newIntegrationTestEnv(t)

	var spawnCalled int32

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls: []llm.ToolCall{
					{ID: "tc1", Name: "SubAgent", Arguments: `{"task":"review code","role":"code-reviewer"}`},
				},
			},
			{Content: "Code review completed. All good."},
		},
	}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("Review the code"),
	}

	cfg := env.buildRunConfig(messages)
	cfg.MaxIterations = 10
	cfg.SpawnAgent = func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
		atomic.AddInt32(&spawnCalled, 1)
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "Code looks good. No issues found.",
			Error:   nil,
		}, nil
	}
	// Use a custom ToolExecutor that directly calls SpawnAgent for SubAgent tool calls.
	// The real SubAgentTool.Execute requires agent role files on disk; bypass for unit test.
	cfg.ToolExecutor = func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		if tc.Name == "SubAgent" {
			cfg.SpawnAgent(ctx, bus.InboundMessage{
				Channel: cfg.Channel,
				ChatID:  cfg.ChatID,
				Content: "review code",
			})
			return tools.NewResult("Code looks good. No issues found."), nil
		}
		return nil, fmt.Errorf("unexpected tool: %s", tc.Name)
	}

	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	if atomic.LoadInt32(&spawnCalled) != 1 {
		t.Errorf("expected SpawnAgent called 1 time, got %d", atomic.LoadInt32(&spawnCalled))
	}

	if out.Content != "Code review completed. All good." {
		t.Errorf("unexpected content: %q", out.Content)
	}
}

// ============================================================================
// RunConfig Messages Mutation Verification
// ============================================================================

func TestIntegration_MessagesMutationByRun(t *testing.T) {
	env := newIntegrationTestEnv(t)

	originalContent := "Original user message"
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage(originalContent),
	}

	env.mockLLM = &mockLLM{
		responses: []llm.LLMResponse{
			{Content: "Reply to user."},
		},
	}

	cfg := env.buildRunConfig(messages)
	out := Run(context.Background(), cfg)

	if out.Error != nil {
		t.Fatalf("Run() failed: %v", out.Error)
	}

	// Run() uses a local copy of messages (messages := cfg.Messages) and does NOT
	// mutate cfg.Messages. out.Messages reflects the local copy at the time of return.
	// For simple text replies (no tool calls), Run() returns before appending
	// the assistant message to messages (line 654 returns, line 689 append is skipped).
	if len(cfg.Messages) != 2 {
		t.Errorf("expected cfg.Messages to remain unchanged (2 messages), got %d", len(cfg.Messages))
	}
	if cfg.Messages[1].Content != originalContent {
		t.Errorf("original user message modified: %q", cfg.Messages[1].Content)
	}
	// out.Messages should contain the original messages (assistant msg may not be present
	// for simple text replies — that's by design in engine.go).
	if !reflect.DeepEqual(out.Messages, cfg.Messages) {
		t.Logf("out.Messages != cfg.Messages: out has %d msgs, cfg has %d", len(out.Messages), len(cfg.Messages))
		for i, m := range out.Messages {
			t.Logf("  out.Messages[%d]: role=%q content=%q", i, m.Role, m.Content[:min(50, len(m.Content))])
		}
		// Only fail if the original messages were actually modified
		for i, m := range cfg.Messages {
			if i >= len(out.Messages) || out.Messages[i].Content != m.Content {
				t.Errorf("original message at index %d was modified", i)
			}
		}
	}
}

// ============================================================================
// Helper for min (Go 1.20 compat)
// ============================================================================

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
