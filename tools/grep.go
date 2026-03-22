package tools

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
	"xbot/llm"
)

// GrepTool 文件内容搜索工具
type GrepTool struct{}

func (t *GrepTool) Name() string {
	return "Grep"
}

func (t *GrepTool) Description() string {
	return `Search for a pattern in file contents recursively.
Use **Go RE2 regular expression syntax**: \d+ (digits), \w+ (word chars), \s+ (whitespace), \b (word boundary), (?i) (case-insensitive), named groups (?P<name>...), etc.
The tool automatically handles compatibility between Go RE2 and POSIX ERE syntax when running in different modes.
Supports regular expressions. Returns matching lines with file paths and line numbers.
Parameters (JSON):
  - pattern: string, the regex pattern to search for (e.g., "func main", "TODO|FIXME", "error\.(New|Wrap)", "\d+")
  - path: string, optional, the directory to search in (defaults to current working directory)
  - include: string, optional, glob pattern to filter files (e.g., "*.go", "*.{ts,tsx}")
  - ignore_case: boolean, optional, perform case-insensitive matching (defaults to false)
  - context_lines: integer, optional, number of context lines to show before and after each match (defaults to 0)
Example: {"pattern": "func main", "path": "/project", "include": "*.go"}`
}

func (t *GrepTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "pattern", Type: "string", Description: "The regex pattern to search for in file contents", Required: true},
		{Name: "path", Type: "string", Description: "The directory to search in (defaults to current working directory)", Required: false},
		{Name: "include", Type: "string", Description: "Glob pattern to filter which files to search (e.g., \"*.go\", \"*.{ts,tsx}\")", Required: false},
		{Name: "ignore_case", Type: "boolean", Description: "Perform case-insensitive matching (defaults to false)", Required: false},
		{Name: "context_lines", Type: "integer", Description: "Number of context lines to show before and after each match (defaults to 0)", Required: false},
	}
}

// grepParams holds the parsed parameters for the grep tool.
type grepParams struct {
	Pattern      string `json:"pattern"`
	Path         string `json:"path"`
	Include      string `json:"include"`
	IgnoreCase   bool   `json:"ignore_case"`
	ContextLines int    `json:"context_lines"`
}

// grepMatch represents a single match result.
type grepMatch struct {
	File       string
	LineNumber int
	Line       string
}

const (
	maxGrepMatches    = 200
	maxGrepFileSize   = 1 * 1024 * 1024 // 1MB
	maxGrepLineLength = 500
)

// Pre-compiled regexes for parsing grep output lines.
// Match lines:    "filename:linenumber:content"
// Context lines:  "filename-linenumber-content" (from grep -C)
// Using greedy (.+) ensures the rightmost separator is matched,
// correctly handling filenames that contain ':' or '-' characters.
var (
	grepMatchLineRe   = regexp.MustCompile(`^(.+):(\d+):(.*)$`)
	grepContextLineRe = regexp.MustCompile(`^(.+)-(\d+)-(.*)$`)
)

func (t *GrepTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[grepParams](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	if params.ContextLines < 0 {
		params.ContextLines = 0
	}

	// 沙箱模式：在容器内执行 grep 命令
	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
		return t.executeInSandbox(ctx, params.Pattern, params.Path, params.Include, params.IgnoreCase, params.ContextLines)
	}

	// 非沙箱模式：本地搜索
	return t.executeLocal(ctx, params.Pattern, params.Path, params.Include, params.IgnoreCase, params.ContextLines)
}

// convertGoRE2ToERE converts a Go RE2 regex pattern to a POSIX ERE pattern
// for use with grep -E. It handles common Go RE2 idioms that are not valid
// or behave differently in POSIX ERE:
//   - Shorthand character classes: \d, \D, \w, \W, \s, \S
//   - Escape sequences: \t, \n, \r
//   - ERE quantifier escapes: \{n,m}, \{n,}, \{n} to {n,m}, {n,}, {n}
//   - Inline flags: (?i), (?m), (?s), (?U), (?im:...) are removed or stripped
//   - Named groups: (?P<name>...) to (...)
//   - Non-capturing groups: (?:...) to (...)
//   - Literal escapes like \( and \) are preserved as-is for ERE.
func convertGoRE2ToERE(pattern string) (string, error) {
	var sb strings.Builder
	i := 0
	n := len(pattern)

	for i < n {
		c := pattern[i]

		// Handle inline constructs starting with (?...
		if c == '(' && i+1 < n && pattern[i+1] == '?' {
			action, newI := handleInlineConstruct(pattern, i)
			if action >= 0 {
				if action == 1 {
					sb.WriteByte('(')
				}
				i = newI
				continue
			}
			sb.WriteByte('(')
			i++
			continue
		}

		// Handle backslash escapes
		if c == '\\' && i+1 < n {
			next := pattern[i+1]
			switch next {
			case 'd':
				sb.WriteString("[0-9]")
			case 'D':
				sb.WriteString("[^0-9]")
			case 'w':
				sb.WriteString("[a-zA-Z0-9_]")
			case 'W':
				sb.WriteString("[^a-zA-Z0-9_]")
			case 's':
				sb.WriteString("[[:space:]]")
			case 'S':
				sb.WriteString("[^[:space:]]")
			case 't':
				sb.WriteByte('\t')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 'b':
				sb.WriteString("\\b")
			case '\\':
				sb.WriteString("\\\\")
			case '{':
				if consumed, ok := tryParseBracedQuantifier(pattern, i, &sb); ok {
					i += consumed
					continue
				}
				sb.WriteString("\\{")
			default:
				sb.WriteByte('\\')
				sb.WriteByte(next)
			}
			i += 2
			continue
		}

		sb.WriteByte(c)
		i++
	}

	return sb.String(), nil
}

// handleInlineConstruct handles Go RE2 inline constructs starting at i where
// pattern[i] == '(' and pattern[i+1] == '?'.
// Returns:
//
//	action: -1 = not recognized, 0 = removed entirely, 1 = write '(' and skip
//	newI:   new scanning position
func handleInlineConstruct(pattern string, i int) (action int, newI int) {
	n := len(pattern)
	if i+2 >= n {
		return -1, i + 1
	}

	// Named group: (?P<name>...)
	if pattern[i+2] == 'P' && i+3 < n && pattern[i+3] == '<' {
		gtIdx := strings.IndexByte(pattern[i+4:], '>')
		if gtIdx == -1 {
			return -1, i + 1
		}
		return 1, i + 4 + gtIdx + 1
	}

	// Plain non-capturing group: (?:...)
	if pattern[i+2] == ':' {
		return 1, i + 3
	}

	// Flag constructs: (?flags) or (?flags:...)
	j := i + 2
	for j < n {
		ch := pattern[j]
		if ch == 'i' || ch == 'm' || ch == 's' || ch == 'U' || ch == '-' {
			j++
			continue
		}
		break
	}

	if j == i+2 {
		return -1, i + 1
	}

	if j < n && pattern[j] == ')' {
		return 0, j + 1
	}

	if j < n && pattern[j] == ':' {
		return 1, j + 1
	}

	return -1, i + 1
}

// tryParseBracedQuantifier tries to parse a Go RE2 escaped quantifier at i
// where pattern[i] == '\\' and pattern[i+1] == '{'.
func tryParseBracedQuantifier(pattern string, i int, sb *strings.Builder) (int, bool) {
	j := i + 2
	n := len(pattern)

	numStart := j
	for j < n && pattern[j] >= '0' && pattern[j] <= '9' {
		j++
	}
	if j == numStart || j >= n {
		return 0, false
	}

	if pattern[j] == '}' {
		sb.WriteByte('{')
		sb.WriteString(pattern[numStart:j])
		sb.WriteByte('}')
		return j + 1 - i, true
	}

	if pattern[j] == ',' {
		j++
		for j < n && pattern[j] >= '0' && pattern[j] <= '9' {
			j++
		}
		if j < n && pattern[j] == '}' {
			sb.WriteByte('{')
			sb.WriteString(pattern[numStart:j])
			sb.WriteByte('}')
			return j + 1 - i, true
		}
	}

	return 0, false
}

// executeInSandbox 在沙箱容器内执行 grep 命令
func (t *GrepTool) executeInSandbox(ctx *ToolContext, pattern, path, include string, ignoreCase bool, contextLines int) (*ToolResult, error) {
	sandboxBase := sandboxBaseDir(ctx)

	searchDir := sandboxBase
	if path != "" {
		if path == sandboxBase || strings.HasPrefix(path, sandboxBase+"/") {
			searchDir = path
		} else if ctx != nil && ctx.WorkspaceRoot != "" && strings.HasPrefix(path, ctx.WorkspaceRoot+"/") {
			// path 是宿主机绝对路径（如 /workspace/xbot/agent/engine.go），
			// 需要转为沙箱内的相对路径（sandboxBase + /xbot/agent/engine.go）
			rel, err := filepath.Rel(ctx.WorkspaceRoot, path)
			if err == nil {
				searchDir = sandboxBase + "/" + rel
			} else {
				searchDir = sandboxBase + "/" + path
			}
		} else {
			searchDir = sandboxBase + "/" + path
		}
	} else if ctx != nil && ctx.CurrentDir != "" {
		// 使用 CurrentDir（PWD 工具优化）
		// CurrentDir 是宿主机路径，需要转换为容器内路径
		if strings.HasPrefix(ctx.CurrentDir, ctx.WorkspaceRoot) {
			rel, err := filepath.Rel(ctx.WorkspaceRoot, ctx.CurrentDir)
			if err == nil {
				searchDir = sandboxBase + "/" + rel
			}
		}
	}

	// 将 Go RE2 pattern 转换为 POSIX ERE pattern（grep -E 兼容）
	erePattern, err := convertGoRE2ToERE(pattern)
	if err != nil {
		// 转换失败，fallback 到本地 Go regexp 执行
		return t.executeLocal(ctx, pattern, path, include, ignoreCase, contextLines)
	}

	// 构建 grep 命令（使用 -E 扩展正则）
	grepCmd := "grep -E"
	if ignoreCase {
		grepCmd += "i" // -Ei
	}
	if contextLines > 0 {
		grepCmd += fmt.Sprintf(" -C %d", contextLines)
	}
	grepCmd += " -rn --binary-files=without-match --exclude-dir=.git --exclude-dir=node_modules"

	// include brace 展开（复用已有函数 expandBracePattern）
	if include != "" {
		patterns := expandBracePattern(include)
		for _, p := range patterns {
			grepCmd += fmt.Sprintf(" --include='%s'", shellEscape(p))
		}
	}

	grepCmd += fmt.Sprintf(" '%s' '%s'", shellEscape(erePattern), shellEscape(searchDir))
	// 不用 pipefail：head 关闭管道时 grep 收到 SIGPIPE (exit 141)，
	// pipefail 会将其传播为错误，导致有效结果被丢弃。
	grepCmd += " | head -200"

	output, err := RunInSandboxWithShell(ctx, grepCmd)
	if err != nil {
		// SIGPIPE (exit 141) 是 head 正常关闭管道导致的，不是真正的错误
		if output != "" && !strings.Contains(output, "No matches found") {
			// 有输出但 err != nil → 很可能是 SIGPIPE，正常返回结果
		} else {
			return NewResultWithTips("No matches found.", "尝试换一个关键词，或检查路径/正则是否正确。"), nil
		}
	}

	if output == "" {
		return NewResultWithTips("No matches found.", "尝试换一个关键词，或检查路径/正则是否正确。"), nil
	}

	// 解析 grep 输出并格式化
	lines := strings.Split(output, "\n")
	var sb strings.Builder
	matchCount := 0
	currentFile := ""

	for _, line := range lines {
		if line == "" {
			continue
		}
		// Parse grep output using regex for precise separator handling.
		// Try match-line format first (filename:linenumber:content), then
		// context-line format (filename-linenumber-content from grep -C).
		var filePath, rest string
		var lineNum int
		if m := grepMatchLineRe.FindStringSubmatch(line); m != nil {
			filePath = m[1]
			fmt.Sscanf(m[2], "%d", &lineNum)
			rest = m[3]
		} else if m := grepContextLineRe.FindStringSubmatch(line); m != nil {
			filePath = m[1]
			fmt.Sscanf(m[2], "%d", &lineNum)
			rest = m[3]
		} else {
			// Skip lines that don't match either format (e.g., "--" group separators)
			continue
		}

		if filePath != currentFile {
			if currentFile != "" {
				sb.WriteString("\n")
			}
			currentFile = filePath
			sb.WriteString("## ")
			sb.WriteString(filePath)
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "%d: %s\n", lineNum, rest)
		matchCount++
	}

	if matchCount == 0 {
		return NewResultWithTips("No matches found.", "尝试换一个关键词，或检查路径/正则是否正确。"), nil
	}

	fmt.Fprintf(&sb, "\n(Found %d match(es))", matchCount)
	return NewResultWithTips(sb.String(), "使用 Read 查看具体匹配行的完整上下文。"), nil
}

// searchFile searches a single file for the pattern and returns matches with optional context lines.
func searchFile(path string, re *regexp.Regexp, contextLines int) ([]grepMatch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Read all lines
	var lines []string
	scanner := bufio.NewScanner(f)
	// Increase buffer for long lines
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		// Quick binary detection: if a line has invalid UTF-8 or null bytes, skip the file
		if !utf8.ValidString(line) || strings.ContainsRune(line, 0) {
			return nil, nil
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// Find matching line indices
	var matchIndices []int
	for i, line := range lines {
		if re.MatchString(line) {
			matchIndices = append(matchIndices, i)
		}
	}

	if len(matchIndices) == 0 {
		return nil, nil
	}

	// Collect matches with context, deduplicating overlapping context lines
	var matches []grepMatch
	emitted := make(map[int]bool)

	for _, idx := range matchIndices {
		start := idx - contextLines
		if start < 0 {
			start = 0
		}
		end := idx + contextLines
		if end >= len(lines) {
			end = len(lines) - 1
		}

		for i := start; i <= end; i++ {
			if emitted[i] {
				continue
			}
			emitted[i] = true
			matches = append(matches, grepMatch{
				File:       path,
				LineNumber: i + 1, // 1-based line numbers
				Line:       lines[i],
			})
		}
	}

	return matches, nil
}

// expandBracePattern expands a simple brace pattern like "*.{go,ts}" into ["*.go", "*.ts"].
// Supports a single level of braces. If no braces are found, returns the pattern as-is.
func expandBracePattern(pattern string) []string {
	openIdx := strings.Index(pattern, "{")
	closeIdx := strings.Index(pattern, "}")

	if openIdx == -1 || closeIdx == -1 || closeIdx < openIdx {
		return []string{pattern}
	}

	prefix := pattern[:openIdx]
	suffix := pattern[closeIdx+1:]
	alternatives := strings.Split(pattern[openIdx+1:closeIdx], ",")

	results := make([]string, 0, len(alternatives))
	for _, alt := range alternatives {
		results = append(results, prefix+strings.TrimSpace(alt)+suffix)
	}
	return results
}

// executeLocal 在本地执行 grep 搜索（非沙箱模式）
func (t *GrepTool) executeLocal(ctx *ToolContext, pattern, path, include string, ignoreCase bool, contextLines int) (*ToolResult, error) {
	// Compile regex
	regexPattern := pattern
	if ignoreCase {
		regexPattern = "(?i)" + regexPattern
	}
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	// Determine base directory
	baseDir := path
	if baseDir == "" {
		// 优先使用 CurrentDir（PWD 工具优化）
		if ctx != nil && ctx.CurrentDir != "" {
			baseDir = ctx.CurrentDir
		} else if ctx != nil && ctx.WorkspaceRoot != "" {
			baseDir = ctx.WorkspaceRoot
		} else if ctx != nil && ctx.WorkingDir != "" {
			baseDir = ctx.WorkingDir
		} else {
			baseDir, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("failed to get working directory: %w", err)
			}
		}
	}

	baseDir, err = ResolveReadPath(ctx, baseDir)
	if err != nil {
		return nil, err
	}

	info, err := os.Stat(baseDir)
	if err != nil {
		return nil, fmt.Errorf("path does not exist: %s", baseDir)
	}

	// Support single file path: if path points to a file, search it directly
	var matches []grepMatch
	truncated := false

	if !info.IsDir() {
		// Single file mode
		if info.Size() > maxGrepFileSize {
			return nil, fmt.Errorf("file too large (>%d bytes): %s", maxGrepFileSize, baseDir)
		}
		fileMatches, err := searchFile(baseDir, re, contextLines)
		if err != nil {
			return nil, fmt.Errorf("failed to search file: %w", err)
		}
		matches = fileMatches
	} else {
		// Expand brace patterns in include (e.g., "*.{go,ts}" -> ["*.go", "*.ts"])
		var includePatterns []string
		if include != "" {
			includePatterns = expandBracePattern(include)
		}

		// Walk the directory and search files
		err = filepath.WalkDir(baseDir, func(walkPath string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // skip inaccessible files
			}

			// Skip hidden directories
			if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}

			// Skip node_modules
			if d.IsDir() && d.Name() == "node_modules" {
				return filepath.SkipDir
			}

			if d.IsDir() {
				return nil
			}

			// Apply include filter
			if len(includePatterns) > 0 {
				matched := false
				for _, p := range includePatterns {
					if m, _ := filepath.Match(p, d.Name()); m {
						matched = true
						break
					}
				}
				if !matched {
					return nil
				}
			}

			// Skip large files
			fileInfo, err := d.Info()
			if err != nil {
				return nil
			}
			if fileInfo.Size() > maxGrepFileSize {
				return nil
			}

			// Search file
			fileMatches, err := searchFile(walkPath, re, contextLines)
			if err != nil {
				return nil // skip files that can't be read
			}

			matches = append(matches, fileMatches...)
			if len(matches) >= maxGrepMatches {
				truncated = true
				matches = matches[:maxGrepMatches]
				return filepath.SkipAll
			}

			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
	}

	if len(matches) == 0 {
		return NewResultWithTips("No matches found.", "尝试换一个关键词，或检查路径/正则是否正确。"), nil
	}

	// Format output
	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d match(es):\n\n", len(matches))

	currentFile := ""
	for _, m := range matches {
		if m.File != currentFile {
			if currentFile != "" {
				sb.WriteString("\n")
			}
			currentFile = m.File
			fmt.Fprintf(&sb, "## %s\n", m.File)
		}
		line := m.Line
		if len(line) > maxGrepLineLength {
			line = line[:maxGrepLineLength] + "..."
		}
		fmt.Fprintf(&sb, "%d: %s\n", m.LineNumber, line)
	}

	if truncated {
		fmt.Fprintf(&sb, "\n(Results truncated. Showing first %d matches.)\n", maxGrepMatches)
	}

	return NewResultWithTips(sb.String(), "使用 Read 查看具体匹配行的完整上下文。"), nil
}
