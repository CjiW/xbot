package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"xbot/llm"
)

// LoadMCPToolsUsageTool 按需加载 MCP 工具的详细参数定义和使用说明。
// 使用背景：为节省 LLM 上下文，MCP 工具在注册时仅暴露名称和简介，
// 不附带完整参数 schema。调用此工具后，LLM 可获得完整的参数列表，
// 从而正确构造工具调用参数。
type LoadMCPToolsUsageTool struct{}

func (t *LoadMCPToolsUsageTool) Name() string { return "load_mcp_tools_usage" }

func (t *LoadMCPToolsUsageTool) Description() string {
	return "Load detailed usage information and parameter schemas for MCP tools. " +
		"Call this before using an MCP tool to understand its required and optional parameters. " +
		"Provide specific tool names to query, or leave empty to list all available MCP tools."
}

func (t *LoadMCPToolsUsageTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "tools",
			Type:        "string",
			Description: "Comma-separated list of MCP tool names to query (e.g. 'mcp_server_tool1,mcp_server_tool2'). Leave empty to list all available MCP tools and their servers.",
			Required:    false,
		},
	}
}

type loadMCPToolsUsageArgs struct {
	Tools string `json:"tools"`
}

func (t *LoadMCPToolsUsageTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args loadMCPToolsUsageArgs
	if input != "" && input != "{}" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return nil, fmt.Errorf("parse arguments: %w", err)
		}
	}

	if ctx.Registry == nil {
		return nil, fmt.Errorf("tool registry not available")
	}

	sessionKey := ctx.Channel + ":" + ctx.ChatID

	// 没有指定工具名时，返回所有 MCP 工具的摘要列表
	if strings.TrimSpace(args.Tools) == "" {
		return t.listAllMCPTools(ctx.Registry, sessionKey)
	}

	// 解析工具名列表
	var toolNames []string
	for _, name := range strings.Split(args.Tools, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			toolNames = append(toolNames, trimmed)
		}
	}

	return t.loadToolSchemas(ctx.Registry, sessionKey, toolNames)
}

// listAllMCPTools 列出所有可用 MCP 工具的简要信息
func (t *LoadMCPToolsUsageTool) listAllMCPTools(registry *Registry, sessionKey string) (*ToolResult, error) {
	catalog := registry.GetMCPCatalog(sessionKey)
	if len(catalog) == 0 {
		return NewResult("No MCP servers are currently connected."), nil
	}

	var sb strings.Builder
	sb.WriteString("## Available MCP Tools\n\n")

	// 按服务器名排序保证输出稳定
	sort.Slice(catalog, func(i, j int) bool {
		return catalog[i].Name < catalog[j].Name
	})

	for _, entry := range catalog {
		fmt.Fprintf(&sb, "### MCP Server: %s\n", entry.Name)
		if entry.Instructions != "" {
			fmt.Fprintf(&sb, "%s\n\n", entry.Instructions)
		}
		if len(entry.ToolNames) == 0 {
			sb.WriteString("_(no tools)_\n\n")
			continue
		}
		sb.WriteString("Tools:\n")
		for _, toolName := range entry.ToolNames {
			fmt.Fprintf(&sb, "  - mcp_%s_%s\n", entry.Name, toolName)
		}
		sb.WriteByte('\n')
	}

	sb.WriteString("Use `load_mcp_tools_usage` with specific tool names to get parameter details.")
	return NewResult(sb.String()), nil
}

// loadToolSchemas 返回指定工具的完整参数 schema
func (t *LoadMCPToolsUsageTool) loadToolSchemas(registry *Registry, sessionKey string, toolNames []string) (*ToolResult, error) {
	schemas := registry.GetMCPToolSchemas(sessionKey, toolNames)

	if len(schemas) == 0 {
		return NewResult(fmt.Sprintf("No MCP tool schemas found for: %s\n\nUse load_mcp_tools_usage with no arguments to list all available MCP tools.",
			strings.Join(toolNames, ", "))), nil
	}

	// 按工具名排序
	sort.Slice(schemas, func(i, j int) bool {
		return schemas[i].ToolName < schemas[j].ToolName
	})

	// 找出未找到的工具（有请求但无 schema）
	found := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		found[s.ToolName] = true
	}
	var notFound []string
	for _, name := range toolNames {
		if !found[name] {
			notFound = append(notFound, name)
		}
	}

	var sb strings.Builder
	sb.WriteString("## MCP Tool Parameter Details\n\n")

	for _, schema := range schemas {
		fmt.Fprintf(&sb, "### %s\n", schema.ToolName)
		fmt.Fprintf(&sb, "**Server:** %s\n\n", schema.ServerName)
		if schema.Description != "" {
			fmt.Fprintf(&sb, "**Description:** %s\n\n", schema.Description)
		}

		if len(schema.Params) == 0 {
			sb.WriteString("_No parameters required._\n\n")
			continue
		}

		sb.WriteString("**Parameters:**\n\n")
		for _, p := range schema.Params {
			req := ""
			if p.Required {
				req = " *(required)*"
			}
			fmt.Fprintf(&sb, "- `%s` (%s)%s", p.Name, p.Type, req)
			if p.Description != "" {
				fmt.Fprintf(&sb, ": %s", p.Description)
			}
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n')
	}

	if len(notFound) > 0 {
		fmt.Fprintf(&sb, "_Tools not found: %s_\n", strings.Join(notFound, ", "))
	}

	return NewResult(sb.String()), nil
}
