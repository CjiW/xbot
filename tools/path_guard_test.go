package tools

import (
	"os"
	"path/filepath"
	"strings"
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
		{"none sandbox", &ToolContext{Sandbox: &mockSandbox{name: "none", workspace: ""}}, ""},
		{"custom sandbox workspace", &ToolContext{Sandbox: &mockSandbox{name: "docker", workspace: "/data/ws"}}, "/data/ws"},
		{"docker default", &ToolContext{Sandbox: &mockSandbox{name: "docker", workspace: "/workspace"}}, "/workspace"},
		{"remote sandbox", &ToolContext{Sandbox: &mockSandbox{name: "remote", workspace: "/home/user/workspace"}}, "/home/user/workspace"},
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
		Sandbox:        &mockSandbox{name: "docker", workspace: sandboxDir},
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
		Sandbox:        &mockSandbox{name: "docker", workspace: sandboxDir},
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

// TestResolveWritePath_CurrentDir 验证 ResolveWritePath 在设置 CurrentDir 后，
// 相对路径基于 CurrentDir 解析，而非 WorkingDir。
func TestResolveWritePath_CurrentDir(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	ctx := &ToolContext{
		WorkingDir: tmpDir,
	}

	// 无 CurrentDir 时，相对路径基于 WorkingDir
	got, err := ResolveWritePath(ctx, "test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join(tmpDir, "test.txt") {
		t.Errorf("without CurrentDir: got %q, want %q", got, filepath.Join(tmpDir, "test.txt"))
	}

	// 设置 CurrentDir 后，相对路径基于 CurrentDir
	ctx.CurrentDir = subDir
	got, err = ResolveWritePath(ctx, "test.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join(subDir, "test.txt") {
		t.Errorf("with CurrentDir: got %q, want %q", got, filepath.Join(subDir, "test.txt"))
	}

	// 绝对路径不受 CurrentDir 影响
	absPath := filepath.Join(tmpDir, "abs.txt")
	got, err = ResolveWritePath(ctx, absPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != absPath {
		t.Errorf("absolute path: got %q, want %q", got, absPath)
	}
}

// TestResolveReadPath_CurrentDir 验证 ResolveReadPath 在设置 CurrentDir 后，
// 相对路径基于 CurrentDir 解析。
func TestResolveReadPath_CurrentDir(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	// 创建测试文件
	testFile := filepath.Join(subDir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := &ToolContext{
		WorkingDir: tmpDir,
	}

	// 无 CurrentDir 时，相对路径基于 WorkingDir
	got, err := ResolveReadPath(ctx, "hello.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != filepath.Join(tmpDir, "hello.txt") {
		t.Errorf("without CurrentDir: got %q, want %q", got, filepath.Join(tmpDir, "hello.txt"))
	}

	// 设置 CurrentDir 后，相对路径基于 CurrentDir
	ctx.CurrentDir = subDir
	got, err = ResolveReadPath(ctx, "hello.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testFile {
		t.Errorf("with CurrentDir: got %q, want %q", got, testFile)
	}

	// 绝对路径不受 CurrentDir 影响
	got, err = ResolveReadPath(ctx, testFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != testFile {
		t.Errorf("absolute path: got %q, want %q", got, testFile)
	}
}

// TestResolveWritePath_CurrentDir_EscapeWorkspace 验证 CurrentDir 在 workspace 外时，
// 写入操作被 isWithinRoot 正确拦截。
func TestResolveWritePath_CurrentDir_EscapeWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()

	ctx := &ToolContext{
		WorkspaceRoot: workspace,
		CurrentDir:    outsideDir,
	}

	// CurrentDir 在 workspace 外，相对路径写入应被拒绝
	_, err := ResolveWritePath(ctx, "evil.txt")
	if err == nil {
		t.Fatal("expected error when CurrentDir is outside workspace, got nil")
	}

	// 路径穿越也应被拒绝
	ctx.CurrentDir = filepath.Join(workspace, "sub")
	os.MkdirAll(ctx.CurrentDir, 0755)
	_, err = ResolveWritePath(ctx, "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal, got nil")
	}
}

// TestResolveReadPath_CurrentDir_EscapeWorkspace 验证 CurrentDir 在 workspace 外时，
// 读取操作被正确拦截。
func TestResolveReadPath_CurrentDir_EscapeWorkspace(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()

	ctx := &ToolContext{
		WorkspaceRoot: workspace,
		CurrentDir:    outsideDir,
	}

	// CurrentDir 在 workspace 外，相对路径读取应被拒绝
	_, err := ResolveReadPath(ctx, "secret.txt")
	if err == nil {
		t.Fatal("expected error when CurrentDir is outside workspace, got nil")
	}
}

// TestReadTool_Fallthrough_CurrentDir 验证 cd 到子目录后，
// 仍能通过 fallthrough 读取 workspace root 下的文件。
func TestReadTool_Fallthrough_CurrentDir(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 在 workspace root 创建文件（不在 subdir 中）
	rootFile := filepath.Join(tmpDir, "root.txt")
	if err := os.WriteFile(rootFile, []byte("root content"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{}
	ctx := &ToolContext{
		WorkingDir: tmpDir,
		CurrentDir: subDir,
	}

	// cd 到 subdir 后，读取 root.txt 应该 fallthrough 到 workspace root
	result, err := tool.Execute(ctx, `{"path": "root.txt"}`)
	if err != nil {
		t.Fatalf("expected fallthrough to workspace root, got error: %v", err)
	}
	if !strings.Contains(result.Summary, "root content") {
		t.Errorf("expected 'root content' in result, got: %s", result.Summary)
	}
}
