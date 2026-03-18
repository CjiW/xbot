package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"xbot/llm"
)

// ReadTool 读取文件工具
type ReadTool struct{}

func (t *ReadTool) Name() string {
	return "Read"
}

func (t *ReadTool) Description() string {
	return `Read a file and return its content.
Parameters (JSON):
  - path: string, the file path to read (relative to working directory or absolute)
Example: {"path": "hello.txt"}`
}

func (t *ReadTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "The file path to read", Required: true},
	}
}

func (t *ReadTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path string `json:"path"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// 沙箱模式：在容器内执行 cat 命令
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
		return t.executeInSandbox(ctx, params.Path)
	}

	// 非沙箱模式：本地读取
	return t.executeLocal(ctx, params.Path)
}

// executeInSandbox 在沙箱容器内执行 cat 命令
func (t *ReadTool) executeInSandbox(ctx *ToolContext, filePath string) (*ToolResult, error) {
	// 将用户输入的路径转换为容器内路径
	sandboxPath := filePath
	if !strings.HasPrefix(filePath, "/workspace/") && !strings.HasPrefix(filePath, "/") {
		// 相对路径，优先使用 CurrentDir（PWD 工具优化）
		if ctx != nil && ctx.CurrentDir != "" && strings.HasPrefix(ctx.CurrentDir, ctx.WorkspaceRoot) {
			rel, err := filepath.Rel(ctx.WorkspaceRoot, ctx.CurrentDir)
			if err == nil {
				// 从 CurrentDir 解析相对路径
				sandboxPath = "/workspace/" + filepath.Join(rel, filePath)
			} else {
				// fallback 到 /workspace
				sandboxPath = "/workspace/" + filePath
			}
		} else {
			// 相对路径，假设相对于 /workspace
			sandboxPath = "/workspace/" + filePath
		}
	} else if strings.HasPrefix(filePath, "/workspace/") {
		sandboxPath = filePath
	} else if strings.HasPrefix(filePath, "/") {
		// 绝对路径，检查是否在 /workspace 内
		if ctx.WorkspaceRoot != "" {
			// 尝试转换为容器内路径
			rel, err := filepath.Rel(ctx.WorkspaceRoot, filePath)
			if err == nil && !strings.HasPrefix(rel, "..") {
				sandboxPath = "/workspace/" + rel
			}
		}
	}

	// 在容器内执行 cat
	cmd := fmt.Sprintf("cat '%s'", sandboxPath)
	output, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read file in sandbox: %v, output: %s", err, output)
	}

	return NewResult(output), nil
}

// executeLocal 在本地读取文件
func (t *ReadTool) executeLocal(ctx *ToolContext, filePath string) (*ToolResult, error) {
	// 如果是相对路径且 CurrentDir 存在，先尝试从 CurrentDir 解析。
	// 若在 CurrentDir 中未找到，有意 fallthrough 到 WorkspaceRoot 解析——
	// 这使得 agent cd 到子目录后仍能读取 workspace root 下的文件。
	if ctx != nil && ctx.CurrentDir != "" && !filepath.IsAbs(filePath) {
		absPath := filepath.Join(ctx.CurrentDir, filePath)
		if resolved, err := ResolveReadPath(ctx, absPath); err == nil {
			if _, statErr := os.Stat(resolved); statErr == nil {
				content, err := os.ReadFile(resolved)
				if err != nil {
					return nil, fmt.Errorf("failed to read file: %w", err)
				}
				return NewResult(string(content)), nil
			}
		}
	}

	resolvedPath, err := ResolveReadPath(ctx, filePath)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return NewResult(string(content)), nil
}
