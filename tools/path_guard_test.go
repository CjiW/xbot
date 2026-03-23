package tools

import (
	"os"
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
		{"nil ctx", nil, ""},
		{"empty SandboxWorkDir (none mode)", &ToolContext{SandboxWorkDir: ""}, ""},
		{"custom SandboxWorkDir", &ToolContext{SandboxWorkDir: "/data/ws"}, "/data/ws"},
		{"docker default", &ToolContext{SandboxWorkDir: "/workspace"}, "/workspace"},
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

func TestResolveReadPath_SandboxPathConversion(t *testing.T) {
	root := t.TempDir()
	sandboxDir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(sandboxDir, 0755); err != nil {
		t.Fatal(err)
	}

	ctx := &ToolContext{
		WorkspaceRoot:  filepath.Join(root, "host-workspace"),
		SandboxWorkDir: sandboxDir,
		SandboxEnabled: true,
	}

	// LLM sends sandbox path, should be accepted (no translation needed inside container)
	got, err := ResolveReadPath(ctx, filepath.Join(sandboxDir, "foo.txt"))
	if err != nil {
		t.Fatalf("expected sandbox path to be accepted, got err: %v", err)
	}
	if !isWithinRoot(got, sandboxDir) {
		t.Fatalf("expected resolved path under sandbox dir, got: %s", got)
	}

	// Outside path should be denied
	outside := filepath.Join(root, "other", "x.txt")
	if _, err := ResolveReadPath(ctx, outside); err == nil {
		t.Fatalf("expected read outside sandbox to be denied")
	}
}

func TestResolveWritePath_SandboxPathConversion(t *testing.T) {
	root := t.TempDir()
	sandboxDir := filepath.Join(root, "workspace")
	if err := os.MkdirAll(filepath.Join(sandboxDir, "notes"), 0755); err != nil {
		t.Fatal(err)
	}

	ctx := &ToolContext{
		WorkspaceRoot:  filepath.Join(root, "host-workspace"),
		SandboxWorkDir: sandboxDir,
		SandboxEnabled: true,
	}

	got, err := ResolveWritePath(ctx, filepath.Join(sandboxDir, "notes", "todo.txt"))
	if err != nil {
		t.Fatalf("expected sandbox path to be accepted, got err: %v", err)
	}
	if !isWithinRoot(got, sandboxDir) {
		t.Fatalf("expected resolved path under sandbox dir, got: %s", got)
	}
}

func TestSandboxToHostPath_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		ctx  *ToolContext
		in   string
		want string
	}{
		{
			name: "nil ctx returns input",
			ctx:  nil,
			in:   "/data/.xbot/users/ou_xxx/workspace/foo.go",
			want: "/data/.xbot/users/ou_xxx/workspace/foo.go",
		},
		{
			name: "host to sandbox path translation",
			ctx: &ToolContext{
				SandboxEnabled: true,
				SandboxWorkDir: "/workspace",
				WorkspaceRoot:  "/data/.xbot/users/ou_xxx/workspace",
			},
			in:   "/data/.xbot/users/ou_xxx/workspace/foo.go",
			want: "/workspace/foo.go",
		},
		{
			name: "host to sandbox nested path",
			ctx: &ToolContext{
				SandboxEnabled: true,
				SandboxWorkDir: "/workspace",
				WorkspaceRoot:  "/data/.xbot/users/ou_xxx/workspace",
			},
			in:   "/data/.xbot/users/ou_xxx/workspace/deep/nested/dir/file.txt",
			want: "/workspace/deep/nested/dir/file.txt",
		},
		{
			name: "host root only",
			ctx: &ToolContext{
				SandboxEnabled: true,
				SandboxWorkDir: "/workspace",
				WorkspaceRoot:  "/data/.xbot/users/ou_xxx/workspace",
			},
			in:   "/data/.xbot/users/ou_xxx/workspace",
			want: "/workspace",
		},
		{
			name: "outside workspace prefix returns input",
			ctx: &ToolContext{
				SandboxEnabled: true,
				SandboxWorkDir: "/workspace",
				WorkspaceRoot:  "/data/.xbot/users/ou_xxx/workspace",
			},
			in:   "/etc/passwd",
			want: "/etc/passwd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HostToSandboxPath(tt.ctx, tt.in)
			if got != tt.want {
				t.Errorf("HostToSandboxPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ============================================================================
// resolveSandboxCWD 回归测试
// LOCKED: 这些测试锁定 Cd→Read/Edit/Glob/Grep 路径解析的核心行为。
// 修改前请确保理解 sandbox 路径约定（Cd 存沙箱路径，工具直接使用）。
// DO NOT MODIFY without understanding the sandbox CWD convention.
// ============================================================================

func TestResolveSandboxCWD(t *testing.T) {
	sandboxBase := "/workspace"

	tests := []struct {
		name string
		ctx  *ToolContext
		want string
	}{
		{
			name: "nil ctx returns empty",
			ctx:  nil,
			want: "",
		},
		{
			name: "empty CurrentDir returns empty",
			ctx:  &ToolContext{CurrentDir: "", WorkspaceRoot: "/data/users/ou_xxx/workspace"},
			want: "",
		},
		{
			name: "sandbox path passed through directly",
			ctx:  &ToolContext{CurrentDir: "/workspace/xbot", WorkspaceRoot: "/data/users/ou_xxx/workspace"},
			want: "/workspace/xbot",
		},
		{
			name: "sandbox root passed through",
			ctx:  &ToolContext{CurrentDir: "/workspace", WorkspaceRoot: "/data/users/ou_xxx/workspace"},
			want: "/workspace",
		},
		{
			name: "host path converted to sandbox path",
			ctx:  &ToolContext{CurrentDir: "/data/users/ou_xxx/workspace/src", WorkspaceRoot: "/data/users/ou_xxx/workspace"},
			want: "/workspace/src",
		},
		{
			name: "host root converted to sandbox root",
			ctx:  &ToolContext{CurrentDir: "/data/users/ou_xxx/workspace", WorkspaceRoot: "/data/users/ou_xxx/workspace"},
			want: "/workspace",
		},
		{
			name: "unrecognized path returns empty",
			ctx:  &ToolContext{CurrentDir: "/some/random/path", WorkspaceRoot: "/data/users/ou_xxx/workspace"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSandboxCWD(tt.ctx, sandboxBase)
			if got != tt.want {
				t.Errorf("resolveSandboxCWD() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSandboxHostPathRoundTrip(t *testing.T) {
	ctx := &ToolContext{
		SandboxEnabled: true,
		SandboxWorkDir: "/workspace",
		WorkspaceRoot:  "/data/.xbot/users/ou_xxx/workspace",
	}

	paths := []string{
		"/workspace/foo.go",
		"/workspace/deep/nested/file.txt",
		"/workspace/readme.md",
	}

	for _, sandboxPath := range paths {
		hostPath := SandboxToHostPath(ctx, sandboxPath)
		roundTrip := HostToSandboxPath(ctx, hostPath)
		if roundTrip != sandboxPath {
			t.Errorf("round trip failed: %q → %q → %q", sandboxPath, hostPath, roundTrip)
		}
	}
}
