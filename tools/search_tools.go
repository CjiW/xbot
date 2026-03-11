package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
	log "xbot/logger"
)

// SearchToolsTool 搜索可用工具
type SearchToolsTool struct{}

func (t *SearchToolsTool) Name() string { return "search_tools" }

func (t *SearchToolsTool) Description() string {
	return "Search for available tools using semantic similarity. Use this when you need to find tools related to a specific task but don't know their exact names."
}

func (t *SearchToolsTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "query",
			Type:        "string",
			Description: "Search query describing what you want to do (e.g., 'send message to user', 'search wiki', 'create file')",
			Required:    true,
		},
		{
			Name:        "top_k",
			Type:        "number",
			Description: "Maximum number of results to return (default: 5)",
			Required:    false,
		},
	}
}

type searchToolsArgs struct {
	Query string `json:"query"`
	TopK  int    `json:"top_k"`
}

func (t *SearchToolsTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args searchToolsArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return &ToolResult{
			Summary: "Failed to parse arguments",
			Detail:  fmt.Sprintf("Error: %v", err),
		}, nil
	}

	if args.Query == "" {
		return &ToolResult{
			Summary: "Query is required",
			Detail:  "Please provide a search query describing what you want to do.",
		}, nil
	}

	if args.TopK <= 0 {
		args.TopK = 5
	}

	// Get tool indexer from context
	indexer := ctx.ToolIndexer
	if indexer == nil {
		// Fallback: try to get from registry's MCP catalog
		return t.executeFallback(ctx, args.Query, args.TopK)
	}

	// Search using the tool indexer
	results, err := indexer.SearchTools(ctx.Ctx, args.Query, args.TopK)
	if err != nil {
		log.WithError(err).Warn("Tool index search failed, using fallback")
		return t.executeFallback(ctx, args.Query, args.TopK)
	}

	if len(results) == 0 {
		return &ToolResult{
			Summary: "No tools found",
			Detail:  "No tools match your query. Try a different search term or use load_tools to see all available tools.",
		}, nil
	}

	// Format results
	var sb strings.Builder
	sb.WriteString("## Search Results\n\n")
	sb.WriteString("Found the following tools that match your query:\n\n")

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("%d. **%s** (server: %s, source: %s)\n", i+1, r.Name, r.ServerName, r.Source))
		sb.WriteString(fmt.Sprintf("   %s\n\n", r.Description))
	}

	sb.WriteString("To use a tool, first call `load_tools` with the tool name to load it, then you can call the tool.\n")

	return &ToolResult{
		Summary: fmt.Sprintf("Found %d tools matching '%s'", len(results), args.Query),
		Detail:  sb.String(),
		Tips:    "Use `load_tools` to load the tool you want to use, then call it directly.",
	}, nil
}

// executeFallback provides a simple text-based search when tool indexer is not available
func (t *SearchToolsTool) executeFallback(ctx *ToolContext, query string, topK int) (*ToolResult, error) {
	// Get MCP catalog from registry
	sessionKey := ctx.Channel + ":" + ctx.ChatID
	mcpCatalog := ctx.Registry.GetMCPCatalog(sessionKey)
	toolGroups := ctx.Registry.GetToolGroups()

	var allTools []string
	var toolDescriptions []string

	// Collect tool groups
	for _, group := range toolGroups {
		for _, toolName := range group.ToolNames {
			allTools = append(allTools, toolName)
			toolDescriptions = append(toolDescriptions, group.Name+": "+group.Instructions)
		}
	}

	// Collect MCP tools
	for _, entry := range mcpCatalog {
		for _, toolName := range entry.ToolNames {
			fullName := fmt.Sprintf("mcp_%s_%s", entry.Name, toolName)
			allTools = append(allTools, fullName)
			desc := entry.Name + " MCP server"
			if entry.Instructions != "" {
				desc += ": " + entry.Instructions
			}
			toolDescriptions = append(toolDescriptions, desc)
		}
	}

	// Simple text matching
	queryLower := strings.ToLower(query)
	var matched []struct {
		name        string
		description string
		score       int
	}

	for i, toolName := range allTools {
		toolLower := strings.ToLower(toolName)
		desc := ""
		if i < len(toolDescriptions) {
			desc = toolDescriptions[i]
		}

		// Score based on substring match
		score := 0
		if strings.Contains(toolLower, queryLower) {
			score = 100
		} else if strings.Contains(queryLower, toolLower) {
			score = 80
		} else if desc != "" && strings.Contains(strings.ToLower(desc), queryLower) {
			score = 60
		}

		if score > 0 {
			matched = append(matched, struct {
				name        string
				description string
				score       int
			}{toolName, desc, score})
		}
	}

	// Sort by score descending
	for i := 0; i < len(matched)-1; i++ {
		for j := i + 1; j < len(matched); j++ {
			if matched[j].score > matched[i].score {
				matched[i], matched[j] = matched[j], matched[i]
			}
		}
	}

	if len(matched) == 0 {
		return &ToolResult{
			Summary: "No tools found",
			Detail:  "No tools match your query. Try a different search term or use load_tools to see all available tools.",
		}, nil
	}

	// Limit results
	if len(matched) > topK {
		matched = matched[:topK]
	}

	// Format results
	var sb strings.Builder
	sb.WriteString("## Search Results\n\n")
	sb.WriteString("Found the following tools that match your query:\n\n")

	for i, m := range matched {
		sb.WriteString(fmt.Sprintf("%d. **%s**\n", i+1, m.name))
		if m.description != "" {
			sb.WriteString(fmt.Sprintf("   %s\n\n", m.description))
		}
	}

	sb.WriteString("To use a tool, first call `load_tools` with the tool name to load it, then you can call the tool.\n")

	return &ToolResult{
		Summary: fmt.Sprintf("Found %d tools matching '%s'", len(matched), query),
		Detail:  sb.String(),
		Tips:    "Use `load_tools` to load the tool you want to use, then call it directly.",
	}, nil
}
