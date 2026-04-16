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

const knowledgeDirName = ".xbot/knowledge"

// KnowledgeWriteTool creates or updates a file in the project knowledge directory.
// Read knowledge files with the standard Read tool (path: .xbot/knowledge/xxx.md).
type KnowledgeWriteTool struct{}

func (t *KnowledgeWriteTool) Name() string { return "knowledge_write" }
func (t *KnowledgeWriteTool) Description() string {
	return "Create or update a file in the project knowledge directory (.xbot/knowledge/). Use this to persist project knowledge: pitfalls encountered, architecture decisions, coding conventions, etc. Read existing files with the Read tool (path: .xbot/knowledge/<file>)."
}
func (t *KnowledgeWriteTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "File path relative to knowledge directory (e.g. \"gotchas.md\", \"decisions/api-design.md\"). Subdirectories are auto-created.", Required: true},
		{Name: "content", Type: "string", Description: "File content in markdown format.", Required: true},
	}
}

func (t *KnowledgeWriteTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
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

	// Security: prevent path traversal using filepath.Abs + prefix check
		dir := knowledgeDir(ctx)
		fullPath := filepath.Join(dir, params.Path)
		absFullPath, err := filepath.Abs(fullPath)
		if err != nil {
			return nil, fmt.Errorf("invalid path: %w", err)
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return nil, fmt.Errorf("invalid knowledge directory: %w", err)
		}
		if !strings.HasPrefix(absFullPath, absDir+string(filepath.Separator)) && absFullPath != absDir {
			return nil, fmt.Errorf("path traversal not allowed")
		}

	// Auto-create parent directories
	if err := writeFileSandboxAware(ctx, fullPath, []byte(params.Content)); err != nil {
		return nil, fmt.Errorf("write knowledge file: %w", err)
	}

	log.WithFields(log.Fields{
		"path": params.Path,
		"size": len(params.Content),
	}).Info("Knowledge file written")

	return NewResult(fmt.Sprintf("Written to %s (%d bytes)", params.Path, len(params.Content))), nil
}

// KnowledgeListTool lists the project knowledge directory structure.
type KnowledgeListTool struct{}

func (t *KnowledgeListTool) Name() string { return "knowledge_list" }
func (t *KnowledgeListTool) Description() string {
	return "List all files in the project knowledge directory (.xbot/knowledge/). Discover what knowledge files exist, then read them with the Read tool."
}
func (t *KnowledgeListTool) Parameters() []llm.ToolParam {
	return nil
}

func (t *KnowledgeListTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	dir := knowledgeDir(ctx)
	return listKnowledgeDir(dir)
}

// KnowledgeTools returns all project knowledge tools.
func KnowledgeTools() []Tool {
	return []Tool{
		&KnowledgeWriteTool{},
		&KnowledgeListTool{},
	}
}

// knowledgeDir returns the project knowledge directory path.
func knowledgeDir(ctx *ToolContext) string {
	return filepath.Join(ctx.WorkspaceRoot, knowledgeDirName)
}

// listKnowledgeDir lists files in the knowledge directory.
func listKnowledgeDir(dir string) (*ToolResult, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Skip unreadable entries
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
		return nil, fmt.Errorf("list knowledge directory: %w", err)
	}

	if len(files) == 0 {
		return NewResult("(no knowledge files yet. Use knowledge_write to create some.)"), nil
	}

	sort.Strings(files)
	var sb strings.Builder
	fmt.Fprintf(&sb, "## Knowledge Files\n\nDirectory: %s\n\n", dir)
	for _, f := range files {
		fullPath := filepath.Join(dir, f)
		info, err := os.Stat(fullPath)
		size := ""
		if err == nil {
			size = fmt.Sprintf(" (%d bytes)", info.Size())
		}
		fmt.Fprintf(&sb, "- `%s`%s\n", fullPath, size)
	}
	return NewResult(sb.String()), nil
}

// writeFileSandboxAware writes a file, auto-creating parent directories.
// Uses Sandbox API when in sandbox mode.
func writeFileSandboxAware(ctx *ToolContext, path string, data []byte) error {
	if shouldUseSandbox(ctx) {
		userID := ctx.OriginUserID
		if userID == "" {
			userID = ctx.SenderID
		}
		// Sandbox: create parent dirs then write
		dir := filepath.Dir(path)
		if err := ctx.Sandbox.MkdirAll(ctx.Ctx, dir, 0o755, userID); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		return ctx.Sandbox.WriteFile(ctx.Ctx, path, data, 0o644, userID)
	}
	// OS: create parent dirs then write
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return os.WriteFile(path, data, 0o644)
}
