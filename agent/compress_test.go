package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

func makeAssistantWithToolCalls(content string, toolCalls ...llm.ToolCall) llm.ChatMessage {
	return llm.ChatMessage{
		Role:      "assistant",
		Content:   content,
		ToolCalls: toolCalls,
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"hello world", 5, "hello...[truncated]"},
		{"你好世界测试", 3, "你好世...[truncated]"},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncateRunes(tt.input, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
		}
	}
}

func TestExtractDialogueFromTail_Basic(t *testing.T) {
	tail := []llm.ChatMessage{
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi there"),
	}
	result := extractDialogueFromTail(tail)
	if len(result) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(result))
	}
	if result[0].Role != "user" || result[0].Content != "hello" {
		t.Errorf("expected user message, got %+v", result[0])
	}
	if result[1].Role != "assistant" || result[1].Content != "hi there" {
		t.Errorf("expected assistant message, got %+v", result[1])
	}
}

func TestExtractDialogueFromTail_ToolGroupFolding(t *testing.T) {
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do something"),
		{
			Role:      "assistant",
			Content:   "let me check",
			ToolCalls: []llm.ToolCall{{ID: "1", Name: "Read", Arguments: `{"path":"foo.go"}`}},
		},
		{Role: "tool", Content: "file content here"},
		llm.NewAssistantMessage("done"),
	}
	result := extractDialogueFromTail(tail)

	if len(result) != 3 {
		t.Fatalf("expected 3 messages (user, folded-assistant, assistant), got %d", len(result))
	}
	if result[0].Content != "do something" {
		t.Error("first message should be user")
	}
	if !strings.Contains(result[1].Content, "Read") {
		t.Error("folded message should mention tool name")
	}
	if result[2].Content != "done" {
		t.Error("last message should be final assistant")
	}
}

func TestExtractDialogueFromTail_OffloadStrip(t *testing.T) {
	tail := []llm.ChatMessage{
		{
			Role:      "assistant",
			Content:   "",
			ToolCalls: []llm.ToolCall{{ID: "1", Name: "Read", Arguments: "{}"}},
		},
		{Role: "tool", Content: "📂 [offload:ol_abc123] Read(foo.go)\nfile summary here"},
	}
	result := extractDialogueFromTail(tail)
	if len(result) != 1 {
		t.Fatalf("expected 1 folded message, got %d", len(result))
	}
	if strings.Contains(result[0].Content, "ol_abc123") {
		t.Error("offload ID should be stripped")
	}
	if !strings.Contains(result[0].Content, "Read(foo.go)") {
		t.Error("should preserve the tool info after stripping ID")
	}
}

func TestTruncateArgs(t *testing.T) {
	short := "short"
	if truncateArgs(short, 10) != "short" {
		t.Error("short args should not be truncated")
	}
	long := strings.Repeat("x", 200)
	result := truncateArgs(long, 100)
	if len([]rune(result)) > 104 {
		t.Errorf("long args should be truncated, got len=%d", len([]rune(result)))
	}
	if !strings.HasSuffix(result, "...") {
		t.Error("truncated args should end with ...")
	}
}
