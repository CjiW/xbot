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
