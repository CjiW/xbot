package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsBangCommand(t *testing.T) {
	tests := []struct {
		input   string
		wantCmd string
		wantOK  bool
	}{
		{"!ls -la", "ls -la", true},
		{"!pwd", "pwd", true},
		{"! echo hello", "echo hello", true},
		{"!  echo hello", "echo hello", true}, // multiple spaces after !
		{"!cat /etc/os-release", "cat /etc/os-release", true},
		{"!", "", false},        // just `!`, no command
		{"!  ", "", false},      // `!` followed by whitespace only
		{"hello", "", false},    // normal message
		{"/version", "", false}, // slash command
		{"", "", false},         // empty
		{"  !ls", "ls", true},   // leading whitespace
		{"!!ls", "!ls", true},   // double bang (passes through, shell handles it)
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			cmd, ok := isBangCommand(tt.input)
			if ok != tt.wantOK {
				t.Errorf("isBangCommand(%q) ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if cmd != tt.wantCmd {
				t.Errorf("isBangCommand(%q) cmd = %q, want %q", tt.input, cmd, tt.wantCmd)
			}
		})
	}
}

func TestFormatBangOutput(t *testing.T) {
	tests := []struct {
		name    string
		command string
		output  string
		err     error
		want    string
	}{
		{
			name:    "success with output",
			command: "ls",
			output:  "file1\nfile2",
			err:     nil,
			want:    "```\nfile1\nfile2\n```",
		},
		{
			name:    "success no output",
			command: "mkdir test",
			output:  "",
			err:     nil,
			want:    "`OK (no output)`",
		},
		{
			name:    "error with output",
			command: "cat missing",
			output:  "cat: missing: No such file or directory",
			err:     fmt.Errorf("exit status 1"),
			want:    "```\ncat: missing: No such file or directory\n```\n`exit: exit status 1`",
		},
		{
			name:    "error no output",
			command: "false",
			output:  "",
			err:     fmt.Errorf("exit status 1"),
			want:    "`exit: exit status 1`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatBangOutput(tt.command, tt.output, tt.err)
			if got != tt.want {
				t.Errorf("formatBangOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteBangOutputFile(t *testing.T) {
	tmpDir := t.TempDir()

	command := "find / -type f"
	output := strings.Repeat("line\n", 1000)

	a := &Agent{} // no sandbox, uses os.WriteFile directly
	filePath, err := a.writeBangOutputFile(context.Background(), tmpDir, command, output, nil, "test-user")
	if err != nil {
		t.Fatalf("writeBangOutputFile() error = %v", err)
	}

	// Check file exists
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("output file not found: %v", err)
	}

	// Check file is in the workspace dir
	if !strings.HasPrefix(filePath, tmpDir) {
		t.Errorf("file path %q not under workspace %q", filePath, tmpDir)
	}

	// Check file extension
	if filepath.Ext(filePath) != ".md" {
		t.Errorf("file extension = %q, want .md", filepath.Ext(filePath))
	}

	// Check content contains code block
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "```") {
		t.Error("output file should contain code block markers")
	}
	if !strings.Contains(content, command) {
		t.Error("output file should contain the command")
	}
}

func TestWriteBangOutputFileWithError(t *testing.T) {
	tmpDir := t.TempDir()

	command := "cat missing"
	output := "cat: missing: No such file or directory"
	execErr := fmt.Errorf("exit status 1")

	a := &Agent{} // no sandbox, uses os.WriteFile directly
	filePath, err := a.writeBangOutputFile(context.Background(), tmpDir, command, output, execErr, "test-user")
	if err != nil {
		t.Fatalf("writeBangOutputFile() error = %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "exit status 1") {
		t.Error("output file should contain exit status")
	}
}
