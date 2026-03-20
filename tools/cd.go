package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"xbot/llm"

	log "xbot/logger"
)

// CdTool changes the agent's working directory (persisted across tool calls).
type CdTool struct{}

func (t *CdTool) Name() string {
	return "Cd"
}

func (t *CdTool) Description() string {
	return `Change the current working directory. The new directory persists across subsequent tool calls (Shell, Read, Glob, Grep, etc.).
Parameters (JSON):
  - path: string, the directory to change to (relative or absolute)
Example: {"path": "src/components"}`
}

func (t *CdTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "path", Type: "string", Description: "The directory to change to", Required: true},
	}
}

func (t *CdTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	params, err := parseToolArgs[struct {
		Path string `json:"path"`
	}](input)
	if err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Path == "" {
		return nil, fmt.Errorf("path is required")
	}

	if ctx != nil && ctx.SandboxEnabled && ctx.WorkspaceRoot != "" {
		return t.executeInSandbox(ctx, params.Path)
	}

	return t.executeLocal(ctx, params.Path)
}

func (t *CdTool) executeLocal(ctx *ToolContext, dir string) (*ToolResult, error) {
	// Resolve relative paths against CurrentDir, then WorkspaceRoot
	target := dir
	if !filepath.IsAbs(target) {
		base := ""
		if ctx != nil && ctx.CurrentDir != "" {
			base = ctx.CurrentDir
		} else if ctx != nil && ctx.WorkspaceRoot != "" {
			base = ctx.WorkspaceRoot
		} else if ctx != nil {
			base = ctx.WorkingDir
		}
		if base != "" {
			target = filepath.Join(base, target)
		}
	}

	target = filepath.Clean(target)

	info, err := os.Stat(target)
	if err != nil {
		return nil, fmt.Errorf("directory not found: %s", dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dir)
	}

	if ctx != nil && ctx.SetCurrentDir != nil {
		ctx.CurrentDir = target
		ctx.SetCurrentDir(target)
	}

	log.WithField("dir", target).Debug("Working directory changed")
	return NewResult(fmt.Sprintf("Changed directory to %s", target)), nil
}

func (t *CdTool) executeInSandbox(ctx *ToolContext, dir string) (*ToolResult, error) {
	// Resolve relative paths against CurrentDir in sandbox
	target := dir
	if !filepath.IsAbs(target) {
		base := ""
		if ctx.CurrentDir != "" {
			// CurrentDir in sandbox mode stores sandbox paths (e.g. /workspace/src)
			base = ctx.CurrentDir
		} else {
			base = ctx.SandboxWorkDir
		}
		if base != "" {
			target = filepath.Join(base, target)
		}
	}
	target = filepath.Clean(target)

	// Verify directory exists inside the sandbox
	cmd := fmt.Sprintf("test -d '%s' && echo ok", strings.ReplaceAll(target, "'", "'\\''"))
	output, err := RunInSandboxWithShell(ctx, cmd)
	if err != nil || strings.TrimSpace(output) != "ok" {
		return nil, fmt.Errorf("directory not found in sandbox: %s", dir)
	}

	if ctx.SetCurrentDir != nil {
		ctx.CurrentDir = target
		ctx.SetCurrentDir(target)
	}

	log.WithField("dir", target).Debug("Working directory changed")
	return NewResult(fmt.Sprintf("Changed directory to %s", target)), nil
}
