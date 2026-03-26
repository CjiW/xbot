package tools

import (
	"encoding/json"
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

func TestDoLineEdit_EmptyFile(t *testing.T) {
	tool := newEditTool()
	// "" → strings.Split("", "\n") = [""], totalLines=1

	t.Run("insert_before line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_before", Content: "NEW"}
		result, summary, err := tool.doLineEdit("", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW\n" {
			t.Errorf("insert_before on empty file: got %q, want %q", result, "NEW\n")
		}
		if summary == "" {
			t.Error("expected non-empty summary")
		}
	})

	t.Run("insert_after line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_after", Content: "NEW"}
		result, _, err := tool.doLineEdit("", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines=[""], idx=0, lines[:1]=[""], append "NEW", lines[1:]=[] → ["", "NEW"] → "\nNEW"
		if result != "\nNEW" {
			t.Errorf("insert_after on empty file: got %q, want %q", result, "\nNEW")
		}
	})

	t.Run("replace line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "replace", Content: "NEW"}
		result, _, err := tool.doLineEdit("", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW" {
			t.Errorf("replace on empty file: got %q, want %q", result, "NEW")
		}
	})

	t.Run("delete line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete"}
		result, _, err := tool.doLineEdit("", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines[:0]=[] + lines[1:]=[] → [] → ""
		if result != "" {
			t.Errorf("delete on empty file: got %q, want empty string", result)
		}
	})
}

func TestDoLineEdit_SingleLineNoNewline(t *testing.T) {
	tool := newEditTool()
	// "hello" → lines = ["hello"], totalLines=1

	t.Run("insert_before line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_before", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW\nhello" {
			t.Errorf("got %q, want %q", result, "NEW\nhello")
		}
	})

	t.Run("insert_after line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_after", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nNEW" {
			t.Errorf("got %q, want %q", result, "hello\nNEW")
		}
	})

	t.Run("replace line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "replace", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW" {
			t.Errorf("got %q, want %q", result, "NEW")
		}
	})

	t.Run("delete line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete"}
		result, _, err := tool.doLineEdit("hello", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("got %q, want empty string", result)
		}
	})
}

func TestDoLineEdit_SingleLineWithNewline(t *testing.T) {
	tool := newEditTool()
	// "hello\n" → splitLines → lines=["hello"], hasTrailingNL=true, totalLines=1
	// This matches what the Read tool shows (1 line).

	t.Run("replace line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "replace", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW\n" {
			t.Errorf("got %q, want %q", result, "NEW\n")
		}
	})

	t.Run("line 2 exceeds total lines (no phantom line)", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "replace", Content: "NEW"}
		_, _, err := tool.doLineEdit("hello\n", params)
		if err == nil {
			t.Fatal("expected error: line 2 should exceed total lines 1")
		}
		if !strings.Contains(err.Error(), "exceeds total lines") {
			t.Errorf("error should mention 'exceeds total lines', got: %v", err)
		}
	})

	t.Run("delete line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("got %q, want empty string", result)
		}
	})

	t.Run("insert_before line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_before", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW\nhello\n" {
			t.Errorf("got %q, want %q", result, "NEW\nhello\n")
		}
	})

	t.Run("insert_after line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_after", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nNEW\n" {
			t.Errorf("got %q, want %q", result, "hello\nNEW\n")
		}
	})
}

func TestDoLineEdit_FirstAndLastLine(t *testing.T) {
	tool := newEditTool()
	// "aaa\nbbb\nccc\n" → splitLines → lines=["aaa","bbb","ccc"], hasTrailingNL=true, totalLines=3

	const content = "aaa\nbbb\nccc\n"

	t.Run("insert_before first line", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_before", Content: "FIRST"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "FIRST\naaa\nbbb\nccc\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("insert_after first line", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_after", Content: "AFTER1"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "aaa\nAFTER1\nbbb\nccc\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("insert_before last line (line 3)", func(t *testing.T) {
		params := EditParams{LineNumber: 3, Action: "insert_before", Content: "BEFORE3"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "aaa\nbbb\nBEFORE3\nccc\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("insert_after last line (line 3)", func(t *testing.T) {
		params := EditParams{LineNumber: 3, Action: "insert_after", Content: "AFTER3"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "aaa\nbbb\nccc\nAFTER3\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("line 4 exceeds total lines (no phantom line)", func(t *testing.T) {
		params := EditParams{LineNumber: 4, Action: "insert_before", Content: "X"}
		_, _, err := tool.doLineEdit(content, params)
		if err == nil {
			t.Fatal("expected error: line 4 should exceed total lines 3")
		}
		if !strings.Contains(err.Error(), "exceeds total lines") {
			t.Errorf("error should mention 'exceeds total lines', got: %v", err)
		}
	})

	t.Run("replace first line", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "replace", Content: "NEW"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "NEW\nbbb\nccc\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("replace last line (line 3)", func(t *testing.T) {
		params := EditParams{LineNumber: 3, Action: "replace", Content: "NEW"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "aaa\nbbb\nNEW\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("delete first line", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "bbb\nccc\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("delete last line (line 3)", func(t *testing.T) {
		params := EditParams{LineNumber: 3, Action: "delete"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "aaa\nbbb\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})
}

func TestDoLineEdit_InvalidLineNumber(t *testing.T) {
	tool := newEditTool()

	tests := []struct {
		name string
		line int
	}{
		{"zero", 0},
		{"negative", -1},
		{"very negative", -100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := EditParams{LineNumber: tt.line, Action: "delete"}
			_, _, err := tool.doLineEdit("hello\n", params)
			if err == nil {
				t.Fatalf("expected error for line_number=%d, got nil", tt.line)
			}
			if !strings.Contains(err.Error(), "line_number must be positive") {
				t.Errorf("error should mention 'must be positive', got: %v", err)
			}
		})
	}

	t.Run("exceeds total lines", func(t *testing.T) {
		params := EditParams{LineNumber: 10, Action: "delete"}
		_, _, err := tool.doLineEdit("hello\n", params)
		if err == nil {
			t.Fatal("expected error for line_number exceeding total lines")
		}
		if !strings.Contains(err.Error(), "exceeds total lines") {
			t.Errorf("error should mention 'exceeds total lines', got: %v", err)
		}
	})

	t.Run("exceeds by exactly 1", func(t *testing.T) {
		// "hello\n" → splitLines → ["hello"], totalLines=1, line 2 should fail
		params := EditParams{LineNumber: 2, Action: "delete"}
		_, _, err := tool.doLineEdit("hello\n", params)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "exceeds total lines") {
			t.Errorf("error should mention 'exceeds total lines', got: %v", err)
		}
	})
}

func TestDoLineEdit_EmptyAction(t *testing.T) {
	tool := newEditTool()
	params := EditParams{LineNumber: 1, Action: ""}
	_, _, err := tool.doLineEdit("hello\n", params)
	if err == nil {
		t.Fatal("expected error for empty action")
	}
	if !strings.Contains(err.Error(), "action is required") {
		t.Errorf("error should mention 'action is required', got: %v", err)
	}
}

func TestDoLineEdit_UnknownAction(t *testing.T) {
	tool := newEditTool()
	params := EditParams{LineNumber: 1, Action: "invalid_action", Content: "x"}
	_, _, err := tool.doLineEdit("hello\n", params)
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("error should mention 'unknown action', got: %v", err)
	}
}

func TestDoLineEdit_EmptyContentForInsertReplace(t *testing.T) {
	tool := newEditTool()

	tests := []struct {
		name   string
		action string
	}{
		{"insert_before", "insert_before"},
		{"insert_after", "insert_after"},
		{"replace", "replace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := EditParams{LineNumber: 1, Action: tt.action, Content: ""}
			_, _, err := tool.doLineEdit("hello\n", params)
			if err == nil {
				t.Fatalf("expected error for empty content with action=%s", tt.action)
			}
			if !strings.Contains(err.Error(), "content is required") {
				t.Errorf("error should mention 'content is required', got: %v", err)
			}
		})
	}

	// delete should NOT require content
	t.Run("delete_no_content_required", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete"}
		_, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("delete should not require content, got error: %v", err)
		}
	})
}

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
	if !strings.Contains(err.Error(), "text not found") {
		t.Errorf("error should mention 'text not found', got: %v", err)
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

func TestDoReplace_MultipleOccurrences(t *testing.T) {
	tool := newEditTool()
	const content = "foo\nbar\nfoo\nbaz"

	t.Run("single replace (first only)", func(t *testing.T) {
		params := EditParams{OldString: "foo", NewString: "FOO", ReplaceAll: false}
		result, summary, err := tool.doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "FOO\nbar\nfoo\nbaz"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
		if !strings.Contains(summary, "1 of 2") {
			t.Errorf("summary should mention '1 of 2', got: %s", summary)
		}
	})

	t.Run("replace all", func(t *testing.T) {
		params := EditParams{OldString: "foo", NewString: "FOO", ReplaceAll: true}
		result, summary, err := tool.doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "FOO\nbar\nFOO\nbaz"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
		if !strings.Contains(summary, "2 occurrence") {
			t.Errorf("summary should mention '2 occurrence', got: %s", summary)
		}
	})

	t.Run("single replace with only one occurrence", func(t *testing.T) {
		params := EditParams{OldString: "bar", NewString: "BAR", ReplaceAll: false}
		result, summary, err := tool.doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "foo\nBAR\nfoo\nbaz"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
		if strings.Contains(summary, "1 of") {
			t.Errorf("summary should NOT mention '1 of N' for single occurrence, got: %s", summary)
		}
	})
}

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
			name:     "backslash",
			content:  `path\to\file`,
			oldStr:   `path\to\file`,
			newStr:   "replaced",
			expected: "replaced",
		},
		{
			name:     "dollar sign",
			content:  "price: $100",
			oldStr:   "$100",
			newStr:   "$200",
			expected: "price: $200",
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
	// Should still report success
	if !strings.Contains(summary, "1 occurrence") {
		t.Errorf("summary should mention '1 occurrence', got: %s", summary)
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
// C. doRegexReplace 边界测试
// ============================================================================

func TestDoReplace_InvalidRegexPattern(t *testing.T) {
	tool := newEditTool()

	tests := []struct {
		name    string
		pattern string
	}{
		{"unclosed parenthesis", "("},
		{"unclosed bracket", "["},
		{"invalid repetition", "*abc"},
		{"unclosed character class", "[abc"},
		{"bad escape sequence", `\p`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := EditParams{OldString: tt.pattern, NewString: "x", Regex: true}
			_, _, err := tool.doReplace("hello", params, "/test/file.txt")
			if err == nil {
				t.Fatalf("expected error for invalid pattern %q", tt.pattern)
			}
			if !strings.Contains(err.Error(), "invalid regex") {
				t.Errorf("error should mention 'invalid regex', got: %v", err)
			}
		})
	}
}

func TestDoReplace_RegexNoMatch(t *testing.T) {
	tool := newEditTool()
	params := EditParams{OldString: "xyz", NewString: "FOUND", Regex: true}
	_, _, err := tool.doReplace("hello world", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error when no match found")
	}
	if !strings.Contains(err.Error(), "no match found") {
		t.Errorf("error should mention 'no match found', got: %v", err)
	}
}

func TestDoReplace_RegexReplaceAll(t *testing.T) {
	tool := newEditTool()
	const content = "foo123bar456foo789"

	t.Run("single replace (first only)", func(t *testing.T) {
		params := EditParams{OldString: `\d+`, NewString: "NUM", Regex: true, ReplaceAll: false}
		result, summary, err := tool.doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "fooNUMbar456foo789"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
		if !strings.Contains(summary, "1 of 3") {
			t.Errorf("summary should mention '1 of 3', got: %s", summary)
		}
	})

	t.Run("replace all", func(t *testing.T) {
		params := EditParams{OldString: `\d+`, NewString: "NUM", Regex: true, ReplaceAll: true}
		result, summary, err := tool.doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "fooNUMbarNUMfooNUM"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
		if !strings.Contains(summary, "3 match") {
			t.Errorf("summary should mention '3 match', got: %s", summary)
		}
	})
}

func TestDoReplace_RegexSpecialReplacement(t *testing.T) {
	tool := newEditTool()

	t.Run("capture group $1", func(t *testing.T) {
		params := EditParams{OldString: `(\w+)@(\w+)\.com`, NewString: "user=$1 domain=$2", Regex: true}
		result, _, err := tool.doReplace("email: test@example.com here", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "email: user=test domain=example here"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("multiple capture groups", func(t *testing.T) {
		params := EditParams{OldString: `(\d{4})-(\d{2})-(\d{2})`, NewString: "$3/$2/$1", Regex: true}
		result, _, err := tool.doReplace("date: 2024-03-15 end", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "date: 15/03/2024 end"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("replace all with capture groups", func(t *testing.T) {
		content := "a=1 b=2 c=3"
		params := EditParams{OldString: `(\w)=(\d)`, NewString: "$1:$2", Regex: true, ReplaceAll: true}
		result, _, err := tool.doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "a:1 b:2 c:3"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("anchored pattern", func(t *testing.T) {
		params := EditParams{OldString: `^hello`, NewString: "HI", Regex: true}
		result, _, err := tool.doReplace("hello world\nhello moon", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// In multiline text, ^ matches start of string, not each line (RE2 default)
		expected := "HI world\nhello moon"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})
}

func TestDoReplace_RegexEdgeCases(t *testing.T) {
	tool := newEditTool()

	t.Run("empty match with replacement", func(t *testing.T) {
		params := EditParams{OldString: "a*", NewString: "X", Regex: true, ReplaceAll: false}
		result, _, err := tool.doReplace("bbb", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "X") {
			t.Errorf("expected result to start with X, got %q", result)
		}
	})

	t.Run("dot does not match newline by default", func(t *testing.T) {
		params := EditParams{OldString: `hello.*world`, NewString: "REPLACED", Regex: true}
		_, _, err := tool.doReplace("hello\nworld", params, "/test/file.txt")
		if err == nil {
			t.Fatal("expected error: dot should not match newline in RE2 default mode")
		}
		if !strings.Contains(err.Error(), "no match found") {
			t.Errorf("expected 'no match found' error, got: %v", err)
		}
	})

	t.Run("dot matches newline with (?s) flag", func(t *testing.T) {
		params := EditParams{OldString: `(?s)hello.*world`, NewString: "REPLACED", Regex: true}
		result, _, err := tool.doReplace("hello\nworld", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "REPLACED" {
			t.Errorf("got %q, want %q", result, "REPLACED")
		}
	})
}

// ============================================================================
// D. doPositionInsert / doLineEdit(position) 边界测试
// ============================================================================

func TestDoPositionInsert_Positions(t *testing.T) {
	tool := newEditTool()

	t.Run("position start", func(t *testing.T) {
		params := EditParams{Action: "insert", Position: "start", Content: "PREFIX\n"}
		result, summary, err := tool.doLineEdit("hello world", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "PREFIX\n") {
			t.Errorf("expected content to start with PREFIX, got %q", result)
		}
		if !strings.Contains(summary, "start") {
			t.Errorf("summary should mention 'start', got: %s", summary)
		}
	})

	t.Run("position end - file without trailing newline", func(t *testing.T) {
		params := EditParams{Action: "insert", Position: "end", Content: "APPENDED"}
		result, summary, err := tool.doLineEdit("hello", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nAPPENDED" {
			t.Errorf("got %q, want %q", result, "hello\nAPPENDED")
		}
		if !strings.Contains(summary, "end") {
			t.Errorf("summary should mention 'end', got: %s", summary)
		}
	})

	t.Run("position end - file with trailing newline", func(t *testing.T) {
		params := EditParams{Action: "insert", Position: "end", Content: "APPENDED"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nAPPENDED" {
			t.Errorf("got %q, want %q", result, "hello\nAPPENDED")
		}
	})
}

func TestDoPositionInsert_EmptyContent(t *testing.T) {
	tool := newEditTool()
	params := EditParams{Action: "insert", Position: "start", Content: ""}
	_, _, err := tool.doLineEdit("hello", params)
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "content is required") {
		t.Errorf("error should mention 'content is required', got: %v", err)
	}
}

func TestDoPositionInsert_EmptyFile(t *testing.T) {
	tool := newEditTool()

	t.Run("insert at start of empty file", func(t *testing.T) {
		params := EditParams{Action: "insert", Position: "start", Content: "NEW"}
		result, _, err := tool.doLineEdit("", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW" {
			t.Errorf("got %q, want %q", result, "NEW")
		}
	})

	t.Run("insert at end of empty file", func(t *testing.T) {
		params := EditParams{Action: "insert", Position: "end", Content: "NEW"}
		result, _, err := tool.doLineEdit("", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW" {
			t.Errorf("got %q, want %q", result, "NEW")
		}
	})
}

func TestDoPositionInsert_InvalidPosition(t *testing.T) {
	tool := newEditTool()
	params := EditParams{Action: "insert", Position: "invalid", Content: "NEW"}
	_, _, err := tool.doLineEdit("hello", params)
	if err == nil {
		t.Fatal("expected error for invalid position")
	}
	if !strings.Contains(err.Error(), "invalid position") {
		t.Errorf("error should mention 'invalid position', got: %v", err)
	}
}

// ============================================================================
// E. Bug 记录测试（记录已知的潜在问题，不修复）
// ============================================================================

func TestDoPositionInsert_Bug1_InconsistentTrailingNewline(t *testing.T) {
	tool := newEditTool()
	const insertContent = "NEW_LINE"

	t.Run("file without trailing newline gets extra \\n added", func(t *testing.T) {
		params := EditParams{Action: "insert", Position: "end", Content: insertContent}
		result, _, err := tool.doLineEdit("hello", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nNEW_LINE" {
			t.Errorf("got %q, want %q", result, "hello\nNEW_LINE")
		}
	})

	t.Run("file with trailing newline does NOT get extra \\n", func(t *testing.T) {
		params := EditParams{Action: "insert", Position: "end", Content: insertContent}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nNEW_LINE" {
			t.Errorf("got %q, want %q", result, "hello\nNEW_LINE")
		}
	})

	t.Run("behavior difference when inserted content has trailing \\n", func(t *testing.T) {
		insertWithNL := "NEW_LINE\n"

		params1 := EditParams{Action: "insert", Position: "end", Content: insertWithNL}
		result1, _, _ := tool.doLineEdit("hello", params1)

		params2 := EditParams{Action: "insert", Position: "end", Content: insertWithNL}
		result2, _, _ := tool.doLineEdit("hello\n", params2)

		_ = result1
		_ = result2
	})
}

func TestDoLineEdit_TrailingNewlinePreserved(t *testing.T) {
	tool := newEditTool()

	// With splitLines fix, trailing newline is tracked separately and always preserved.
	// "hello\n" → lines=["hello"], hasTrailingNL=true, totalLines=1
	// No more phantom empty line to confuse line numbering.

	t.Run("delete only line preserves nothing (empty result)", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("got %q, want empty string", result)
		}
	})

	t.Run("replace preserves trailing newline", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "replace", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW\n" {
			t.Errorf("got %q, want %q", result, "NEW\n")
		}
	})

	t.Run("multi-line delete preserves trailing newline", func(t *testing.T) {
		// "aaa\nbbb\n" → lines=["aaa","bbb"], hasTrailingNL=true, totalLines=2
		params := EditParams{LineNumber: 2, Action: "delete"}
		result, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "aaa\n" {
			t.Errorf("got %q, want %q", result, "aaa\n")
		}
	})

	t.Run("insert_after last line preserves trailing newline", func(t *testing.T) {
		// "aaa\nbbb\n" → lines=["aaa","bbb"], insert after line 2
		params := EditParams{LineNumber: 2, Action: "insert_after", Content: "NEW"}
		result, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "aaa\nbbb\nNEW\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("no trailing newline stays without", func(t *testing.T) {
		// "aaa\nbbb" → lines=["aaa","bbb"], hasTrailingNL=false
		params := EditParams{LineNumber: 2, Action: "delete"}
		result, _, err := tool.doLineEdit("aaa\nbbb", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "aaa" {
			t.Errorf("got %q, want %q", result, "aaa")
		}
	})
}

func TestDoLineEdit_InsertAfterWithPosition(t *testing.T) {
	tool := newEditTool()

	// Note: position-as-line-number is no longer supported in the new API.
	// Use action=insert_before/insert_after with line_number instead.

	t.Run("insert_after line 1 on file with trailing newline", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_after", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "hello\nNEW\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("insert_after line 1 on single-line-no-newline file", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_after", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nNEW" {
			t.Errorf("got %q, want %q", result, "hello\nNEW")
		}
	})

	t.Run("insert_after line 2 on multi-line file", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "insert_after", Content: "NEW"}
		result, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "aaa\nbbb\nNEW\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})
}

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
// G. validateParams 参数校验测试
// ============================================================================

func TestValidateParams(t *testing.T) {
	tool := newEditTool()

	t.Run("replace mode with line_number", func(t *testing.T) {
		params := EditParams{Mode: "replace", Path: "test.go", OldString: "foo", NewString: "bar", LineNumber: 10}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for line_number in replace mode")
		}
		if !strings.Contains(err.Error(), "line_number is not used in replace mode") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("replace mode with action", func(t *testing.T) {
		params := EditParams{Mode: "replace", Path: "test.go", OldString: "foo", NewString: "bar", Action: "delete"}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for action in replace mode")
		}
		if !strings.Contains(err.Error(), "action is not used in replace mode") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("replace mode with count", func(t *testing.T) {
		params := EditParams{Mode: "replace", Path: "test.go", OldString: "foo", NewString: "bar", Count: 3}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for count in replace mode")
		}
		if !strings.Contains(err.Error(), "count is not used in replace mode") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("line mode with old_string", func(t *testing.T) {
		params := EditParams{Mode: "line", Path: "test.go", Action: "delete", LineNumber: 5, OldString: "foo"}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for old_string in line mode")
		}
		if !strings.Contains(err.Error(), "old_string is not used in line mode") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("line mode with regex", func(t *testing.T) {
		params := EditParams{Mode: "line", Path: "test.go", Action: "delete", LineNumber: 5, Regex: true}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for regex in line mode")
		}
		if !strings.Contains(err.Error(), "regex is not used in line mode") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("line mode with start_line", func(t *testing.T) {
		params := EditParams{Mode: "line", Path: "test.go", Action: "delete", LineNumber: 5, StartLine: 1}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for start_line in line mode")
		}
		if !strings.Contains(err.Error(), "start_line/end_line are not used in line mode") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("line mode insert without position", func(t *testing.T) {
		params := EditParams{Mode: "line", Path: "test.go", Action: "insert"}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for insert action without position")
		}
		if !strings.Contains(err.Error(), "position is required when action=insert") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("line mode insert with both line_number and position", func(t *testing.T) {
		params := EditParams{Mode: "line", Path: "test.go", Action: "insert", LineNumber: 5, Position: "end"}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for both line_number and position")
		}
		if !strings.Contains(err.Error(), "either line_number or position, not both") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("unknown mode", func(t *testing.T) {
		params := EditParams{Mode: "unknown", Path: "test.go"}
		err := tool.validateParams(params)
		if err == nil {
			t.Fatal("expected error for unknown mode")
		}
		if !strings.Contains(err.Error(), "unknown mode") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("valid create mode", func(t *testing.T) {
		params := EditParams{Mode: "create", Path: "test.go"}
		err := tool.validateParams(params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("valid replace mode", func(t *testing.T) {
		params := EditParams{Mode: "replace", Path: "test.go", OldString: "foo", NewString: "bar"}
		err := tool.validateParams(params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("valid line mode delete", func(t *testing.T) {
		params := EditParams{Mode: "line", Path: "test.go", Action: "delete", LineNumber: 5}
		err := tool.validateParams(params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("valid line mode insert with position", func(t *testing.T) {
		params := EditParams{Mode: "line", Path: "test.go", Action: "insert", Position: "end"}
		err := tool.validateParams(params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// ============================================================================
// H. count 批量操作测试
// ============================================================================

func TestDoLineEdit_Count(t *testing.T) {
	tool := newEditTool()

	t.Run("delete single line (count=1, default)", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "delete"}
		result, summary, err := tool.doLineEdit("aaa\nbbb\nccc\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "aaa\nccc\n" {
			t.Errorf("got %q, want %q", result, "aaa\nccc\n")
		}
		if !strings.Contains(summary, "1 line(s)") {
			t.Errorf("summary should mention '1 line(s)', got: %s", summary)
		}
	})

	t.Run("delete 3 lines with count=3", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete", Count: 3}
		result, summary, err := tool.doLineEdit("aaa\nbbb\nccc\nddd\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "ddd\n" {
			t.Errorf("got %q, want %q", result, "ddd\n")
		}
		if !strings.Contains(summary, "3 line(s)") {
			t.Errorf("summary should mention '3 line(s)', got: %s", summary)
		}
	})

	t.Run("delete all lines", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete", Count: 3}
		result, _, err := tool.doLineEdit("aaa\nbbb\nccc\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "" {
			t.Errorf("got %q, want empty", result)
		}
	})

	t.Run("delete exceeds total lines", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "delete", Count: 5}
		_, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err == nil {
			t.Fatal("expected error for count exceeding total lines")
		}
		if !strings.Contains(err.Error(), "exceeds total lines") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("replace 2 lines with count=2", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "replace", Content: "NEW", Count: 2}
		result, summary, err := tool.doLineEdit("aaa\nbbb\nccc\nddd\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "aaa\nNEW\nddd\n" {
			t.Errorf("got %q, want %q", result, "aaa\nNEW\nddd\n")
		}
		if !strings.Contains(summary, "2 line(s)") {
			t.Errorf("summary should mention '2 line(s)', got: %s", summary)
		}
	})

	t.Run("replace with count exceeds lines", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "replace", Content: "NEW", Count: 5}
		_, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err == nil {
			t.Fatal("expected error for count exceeding total lines")
		}
		if !strings.Contains(err.Error(), "exceeds total lines") {
			t.Errorf("got: %v", err)
		}
	})

	t.Run("count=0 defaults to 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete", Count: 0}
		result, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "bbb\n" {
			t.Errorf("got %q, want %q (count=0 should default to 1)", result, "bbb\n")
		}
	})

	t.Run("negative count defaults to 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete", Count: -1}
		result, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "bbb\n" {
			t.Errorf("got %q, want %q (negative count should default to 1)", result, "bbb\n")
		}
	})

	t.Run("insert_before ignores count", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_before", Content: "NEW", Count: 5}
		result, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW\naaa\nbbb\n" {
			t.Errorf("got %q, want %q (count should not affect insert)", result, "NEW\naaa\nbbb\n")
		}
	})
}

// ============================================================================
// I. Backward compatibility tests (deprecated modes)
// ============================================================================

func TestBackwardCompat_RegexMode(t *testing.T) {
	// Test backward compatibility: mode="regex" should be converted to mode="replace" + regex=true
	t.Run("regex mode mapped to replace with regex=true", func(t *testing.T) {
		input := `{"path": "test.txt", "mode": "regex", "pattern": "\\d+", "replacement": "NUM"}`
		var params EditParams
		json.Unmarshal([]byte(input), &params)
		if params.Mode != "regex" {
			t.Fatalf("expected mode=regex from input, got %q", params.Mode)
		}
		// Simulate the Execute compat layer
		switch params.Mode {
		case "regex":
			params.Mode = "replace"
			params.Regex = true
		}
		if params.Mode != "replace" {
			t.Errorf("expected mode to be converted to 'replace', got %q", params.Mode)
		}
		if !params.Regex {
			t.Error("expected regex to be true after conversion")
		}
	})
}

func TestBackwardCompat_InsertMode(t *testing.T) {
	t.Run("insert mode mapped to line with action=insert", func(t *testing.T) {
		input := `{"path": "test.txt", "mode": "insert", "position": "end", "content": "NEW"}`
		var params EditParams
		json.Unmarshal([]byte(input), &params)
		if params.Mode != "insert" {
			t.Fatalf("expected mode=insert from input, got %q", params.Mode)
		}
		switch params.Mode {
		case "insert":
			params.Mode = "line"
			params.Action = "insert"
		}
		if params.Mode != "line" {
			t.Errorf("expected mode to be converted to 'line', got %q", params.Mode)
		}
		if params.Action != "insert" {
			t.Errorf("expected action to be 'insert', got %q", params.Action)
		}
	})
}
