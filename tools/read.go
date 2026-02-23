package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// 解析路径：如果是相对路径，则基于工作目录
	filePath := params.Path
	if !filepath.IsAbs(filePath) && ctx != nil && ctx.WorkingDir != "" {
		filePath = filepath.Join(ctx.WorkingDir, filePath)
	}

	// Read file
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return NewResult(string(content)), nil
}
