package tools

import (
	"fmt"

	"os"

	"path/filepath"

	"strings"

	"testing"
)

func TestCdTool_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var savedDir string
	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		SetCurrentDir: func(dir string) { savedDir = dir },
	}

	tool := &CdTool{}

	res, err := tool.Execute(ctx, `{"path":"sub"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if savedDir != subDir {
		t.Errorf("expected savedDir=%q, got %q", subDir, savedDir)
	}
	if ctx.CurrentDir != subDir {
		t.Errorf("expected CurrentDir=%q, got %q", subDir, ctx.CurrentDir)
	}
	if res == nil || res.Summary == "" {
		t.Error("expected non-empty result")
	}
}

func TestCdTool_AbsolutePath(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "abs")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var savedDir string
	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		SetCurrentDir: func(dir string) { savedDir = dir },
	}

	tool := &CdTool{}
	_, err := tool.Execute(ctx, `{"path":"`+subDir+`"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if savedDir != subDir {
		t.Errorf("expected %q, got %q", subDir, savedDir)
	}
}

func TestCdTool_RelativeFromCurrentDir(t *testing.T) {
	tmpDir := t.TempDir()
	aDir := filepath.Join(tmpDir, "a")
	bDir := filepath.Join(aDir, "b")
	if err := os.MkdirAll(bDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var savedDir string
	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		CurrentDir:    aDir,
		SetCurrentDir: func(dir string) { savedDir = dir },
	}

	tool := &CdTool{}
	_, err := tool.Execute(ctx, `{"path":"b"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if savedDir != bDir {
		t.Errorf("expected %q, got %q", bDir, savedDir)
	}
}

func TestCdTool_DotDot(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "child")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var savedDir string
	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		CurrentDir:    subDir,
		SetCurrentDir: func(dir string) { savedDir = dir },
	}

	tool := &CdTool{}
	_, err := tool.Execute(ctx, `{"path":".."}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	realTmp, _ := filepath.EvalSymlinks(tmpDir)
	if savedDir != realTmp {
		t.Errorf("expected %q, got %q", realTmp, savedDir)
	}
}

func TestCdTool_NotADirectory(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "file.txt")
	if err := os.WriteFile(filePath, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		SetCurrentDir: func(dir string) {},
	}

	tool := &CdTool{}
	_, err := tool.Execute(ctx, `{"path":"file.txt"}`)
	if err == nil {
		t.Error("expected error for non-directory path")
	}
}

func TestCdTool_NonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		SetCurrentDir: func(dir string) {},
	}

	tool := &CdTool{}
	_, err := tool.Execute(ctx, `{"path":"nonexistent"}`)
	if err == nil {
		t.Error("expected error for non-existent directory")
	}
}

func TestCdTool_EscapeWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		CurrentDir:    tmpDir,
		SetCurrentDir: func(dir string) {},
	}

	tool := &CdTool{}
	_, err := tool.Execute(ctx, `{"path":"/tmp"}`)
	if err == nil {
		t.Error("expected error when trying to cd outside workspace")
	}
}

func TestCdTool_EmptyPath(t *testing.T) {
	tool := &CdTool{}
	_, err := tool.Execute(&ToolContext{}, `{"path":""}`)
	if err == nil {
		t.Error("expected error for empty path")
	}
}

// --- Phase 1: Cd tool project context tests ---

func TestDetectProjectContext_GoProject(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module example\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "go.sum"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Test\n"), 0o644)

	result := detectProjectContext(tmpDir)
	if result == "" {
		t.Fatal("expected non-empty project context for Go project")
	}
	if !contains(result, "Go") {
		t.Errorf("expected 'Go' type, got: %s", result)
	}
	if !contains(result, "go.mod") {
		t.Errorf("expected 'go.mod', got: %s", result)
	}
}

func TestDetectProjectContext_NodeProject(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte("{}\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "tsconfig.json"), []byte("{}\n"), 0o644)

	result := detectProjectContext(tmpDir)
	if result == "" {
		t.Fatal("expected non-empty project context for Node.js project")
	}
	if !contains(result, "Node.js") {
		t.Errorf("expected 'Node.js' type, got: %s", result)
	}
	if !contains(result, "TypeScript") {
		t.Errorf("expected 'TypeScript' type, got: %s", result)
	}
}

func TestDetectProjectContext_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	result := detectProjectContext(tmpDir)
	if result != "" {
		t.Errorf("expected empty project context for empty dir, got: %s", result)
	}
}

func TestBuildDirectoryTree(t *testing.T) {
	tmpDir := t.TempDir()
	os.Mkdir(filepath.Join(tmpDir, "src"), 0o755)
	os.Mkdir(filepath.Join(tmpDir, "cmd"), 0o755)
	os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte("module test\n"), 0o644)
	// Hidden file that should be shown
	os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("*.o\n"), 0o644)
	// Hidden file that should be hidden
	os.WriteFile(filepath.Join(tmpDir, ".hidden_secret"), []byte("secret\n"), 0o644)

	result := buildDirectoryTree(tmpDir)
	if result == "" {
		t.Fatal("expected non-empty directory tree")
	}
	if !contains(result, "Directory structure") {
		t.Error("expected 'Directory structure' header")
	}
	if !contains(result, "src") {
		t.Error("expected 'src' directory in tree")
	}
	if !contains(result, "cmd") {
		t.Error("expected 'cmd' directory in tree")
	}
	if !contains(result, "main.go") {
		t.Error("expected 'main.go' in tree")
	}
	if !contains(result, ".gitignore") {
		t.Error("expected '.gitignore' (known dot file) in tree")
	}
	if contains(result, ".hidden_secret") {
		t.Error("expected '.hidden_secret' to be filtered out")
	}
}

func TestCdTool_EnhancedResult(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "myproject")
	os.Mkdir(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "go.mod"), []byte("module example\n"), 0o644)
	os.Mkdir(filepath.Join(subDir, "cmd"), 0o755)
	os.WriteFile(filepath.Join(subDir, "main.go"), []byte("package main\n"), 0o644)

	var savedDir string
	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		SetCurrentDir: func(dir string) { savedDir = dir },
	}

	tool := &CdTool{}
	res, err := tool.Execute(ctx, `{"path":"myproject"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify cd worked
	if savedDir != subDir {
		t.Errorf("expected savedDir=%q, got %q", subDir, savedDir)
	}

	// Verify enhanced result contains project context
	if !contains(res.Summary, "Changed directory to") {
		t.Error("expected 'Changed directory to' in result")
	}
	if !contains(res.Summary, "Project context") {
		t.Error("expected 'Project context' in result")
	}
	if !contains(res.Summary, "Go") {
		t.Error("expected 'Go' project type in result")
	}
	if !contains(res.Summary, "Directory structure") {
		t.Error("expected 'Directory structure' in result")
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tt := range tests {
		got := formatSize(tt.bytes)
		if got != tt.want {
			t.Errorf("formatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}
func TestBuildDirectoryTree_ExceedsLimit(t *testing.T) {
	tmpDir := t.TempDir()
	// Create 40 files + 5 directories = 45 items
	for i := 0; i < 40; i++ {
		os.WriteFile(filepath.Join(tmpDir, fmt.Sprintf("file%02d.txt", i)), []byte("x"), 0o644)
	}
	for i := 0; i < 5; i++ {
		os.Mkdir(filepath.Join(tmpDir, fmt.Sprintf("dir%02d", i)), 0o755)
	}

	result := buildDirectoryTree(tmpDir)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	lines := strings.Count(result, "\n")
	if lines > 32 { // header + max 30 entries + trailing newline
		t.Errorf("expected at most 32 lines, got %d", lines)
	}
	// Verify the last visible file/dir is within limit
	// After sorting: dir00-dir04 (5 dirs first), then file00-file24 (25 files) = 30
	if strings.Contains(result, "file25") {
		t.Error("file25 should be truncated (exceeds 30 entry limit)")
	}
}

func TestDetectProjectContext_ExceedsFileLimit(t *testing.T) {
	tmpDir := t.TempDir()
	// Create many known marker files
	markers := []string{
		"go.mod", "go.sum", "package.json", "tsconfig.json",
		"pnpm-lock.yaml", "yarn.lock", "package-lock.json",
		"Cargo.toml", "pyproject.toml", "setup.py",
		"requirements.txt", "Pipfile", "pom.xml", "build.gradle",
		"build.gradle.kts", "Gemfile", "composer.json",
	}
	for _, m := range markers {
		os.WriteFile(filepath.Join(tmpDir, m), []byte(""), 0o644)
	}

	result := detectProjectContext(tmpDir)
	if result == "" {
		t.Fatal("expected non-empty project context")
	}
	if !strings.Contains(result, "more") {
		t.Errorf("expected 'X more' truncation hint, got: %s", result)
	}
	if !strings.Contains(result, "Project context") {
		t.Error("expected 'Project context' header")
	}
}
