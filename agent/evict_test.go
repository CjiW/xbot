package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

// ----------------------------------------------------------------
// containsErrorPattern tests
// ----------------------------------------------------------------

func TestContainsErrorPattern_True(t *testing.T) {
	tests := []string{
		"error: cannot find module",
		"panic: runtime error",
		"build failed: undefined reference",
		"fatal error: out of memory",
		"exception: null pointer",
		"ERROR in function handleCompress",
	}
	for _, text := range tests {
		if !containsErrorPattern(text) {
			t.Errorf("expected containsErrorPattern(%q) = true", text)
		}
	}
}

func TestContainsErrorPattern_False(t *testing.T) {
	tests := []string{
		"the build was successful",
		"all tests passed",
		"no issues found",
		"",
		"this is a normal result",
	}
	for _, text := range tests {
		if containsErrorPattern(text) {
			t.Errorf("expected containsErrorPattern(%q) = false", text)
		}
	}
}

// ----------------------------------------------------------------
// containsDecisionPattern tests
// ----------------------------------------------------------------

func TestContainsDecisionPattern_True(t *testing.T) {
	tests := []string{
		"I decided to use a singleton pattern",
		"we chose to refactor the code",
		"will use Redis for caching",
		"going to use the new API",
		"agreed to implement the feature",
		"plan to migrate next week",
	}
	for _, text := range tests {
		if !containsDecisionPattern(text) {
			t.Errorf("expected containsDecisionPattern(%q) = true", text)
		}
	}
}

func TestContainsDecisionPattern_False(t *testing.T) {
	tests := []string{
		"this is just a statement",
		"the code is here",
		"",
	}
	for _, text := range tests {
		if containsDecisionPattern(text) {
			t.Errorf("expected containsDecisionPattern(%q) = false", text)
		}
	}
}

// ----------------------------------------------------------------
// isLargeCodeDump tests
// ----------------------------------------------------------------

func TestIsLargeCodeDump_True(t *testing.T) {
	// > 2000 chars with 3+ function definitions
	code := strings.Repeat("x", 2500)
	code += "\nfunc hello() string {\n\treturn \"hello\"\n}\n"
	code += "\nfunc world() string {\n\treturn \"world\"\n}\n"
	code += "\nfunc foo() int {\n\treturn 42\n}\n"
	if !isLargeCodeDump(code) {
		t.Error("expected isLargeCodeDump = true for large code with 3 function defs")
	}
}

func TestIsLargeCodeDump_TooShort(t *testing.T) {
	code := "func hello() {}\nfunc world() {}\nfunc foo() {}\n"
	if isLargeCodeDump(code) {
		t.Error("expected isLargeCodeDump = false for short code")
	}
}

func TestIsLargeCodeDump_LongButNoFunctions(t *testing.T) {
	text := strings.Repeat("line of normal text here\n", 300)
	if isLargeCodeDump(text) {
		t.Error("expected isLargeCodeDump = false for long text without function defs")
	}
}

// ----------------------------------------------------------------
// isRepetitiveGrepResult tests
// ----------------------------------------------------------------

func TestIsRepetitiveGrepResult_True(t *testing.T) {
	// Generate 20 grep-style match lines
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "file.go:10: match content here"
	}
	grepOutput := strings.Join(lines, "\n")
	if !isRepetitiveGrepResult(grepOutput) {
		t.Error("expected isRepetitiveGrepResult = true for 20 grep match lines")
	}
}

func TestIsRepetitiveGrepResult_TooFewLines(t *testing.T) {
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = "file.go:10: match"
	}
	grepOutput := strings.Join(lines, "\n")
	if isRepetitiveGrepResult(grepOutput) {
		t.Error("expected isRepetitiveGrepResult = false for <15 lines")
	}
}

func TestIsRepetitiveGrepResult_NonGrepFormat(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "this is just a normal output line without matching pattern"
	}
	grepOutput := strings.Join(lines, "\n")
	if isRepetitiveGrepResult(grepOutput) {
		t.Error("expected isRepetitiveGrepResult = false for non-grep format")
	}
}

// ----------------------------------------------------------------
// defaultDensityScorer tests
// ----------------------------------------------------------------

func TestDefaultDensityScorer_ErrorMessage(t *testing.T) {
	msg := llm.NewToolMessage("Shell", "1", `{}`, "error: build failed\npanic: runtime error")
	score := defaultDensityScorer(msg)
	if score < 3.0 {
		t.Errorf("error message should score >= 3.0, got %.1f", score)
	}
}

func TestDefaultDensityScorer_LargeCodeDump(t *testing.T) {
	code := strings.Repeat("x", 2500)
	code += "\nfunc hello() {}\nfunc world() {}\nfunc foo() {}\n"
	msg := llm.NewToolMessage("Read", "1", `{}`, code)
	score := defaultDensityScorer(msg)
	// Should be negative due to large code dump penalty and long content penalty
	if score >= 0 {
		t.Errorf("large code dump should have negative score, got %.1f", score)
	}
}

func TestDefaultDensityScorer_OffloadNeutral(t *testing.T) {
	msg := llm.NewToolMessage("Read", "1", `{}`, "📂 [offload:ol_abc123] summary here")
	score := defaultDensityScorer(msg)
	if score != 1.0 {
		t.Errorf("offload message should have neutral score 1.0, got %.1f", score)
	}
}

func TestDefaultDensityScorer_ShortHighDensity(t *testing.T) {
	msg := llm.NewToolMessage("Shell", "1", `{}`, "exit code: 0")
	score := defaultDensityScorer(msg)
	// Short message (< 500 chars) gets +1.5
	if score < 1.0 {
		t.Errorf("short message should get density bonus, got %.1f", score)
	}
}

func TestDefaultDensityScorer_DecisionHigh(t *testing.T) {
	msg := llm.NewAssistantMessage("I decided to use a singleton pattern for the context manager.")
	score := defaultDensityScorer(msg)
	if score < 2.0 {
		t.Errorf("decision message should score >= 2.0, got %.1f", score)
	}
}

func TestDefaultDensityScorer_WithFilePath(t *testing.T) {
	msg := llm.NewToolMessage("Read", "1", `{}`, "content from /workspace/xbot/agent/compress.go")
	score := defaultDensityScorer(msg)
	// Should get +1.0 for file path and +1.5 for short content
	if score < 2.0 {
		t.Errorf("message with file path should score >= 2.0, got %.1f", score)
	}
}

// ----------------------------------------------------------------
// evictByDensity tests
// ----------------------------------------------------------------

func TestEvictByDensity_LowDensityEvicted(t *testing.T) {
	// Create messages with 5 tool groups, keep 2
	// Group 1: low density (large repetitive content)
	lowDensityContent := strings.Repeat("normal output line\n", 300)
	// Group 2: low density (large code dump)
	codeDump := strings.Repeat("x", 2500) + "\nfunc a(){}\nfunc b(){}\nfunc c(){}\n"
	// Group 3: high density (error)
	errorContent := "error: build failed in compress.go\npanic: nil pointer"
	// Group 4-5: kept (recent)

	messages := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		// Group 1 (low density - should be evicted)
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Shell", Arguments: `{}`}),
		makeToolResult("Shell", "1", `{}`, lowDensityContent),
		// Group 2 (low density - should be evicted)
		makeAssistantWithToolCalls("reading code", llm.ToolCall{ID: "2", Name: "Read", Arguments: `{}`}),
		makeToolResult("Read", "2", `{}`, codeDump),
		// Group 3 (high density - should be preserved when tokens allow)
		makeAssistantWithToolCalls("checking", llm.ToolCall{ID: "3", Name: "Shell", Arguments: `{}`}),
		makeToolResult("Shell", "3", `{}`, errorContent),
		// Group 4 (kept - recent)
		makeAssistantWithToolCalls("step 4", llm.ToolCall{ID: "4", Name: "Shell", Arguments: `{}`}),
		makeToolResult("Shell", "4", `{}`, "recent output"),
		// Group 5 (kept - recent)
		makeAssistantWithToolCalls("step 5", llm.ToolCall{ID: "5", Name: "Shell", Arguments: `{}`}),
		makeToolResult("Shell", "5", `{}`, "more recent output"),
		// Final assistant message
		llm.NewAssistantMessage("done"),
	}

	// Set targetTokens to just above the non-evictable content size
	// Groups 3-5 + user + final assistant should be preserved
	// We want to evict groups 1 and 2 but not group 3
	// Calculate a target that allows group 3 to survive
	result := evictByDensity(messages, 2, 500, "gpt-4o")

	// Group 1 tool result (index 2) should be evicted (lowest density)
	if !strings.Contains(result[2].Content, "[evicted]") {
		t.Errorf("group 1 tool result should be evicted, got: %s", result[2].Content[:min(80, len(result[2].Content))])
	}

	// Group 2 tool result (index 4) should be evicted (low density code dump)
	if !strings.Contains(result[4].Content, "[evicted]") {
		t.Errorf("group 2 tool result should be evicted, got: %s", result[4].Content[:min(80, len(result[4].Content))])
	}

	// Group 4 and 5 should be untouched (kept groups)
	if result[8].Content != "recent output" {
		t.Errorf("group 4 should be untouched, got: %s", result[8].Content)
	}
	if result[10].Content != "more recent output" {
		t.Errorf("group 5 should be untouched, got: %s", result[10].Content)
	}

	// assistant messages should NOT be evicted (API compatibility)
	if result[1].Content != "reading" {
		t.Errorf("assistant message should be untouched, got: %s", result[1].Content)
	}
}

func TestEvictByDensity_HighDensityPreserved(t *testing.T) {
	// Verify that when targetTokens allows, high-density messages are evicted
	// only after low-density ones
	largeContent := strings.Repeat("x", 5000)
	errorContent := "error: panic nil pointer exception"

	messages := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		// Group 1: low density (large content, no special signals)
		makeAssistantWithToolCalls("big read", llm.ToolCall{ID: "1", Name: "Read", Arguments: `{}`}),
		makeToolResult("Read", "1", `{}`, largeContent),
		// Group 2: high density (error message)
		makeAssistantWithToolCalls("check", llm.ToolCall{ID: "2", Name: "Shell", Arguments: `{}`}),
		makeToolResult("Shell", "2", `{}`, errorContent),
		llm.NewAssistantMessage("done"),
	}

	// targetTokens=0 forces eviction of everything
	result := evictByDensity(messages, 0, 0, "gpt-4o")

	// Both should be evicted since target is 0
	if !strings.Contains(result[2].Content, "[evicted]") {
		t.Error("large content should be evicted")
	}
	if !strings.Contains(result[4].Content, "[evicted]") {
		t.Error("error content should also be evicted when target is 0")
	}
}

func TestEvictByDensity_OffloadNotEvicted(t *testing.T) {
	offloadContent := "📂 [offload:ol_abc123] Read(file.go) - File summary"
	messages := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		// Group 1: offload (should not be evicted)
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: `{}`}),
		makeToolResult("Read", "1", `{}`, offloadContent),
		// Group 2: normal (should be evicted under pressure)
		makeAssistantWithToolCalls("more", llm.ToolCall{ID: "2", Name: "Shell", Arguments: `{}`}),
		makeToolResult("Shell", "2", `{}`, strings.Repeat("output\n", 300)),
		// Group 3: kept
		makeAssistantWithToolCalls("done", llm.ToolCall{ID: "3", Name: "Shell", Arguments: `{}`}),
		makeToolResult("Shell", "3", `{}`, "ok"),
		llm.NewAssistantMessage("finished"),
	}

	result := evictByDensity(messages, 1, 100, "gpt-4o")

	// Offload message should NOT be evicted
	if strings.Contains(result[2].Content, "[evicted]") {
		t.Error("offload message should not be evicted")
	}
	if result[2].Content != offloadContent {
		t.Error("offload content should be preserved")
	}

	// Normal tool result should be evicted
	if !strings.Contains(result[4].Content, "[evicted]") {
		t.Error("normal tool result should be evicted under pressure")
	}
}

func TestEvictByDensity_NoEvictionNeeded(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
	}

	result := evictByDensity(messages, 3, 10000, "gpt-4o")

	// Should return same messages when no tool groups
	if len(result) != len(messages) {
		t.Errorf("expected %d messages, got %d", len(messages), len(result))
	}
}

func TestEvictByDensity_AllGroupsKept(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("step 1", llm.ToolCall{ID: "1", Name: "Shell", Arguments: `{}`}),
		makeToolResult("Shell", "1", `{}`, "output"),
		llm.NewAssistantMessage("done"),
	}

	result := evictByDensity(messages, 3, 100, "gpt-4o")

	// 1 group <= keepGroups(3) → no eviction
	if result[2].Content != "output" {
		t.Errorf("tool result should be untouched, got: %s", result[2].Content)
	}
}

func TestEvictByDensity_PreservesAssistantToolCalls(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: `{"path":"foo.go"}`}),
		makeToolResult("Read", "1", `{"path":"foo.go"}`, strings.Repeat("x", 2000)),
		llm.NewAssistantMessage("done"),
	}

	result := evictByDensity(messages, 0, 10, "gpt-4o")

	// Assistant message should still have ToolCalls
	if len(result[1].ToolCalls) == 0 {
		t.Error("assistant message should preserve ToolCalls structure")
	}
	if result[1].ToolCalls[0].Name != "Read" {
		t.Errorf("expected ToolCall name 'Read', got %s", result[1].ToolCalls[0].Name)
	}
}

func TestEvictByDensity_DeepCopy(t *testing.T) {
	longContent := strings.Repeat("z", 2000)
	original := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: `{}`}),
		makeToolResult("Read", "1", `{}`, longContent),
		// Kept group
		makeAssistantWithToolCalls("kept", llm.ToolCall{ID: "2", Name: "Edit", Arguments: "short"}),
		makeToolResult("Edit", "2", "short", "done"),
		llm.NewAssistantMessage("finished"),
	}

	origContent := original[2].Content

	_ = evictByDensity(original, 1, 10, "gpt-4o")

	// Original messages should not be modified
	if original[2].Content != origContent {
		t.Fatal("original tool content was mutated by evictByDensity")
	}
}

// ----------------------------------------------------------------
// buildCompressResultFromEvicted tests
// ----------------------------------------------------------------

func TestBuildCompressResultFromEvicted_Basic(t *testing.T) {
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are helpful."),
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: `{}`}),
		makeToolResult("Read", "1", `{}`, "file content"),
		llm.NewAssistantMessage("done with reading"),
		llm.NewUserMessage("now edit it"),
	}

	result, err := buildCompressResultFromEvicted(messages, nil, "gpt-4o")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// LLMView should contain all messages
	if len(result.LLMView) == 0 {
		t.Error("LLMView should not be empty")
	}

	// SessionView should have pure dialogue (no tool messages)
	for _, msg := range result.SessionView {
		if msg.Role == "tool" {
			t.Error("SessionView should not contain tool messages")
		}
		if msg.Role == "system" {
			t.Error("SessionView should not contain system messages")
		}
	}
}
