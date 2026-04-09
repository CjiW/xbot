package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileReplaceTool_PreservesExistingFileMode(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "mode.txt")
	if err := os.WriteFile(path, []byte("hello world\n"), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}

	tool := &FileReplaceTool{}
	ctx := &ToolContext{WorkingDir: tmpDir}
	_, err := tool.Execute(ctx, `{"path":"`+path+`","old_string":"world","new_string":"xbot"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat result: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("expected mode 0600 preserved, got %#o", got)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(content) != "hello xbot\n" {
		t.Fatalf("unexpected content: %q", string(content))
	}
}
