package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"xbot/llm"
)

// GlobTool 文件模式匹配搜索工具
type GlobTool struct{}

func (t *GlobTool) Name() string {
	return "Glob"
}

func (t *GlobTool) Description() string {
	return `Search for files matching a glob pattern.
Supports standard glob patterns including ** for recursive directory matching.
Parameters (JSON):
  - pattern: string, the glob pattern to match (e.g., "**/*.go", "src/**/*.ts", "*.txt")
  - path: string, optional, the base directory to search in (defaults to current working directory)
Example: {"pattern": "**/*.go", "path": "/project"}`
}

func (t *GlobTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "pattern", Type: "string", Description: "The glob pattern to match files against (supports ** for recursive matching)", Required: true},
		{Name: "path", Type: "string", Description: "The base directory to search in (defaults to current working directory)", Required: false},
	}
}

func (t *GlobTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}

	// Determine base directory
	baseDir := params.Path
	if baseDir == "" {
		// 优先使用 ToolContext 中的工作目录
		if ctx != nil && ctx.WorkingDir != "" {
			baseDir = ctx.WorkingDir
		} else {
			var err error
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

	// Convert to absolute path
	baseDir, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve base directory: %w", err)
	}

	// Verify base directory exists
	info, err := os.Stat(baseDir)
	if err != nil {
		return nil, fmt.Errorf("base directory does not exist: %s", baseDir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", baseDir)
	}

	var matches []string

	if strings.Contains(params.Pattern, "**") {
		// Handle ** patterns with recursive walk
		matches, err = globWithDoublestar(baseDir, params.Pattern)
		if err != nil {
			return nil, fmt.Errorf("glob search failed: %w", err)
		}
	} else {
		// Use standard filepath.Glob for simple patterns
		fullPattern := filepath.Join(baseDir, params.Pattern)
		matches, err = filepath.Glob(fullPattern)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", err)
		}
	}

	sort.Strings(matches)

	if len(matches) == 0 {
		return NewResult("No files matched the pattern."), nil
	}

	// Limit results to avoid excessive output
	const maxResults = 200
	truncated := false
	if len(matches) > maxResults {
		matches = matches[:maxResults]
		truncated = true
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d matching file(s):\n", len(matches))
	for _, match := range matches {
		sb.WriteString(match)
		sb.WriteString("\n")
	}
	if truncated {
		fmt.Fprintf(&sb, "\n(Results truncated. Showing first %d matches.)\n", maxResults)
	}

	return NewResult(sb.String()), nil
}

// globWithDoublestar handles glob patterns containing ** for recursive directory matching.
// It splits the pattern at ** boundaries, walks the directory tree, and matches each
// path segment against the corresponding pattern part.
func globWithDoublestar(baseDir, pattern string) ([]string, error) {
	var matches []string

	// Normalize the pattern separators
	pattern = filepath.FromSlash(pattern)

	err := filepath.WalkDir(baseDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip files/dirs we can't access
		}

		// Get the path relative to baseDir for matching
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return nil
		}

		// Skip the base directory itself
		if relPath == "." {
			return nil
		}

		// Skip hidden directories (starting with .)
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}

		// Skip node_modules
		if d.IsDir() && d.Name() == "node_modules" {
			return filepath.SkipDir
		}

		// Match the relative path against the pattern
		if matchDoublestar(pattern, relPath) {
			matches = append(matches, path)
		}

		return nil
	})

	return matches, err
}

// matchDoublestar checks if a path matches a pattern that may contain ** wildcards.
// ** matches zero or more directory levels.
func matchDoublestar(pattern, path string) bool {
	// Split pattern and path into segments
	patternParts := splitPath(pattern)
	pathParts := splitPath(path)

	return matchParts(patternParts, pathParts)
}

// splitPath splits a file path into its component parts.
func splitPath(path string) []string {
	path = filepath.ToSlash(path)
	parts := strings.Split(path, "/")
	// Filter out empty parts
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// matchParts recursively matches pattern parts against path parts.
// Supports ** (matches zero or more directories) and standard glob wildcards (* and ?).
func matchParts(patternParts, pathParts []string) bool {
	for len(patternParts) > 0 {
		part := patternParts[0]

		if part == "**" {
			// Remove the ** from pattern
			patternParts = patternParts[1:]

			// If ** is the last element, it matches everything remaining
			if len(patternParts) == 0 {
				return true
			}

			// Try matching ** against zero or more path segments
			for i := 0; i <= len(pathParts); i++ {
				if matchParts(patternParts, pathParts[i:]) {
					return true
				}
			}
			return false
		}

		// No more path parts but still have pattern parts
		if len(pathParts) == 0 {
			return false
		}

		// Match current parts using filepath.Match
		matched, err := filepath.Match(part, pathParts[0])
		if err != nil || !matched {
			return false
		}

		patternParts = patternParts[1:]
		pathParts = pathParts[1:]
	}

	// Pattern exhausted, path must also be exhausted
	return len(pathParts) == 0
}
