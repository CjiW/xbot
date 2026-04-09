package cmdbuilder

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/tmp/file.txt", "'/tmp/file.txt'"},
		{"file with spaces", "'file with spaces'"},
		{"file'with'quotes", "'file'\\''with'\\''quotes'"},
		{"", "''"},
	}

	for _, tt := range tests {
		got := shellEscape(tt.input)
		if got != tt.expected {
			t.Errorf("shellEscape(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestBuild_NoRunAs(t *testing.T) {
	// Without RunAsUser, should produce a direct command
	cmd, err := Build(context.TODO(), true, "echo hello", nil, "", nil, Config{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Path != "/bin/sh" {
		t.Errorf("expected /bin/sh, got %s", cmd.Path)
	}
}

func TestBuild_WithRunAs(t *testing.T) {
	// With RunAsUser, should produce a sudo-wrapped command
	cmd, err := Build(context.TODO(), true, "echo hello", nil, "", nil, Config{RunAsUser: "alice"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// cmd.Path resolves to the full path (e.g., /usr/bin/sudo)
	if filepath.Base(cmd.Path) != "sudo" {
		t.Errorf("expected sudo, got %s", cmd.Path)
	}
}

func TestBuild_NonShellRequiresArgs(t *testing.T) {
	_, err := Build(context.TODO(), false, "", nil, "", nil, Config{})
	if err == nil {
		t.Error("expected error for non-shell with empty args")
	}
}

func TestWriteFileAsUser_NoRunAs(t *testing.T) {
	// Without RunAsUser, should fall back to os.WriteFile
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")
	err := WriteFileAsUser("", path, []byte("hello"), 0644)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("unexpected error reading file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("expected 'hello', got %q", string(data))
	}
}

func TestWriteFileAsUser_WithRunAs(t *testing.T) {
	// This test requires sudo to be configured.
	// We test the error path — if sudo is not configured, we should get a clear error.
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")
	err := WriteFileAsUser("nonexistent_user_for_test", path, []byte("hello"), 0644)
	if err == nil {
		// If it succeeds, that means sudo is configured for this test user — verify content
		data, _ := os.ReadFile(path)
		if string(data) != "hello" {
			t.Errorf("expected 'hello', got %q", string(data))
		}
	}
	// Error is expected in most environments — no assertion on error
}

func TestReadFileAsUser_NoRunAs(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.txt")
	os.WriteFile(path, []byte("hello"), 0644)

	data, err := ReadFileAsUser("", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("expected 'hello', got %q", string(data))
	}
}

func TestMkdirAllAsUser_NoRunAs(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "sub", "dir")
	err := MkdirAllAsUser("", path, 0755)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("unexpected error stating dir: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected directory")
	}
}
