package agent

import (
	"context"
	"errors"
	"testing"

	"xbot/llm"
)

// mockContextManager implements ContextManager for pipeline tests.
type mockContextManager struct {
	compressFn       func(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)
	manualCompressFn func(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)
}

func (m *mockContextManager) Mode() ContextMode { return ContextModePhase1 }
func (m *mockContextManager) ShouldCompress([]llm.ChatMessage, string, int) bool {
	return false
}
func (m *mockContextManager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	if m.compressFn != nil {
		return m.compressFn(ctx, messages, client, model)
	}
	return nil, errors.New("compress not configured")
}
func (m *mockContextManager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	if m.manualCompressFn != nil {
		return m.manualCompressFn(ctx, messages, client, model)
	}
	return nil, errors.New("manual compress not configured")
}
func (m *mockContextManager) ContextInfo([]llm.ChatMessage, string, int) *ContextStats {
	return nil
}
func (m *mockContextManager) SessionHook() SessionCompressHook { return nil }
func (m *mockContextManager) SetMemoryTools([]llm.ToolDefinition, func(context.Context, llm.ToolCall) (string, error)) {
}

// sampleCompressResult returns a deterministic CompressResult for tests.
func sampleCompressResult() *CompressResult {
	return &CompressResult{
		LLMView: []llm.ChatMessage{
			llm.NewUserMessage("summary of context"),
			llm.NewAssistantMessage("understood"),
		},
		SessionView: []llm.ChatMessage{
			llm.NewUserMessage("summary of context"),
			llm.NewAssistantMessage("understood"),
		},
		InputTokens:  100,
		OutputTokens: 50,
		CachedTokens: 10,
		LLMCalls:     1,
	}
}

func TestApplyCompress_CompressSuccess(t *testing.T) {
	result := sampleCompressResult()
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string) (*CompressResult, error) {
			return result, nil
		},
	}

	var usageAccumulated bool
	var syncedMessages []llm.ChatMessage
	tracker := NewTokenTracker(500, 200)

	params := CompressPipelineParams{
		CM:           cm,
		Messages:     []llm.ChatMessage{llm.NewUserMessage("hello")},
		LLMClient:    &mockLLM{},
		Model:        "test-model",
		UseManual:    false,
		TokenTracker: tracker,
		Persistence:  nil, // no session to persist
		OffloadStore: nil,
		MaskStore:    nil,
		AccumulateUsage: func(r *CompressResult) {
			usageAccumulated = true
			if r.InputTokens != 100 {
				t.Errorf("expected InputTokens=100, got %d", r.InputTokens)
			}
		},
		SyncMessages: func(msgs []llm.ChatMessage) []llm.ChatMessage {
			syncedMessages = msgs
			return msgs
		},
	}

	got, err := ApplyCompress(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}

	// Verify AccumulateUsage was called
	if !usageAccumulated {
		t.Error("AccumulateUsage was not called")
	}

	// Verify SyncMessages was called
	if syncedMessages == nil {
		t.Error("SyncMessages was not called")
	}

	// Verify new messages match
	if len(got.NewMessages) != 2 {
		t.Errorf("expected 2 new messages, got %d", len(got.NewMessages))
	}

	// Verify token count was estimated
	if got.NewTokenCount <= 0 {
		t.Errorf("expected positive NewTokenCount, got %d", got.NewTokenCount)
	}

	// Verify CompressOutput is the original result
	if got.CompressOutput != result {
		t.Error("CompressOutput should point to the original result")
	}

	// Verify TokenTracker was reset
	if tracker.promptTokens != got.NewTokenCount {
		t.Errorf("tracker promptTokens=%d, want %d", tracker.promptTokens, got.NewTokenCount)
	}
	if tracker.completionTokens != 0 {
		t.Errorf("tracker completionTokens=%d, want 0", tracker.completionTokens)
	}
}

func TestApplyCompress_ManualCompressSuccess(t *testing.T) {
	result := sampleCompressResult()
	manualCalled := false
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string) (*CompressResult, error) {
			t.Error("Compress should not be called when UseManual=true")
			return nil, nil
		},
		manualCompressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string) (*CompressResult, error) {
			manualCalled = true
			return result, nil
		},
	}

	tracker := NewTokenTracker(500, 200)

	params := CompressPipelineParams{
		CM:           cm,
		Messages:     []llm.ChatMessage{llm.NewUserMessage("hello")},
		LLMClient:    &mockLLM{},
		Model:        "test-model",
		UseManual:    true,
		TokenTracker: tracker,
	}

	got, err := ApplyCompress(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !manualCalled {
		t.Error("ManualCompress was not called")
	}
	if len(got.NewMessages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(got.NewMessages))
	}
}

func TestApplyCompress_CompressError(t *testing.T) {
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string) (*CompressResult, error) {
			return nil, errors.New("compression failed")
		},
	}

	params := CompressPipelineParams{
		CM:        cm,
		Messages:  []llm.ChatMessage{llm.NewUserMessage("hello")},
		LLMClient: &mockLLM{},
		Model:     "test-model",
	}

	got, err := ApplyCompress(context.Background(), params)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got != nil {
		t.Fatalf("expected nil result, got %+v", got)
	}
	if err.Error() != "compression failed" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestApplyCompress_NilStores(t *testing.T) {
	result := sampleCompressResult()
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string) (*CompressResult, error) {
			return result, nil
		},
	}

	params := CompressPipelineParams{
		CM:           cm,
		Messages:     []llm.ChatMessage{llm.NewUserMessage("hello")},
		LLMClient:    &mockLLM{},
		Model:        "test-model",
		TokenTracker: NewTokenTracker(0, 0),
		Persistence:  nil,
		OffloadStore: nil,
		MaskStore:    nil,
	}

	got, err := ApplyCompress(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if len(got.NewMessages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(got.NewMessages))
	}
}

func TestApplyCompress_NilTrackerAndPersistence(t *testing.T) {
	result := sampleCompressResult()
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string) (*CompressResult, error) {
			return result, nil
		},
	}

	params := CompressPipelineParams{
		CM:           cm,
		Messages:     []llm.ChatMessage{llm.NewUserMessage("hello")},
		LLMClient:    &mockLLM{},
		Model:        "test-model",
		TokenTracker: nil,
		Persistence:  nil,
		OffloadStore: nil,
		MaskStore:    nil,
	}

	got, err := ApplyCompress(context.Background(), params)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	if len(got.NewMessages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(got.NewMessages))
	}
	if got.NewTokenCount <= 0 {
		t.Errorf("expected positive token count, got %d", got.NewTokenCount)
	}
	if got.CompressOutput.InputTokens != 100 {
		t.Errorf("expected InputTokens=100, got %d", got.CompressOutput.InputTokens)
	}
}
