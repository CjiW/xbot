package tools

import (
	"bufio"
	"encoding/json"
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
Supports regular expressions. Returns matching lines with file paths and line numbers.
Parameters (JSON):
  - pattern: string, the regex pattern to search for (e.g., "func main", "TODO|FIXME", "error\.(New|Wrap)")
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

func (t *GrepTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params grepParams
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	// Compile regex
	regexPattern := params.Pattern
	if params.IgnoreCase {
		regexPattern = "(?i)" + regexPattern
	}
	re, err := regexp.Compile(regexPattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	// Determine base directory
	baseDir := params.Path
	if baseDir == "" {
		// 优先使用 ToolContext 中的工作目录
		if ctx != nil && ctx.WorkingDir != "" {
			baseDir = ctx.WorkingDir
		} else {
			baseDir, err = os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("failed to get working directory: %w", err)
			}
		}
	} else if ctx != nil && ctx.WorkingDir != "" {
		// 安全路径解析，防止目录逃逸
		var err error
		baseDir, err = ResolveSafePath(ctx.WorkingDir, baseDir)
		if err != nil {
			return nil, fmt.Errorf("invalid path: %w", err)
		}
	}

	baseDir, err = filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base directory: %w", err)
	}

	info, err := os.Stat(baseDir)
	if err != nil {
		return nil, fmt.Errorf("base directory does not exist: %s", baseDir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", baseDir)
	}

	// Expand brace patterns in include (e.g., "*.{go,ts}" -> ["*.go", "*.ts"])
	var includePatterns []string
	if params.Include != "" {
		includePatterns = expandBracePattern(params.Include)
	}

	contextLines := params.ContextLines
	if contextLines < 0 {
		contextLines = 0
	}

	// Walk the directory and search files
	var matches []grepMatch
	truncated := false

	err = filepath.WalkDir(baseDir, func(path string, d os.DirEntry, walkErr error) error {
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
			for _, pattern := range includePatterns {
				if m, _ := filepath.Match(pattern, d.Name()); m {
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
		fileMatches, err := searchFile(path, re, contextLines)
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

	if len(matches) == 0 {
		return NewResult("No matches found."), nil
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

	return NewResult(sb.String()), nil
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
