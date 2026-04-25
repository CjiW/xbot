package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// setupGrepTestDir creates a temporary directory structure for grep tests.
func setupGrepTestDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	files := map[string]string{
		"main.go": `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}

// TODO: add more features
`,
		"utils.go": `package main

func Add(a, b int) int {
	return a + b
}

func Subtract(a, b int) int {
	return a - b
}

// FIXME: handle overflow
`,
		"src/handler.go": `package src

import "net/http"

func HandleRequest(w http.ResponseWriter, r *http.Request) {
	// TODO: implement authentication
	w.WriteHeader(http.StatusOK)
}

func HandleError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
`,
		"src/handler_test.go": `package src

import "testing"

func TestHandleRequest(t *testing.T) {
	// TODO: write test
}
`,
		"docs/notes.md": `# Notes

This is a TODO list:
- Fix the bug
- Add tests
`,
		".hidden/secret.go": `package hidden

// secret TODO
func secret() {}
`,
	}

	for relPath, content := range files {
		fullPath := filepath.Join(tmpDir, relPath)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return tmpDir
}

func TestGrepTool_BasicSearch(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	input, _ := json.Marshal(map[string]any{
		"pattern": "func main",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "func main()") {
		t.Errorf("expected 'func main()' in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "main.go") {
		t.Errorf("expected main.go in results, got: %s", result.Summary)
	}
}

func TestGrepTool_RegexSearch(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	// Search for TODO or FIXME
	input, _ := json.Marshal(map[string]any{
		"pattern": "TODO|FIXME",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "TODO") {
		t.Errorf("expected TODO in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "FIXME") {
		t.Errorf("expected FIXME in results, got: %s", result.Summary)
	}
}

func TestGrepTool_CaseInsensitive(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	// Lowercase "todo" should not match with default settings
	input, _ := json.Marshal(map[string]any{
		"pattern": "todo",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "todo" should not match "TODO" in case-sensitive mode
	// but might match "TODO list:" in docs (no, "todo" != "TODO")
	if strings.Contains(result.Summary, "TODO: add more") {
		t.Errorf("case-sensitive search for 'todo' should not match 'TODO', got: %s", result.Summary)
	}

	// With ignore_case, should find all TODO occurrences
	input, _ = json.Marshal(map[string]any{
		"pattern":     "todo",
		"path":        tmpDir,
		"ignore_case": true,
	})

	result, err = tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "TODO") {
		t.Errorf("case-insensitive search for 'todo' should match 'TODO', got: %s", result.Summary)
	}
}

func TestGrepTool_IncludeFilter(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	// Search only in .go files
	input, _ := json.Marshal(map[string]any{
		"pattern": "TODO",
		"path":    tmpDir,
		"include": "*.go",
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "main.go") {
		t.Errorf("expected main.go in .go filtered results, got: %s", result.Summary)
	}
	// Should not contain .md files
	if strings.Contains(result.Summary, "notes.md") {
		t.Errorf("should not contain notes.md when filtering *.go, got: %s", result.Summary)
	}
}

func TestGrepTool_IncludeBracePattern(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	// Search in .go and .md files
	input, _ := json.Marshal(map[string]any{
		"pattern": "TODO",
		"path":    tmpDir,
		"include": "*.{go,md}",
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, ".go") {
		t.Errorf("expected .go file in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "notes.md") {
		t.Errorf("expected notes.md in results, got: %s", result.Summary)
	}
}

func TestGrepTool_ContextLines(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	// Search with context
	input, _ := json.Marshal(map[string]any{
		"pattern":       "func main",
		"path":          tmpDir,
		"include":       "*.go",
		"context_lines": 2,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should include the context lines around "func main()"
	if !strings.Contains(result.Summary, "func main()") {
		t.Errorf("expected 'func main()' in results, got: %s", result.Summary)
	}
	// Context should include nearby lines like fmt.Println
	if !strings.Contains(result.Summary, "Println") {
		t.Errorf("expected context line with Println, got: %s", result.Summary)
	}
}

func TestGrepTool_NoMatches(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	input, _ := json.Marshal(map[string]any{
		"pattern": "NONEXISTENT_STRING_XYZ",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "No matches found") {
		t.Errorf("expected 'No matches found' message, got: %s", result.Summary)
	}
}

func TestGrepTool_HiddenDirsSkipped(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	input, _ := json.Marshal(map[string]any{
		"pattern": "secret",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not find matches in .hidden directory
	if strings.Contains(result.Summary, ".hidden") {
		t.Errorf("should not search hidden directories, got: %s", result.Summary)
	}
}

func TestGrepTool_EmptyPattern(t *testing.T) {
	tool := &GrepTool{}

	input, _ := json.Marshal(map[string]any{
		"pattern": "",
	})

	_, err := tool.Execute(nil, string(input))
	if err == nil {
		t.Fatal("expected error for empty pattern, got nil")
	}
	if !strings.Contains(err.Error(), "pattern is required") {
		t.Errorf("expected 'pattern is required' error, got: %v", err)
	}
}

func TestGrepTool_InvalidRegex(t *testing.T) {
	tool := &GrepTool{}

	input, _ := json.Marshal(map[string]any{
		"pattern": "[invalid",
		"path":    "/tmp",
	})

	_, err := tool.Execute(nil, string(input))
	if err == nil {
		t.Fatal("expected error for invalid regex, got nil")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Errorf("expected 'invalid regex' error, got: %v", err)
	}
}

func TestGrepTool_InvalidJSON(t *testing.T) {
	tool := &GrepTool{}

	_, err := tool.Execute(nil, "not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "parse args") {
		t.Errorf("expected 'parse args' error, got: %v", err)
	}
}

func TestGrepTool_NonexistentPath(t *testing.T) {
	tool := &GrepTool{}

	input, _ := json.Marshal(map[string]any{
		"pattern": "foo",
		"path":    "/nonexistent/dir/that/should/not/exist",
	})

	_, err := tool.Execute(nil, string(input))
	if err == nil {
		t.Fatal("expected error for nonexistent path, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected 'does not exist' error, got: %v", err)
	}
}

func TestGrepTool_BinaryFileSkipped(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a binary file with null bytes
	binaryContent := []byte("some text\x00binary content\nfunc main()\n")
	if err := os.WriteFile(filepath.Join(tmpDir, "binary.dat"), binaryContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Create a normal text file
	if err := os.WriteFile(filepath.Join(tmpDir, "normal.go"), []byte("func main() {}"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &GrepTool{}
	input, _ := json.Marshal(map[string]any{
		"pattern": "func main",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "normal.go") {
		t.Errorf("expected normal.go in results, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "binary.dat") {
		t.Errorf("binary file should be skipped, got: %s", result.Summary)
	}
}

func TestGrepTool_LineNumbers(t *testing.T) {
	tmpDir := t.TempDir()

	content := "line one\nline two\nline three\nline four\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &GrepTool{}
	input, _ := json.Marshal(map[string]any{
		"pattern": "three",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Line "line three" is on line 3
	if !strings.Contains(result.Summary, "3: line three") {
		t.Errorf("expected '3: line three' in results, got: %s", result.Summary)
	}
}

func TestGrepTool_FuncRegex(t *testing.T) {
	tmpDir := setupGrepTestDir(t)
	tool := &GrepTool{}

	// Search for function definitions using regex
	input, _ := json.Marshal(map[string]any{
		"pattern": `func \w+\(`,
		"path":    tmpDir,
		"include": "*.go",
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "func main()") {
		t.Errorf("expected 'func main()' in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "func Add(") {
		t.Errorf("expected 'func Add(' in results, got: %s", result.Summary)
	}
}

func TestGrepTool_ResultTruncation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file with many matching lines
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "match line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "big.txt"), []byte(sb.String()), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &GrepTool{}
	input, _ := json.Marshal(map[string]any{
		"pattern": "match line",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "truncated") {
		t.Errorf("expected truncation message for many results, got: %s", result.Summary)
	}
}

func TestExpandBracePattern(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"*.go", []string{"*.go"}},
		{"*.{go,ts}", []string{"*.go", "*.ts"}},
		{"*.{go,ts,js}", []string{"*.go", "*.ts", "*.js"}},
		{"src/*.{go,ts}", []string{"src/*.go", "src/*.ts"}},
		{"no_braces", []string{"no_braces"}},
		{"{a,b}.txt", []string{"a.txt", "b.txt"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := expandBracePattern(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("expandBracePattern(%q) = %v, want %v", tt.input, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("expandBracePattern(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSearchFile_ContextLines(t *testing.T) {
	tmpDir := t.TempDir()
	content := "line1\nline2\nmatch here\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	re := regexp.MustCompile("match here")
	matches, err := searchFile(filepath.Join(tmpDir, "test.txt"), re, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// contextLines=2 should return 5 lines (line1, line2, match here, line4, line5)
	if len(matches) != 5 {
		t.Errorf("expected 5 matches with context_lines=2, got %d: %+v", len(matches), matches)
	}
	if matches[0].LineNumber != 1 {
		t.Errorf("expected first context line to be line 1, got line %d", matches[0].LineNumber)
	}
	if matches[2].LineNumber != 3 || !strings.Contains(matches[2].Line, "match here") {
		t.Errorf("expected match line to be line 3, got line %d: %s", matches[2].LineNumber, matches[2].Line)
	}
}

func TestParseGrepOutputWithContextLines(t *testing.T) {
	// Simulate grep -C 2 output with '-' separator for context lines
	input := `main.go-8-import "fmt"
main.go-9-
main.go:10:func main() {
main.go-11-	fmt.Println("hello")
main.go-12-}`

	lines := strings.Split(input, "\n")
	contextLineCount := 0
	matchLineCount := 0

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) < 2 {
			dashParts := strings.SplitN(line, "-", 2)
			if len(dashParts) >= 2 {
				contextLineCount++
			}
		} else {
			matchLineCount++
		}
	}

	if contextLineCount != 4 {
		t.Errorf("expected 4 context lines (using '-' separator), got %d", contextLineCount)
	}
	if matchLineCount != 1 {
		t.Errorf("expected 1 match line (using ':' separator), got %d", matchLineCount)
	}
}

func TestSandboxPathResolution(t *testing.T) {
	sandboxBase := "/workspace"

	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{"exact match with sandboxBase", "/workspace", "/workspace"},
		{"subdirectory", "/workspace/src", "/workspace/src"},
		{"relative path", "src", "/workspace/src"},
		{"empty path uses sandboxBase", "", "/workspace"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var searchDir string
			if tt.path != "" {
				if tt.path == sandboxBase || strings.HasPrefix(tt.path, sandboxBase+"/") {
					searchDir = tt.path
				} else {
					searchDir = sandboxBase + "/" + tt.path
				}
			} else {
				searchDir = sandboxBase
			}

			if searchDir != tt.expected {
				t.Errorf("got %q, want %q", searchDir, tt.expected)
			}
		})
	}
}

func TestConvertGoRE2ToERE(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"digit shorthand", `\d+`, `[0-9]+`},
		{"word char with literal parens", `\w+\(\)`, `[a-zA-Z0-9_]+\(\)`},
		{"inline case-insensitive flag", `(?i)hello`, `hello`},
		{"space and word shorthand", `func\s+\w+`, "func[[:space:]]+[a-zA-Z0-9_]+"},
		{"tab and newline escapes", `\t\n`, "\t\n"},
		{"digit quantifier", `\d{2,4}`, `[0-9]{2,4}`},
		{"named group", `(?P<name>\w+)`, `([a-zA-Z0-9_]+)`},
		{"double backslash literal", `\\d`, `\\d`},
		{"non-capturing group", `(?:hello)`, `(hello)`},
		{"compound flags", `(?im)test`, `test`},
		{"flag group with colon", `(?i:hello)`, `(hello)`},
		{"word boundary preserved", `\bword\b`, `\bword\b`},
		{"uppercase shortcuts", `\D\W\S`, `[^0-9][^a-zA-Z0-9_][^[:space:]]`},
		{"braced quantifier exact", `\d{3}`, `[0-9]{3}`},
		{"braced quantifier open", `\d{3,}`, `[0-9]{3,}`},
		{"braced quantifier range", `\w{2,5}`, `[a-zA-Z0-9_]{2,5}`},
		{"escaped backslash then word", `\\\w`, `\\[a-zA-Z0-9_]`},
		{"no change plain text", `hello world`, `hello world`},
		{"no change ere alternation", `foo|bar`, `foo|bar`},
		{"no change ere quantifier", `a{2,4}`, `a{2,4}`},
		{"escaped brace non-quantifier", `\{`, `\{`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convertGoRE2ToERE(tt.input)
			if err != nil {
				t.Fatalf("convertGoRE2ToERE(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Errorf("convertGoRE2ToERE(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestConvertGoRE2ToERE_Roundtrip(t *testing.T) {
	// Verify that common Go RE2 patterns compile after conversion.
	// We can't easily test grep -E here, but we can at least verify
	// that the conversion doesn't produce obviously broken output
	// by checking that simple cases round-trip correctly through
	// Go's regexp (which accepts both RE2 and many ERE constructs).
	patterns := []string{
		`\d+`,
		`\w+\(\)`,
		`func\s+\w+`,
		`\d{2,4}`,
		`\bword\b`,
		`(?P<name>\w+)`,
	}

	for _, pat := range patterns {
		t.Run(pat, func(t *testing.T) {
			ere, err := convertGoRE2ToERE(pat)
			if err != nil {
				t.Fatalf("conversion error: %v", err)
			}
			// The ERE output should also be a valid Go RE2 pattern
			// (since RE2 is a superset of ERE in most practical cases).
			// This is just a sanity check, not a guarantee of ERE correctness.
			re, err := regexp.Compile(ere)
			if err != nil {
				t.Errorf("converted ERE pattern %q is not valid Go RE2: %v", ere, err)
			}
			_ = re // use re to avoid unused variable error
		})
	}
}

func TestGrepTool_AltPatternMatchesBoth(t *testing.T) {
	// Regression test: pattern with | (alternation) must match both alternatives.
	// Without single-quoting in sandbox mode, the shell interprets | as a pipe.
	tmpDir := t.TempDir()
	content := "first line: publish event\nsecond line: browse catalog\nthird line: nothing here\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &GrepTool{}
	input, _ := json.Marshal(map[string]any{
		"pattern": "publish|browse",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "publish") {
		t.Errorf("expected 'publish' in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "browse") {
		t.Errorf("expected 'browse' in results, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "nothing here") {
		t.Errorf("should not contain 'nothing here', got: %s", result.Summary)
	}
}

func TestGrepTool_SingleFilePath(t *testing.T) {
	// Regression test: path pointing to a file (not directory) should search that file.
	// Previously, GrepTool returned "path is not a directory" error for file paths.
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main.go")
	content := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}

func helper() string {
	return "helper result"
}
`
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &GrepTool{}

	// Test 1: search single file by path
	t.Run("basic pattern on file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"pattern": "func main",
			"path":    filePath,
		})
		result, err := tool.Execute(nil, string(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result.Summary, "func main") {
			t.Errorf("expected 'func main' in results, got: %s", result.Summary)
		}
	})

	// Test 2: alternation pattern on file
	t.Run("alternation pattern on file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"pattern": "func main|func helper",
			"path":    filePath,
		})
		result, err := tool.Execute(nil, string(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result.Summary, "func main") {
			t.Errorf("expected 'func main' in results, got: %s", result.Summary)
		}
		if !strings.Contains(result.Summary, "func helper") {
			t.Errorf("expected 'func helper' in results, got: %s", result.Summary)
		}
	})

	// Test 3: no match returns "No matches found"
	t.Run("no match on file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"pattern": "nonexistent_pattern_xyz",
			"path":    filePath,
		})
		result, err := tool.Execute(nil, string(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result.Summary, "No matches found") {
			t.Errorf("expected 'No matches found', got: %s", result.Summary)
		}
	})

	// Test 4: context_lines works on single file
	t.Run("context lines on file", func(t *testing.T) {
		input, _ := json.Marshal(map[string]any{
			"pattern":       "func helper",
			"path":          filePath,
			"context_lines": 1,
		})
		result, err := tool.Execute(nil, string(input))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(result.Summary, "func helper") {
			t.Errorf("expected 'func helper' in results, got: %s", result.Summary)
		}
		// Context line should include surrounding lines
		if !strings.Contains(result.Summary, "return \"helper result\"") {
			t.Errorf("expected context line with 'return' in results, got: %s", result.Summary)
		}
	})
}
