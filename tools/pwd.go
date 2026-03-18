package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
)

// PWDTool 查询或切换当前工作目录
type PWDTool struct{}

func (t *PWDTool) Name() string { return "PWD" }

func (t *PWDTool) Description() string {
	return `Get or change the current working directory (PWD).

When called without parameters:
- Returns current PWD and workspace root
- Use this to understand where you are in the filesystem

When called with a path parameter:
- Changes the current working directory
- Path can be absolute or relative to current PWD
- Cannot escape workspace root

Parameters (JSON):
  - path: string (optional), the directory to change to
    - Absolute path: "/workspace/project"
    - Relative path: "subdir", "..", "../other"
    - Use "/" to return to workspace root

Example: {"path": "src/components"}
Example: {}  // Just query current directory`
}

func (t *PWDTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "path",
			Type:        "string",
			Description: "Directory to change to (optional). Omit to just query current PWD.",
			Required:    false,
		},
	}
}

func (t *PWDTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	// 确定 workspace root
	workspaceRoot := toolCtx.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = toolCtx.WorkingDir
	}

	// 确定当前目录
	currentDir := toolCtx.CurrentDir
	if currentDir == "" {
		currentDir = workspaceRoot
	}

	// 无参数：查询当前目录
	if params.Path == "" {
		return NewResult(fmt.Sprintf(
			"Current directory: %s\nWorkspace root: %s",
			currentDir, workspaceRoot,
		)), nil
	}

	// 有参数：切换目录
	newPath := params.Path

	// 特殊处理："/" 返回 workspace root
	if newPath == "/" {
		if toolCtx.SetCurrentDir != nil {
			toolCtx.SetCurrentDir(workspaceRoot)
		}
		return NewResult(fmt.Sprintf(
			"Changed to workspace root: %s",
			workspaceRoot,
		)), nil
	}

	// 计算目标路径
	var targetDir string
	if filepath.IsAbs(newPath) {
		targetDir = filepath.Clean(newPath)
	} else {
		targetDir = filepath.Clean(filepath.Join(currentDir, newPath))
	}

	// 安全检查：必须在 workspace 内
	rel, err := filepath.Rel(workspaceRoot, targetDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		return nil, fmt.Errorf("cannot access path outside workspace: %s", newPath)
	}

	// 检查目录是否存在
	info, err := os.Stat(targetDir)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("directory does not exist: %s", targetDir)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to access directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", targetDir)
	}

	// 更新 session cwd
	if toolCtx.SetCurrentDir != nil {
		toolCtx.SetCurrentDir(targetDir)
	}

	return NewResult(fmt.Sprintf(
		"Changed directory to: %s\nWorkspace root: %s",
		targetDir, workspaceRoot,
	)), nil
}
