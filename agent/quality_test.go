package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

// ----------------------------------------------------------------
// containsSemanticMatch tests
// ----------------------------------------------------------------

func TestContainsSemanticMatch_ExactSubstring(t *testing.T) {
	if !containsSemanticMatch("the file compress.go was read", "compress.go") {
		t.Error("expected match for exact substring")
	}
}

func TestContainsSemanticMatch_CaseInsensitive(t *testing.T) {
	if !containsSemanticMatch("The File Compress.Go Was Read", "compress.go") {
		t.Error("expected case-insensitive match")
	}
}

func TestContainsSemanticMatch_KeywordOverlap(t *testing.T) {
	// "nil pointer dereference" → split to words like ["nil", "pointer", "dereference"]
	// "pointer was nil in function" contains "nil" and "pointer" → 2/3 = 0.67 >= 0.6
	if !containsSemanticMatch("pointer was nil in function", "nil pointer dereference") {
		t.Error("expected match via keyword overlap")
	}
}

func TestContainsSemanticMatch_NoMatch(t *testing.T) {
	if containsSemanticMatch("hello world", "quantum physics") {
		t.Error("expected no match for unrelated text")
	}
}

func TestContainsSemanticMatch_EmptyInput(t *testing.T) {
	if containsSemanticMatch("", "target") {
		t.Error("expected no match for empty text")
	}
	if containsSemanticMatch("text", "") {
		t.Error("expected no match for empty target")
	}
}

// ----------------------------------------------------------------
// extractFilePaths tests
// ----------------------------------------------------------------

func TestExtractFilePaths_AbsolutePaths(t *testing.T) {
	text := "Read the files /workspace/xbot/agent/compress.go and /usr/local/bin/app"
	paths := extractFilePaths(text)

	found := false
	for _, p := range paths {
		if strings.Contains(p, "compress.go") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find compress.go, got %v", paths)
	}
}

func TestExtractFilePaths_RelativePaths(t *testing.T) {
	text := "Check ./relative/path.txt and ../parent/file.go"
	paths := extractFilePaths(text)

	if len(paths) < 2 {
		t.Errorf("expected at least 2 paths, got %v", paths)
	}
}

func TestExtractFilePaths_TildePaths(t *testing.T) {
	text := "Edit ~/config/settings.json"
	paths := extractFilePaths(text)

	found := false
	for _, p := range paths {
		if strings.Contains(p, "settings.json") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find settings.json, got %v", paths)
	}
}

func TestExtractFilePaths_NoPaths(t *testing.T) {
	text := "Hello, how are you today?"
	paths := extractFilePaths(text)
	if len(paths) > 0 {
		t.Errorf("expected no paths, got %v", paths)
	}
}

func TestExtractFilePaths_Deduplication(t *testing.T) {
	text := "/path/to/file.go and /path/to/file.go"
	paths := extractFilePaths(text)
	if len(paths) > 1 {
		t.Errorf("expected deduplicated paths, got %v", paths)
	}
}

// ----------------------------------------------------------------
// splitToWords tests
// ----------------------------------------------------------------

func TestSplitToWords_BasicSplitting(t *testing.T) {
	words := splitToWords("the quick brown fox jumps over lazy dog")
	// "the", "over" are stop words, should be removed
	for _, w := range words {
		if w == "the" || w == "over" {
			t.Errorf("stop word should be removed: %s", w)
		}
	}
	if len(words) == 0 {
		t.Error("expected non-empty result after stop word removal")
	}
}

func TestSplitToWords_CodeText(t *testing.T) {
	words := splitToWords("handleCompress encountered nil pointer error")
	// Stop words removed: "nil" is not a stop word, "error" is not
	found := false
	for _, w := range words {
		if strings.EqualFold(w, "handleCompress") || strings.EqualFold(w, "nil") || strings.EqualFold(w, "error") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find meaningful words, got %v", words)
	}
}

func TestSplitToWords_EmptyString(t *testing.T) {
	words := splitToWords("")
	if len(words) != 0 {
		t.Errorf("expected empty result for empty string, got %v", words)
	}
}

func TestSplitToWords_SingleCharFiltered(t *testing.T) {
	words := splitToWords("a b c d e")
	// Single letters and "a" (stop word) should all be removed
	for _, w := range words {
		if len(w) <= 1 {
			t.Errorf("single char should be filtered: %s", w)
		}
	}
}

// ----------------------------------------------------------------
// Integration: extractDialogueFromTail with offload
// ----------------------------------------------------------------

func TestExtractDialogueFromTail_OffloadMarker(t *testing.T) {
	offloadContent := "📂 [offload:summary of 50 messages about implementing context compression]"
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: "{}"}),
		llm.NewToolMessage("Read", "1", "{}", offloadContent),
		llm.NewAssistantMessage("done"),
	}

	result := extractDialogueFromTail(tail)

	// Offload content should be preserved intact (not truncated)
	var foundFull bool
	for _, msg := range result {
		if strings.Contains(msg.Content, offloadContent) {
			foundFull = true
			break
		}
	}
	if !foundFull {
		t.Errorf("offload content should be preserved intact, got results: %v", result)
	}
}

func TestExtractDialogueFromTail_RegularToolTruncated(t *testing.T) {
	longContent := strings.Repeat("x", 500)
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: "{}"}),
		llm.NewToolMessage("Read", "1", "{}", longContent),
		llm.NewAssistantMessage("done"),
	}

	result := extractDialogueFromTail(tail)

	// Long tool content should be truncated (not contain the full 500 chars)
	for _, msg := range result {
		if strings.Contains(msg.Content, longContent) {
			t.Error("long tool content should have been truncated")
			break
		}
	}
}

func TestExtractDialogueFromTail_OffloadIDStripped(t *testing.T) {
	// Realistic offload marker with an ID that should be stripped
	offloadContent := "📂 [offload:ol_abc12345] Read(/workspace/agent/engine.go)\nPackage: agent\nImports: fmt, os\nfunc Run(...) {...}"
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("reading", llm.ToolCall{ID: "1", Name: "Read", Arguments: `{"path":"engine.go"}`}),
		llm.NewToolMessage("Read", "1", `{"path":"engine.go"}`, offloadContent),
		llm.NewAssistantMessage("done"),
	}

	result := extractDialogueFromTail(tail)

	for _, msg := range result {
		if strings.Contains(msg.Content, "ol_abc12345") {
			t.Error("offload ID should be stripped from session view")
		}
		// Summary text should be preserved
		if strings.Contains(msg.Content, "Read(/workspace/agent/engine.go)") {
			return // OK
		}
	}
	t.Error("summary text should be preserved after stripping offload ID")
}

func TestExtractDialogueFromTail_MaskedMarkerStripped(t *testing.T) {
	maskedContent := "📂 [masked:mk_deadbeef] Shell(cat /etc/hosts) — 500 chars — 结果已遮蔽，使用 recall_masked 可查看完整内容"
	tail := []llm.ChatMessage{
		llm.NewUserMessage("do work"),
		makeAssistantWithToolCalls("checking", llm.ToolCall{ID: "1", Name: "Shell", Arguments: `{}`}),
		llm.NewToolMessage("Shell", "1", `{}`, maskedContent),
		llm.NewAssistantMessage("done"),
	}

	result := extractDialogueFromTail(tail)

	for _, msg := range result {
		if strings.Contains(msg.Content, "mk_deadbeef") {
			t.Error("mask ID should be stripped from session view")
		}
	}
	// Should contain some info about the tool call
	found := false
	for _, msg := range result {
		if strings.Contains(msg.Content, "Shell(cat /etc/hosts)") {
			found = true
		}
	}
	if !found {
		t.Error("tool call info should be preserved in stripped mask summary")
	}
}

func TestStripRecallID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantID   bool
		wantText string
	}{
		{
			name:     "offload marker",
			input:    "📂 [offload:ol_abc12345] Read(/path)\nsummary",
			wantText: "📂 Read(/path)\nsummary",
		},
		{
			name:     "masked marker",
			input:    "📂 [masked:mk_deadbeef] Shell(ls) — 500 chars",
			wantText: "📂 Shell(ls) — 500 chars",
		},
		{
			name:     "no closing bracket with space",
			input:    "📂 [offload:no-space-after-bracket]",
			wantText: "📂 [offload:no-space-after-bracket]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripRecallID(tt.input)
			if got != tt.wantText {
				t.Errorf("stripRecallID(%q) = %q, want %q", tt.input, got, tt.wantText)
			}
		})
	}
}
