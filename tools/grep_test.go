package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	if !strings.Contains(err.Error(), "invalid parameters") {
		t.Errorf("expected 'invalid parameters' error, got: %v", err)
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
