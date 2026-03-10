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

	if strings.TrimSpace(args.Tools) == "" {
		return t.listAllTools(ctx.Registry, sessionKey)
	}

	var toolNames []string
	for _, name := range strings.Split(args.Tools, ",") {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			toolNames = append(toolNames, trimmed)
		}
	}

	schemas := ctx.Registry.GetToolSchemas(sessionKey, toolNames)

	result := t.formatSchemaResult(schemas, toolNames)

	// 只激活实际找到 schema 的工具
	if len(schemas) > 0 {
		found := make([]string, 0, len(schemas))
		for _, s := range schemas {
			found = append(found, s.ToolName)
		}
		ctx.Registry.ActivateTools(sessionKey, found)
	}

	return result, nil
}

// listAllTools 列出所有可加载工具（内置 + MCP）的简要信息
func (t *LoadMCPToolsUsageTool) listAllTools(registry *Registry, sessionKey string) (*ToolResult, error) {
	schemas := registry.GetToolSchemas(sessionKey, nil)
	if len(schemas) == 0 {
		return NewResult("No loadable tools available."), nil
	}

	sort.Slice(schemas, func(i, j int) bool {
		return schemas[i].ToolName < schemas[j].ToolName
	})

	// 按来源分组：内置 vs MCP server
	var builtinNames []string
	mcpByServer := make(map[string][]string)
	for _, s := range schemas {
		if s.ServerName == "" {
			builtinNames = append(builtinNames, s.ToolName)
		} else {
			mcpByServer[s.ServerName] = append(mcpByServer[s.ServerName], s.ToolName)
		}
	}

	var sb strings.Builder
	sb.WriteString("## Available Tools\n\n")

	if len(builtinNames) > 0 {
		sb.WriteString("### Built-in Tools\n")
		fmt.Fprintf(&sb, "Tools: %s\n\n", strings.Join(builtinNames, ", "))
	}

	// MCP 服务器工具
	catalog := registry.GetMCPCatalog(sessionKey)
	sort.Slice(catalog, func(i, j int) bool { return catalog[i].Name < catalog[j].Name })
	for _, entry := range catalog {
		fmt.Fprintf(&sb, "### MCP Server: %s\n", entry.Name)
		if entry.Instructions != "" {
			fmt.Fprintf(&sb, "%s\n\n", entry.Instructions)
		}
		if tools, ok := mcpByServer[entry.Name]; ok {
			fmt.Fprintf(&sb, "Tools: %s\n\n", strings.Join(tools, ", "))
		}
	}

	sb.WriteString("Call `load_mcp_tools_usage` with specific tool names to load them (e.g. `tools=\"shell,read,edit\"`).\n")
	return NewResult(sb.String()), nil
}

// formatSchemaResult 格式化 schema 查询结果为可读文本
func (t *LoadMCPToolsUsageTool) formatSchemaResult(schemas []ToolSchema, requestedNames []string) *ToolResult {
	if len(schemas) == 0 {
		return NewResult(fmt.Sprintf("No tool schemas found for: %s\n\nUse load_mcp_tools_usage with no arguments to list all available tools.",
			strings.Join(requestedNames, ", ")))
	}

	sort.Slice(schemas, func(i, j int) bool {
		return schemas[i].ToolName < schemas[j].ToolName
	})

	found := make(map[string]bool, len(schemas))
	for _, s := range schemas {
		found[s.ToolName] = true
	}
	var notFound []string
	for _, name := range requestedNames {
		if !found[name] {
			notFound = append(notFound, name)
		}
	}

	var sb strings.Builder
	sb.WriteString("## Tool Parameter Details\n\n")
	sb.WriteString("The following tools are now loaded and available for calling.\n\n")

	for _, schema := range schemas {
		fmt.Fprintf(&sb, "### %s\n", schema.ToolName)
		if schema.ServerName != "" {
			fmt.Fprintf(&sb, "**Server:** %s\n\n", schema.ServerName)
		}
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

	return NewResult(sb.String())
}
