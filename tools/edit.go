package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"xbot/llm"

	"github.com/sergi/go-diff/diffmatchpatch"
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

// FileIO abstracts file I/O operations so that the same business logic
// can be shared between local (os) and sandbox (docker exec) modes.
type FileIO interface {
	ReadFile(path string) (string, error)
	WriteFile(path string, content string) error
	MkdirAll(path string) error
	FileExists(path string) (bool, error)
}

// localFileIO implements FileIO using the local filesystem.
type localFileIO struct{}

func (f *localFileIO) ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %w", err)
	}
	return string(data), nil
}

func (f *localFileIO) WriteFile(path string, content string) error {
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

func (f *localFileIO) MkdirAll(path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	return nil
}

func (f *localFileIO) FileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// sandboxFileIO implements FileIO by executing commands inside a sandbox container.
type sandboxFileIO struct {
	ctx *ToolContext
}

func (f *sandboxFileIO) ReadFile(path string) (string, error) {
	cmd := fmt.Sprintf("cat '%s'", path)
	content, err := RunInSandboxWithShell(f.ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %v", err)
	}
	return content, nil
}

func (f *sandboxFileIO) WriteFile(path string, content string) error {
	writeCmd := fmt.Sprintf("cat > '%s' << 'XBOT_EOF'\n%s\nXBOT_EOF", path, content)
	_, err := RunInSandboxWithShell(f.ctx, writeCmd)
	if err != nil {
		return fmt.Errorf("failed to write file: %v", err)
	}
	return nil
}

func (f *sandboxFileIO) MkdirAll(path string) error {
	cmd := fmt.Sprintf("mkdir -p '%s'", path)
	_, err := RunInSandboxWithShell(f.ctx, cmd)
	if err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}
	return nil
}

func (f *sandboxFileIO) FileExists(path string) (bool, error) {
	cmd := fmt.Sprintf("test -e '%s' && echo yes || echo no", path)
	output, err := RunInSandboxWithShell(f.ctx, cmd)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == "yes", nil
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
		// path already validated above
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
	default:
		return nil, fmt.Errorf("unknown mode: %s (supported: create, replace, line, regex, insert)", params.Mode)
	}

	// 沙箱模式
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
		return t.executeInSandbox(ctx, params)
	}

	// 非沙箱模式
	return t.executeLocal(ctx, params)
}

// resolveSandboxPath converts a user-supplied path to a container-internal path.
func resolveSandboxPath(ctx *ToolContext, path string) string {
	if !strings.HasPrefix(path, "/workspace/") && !strings.HasPrefix(path, "/") {
		return "/workspace/" + path
	}
	if strings.HasPrefix(path, "/workspace/") {
		return path
	}
	if strings.HasPrefix(path, "/") && ctx.WorkspaceRoot != "" {
		rel, err := filepath.Rel(ctx.WorkspaceRoot, path)
		if err == nil && !strings.HasPrefix(rel, "..") {
			return "/workspace/" + rel
		}
	}
	return path
}

// executeInSandbox 在沙箱内执行编辑操作
func (t *EditTool) executeInSandbox(ctx *ToolContext, params EditParams) (*ToolResult, error) {
	sandboxPath := resolveSandboxPath(ctx, params.Path)
	fio := &sandboxFileIO{ctx: ctx}
	return t.executeWithIO(fio, sandboxPath, params)
}

// executeLocal 在本地执行编辑操作（非沙箱模式）
func (t *EditTool) executeLocal(ctx *ToolContext, params EditParams) (*ToolResult, error) {
	filePath, err := ResolveWritePath(ctx, params.Path)
	if err != nil {
		return nil, err
	}
	fio := &localFileIO{}
	return t.executeWithIO(fio, filePath, params)
}

// executeWithIO is the unified implementation that works with any FileIO backend.
func (t *EditTool) executeWithIO(fio FileIO, filePath string, params EditParams) (*ToolResult, error) {
	// create 模式不需要读取现有文件
	if params.Mode == "create" {
		return doCreate(fio, filePath, params.Content)
	}

	// 读取文件内容
	oldContent, err := fio.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var newContent string
	var result string

	switch params.Mode {
	case "replace":
		newContent, result, err = doReplace(oldContent, params.OldString, params.NewString, params.ReplaceAll, filePath)
	case "line":
		newContent, result, err = doLineEdit(oldContent, params)
	case "regex":
		newContent, result, err = doRegexReplace(oldContent, params.Pattern, params.Replacement, params.ReplaceAll, filePath)
	case "insert":
		newContent, result, err = doInsert(oldContent, params, filePath)
	default:
		return nil, fmt.Errorf("unknown mode: %s (supported: create, replace, line, regex, insert)", params.Mode)
	}

	if err != nil {
		return nil, err
	}

	// 写入文件
	if err := fio.WriteFile(filePath, newContent); err != nil {
		return nil, err
	}

	diff := generateUnifiedDiff(oldContent, newContent, filePath)
	return &ToolResult{Summary: result, Detail: diff}, nil
}

// doCreate 创建新文件
func doCreate(fio FileIO, filePath string, content string) (*ToolResult, error) {
	// Create parent directories if they don't exist
	dir := filepath.Dir(filePath)
	if dir != "." && dir != "" {
		if err := fio.MkdirAll(dir); err != nil {
			return nil, err
		}
	}

	// Write file
	if err := fio.WriteFile(filePath, content); err != nil {
		return nil, err
	}

	summary := fmt.Sprintf("File created successfully: %s", filePath)
	diff := generateUnifiedDiff("", content, filePath)
	return &ToolResult{Summary: summary, Detail: diff}, nil
}

// doReplace 执行文本替换
func doReplace(content, oldStr, newStr string, replaceAll bool, filePath string) (string, string, error) {
	// 检查是否存在要替换的文本
	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", "", fmt.Errorf("text not found: %q", oldStr)
	}

	var newContent string
	var replacedCount int

	if replaceAll {
		newContent = strings.ReplaceAll(content, oldStr, newStr)
		replacedCount = count
	} else {
		newContent = strings.Replace(content, oldStr, newStr, 1)
		replacedCount = 1
	}

	if count > 1 && !replaceAll {
		return newContent, fmt.Sprintf("Replaced 1 of %d occurrences. Use replace_all=true to replace all.", count), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced %d occurrence(s) in %s", replacedCount, filePath), nil
}

// doLineEdit 执行行编辑
func doLineEdit(content string, params EditParams) (string, string, error) {
	if params.LineNumber <= 0 {
		return "", "", fmt.Errorf("line_number must be positive (1-based)")
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
		return strings.Join(newLines, "\n"), fmt.Sprintf("Deleted line %d: %q", params.LineNumber, Truncate(oldLine, 50)), nil

	default:
		return "", "", fmt.Errorf("unknown action: %s (supported: insert_before, insert_after, replace, delete)", params.Action)
	}
}

// doRegexReplace 执行正则替换
func doRegexReplace(content, pattern, replacement string, replaceAll bool, filePath string) (string, string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	// 统计匹配数量
	matches := re.FindAllString(content, -1)
	if len(matches) == 0 {
		return "", "", fmt.Errorf("no match found for pattern: %s", pattern)
	}

	var newContent string
	var replacedCount int

	if replaceAll {
		newContent = re.ReplaceAllString(content, replacement)
		replacedCount = len(matches)
	} else {
		newContent = re.ReplaceAllStringFunc(content, func(m string) string {
			if replacedCount == 0 {
				replacedCount++
				return re.ReplaceAllString(m, replacement)
			}
			return m
		})
	}

	if len(matches) > 1 && !replaceAll {
		return newContent, fmt.Sprintf("Replaced 1 of %d matches. Use replace_all=true to replace all.", len(matches)), nil
	}

	return newContent, fmt.Sprintf("Successfully replaced %d match(es) in %s", replacedCount, filePath), nil
}

// doInsert 执行插入操作
func doInsert(content string, params EditParams, filePath string) (string, string, error) {
	if params.Content == "" {
		return "", "", fmt.Errorf("content is required for insert mode")
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
		return doLineEdit(content, params)
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

// diffLine represents a single line in a diff with its operation type.
type diffLine struct {
	op   diffmatchpatch.Operation
	text string
}

// flattenDiffsToLines expands diff chunks into individual lines.
func flattenDiffsToLines(diffs []diffmatchpatch.Diff) []diffLine {
	var allLines []diffLine
	for _, d := range diffs {
		lines := strings.Split(d.Text, "\n")
		// 如果最后一个元素是空字符串（trailing newline），去掉
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		for _, l := range lines {
			allLines = append(allLines, diffLine{op: d.Type, text: l})
		}
	}
	return allLines
}

// hunkRange represents a contiguous range of changed lines.
type hunkRange struct{ start, end int }

// findChangeRanges identifies index ranges where changes (non-equal lines) occur.
func findChangeRanges(allLines []diffLine) []hunkRange {
	var ranges []hunkRange
	inChange := false
	changeStart := 0
	for i, l := range allLines {
		if l.op != diffmatchpatch.DiffEqual {
			if !inChange {
				inChange = true
				changeStart = i
			}
		} else {
			if inChange {
				ranges = append(ranges, hunkRange{changeStart, i})
				inChange = false
			}
		}
	}
	if inChange {
		ranges = append(ranges, hunkRange{changeStart, len(allLines)})
	}
	return ranges
}

// mergeRanges merges adjacent change ranges that are within 2*contextLines of each other.
func mergeRanges(ranges []hunkRange, contextLines int) []hunkRange {
	if len(ranges) == 0 {
		return nil
	}
	var merged []hunkRange
	cur := ranges[0]
	for i := 1; i < len(ranges); i++ {
		if ranges[i].start-cur.end <= 2*contextLines {
			cur.end = ranges[i].end
		} else {
			merged = append(merged, cur)
			cur = ranges[i]
		}
	}
	merged = append(merged, cur)
	return merged
}

// formatHunk writes a single unified-diff hunk for the given merged range.
func formatHunk(sb *strings.Builder, allLines []diffLine, mr hunkRange, contextLines int) {
	hStart := mr.start - contextLines
	if hStart < 0 {
		hStart = 0
	}
	hEnd := mr.end + contextLines
	if hEnd > len(allLines) {
		hEnd = len(allLines)
	}

	// 计算 old/new 行号
	oldLine := 1
	newLine := 1
	for i := 0; i < hStart; i++ {
		switch allLines[i].op {
		case diffmatchpatch.DiffEqual:
			oldLine++
			newLine++
		case diffmatchpatch.DiffDelete:
			oldLine++
		case diffmatchpatch.DiffInsert:
			newLine++
		}
	}

	// 计算 hunk 中 old/new 的行数
	oldCount, newCount := 0, 0
	for i := hStart; i < hEnd; i++ {
		switch allLines[i].op {
		case diffmatchpatch.DiffEqual:
			oldCount++
			newCount++
		case diffmatchpatch.DiffDelete:
			oldCount++
		case diffmatchpatch.DiffInsert:
			newCount++
		}
	}

	fmt.Fprintf(sb, "@@ -%d,%d +%d,%d @@\n", oldLine, oldCount, newLine, newCount)

	for i := hStart; i < hEnd; i++ {
		switch allLines[i].op {
		case diffmatchpatch.DiffEqual:
			sb.WriteString(" " + allLines[i].text + "\n")
		case diffmatchpatch.DiffDelete:
			sb.WriteString("-" + allLines[i].text + "\n")
		case diffmatchpatch.DiffInsert:
			sb.WriteString("+" + allLines[i].text + "\n")
		}
	}
}

// generateUnifiedDiff 使用 go-diff 库生成 unified diff 格式的差异
func generateUnifiedDiff(oldContent, newContent, filePath string) string {
	dmp := diffmatchpatch.New()

	// 按行进行 diff
	a, b, c := dmp.DiffLinesToChars(oldContent, newContent)
	diffs := dmp.DiffMain(a, b, false)
	diffs = dmp.DiffCharsToLines(diffs, c)
	diffs = dmp.DiffCleanupSemantic(diffs)

	if len(diffs) == 0 || (len(diffs) == 1 && diffs[0].Type == diffmatchpatch.DiffEqual) {
		return ""
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "--- a/%s\n", filePath)
	fmt.Fprintf(&sb, "+++ b/%s\n", filePath)

	contextLines := 3
	allLines := flattenDiffsToLines(diffs)
	changeRanges := findChangeRanges(allLines)

	if len(changeRanges) == 0 {
		return ""
	}

	merged := mergeRanges(changeRanges, contextLines)

	for _, mr := range merged {
		formatHunk(&sb, allLines, mr, contextLines)
	}

	return sb.String()
}
