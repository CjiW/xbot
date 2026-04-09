package tools

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"xbot/internal/cmdbuilder"
	"xbot/llm"
)

// ============================================================================
// FileCreateTool — 创建新文件（2 params: path, content）
// ============================================================================

// FileCreateTool 创建新文件工具
type FileCreateTool struct{}

func (t *FileCreateTool) Name() string {
	return "FileCreate"
}

func (t *FileCreateTool) Description() string {
	return `Create a new file.
Required: path, content
Creates the file (and parent directories if needed). Returns error if file already exists.

Examples:
- {"path": "hello.txt", "content": "Hello!"}
- {"path": "src/main.go", "content": "package main\n\nfunc main() {}"}`
}

func (t *FileCreateTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path to create (relative to working directory or absolute)", Required: true},
		{Name: "content", Type: "string", Description: "Content to write to the new file", Required: true},
		{Name: "run_as", Type: "string", Description: "OS username to execute as. Requires permission control to be enabled. Only effective in none sandbox mode.", Required: false},
		{Name: "reason", Type: "string", Description: "Optional human-readable reason shown in approval requests when approval is required.", Required: false},
	}
}

type FileCreateParams struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	RunAs   string `json:"run_as"`
	Reason  string `json:"reason"`
}

func (t *FileCreateTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params FileCreateParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if (strings.TrimSpace(params.RunAs) == "") != (strings.TrimSpace(params.Reason) == "") {
		return nil, fmt.Errorf("run_as and reason must be provided together")
	}

	if shouldUseSandbox(ctx) {
		sandboxPath := resolveSandboxPath(ctx, params.Path)
		return t.sandboxCreate(ctx, sandboxPath, params.Content)
	}
	return t.executeLocal(ctx, params)
}

func (t *FileCreateTool) sandboxCreate(ctx *ToolContext, path, content string) (*ToolResult, error) {
	if err := sandboxWriteNewFile(ctx, path, content); err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("File created successfully: %s", path)
	return &ToolResult{Summary: summary, Tips: "文件已创建。建议用 Read 验证内容。"}, nil
}

func (t *FileCreateTool) executeLocal(ctx *ToolContext, params FileCreateParams) (*ToolResult, error) {
	filePath, err := ResolveWritePath(ctx, params.Path)
	if err != nil {
		return nil, err
	}

	// Check if file already exists
	if _, err := os.Stat(filePath); err == nil {
		return nil, fmt.Errorf("file already exists: %s (use FileReplace to modify existing files)", filePath)
	}

	// Create parent directories if needed
	dir := filepath.Dir(filePath)
	if dir != "." && dir != "" {
		if params.RunAs != "" {
			if err := cmdbuilder.MkdirAllAsUser(params.RunAs, dir, 0755); err != nil {
				return nil, fmt.Errorf("failed to create directory as user %q: %w", params.RunAs, err)
			}
		} else if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create directory: %w", err)
		}
	}

	if err := cmdbuilder.WriteFileAsUser(params.RunAs, filePath, []byte(params.Content), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	summary := fmt.Sprintf("File created successfully: %s", filePath)
	return &ToolResult{Summary: summary, Tips: "文件已创建。建议用 Read 验证内容。"}, nil
}

// ============================================================================
// FileReplaceTool — 查找替换文件内容（7 params）
// ============================================================================

// FileReplaceTool 文件替换工具
type FileReplaceTool struct{}

func (t *FileReplaceTool) Name() string {
	return "FileReplace"
}

func (t *FileReplaceTool) Description() string {
	return `Find and replace text in a file.
Required: path, old_string, new_string
Optional: replace_all (default false), regex (default false), start_line, end_line

Default behavior: exact string match, replaces first occurrence only.
When regex=true, old_string is treated as RE2 pattern, new_string supports $1/$2 captures.
When replace_all=true, replaces all occurrences.

⚠️ Common mistakes (avoid these!):
- old_string should be unique in the file to avoid replacing the wrong occurrence.
- To restrict replacement to a specific range, use start_line and end_line.
- start_line and end_line restrict the search range. They do NOT select lines for replacement.

Examples:
- {"path": "main.go", "old_string": "foo", "new_string": "bar"}
- {"path": "main.go", "old_string": "oldName", "new_string": "newName", "replace_all": true}
- {"path": "main.go", "old_string": "v\\d+\\.\\d+", "new_string": "v2.0", "regex": true}
- {"path": "main.go", "old_string": "foo", "new_string": "bar", "start_line": 10, "end_line": 20}`
}

func (t *FileReplaceTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path (relative to working directory or absolute)", Required: true},
		{Name: "old_string", Type: "string", Description: "Text to find (exact match by default). When regex=true, treated as RE2 pattern.", Required: true},
		{Name: "new_string", Type: "string", Description: "Replacement text. Supports $1/$2 captures when regex=true.", Required: true},
		{Name: "replace_all", Type: "boolean", Description: "Replace all occurrences (default false, replaces first only)", Required: false},
		{Name: "regex", Type: "boolean", Description: "Use RE2 regex matching (default false, exact match)", Required: false},
		{Name: "start_line", Type: "integer", Description: "Restrict search from this line, 1-based inclusive", Required: false},
		{Name: "end_line", Type: "integer", Description: "Restrict search to this line, 1-based inclusive", Required: false},
		{Name: "run_as", Type: "string", Description: "OS username to execute as. Requires permission control to be enabled. Only effective in none sandbox mode.", Required: false},
		{Name: "reason", Type: "string", Description: "Optional human-readable reason shown in approval requests when approval is required.", Required: false},
	}
}

type FileReplaceParams struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
	Regex      bool   `json:"regex"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	RunAs      string `json:"run_as"`
	Reason     string `json:"reason"`
}

func (t *FileReplaceTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params FileReplaceParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if params.OldString == "" {
		return nil, fmt.Errorf("old_string is required")
	}
	if (strings.TrimSpace(params.RunAs) == "") != (strings.TrimSpace(params.Reason) == "") {
		return nil, fmt.Errorf("run_as and reason must be provided together")
	}

	// Safety: reject oversized inputs to prevent CPU/memory exhaustion
	const maxParamLen = 1 << 20 // 1 MB
	if len(params.OldString) > maxParamLen {
		return nil, fmt.Errorf("old_string too large (%d bytes, max %d)", len(params.OldString), maxParamLen)
	}
	if len(params.NewString) > maxParamLen {
		return nil, fmt.Errorf("new_string too large (%d bytes, max %d)", len(params.NewString), maxParamLen)
	}

	// When only end_line is specified, default start_line to 1
	if params.EndLine > 0 && params.StartLine <= 0 {
		params.StartLine = 1
	}

	if shouldUseSandbox(ctx) {
		sandboxPath := resolveSandboxPath(ctx, params.Path)
		return t.executeInSandbox(ctx, sandboxPath, params)
	}
	return t.executeLocal(ctx, params)
}

func (t *FileReplaceTool) executeInSandbox(ctx *ToolContext, path string, params FileReplaceParams) (*ToolResult, error) {
	oldContent, err := sandboxReadFile(ctx, path)
	if err != nil {
		return nil, err
	}

	newContent, result, err := doReplace(oldContent, params, path)
	if err != nil {
		return nil, err
	}

	if err := sandboxWriteFile(ctx, path, newContent); err != nil {
		return nil, err
	}

	return &ToolResult{Summary: result, Tips: "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"}, nil
}

func (t *FileReplaceTool) executeLocal(ctx *ToolContext, params FileReplaceParams) (*ToolResult, error) {
	filePath, err := ResolveWritePath(ctx, params.Path)
	if err != nil {
		return nil, err
	}

	content, err := cmdbuilder.ReadFileAsUser(params.RunAs, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	newContent, result, err := doReplace(string(content), params, filePath)
	if err != nil {
		return nil, err
	}

	if err := cmdbuilder.WriteFileAsUser(params.RunAs, filePath, []byte(newContent), 0644); err != nil {
		return nil, fmt.Errorf("failed to write file: %w", err)
	}

	return &ToolResult{Summary: result, Tips: "修改已完成。建议用 Read 验证修改结果，确认文件内容正确。"}, nil
}

// ============================================================================
// Shared helpers
// ============================================================================

// resolveSandboxPath 将用户输入的路径转换为容器内路径
func resolveSandboxPath(ctx *ToolContext, userPath string) string {
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

// doReplace 执行文本替换（支持精确匹配和 RE2 正则匹配）
// SECURITY NOTE: Go's regexp package uses RE2 engine which guarantees O(n) time complexity
// for all operations, preventing ReDoS attacks.
func doReplace(content string, params FileReplaceParams, filePath string) (string, string, error) {
	if params.OldString == "" {
		return "", "", fmt.Errorf("old_string is required")
	}

	// Split content by line range if start_line/end_line specified
	prefix, rangeText, suffix, err := splitContentByLineRange(content, params.StartLine, params.EndLine)
	if err != nil {
		return "", "", err
	}

	if params.Regex {
		return doRegexReplace(prefix, rangeText, suffix, params, filePath, content)
	}

	// Exact string match
	count := strings.Count(rangeText, params.OldString)
	if count == 0 {
		// Fuzzy whitespace fallback: try matching with leading/trailing whitespace stripped per line
		if actualOld, adjustedNew, ok := fuzzyWhitespaceMatch(rangeText, params.OldString, params.NewString); ok {
			actualCount := strings.Count(rangeText, actualOld)
			var newRangeText string
			var replacedCount int
			if params.ReplaceAll {
				newRangeText = strings.ReplaceAll(rangeText, actualOld, adjustedNew)
				replacedCount = actualCount
			} else {
				newRangeText = strings.Replace(rangeText, actualOld, adjustedNew, 1)
				replacedCount = 1
			}
			newContent := prefix + newRangeText + suffix
			if actualCount > 1 && !params.ReplaceAll {
				return newContent, fmt.Sprintf("Replaced 1 of %d occurrences (auto-corrected whitespace) in %s. Use replace_all=true to replace all.", actualCount, filePath), nil
			}
			return newContent, fmt.Sprintf("Successfully replaced %d occurrence(s) (auto-corrected whitespace) in %s", replacedCount, filePath), nil
		}

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

// doRegexReplace 执行正则替换（由 doReplace 在 regex=true 时调用）
func doRegexReplace(prefix, rangeText, suffix string, params FileReplaceParams, filePath, fullContent string) (string, string, error) {
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
				lines, _ := splitLines(fullContent)
				effEnd = len(lines)
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

// splitLines splits content into lines, correctly handling trailing newline.
func splitLines(content string) ([]string, bool) {
	lines := strings.Split(content, "\n")
	hasTrailingNL := len(lines) > 1 && lines[len(lines)-1] == ""
	if hasTrailingNL {
		lines = lines[:len(lines)-1]
	}
	return lines, hasTrailingNL
}

// splitContentByLineRange splits content by line range for replace operations.
// Returns prefix (before range), rangeText (within range), suffix (after range), and error.
// When start=0 and end=0, returns the entire content as rangeText (no line restriction).
func splitContentByLineRange(content string, start, end int) (string, string, string, error) {
	lines, hasTrailingNL := splitLines(content)
	totalLines := len(lines)

	if start <= 0 && end <= 0 {
		return "", content, "", nil
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

// leadingWhitespace returns the leading whitespace prefix of s.
func leadingWhitespace(s string) string {
	return s[:len(s)-len(strings.TrimLeft(s, " \t"))]
}

// adjustIndentation adjusts newStr's indentation based on the difference
// between oldLines (what the LLM sent) and actualLines (what the file has).
// It detects the base indent from the first non-empty line pair and applies
// the same prefix substitution to every line in newStr.
func adjustIndentation(oldLines, actualLines []string, newStr string) string {
	var oldBase, actualBase string
	for i, line := range oldLines {
		if strings.TrimSpace(line) != "" && i < len(actualLines) {
			oldBase = leadingWhitespace(line)
			actualBase = leadingWhitespace(actualLines[i])
			break
		}
	}

	if oldBase == actualBase {
		return newStr
	}

	newLines := strings.Split(newStr, "\n")
	for i, line := range newLines {
		indent := leadingWhitespace(line)
		rest := line[len(indent):]
		level := 0
		remaining := indent
		// Guard: empty oldBase would cause infinite loop
		if oldBase == "" {
			continue
		}
		for strings.HasPrefix(remaining, oldBase) {
			level++
			remaining = remaining[len(oldBase):]
		}
		if level > 0 {
			newLines[i] = strings.Repeat(actualBase, level) + remaining + rest
		}
	}
	return strings.Join(newLines, "\n")
}

// fuzzyWhitespaceMatch attempts whitespace-tolerant matching when exact match fails.
// It strips leading+trailing whitespace per line and uses a sliding window to find
// a unique match. Returns (actualOldString, adjustedNewString, true) on success.
// Requires exactly 1 match; returns false on 0 or 2+ matches (ambiguous).
func fuzzyWhitespaceMatch(content, oldStr, newStr string) (string, string, bool) {
	oldLines := strings.Split(oldStr, "\n")
	contentLines := strings.Split(content, "\n")

	// Strip for comparison
	strippedOld := make([]string, len(oldLines))
	hasContent := false
	for i, line := range oldLines {
		strippedOld[i] = strings.TrimSpace(line)
		if strippedOld[i] != "" {
			hasContent = true
		}
	}
	if !hasContent {
		return "", "", false
	}

	windowSize := len(strippedOld)
	if windowSize > len(contentLines) {
		return "", "", false
	}

	// Sliding window search
	var matchIdx int
	matchCount := 0
	for i := 0; i <= len(contentLines)-windowSize; i++ {
		match := true
		for j, stripped := range strippedOld {
			if strings.TrimSpace(contentLines[i+j]) != stripped {
				match = false
				break
			}
		}
		if match {
			matchCount++
			matchIdx = i
			if matchCount > 1 {
				return "", "", false
			}
		}
	}

	if matchCount != 1 {
		return "", "", false
	}

	actualLines := contentLines[matchIdx : matchIdx+windowSize]
	actualOld := strings.Join(actualLines, "\n")

	adjustedNew := adjustIndentation(oldLines, actualLines, newStr)

	return actualOld, adjustedNew, true
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

// Truncate 截断字符串（公共函数，供多处使用）
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
