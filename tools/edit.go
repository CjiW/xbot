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

	// 解析路径：如果是相对路径，则基于工作目录
	filePath := params.Path
	if !filepath.IsAbs(filePath) && ctx != nil && ctx.WorkingDir != "" {
		filePath = filepath.Join(ctx.WorkingDir, filePath)
	}

	// create 模式不需要读取现有文件
	if params.Mode == "create" {
		summary, err := t.doCreate(filePath, params)
		if err != nil {
			return nil, err
		}
		// create 模式：detail 显示新文件内容的 diff
		diff := generateUnifiedDiff("", params.Content, filePath)
		return &ToolResult{Summary: summary, Detail: diff}, nil
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

	// 生成 unified diff 作为 detail
	diff := generateUnifiedDiff(oldContent, newContent, filePath)
	return &ToolResult{Summary: result, Detail: diff}, nil
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
		return strings.Join(lines, "\n"), fmt.Sprintf("Replaced line %d: %q -> %q", params.LineNumber, truncate(oldLine, 50), truncate(params.Content, 50)), nil

	case "delete":
		oldLine := lines[idx]
		newLines := make([]string, 0, len(lines)-1)
		newLines = append(newLines, lines[:idx]...)
		newLines = append(newLines, lines[idx+1:]...)
		return strings.Join(newLines, "\n"), fmt.Sprintf("Deleted line %d: %q", params.LineNumber, truncate(oldLine, 50)), nil

	default:
		return "", "", fmt.Errorf("unknown action: %s (supported: insert_before, insert_after, replace, delete)", params.Action)
	}
}

// doRegexReplace 执行正则替换
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

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
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

	// 将 diffs 转换为带行号的 hunks
	contextLines := 3
	type line struct {
		op   diffmatchpatch.Operation
		text string
	}

	// 展平所有 diff 为行
	var allLines []line
	for _, d := range diffs {
		text := d.Text
		// 拆分成行
		lines := strings.Split(text, "\n")
		// 如果最后一个元素是空字符串（trailing newline），去掉
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		for _, l := range lines {
			allLines = append(allLines, line{op: d.Type, text: l})
		}
	}

	// 找出变更行的索引
	type hunkRange struct{ start, end int }
	var changeRanges []hunkRange
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
				changeRanges = append(changeRanges, hunkRange{changeStart, i})
				inChange = false
			}
		}
	}
	if inChange {
		changeRanges = append(changeRanges, hunkRange{changeStart, len(allLines)})
	}

	if len(changeRanges) == 0 {
		return ""
	}

	// 合并相邻的 change ranges（间距 <= 2*contextLines）
	var merged []hunkRange
	cur := changeRanges[0]
	for i := 1; i < len(changeRanges); i++ {
		if changeRanges[i].start-cur.end <= 2*contextLines {
			cur.end = changeRanges[i].end
		} else {
			merged = append(merged, cur)
			cur = changeRanges[i]
		}
	}
	merged = append(merged, cur)

	// 为每个合并后的 range 生成 hunk
	for _, mr := range merged {
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

		fmt.Fprintf(&sb, "@@ -%d,%d +%d,%d @@\n", oldLine, oldCount, newLine, newCount)

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

	return sb.String()
}
