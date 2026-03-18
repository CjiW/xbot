package tools

import (
	"path/filepath"
	"testing"
)

func TestResolveWritePath_EnforceWorkspace(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	ctx := &ToolContext{WorkspaceRoot: workspace}

	allowed, err := ResolveWritePath(ctx, "notes/todo.txt")
	if err != nil {
		t.Fatalf("expected relative path allowed, got err: %v", err)
	}
	if !isWithinRoot(allowed, workspace) {
		t.Fatalf("expected path under workspace, got: %s", allowed)
	}

	outside := filepath.Join(root, "outside.txt")
	if _, err := ResolveWritePath(ctx, outside); err == nil {
		t.Fatalf("expected write outside workspace to be denied")
	}
}

func TestResolveReadPath_AllowReadOnlyRoots(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	globalSkills := filepath.Join(root, "global-skills")

	ctx := &ToolContext{
		WorkspaceRoot: workspace,
		ReadOnlyRoots: []string{globalSkills},
	}

	workspaceFile := filepath.Join(workspace, "a.txt")
	got, err := ResolveReadPath(ctx, workspaceFile)
	if err != nil {
		t.Fatalf("expected workspace read allowed, got err: %v", err)
	}
	if got == "" {
		t.Fatalf("expected resolved workspace path")
	}

	globalFile := filepath.Join(globalSkills, "skill", "SKILL.md")
	got, err = ResolveReadPath(ctx, globalFile)
	if err != nil {
		t.Fatalf("expected readonly root read allowed, got err: %v", err)
	}
	if got == "" {
		t.Fatalf("expected resolved global path")
	}

	outside := filepath.Join(root, "other", "x.txt")
	if _, err := ResolveReadPath(ctx, outside); err == nil {
		t.Fatalf("expected read outside allowed roots to be denied")
	}
}

func TestUserPaths_SenderScoped(t *testing.T) {
	workDir := t.TempDir()
	u1 := UserWorkspaceRoot(workDir, "alice")
	u2 := UserWorkspaceRoot(workDir, "bob")
	if u1 == u2 {
		t.Fatalf("different sender should map to different workspace")
	}
	if filepath.Dir(UserMCPConfigPath(workDir, "alice")) == filepath.Dir(UserMCPConfigPath(workDir, "bob")) {
		t.Fatalf("different sender should map to different MCP config directory")
	}
}

func TestSandboxBaseDir(t *testing.T) {
	tests := []struct {
		name string
		ctx  *ToolContext
		want string
	}{
		{"nil ctx", nil, "/workspace"},
		{"empty SandboxWorkDir", &ToolContext{SandboxWorkDir: ""}, "/workspace"},
		{"custom SandboxWorkDir", &ToolContext{SandboxWorkDir: "/data/ws"}, "/data/ws"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sandboxBaseDir(tt.ctx)
			if got != tt.want {
				t.Errorf("sandboxBaseDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestShellEscape(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "hello"},
		{"hello world", "hello world"},
		{"it's", "it'\\''s"},
		{"\"", "\""},
		{"\\", "\\"},
		{"$HOME", "$HOME"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shellEscape(tt.input)
			if got != tt.want {
				t.Errorf("shellEscape(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
