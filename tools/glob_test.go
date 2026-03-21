package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupGlobTestDir creates a temporary directory structure for glob tests.
func setupGlobTestDir(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	// Create directory structure:
	// tmpDir/
	//   main.go
	//   README.md
	//   src/
	//     app.go
	//     app_test.go
	//     utils/
	//       helper.go
	//       helper_test.go
	//   docs/
	//     guide.md
	//   .hidden/
	//     secret.go

	files := map[string]string{
		"main.go":                  "package main",
		"README.md":                "# README",
		"src/app.go":               "package src",
		"src/app_test.go":          "package src",
		"src/utils/helper.go":      "package utils",
		"src/utils/helper_test.go": "package utils",
		"docs/guide.md":            "# Guide",
		".hidden/secret.go":        "package hidden",
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

func TestGlobTool_BasicPattern(t *testing.T) {
	tmpDir := setupGlobTestDir(t)
	tool := &GlobTool{}

	// Match *.go in root directory only
	input, _ := json.Marshal(map[string]string{
		"pattern": "*.go",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "main.go") {
		t.Errorf("expected main.go in results, got: %s", result.Summary)
	}
	// Should not contain nested files
	if strings.Contains(result.Summary, "app.go") {
		t.Errorf("should not contain nested app.go for non-recursive pattern, got: %s", result.Summary)
	}
}

func TestGlobTool_RecursiveDoublestar(t *testing.T) {
	tmpDir := setupGlobTestDir(t)
	tool := &GlobTool{}

	// Match **/*.go recursively
	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*.go",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should find all .go files (except .hidden)
	expectedFiles := []string{"main.go", "app.go", "app_test.go", "helper.go", "helper_test.go"}
	for _, f := range expectedFiles {
		if !strings.Contains(result.Summary, f) {
			t.Errorf("expected %s in results, got: %s", f, result.Summary)
		}
	}

	// Should NOT find files in .hidden directory
	if strings.Contains(result.Summary, "secret.go") {
		t.Errorf("should not contain files from .hidden directory, got: %s", result.Summary)
	}
}

func TestGlobTool_DoublestarMatchesRoot(t *testing.T) {
	tmpDir := setupGlobTestDir(t)
	tool := &GlobTool{}

	// **/*.go should also match files in the root (** matches zero dirs)
	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*.go",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "main.go") {
		t.Errorf("**/*.go should match root-level main.go, got: %s", result.Summary)
	}
}

func TestGlobTool_SubdirectoryPattern(t *testing.T) {
	tmpDir := setupGlobTestDir(t)
	tool := &GlobTool{}

	// Match src/**/*.go
	input, _ := json.Marshal(map[string]string{
		"pattern": "src/**/*.go",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "app.go") {
		t.Errorf("expected app.go in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "helper.go") {
		t.Errorf("expected helper.go in results, got: %s", result.Summary)
	}
	// Should not match root main.go
	if strings.Contains(result.Summary, filepath.Join(tmpDir, "main.go")) {
		t.Errorf("should not match root main.go for src/**/*.go pattern, got: %s", result.Summary)
	}
}

func TestGlobTool_TestFilePattern(t *testing.T) {
	tmpDir := setupGlobTestDir(t)
	tool := &GlobTool{}

	// Match **/*_test.go
	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*_test.go",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "app_test.go") {
		t.Errorf("expected app_test.go in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "helper_test.go") {
		t.Errorf("expected helper_test.go in results, got: %s", result.Summary)
	}
	// Should not match non-test files
	if strings.Contains(result.Summary, filepath.Join(tmpDir, "main.go")) {
		t.Errorf("should not match main.go for *_test.go pattern, got: %s", result.Summary)
	}
}

func TestGlobTool_MarkdownPattern(t *testing.T) {
	tmpDir := setupGlobTestDir(t)
	tool := &GlobTool{}

	// Match **/*.md
	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*.md",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "README.md") {
		t.Errorf("expected README.md in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "guide.md") {
		t.Errorf("expected guide.md in results, got: %s", result.Summary)
	}
}

func TestGlobTool_NoMatches(t *testing.T) {
	tmpDir := setupGlobTestDir(t)
	tool := &GlobTool{}

	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*.xyz",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "No files matched") {
		t.Errorf("expected no match message, got: %s", result.Summary)
	}
}

func TestGlobTool_EmptyPattern(t *testing.T) {
	tool := &GlobTool{}

	input, _ := json.Marshal(map[string]string{
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

func TestGlobTool_InvalidJSON(t *testing.T) {
	tool := &GlobTool{}

	_, err := tool.Execute(nil, "not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
	if !strings.Contains(err.Error(), "invalid parameters") {
		t.Errorf("expected 'invalid parameters' error, got: %v", err)
	}
}

func TestGlobTool_NonexistentPath(t *testing.T) {
	tool := &GlobTool{}

	input, _ := json.Marshal(map[string]string{
		"pattern": "*.go",
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

func TestGlobTool_HiddenDirsSkipped(t *testing.T) {
	tmpDir := setupGlobTestDir(t)
	tool := &GlobTool{}

	// Should not find files in .hidden directory
	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*.go",
		"path":    tmpDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if strings.Contains(result.Summary, "secret.go") {
		t.Errorf("hidden directory files should be skipped, got: %s", result.Summary)
	}
}

func TestMatchDoublestar(t *testing.T) {
	tests := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"**/*.go", "main.go", true},
		{"**/*.go", "src/app.go", true},
		{"**/*.go", "src/utils/helper.go", true},
		{"**/*.go", "main.txt", false},
		{"src/**/*.go", "src/app.go", true},
		{"src/**/*.go", "src/utils/helper.go", true},
		{"src/**/*.go", "main.go", false},
		{"*.go", "main.go", true},
		{"*.go", "src/app.go", false},
		{"src/*.go", "src/app.go", true},
		{"src/*.go", "src/utils/helper.go", false},
		{"**/test/**/*.go", "test/a.go", true},
		{"**/test/**/*.go", "src/test/a.go", true},
		{"**/test/**/*.go", "src/test/sub/a.go", true},
		{"**", "anything", true},
		{"**", "a/b/c", true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+"_"+tt.path, func(t *testing.T) {
			got := matchDoublestar(tt.pattern, tt.path)
			if got != tt.want {
				t.Errorf("matchDoublestar(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.want)
			}
		})
	}
}

func TestGlobToFindArgs(t *testing.T) {
	tests := []struct {
		pattern    string
		searchBase string
		args       string
	}{
		{"*.go", "", "-maxdepth 1 -name '*.go'"},
		{"*.txt", "", "-maxdepth 1 -name '*.txt'"},
		{"*", "", "-maxdepth 1 -name '*'"},
		{"src/*.go", "src", "-maxdepth 1 -name '*.go'"},
		{"pkg/utils/*.go", "pkg/utils", "-maxdepth 1 -name '*.go'"},
		{"a/b/c/*.go", "a/b/c", "-maxdepth 1 -name '*.go'"},
		{"**/*.go", "", "-name '*.go'"},
		{"**/*.ts", "", "-name '*.ts'"},
		{"src/**/*.go", "src", "-name '*.go'"},
		{"**/test/*.go", "", "-path '*/test/*.go'"},
		{"src/**/test/*.go", "src", "-path '*/test/*.go'"},
		{"**", "", ""},
		{"src/**", "src", ""},
		{"**/*", "", "-name '*'"},
		{"/**/*.go", "", "-name '*.go'"},
		{"**/*.go/", "", "-name '*.go'"},
		{"", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.pattern, func(t *testing.T) {
			searchBase, args := globToFindArgs(tt.pattern)
			if searchBase != tt.searchBase {
				t.Errorf("globToFindArgs(%q) searchBase = %q, want %q", tt.pattern, searchBase, tt.searchBase)
			}
			if args != tt.args {
				t.Errorf("globToFindArgs(%q) args = %q, want %q", tt.pattern, args, tt.args)
			}
		})
	}
}

func TestGlobToFindArgs_ShellEscape(t *testing.T) {
	// Security regression tests: verify shellEscape prevents command injection
	// via crafted glob patterns containing single quotes.
	tests := []struct {
		name           string
		pattern        string
		wantArgs       string
		wantNoContains []string
	}{
		{
			name:     "single quote injection attempt",
			pattern:  "*'; echo pwned; '",
			wantArgs: "-maxdepth 1 -name '*'\\''; echo pwned; '\\'''",
		},
		{
			name:     "single quote in doublestar pattern",
			pattern:  "**/*'; echo pwned; '",
			wantArgs: "-name '*'\\''; echo pwned; '\\'''",
		},
		{
			name:     "single quote in path pattern",
			pattern:  "**/test'/*.go",
			wantArgs: "-path '*/test'\\''/*.go'",
		},
		{
			name:     "dollar sign in pattern (no-op in single quotes)",
			pattern:  "*$HOME*",
			wantArgs: "-maxdepth 1 -name '*$HOME*'",
		},
		{
			name:     "backtick in pattern",
			pattern:  "*`whoami`*",
			wantArgs: "-maxdepth 1 -name '*`whoami`*'",
		},
		{
			name:     "normal patterns unchanged (no quotes)",
			pattern:  "*.go",
			wantArgs: "-maxdepth 1 -name '*.go'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, args := globToFindArgs(tt.pattern)
			if args != tt.wantArgs {
				t.Errorf("globToFindArgs(%q) args = %q, want %q", tt.pattern, args, tt.wantArgs)
			}
			// Verify raw injected pattern doesn't appear unescaped
			for _, forbidden := range tt.wantNoContains {
				if strings.Contains(args, forbidden) {
					t.Errorf("globToFindArgs(%q) args contains unescaped sequence %q: %s", tt.pattern, forbidden, args)
				}
			}
		})
	}
}

func TestGlobTool_PathWithSpaces(t *testing.T) {
	// Regression test: paths with spaces must work in sandbox find commands.
	// Without single-quoting the search directory, paths with spaces break.
	tmpDir := t.TempDir()
	spaceDir := filepath.Join(tmpDir, "my project")
	if err := os.MkdirAll(spaceDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spaceDir, "app.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &GlobTool{}
	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*.go",
		"path":    spaceDir,
	})

	result, err := tool.Execute(nil, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Summary, "app.go") {
		t.Errorf("expected app.go in results, got: %s", result.Summary)
	}
}
