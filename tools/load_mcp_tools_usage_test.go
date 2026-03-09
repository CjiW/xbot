package tools

import (
	"strings"
	"testing"

	"xbot/llm"
)

// mockMCPTool simulates a registered MCPRemoteTool for testing purposes.
// It satisfies the mcpSchemaProvider interface.
type mockMCPTool struct {
	name        string
	server      string
	description string
	params      []llm.ToolParam
}

func (m *mockMCPTool) Name() string                { return "mcp_" + m.server + "_" + m.name }
func (m *mockMCPTool) Description() string         { return "[MCP:" + m.server + "] " + m.description }
func (m *mockMCPTool) Parameters() []llm.ToolParam { return nil } // stub mode
func (m *mockMCPTool) Execute(_ *ToolContext, _ string) (*ToolResult, error) {
	return NewResult("ok"), nil
}
func (m *mockMCPTool) fullDescription() string     { return m.description }
func (m *mockMCPTool) fullParams() []llm.ToolParam { return m.params }
func (m *mockMCPTool) mcpServerName() string       { return m.server }

func TestLoadMCPToolsUsageTool_Name(t *testing.T) {
	tool := &LoadMCPToolsUsageTool{}
	if tool.Name() != "load_mcp_tools_usage" {
		t.Errorf("Expected 'load_mcp_tools_usage', got '%s'", tool.Name())
	}
}

func TestLoadMCPToolsUsageTool_Description(t *testing.T) {
	tool := &LoadMCPToolsUsageTool{}
	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestLoadMCPToolsUsageTool_Parameters(t *testing.T) {
	tool := &LoadMCPToolsUsageTool{}
	params := tool.Parameters()
	if len(params) != 1 {
		t.Errorf("Expected 1 parameter, got %d", len(params))
	}
	if params[0].Name != "tools" {
		t.Errorf("Expected parameter named 'tools', got '%s'", params[0].Name)
	}
}

func TestLoadMCPToolsUsageTool_ListAll(t *testing.T) {
	registry := NewRegistry()

	// Register a mock MCP tool
	registry.Register(&mockMCPTool{
		name:        "search",
		server:      "github",
		description: "Search GitHub",
		params: []llm.ToolParam{
			{Name: "query", Type: "string", Required: true, Description: "Search query"},
		},
	})

	// Set catalog
	registry.SetGlobalMCPCatalog([]MCPServerCatalogEntry{
		{
			Name:         "github",
			Instructions: "GitHub MCP server",
			ToolNames:    []string{"search"},
		},
	})

	tool := &LoadMCPToolsUsageTool{}
	ctx := &ToolContext{
		Registry: registry,
		Channel:  "test",
		ChatID:   "chat1",
	}

	result, err := tool.Execute(ctx, `{}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "github") {
		t.Errorf("Expected 'github' in result, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "mcp_github_search") {
		t.Errorf("Expected 'mcp_github_search' in result, got: %s", result.Summary)
	}
}

func TestLoadMCPToolsUsageTool_GetSchemas(t *testing.T) {
	registry := NewRegistry()

	registry.Register(&mockMCPTool{
		name:        "list_repos",
		server:      "github",
		description: "List GitHub repositories",
		params: []llm.ToolParam{
			{Name: "org", Type: "string", Required: true, Description: "Organization name"},
			{Name: "limit", Type: "integer", Required: false, Description: "Max results"},
		},
	})

	tool := &LoadMCPToolsUsageTool{}
	ctx := &ToolContext{
		Registry: registry,
		Channel:  "test",
		ChatID:   "chat1",
	}

	result, err := tool.Execute(ctx, `{"tools": "mcp_github_list_repos"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !strings.Contains(result.Summary, "mcp_github_list_repos") {
		t.Errorf("Expected tool name in result, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "org") {
		t.Errorf("Expected 'org' parameter in result, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "limit") {
		t.Errorf("Expected 'limit' parameter in result, got: %s", result.Summary)
	}
}

func TestLoadMCPToolsUsageTool_NotFound(t *testing.T) {
	registry := NewRegistry()
	tool := &LoadMCPToolsUsageTool{}
	ctx := &ToolContext{
		Registry: registry,
		Channel:  "test",
		ChatID:   "chat1",
	}

	result, err := tool.Execute(ctx, `{"tools": "mcp_nonexistent_tool"}`)
	if err != nil {
		t.Fatalf("Execute should not fail for missing tool, got: %v", err)
	}
	if result.Summary == "" {
		t.Error("Expected non-empty result for missing tool")
	}
}

func TestRegistry_GetMCPCatalog(t *testing.T) {
	registry := NewRegistry()

	catalog := []MCPServerCatalogEntry{
		{
			Name:         "server1",
			Instructions: "Server 1 instructions",
			ToolNames:    []string{"tool1", "tool2"},
		},
	}
	registry.SetGlobalMCPCatalog(catalog)

	result := registry.GetMCPCatalog("test:chat")
	if len(result) != 1 {
		t.Errorf("Expected 1 catalog entry, got %d", len(result))
	}
	if result[0].Name != "server1" {
		t.Errorf("Expected 'server1', got '%s'", result[0].Name)
	}
	if result[0].Instructions != "Server 1 instructions" {
		t.Errorf("Expected 'Server 1 instructions', got '%s'", result[0].Instructions)
	}
}

func TestRegistry_GetMCPToolSchemas(t *testing.T) {
	registry := NewRegistry()

	// Register a mock MCP tool
	registry.Register(&mockMCPTool{
		name:        "search",
		server:      "github",
		description: "Search GitHub repos",
		params: []llm.ToolParam{
			{Name: "query", Type: "string", Required: true},
		},
	})

	schemas := registry.GetMCPToolSchemas("test:chat", []string{"mcp_github_search"})
	if len(schemas) != 1 {
		t.Errorf("Expected 1 schema, got %d", len(schemas))
	}
	if schemas[0].ToolName != "mcp_github_search" {
		t.Errorf("Expected 'mcp_github_search', got '%s'", schemas[0].ToolName)
	}
	if schemas[0].ServerName != "github" {
		t.Errorf("Expected 'github', got '%s'", schemas[0].ServerName)
	}
	if len(schemas[0].Params) != 1 {
		t.Errorf("Expected 1 param, got %d", len(schemas[0].Params))
	}
}

func TestMCPRemoteTool_StubMode(t *testing.T) {
	// Verify that MCPRemoteTool returns nil parameters in stub mode
	registry := NewRegistry()
	registry.Register(&mockMCPTool{
		name:        "search",
		server:      "github",
		description: "Search",
		params: []llm.ToolParam{
			{Name: "query", Type: "string", Required: true},
		},
	})

	tool, ok := registry.Get("mcp_github_search")
	if !ok {
		t.Fatal("Tool not found")
	}

	// In stub mode, Parameters() should return nil
	params := tool.Parameters()
	if params != nil {
		t.Errorf("Stub mode: expected nil parameters, got %v", params)
	}

	// But full params should be accessible via mcpSchemaProvider
	if p, ok := tool.(mcpSchemaProvider); ok {
		fullParams := p.fullParams()
		if len(fullParams) != 1 {
			t.Errorf("Expected 1 full param, got %d", len(fullParams))
		}
	} else {
		t.Error("Tool should implement mcpSchemaProvider")
	}
}

func TestDefaultRegistry_ContainsLoadMCPToolsUsage(t *testing.T) {
	registry := DefaultRegistry()
	tool, ok := registry.Get("load_mcp_tools_usage")
	if !ok {
		t.Error("DefaultRegistry should contain load_mcp_tools_usage tool")
	}
	if tool.Name() != "load_mcp_tools_usage" {
		t.Errorf("Expected 'load_mcp_tools_usage', got '%s'", tool.Name())
	}
}
