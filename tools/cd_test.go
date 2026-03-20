package tools

import (
	"os"
	"path/filepath"
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
	var savedDir string
	ctx := &ToolContext{
		WorkspaceRoot: tmpDir,
		CurrentDir:    tmpDir,
		SetCurrentDir: func(dir string) { savedDir = dir },
	}

	tool := &CdTool{}
	_, err := tool.Execute(ctx, `{"path":"/tmp"}`)
	if err != nil {
		t.Errorf("expected cd /tmp to succeed (no permission check), got err: %v", err)
	}
	if savedDir != "/tmp" {
		t.Errorf("expected savedDir=/tmp, got %q", savedDir)
	}
}

func TestCdTool_EmptyPath(t *testing.T) {
	tool := &CdTool{}
	_, err := tool.Execute(&ToolContext{}, `{"path":""}`)
	if err == nil {
		t.Error("expected error for empty path")
	}
}
