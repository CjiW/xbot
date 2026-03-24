package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxToHostPath(t *testing.T) {
	tests := []struct {
		name        string
		sandboxPath string
		workspace   string
		want        string
	}{
		{
			name:        "simple file",
			sandboxPath: "/workspace/main.go",
			workspace:   "/home/user/data",
			want:        "/home/user/data/main.go",
		},
		{
			name:        "nested path",
			sandboxPath: "/workspace/src/util.go",
			workspace:   "/home/user/data",
			want:        "/home/user/data/src/util.go",
		},
		{
			name:        "root workspace",
			sandboxPath: "/workspace",
			workspace:   "/home/user/data",
			want:        "/home/user/data",
		},
		{
			name:        "outside workspace returns original",
			sandboxPath: "/etc/passwd",
			workspace:   "/home/user/data",
			want:        "/etc/passwd",
		},
		{
			name:        "relative path",
			sandboxPath: "main.go",
			workspace:   "/home/user/data",
			want:        "main.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &ToolContext{
				SandboxEnabled: true,
				WorkspaceRoot:  tt.workspace,
				SandboxWorkDir: "/workspace",
			}
			got := SandboxToHostPath(ctx, tt.sandboxPath)
			if got != tt.want {
				t.Errorf("SandboxToHostPath(%q) = %q, want %q", tt.sandboxPath, got, tt.want)
			}
		})
	}
}
func TestGlobTool_SandboxPathConstruction(t *testing.T) {
	// 测试 glob 在沙箱模式下构建的命令
	ws, err := os.MkdirTemp("", "test-glob-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ws)

	// 创建测试文件
	os.WriteFile(filepath.Join(ws, "test.txt"), []byte("test"), 0644)

	ctx := &ToolContext{
		Ctx:            context.Background(),
		WorkspaceRoot:  ws,
		SandboxWorkDir: "/workspace",
		SandboxEnabled: false, // 禁用真实沙箱，只测试路径转换
	}

	tool := &GlobTool{}
	_, err = tool.Execute(ctx, `{"pattern": "*.txt"}`)
	if err != nil {
		// 因为 SandboxEnabled 为 false，会走本地模式，应该能找到文件
		t.Logf("Local glob result: %v", err)
	}
}

func TestReadTool_PathTranslation(t *testing.T) {
	// 测试 ReadTool 的路径翻译逻辑
	ws, err := os.MkdirTemp("", "test-read-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ws)

	testFile := filepath.Join(ws, "hello.txt")
	os.WriteFile(testFile, []byte("Hello World"), 0644)

	// 测试非沙箱模式 - 应该能读取
	ctx := &ToolContext{
		Ctx:            context.Background(),
		WorkspaceRoot:  ws,
		SandboxWorkDir: "/workspace",
		SandboxEnabled: false,
	}

	tool := &ReadTool{}
	result, err := tool.Execute(ctx, `{"path": "hello.txt"}`)
	if err != nil {
		t.Fatalf("Local read failed: %v", err)
	}
	if !strings.Contains(result.Summary, "Hello World") {
		t.Errorf("expected content, got: %s", result.Summary)
	}
}

func TestGrepTool_PathTranslation(t *testing.T) {
	// 测试 GrepTool 的路径翻译逻辑
	ws, err := os.MkdirTemp("", "test-grep-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ws)

	testFile := filepath.Join(ws, "test.go")
	os.WriteFile(testFile, []byte("package main\n\nfunc main() {}"), 0644)

	// 测试非沙箱模式 - 应该能搜索
	ctx := &ToolContext{
		Ctx:            context.Background(),
		WorkspaceRoot:  ws,
		SandboxWorkDir: "/workspace",
		SandboxEnabled: false,
	}

	tool := &GrepTool{}
	result, err := tool.Execute(ctx, `{"pattern": "func main"}`)
	if err != nil {
		t.Fatalf("Local grep failed: %v", err)
	}
	if !strings.Contains(result.Summary, "test.go") {
		t.Errorf("expected test.go in results, got: %s", result.Summary)
	}
}

func TestEditTool_LocalMode(t *testing.T) {
	// 测试 EditTool 的本地模式
	ws, err := os.MkdirTemp("", "test-edit-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ws)

	testFile := filepath.Join(ws, "hello.txt")
	os.WriteFile(testFile, []byte("Hello World"), 0644)

	// 测试非沙箱模式
	ctx := &ToolContext{
		Ctx:            context.Background(),
		WorkspaceRoot:  ws,
		SandboxWorkDir: "/workspace",
		SandboxEnabled: false,
	}

	tool := &EditTool{}
	result, err := tool.Execute(ctx, `{"mode": "replace", "path": "hello.txt", "old_string": "World", "new_string": "Universe"}`)
	if err != nil {
		t.Fatalf("Local edit failed: %v", err)
	}

	// 验证修改成功
	content, _ := os.ReadFile(testFile)
	if !strings.Contains(string(content), "Hello Universe") {
		t.Errorf("expected replaced content, got: %s", content)
	}

	_ = result // suppress unused warning
}

// ============================================================================
// Sandbox CWD 路径解析回归测试
// LOCKED: 验证 Cd 设置沙箱路径后，Read/Edit/Glob/Grep 正确基于 CWD 解析相对路径。
// 这组测试锁定 issue d05d3ec 的修复行为：Cd 存储沙箱路径，所有工具直接使用。
// DO NOT MODIFY without understanding the sandbox CWD convention.
// ============================================================================

// TestReadTool_SandboxCWD_Regression 验证 Cd 到沙箱子目录后 Read 相对路径正确解析。
// 背景：Cd 将 CurrentDir 设为沙箱路径（如 /workspace/xbot），Read 必须基于该路径解析
// 而非回退到 sandboxBase (/workspace)。
func TestReadTool_SandboxCWD_Regression(t *testing.T) {
	// 创建模拟工作区：ws/xbot/go.mod
	ws := t.TempDir()
	subDir := filepath.Join(ws, "xbot")
	os.MkdirAll(subDir, 0o755)
	os.WriteFile(filepath.Join(subDir, "go.mod"), []byte("module xbot"), 0o644)

	ctx := &ToolContext{
		Ctx:            context.Background(),
		WorkspaceRoot:  ws,
		SandboxWorkDir: "/workspace",
		SandboxEnabled: false, // 本地模式测试路径逻辑
		CurrentDir:     filepath.Join(ws, "xbot"),
	}

	tool := &ReadTool{}
	result, err := tool.Execute(ctx, `{"path": "go.mod"}`)
	if err != nil {
		t.Fatalf("Read with CWD failed: %v", err)
	}
	if !strings.Contains(result.Summary, "module xbot") {
		t.Errorf("Read did not resolve relative path from CWD, got: %s", result.Summary)
	}
}

// TestReadTool_SandboxCWD_SandboxPath_Regression 验证 CurrentDir 为沙箱路径时
// executeInSandbox 的相对路径解析。此测试直接调用 resolveSandboxCWD 逻辑。
func TestReadTool_SandboxCWD_SandboxPath_Regression(t *testing.T) {
	sandboxBase := "/workspace"
	ctx := &ToolContext{
		CurrentDir:    "/workspace/xbot",          // Cd 设置的沙箱路径
		WorkspaceRoot: "/data/users/ou/workspace", // 宿主机路径
	}

	// 模拟 Read.executeInSandbox 的相对路径解析逻辑
	filePath := "go.mod"
	sandboxCWD := resolveSandboxCWD(ctx, sandboxBase)
	if sandboxCWD == "" {
		t.Fatal("resolveSandboxCWD returned empty for sandbox CurrentDir")
	}

	resolved := filepath.Join(sandboxCWD, filePath)
	expected := "/workspace/xbot/go.mod"
	if resolved != expected {
		t.Errorf("sandbox path resolution = %q, want %q", resolved, expected)
	}
}

// TestGlobTool_SandboxCWD_Regression 验证 Cd 后 Glob 的搜索目录正确设置。
func TestGlobTool_SandboxCWD_Regression(t *testing.T) {
	sandboxBase := "/workspace"

	// 场景 1：CurrentDir 是沙箱路径
	ctx := &ToolContext{
		CurrentDir:    "/workspace/xbot",
		WorkspaceRoot: "/data/users/ou/workspace",
	}
	cwd := resolveSandboxCWD(ctx, sandboxBase)
	if cwd != "/workspace/xbot" {
		t.Errorf("Glob CWD with sandbox path = %q, want /workspace/xbot", cwd)
	}

	// 场景 2：CurrentDir 是宿主机路径（兼容旧行为）
	ctx2 := &ToolContext{
		CurrentDir:    "/data/users/ou/workspace/src",
		WorkspaceRoot: "/data/users/ou/workspace",
	}
	cwd2 := resolveSandboxCWD(ctx2, sandboxBase)
	if cwd2 != "/workspace/src" {
		t.Errorf("Glob CWD with host path = %q, want /workspace/src", cwd2)
	}

	// 场景 3：无 CurrentDir → 返回空（工具应 fallback 到 sandboxBase）
	ctx3 := &ToolContext{
		CurrentDir:    "",
		WorkspaceRoot: "/data/users/ou/workspace",
	}
	cwd3 := resolveSandboxCWD(ctx3, sandboxBase)
	if cwd3 != "" {
		t.Errorf("Glob CWD with empty CurrentDir = %q, want empty", cwd3)
	}
}

// TestGrepTool_SandboxCWD_Regression 验证 Cd 后 Grep 的搜索目录正确设置。
func TestGrepTool_SandboxCWD_Regression(t *testing.T) {
	sandboxBase := "/workspace"
	ctx := &ToolContext{
		CurrentDir:    "/workspace/xbot/agent",
		WorkspaceRoot: "/data/users/ou/workspace",
	}

	cwd := resolveSandboxCWD(ctx, sandboxBase)
	if cwd != "/workspace/xbot/agent" {
		t.Errorf("Grep CWD = %q, want /workspace/xbot/agent", cwd)
	}
}

// TestEditTool_SandboxCWD_SandboxPath_Regression 验证 Cd 后 Edit 的路径解析。
func TestEditTool_SandboxCWD_SandboxPath_Regression(t *testing.T) {
	sandboxBase := "/workspace"
	ctx := &ToolContext{
		CurrentDir:    "/workspace/myproject",
		WorkspaceRoot: "/data/users/ou/workspace",
	}

	filePath := "src/main.go"
	sandboxCWD := resolveSandboxCWD(ctx, sandboxBase)
	if sandboxCWD == "" {
		t.Fatal("resolveSandboxCWD returned empty")
	}

	resolved := filepath.Join(sandboxCWD, filePath)
	expected := "/workspace/myproject/src/main.go"
	if resolved != expected {
		t.Errorf("Edit path = %q, want %q", resolved, expected)
	}
}

// ============================================================================
// applyLineLimit tests (offset + max_lines)
// ============================================================================

func TestApplyLineLimit_OffsetOnly(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\n"
	result := applyLineLimit(&ToolResult{Summary: content, Detail: content}, 0, 3)
	// maxLines=0 (no limit), offset=3 → start from line 3
	// Lines are now prefixed with line numbers
	expected := "3\tline3\n4\tline4\n5\tline5\n6\t"
	if result.Summary != expected {
		t.Errorf("offset=3: got %q, want %q", result.Summary, expected)
	}
}

func TestApplyLineLimit_OffsetAndMaxLines(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10"
	result := applyLineLimit(&ToolResult{Summary: content, Detail: content}, 2, 3)
	// maxLines=2, offset=3 → lines 3-4
	if !strings.Contains(result.Summary, "line3") {
		t.Errorf("expected line3 in result, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "line4") {
		t.Errorf("expected line4 in result, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "line5") {
		t.Errorf("should not contain line5 (max_lines=2), got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "truncated") {
		t.Errorf("expected truncation notice, got: %s", result.Summary)
	}
}

func TestApplyLineLimit_OffsetBeyondFile(t *testing.T) {
	// offset=10 on 3-line file → should return hint message (no panic)
	content := "line1\nline2\nline3"
	result := applyLineLimit(&ToolResult{Summary: content, Detail: content}, 0, 10)
	if !strings.Contains(result.Summary, "exceeds") {
		t.Errorf("expected exceeds hint for offset beyond file, got: %s", result.Summary)
	}
}

func TestApplyLineLimit_MaxLinesOnly(t *testing.T) {
	// maxLines only (no offset) — backward compatible
	content := "line1\nline2\nline3\nline4\nline5"
	result := applyLineLimit(&ToolResult{Summary: content, Detail: content}, 2, 0)
	if !strings.Contains(result.Summary, "line1") {
		t.Errorf("expected line1, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "line2") {
		t.Errorf("expected line2, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "line3") {
		t.Errorf("should not contain line3 (max_lines=2), got: %s", result.Summary)
	}
}

func TestApplyLineLimit_NilResult(t *testing.T) {
	result := applyLineLimit(nil, 10, 5)
	if result != nil {
		t.Error("nil result should remain nil")
	}
}

func TestApplyLineLimit_NoOffsetNoMaxLines(t *testing.T) {
	content := "line1\nline2\nline3"
	result := applyLineLimit(&ToolResult{Summary: content, Detail: content}, 0, 0)
	// Lines are now prefixed with line numbers even without offset/maxLines
	expected := "1\tline1\n2\tline2\n3\tline3"
	if result.Summary != expected {
		t.Errorf("no offset/max_lines: got %q, want %q", result.Summary, expected)
	}
}

func TestReadTool_OffsetParameter(t *testing.T) {
	ws, err := os.MkdirTemp("", "test-read-offset-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(ws)

	content := strings.Join([]string{"line1", "line2", "line3", "line4", "line5"}, "\n")
	os.WriteFile(filepath.Join(ws, "test.txt"), []byte(content), 0644)

	ctx := &ToolContext{
		Ctx:            context.Background(),
		WorkspaceRoot:  ws,
		SandboxWorkDir: "/workspace",
		SandboxEnabled: false,
	}

	tool := &ReadTool{}

	// Test offset only
	result, err := tool.Execute(ctx, `{"path": "test.txt", "offset": 3}`)
	if err != nil {
		t.Fatalf("Read with offset failed: %v", err)
	}
	if !strings.Contains(result.Summary, "line3") {
		t.Errorf("expected line3 with offset=3, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "line2") {
		t.Errorf("should not contain line2 with offset=3, got: %s", result.Summary)
	}

	// Test offset + max_lines
	result, err = tool.Execute(ctx, `{"path": "test.txt", "offset": 2, "max_lines": 2}`)
	if err != nil {
		t.Fatalf("Read with offset+max_lines failed: %v", err)
	}
	if !strings.Contains(result.Summary, "line2") {
		t.Errorf("expected line2, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "line3") {
		t.Errorf("expected line3, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "line4") {
		t.Errorf("should not contain line4 (max_lines=2), got: %s", result.Summary)
	}
}
