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
	// "hello\n" → lines = ["hello", ""], totalLines=2

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

	t.Run("replace line 2 (empty trailing line)", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "replace", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nNEW" {
			t.Errorf("got %q, want %q", result, "hello\nNEW")
		}
	})

	t.Run("delete line 1", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines[:0]=[] + lines[1:]=[""] → [""] → ""
		if result != "" {
			t.Errorf("got %q, want empty string", result)
		}
	})

	t.Run("delete line 2", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "delete"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// After Bug2 fix: deleting the empty trailing line preserves the trailing \n
		if result != "hello\n" {
			t.Errorf("got %q, want %q", result, "hello\n")
		}
	})

	t.Run("insert_before line 2", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "insert_before", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines[:1]=["hello"] + ["NEW"] + lines[1:]=[""] → ["hello","NEW",""] → "hello\nNEW\n"
		if result != "hello\nNEW\n" {
			t.Errorf("got %q, want %q", result, "hello\nNEW\n")
		}
	})

	t.Run("insert_after line 2", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "insert_after", Content: "NEW"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines[:2]=["hello",""] + ["NEW"] + lines[2:]=[] → ["hello","","NEW"] → "hello\n\nNEW"
		if result != "hello\n\nNEW" {
			t.Errorf("got %q, want %q", result, "hello\n\nNEW")
		}
	})
}

func TestDoLineEdit_FirstAndLastLine(t *testing.T) {
	tool := newEditTool()
	// "aaa\nbbb\nccc\n" → lines=["aaa","bbb","ccc",""], totalLines=4

	const content = "aaa\nbbb\nccc\n"

	t.Run("insert_before first line", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "insert_before", Content: "FIRST"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "FIRST\naaa") {
			t.Errorf("expected content to start with %q, got %q", "FIRST\naaa", result)
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

	t.Run("insert_before last line (line 4, empty trailing)", func(t *testing.T) {
		params := EditParams{LineNumber: 4, Action: "insert_before", Content: "BEFORE4"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines[:3]=["aaa","bbb","ccc"] + ["BEFORE4"] + lines[3:]=[""] → "aaa\nbbb\nccc\nBEFORE4\n"
		expected := "aaa\nbbb\nccc\nBEFORE4\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("insert_after last line (line 4)", func(t *testing.T) {
		params := EditParams{LineNumber: 4, Action: "insert_after", Content: "AFTER4"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines[:4]=["aaa","bbb","ccc",""] + ["AFTER4"] + lines[4:]=[] → "aaa\nbbb\nccc\n\nAFTER4"
		expected := "aaa\nbbb\nccc\n\nAFTER4"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("replace first line", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "replace", Content: "NEW"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(result, "NEW\nbbb") {
			t.Errorf("expected content to start with %q, got %q", "NEW\nbbb", result)
		}
	})

	t.Run("replace last content line (line 3)", func(t *testing.T) {
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
		// After Bug2 fix: original "aaa\nbbb\nccc\n" → delete line 1 → "bbb\nccc\n" (trailing \n preserved)
		expected := "bbb\nccc\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("delete last content line (line 3)", func(t *testing.T) {
		params := EditParams{LineNumber: 3, Action: "delete"}
		result, _, err := tool.doLineEdit(content, params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines[:2]=["aaa","bbb"] + lines[3:]=[""] → ["aaa","bbb",""] → "aaa\nbbb\n"
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
		// "hello\n" has 2 lines (["hello", ""]), line 3 should fail
		params := EditParams{LineNumber: 3, Action: "delete"}
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

func TestDoRegexReplace_InvalidPattern(t *testing.T) {
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
			params := EditParams{Pattern: tt.pattern, Replacement: "x"}
			_, _, err := tool.doRegexReplace("hello", params, "/test/file.txt")
			if err == nil {
				t.Fatalf("expected error for invalid pattern %q", tt.pattern)
			}
			if !strings.Contains(err.Error(), "invalid regex") {
				t.Errorf("error should mention 'invalid regex', got: %v", err)
			}
		})
	}
}

func TestDoRegexReplace_EmptyPattern(t *testing.T) {
	tool := newEditTool()
	params := EditParams{Pattern: "", Replacement: "x"}
	_, _, err := tool.doRegexReplace("hello", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error for empty pattern")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("error should mention 'pattern is required', got: %v", err)
	}
}

func TestDoRegexReplace_NoMatch(t *testing.T) {
	tool := newEditTool()
	params := EditParams{Pattern: "xyz", Replacement: "FOUND"}
	_, _, err := tool.doRegexReplace("hello world", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error when no match found")
	}
	if !strings.Contains(err.Error(), "no match found") {
		t.Errorf("error should mention 'no match found', got: %v", err)
	}
}

func TestDoRegexReplace_ReplaceAll(t *testing.T) {
	tool := newEditTool()
	const content = "foo123bar456foo789"

	t.Run("single replace (first only)", func(t *testing.T) {
		params := EditParams{Pattern: `\d+`, Replacement: "NUM", ReplaceAll: false}
		result, summary, err := tool.doRegexReplace(content, params, "/test/file.txt")
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
		params := EditParams{Pattern: `\d+`, Replacement: "NUM", ReplaceAll: true}
		result, summary, err := tool.doRegexReplace(content, params, "/test/file.txt")
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

func TestDoRegexReplace_SpecialReplacement(t *testing.T) {
	tool := newEditTool()

	t.Run("capture group $1", func(t *testing.T) {
		params := EditParams{Pattern: `(\w+)@(\w+)\.com`, Replacement: "user=$1 domain=$2"}
		result, _, err := tool.doRegexReplace("email: test@example.com here", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "email: user=test domain=example here"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("multiple capture groups", func(t *testing.T) {
		params := EditParams{Pattern: `(\d{4})-(\d{2})-(\d{2})`, Replacement: "$3/$2/$1"}
		result, _, err := tool.doRegexReplace("date: 2024-03-15 end", params, "/test/file.txt")
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
		params := EditParams{Pattern: `(\w)=(\d)`, Replacement: "$1:$2", ReplaceAll: true}
		result, _, err := tool.doRegexReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "a:1 b:2 c:3"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("anchored pattern", func(t *testing.T) {
		params := EditParams{Pattern: `^hello`, Replacement: "HI"}
		result, _, err := tool.doRegexReplace("hello world\nhello moon", params, "/test/file.txt")
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

func TestDoRegexReplace_EdgeCases(t *testing.T) {
	tool := newEditTool()

	t.Run("empty match with replacement", func(t *testing.T) {
		// Pattern that matches empty string at various positions
		params := EditParams{Pattern: "a*", Replacement: "X", ReplaceAll: false}
		result, _, err := tool.doRegexReplace("bbb", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// a* matches empty string at position 0, replaced with X
		if !strings.HasPrefix(result, "X") {
			t.Errorf("expected result to start with X, got %q", result)
		}
	})

	t.Run("dot does not match newline by default", func(t *testing.T) {
		params := EditParams{Pattern: `hello.*world`, Replacement: "REPLACED"}
		_, _, err := tool.doRegexReplace("hello\nworld", params, "/test/file.txt")
		// . does not match \n by default in RE2, so no match → error
		if err == nil {
			t.Fatal("expected error: dot should not match newline in RE2 default mode")
		}
		if !strings.Contains(err.Error(), "no match found") {
			t.Errorf("expected 'no match found' error, got: %v", err)
		}
	})

	t.Run("dot matches newline with (?s) flag", func(t *testing.T) {
		params := EditParams{Pattern: `(?s)hello.*world`, Replacement: "REPLACED"}
		result, _, err := tool.doRegexReplace("hello\nworld", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "REPLACED" {
			t.Errorf("got %q, want %q", result, "REPLACED")
		}
	})
}

// ============================================================================
// D. doInsert 边界测试
// ============================================================================

func TestDoInsert_Positions(t *testing.T) {
	tool := newEditTool()
	const filePath = "/test/file.txt"

	t.Run("position start", func(t *testing.T) {
		params := EditParams{Position: "start", Content: "PREFIX\n"}
		result, summary, err := tool.doInsert("hello world", params, filePath)
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
		params := EditParams{Position: "end", Content: "APPENDED"}
		result, summary, err := tool.doInsert("hello", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// doInsert adds \n if file doesn't end with \n
		if result != "hello\nAPPENDED" {
			t.Errorf("got %q, want %q", result, "hello\nAPPENDED")
		}
		if !strings.Contains(summary, "end") {
			t.Errorf("summary should mention 'end', got: %s", summary)
		}
	})

	t.Run("position end - file with trailing newline", func(t *testing.T) {
		params := EditParams{Position: "end", Content: "APPENDED"}
		result, _, err := tool.doInsert("hello\n", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// File already ends with \n, no extra \n added
		if result != "hello\nAPPENDED" {
			t.Errorf("got %q, want %q", result, "hello\nAPPENDED")
		}
	})

	t.Run("position as line number", func(t *testing.T) {
		params := EditParams{Position: "1", Content: "INSERTED"}
		result, _, err := tool.doInsert("aaa\nbbb\n", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Position "1" → lineNum=1, action=insert_after line 1
		// lines=["aaa","bbb",""], idx=0, lines[:1]=["aaa"], append "INSERTED", lines[1:]=["bbb",""]
		// → ["aaa","INSERTED","bbb",""] → "aaa\nINSERTED\nbbb\n"
		expected := "aaa\nINSERTED\nbbb\n"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("position 0 parses to line 0 (error in doLineEdit)", func(t *testing.T) {
		params := EditParams{Position: "0", Content: "INSERTED"}
		_, _, err := tool.doInsert("hello\n", params, filePath)
		if err == nil {
			t.Fatal("expected error for position 0")
		}
		if !strings.Contains(err.Error(), "line_number must be positive") {
			t.Errorf("error should mention 'must be positive', got: %v", err)
		}
	})
}

func TestDoInsert_EmptyContent(t *testing.T) {
	tool := newEditTool()
	params := EditParams{Position: "start", Content: ""}
	_, _, err := tool.doInsert("hello", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "content is required") {
		t.Errorf("error should mention 'content is required', got: %v", err)
	}
}

func TestDoInsert_EmptyFile(t *testing.T) {
	tool := newEditTool()
	const filePath = "/test/file.txt"

	t.Run("insert at start of empty file", func(t *testing.T) {
		params := EditParams{Position: "start", Content: "NEW"}
		result, _, err := tool.doInsert("", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "NEW" {
			t.Errorf("got %q, want %q", result, "NEW")
		}
	})

	t.Run("insert at end of empty file", func(t *testing.T) {
		params := EditParams{Position: "end", Content: "NEW"}
		result, _, err := tool.doInsert("", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// len("") == 0, so the trailing newline check is skipped, result = "" + "NEW" = "NEW"
		if result != "NEW" {
			t.Errorf("got %q, want %q", result, "NEW")
		}
	})
}

func TestDoInsert_InvalidPosition(t *testing.T) {
	tool := newEditTool()
	params := EditParams{Position: "invalid", Content: "NEW"}
	_, _, err := tool.doInsert("hello", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error for invalid position")
	}
	if !strings.Contains(err.Error(), "invalid position") {
		t.Errorf("error should mention 'invalid position', got: %v", err)
	}
}

func TestDoInsert_PositionExceedsLines(t *testing.T) {
	tool := newEditTool()
	// "hello\n" has 2 lines. Position "3" → lineNum=3, doLineEdit will reject
	params := EditParams{Position: "3", Content: "NEW"}
	_, _, err := tool.doInsert("hello\n", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error for position exceeding total lines")
	}
	if !strings.Contains(err.Error(), "exceeds total lines") {
		t.Errorf("error should mention 'exceeds total lines', got: %v", err)
	}
}

// ============================================================================
// E. Bug 记录测试（记录已知的潜在问题，不修复）
// ============================================================================

func TestDoInsert_Bug1_InconsistentTrailingNewline(t *testing.T) {
	tool := newEditTool()
	const filePath = "/test/file.txt"
	const insertContent = "NEW_LINE"

	// Bug 1: doInsert position="end" 时，如果文件不以 \n 结尾会自动添加 \n。
	// 但如果文件以 \n 结尾，则不会添加。这导致插入结果中的行数不一致。

	t.Run("file without trailing newline gets extra \\n added", func(t *testing.T) {
		params := EditParams{Position: "end", Content: insertContent}
		result, _, err := tool.doInsert("hello", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// "hello" doesn't end with \n, so \n is added: "hello\n" + "NEW_LINE" = "hello\nNEW_LINE"
		if result != "hello\nNEW_LINE" {
			t.Errorf("got %q, want %q", result, "hello\nNEW_LINE")
		}
	})

	t.Run("file with trailing newline does NOT get extra \\n", func(t *testing.T) {
		params := EditParams{Position: "end", Content: insertContent}
		result, _, err := tool.doInsert("hello\n", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// "hello\n" ends with \n, so no extra \n: "hello\n" + "NEW_LINE" = "hello\nNEW_LINE"
		if result != "hello\nNEW_LINE" {
			t.Errorf("got %q, want %q", result, "hello\nNEW_LINE")
		}
	})

	// Both produce "hello\nNEW_LINE" which looks the same here, BUT if we count
	// newlines or look at behavior where inserted content contains its own trailing \n:
	t.Run("behavior difference when inserted content has trailing \\n", func(t *testing.T) {
		insertWithNL := "NEW_LINE\n"

		params1 := EditParams{Position: "end", Content: insertWithNL}
		result1, _, _ := tool.doInsert("hello", params1, filePath)

		params2 := EditParams{Position: "end", Content: insertWithNL}
		result2, _, _ := tool.doInsert("hello\n", params2, filePath)

		// "hello" → "hello\n" + "NEW_LINE\n" = "hello\nNEW_LINE\n"
		// "hello\n" → "hello\n" + "NEW_LINE\n" = "hello\nNEW_LINE\n"
		// In this case they're the same. The inconsistency is more subtle:
		// For "hello" + "NEW" (no trailing \n in content):
		//   "hello" → "hello\nNEW" (extra \n added by doInsert)
		//   "hello\n" → "hello\nNEW" (no extra \n)
		// The inconsistency is that doInsert decides whether to add \n based on the file's
		// current trailing newline status, which may not be what the user expects.
		t.Logf("Without trailing newline: %q", result1)
		t.Logf("With trailing newline:    %q", result2)

		// Document the actual behavior
		_ = result1 // avoid unused
		_ = result2 // avoid unused
	})
}

func TestDoLineEdit_Bug2_DeleteLastEmptyLine(t *testing.T) {
	tool := newEditTool()

	// Bug 2: "hello\n" has 2 lines: ["hello", ""]. Deleting line 2 removes the empty string.
	// Result: ["hello"] joined = "hello" (trailing \n is lost).
	// This is because strings.Split("hello\n", "\n") produces ["hello", ""].

	t.Run("delete trailing empty line preserves newline", func(t *testing.T) {
		params := EditParams{LineNumber: 2, Action: "delete"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// After Bug2 fix: trailing \n is preserved
		if result != "hello\n" {
			t.Errorf("got %q, want %q", result, "hello\n")
		}
	})

	t.Run("delete last non-empty line preserves structure", func(t *testing.T) {
		params := EditParams{LineNumber: 1, Action: "delete"}
		result, _, err := tool.doLineEdit("hello\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines[:0]=[] + lines[1:]=[""] → [""] → ""
		// Bug2 fix: len(result)==0, so no trailing \n added (correct - empty file)
		if result != "" {
			t.Errorf("got %q, want empty string", result)
		}
	})

	t.Run("multi-line file preserves trailing newline when last line deleted", func(t *testing.T) {
		// "aaa\nbbb\n" → lines=["aaa","bbb",""], totalLines=3
		// Delete line 3 (the empty string after trailing \n)
		params := EditParams{LineNumber: 3, Action: "delete"}
		result, _, err := tool.doLineEdit("aaa\nbbb\n", params)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// After Bug2 fix: trailing \n is preserved
		if result != "aaa\nbbb\n" {
			t.Errorf("got %q, want %q", result, "aaa\nbbb\n")
		}
	})
}

func TestDoInsert_Bug3_InsertAfterLastLine(t *testing.T) {
	tool := newEditTool()
	const filePath = "/test/file.txt"

	// Bug 3: doInsert with numeric position sets action="insert_after" and delegates to doLineEdit.
	// For "hello\n" (totalLines=2), position="2" sets line=2, action=insert_after.
	// idx=1, lines[:2]=["hello",""], append "NEW", lines[2:]=[] → ["hello","","NEW"] → "hello\n\nNEW"
	// This is arguably correct (inserting after the last line which is empty).
	// But position="3" will fail with "exceeds total lines 2".

	t.Run("position=2 on 2-line file works", func(t *testing.T) {
		params := EditParams{Position: "2", Content: "NEW"}
		result, _, err := tool.doInsert("hello\n", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// lines=["hello",""], insert_after line 2: lines[:2]=["hello",""] + ["NEW"] + [] → "hello\n\nNEW"
		expected := "hello\n\nNEW"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
		t.Logf("Note: inserting after line 2 of 'hello\\n' produces double newline: %q", result)
	})

	t.Run("position=3 on 2-line file fails", func(t *testing.T) {
		params := EditParams{Position: "3", Content: "NEW"}
		_, _, err := tool.doInsert("hello\n", params, filePath)
		if err == nil {
			t.Fatal("expected error for position=3 on 2-line file")
		}
		if !strings.Contains(err.Error(), "exceeds total lines") {
			t.Errorf("error should mention 'exceeds total lines', got: %v", err)
		}
		t.Logf("BUG: User cannot use position='3' to append after the last content line of a 2-line file (includes trailing empty line from \\n)")
	})

	t.Run("position=1 on single-line-no-newline file", func(t *testing.T) {
		// "hello" → lines=["hello"], totalLines=1
		// insert_after line 1: lines[:1]=["hello"] + ["NEW"] + [] → "hello\nNEW"
		params := EditParams{Position: "1", Content: "NEW"}
		result, _, err := tool.doInsert("hello", params, filePath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "hello\nNEW" {
			t.Errorf("got %q, want %q", result, "hello\nNEW")
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
