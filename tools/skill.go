package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"xbot/llm"
)

const skillLoadMaxLines = 300

// MaxSkillSearchResults limits the number of files returned by skill search.
const MaxSkillSearchResults = 20

// SkillTool discovers and reads skills from the workspace.
// In sandbox mode, skills are pre-synced to /workspace/.skills/ (global) and /workspace/skills/ (user).
// Supports two actions: "load" (read content) and "list_files" (list all files with container paths).
type SkillTool struct{}

func (t *SkillTool) Name() string { return "Skill" }
func (t *SkillTool) Description() string {
	return "Load a skill by name or list its files. Use action=load to read SKILL.md (default), action=list_files to get full paths for Shell execution."
}
func (t *SkillTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "name", Type: "string", Description: "The skill name (as shown in available_skills)", Required: true},
		{Name: "action", Type: "string", Description: "Action to perform: load (default) or list_files", Required: false},
		{Name: "file", Type: "string", Description: "File to read within the skill directory (default: SKILL.md, only used with action=load)", Required: false},
	}
}

func (t *SkillTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Name   string `json:"name"`
		Action string `json:"action"`
		File   string `json:"file"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if params.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if params.Action == "" {
		params.Action = "load"
	}
	if params.File == "" {
		params.File = "SKILL.md"
	}

	if strings.Contains(params.Name, "..") || strings.Contains(params.Name, "/") || strings.Contains(params.Name, "\\") {
		return nil, fmt.Errorf("invalid skill name: %s", params.Name)
	}
	if strings.Contains(params.File, "..") {
		return nil, fmt.Errorf("invalid file path: %s", params.File)
	}

	// Trigger lazy sync so global skills are available in the workspace
	EnsureSynced(ctx)

	// Resolve skill directory on the host filesystem
	hostDir, containerBase := t.resolveSkill(ctx, params.Name)
	if hostDir == "" {
		return nil, fmt.Errorf("skill %q not found", params.Name)
	}

	switch params.Action {
	case "load":
		return t.doLoad(hostDir, containerBase, params.File)
	case "list_files":
		return t.doListFiles(hostDir, containerBase)
	default:
		return nil, fmt.Errorf("unknown action %q, expected load or list_files", params.Action)
	}
}

// resolveSkill finds the skill's host-side directory and returns the container-side base path.
// Search order: user private (workspace/skills/) > global synced (workspace/.skills/)
// Returns ("", "") if not found.
func (t *SkillTool) resolveSkill(ctx *ToolContext, name string) (hostDir, containerBase string) {
	type candidate struct {
		hostRoot      string
		containerRoot string
	}
	sandboxBase := sandboxBaseDir(ctx)
	candidates := []candidate{
		{filepath.Join(ctx.WorkspaceRoot, "skills"), filepath.Join(sandboxBase, "skills")},
		{filepath.Join(ctx.WorkspaceRoot, ".skills"), filepath.Join(sandboxBase, ".skills")},
	}

	for _, c := range candidates {
		dir := filepath.Join(c.hostRoot, name)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, filepath.Join(c.containerRoot, name)
		}
	}

	// Fallback: scan for matching frontmatter name
	for _, c := range candidates {
		entries, err := os.ReadDir(c.hostRoot)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillFile := filepath.Join(c.hostRoot, e.Name(), "SKILL.md")
			if fmName, _ := parseSkillName(skillFile); fmName == name {
				return filepath.Join(c.hostRoot, e.Name()), filepath.Join(c.containerRoot, e.Name())
			}
		}
	}
	return "", ""
}

func (t *SkillTool) doLoad(hostDir, containerBase, file string) (*ToolResult, error) {
	target := filepath.Join(hostDir, file)
	data, err := os.ReadFile(target)
	if err != nil {
		return nil, fmt.Errorf("file %q not readable in skill: %w", file, err)
	}

	content := string(data)
	lines := strings.Split(content, "\n")
	if len(lines) > skillLoadMaxLines {
		content = strings.Join(lines[:skillLoadMaxLines], "\n")
		content += fmt.Sprintf("\n\n[Truncated at %d lines. Use file parameter to read specific files, or list_files to see all available files.]", skillLoadMaxLines)
	}
	return NewResult(content), nil
}

func (t *SkillTool) doListFiles(hostDir, containerBase string) (*ToolResult, error) {
	var files []string
	err := filepath.Walk(hostDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(hostDir, path)
		containerPath := filepath.Join(containerBase, rel)
		files = append(files, containerPath)
		if len(files) >= MaxSkillSearchResults {
			return fmt.Errorf("skill has too many files, showing first %d", MaxSkillSearchResults)
		}
		return nil
	})
	if err != nil {
		// If err is our sentinel about too many files, still return the results
		if len(files) > 0 {
			result := strings.Join(files, "\n")
			result += fmt.Sprintf("\n\n... [truncated: showing %d of potentially more files]", len(files))
			return NewResult(result), nil
		}
		return nil, fmt.Errorf("listing skill files: %w", err)
	}
	if len(files) == 0 {
		return NewResult("No files found in skill directory."), nil
	}
	return NewResult(strings.Join(files, "\n")), nil
}

// parseSkillName extracts just the name field from a SKILL.md frontmatter.
func parseSkillName(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return "", nil
	}
	trimmed := strings.TrimSpace(content)
	rest := trimmed[3:]
	endIdx := strings.Index(rest, "\n---")
	if endIdx < 0 {
		return "", nil
	}
	for _, line := range strings.Split(rest[:endIdx], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "name:")), nil
		}
	}
	return "", nil
}
