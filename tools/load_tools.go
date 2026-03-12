package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
)

// LoadToolsTool activates MCP tools by name, making their full parameter schemas
// available in the LLM context. MCP tools are registered with minimal info to save
// context; this tool activates them so their schemas appear in subsequent tool lists.
type LoadToolsTool struct{}

func (t *LoadToolsTool) Name() string { return "load_tools" }

func (t *LoadToolsTool) Description() string {
	return "Activate MCP tools by name. Once activated, their parameter schemas will be available in the tool list. " +
		"Use this before calling unfamiliar MCP tools."
}

func (t *LoadToolsTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "tools",
			Type:        "string",
			Description: "Comma-separated list of tool names to activate (e.g. 'shell,read,edit')",
			Required:    true,
		},
	}
}

type loadToolsArgs struct {
	Tools string `json:"tools"`
}

func (t *LoadToolsTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args loadToolsArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	if strings.TrimSpace(args.Tools) == "" {
		return nil, fmt.Errorf("tools parameter is required")
	}

	if ctx.Registry == nil {
		return nil, fmt.Errorf("tool registry not available")
	}

	var toolNames []string
	for _, name := range strings.Split(args.Tools, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			toolNames = append(toolNames, trimmed)
		}
	}

	sessionKey := ctx.Channel + ":" + ctx.ChatID

	// Get schemas to find which tools exist
	schemas := ctx.Registry.GetToolSchemas(sessionKey, toolNames)
	if len(schemas) == 0 {
		return NewResult(fmt.Sprintf("No tools found for: %s", strings.Join(toolNames, ", "))), nil
	}

	// Activate found tools
	found := make([]string, 0, len(schemas))
	for _, s := range schemas {
		found = append(found, s.ToolName)
	}
	ctx.Registry.ActivateTools(sessionKey, found)

	// Check for tools that weren't found
	foundSet := make(map[string]bool, len(found))
	for _, name := range found {
		foundSet[name] = true
	}
	var notFound []string
	for _, name := range toolNames {
		if !foundSet[name] {
			notFound = append(notFound, name)
		}
	}

	var msg strings.Builder
	fmt.Fprintf(&msg, "Activated: %s", strings.Join(found, ", "))
	if len(notFound) > 0 {
		fmt.Fprintf(&msg, "\nNot found: %s", strings.Join(notFound, ", "))
	}

	return NewResult(msg.String()), nil
}
