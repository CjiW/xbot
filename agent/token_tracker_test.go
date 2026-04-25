package agent

import (
	"testing"

	"xbot/llm"
)

const testModel = "gpt-4o"

// helper to build simple messages for token counting tests
func makeMessages(n int) []llm.ChatMessage {
	msgs := make([]llm.ChatMessage, n)
	for i := range msgs {
		msgs[i] = llm.ChatMessage{Role: "user", Content: "hello world"}
	}
	return msgs
}

// ----------------------------------------------------------------
// NewTokenTracker
// ----------------------------------------------------------------

func TestTokenTracker_New_ZeroValues(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	if tt.PromptTokens() != 0 {
		t.Errorf("expected promptTokens=0, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 0 {
		t.Errorf("expected completionTokens=0, got %d", tt.CompletionTokens())
	}
	if tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=false for zero values")
	}
	if tt.HadLLMCall() {
		t.Error("expected hadLLMCall=false initially")
	}
	if tt.MsgCountAtCall() != 0 {
		t.Errorf("expected msgCountAtCall=0, got %d", tt.MsgCountAtCall())
	}
}

func TestTokenTracker_New_NonZeroPromptTokens(t *testing.T) {
	tt := NewTokenTracker(500, 200)
	if tt.PromptTokens() != 500 {
		t.Errorf("expected promptTokens=500, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 200 {
		t.Errorf("expected completionTokens=200, got %d", tt.CompletionTokens())
	}
	if !tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=true when promptTokens > 0")
	}
}

func TestTokenTracker_New_NonZeroCompletionOnly(t *testing.T) {
	// completionTokens > 0 but promptTokens == 0 → restoredFromDB should be false
	tt := NewTokenTracker(0, 300)
	if tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=false when promptTokens=0 even if completionTokens>0")
	}
}

// ----------------------------------------------------------------
// RecordLLMCall
// ----------------------------------------------------------------

func TestTokenTracker_RecordLLMCall(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 250, 5)

	if tt.PromptTokens() != 1000 {
		t.Errorf("expected promptTokens=1000, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 250 {
		t.Errorf("expected completionTokens=250, got %d", tt.CompletionTokens())
	}
	if tt.MsgCountAtCall() != 5 {
		t.Errorf("expected msgCountAtCall=5, got %d", tt.MsgCountAtCall())
	}
	if !tt.HadLLMCall() {
		t.Error("expected hadLLMCall=true after RecordLLMCall")
	}
}

func TestTokenTracker_RecordLLMCall_Overwrite(t *testing.T) {
	tt := NewTokenTracker(100, 50)
	tt.RecordLLMCall(2000, 400, 8)

	if tt.PromptTokens() != 2000 {
		t.Errorf("expected promptTokens=2000, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 400 {
		t.Errorf("expected completionTokens=400, got %d", tt.CompletionTokens())
	}
}

// ----------------------------------------------------------------
// ResetAfterCompress
// ----------------------------------------------------------------

func TestTokenTracker_ResetAfterCompress(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(5000, 800, 10)

	tt.ResetAfterCompress(2000, 4)

	if tt.PromptTokens() != 2000 {
		t.Errorf("expected promptTokens=2000, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 0 {
		t.Errorf("expected completionTokens=0 after compress reset, got %d", tt.CompletionTokens())
	}
	if tt.MsgCountAtCall() != 4 {
		t.Errorf("expected msgCountAtCall=4, got %d", tt.MsgCountAtCall())
	}
	// hadLLMCall should remain true
	if !tt.HadLLMCall() {
		t.Error("expected hadLLMCall to remain true after compress")
	}
}

// ----------------------------------------------------------------
// MarkRestoredFromDB
// ----------------------------------------------------------------

func TestTokenTracker_MarkRestoredFromDB(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	if tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=false initially")
	}
	tt.MarkRestoredFromDB()
	if !tt.RestoredFromDB() {
		t.Error("expected restoredFromDB=true after MarkRestoredFromDB")
	}
}

// ----------------------------------------------------------------
// DetectTruncation
// ----------------------------------------------------------------

func TestTokenTracker_DetectTruncation_NoTruncation(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200, 5)

	msgs := makeMessages(5)
	re, prev := tt.DetectTruncation(msgs, testModel)
	if re {
		t.Error("expected no truncation detected when msg count equals boundary")
	}
	if prev != 0 {
		t.Errorf("expected prevPromptTokens=0, got %d", prev)
	}
}

func TestTokenTracker_DetectTruncation_MoreMessages(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200, 5)

	msgs := makeMessages(8) // more messages than boundary → not truncated
	re, prev := tt.DetectTruncation(msgs, testModel)
	if re {
		t.Error("expected no truncation when messages grew past boundary")
	}
	if prev != 0 {
		t.Errorf("expected prevPromptTokens=0, got %d", prev)
	}
}

func TestTokenTracker_DetectTruncation_Truncated(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200, 10)

	// Messages were truncated from 10 to 3
	msgs := makeMessages(3)
	re, prev := tt.DetectTruncation(msgs, testModel)

	if !re {
		t.Fatal("expected truncation to be detected")
	}
	if prev != 1000 {
		t.Errorf("expected prevPromptTokens=1000, got %d", prev)
	}
	// After truncation, promptTokens should be re-estimated (non-zero for valid content)
	if tt.PromptTokens() <= 0 {
		t.Error("expected promptTokens to be re-estimated after truncation")
	}
	if tt.PromptTokens() >= 1000 {
		t.Errorf("expected promptTokens to be smaller than original after truncation, got %d", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 0 {
		t.Errorf("expected completionTokens=0 after truncation, got %d", tt.CompletionTokens())
	}
	if tt.MsgCountAtCall() != 3 {
		t.Errorf("expected msgCountAtCall=3 after truncation, got %d", tt.MsgCountAtCall())
	}
}

func TestTokenTracker_DetectTruncation_ZeroBoundary(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	// No LLM call recorded → msgCountAtCall=0, should return false
	msgs := makeMessages(2)
	re, prev := tt.DetectTruncation(msgs, testModel)
	if re {
		t.Error("expected no truncation when boundary is 0")
	}
	if prev != 0 {
		t.Errorf("expected prevPromptTokens=0, got %d", prev)
	}
}

func TestTokenTracker_DetectTruncation_EmptyMessages(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200, 5)

	// Truncated to empty — should still work but with 0 estimated tokens
	// since there are no messages to count.
	re, prev := tt.DetectTruncation([]llm.ChatMessage{}, testModel)
	// empty messages → CountMessagesTokens returns 0 → estimated <= 0 → returns false
	if re {
		t.Error("expected no re-estimation when remaining messages are empty (estimated=0)")
	}
	if prev != 0 {
		t.Errorf("expected prevPromptTokens=0 for empty estimation, got %d", prev)
	}
}

// ----------------------------------------------------------------
// EstimateTotal
// ----------------------------------------------------------------

func TestTokenTracker_EstimateTotal_APIPlusCompletion(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200, 5)

	// Exactly 5 messages — no tool messages after boundary
	msgs := makeMessages(5)
	total, source := tt.EstimateTotal(msgs, testModel)

	if total != 1200 {
		t.Errorf("expected total=1200 (1000+200), got %d", total)
	}
	if source != "api+completion" {
		t.Errorf("expected source='api+completion', got %q", source)
	}
}

func TestTokenTracker_EstimateTotal_APIPlusCompletionPlusToolDelta(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200, 3)

	// 6 messages total, boundary at 3 → tool messages from index 4 onward (indices 4,5)
	msgs := makeMessages(6)
	total, source := tt.EstimateTotal(msgs, testModel)

	if total <= 1200 {
		t.Errorf("expected total > 1200 (with tool delta), got %d", total)
	}
	if source != "api+completion+tool_delta" {
		t.Errorf("expected source='api+completion+tool_delta', got %q", source)
	}
}

func TestTokenTracker_EstimateTotal_APIPlusCompletionExactBoundary(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200, 5)

	// 6 messages → len=6, boundary+1=6 → NOT greater than boundary+1, no delta
	msgs := makeMessages(6)
	total, source := tt.EstimateTotal(msgs, testModel)

	if total != 1200 {
		t.Errorf("expected total=1200 (no delta), got %d", total)
	}
	if source != "api+completion" {
		t.Errorf("expected source='api+completion', got %q", source)
	}
}

func TestTokenTracker_EstimateTotal_Restored(t *testing.T) {
	tt := NewTokenTracker(800, 300)
	// No RecordLLMCall → msgCountAtCall=0, but promptTokens > 0

	msgs := makeMessages(3)
	total, source := tt.EstimateTotal(msgs, testModel)

	// Restored path: uses promptTokens only (not completionTokens)
	if total != 800 {
		t.Errorf("expected total=800 (restored, prompt only), got %d", total)
	}
	if source != "restored" {
		t.Errorf("expected source='restored', got %q", source)
	}
}

func TestTokenTracker_EstimateTotal_LocalEstimateFallback(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	// No tokens, no LLM call

	msgs := makeMessages(3)
	total, source := tt.EstimateTotal(msgs, testModel)

	if total <= 0 {
		t.Errorf("expected total > 0 from local estimation, got %d", total)
	}
	if source != "local_estimate_fallback" {
		t.Errorf("expected source='local_estimate_fallback', got %q", source)
	}

	// Verify 1.5x multiplier: manually count tokens
	rawCount, err := llm.CountMessagesTokens(msgs, testModel)
	if err != nil {
		t.Fatalf("CountMessagesTokens error: %v", err)
	}
	expectedTotal := int64(float64(rawCount) * 1.5)
	if total != expectedTotal {
		t.Errorf("expected total=%d (1.5x of %d), got %d", expectedTotal, rawCount, total)
	}
}

func TestTokenTracker_EstimateTotal_NoData(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	// No tokens, no LLM call, no messages

	total, source := tt.EstimateTotal([]llm.ChatMessage{}, testModel)

	if total != 0 {
		t.Errorf("expected total=0 for no data, got %d", total)
	}
	if source != "no_data" {
		t.Errorf("expected source='no_data', got %q", source)
	}
}

// ----------------------------------------------------------------
// SaveState
// ----------------------------------------------------------------

func TestTokenTracker_SaveState_NilFn(t *testing.T) {
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 200, 5)
	// Should not panic with nil saveFn
	tt.SaveState(nil)
}

func TestTokenTracker_SaveState_NoLLMCall(t *testing.T) {
	called := false
	tt := NewTokenTracker(500, 100) // has tokens from DB, but no LLM call in this Run
	tt.SaveState(func(pt, ct int64) {
		called = true
	})
	if called {
		t.Error("expected saveFn NOT to be called when no LLM call in this Run")
	}
}

func TestTokenTracker_SaveState_ZeroPromptTokens(t *testing.T) {
	called := false
	tt := NewTokenTracker(0, 0)
	// Manually set hadLLMCall without promptTokens
	tt.RecordLLMCall(0, 100, 3)
	tt.SaveState(func(pt, ct int64) {
		called = true
	})
	if called {
		t.Error("expected saveFn NOT to be called when promptTokens=0")
	}
}

func TestTokenTracker_SaveState_Called(t *testing.T) {
	var savedPrompt, savedCompletion int64
	tt := NewTokenTracker(0, 0)
	tt.RecordLLMCall(1000, 250, 5)

	tt.SaveState(func(pt, ct int64) {
		savedPrompt = pt
		savedCompletion = ct
	})

	if savedPrompt != 1000 {
		t.Errorf("expected savedPrompt=1000, got %d", savedPrompt)
	}
	if savedCompletion != 250 {
		t.Errorf("expected savedCompletion=250, got %d", savedCompletion)
	}
}

// ----------------------------------------------------------------
// Getters
// ----------------------------------------------------------------

func TestTokenTracker_Getters(t *testing.T) {
	tt := NewTokenTracker(100, 50)

	// Before any LLM call
	if tt.PromptTokens() != 100 {
		t.Errorf("PromptTokens() = %d, want 100", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 50 {
		t.Errorf("CompletionTokens() = %d, want 50", tt.CompletionTokens())
	}
	if tt.MsgCountAtCall() != 0 {
		t.Errorf("MsgCountAtCall() = %d, want 0", tt.MsgCountAtCall())
	}
	if tt.HadLLMCall() {
		t.Error("HadLLMCall() = true, want false")
	}
	if !tt.RestoredFromDB() {
		t.Error("RestoredFromDB() = false, want true")
	}

	// After LLM call
	tt.RecordLLMCall(2000, 400, 7)
	if tt.PromptTokens() != 2000 {
		t.Errorf("PromptTokens() = %d, want 2000", tt.PromptTokens())
	}
	if tt.CompletionTokens() != 400 {
		t.Errorf("CompletionTokens() = %d, want 400", tt.CompletionTokens())
	}
	if tt.MsgCountAtCall() != 7 {
		t.Errorf("MsgCountAtCall() = %d, want 7", tt.MsgCountAtCall())
	}
	if !tt.HadLLMCall() {
		t.Error("HadLLMCall() = false, want true")
	}
}
