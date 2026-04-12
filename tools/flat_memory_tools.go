package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"xbot/llm"
	log "xbot/logger"
)

// MemoryReadTool reads a file from the user's flat memory directory (~/.xbot/memory/{tenant}/).
// Only available in flat memory mode.
type MemoryReadTool struct{}

func (t *MemoryReadTool) Name() string { return "memory_read" }
func (t *MemoryReadTool) Description() string {
	return "Read a file from your personal memory directory. Use this to retrieve your own notes, cross-project knowledge, or any non-project-specific information you previously saved."
}
func (t *MemoryReadTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path relative to memory directory (e.g. \"MEMORY.md\", \"knowledge/notes.md\"). Use \"MEMORY.md\" for core memory, \"HISTORY.md\" for event history.", Required: true},
	}
}

func (t *MemoryReadTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path string `json:"path"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// Security: prevent path traversal
	if strings.Contains(params.Path, "..") {
		return nil, fmt.Errorf("path traversal not allowed")
	}

	dir := flatMemoryDir(ctx)
	fullPath := filepath.Join(dir, params.Path)

	data, err := readFileSandboxAware(ctx, fullPath)
	if err != nil {
		return nil, fmt.Errorf("read memory file: %w", err)
	}

	return NewResult(string(data)), nil
}

// MemoryWriteTool creates or updates a file in the user's flat memory directory.
type MemoryWriteTool struct{}

func (t *MemoryWriteTool) Name() string { return "memory_write" }
func (t *MemoryWriteTool) Description() string {
	return "Create or update a file in your personal memory directory. Use this to save personal notes, cross-project knowledge, or any non-project-specific information. MEMORY.md is auto-injected into system prompt (keep it under 1000 chars)."
}
func (t *MemoryWriteTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path relative to memory directory (e.g. \"MEMORY.md\", \"knowledge/notes.md\"). Subdirectories are auto-created.", Required: true},
		{Name: "content", Type: "string", Description: "File content in markdown format.", Required: true},
	}
}

func (t *MemoryWriteTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	// Security: prevent path traversal
	if strings.Contains(params.Path, "..") {
		return nil, fmt.Errorf("path traversal not allowed")
	}

	dir := flatMemoryDir(ctx)
	fullPath := filepath.Join(dir, params.Path)

	if err := writeFileSandboxAware(ctx, fullPath, []byte(params.Content)); err != nil {
		return nil, fmt.Errorf("write memory file: %w", err)
	}

	log.WithFields(log.Fields{
		"path": params.Path,
		"size": len(params.Content),
	}).Info("Memory file written")

	return NewResult(fmt.Sprintf("Written to %s (%d bytes)", params.Path, len(params.Content))), nil
}

// MemoryListTool lists the user's flat memory directory structure.
type MemoryListTool struct{}

func (t *MemoryListTool) Name() string { return "memory_list" }
func (t *MemoryListTool) Description() string {
	return "List all files in your personal memory directory. Use this to discover what memory files exist before reading them."
}
func (t *MemoryListTool) Parameters() []llm.ToolParam {
	return nil
}

func (t *MemoryListTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	dir := flatMemoryDir(ctx)

	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list memory directory: %w", err)
	}

	if len(files) == 0 {
		return NewResult("(no memory files yet. Use memory_write to create some.)"), nil
	}

	sort.Strings(files)
	var sb strings.Builder
	sb.WriteString("## Memory Files\n\n")
	for _, f := range files {
		fullPath := filepath.Join(dir, f)
		info, err := os.Stat(fullPath)
		size := ""
		if err == nil {
			size = fmt.Sprintf(" (%d bytes)", info.Size())
		}
		fmt.Fprintf(&sb, "- `%s`%s\n", f, size)
	}
	return NewResult(sb.String()), nil
}

// FlatMemoryTools returns all flat memory tools for registration.
func FlatMemoryTools() []Tool {
	return []Tool{
		&MemoryReadTool{},
		&MemoryWriteTool{},
		&MemoryListTool{},
	}
}

// flatMemoryDir returns the user's flat memory directory path.
// Uses XbotHome/memory/{tenantID}/ layout (numeric ID, filesystem-safe).
func flatMemoryDir(ctx *ToolContext) string {
	tenantID := ctx.TenantID
	if tenantID == 0 {
		tenantID = 1 // fallback
	}
	home := os.Getenv("XBOT_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".xbot")
		} else {
			home = ".xbot"
		}
	}
	return filepath.Join(home, "memory", fmt.Sprintf("%d", tenantID))
}
