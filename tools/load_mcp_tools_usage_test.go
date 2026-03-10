package tools

import (
	"strings"
	"testing"
	"time"

	"xbot/llm"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

type mockBuiltinTool struct {
	name   string
	desc   string
	params []llm.ToolParam
}

func (m *mockBuiltinTool) Name() string {
	return m.name
}
func (m *mockBuiltinTool) Description() string {
	if m.desc != "" {
		return m.desc
	}
	return "builtin test tool"
}
func (m *mockBuiltinTool) Parameters() []llm.ToolParam { return m.params }
func (m *mockBuiltinTool) Execute(_ *ToolContext, _ string) (*ToolResult, error) {
	return NewResult("ok"), nil
}

type mockSessionMCPProvider struct {
	manager *SessionMCPManager
}

func (m *mockSessionMCPProvider) GetSessionMCPManager(_ string) *SessionMCPManager {
	return m.manager
}

func hasToolDefinitionName(defs []llm.ToolDefinition, name string) bool {
	for _, d := range defs {
		if d.Name() == name {
			return true
		}
	}
	return false
}

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

	registry.Register(&mockMCPTool{
		name:        "search",
		server:      "github",
		description: "Search GitHub",
		params: []llm.ToolParam{
			{Name: "query", Type: "string", Required: true, Description: "Search query"},
		},
	})

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

func TestLoadMCPToolsUsageTool_ListAll_IncludesBuiltinTools(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&mockBuiltinTool{name: "shell", desc: "Run shell commands"})
	registry.Register(&mockBuiltinTool{name: "read", desc: "Read files"})
	registry.RegisterCore(&mockBuiltinTool{name: "core_tool", desc: "Always available"})

	tool := &LoadMCPToolsUsageTool{}
	ctx := &ToolContext{Registry: registry, Channel: "test", ChatID: "chat1"}

	result, err := tool.Execute(ctx, `{}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "shell") {
		t.Errorf("Expected 'shell' in result, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "read") {
		t.Errorf("Expected 'read' in result, got: %s", result.Summary)
	}
	// Core tools should NOT appear in the loadable list
	if strings.Contains(result.Summary, "core_tool") {
		t.Errorf("Core tool should not appear in loadable tool list, got: %s", result.Summary)
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

func TestLoadMCPToolsUsageTool_GetSchemas_BuiltinTool(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&mockBuiltinTool{
		name: "shell",
		desc: "Execute shell commands",
		params: []llm.ToolParam{
			{Name: "command", Type: "string", Required: true, Description: "Command to run"},
		},
	})

	tool := &LoadMCPToolsUsageTool{}
	ctx := &ToolContext{Registry: registry, Channel: "test", ChatID: "chat1"}

	result, err := tool.Execute(ctx, `{"tools": "shell"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if !strings.Contains(result.Summary, "shell") {
		t.Errorf("Expected 'shell' in result, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "command") {
		t.Errorf("Expected 'command' parameter, got: %s", result.Summary)
	}
}

func TestLoadMCPToolsUsageTool_ActivatesToolsOnLoad(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&mockBuiltinTool{name: "shell", desc: "Shell"})

	tool := &LoadMCPToolsUsageTool{}
	ctx := &ToolContext{Registry: registry, Channel: "test", ChatID: "chat1"}

	if registry.IsToolActive("test:chat1", "shell") {
		t.Fatal("Tool should not be active before loading")
	}

	_, err := tool.Execute(ctx, `{"tools": "shell"}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if !registry.IsToolActive("test:chat1", "shell") {
		t.Fatal("Tool should be active after loading")
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

func TestRegistry_GetToolSchemas_MCP(t *testing.T) {
	registry := NewRegistry()

	registry.Register(&mockMCPTool{
		name:        "search",
		server:      "github",
		description: "Search GitHub repos",
		params: []llm.ToolParam{
			{Name: "query", Type: "string", Required: true},
		},
	})

	schemas := registry.GetToolSchemas("test:chat", []string{"mcp_github_search"})
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

func TestRegistry_GetToolSchemas_Builtin(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&mockBuiltinTool{
		name: "shell",
		desc: "Execute shell commands",
		params: []llm.ToolParam{
			{Name: "command", Type: "string", Required: true},
		},
	})

	schemas := registry.GetToolSchemas("test:chat", []string{"shell"})
	if len(schemas) != 1 {
		t.Fatalf("Expected 1 schema, got %d", len(schemas))
	}
	if schemas[0].ToolName != "shell" {
		t.Errorf("Expected 'shell', got '%s'", schemas[0].ToolName)
	}
	if schemas[0].ServerName != "" {
		t.Errorf("Built-in tool should have empty ServerName, got '%s'", schemas[0].ServerName)
	}
	if schemas[0].Description != "Execute shell commands" {
		t.Errorf("Unexpected description: %s", schemas[0].Description)
	}
}

func TestRegistry_GetToolSchemas_ExcludesCoreTools(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCore(&mockBuiltinTool{name: "core_tool"})
	registry.Register(&mockBuiltinTool{name: "loadable_tool"})

	schemas := registry.GetToolSchemas("test:chat", nil)
	for _, s := range schemas {
		if s.ToolName == "core_tool" {
			t.Error("Core tools should not appear in GetToolSchemas (they are always loaded)")
		}
	}
	found := false
	for _, s := range schemas {
		if s.ToolName == "loadable_tool" {
			found = true
		}
	}
	if !found {
		t.Error("Non-core tool should appear in GetToolSchemas")
	}
}

func TestMCPRemoteTool_StubMode(t *testing.T) {
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

	params := tool.Parameters()
	if params != nil {
		t.Errorf("Stub mode: expected nil parameters, got %v", params)
	}

	if p, ok := tool.(mcpSchemaProvider); ok {
		fullParams := p.fullParams()
		if len(fullParams) != 1 {
			t.Errorf("Expected 1 full param, got %d", len(fullParams))
		}
	} else {
		t.Error("Tool should implement mcpSchemaProvider")
	}
}

// ---- AsDefinitionsForSession: 二阶段加载逻辑 ----

func TestRegistry_AsDefinitionsForSession_OnlyCoreToolsByDefault(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCore(&mockBuiltinTool{name: "load_mcp_tools_usage"})
	registry.Register(&mockBuiltinTool{name: "shell"})
	registry.Register(&mockBuiltinTool{name: "read"})
	registry.Register(&mockMCPTool{name: "search", server: "github", description: "Search"})

	defs := registry.AsDefinitionsForSession("test:chat")

	if !hasToolDefinitionName(defs, "load_mcp_tools_usage") {
		t.Fatal("Core tool should always be in definitions")
	}
	if hasToolDefinitionName(defs, "shell") {
		t.Fatal("Non-core builtin tool should NOT be in definitions before activation")
	}
	if hasToolDefinitionName(defs, "read") {
		t.Fatal("Non-core builtin tool should NOT be in definitions before activation")
	}
	if hasToolDefinitionName(defs, "mcp_github_search") {
		t.Fatal("MCP tool should NOT be in definitions before activation")
	}
}

func TestRegistry_AsDefinitionsForSession_IncludesActivatedBuiltinTools(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCore(&mockBuiltinTool{name: "load_mcp_tools_usage"})
	registry.Register(&mockBuiltinTool{name: "shell"})
	registry.Register(&mockBuiltinTool{name: "read"})

	registry.ActivateTools("test:chat", []string{"shell"})

	defs := registry.AsDefinitionsForSession("test:chat")

	if !hasToolDefinitionName(defs, "load_mcp_tools_usage") {
		t.Fatal("Core tool should be present")
	}
	if !hasToolDefinitionName(defs, "shell") {
		t.Fatal("Activated builtin tool should be in definitions")
	}
	if hasToolDefinitionName(defs, "read") {
		t.Fatal("Non-activated builtin tool should NOT be in definitions")
	}
}

func TestRegistry_AsDefinitionsForSession_IncludesActivatedSessionMCPTools(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCore(&mockBuiltinTool{name: "load_mcp_tools_usage"})

	sessionMCP := NewSessionMCPManager("test:chat", "", "", "", time.Minute)
	sessionMCP.initialized = true
	sessionMCP.connections["github"] = &mcpConnection{
		name: "github",
		tools: []*mcp.Tool{
			{Name: "search", Description: "Search GitHub"},
		},
	}

	registry.SetSessionMCPManagerProvider(&mockSessionMCPProvider{manager: sessionMCP})

	defs := registry.AsDefinitionsForSession("test:chat")
	if hasToolDefinitionName(defs, "mcp_github_search") {
		t.Fatal("Unactivated session MCP tool should be excluded")
	}

	registry.ActivateTools("test:chat", []string{"mcp_github_search"})

	defs = registry.AsDefinitionsForSession("test:chat")
	if !hasToolDefinitionName(defs, "mcp_github_search") {
		t.Fatal("Activated session MCP tool should be included")
	}

	for _, d := range defs {
		if d.Name() == "mcp_github_search" {
			if d.Description() != "[MCP:github] Search GitHub" {
				t.Errorf("Unexpected description: %s", d.Description())
			}
		}
	}
}

// ---- IsToolActive ----

func TestRegistry_IsToolActive(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCore(&mockBuiltinTool{name: "core_tool"})
	registry.Register(&mockBuiltinTool{name: "non_core"})

	if !registry.IsToolActive("test:chat", "core_tool") {
		t.Fatal("Core tool should always be active")
	}
	if registry.IsToolActive("test:chat", "non_core") {
		t.Fatal("Non-core tool should not be active before activation")
	}

	registry.ActivateTools("test:chat", []string{"non_core"})
	if !registry.IsToolActive("test:chat", "non_core") {
		t.Fatal("Tool should be active after activation")
	}

	// Different session should not be affected
	if registry.IsToolActive("other:chat", "non_core") {
		t.Fatal("Activation should be per-session")
	}
}

func TestRegistry_DeactivateSession(t *testing.T) {
	registry := NewRegistry()
	registry.Register(&mockBuiltinTool{name: "shell"})

	registry.ActivateTools("test:chat", []string{"shell"})
	if !registry.IsToolActive("test:chat", "shell") {
		t.Fatal("Tool should be active")
	}

	registry.DeactivateSession("test:chat")
	if registry.IsToolActive("test:chat", "shell") {
		t.Fatal("Tool should not be active after session deactivation")
	}
}

func tickN(registry *Registry, sessionKey string, n int) {
	for i := 0; i < n; i++ {
		registry.TickSession(sessionKey)
	}
}

func TestRegistry_ToolExpiry_AfterIdleRounds(t *testing.T) {
	registry := NewRegistry()
	max := int(registry.maxIdleRounds)
	registry.Register(&mockBuiltinTool{name: "tool_a"})
	registry.Register(&mockBuiltinTool{name: "tool_b"})

	registry.ActivateTools("s", []string{"tool_a", "tool_b"})

	if !registry.IsToolActive("s", "tool_a") || !registry.IsToolActive("s", "tool_b") {
		t.Fatal("Both tools should be active immediately after activation")
	}

	// Tick exactly maxIdleRounds: idle == max, still within limit
	tickN(registry, "s", max)
	if !registry.IsToolActive("s", "tool_a") {
		t.Fatalf("Tool should still be active after %d idle rounds (maxIdleRounds=%d)", max, max)
	}

	// One more tick: idle == max+1, exceeds limit → expired
	registry.TickSession("s")
	if registry.IsToolActive("s", "tool_a") {
		t.Fatal("Tool should expire after exceeding maxIdleRounds")
	}

	defs := registry.AsDefinitionsForSession("s")
	if hasToolDefinitionName(defs, "tool_a") {
		t.Fatal("Expired tool should not appear in definitions")
	}
}

func TestRegistry_TouchTool_ExtendsLifetime(t *testing.T) {
	registry := NewRegistry()
	max := int(registry.maxIdleRounds)
	registry.Register(&mockBuiltinTool{name: "touched"})
	registry.Register(&mockBuiltinTool{name: "untouched"})

	registry.ActivateTools("s", []string{"touched", "untouched"})

	// Advance halfway, touch only one tool
	half := max / 2
	if half < 1 {
		half = 1
	}
	tickN(registry, "s", half)
	registry.TouchTool("s", "touched")

	// Advance maxIdleRounds more from activation (total > max from activation, but ≤ max from touch)
	remaining := max - half + 1
	tickN(registry, "s", remaining)

	// "touched" was refreshed at round `half`, now at round `half + remaining`
	// idle from touch = remaining ≤ max → still active
	if !registry.IsToolActive("s", "touched") {
		t.Fatal("Touched tool should still be active")
	}

	// "untouched" was last used at round 0, now at round half+remaining > max → expired
	if registry.IsToolActive("s", "untouched") {
		t.Fatal("Untouched tool should have expired")
	}
}

func TestRegistry_TickSession_PrunesExpiredEntries(t *testing.T) {
	registry := NewRegistry()
	max := int(registry.maxIdleRounds)
	registry.Register(&mockBuiltinTool{name: "ephemeral"})

	registry.ActivateTools("s", []string{"ephemeral"})

	// Advance well past expiry
	tickN(registry, "s", max+2)

	registry.mu.RLock()
	_, exists := registry.sessionActivated["s"]["ephemeral"]
	registry.mu.RUnlock()
	if exists {
		t.Fatal("TickSession should prune expired entries from the map")
	}
}

// ---- GetBuiltinToolNames ----

func TestRegistry_GetBuiltinToolNames(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCore(&mockBuiltinTool{name: "core_a"})
	registry.Register(&mockBuiltinTool{name: "tool_b"})
	registry.Register(&mockMCPTool{name: "search", server: "github"})

	names := registry.GetBuiltinToolNames()
	// Should include core and non-core built-in tools, but NOT MCP tools
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["core_a"] {
		t.Error("Expected core_a in builtin names")
	}
	if !found["tool_b"] {
		t.Error("Expected tool_b in builtin names")
	}
	if found["mcp_github_search"] {
		t.Error("MCP tools should not be in builtin names")
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

// ---- UnloadInactiveServers: 卸载后重连 ----

func TestSessionMCP_UnloadResetsInitialized(t *testing.T) {
	sm := NewSessionMCPManager("test:chat", "", "", "", 0) // timeout=0 → 立即过期
	sm.initialized = true
	sm.connections["linear"] = &mcpConnection{
		name: "linear",
		tools: []*mcp.Tool{
			{Name: "list_issues", Description: "List issues"},
		},
	}
	sm.lastActive["linear"] = time.Now().Add(-time.Hour) // 1 小时前活跃

	sm.UnloadInactiveServers()

	if len(sm.connections) != 0 {
		t.Fatal("Connection should be unloaded")
	}
	if sm.initialized {
		t.Fatal("initialized should be reset to false after unloading servers, so next access triggers reconnection")
	}
}

func TestSessionMCP_UnloadKeepsInitializedWhenNothingUnloaded(t *testing.T) {
	sm := NewSessionMCPManager("test:chat", "", "", "", time.Hour)
	sm.initialized = true
	sm.connections["linear"] = &mcpConnection{
		name: "linear",
		tools: []*mcp.Tool{
			{Name: "list_issues", Description: "List issues"},
		},
	}
	sm.lastActive["linear"] = time.Now() // 刚刚活跃

	sm.UnloadInactiveServers()

	if len(sm.connections) != 1 {
		t.Fatal("Active connection should not be unloaded")
	}
	if !sm.initialized {
		t.Fatal("initialized should remain true when no servers were unloaded")
	}
}

// ---- AsDefinitionsForSession: 全局 MCP 工具激活后含完整参数 ----

func TestRegistry_AsDefinitionsForSession_ActivatedGlobalMCPToolHasFullParams(t *testing.T) {
	registry := NewRegistry()
	registry.RegisterCore(&mockBuiltinTool{name: "load_mcp_tools_usage"})

	expectedParams := []llm.ToolParam{
		{Name: "query", Type: "string", Description: "Search query", Required: true},
		{Name: "limit", Type: "number", Description: "Max results"},
	}
	registry.Register(&mockMCPTool{
		name:        "search",
		server:      "github",
		description: "Search GitHub",
		params:      expectedParams,
	})

	// 未激活时不应出现
	defs := registry.AsDefinitionsForSession("test:chat")
	if hasToolDefinitionName(defs, "mcp_github_search") {
		t.Fatal("Unactivated global MCP tool should NOT be in definitions")
	}

	// 激活后应出现，且带完整参数
	registry.ActivateTools("test:chat", []string{"mcp_github_search"})
	defs = registry.AsDefinitionsForSession("test:chat")
	if !hasToolDefinitionName(defs, "mcp_github_search") {
		t.Fatal("Activated global MCP tool should be in definitions")
	}

	for _, d := range defs {
		if d.Name() == "mcp_github_search" {
			params := d.Parameters()
			if len(params) != len(expectedParams) {
				t.Fatalf("Expected %d params, got %d (empty params bug)", len(expectedParams), len(params))
			}
			if params[0].Name != "query" || !params[0].Required {
				t.Errorf("First param should be 'query' (required), got %+v", params[0])
			}
		}
	}
}

func TestDefaultRegistry_CoreToolsAlwaysInDefinitions(t *testing.T) {
	registry := DefaultRegistry()
	defs := registry.AsDefinitions()

	coreExpected := []string{"load_mcp_tools_usage", "Shell", "Glob", "Grep", "Read", "Edit"}
	for _, name := range coreExpected {
		if !hasToolDefinitionName(defs, name) {
			t.Errorf("%s should always appear in definitions (core tool)", name)
		}
	}

	// Non-core tools should NOT be in AsDefinitions
	nonCore := []string{"WebSearch", "SubAgent", "Cron", "DownloadFile"}
	for _, name := range nonCore {
		if hasToolDefinitionName(defs, name) {
			t.Errorf("%s should NOT appear in AsDefinitions (non-core)", name)
		}
	}
}
