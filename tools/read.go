package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"xbot/llm"
)

// DefaultMaxReadLines is the default maximum lines returned by the Read tool.
// Users can override via the max_lines parameter.
const DefaultMaxReadLines = 500

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
		{Name: "max_lines", Type: "integer", Description: "Maximum lines to return (default 500, 0 = no limit)"},
	}
}

func (t *ReadTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path     string `json:"path"`
		MaxLines int    `json:"max_lines"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// 沙箱模式：在容器内执行 cat 命令
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
		result, err := t.executeInSandbox(ctx, params.Path)
		if err != nil {
			return nil, err
		}
		return applyLineLimit(result, params.MaxLines), nil
	}

	// 非沙箱模式：本地读取
	result, err := t.executeLocal(ctx, params.Path)
	if err != nil {
		return nil, err
	}
	return applyLineLimit(result, params.MaxLines), nil
}

// applyLineLimit truncates the tool result to maxLines lines.
func applyLineLimit(result *ToolResult, maxLines int) *ToolResult {
	if result == nil {
		return result
	}
	if maxLines <= 0 {
		maxLines = DefaultMaxReadLines
	}
	lines := strings.Split(result.Summary, "\n")
	if len(lines) <= maxLines {
		return result
	}
	result.Summary = strings.Join(lines[:maxLines], "\n") +
		fmt.Sprintf("\n\n... [truncated: showing %d of %d lines, use max_lines parameter to see more]", maxLines, len(lines))
	result.Detail = result.Summary
	return result
}

// executeInSandbox 在沙箱容器内执行 cat 命令
func (t *ReadTool) executeInSandbox(ctx *ToolContext, filePath string) (*ToolResult, error) {
	sandboxBase := sandboxBaseDir(ctx)

	// 将用户输入的路径转换为容器内路径
	sandboxPath := filePath
	if !strings.HasPrefix(filePath, sandboxBase+"/") && filePath != sandboxBase && !strings.HasPrefix(filePath, "/") {
		// 相对路径：优先基于 CurrentDir（Cd 后的沙箱路径），否则 sandboxBase
		if sandboxCWD := resolveSandboxCWD(ctx, sandboxBase); sandboxCWD != "" {
			sandboxPath = filepath.Join(sandboxCWD, filePath)
		} else {
			sandboxPath = sandboxBase + "/" + filePath
		}
	} else if strings.HasPrefix(filePath, sandboxBase+"/") || filePath == sandboxBase {
		sandboxPath = filePath
	} else if strings.HasPrefix(filePath, "/") {
		if ctx.WorkspaceRoot != "" {
			rel, err := filepath.Rel(ctx.WorkspaceRoot, filePath)
			if err == nil && !strings.HasPrefix(rel, "..") {
				sandboxPath = sandboxBase + "/" + rel
			}
		}
	}

	// 在容器内执行 cat
	cmd := fmt.Sprintf("cat '%s'", shellEscape(sandboxPath))
	output, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to read file in sandbox: %v, output: %s", err, output)
	}

	return NewResultWithTips(output, "如需修改此文件，优先使用 Edit 工具。"), nil
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
				return NewResultWithTips(string(content), "如需修改此文件，优先使用 Edit 工具。"), nil
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

	return NewResultWithTips(string(content), "如需修改此文件，优先使用 Edit 工具。"), nil
}
