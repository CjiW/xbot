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
	return `Edit a file. Choose ONE mode and supply its required parameters.

Modes:
1. "create" — Create a new file.
   Required: path, content

2. "replace" — Find and replace text (exact or regex).
   Required: path, old_string, new_string
   Optional: replace_all (default false), start_line, end_line, regex (default false)
   When regex=true, old_string is treated as RE2 pattern, new_string supports $1/$2 captures.

3. "line" — Edit specific line(s) by number.
   Required: path, line_number, action ("insert_before"|"insert_after"|"replace"|"delete")
   Required for insert/replace actions: content
   Optional: count (default 1, how many consecutive lines to replace or delete)
   Position "start"/"end" is also supported via action.

⚠️ Common mistakes (avoid these!):
- line mode: line_number is 1-based. delete only removes 1 line — set count to delete multiple.
- replace mode: uses old_string/new_string, NOT content. Regex mode uses pattern/replacement (deprecated, use regex flag).
- To replace ALL occurrences, you MUST set replace_all=true. Without it, only the first match is replaced.
- start_line and end_line restrict the search range (1-based, inclusive). They do NOT select lines for replacement.

Examples:
- {"mode": "create", "path": "hello.txt", "content": "Hello!"}
- {"mode": "replace", "path": "main.go", "old_string": "foo", "new_string": "bar"}
- {"mode": "replace", "path": "main.go", "old_string": "foo", "new_string": "bar", "replace_all": true, "start_line": 10, "end_line": 20}
- {"mode": "replace", "path": "main.go", "old_string": "v\\d+\\.\\d+", "new_string": "v2.0", "regex": true}
- {"mode": "line", "path": "main.go", "line_number": 10, "action": "insert_after", "content": "// comment"}
- {"mode": "line", "path": "main.go", "line_number": 5, "action": "delete"}
- {"mode": "line", "path": "main.go", "line_number": 3, "action": "delete", "count": 3}
- {"mode": "line", "path": "log.txt", "action": "insert", "position": "end", "content": "new entry\n"}`
}

func (t *EditTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path (relative to working directory or absolute)", Required: true},
		{Name: "mode", Type: "string", Description: "Edit mode: create, replace, or line", Required: true},
		{Name: "content", Type: "string", Description: "Content for create/line modes (NOT used by replace mode)", Required: false},
		{Name: "old_string", Type: "string", Description: "Exact text to find (replace mode). When regex=true, treated as RE2 pattern.", Required: false},
		{Name: "new_string", Type: "string", Description: "Text to replace old_string with (replace mode). Supports $1/$2 when regex=true.", Required: false},
		{Name: "line_number", Type: "integer", Description: "1-based line number (line mode only)", Required: false},
		{Name: "action", Type: "string", Description: "Line action: insert_before, insert_after, replace, delete (line mode only)", Required: false},
		{Name: "position", Type: "string", Description: "Insert position: start or end (line mode, alternative to line_number)", Required: false},
		{Name: "replace_all", Type: "boolean", Description: "Replace all occurrences, default false (replace mode)", Required: false},
		{Name: "regex", Type: "boolean", Description: "Use RE2 regex matching in replace mode, default false", Required: false},
		{Name: "count", Type: "integer", Description: "Number of consecutive lines to replace or delete, default 1 (line mode)", Required: false},
		{Name: "start_line", Type: "integer", Description: "Restrict search from this line, 1-based inclusive (replace mode)", Required: false},
		{Name: "end_line", Type: "integer", Description: "Restrict search to this line, 1-based inclusive (replace mode)", Required: false},
	}
}

// EditParams 编辑参数
type EditParams struct {
	Path       string `json:"path"`
	Mode       string `json:"mode"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	LineNumber int    `json:"line_number"`
	Action     string `json:"action"`
	Content    string `json:"content"`
	Position   string `json:"position"`
	ReplaceAll bool   `json:"replace_all"`
	Regex      bool   `json:"regex"`
	Count      int    `json:"count"`
	StartLine  int    `json:"start_line"` // Optional: restrict replace search start line (1-based, inclusive)
	EndLine    int    `json:"end_line"`   // Optional: restrict replace search end line (1-based, inclusive)
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

	// --- Backward compatibility: map deprecated modes ---
	switch params.Mode {
	case "regex":
		params.Mode = "replace"
		params.Regex = true
	case "insert":
		params.Mode = "line"
		params.Action = "insert"
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
		if params.LineNumber > 0 {
			return fmt.Errorf("line_number is not used in replace mode — use start_line/end_line to restrict the search range")
		}
		if params.Action != "" {
			return fmt.Errorf("action is not used in replace mode — it is only for line mode")
		}
		if params.Count > 0 {
			return fmt.Errorf("count is not used in replace mode — it is only for line mode (delete/replace actions)")
		}
	case "line":
		if params.Action == "" {
			return fmt.Errorf("action is required for line mode")
		}
		if params.OldString != "" {
			return fmt.Errorf("old_string is not used in line mode — use content for insert/replace, or omit for delete")
		}
		if params.NewString != "" {
			return fmt.Errorf("new_string is not used in line mode — use content for insert/replace")
		}
		if params.Regex {
			return fmt.Errorf("regex is not used in line mode — use replace mode with regex=true instead")
		}
		if params.StartLine > 0 || params.EndLine > 0 {
			return fmt.Errorf("start_line/end_line are not used in line mode — they are for replace mode")
		}
		if params.Action == "insert" && params.Position == "" {
			return fmt.Errorf("position is required when action=insert (use 'start' or 'end')")
		}
		if params.Action == "insert" && params.LineNumber > 0 && params.Position != "" {
			return fmt.Errorf("specify either line_number or position, not both — use line_number for insert_before/insert_after, or position for start/end")
		}
	default:
		return fmt.Errorf("unknown mode: %q (supported: create, replace, line)", params.Mode)
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
	case "line":
		return t.sandboxLineEdit(ctx, sandboxPath, params)
	default:
		return nil, fmt.Errorf("unknown mode: %q (supported: create, replace, line)", params.Mode)
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

func (t *EditTool) sandboxLineEdit(ctx *ToolContext, path string, params EditParams) (*ToolResult, error) {
	// 读取文件内容
	oldContent, err := sandboxReadFile(ctx, path)
	if err != nil {
		return nil, err
	}

	// 复用纯 Go 的 doLineEdit 逻辑
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
	default:
		return nil, fmt.Errorf("unknown mode: %q (supported: create, replace, line)", params.Mode)
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

// splitLines splits content into lines, correctly handling trailing newline.
// Returns real lines (without the phantom empty element caused by trailing \n)
// and whether the content originally ended with a newline.
// This ensures line numbering matches what the Read tool displays.
func splitLines(content string) ([]string, bool) {
	lines := strings.Split(content, "\n")
	hasTrailingNL := len(lines) > 1 && lines[len(lines)-1] == ""
	if hasTrailingNL {
		lines = lines[:len(lines)-1]
	}
	return lines, hasTrailingNL
}

// joinWithTrailing joins lines with \n and appends trailing newline if the original had one.
func joinWithTrailing(lines []string, hasTrailingNL bool) string {
	result := strings.Join(lines, "\n")
	if hasTrailingNL && len(lines) > 0 {
		result += "\n"
	}
	return result
}

// suggestMatch tries to find similar text when exact match fails.
// Helps the LLM identify whitespace/indentation mismatches.
func suggestMatch(content, searchStr string) string {
	var firstLine string
	for _, l := range strings.Split(searchStr, "\n") {
		if t := strings.TrimSpace(l); t != "" && len(t) >= 3 {
			firstLine = t
			break
		}
	}
	if firstLine == "" {
		return ""
	}
	lines, _ := splitLines(content)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, firstLine) {
			return fmt.Sprintf("\nHint: line %d has similar text (possible whitespace mismatch): %q", i+1, Truncate(trimmed, 100))
		}
	}
	return ""
}

// splitContentByLineRange splits content into prefix, target range, and suffix based on start_line/end_line.
// Returns (prefix, rangeText, suffix, error). When both start_line and end_line are 0, rangeText equals the full content.
func splitContentByLineRange(content string, startLine, endLine int) (string, string, string, error) {
	if startLine <= 0 && endLine <= 0 {
		return "", content, "", nil
	}

	lines, hasTrailingNL := splitLines(content)
	totalLines := len(lines)

	start := 1
	end := totalLines

	if startLine > 0 {
		start = startLine
	}
	if endLine > 0 {
		end = endLine
	}

	if start > totalLines {
		return "", "", "", fmt.Errorf("start_line %d exceeds total lines %d", start, totalLines)
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

// doReplace 执行文本替换（支持精确匹配和正则匹配）
func (t *EditTool) doReplace(content string, params EditParams, filePath string) (string, string, error) {
	if params.OldString == "" {
		return "", "", fmt.Errorf("old_string is required for replace mode")
	}

	// Split content by line range if start_line/end_line specified
	prefix, rangeText, suffix, err := splitContentByLineRange(content, params.StartLine, params.EndLine)
	if err != nil {
		return "", "", err
	}

	if params.Regex {
		return t.doRegexReplaceIn(prefix, rangeText, suffix, params, filePath, content)
	}

	// 检查是否存在要替换的文本
	count := strings.Count(rangeText, params.OldString)
	if count == 0 {
		hint := suggestMatch(rangeText, params.OldString)
		if params.StartLine > 0 || params.EndLine > 0 {
			effStart := params.StartLine
			if effStart <= 0 {
				effStart = 1
			}
			effEnd := params.EndLine
			if effEnd <= 0 {
				lines, _ := splitLines(content)
				effEnd = len(lines)
			}
			return "", "", fmt.Errorf("text not found in lines %d-%d: %q%s", effStart, effEnd, params.OldString, hint)
		}
		return "", "", fmt.Errorf("text not found: %q%s", params.OldString, hint)
	}

	var newRangeText string
	var replacedCount int

	if params.ReplaceAll {
		newRangeText = strings.ReplaceAll(rangeText, params.OldString, params.NewString)
		replacedCount = count
	} else {
		newRangeText = strings.Replace(rangeText, params.OldString, params.NewString, 1)
		replacedCount = 1
	}

	newContent := prefix + newRangeText + suffix

	if count > 1 && !params.ReplaceAll {
		return newContent, fmt.Sprintf("Replaced 1 of %d occurrences. Use replace_all=true to replace all.", count), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced %d occurrence(s) in %s", replacedCount, filePath), nil
}

// doRegexReplaceIn 执行正则替换（内部函数，由 doReplace 在 regex=true 时调用）
// SECURITY NOTE: Go's regexp package uses RE2 engine which guarantees O(n) time complexity
// for all operations, preventing ReDoS attacks.
func (t *EditTool) doRegexReplaceIn(prefix, rangeText, suffix string, params EditParams, filePath, fullContent string) (string, string, error) {
	re, err := regexp.Compile(params.OldString)
	if err != nil {
		return "", "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	matches := re.FindAllString(rangeText, -1)
	if len(matches) == 0 {
		if params.StartLine > 0 || params.EndLine > 0 {
			effStart := params.StartLine
			if effStart <= 0 {
				effStart = 1
			}
			effEnd := params.EndLine
			if effEnd <= 0 {
				effEnd = len(strings.Split(fullContent, "\n"))
			}
			return "", "", fmt.Errorf("no match found for pattern in lines %d-%d: %s", effStart, effEnd, params.OldString)
		}
		return "", "", fmt.Errorf("no match found for pattern: %s", params.OldString)
	}

	var newRangeText string
	var replacedCount int

	if params.ReplaceAll {
		newRangeText = re.ReplaceAllString(rangeText, params.NewString)
		replacedCount = len(matches)
	} else {
		newRangeText = re.ReplaceAllStringFunc(rangeText, func(m string) string {
			if replacedCount == 0 {
				replacedCount++
				return re.ReplaceAllString(m, params.NewString)
			}
			return m
		})
	}

	newContent := prefix + newRangeText + suffix

	if len(matches) > 1 && !params.ReplaceAll {
		return newContent, fmt.Sprintf("Replaced 1 of %d matches. Use replace_all=true to replace all.", len(matches)), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced %d match(es) in %s", replacedCount, filePath), nil
}

// doLineEdit 执行行编辑（支持 count 批量操作和 position 定位）
func (t *EditTool) doLineEdit(content string, params EditParams) (string, string, error) {
	// Handle action="insert" with position (formerly "insert" mode)
	if params.Action == "insert" {
		return t.doPositionInsert(content, params)
	}

	if params.Action == "" {
		return "", "", fmt.Errorf("action is required for line mode")
	}

	lines, hasTrailingNL := splitLines(content)
	totalLines := len(lines)

	// Default count to 1
	count := params.Count
	if count <= 0 {
		count = 1
	}

	if params.LineNumber <= 0 {
		return "", "", fmt.Errorf("line_number must be positive (1-based)")
	}

	if params.LineNumber > totalLines {
		return "", "", fmt.Errorf("line_number %d exceeds total lines %d", params.LineNumber, totalLines)
	}

	idx := params.LineNumber - 1

	switch params.Action {
	case "insert_before":
		if params.Content == "" {
			return "", "", fmt.Errorf("content is required for insert_before action")
		}
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:idx]...)
		newLines = append(newLines, params.Content)
		newLines = append(newLines, lines[idx:]...)
		return joinWithTrailing(newLines, hasTrailingNL), fmt.Sprintf("Inserted line before line %d", params.LineNumber), nil

	case "insert_after":
		if params.Content == "" {
			return "", "", fmt.Errorf("content is required for insert_after action")
		}
		newLines := make([]string, 0, len(lines)+1)
		newLines = append(newLines, lines[:idx+1]...)
		newLines = append(newLines, params.Content)
		newLines = append(newLines, lines[idx+1:]...)
		return joinWithTrailing(newLines, hasTrailingNL), fmt.Sprintf("Inserted line after line %d", params.LineNumber), nil

	case "replace":
		if params.Content == "" {
			return "", "", fmt.Errorf("content is required for replace action")
		}
		if idx+count > totalLines {
			return "", "", fmt.Errorf("line_number %d + count %d exceeds total lines %d", params.LineNumber, count, totalLines)
		}
		oldLines := make([]string, count)
		copy(oldLines, lines[idx:idx+count])
		lines[idx] = params.Content
		newLines := make([]string, 0, len(lines)-count+1)
		newLines = append(newLines, lines[:idx+1]...)
		newLines = append(newLines, lines[idx+count:]...)
		return joinWithTrailing(newLines, hasTrailingNL),
			fmt.Sprintf("Replaced %d line(s) at line %d: %q -> %q", count, params.LineNumber, Truncate(strings.Join(oldLines, "\n"), 50), Truncate(params.Content, 50)), nil

	case "delete":
		if idx+count > totalLines {
			return "", "", fmt.Errorf("line_number %d + count %d exceeds total lines %d", params.LineNumber, count, totalLines)
		}
		deletedLines := make([]string, count)
		copy(deletedLines, lines[idx:idx+count])
		newLines := make([]string, 0, len(lines)-count)
		newLines = append(newLines, lines[:idx]...)
		newLines = append(newLines, lines[idx+count:]...)
		return joinWithTrailing(newLines, hasTrailingNL),
			fmt.Sprintf("Deleted %d line(s) at line %d: %q", count, params.LineNumber, Truncate(strings.Join(deletedLines, "\n"), 80)), nil

	default:
		return "", "", fmt.Errorf("unknown action: %q (supported: insert_before, insert_after, replace, delete)", params.Action)
	}
}

// doPositionInsert handles position-based insertion (formerly "insert" mode).
// action must be "insert", position must be "start" or "end".
func (t *EditTool) doPositionInsert(content string, params EditParams) (string, string, error) {
	if params.Content == "" {
		return "", "", fmt.Errorf("content is required for insert action")
	}

	switch params.Position {
	case "start":
		if len(content) > 0 && len(params.Content) > 0 && !strings.HasSuffix(params.Content, "\n") {
			return params.Content + "\n" + content, "Inserted content at the start", nil
		}
		return params.Content + content, "Inserted content at the start", nil

	case "end":
		// 确保末尾有换行符
		if len(content) > 0 && content[len(content)-1] != '\n' {
			content += "\n"
		}
		return content + params.Content, "Inserted content at the end", nil

	default:
		return "", "", fmt.Errorf("invalid position: %q (use 'start' or 'end', or use action=insert_before/insert_after with line_number)", params.Position)
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
