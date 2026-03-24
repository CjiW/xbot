package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadFileTool_SandboxPathTranslation(t *testing.T) {
	// Test that DownloadFile correctly translates sandbox paths to host paths.
	// This mirrors the logic in Execute() and the MCP feishu_download_file tool.
	root := t.TempDir()
	hostWorkspace := filepath.Join(root, "users", "ou_test", "workspace")
	sandboxDir := filepath.Join(root, "workspace") // container-visible /workspace
	if err := os.MkdirAll(hostWorkspace, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sandboxDir, 0755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		ctx           *ToolContext
		outputPath    string
		wantHostIn    string // hostPath should be under this root
		wantDisplayIn string // displayPath should be under this root
	}{
		{
			name: "sandbox mode: relative path writes to host, displays sandbox path",
			ctx: &ToolContext{
				WorkspaceRoot:  hostWorkspace,
				SandboxWorkDir: sandboxDir,
				SandboxEnabled: true,
				Channel:        "feishu",
			},
			outputPath:    "downloads/report.pdf",
			wantHostIn:    hostWorkspace, // hostPath under hostWorkspace
			wantDisplayIn: sandboxDir,    // displayPath under sandboxDir
		},
		{
			name: "non-sandbox mode: writes directly, no translation",
			ctx: &ToolContext{
				WorkspaceRoot:  hostWorkspace,
				SandboxEnabled: false,
				Channel:        "feishu",
			},
			outputPath:    "downloads/report.pdf",
			wantHostIn:    hostWorkspace, // hostPath = outputPath, under hostWorkspace
			wantDisplayIn: hostWorkspace, // displayPath = outputPath, under hostWorkspace
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the path translation logic from DownloadFile.Execute()
			outputPath, err := ResolveWritePath(tt.ctx, tt.outputPath)
			if err != nil {
				t.Fatalf("ResolveWritePath failed: %v", err)
			}

			hostPath := SandboxToHostPath(tt.ctx, outputPath)
			displayPath := HostToSandboxPath(tt.ctx, hostPath)

			if !isWithinRoot(hostPath, tt.wantHostIn) {
				t.Errorf("hostPath = %s, want under %s", hostPath, tt.wantHostIn)
			}
			if !isWithinRoot(displayPath, tt.wantDisplayIn) {
				t.Errorf("displayPath = %s, want under %s", displayPath, tt.wantDisplayIn)
			}

			// Verify round-trip: sandbox → host → sandbox gives consistent display
			if tt.ctx.SandboxEnabled {
				if displayPath != outputPath {
					t.Errorf("displayPath round-trip mismatch: outputPath=%s, displayPath=%s", outputPath, displayPath)
				}
			}
		})
	}
}

func TestDownloadFileTool_ParameterValidation(t *testing.T) {
	tool := NewDownloadFileTool("", "")

	tests := []struct {
		name    string
		input   map[string]string
		wantErr bool
		errSub  string
	}{
		{
			name:    "missing message_id",
			input:   map[string]string{"file_key": "fk", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "message_id is required",
		},
		{
			name:    "missing file_key",
			input:   map[string]string{"message_id": "om_123", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "file_key is required",
		},
		{
			name:    "missing output_path",
			input:   map[string]string{"message_id": "om_123", "file_key": "fk"},
			wantErr: true,
			errSub:  "output_path is required",
		},
		{
			name:    "invalid message_id chars",
			input:   map[string]string{"message_id": "om_123/bad", "file_key": "fk", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "invalid message_id",
		},
		{
			name:    "invalid file_key chars",
			input:   map[string]string{"message_id": "om_123", "file_key": "fk/bad", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "invalid file_key",
		},
		{
			name:    "valid params but no feishu channel",
			input:   map[string]string{"message_id": "om_123", "file_key": "file_v3_abc", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "not supported for channel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, _ := json.Marshal(tt.input)
			root := t.TempDir()
			ctx := &ToolContext{
				WorkspaceRoot: root,
				Channel:       "qq", // non-feishu channel
			}
			_, err := tool.Execute(ctx, string(inputJSON))
			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errSub != "" {
				if !contains(err.Error(), tt.errSub) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errSub)
				}
			}
		})
	}
}

func TestSandboxToHostPath_DownloadFile(t *testing.T) {
	root := t.TempDir()
	hostWorkspace := filepath.Join(root, "users", "ou_xxx", "workspace")
	sandboxDir := filepath.Join(root, "workspace") // simulate container /workspace

	tests := []struct {
		name     string
		ctx      *ToolContext
		input    string
		wantHost string
		wantSans string
	}{
		{
			name: "sandbox path converts to host",
			ctx: &ToolContext{
				WorkspaceRoot:  hostWorkspace,
				SandboxWorkDir: sandboxDir,
				SandboxEnabled: true,
			},
			input:    filepath.Join(sandboxDir, "downloads", "report.pdf"),
			wantHost: filepath.Join(hostWorkspace, "downloads", "report.pdf"),
			wantSans: filepath.Join(sandboxDir, "downloads", "report.pdf"),
		},
		{
			name: "host path converts to sandbox",
			ctx: &ToolContext{
				WorkspaceRoot:  hostWorkspace,
				SandboxWorkDir: sandboxDir,
				SandboxEnabled: true,
			},
			input:    filepath.Join(hostWorkspace, "notes", "todo.txt"),
			wantHost: filepath.Join(hostWorkspace, "notes", "todo.txt"),
			wantSans: filepath.Join(sandboxDir, "notes", "todo.txt"),
		},
		{
			name: "non-sandbox: no translation",
			ctx: &ToolContext{
				WorkspaceRoot:  hostWorkspace,
				SandboxEnabled: false,
			},
			input:    filepath.Join(hostWorkspace, "file.txt"),
			wantHost: filepath.Join(hostWorkspace, "file.txt"),
			wantSans: filepath.Join(hostWorkspace, "file.txt"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hostPath := SandboxToHostPath(tt.ctx, tt.input)
			if hostPath != tt.wantHost {
				t.Errorf("SandboxToHostPath(%s) = %s, want %s", tt.input, hostPath, tt.wantHost)
			}

			sansPath := HostToSandboxPath(tt.ctx, hostPath)
			if sansPath != tt.wantSans {
				t.Errorf("HostToSandboxPath(%s) = %s, want %s", hostPath, sansPath, tt.wantSans)
			}
		})
	}
}
