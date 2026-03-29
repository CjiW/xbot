package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"xbot/llm"
	log "xbot/logger"
)

// EditTool 文件编辑工具
type EditTool struct{}

func (t *EditTool) Name() string {
	return "Edit"
}

func (t *EditTool) Description() string {
	return `Edit a file. Choose ONE mode and supply its required parameters.

Modes:
1. "create" — Create a new file.
   Required: path, content

2. "replace" — Find and replace text using RE2 regex.
   Required: path, old_string, new_string
   Optional: start_line, end_line (restrict search range, 1-based inclusive)
   Note: old_string is always treated as RE2 regex pattern. For literal text, escape special chars: . * + ? [ ] ( ) { } | ^ $ \
   new_string supports $1/$2 captures for regex groups.

⚠️ Common mistakes (avoid these!):
- replace mode uses old_string/new_string, NOT content.
- To replace literal "v1.0", escape the dot: "v1\\.0"
- start_line and end_line restrict the search range. They do NOT select lines for replacement.

Examples:
- {"mode": "create", "path": "hello.txt", "content": "Hello!"}
- {"mode": "replace", "path": "main.go", "old_string": "foo", "new_string": "bar"}
- {"mode": "replace", "path": "main.go", "old_string": "v\\d+\\.\\d+", "new_string": "v2.0"}
- {"mode": "replace", "path": "main.go", "old_string": "foo", "new_string": "bar", "start_line": 10, "end_line": 20}`
}

func (t *EditTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path (relative to working directory or absolute)", Required: true},
		{Name: "mode", Type: "string", Description: "Edit mode: create or replace", Required: true},
		{Name: "content", Type: "string", Description: "Content for create mode (NOT used by replace mode)", Required: false},
		{Name: "old_string", Type: "string", Description: "RE2 regex pattern to find (replace mode). For literal text, escape special chars: . * + ? [ ] ( ) { } | ^ $ \\", Required: false},
		{Name: "new_string", Type: "string", Description: "Text to replace old_string with (replace mode). Supports $1/$2 for regex captures.", Required: false},
		{Name: "start_line", Type: "integer", Description: "Restrict search from this line, 1-based inclusive (replace mode)", Required: false},
		{Name: "end_line", Type: "integer", Description: "Restrict search to this line, 1-based inclusive (replace mode)", Required: false},
	}
}

// EditParams 编辑参数
type EditParams struct {
	Path      string `json:"path"`
	Mode      string `json:"mode"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
	Content   string `json:"content"`
	StartLine int    `json:"start_line"` // Optional: restrict replace search start line (1-based, inclusive)
	EndLine   int    `json:"end_line"`   // Optional: restrict replace search end line (1-based, inclusive)
}

func (t *EditTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params EditParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	if params.Mode == "" {
		return nil, fmt.Errorf("mode is required")
	}

	// --- Auto-correct: LLM sometimes puts replacement content in "content" ---
	if params.Mode == "replace" && params.NewString == "" && params.Content != "" {
		params.NewString = params.Content
		params.Content = ""
		log.WithFields(log.Fields{
			"old_string_preview": Truncate(params.OldString, 80),
			"new_string_len":     len([]rune(params.NewString)),
		}).Info("Edit tool: auto-corrected content→new_string for replace mode")
	}

	// Validate parameters
	if err := t.validateParams(params); err != nil {
		return nil, err
	}

	// 沙箱模式
	if shouldUseSandbox(ctx) {
		return t.executeInSandbox(ctx, params)
	}

	// 非沙箱模式
	return t.executeLocal(ctx, params)
}

// validateParams checks for contradictory parameter combinations and returns clear error messages.
func (t *EditTool) validateParams(params EditParams) error {
	switch params.Mode {
	case "create":
		// No contradictions possible for create
	case "replace":
		if params.OldString == "" {
			return fmt.Errorf("old_string is required for replace mode")
		}
	default:
		return fmt.Errorf("unknown mode: %q (supported: create, replace)", params.Mode)
	}
	return nil
}

// executeInSandbox 在沙箱内执行编辑操作
func (t *EditTool) executeInSandbox(ctx *ToolContext, params EditParams) (*ToolResult, error) {
	sandboxPath := t.resolveSandboxPath(ctx, params.Path)

	switch params.Mode {
	case "create":
		return t.sandboxCreate(ctx, sandboxPath, params.Content)
	case "replace":
		return t.sandboxReplace(ctx, sandboxPath, params)
	default:
		return nil, fmt.Errorf("unknown mode: %q (supported: create, replace)", params.Mode)
	}
}

// resolveSandboxPath 将用户输入的路径转换为容器内路径
func (t *EditTool) resolveSandboxPath(ctx *ToolContext, userPath string) string {
	sandboxBase := sandboxBaseDir(ctx)

	if !strings.HasPrefix(userPath, sandboxBase+"/") && userPath != sandboxBase && !strings.HasPrefix(userPath, "/") {
		if sandboxCWD := resolveSandboxCWD(ctx, sandboxBase); sandboxCWD != "" {
			return filepath.Join(sandboxCWD, userPath)
		}
		return sandboxBase + "/" + userPath
	} else if strings.HasPrefix(userPath, sandboxBase+"/") || userPath == sandboxBase {
		return userPath
	} else if strings.HasPrefix(userPath, "/") {
		if ctx.WorkspaceRoot != "" {
			if rel, err := filepath.Rel(ctx.WorkspaceRoot, userPath); err == nil && !strings.HasPrefix(rel, "..") {
				return sandboxBase + "/" + rel
			}
		}
	}
	return userPath
}

// sandboxReadFile 通过 cat 读取沙箱内文件内容（保留原始内容，不做 TrimSpace）
func sandboxReadFile(ctx *ToolContext, path string) (string, error) {
	cmd := fmt.Sprintf("cat '%s'", strings.ReplaceAll(path, "'", "'\\''"))
	content, err := RunInSandboxRawWithShell(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %v", path, err)
	}
	return content, nil
}

// sandboxWriteFile 将内容 base64 编码后写入沙箱内文件（彻底避免 shell 转义问题）
func sandboxWriteFile(ctx *ToolContext, path, content string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	safePath := strings.ReplaceAll(path, "'", "'\\''")
	cmd := fmt.Sprintf("echo '%s' | base64 -d > '%s'", encoded, safePath)
	_, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to write file %s: %v", path, err)
	}
	return nil
}

// sandboxWriteNewFile 创建新文件并写入内容（含 mkdir -p），通过 base64 避免转义
func sandboxWriteNewFile(ctx *ToolContext, path, content string) error {
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	safePath := strings.ReplaceAll(path, "'", "'\\''")
	cmd := fmt.Sprintf("mkdir -p '%s' && echo '%s' | base64 -d > '%s'",
		strings.ReplaceAll(filepath.Dir(path), "'", "'\\''"), encoded, safePath)
	_, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %v", path, err)
	}
	return nil
}

func (t *EditTool) sandboxCreate(ctx *ToolContext, path, content string) (*ToolResult, error) {
	if err := sandboxWriteNewFile(ctx, path, content); err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("File created successfully: %s", path)
	return &ToolResult{Summary: summary, Tips: "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"}, nil
}

func (t *EditTool) sandboxReplace(ctx *ToolContext, path string, params EditParams) (*ToolResult, error) {
	// 读取文件内容（保留原始内容含 trailing newline）
	oldContent, err := sandboxReadFile(ctx, path)
	if err != nil {
		return nil, err
	}

	// 复用 doReplace 逻辑（纯 Go，无 shell 转义问题）
	newContent, result, err := t.doReplace(oldContent, params, path)
	if err != nil {
		return nil, err
	}

	// 写回文件（base64 编码，彻底避免 shell 转义）
	if err := sandboxWriteFile(ctx, path, newContent); err != nil {
		return nil, err
	}

	return &ToolResult{Summary: result, Tips: "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"}, nil
}

// executeLocal 在本地执行编辑操作（非沙箱模式）
func (t *EditTool) executeLocal(ctx *ToolContext, params EditParams) (*ToolResult, error) {
	filePath, err := ResolveWritePath(ctx, params.Path)
	if err != nil {
		return nil, err
	}

	// create 模式不需要读取现有文件
	if params.Mode == "create" {
		summary, err := t.doCreate(filePath, params)
		if err != nil {
			return nil, err
		}
		return &ToolResult{Summary: summary, Tips: "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"}, nil
	}

	// 读取文件内容
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	oldContent := string(content)
	newContent, result, err := t.doReplace(oldContent, params, filePath)
	if err != nil {
		return nil, err
	}

	// 写入文件
	if err := os.WriteFile(filePath, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &ToolResult{Summary: result, Tips: "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"}, nil
}

// doCreate 创建新文件
func (t *EditTool) doCreate(filePath string, params EditParams) (string, error) {
	// Create parent directories if they don't exist
	dir := filepath.Dir(filePath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("failed to create directory: %w", err)
		}
	}

	// Write file
	if err := os.WriteFile(filePath, []byte(params.Content), 0644); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return fmt.Sprintf("File created successfully: %s", filePath), nil
}

// splitContentByLineRange splits content by line range for replace operations.
// Returns prefix (before range), rangeText (within range), suffix (after range), and error.
// When start=0 and end=0, returns the entire content as rangeText (no line restriction).
func splitContentByLineRange(content string, start, end int) (string, string, string, error) {
	// Inline splitLines logic: split content and handle trailing newline
	lines := strings.Split(content, "\n")
	hasTrailingNL := len(lines) > 1 && lines[len(lines)-1] == ""
	if hasTrailingNL {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)

	// Handle default case: no line range specified
	if start == 0 && end == 0 {
		rangeText := content
		if hasTrailingNL && len(lines) > 0 {
			rangeText = strings.Join(lines, "\n")
		} else if len(lines) == 1 {
			rangeText = lines[0]
		}
		return "", rangeText, "", nil
	}

	if end > totalLines {
		return "", "", "", fmt.Errorf("end_line %d exceeds total lines %d", end, totalLines)
	}
	if start > end {
		return "", "", "", fmt.Errorf("start_line %d is greater than end_line %d", start, end)
	}

	startIdx := start - 1
	endIdx := end

	prefix := ""
	if startIdx > 0 {
		prefix = strings.Join(lines[:startIdx], "\n") + "\n"
	}

	rangeText := strings.Join(lines[startIdx:endIdx], "\n")

	suffix := ""
	if endIdx < totalLines {
		suffix = "\n" + strings.Join(lines[endIdx:], "\n")
	}
	if hasTrailingNL {
		suffix += "\n"
	}

	return prefix, rangeText, suffix, nil
}

// doReplace 执行文本替换（使用 RE2 正则匹配）
// SECURITY NOTE: Go's regexp package uses RE2 engine which guarantees O(n) time complexity
// for all operations, preventing ReDoS attacks.
func (t *EditTool) doReplace(content string, params EditParams, filePath string) (string, string, error) {
	if params.OldString == "" {
		return "", "", fmt.Errorf("old_string is required for replace mode")
	}

	// Split content by line range if start_line/end_line specified
	prefix, rangeText, suffix, err := splitContentByLineRange(content, params.StartLine, params.EndLine)
	if err != nil {
		return "", "", err
	}

	// 编译正则表达式
	re, err := regexp.Compile(params.OldString)
	if err != nil {
		return "", "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	// 查找匹配
	matches := re.FindAllString(rangeText, -1)
	if len(matches) == 0 {
		if params.StartLine > 0 || params.EndLine > 0 {
			effStart := params.StartLine
			if effStart <= 0 {
				effStart = 1
			}
			effEnd := params.EndLine
				if effEnd <= 0 {
					// Inline splitLines: count lines correctly handling trailing newline
					lines := strings.Split(content, "\n")
					if len(lines) > 0 && lines[len(lines)-1] == "" {
						effEnd = len(lines) - 1
					} else {
						effEnd = len(lines)
					}
				}
			return "", "", fmt.Errorf("no match found for pattern in lines %d-%d: %s", effStart, effEnd, params.OldString)
		}
		return "", "", fmt.Errorf("no match found for pattern: %s", params.OldString)
	}

	// 执行替换（始终替换第一个匹配）
	replacedCount := 0
	newRangeText := re.ReplaceAllStringFunc(rangeText, func(m string) string {
		if replacedCount == 0 {
			replacedCount++
			return re.ReplaceAllString(m, params.NewString)
		}
		return m
	})

	newContent := prefix + newRangeText + suffix

	if len(matches) > 1 {
		return newContent, fmt.Sprintf("Replaced 1 of %d matches for pattern: %s", len(matches), params.OldString), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced match in %s", filePath), nil
}

// Truncate 截断字符串（公共函数，供多处使用）
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
