package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
)

// SearchToolsTool 搜索可用工具
type SearchToolsTool struct{}

func (t *SearchToolsTool) Name() string { return "search_tools" }

func (t *SearchToolsTool) Description() string {
	return "Search for available tools using semantic similarity in english. Use this when you need to find tools related to a specific task.\n\n" +
		"DO NOT: use this tool for vague queries like 'what tools are available' or 'list tools'\n"
}

func (t *SearchToolsTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "query",
			Type:        "string",
			Description: "Search query describing demanded task (e.g., 'send message to user', 'search wiki', 'create file', 'github issues management')",
			Required:    true,
		},
		{
			Name:        "top_k",
			Type:        "number",
			Description: "Maximum number of results to return (default: 20)",
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
		args.TopK = 20
	}

	// Try to get tool indexer from context (Letta mode)
	indexer := ctx.ToolIndexer
	if indexer != nil {
		// Try to cast to LettaMemory to access both global (tenant 0) and personal tools
		if lm, ok := indexer.(*letta.LettaMemory); ok {
			// Search global tools first (tenant 0), filter by current channel
			globalResults, err := lm.SearchToolsForTenant(ctx.Ctx, 0, args.Query, args.TopK, ctx.Channel)
			if err != nil {
				log.WithError(err).Warn("Global tool index search failed")
			}

			// Then search personal tools (user's tenant), filter by current channel
			personalResults, err := lm.SearchToolsForTenant(ctx.Ctx, lm.TenantID(), args.Query, args.TopK, ctx.Channel)
			if err != nil {
				log.WithError(err).Warn("Personal tool index search failed")
			}

			// Merge results, prefer personal over global for same tools
			allResults := append(personalResults, globalResults...)

			if len(allResults) > 0 {
				return t.formatResultsDedup(allResults, args.Query)
			}
		} else {
			// Generic ToolIndexer (flat mode)
			results, err := indexer.SearchTools(ctx.Ctx, args.Query, args.TopK)
			if err != nil {
				log.WithError(err).Warn("Tool index search failed, using fallback")
			} else if len(results) > 0 {
				return t.formatResults(results, args.Query)
			}
		}
	}

	// Fallback: use registry's MCP catalog for text-based search
	return t.executeFallback(ctx, args.Query, args.TopK)
}

func (t *SearchToolsTool) formatResults(results []memory.ToolIndexEntry, query string) (*ToolResult, error) {
	var sb strings.Builder
	sb.WriteString("## Search Results\n\n")
	sb.WriteString("Found the following tools that match your query:\n\n")

	for i, r := range results {
		// Name is already the correct loadable name (e.g., mcp_linear_list_issues or feishu_search_wiki)
		fmt.Fprintf(&sb, "%d. **%s** (server: %s, source: %s)\n", i+1, r.Name, r.ServerName, r.Source)
		fmt.Fprintf(&sb, "   %s\n\n", r.Description)
	}

	sb.WriteString("To use a tool, call `load_tools` with the tool name to load it, then you can call the tool.\n")

	return &ToolResult{
		Summary: fmt.Sprintf("Found %d tools matching '%s'", len(results), query),
		Detail:  sb.String(),
		Tips:    "Use `load_tools` with the tool name to use the tool.",
	}, nil
}

// formatResultsDedup deduplicates results by tool name and formats them.
func (t *SearchToolsTool) formatResultsDedup(results []memory.ToolIndexEntry, query string) (*ToolResult, error) {
	// Deduplicate by tool name, prefer personal over global
	seen := make(map[string]memory.ToolIndexEntry)
	for _, r := range results {
		key := r.Name + "@" + r.ServerName
		if _, exists := seen[key]; !exists {
			seen[key] = r
		}
	}

	var deduped []memory.ToolIndexEntry
	for _, r := range results {
		key := r.Name + "@" + r.ServerName
		if existing, ok := seen[key]; ok {
			deduped = append(deduped, existing)
			delete(seen, key)
		}
	}

	return t.formatResults(deduped, query)
}

// executeFallback provides a simple text-based search when tool indexer is not available
func (t *SearchToolsTool) executeFallback(ctx *ToolContext, query string, topK int) (*ToolResult, error) {
	// Get MCP catalog from registry
	sessionKey := ctx.Channel + ":" + ctx.ChatID
	mcpCatalog := ctx.Registry.GetMCPCatalog(sessionKey)
	// 使用渠道过滤的工具组
	toolGroups := ctx.Registry.GetToolGroupsForChannel(ctx.Channel)

	log.WithFields(log.Fields{
		"session":    sessionKey,
		"mcpCount":   len(mcpCatalog),
		"groupCount": len(toolGroups),
		"query":      query,
		"channel":    ctx.Channel,
	}).Warn("search_tools fallback: checking catalogs")

	var allTools []string
	var toolDescriptions []string

	// Collect tool groups (already filtered by channel)
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
		// Check if it's already a full MCP tool name
		loadableName := m.name
		if !strings.HasPrefix(m.name, "mcp_") && !strings.HasPrefix(m.name, "feishu_") {
			// This might be a server tool without prefix, try to detect from description
			// Just show as-is, user can try loading
			loadableName = m.name
		}
		fmt.Fprintf(&sb, "%d. **%s**\n", i+1, loadableName)
		if m.description != "" {
			fmt.Fprintf(&sb, "   %s\n\n", m.description)
		}
	}

	sb.WriteString("To use a tool, first call `load_tools` with the loadable name to load it, then you can call the tool.\n")

	return &ToolResult{
		Summary: fmt.Sprintf("Found %d tools matching '%s'", len(matched), query),
		Detail:  sb.String(),
		Tips:    "Use `load_tools` to load the tool you want to use, then call it directly.",
	}, nil
}
