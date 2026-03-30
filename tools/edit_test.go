package tools

import (
	"strings"
	"testing"
)

// ============================================================================
// Helper: create EditTool instance
// ============================================================================

func newEditTool() *EditTool {
	return &EditTool{}
}

// ============================================================================
// A. doLineEdit 边界测试
// ============================================================================

// ============================================================================
// B. doReplace 边界测试
// ============================================================================

func TestDoReplace_NotFound(t *testing.T) {
	tool := newEditTool()
	params := EditParams{OldString: "not_found", NewString: "replacement"}
	_, _, err := tool.doReplace("hello world", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error when text not found")
	}
	// Error message changed: now uses "no match found for pattern" (regex mode)
	if !strings.Contains(err.Error(), "no match found") {
		t.Errorf("error should mention 'no match found', got: %v", err)
	}
}

func TestDoReplace_EmptyOldString(t *testing.T) {
	tool := newEditTool()
	params := EditParams{OldString: "", NewString: "something"}
	_, _, err := tool.doReplace("hello world", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error for empty old_string")
	}
	if !strings.Contains(err.Error(), "old_string is required") {
		t.Errorf("error should mention 'old_string is required', got: %v", err)
	}
}

// TestDoReplace_MultipleOccurrences - DELETED: ReplaceAll field removed
// Edit tool now always replaces first match only

func TestDoReplace_SpecialCharacters(t *testing.T) {
	tool := newEditTool()

	tests := []struct {
		name     string
		content  string
		oldStr   string
		newStr   string
		expected string
	}{
		{
			name:     "tab characters",
			content:  "hello\tworld",
			oldStr:   "hello\tworld",
			newStr:   "replaced",
			expected: "replaced",
		},
		{
			name:     "newline in old_string",
			content:  "line1\nline2\nline3",
			oldStr:   "line1\nline2",
			newStr:   "REPLACED",
			expected: "REPLACED\nline3",
		},
		{
			name:     "unicode characters",
			content:  "你好世界 hello",
			oldStr:   "你好世界",
			newStr:   "Hello World",
			expected: "Hello World hello",
		},
		{
			name:     "emoji",
			content:  "Hello 🌍 World",
			oldStr:   "🌍",
			newStr:   "Earth",
			expected: "Hello Earth World",
		},
		{
			name:     "backslash (regex escaped)",
			content:  `path\to\file`,
			oldStr:   `path\\to\\file`, // escape backslash for regex
			newStr:   "replaced",
			expected: "replaced",
		},
		{
			name:     "null-like content",
			content:  "before\x00after",
			oldStr:   "before\x00after",
			newStr:   "clean",
			expected: "clean",
		},
		{
			name:     "replace with empty string",
			content:  "hello world",
			oldStr:   "world",
			newStr:   "",
			expected: "hello ",
		},
		{
			name:     "very long content",
			content:  strings.Repeat("a", 10000) + "TARGET" + strings.Repeat("b", 10000),
			oldStr:   "TARGET",
			newStr:   "FOUND",
			expected: strings.Repeat("a", 10000) + "FOUND" + strings.Repeat("b", 10000),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := EditParams{OldString: tt.oldStr, NewString: tt.newStr}
			result, _, err := tool.doReplace(tt.content, params, "/test/file.txt")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if result != tt.expected {
				t.Errorf("got %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestDoReplace_OldStringEqualsNewString(t *testing.T) {
	tool := newEditTool()
	const content = "hello world"
	params := EditParams{OldString: "hello", NewString: "hello"}
	result, summary, err := tool.doReplace(content, params, "/test/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Errorf("content should be unchanged, got %q", result)
	}
	// Should still report success (summary format changed in simplified version)
	if !strings.Contains(summary, "replaced") {
		t.Errorf("summary should mention 'replaced', got: %s", summary)
	}
}

func TestDoReplace_ExactMatchOnly(t *testing.T) {
	tool := newEditTool()

	t.Run("substring should not partially match", func(t *testing.T) {
		content := "foobar"
		params := EditParams{OldString: "foo", NewString: "FOO"}
		result, _, err := tool.doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "FOObar" {
			t.Errorf("got %q, want %q", result, "FOObar")
		}
	})

	t.Run("case sensitive", func(t *testing.T) {
		content := "Hello hello HELLO"
		params := EditParams{OldString: "hello", NewString: "HI"}
		result, _, err := tool.doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Hello HI HELLO" {
			t.Errorf("got %q, want %q", result, "Hello HI HELLO")
		}
	})
}

// ============================================================================
// C. doRegexReplace 边界测试 - DELETED
// All tests in this section used Regex and/or ReplaceAll fields which are removed.
// Edit tool now always uses regex matching (built-in, no parameter needed).
// ============================================================================

// ============================================================================
// D. doPositionInsert / doLineEdit(position) 边界测试 - DELETED
// These tests used doLineEdit function and Action/Position fields which are removed.
// ============================================================================

// ============================================================================
// E. Bug 记录测试 - DELETED
// These tests used doLineEdit function which is removed.
// ============================================================================

// ============================================================================
// F. Truncate 辅助函数测试
// ============================================================================

func TestTruncate(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"shorter than max", "hello", 10, "hello"},
		{"exact length", "hello", 5, "hello"},
		{"needs truncation", "hello world", 8, "hello..."},
		{"empty string", "", 10, ""},
		{"unicode characters", "你好世界", 3, "..."},    // 4 runes, maxLen=3, need to truncate to 0+"..."
		{"unicode fits exactly", "你好世界", 4, "你好世界"}, // 4 runes, maxLen=4, fits exactly
		{"unicode fits within", "你好世界", 5, "你好世界"},  // 4 runes, maxLen=5, fits (4<=5)
		{"single rune", "x", 1, "x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Truncate(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("Truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// ============================================================================
// G. validateParams 参数校验测试 - DELETED
// Most tests used LineNumber, Action, Count, Regex, Position fields which are removed.
// Only create and replace modes remain, with simplified validation.
// ============================================================================

// ============================================================================
// H. count 批量操作测试 - DELETED
// Count field removed.
// ============================================================================

// ============================================================================
// I. Backward compatibility tests - DELETED
// Regex and insert modes removed, no backward compat needed.
// ============================================================================
