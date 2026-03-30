package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ============================================================================
// Helper
// ============================================================================

// ============================================================================
// FileReplaceTool — doReplace tests
// ============================================================================

func TestDoReplace_NotFound(t *testing.T) {
	params := FileReplaceParams{OldString: "not_found", NewString: "replacement"}
	_, _, err := doReplace("hello world", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error when text not found")
	}
	if !strings.Contains(err.Error(), "text not found") {
		t.Errorf("error should mention 'text not found', got: %v", err)
	}
}

func TestDoReplace_EmptyOldString(t *testing.T) {
	params := FileReplaceParams{OldString: "", NewString: "something"}
	_, _, err := doReplace("hello world", params, "/test/file.txt")
	if err == nil {
		t.Fatal("expected error for empty old_string")
	}
	if !strings.Contains(err.Error(), "old_string is required") {
		t.Errorf("error should mention 'old_string is required', got: %v", err)
	}
}

func TestDoReplace_SpecialCharacters(t *testing.T) {
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
			name:     "backslash (literal)",
			content:  `path\to\file`,
			oldStr:   `path\to\file`,
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
			params := FileReplaceParams{OldString: tt.oldStr, NewString: tt.newStr}
			result, _, err := doReplace(tt.content, params, "/test/file.txt")
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
	const content = "hello world"
	params := FileReplaceParams{OldString: "hello", NewString: "hello"}
	result, summary, err := doReplace(content, params, "/test/file.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != content {
		t.Errorf("content should be unchanged, got %q", result)
	}
	if !strings.Contains(summary, "replaced") {
		t.Errorf("summary should mention 'replaced', got: %s", summary)
	}
}

func TestDoReplace_ExactMatchOnly(t *testing.T) {
	t.Run("substring should partially match", func(t *testing.T) {
		content := "foobar"
		params := FileReplaceParams{OldString: "foo", NewString: "FOO"}
		result, _, err := doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "FOObar" {
			t.Errorf("got %q, want %q", result, "FOObar")
		}
	})

	t.Run("case sensitive", func(t *testing.T) {
		content := "Hello hello HELLO"
		params := FileReplaceParams{OldString: "hello", NewString: "HI"}
		result, _, err := doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "Hello HI HELLO" {
			t.Errorf("got %q, want %q", result, "Hello HI HELLO")
		}
	})
}

func TestDoReplace_ReplaceAll(t *testing.T) {
	content := "foo bar foo baz foo"
	t.Run("single match (default)", func(t *testing.T) {
		params := FileReplaceParams{OldString: "foo", NewString: "FOO", ReplaceAll: false}
		result, summary, err := doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "FOO bar foo baz foo" {
			t.Errorf("got %q, want %q", result, "FOO bar foo baz foo")
		}
		if !strings.Contains(summary, "1 of 3") {
			t.Errorf("summary should mention partial replacement, got: %s", summary)
		}
	})

	t.Run("replace all", func(t *testing.T) {
		params := FileReplaceParams{OldString: "foo", NewString: "FOO", ReplaceAll: true}
		result, summary, err := doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "FOO bar FOO baz FOO" {
			t.Errorf("got %q, want %q", result, "FOO bar FOO baz FOO")
		}
		if !strings.Contains(summary, "3 occurrence") {
			t.Errorf("summary should mention 3 occurrences, got: %s", summary)
		}
	})
}

func TestDoReplace_Regex(t *testing.T) {
	content := "version 1.2.3 and version 4.5.6"

	t.Run("regex match", func(t *testing.T) {
		params := FileReplaceParams{OldString: `version \d+\.\d+\.\d+`, NewString: "VERSION_X", Regex: true}
		result, _, err := doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "VERSION_X and version 4.5.6" {
			t.Errorf("got %q, want %q", result, "VERSION_X and version 4.5.6")
		}
	})

	t.Run("regex with captures", func(t *testing.T) {
		params := FileReplaceParams{OldString: `(\d+)\.(\d+)\.(\d+)`, NewString: "$1.$2.$3-patched", Regex: true, ReplaceAll: true}
		result, _, err := doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result != "version 1.2.3-patched and version 4.5.6-patched" {
			t.Errorf("got %q, want %q", result, "version 1.2.3-patched and version 4.5.6-patched")
		}
	})

	t.Run("regex special chars without flag are literal", func(t *testing.T) {
		// Without regex=true, "v1.2" matches literally as substring of "v1.2.3"
		params := FileReplaceParams{OldString: "v1.2", NewString: "V12", Regex: false}
		result, _, err := doReplace("v1.2.3", params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Exact match: "v1.2" is found as substring
		if result != "V12.3" {
			t.Errorf("got %q, want %q", result, "V12.3")
		}
	})
}

func TestDoReplace_LineRange(t *testing.T) {
	content := "line1\nline2\nfoo\nline4\nfoo\nline6"

	t.Run("replace within range", func(t *testing.T) {
		params := FileReplaceParams{OldString: "foo", NewString: "BAR", StartLine: 3, EndLine: 3}
		result, _, err := doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line1\nline2\nBAR\nline4\nfoo\nline6"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("replace all within range", func(t *testing.T) {
		params := FileReplaceParams{OldString: "foo", NewString: "BAR", StartLine: 3, EndLine: 5, ReplaceAll: true}
		result, _, err := doReplace(content, params, "/test/file.txt")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := "line1\nline2\nBAR\nline4\nBAR\nline6"
		if result != expected {
			t.Errorf("got %q, want %q", result, expected)
		}
	})

	t.Run("not found in range", func(t *testing.T) {
		params := FileReplaceParams{OldString: "foo", NewString: "BAR", StartLine: 1, EndLine: 2}
		_, _, err := doReplace(content, params, "/test/file.txt")
		if err == nil {
			t.Fatal("expected error when not found in range")
		}
		if !strings.Contains(err.Error(), "lines 1-2") {
			t.Errorf("error should mention line range, got: %v", err)
		}
	})
}

func TestSuggestMatch(t *testing.T) {
	content := "func main() {\n\tfmt.Println(\"hello\")\n}\n"
	t.Run("finds similar line with case-insensitive check", func(t *testing.T) {
		// suggestMatch does case-sensitive substring check on trimmed lines
		hint := suggestMatch(content, "func main()")
		if !strings.Contains(hint, "line 1") {
			t.Errorf("hint should point to line 1, got: %s", hint)
		}
	})

	t.Run("finds similar line with partial content", func(t *testing.T) {
		hint := suggestMatch(content, "Println(\"hello\")")
		if !strings.Contains(hint, "line 2") {
			t.Errorf("hint should point to line 2, got: %s", hint)
		}
	})

	t.Run("no hint for empty search", func(t *testing.T) {
		hint := suggestMatch(content, "")
		if hint != "" {
			t.Errorf("expected empty hint, got: %s", hint)
		}
	})

	t.Run("no hint when not similar enough", func(t *testing.T) {
		hint := suggestMatch(content, "totally_unrelated_text_xyz")
		if hint != "" {
			t.Errorf("expected empty hint for unrelated text, got: %s", hint)
		}
	})
}

// ============================================================================
// FileCreateTool — local mode test
// ============================================================================

func TestFileCreateTool_LocalMode(t *testing.T) {
	ws, err := os.MkdirTemp("", "test-create-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ws)

	ctx := &ToolContext{
		Ctx:            t.Context(),
		WorkspaceRoot:  ws,
		Sandbox:        &mockSandbox{name: "none", workspace: ""},
		SandboxEnabled: false,
	}

	tool := &FileCreateTool{}
	result, err := tool.Execute(ctx, `{"path": "hello.txt", "content": "Hello World"}`)
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	content, _ := os.ReadFile(filepath.Join(ws, "hello.txt"))
	if string(content) != "Hello World" {
		t.Errorf("got %q, want %q", string(content), "Hello World")
	}
	_ = result

	t.Run("nested path creates directories", func(t *testing.T) {
		_, err := tool.Execute(ctx, `{"path": "sub/dir/file.txt", "content": "nested"}`)
		if err != nil {
			t.Fatalf("nested create failed: %v", err)
		}
		content, _ := os.ReadFile(filepath.Join(ws, "sub/dir/file.txt"))
		if string(content) != "nested" {
			t.Errorf("got %q, want %q", string(content), "nested")
		}
	})

	t.Run("existing file returns error", func(t *testing.T) {
		_, err := tool.Execute(ctx, `{"path": "hello.txt", "content": "duplicate"}`)
		if err == nil {
			t.Fatal("expected error for existing file")
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("error should mention 'already exists', got: %v", err)
		}
	})
}

// ============================================================================
// Truncate 辅助函数测试
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
		{"unicode characters", "你好世界", 3, "..."},
		{"unicode fits exactly", "你好世界", 4, "你好世界"},
		{"unicode fits within", "你好世界", 5, "你好世界"},
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
