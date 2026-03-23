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
)

// EditTool 文件编辑工具
type EditTool struct{}

func (t *EditTool) Name() string {
	return "Edit"
}

func (t *EditTool) Description() string {
	return `Edit a file with various modes: create file, replace text, edit lines, or use regex.

Modes:
1. "create" - Create a new file with specified content
   Parameters: path, content
   
2. "replace" - Find and replace exact text
   Parameters: path, old_string, new_string, replace_all (optional, default false)
   
3. "line" - Edit specific lines
   Parameters: path, line_number, action ("insert_before", "insert_after", "replace", "delete"), content (for insert/replace)
   
4. "regex" - Replace using regular expression
   Parameters: path, pattern, replacement, replace_all (optional, default false)

5. "insert" - Insert content at specific position
   Parameters: path, position ("start", "end", or line number), content

Note: path can be relative to working directory or absolute.

Examples:
- Create file: {"mode": "create", "path": "hello.txt", "content": "Hello, World!"}
- Replace text: {"mode": "replace", "path": "main.go", "old_string": "foo", "new_string": "bar"}
- Replace all: {"mode": "replace", "path": "main.go", "old_string": "foo", "new_string": "bar", "replace_all": true}
- Insert line: {"mode": "line", "path": "main.go", "line_number": 10, "action": "insert_after", "content": "// new comment"}
- Delete line: {"mode": "line", "path": "main.go", "line_number": 5, "action": "delete"}
- Regex replace: {"mode": "regex", "path": "main.go", "pattern": "func\\s+(\\w+)", "replacement": "function $1"}
- Append to file: {"mode": "insert", "path": "log.txt", "position": "end", "content": "new log entry\n"}`
}

func (t *EditTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "The file path to edit or create", Required: true},
		{Name: "mode", Type: "string", Description: "Edit mode: create, replace, line, regex, or insert", Required: true},
		{Name: "content", Type: "string", Description: "Content for create/insert/replace operations", Required: false},
		{Name: "old_string", Type: "string", Description: "Text to find (for replace mode)", Required: false},
		{Name: "new_string", Type: "string", Description: "Replacement text (for replace mode)", Required: false},
		{Name: "line_number", Type: "integer", Description: "Line number to edit (for line mode, 1-based)", Required: false},
		{Name: "action", Type: "string", Description: "Line action: insert_before, insert_after, replace, delete", Required: false},
		{Name: "pattern", Type: "string", Description: "Regex pattern (for regex mode)", Required: false},
		{Name: "replacement", Type: "string", Description: "Replacement string (for regex mode)", Required: false},
		{Name: "position", Type: "string", Description: "Insert position: start, end, or line number (for insert mode)", Required: false},
		{Name: "replace_all", Type: "boolean", Description: "Replace all occurrences (default: false)", Required: false},
	}
}

// EditParams 编辑参数
type EditParams struct {
	Path        string `json:"path"`
	Mode        string `json:"mode"`
	OldString   string `json:"old_string"`
	NewString   string `json:"new_string"`
	LineNumber  int    `json:"line_number"`
	Action      string `json:"action"`
	Content     string `json:"content"`
	Pattern     string `json:"pattern"`
	Replacement string `json:"replacement"`
	Position    string `json:"position"`
	ReplaceAll  bool   `json:"replace_all"`
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

	// 根据不同 mode 检查必要字段非空
	switch params.Mode {
	case "create":
		if params.Path == "" {
			return nil, fmt.Errorf("path is required for create mode")
		}
	case "replace":
		if params.OldString == "" {
			return nil, fmt.Errorf("old_string is required for replace mode")
		}
	case "regex":
		if params.Pattern == "" {
			return nil, fmt.Errorf("pattern is required for regex mode")
		}
	case "line":
		if params.Action == "" {
			return nil, fmt.Errorf("action is required for line mode")
		}
	case "insert":
		if params.Position == "" {
			return nil, fmt.Errorf("position is required for insert mode")
		}
	}

	// 沙箱模式
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
		return t.executeInSandbox(ctx, params)
	}

	// 非沙箱模式
	return t.executeLocal(ctx, params)
}

// executeInSandbox 在沙箱内执行编辑操作
func (t *EditTool) executeInSandbox(ctx *ToolContext, params EditParams) (*ToolResult, error) {
	sandboxBase := sandboxBaseDir(ctx)

	// 将用户输入的路径转换为容器内路径
	sandboxPath := params.Path
	if !strings.HasPrefix(params.Path, sandboxBase+"/") && params.Path != sandboxBase && !strings.HasPrefix(params.Path, "/") {
		// 相对路径：优先基于 CurrentDir（Cd 后的沙箱路径），否则 sandboxBase
		if sandboxCWD := resolveSandboxCWD(ctx, sandboxBase); sandboxCWD != "" {
			sandboxPath = filepath.Join(sandboxCWD, params.Path)
		} else {
			sandboxPath = sandboxBase + "/" + params.Path
		}
	} else if strings.HasPrefix(params.Path, sandboxBase+"/") || params.Path == sandboxBase {
		sandboxPath = params.Path
	} else if strings.HasPrefix(params.Path, "/") {
		if ctx.WorkspaceRoot != "" {
			rel, err := filepath.Rel(ctx.WorkspaceRoot, params.Path)
			if err == nil && !strings.HasPrefix(rel, "..") {
				sandboxPath = sandboxBase + "/" + rel
			}
		}
	}

	// 根据不同模式执行
	switch params.Mode {
	case "create":
		return t.sandboxCreate(ctx, sandboxPath, params.Content)
	case "replace":
		return t.sandboxReplace(ctx, sandboxPath, params.OldString, params.NewString, params.ReplaceAll)
	case "line":
		return t.sandboxLineEdit(ctx, sandboxPath, params.LineNumber, params.Action, params.Content)
	case "regex":
		return t.sandboxRegexReplace(ctx, sandboxPath, params.Pattern, params.Replacement, params.ReplaceAll)
	case "insert":
		return t.sandboxInsert(ctx, sandboxPath, params.Position, params.Content)
	default:
		return nil, fmt.Errorf("unknown mode: %s (supported: create, replace, line, regex, insert)", params.Mode)
	}
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

func (t *EditTool) sandboxReplace(ctx *ToolContext, path, oldStr, newStr string, replaceAll bool) (*ToolResult, error) {
	// 读取文件内容（保留原始内容含 trailing newline）
	oldContent, err := sandboxReadFile(ctx, path)
	if err != nil {
		return nil, err
	}

	// 复用 doReplace 逻辑（纯 Go，无 shell 转义问题）
	params := EditParams{OldString: oldStr, NewString: newStr, ReplaceAll: replaceAll}
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

func (t *EditTool) sandboxLineEdit(ctx *ToolContext, path string, lineNum int, action, content string) (*ToolResult, error) {
	// 读取文件内容
	oldContent, err := sandboxReadFile(ctx, path)
	if err != nil {
		return nil, err
	}

	// 构造 EditParams 复用纯 Go 的 doLineEdit 逻辑
	params := EditParams{
		LineNumber: lineNum,
		Action:     action,
		Content:    content,
	}
	newContent, result, err := t.doLineEdit(oldContent, params)
	if err != nil {
		return nil, err
	}

	// 写回文件
	if err := sandboxWriteFile(ctx, path, newContent); err != nil {
		return nil, err
	}

	return &ToolResult{Summary: result, Tips: "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"}, nil
}

func (t *EditTool) sandboxRegexReplace(ctx *ToolContext, path, pattern, replacement string, replaceAll bool) (*ToolResult, error) {
	// 读取文件内容
	oldContent, err := sandboxReadFile(ctx, path)
	if err != nil {
		return nil, err
	}

	// 构造 EditParams 复用纯 Go 的 doRegexReplace 逻辑
	params := EditParams{
		Pattern:     pattern,
		Replacement: replacement,
		ReplaceAll:  replaceAll,
	}
	newContent, result, err := t.doRegexReplace(oldContent, params, path)
	if err != nil {
		return nil, err
	}

	// 写回文件
	if err := sandboxWriteFile(ctx, path, newContent); err != nil {
		return nil, err
	}

	return &ToolResult{Summary: result, Tips: "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"}, nil
}

func (t *EditTool) sandboxInsert(ctx *ToolContext, path, position, content string) (*ToolResult, error) {
	// 读取文件内容
	oldContent, err := sandboxReadFile(ctx, path)
	if err != nil {
		return nil, err
	}

	// 构造 EditParams 复用纯 Go 的 doInsert 逻辑
	params := EditParams{
		Position: position,
		Content:  content,
	}
	newContent, result, err := t.doInsert(oldContent, params, path)
	if err != nil {
		return nil, err
	}

	// 写回文件
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
	var newContent string
	var result string

	switch params.Mode {
	case "replace":
		newContent, result, err = t.doReplace(oldContent, params, filePath)
	case "line":
		newContent, result, err = t.doLineEdit(oldContent, params)
	case "regex":
		newContent, result, err = t.doRegexReplace(oldContent, params, filePath)
	case "insert":
		newContent, result, err = t.doInsert(oldContent, params, filePath)
	default:
		return nil, fmt.Errorf("unknown mode: %s (supported: create, replace, line, regex, insert)", params.Mode)
	}

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

// doReplace 执行文本替换
func (t *EditTool) doReplace(content string, params EditParams, filePath string) (string, string, error) {
	if params.OldString == "" {
		return "", "", fmt.Errorf("old_string is required for replace mode")
	}

	// 检查是否存在要替换的文本
	count := strings.Count(content, params.OldString)
	if count == 0 {
		return "", "", fmt.Errorf("text not found: %q", params.OldString)
	}

	var newContent string
	var replacedCount int

	if params.ReplaceAll {
		newContent = strings.ReplaceAll(content, params.OldString, params.NewString)
		replacedCount = count
	} else {
		newContent = strings.Replace(content, params.OldString, params.NewString, 1)
		replacedCount = 1
	}

	if count > 1 && !params.ReplaceAll {
		return newContent, fmt.Sprintf("Replaced 1 of %d occurrences. Use replace_all=true to replace all.", count), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced %d occurrence(s) in %s", replacedCount, filePath), nil
}

// doLineEdit 执行行编辑
func (t *EditTool) doLineEdit(content string, params EditParams) (string, string, error) {
	if params.LineNumber <= 0 {
		return "", "", fmt.Errorf("line_number must be positive (1-based)")
	}

	if params.Action == "" {
		return "", "", fmt.Errorf("action is required for line mode")
	}

	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// 验证行号
	if params.LineNumber > totalLines {
		return "", "", fmt.Errorf("line_number %d exceeds total lines %d", params.LineNumber, totalLines)
	}

	idx := params.LineNumber - 1 // 转换为 0-based 索引

	switch params.Action {
	case "insert_before":
		if params.Content == "" {
			return "", "", fmt.Errorf("content is required for insert_before action")
		}
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:idx]...)
		newLines = append(newLines, params.Content)
		newLines = append(newLines, lines[idx:]...)
		return strings.Join(newLines, "\n"), fmt.Sprintf("Inserted line before line %d", params.LineNumber), nil

	case "insert_after":
		if params.Content == "" {
			return "", "", fmt.Errorf("content is required for insert_after action")
		}
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:idx+1]...)
		newLines = append(newLines, params.Content)
		newLines = append(newLines, lines[idx+1:]...)
		return strings.Join(newLines, "\n"), fmt.Sprintf("Inserted line after line %d", params.LineNumber), nil

	case "replace":
		if params.Content == "" {
			return "", "", fmt.Errorf("content is required for replace action")
		}
		oldLine := lines[idx]
		lines[idx] = params.Content
		return strings.Join(lines, "\n"), fmt.Sprintf("Replaced line %d: %q -> %q", params.LineNumber, Truncate(oldLine, 50), Truncate(params.Content, 50)), nil

	case "delete":
		oldLine := lines[idx]
		newLines := make([]string, 0, len(lines)-1)
		newLines = append(newLines, lines[:idx]...)
		newLines = append(newLines, lines[idx+1:]...)
		result := strings.Join(newLines, "\n")
		// 修复 Bug2：保留尾部换行符。
		// strings.Split("hello\n", "\n") 产生 ["hello", ""]，删除最后一行（空串）后
		// Join 结果为 "hello"，丢失了原始的尾部 \n。此处检测并补回。
		if strings.HasSuffix(content, "\n") && !strings.HasSuffix(result, "\n") && len(result) > 0 {
			result += "\n"
		}
		return result, fmt.Sprintf("Deleted line %d: %q", params.LineNumber, Truncate(oldLine, 50)), nil

	default:
		return "", "", fmt.Errorf("unknown action: %s (supported: insert_before, insert_after, replace, delete)", params.Action)
	}
}

// doRegexReplace 执行正则替换
// SECURITY NOTE: Go's regexp package uses RE2 engine which guarantees O(n) time complexity
// for all operations, preventing ReDoS (Regular Expression Denial of Service) attacks.
// No explicit step limit is needed because RE2's design fundamentally avoids exponential
// backtracking found in PCRE/Perl-style engines.
func (t *EditTool) doRegexReplace(content string, params EditParams, filePath string) (string, string, error) {
	if params.Pattern == "" {
		return "", "", fmt.Errorf("pattern is required for regex mode")
	}

	re, err := regexp.Compile(params.Pattern)
	if err != nil {
		return "", "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	// 统计匹配数量
	matches := re.FindAllString(content, -1)
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no match found for pattern: %s", params.Pattern)
	}

	var newContent string
	var replacedCount int

	if params.ReplaceAll {
		newContent = re.ReplaceAllString(content, params.Replacement)
		replacedCount = len(matches)
	} else {
		newContent = re.ReplaceAllStringFunc(content, func(m string) string {
			if replacedCount == 0 {
				replacedCount++
				return re.ReplaceAllString(m, params.Replacement)
			}
			return m
		})
	}

	if len(matches) > 1 && !params.ReplaceAll {
		return newContent, fmt.Sprintf("Replaced 1 of %d matches. Use replace_all=true to replace all.", len(matches)), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced %d match(es) in %s", replacedCount, filePath), nil
}

// doInsert 执行插入操作
func (t *EditTool) doInsert(content string, params EditParams, filePath string) (string, string, error) {
	if params.Content == "" {
		return "", "", fmt.Errorf("content is required for insert mode")
	}

	if params.Position == "" {
		return "", "", fmt.Errorf("position is required for insert mode")
	}

	switch params.Position {
	case "start":
		return params.Content + content, fmt.Sprintf("Inserted content at the start of %s", filePath), nil

	case "end":
		// 确保末尾有换行符
		if len(content) > 0 && content[len(content)-1] != '\n' {
			content += "\n"
		}
		return content + params.Content, fmt.Sprintf("Inserted content at the end of %s", filePath), nil

	default:
		// 尝试解析为行号
		var lineNum int
		if _, err := fmt.Sscanf(params.Position, "%d", &lineNum); err != nil {
			return "", "", fmt.Errorf("invalid position: %s (use 'start', 'end', or a line number)", params.Position)
		}

		// 使用行编辑逻辑
		params.LineNumber = lineNum
		params.Action = "insert_after"
		return t.doLineEdit(content, params)
	}
}

// Truncate 截断字符串（公共函数，供多处使用）
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
