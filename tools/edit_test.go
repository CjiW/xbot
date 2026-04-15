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
		// Verify NO old_string remains after replace_all
		if strings.Contains(result, "foo") {
			t.Errorf("replace_all=true but old_string still present in result: %q", result)
		}
	})
}

// TestDoReplace_ReplaceAll_NoResidual verifies that replace_all truly removes all occurrences,
// including when old_string appears in multiple non-overlapping contexts.
func TestDoReplace_ReplaceAll_NoResidual(t *testing.T) {
	content := "a.role.Model\n\trole.Model,\nrole.Model) // end"
	params := FileReplaceParams{OldString: "role.Model", NewString: "effectiveModel", ReplaceAll: true}
	result, summary, err := doReplace(content, params, "/test/file.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result, "role.Model") {
		t.Errorf("replace_all=true but 'role.Model' still present in result:\n%s", result)
	}
	if !strings.Contains(summary, "3 occurrence") {
		t.Errorf("expected 3 occurrences replaced, got summary: %s", summary)
	}
	expected := "a.effectiveModel\n\teffectiveModel,\neffectiveModel) // end"
	if result != expected {
		t.Errorf("got %q, want %q", result, expected)
	}
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
// Fuzzy whitespace matching tests
// ============================================================================

func TestLeadingWhitespace(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", ""},
		{"\thello", "\t"},
		{"\t\thello", "\t\t"},
		{"    hello", "    "},
		{"\t  hello", "\t  "},
		{"", ""},
		{" ", " "},
	}
	for _, tt := range tests {
		got := leadingWhitespace(tt.input)
		if got != tt.want {
			t.Errorf("leadingWhitespace(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAdjustIndentation(t *testing.T) {
	t.Run("tab level shift (2 tabs -> 1 tab)", func(t *testing.T) {
		oldLines := []string{"\t\tcase \"remote\":", "\t\t\tremoteDir := \"\""}
		actualLines := []string{"\tcase \"remote\":", "\t\tremoteDir := \"\""}
		newStr := "\t\tcase \"remote\":\n\t\t\tremoteDir := \"changed\""
		got := adjustIndentation(oldLines, actualLines, newStr)
		want := "\tcase \"remote\":\n\t\tremoteDir := \"changed\""
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("tab to spaces", func(t *testing.T) {
		oldLines := []string{"\tfunc foo() {", "\t\tx := 1"}
		actualLines := []string{"    func foo() {", "        x := 1"}
		newStr := "\tfunc foo() {\n\t\tx := 2\n\t}"
		got := adjustIndentation(oldLines, actualLines, newStr)
		want := "    func foo() {\n        x := 2\n    }"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("spaces to tab", func(t *testing.T) {
		oldLines := []string{"    return x", "    }"}
		actualLines := []string{"\treturn x", "\t}"}
		newStr := "    return y\n    }"
		got := adjustIndentation(oldLines, actualLines, newStr)
		want := "\treturn y\n\t}"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("no change needed", func(t *testing.T) {
		oldLines := []string{"\tx := 1"}
		actualLines := []string{"\tx := 1"}
		newStr := "\tx := 2"
		got := adjustIndentation(oldLines, actualLines, newStr)
		if got != newStr {
			t.Errorf("got %q, want %q (unchanged)", got, newStr)
		}
	})

	t.Run("new lines added by LLM also adjusted", func(t *testing.T) {
		oldLines := []string{"\t\tx := 1"}
		actualLines := []string{"\tx := 1"}
		newStr := "\t\tx := 1\n\t\ty := 2\n\t\tz := 3"
		got := adjustIndentation(oldLines, actualLines, newStr)
		want := "\tx := 1\n\ty := 2\n\tz := 3"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("empty lines preserved", func(t *testing.T) {
		oldLines := []string{"\t\ta := 1", "", "\t\tb := 2"}
		actualLines := []string{"\ta := 1", "", "\tb := 2"}
		newStr := "\t\ta := 1\n\n\t\tb := 2"
		got := adjustIndentation(oldLines, actualLines, newStr)
		want := "\ta := 1\n\n\tb := 2"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestFuzzyWhitespaceMatch_TabLevelMismatch(t *testing.T) {
	// Simulates the real bug: LLM uses 2 tabs but file has 1 tab
	content := "\tswitch x {\n\tcase \"remote\":\n\t\tremoteDir := \"\"\n\t\tfmt.Println(remoteDir)\n\tdefault:\n\t\tfmt.Println(\"other\")\n\t}"
	oldStr := "\t\tcase \"remote\":\n\t\t\tremoteDir := \"\"\n\t\t\tfmt.Println(remoteDir)"
	newStr := "\t\tcase \"remote\":\n\t\t\tremoteDir := \"changed\"\n\t\t\tfmt.Println(remoteDir)"

	actualOld, adjustedNew, ok := fuzzyWhitespaceMatch(content, oldStr, newStr)
	if !ok {
		t.Fatal("expected fuzzy match to succeed")
	}
	wantActual := "\tcase \"remote\":\n\t\tremoteDir := \"\"\n\t\tfmt.Println(remoteDir)"
	if actualOld != wantActual {
		t.Errorf("actualOld:\ngot  %q\nwant %q", actualOld, wantActual)
	}
	wantNew := "\tcase \"remote\":\n\t\tremoteDir := \"changed\"\n\t\tfmt.Println(remoteDir)"
	if adjustedNew != wantNew {
		t.Errorf("adjustedNew:\ngot  %q\nwant %q", adjustedNew, wantNew)
	}
}

func TestFuzzyWhitespaceMatch_TabVsSpace(t *testing.T) {
	content := "    func foo() {\n        x := 1\n    }"
	oldStr := "\tfunc foo() {\n\t\tx := 1\n\t}"
	newStr := "\tfunc bar() {\n\t\tx := 2\n\t}"

	actualOld, adjustedNew, ok := fuzzyWhitespaceMatch(content, oldStr, newStr)
	if !ok {
		t.Fatal("expected fuzzy match to succeed")
	}
	if actualOld != content {
		t.Errorf("actualOld:\ngot  %q\nwant %q", actualOld, content)
	}
	wantNew := "    func bar() {\n        x := 2\n    }"
	if adjustedNew != wantNew {
		t.Errorf("adjustedNew:\ngot  %q\nwant %q", adjustedNew, wantNew)
	}
}

func TestFuzzyWhitespaceMatch_NoMatch(t *testing.T) {
	content := "\tcase \"docker\":\n\t\tfmt.Println(\"docker\")"
	oldStr := "\t\tcase \"remote\":\n\t\t\tfmt.Println(\"remote\")"
	_, _, ok := fuzzyWhitespaceMatch(content, oldStr, "replacement")
	if ok {
		t.Fatal("expected fuzzy match to fail when content differs")
	}
}

func TestFuzzyWhitespaceMatch_AmbiguousMatch(t *testing.T) {
	content := "\tx := 1\n\ty := 2\n\tx := 1\n\tz := 3"
	oldStr := "\t\tx := 1"
	_, _, ok := fuzzyWhitespaceMatch(content, oldStr, "replacement")
	if ok {
		t.Fatal("expected fuzzy match to fail with multiple matches")
	}
}

func TestFuzzyWhitespaceMatch_AllWhitespaceOld(t *testing.T) {
	content := "\t\n\t\n\tx := 1"
	oldStr := "  \n  "
	_, _, ok := fuzzyWhitespaceMatch(content, oldStr, "replacement")
	if ok {
		t.Fatal("expected fuzzy match to fail when old_string is all whitespace")
	}
}

func TestDoReplace_FuzzyFallback(t *testing.T) {
	// End-to-end: doReplace should auto-correct whitespace and succeed
	content := "\tswitch x {\n\tcase \"a\":\n\t\tfmt.Println(\"a\")\n\tcase \"b\":\n\t\tfmt.Println(\"b\")\n\t}\n"
	oldStr := "\t\tcase \"a\":\n\t\t\tfmt.Println(\"a\")"
	newStr := "\t\tcase \"a\":\n\t\t\tfmt.Println(\"A\")"

	result, summary, err := doReplace(content, FileReplaceParams{OldString: oldStr, NewString: newStr}, "/test/file.go")
	if err != nil {
		t.Fatalf("expected fuzzy fallback to succeed, got error: %v", err)
	}
	if !strings.Contains(summary, "auto-corrected whitespace") {
		t.Errorf("summary should mention auto-correction, got: %s", summary)
	}
	expected := "\tswitch x {\n\tcase \"a\":\n\t\tfmt.Println(\"A\")\n\tcase \"b\":\n\t\tfmt.Println(\"b\")\n\t}\n"
	if result != expected {
		t.Errorf("result:\ngot  %q\nwant %q", result, expected)
	}
}

func TestDoReplace_FuzzyFallbackWithLineRange(t *testing.T) {
	content := "line1\n\tcase \"a\":\n\t\tx := 1\nline4\n"
	oldStr := "\t\tcase \"a\":\n\t\t\tx := 1"
	newStr := "\t\tcase \"a\":\n\t\t\tx := 2"

	result, summary, err := doReplace(content, FileReplaceParams{
		OldString: oldStr, NewString: newStr,
		StartLine: 2, EndLine: 3,
	}, "/test/file.go")
	if err != nil {
		t.Fatalf("expected fuzzy fallback with line range to succeed, got error: %v", err)
	}
	if !strings.Contains(summary, "auto-corrected") {
		t.Errorf("summary should mention auto-correction, got: %s", summary)
	}
	expected := "line1\n\tcase \"a\":\n\t\tx := 2\nline4\n"
	if result != expected {
		t.Errorf("result:\ngot  %q\nwant %q", result, expected)
	}
}

func TestDoReplace_FuzzyDoesNotOverrideExactMatch(t *testing.T) {
	content := "\tcase \"a\":\n\t\tx := 1\n"
	oldStr := "\tcase \"a\":\n\t\tx := 1"
	newStr := "\tcase \"a\":\n\t\tx := 2"

	result, summary, err := doReplace(content, FileReplaceParams{OldString: oldStr, NewString: newStr}, "/test/file.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(summary, "auto-corrected") {
		t.Errorf("exact match should NOT mention auto-correction, got: %s", summary)
	}
	expected := "\tcase \"a\":\n\t\tx := 2\n"
	if result != expected {
		t.Errorf("result:\ngot  %q\nwant %q", result, expected)
	}
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
		Sandbox:        nil,
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
