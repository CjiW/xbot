package tools

import (
	"os/exec"
	"strings"
	"testing"
)

func TestResolveSafePath(t *testing.T) {
	tests := []struct {
		name    string
		workDir string
		path    string
		want    string
		wantErr bool
	}{
		{"relative ok", "/root/work", "hello.txt", "/root/work/hello.txt", false},
		{"relative subdir", "/root/work", "sub/file.go", "/root/work/sub/file.go", false},
		{"escape attempt", "/root/work", "../../etc/passwd", "", true},
		{"absolute passthrough", "/root/work", "/tmp/file", "/tmp/file", false},
		{"empty path", "/root/work", "", "", true},
		{"dot path", "/root/work", ".", "/root/work", false},
		{"dot dot in middle", "/root/work", "sub/../other/file", "/root/work/other/file", false},
		{"empty workdir with relative", "", "hello.txt", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveSafePath(tt.workDir, tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveSafePath(%q, %q) error = %v, wantErr %v", tt.workDir, tt.path, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ResolveSafePath(%q, %q) = %q, want %q", tt.workDir, tt.path, got, tt.want)
			}
		})
	}
}

func TestBuildBwrapCmd(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not installed")
	}

	tmpDir := t.TempDir()
	cfg := DefaultBwrapConfig(tmpDir)
	cmd := BuildBwrapCmd(cfg, "echo hello && pwd")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bwrap failed: %v, output: %s", err, out)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("expected 'hello' in output, got: %s", out)
	}
	if !strings.Contains(string(out), "/workspace") {
		t.Errorf("expected '/workspace' as pwd, got: %s", out)
	}
}
