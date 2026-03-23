package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"xbot/llm"
)

// DefaultMaxReadLines: no default truncation — offload handles large results.
// Only applies when the user explicitly passes max_lines > 0.
const DefaultMaxReadLines = 0

// ReadTool 读取文件工具
type ReadTool struct{}

func (t *ReadTool) Name() string {
	return "Read"
}

func (t *ReadTool) Description() string {
	return `Read a file and return its content.
Parameters (JSON):
  - path: string, the file path to read (relative to working directory or absolute)
  - max_lines: number, maximum lines to return (0 or omit = no limit)
  - offset: number, start reading from this line number (1-based, 0 or omit = start from beginning)
Example: {"path": "hello.txt"}
Example: {"path": "hello.txt", "offset": 100, "max_lines": 50}`
}

func (t *ReadTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "The file path to read", Required: true},
		{Name: "max_lines", Type: "integer", Description: "Maximum lines to return (0 or omit = no limit)"},
		{Name: "offset", Type: "integer", Description: "Start reading from this line number (1-based, 0 or omit = start from beginning)"},
	}
}

func (t *ReadTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path     string `json:"path"`
		MaxLines int    `json:"max_lines"`
		Offset   int    `json:"offset"`
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
		return applyLineLimit(result, params.MaxLines, params.Offset), nil
	}

	// 非沙箱模式：本地读取
	result, err := t.executeLocal(ctx, params.Path)
	if err != nil {
		return nil, err
	}
	return applyLineLimit(result, params.MaxLines, params.Offset), nil
}

// applyLineLimit applies offset and maxLines to the tool result.
// offset is 1-based: offset=10 means skip the first 9 lines, start from line 10.
// Only applies when the respective parameter is > 0 (explicitly requested by user).
// Large results without explicit truncation are handled by the offload system.
func applyLineLimit(result *ToolResult, maxLines, offset int) *ToolResult {
	if result == nil {
		return result
	}
	if maxLines <= 0 && offset <= 0 {
		return result
	}
	lines := strings.Split(result.Summary, "\n")
	totalLines := len(lines)

	// Apply offset (1-based): offset=N means skip first N-1 lines
	if offset > 0 {
		// Convert to 0-based: if offset=10, we want lines[9:]
		startIdx := offset - 1
		if startIdx < 0 {
			startIdx = 0
		}
		if startIdx >= totalLines {
			// offset beyond file end — return empty with a hint
			result.Summary = fmt.Sprintf("(offset %d exceeds file length %d — file has no content from this line)", offset, totalLines)
			result.Detail = result.Summary
			return result
		}
		lines = lines[startIdx:]
	}

	// Apply maxLines truncation
	if maxLines > 0 && len(lines) > maxLines {
		result.Summary = strings.Join(lines[:maxLines], "\n") +
			fmt.Sprintf("\n\n... [truncated: showing %d of %d lines, use max_lines parameter to see more]", maxLines, totalLines)
	} else {
		result.Summary = strings.Join(lines, "\n")
	}
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
		sandboxCWD := resolveSandboxCWD(ctx, sandboxBase)
		if sandboxCWD != "" {
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
