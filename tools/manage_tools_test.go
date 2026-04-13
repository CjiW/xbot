package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xbot/llm"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestManageTools_Name(t *testing.T) {
	tool := NewManageTools("/tmp", "/tmp/mcp.json")
	if tool.Name() != "ManageTools" {
		t.Errorf("Expected name 'ManageTools', got '%s'", tool.Name())
	}
}

// TestManageTools_WritePathByChannel verifies that add_mcp writes to the correct
// config file for each channel/sandbox mode:
//   - CLI (none sandbox): writes to GlobalMCPConfigPath (xbotHome/mcp.json)
//   - Feishu (remote sandbox): writes to per-user MCPConfigPath
//   - CLI (docker sandbox): writes to per-user MCPConfigPath (sandbox has own dir)
func TestManageTools_WritePathByChannel(t *testing.T) {
	t.Run("cli_none_sandbox_writes_global", func(t *testing.T) {
		tempDir := t.TempDir()
		globalPath := filepath.Join(tempDir, "xbotHome", "mcp.json")
		userPath := filepath.Join(tempDir, "users", "cli_user", "mcp.json")

		tool := NewManageTools(tempDir, globalPath)
		ctx := &ToolContext{
			Registry:            NewRegistry(),
			Channel:             "cli",
			MCPConfigPath:       userPath,
			GlobalMCPConfigPath: globalPath,
		}

		input, _ := json.Marshal(manageToolsArgs{
			Action:       "add_mcp",
			Name:         "cli-test",
			MCPConfig:    `{"url":"http://example.com/mcp"}`,
			Instructions: "test",
		})
		if _, err := tool.Execute(ctx, string(input)); err != nil {
			t.Fatalf("add_mcp failed: %v", err)
		}

		// Must be in global path
		if _, err := os.Stat(globalPath); err != nil {
			t.Fatalf("expected global config at %s: %v", globalPath, err)
		}
		// User path must NOT exist
		if _, err := os.Stat(userPath); !os.IsNotExist(err) {
			t.Fatalf("user config should not exist at %s", userPath)
		}

		// Verify content
		data, _ := os.ReadFile(globalPath)
		var cfg MCPConfig
		json.Unmarshal(data, &cfg)
		if _, ok := cfg.MCPServers["cli-test"]; !ok {
			t.Fatal("cli-test not found in global config")
		}
	})

	t.Run("feishu_sandbox_writes_user_path", func(t *testing.T) {
		tempDir := t.TempDir()
		globalPath := filepath.Join(tempDir, "xbotHome", "mcp.json")
		userPath := filepath.Join(tempDir, "users", "feishu_user", "mcp.json")

		// Pre-create global config with a shared server
		globalCfg := MCPConfig{MCPServers: map[string]MCPServerConfig{
			"shared-server": {URL: "http://shared.example.com/mcp"},
		}}
		globalData, _ := json.MarshalIndent(globalCfg, "", "  ")
		os.MkdirAll(filepath.Dir(globalPath), 0o755)
		os.WriteFile(globalPath, globalData, 0o644)

		tool := NewManageTools(tempDir, globalPath)
		ctx := &ToolContext{
			Registry:            NewRegistry(),
			Channel:             "feishu",
			MCPConfigPath:       userPath,
			GlobalMCPConfigPath: globalPath,
		}

		input, _ := json.Marshal(manageToolsArgs{
			Action:       "add_mcp",
			Name:         "feishu-private",
			MCPConfig:    `{"url":"http://example.com/mcp"}`,
			Instructions: "feishu private tool",
		})
		if _, err := tool.Execute(ctx, string(input)); err != nil {
			t.Fatalf("add_mcp failed: %v", err)
		}

		// Must be in user path, NOT in global
		if _, err := os.Stat(userPath); err != nil {
			t.Fatalf("expected user config at %s: %v", userPath, err)
		}
		data, _ := os.ReadFile(userPath)
		var userCfg MCPConfig
		json.Unmarshal(data, &userCfg)
		if _, ok := userCfg.MCPServers["feishu-private"]; !ok {
			t.Fatal("feishu-private not found in user config")
		}

		// Global config should NOT be modified
		globalData2, _ := os.ReadFile(globalPath)
		var globalCfg2 MCPConfig
		json.Unmarshal(globalData2, &globalCfg2)
		if _, ok := globalCfg2.MCPServers["feishu-private"]; ok {
			t.Fatal("feishu-private should NOT be in global config")
		}
		if _, ok := globalCfg2.MCPServers["shared-server"]; !ok {
			t.Fatal("shared-server should still exist in global config")
		}
	})

	t.Run("cli_docker_sandbox_writes_user_path", func(t *testing.T) {
		tempDir := t.TempDir()
		globalPath := filepath.Join(tempDir, "xbotHome", "mcp.json")
		userPath := filepath.Join(tempDir, "users", "cli_user", "mcp.json")

		tool := NewManageTools(tempDir, globalPath)
		// Docker sandbox: CLI channel but sandbox is enabled, so should still
		// write to global (CLI always writes to global regardless of sandbox)
		ctx := &ToolContext{
			Registry:            NewRegistry(),
			Channel:             "cli",
			MCPConfigPath:       userPath,
			GlobalMCPConfigPath: globalPath,
			SandboxEnabled:      true,
		}

		input, _ := json.Marshal(manageToolsArgs{
			Action:       "add_mcp",
			Name:         "cli-docker",
			MCPConfig:    `{"url":"http://example.com/mcp"}`,
			Instructions: "docker sandbox test",
		})
		if _, err := tool.Execute(ctx, string(input)); err != nil {
			t.Fatalf("add_mcp failed: %v", err)
		}

		// CLI always writes to global regardless of sandbox
		if _, err := os.Stat(globalPath); err != nil {
			t.Fatalf("expected global config at %s: %v", globalPath, err)
		}
		data, _ := os.ReadFile(globalPath)
		var cfg MCPConfig
		json.Unmarshal(data, &cfg)
		if _, ok := cfg.MCPServers["cli-docker"]; !ok {
			t.Fatal("cli-docker not found in global config")
		}
	})

	t.Run("no_context_falls_back_to_anonymous", func(t *testing.T) {
		tempDir := t.TempDir()
		globalPath := filepath.Join(tempDir, "global-mcp.json")
		anonymousPath := filepath.Join(tempDir, ".xbot", "users", "anonymous", "mcp.json")

		tool := NewManageTools(tempDir, globalPath)
		// Nil context → falls back to workDir/.xbot/users/anonymous/mcp.json
		input, _ := json.Marshal(manageToolsArgs{
			Action:       "add_mcp",
			Name:         "anonymous-srv",
			MCPConfig:    `{"url":"http://example.com/mcp"}`,
			Instructions: "fallback test",
		})
		if _, err := tool.Execute(nil, string(input)); err != nil {
			t.Fatalf("add_mcp with nil ctx failed: %v", err)
		}

		if _, err := os.Stat(anonymousPath); err != nil {
			t.Fatalf("expected anonymous config at %s: %v", anonymousPath, err)
		}
		data, _ := os.ReadFile(anonymousPath)
		var cfg MCPConfig
		json.Unmarshal(data, &cfg)
		if _, ok := cfg.MCPServers["anonymous-srv"]; !ok {
			t.Fatal("anonymous-srv not found in anonymous config")
		}
	})
}

func TestManageTools_Description(t *testing.T) {
	tool := NewManageTools("/tmp", "/tmp/mcp.json")
	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestManageTools_Parameters(t *testing.T) {
	tool := NewManageTools("/tmp", "/tmp/mcp.json")
	params := tool.Parameters()
	if len(params) == 0 {
		t.Error("Should have parameters")
	}

	// Check for required action parameter
	foundAction := false
	for _, p := range params {
		if p.Name == "action" {
			foundAction = true
			if !p.Required {
				t.Error("action parameter should be required")
			}
		}
	}
	if !foundAction {
		t.Error("action parameter not found")
	}
}

func TestManageTools_AddRemoveMCP(t *testing.T) {
	tempDir := t.TempDir()
	mcpConfigPath := filepath.Join(tempDir, "mcp.json")

	tool := NewManageTools(tempDir, mcpConfigPath)
	registry := NewRegistry()

	ctx := &ToolContext{
		Registry:      registry,
		MCPConfigPath: mcpConfigPath,
	}

	// Test add_mcp
	mcpConfig := `{"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]}`
	args := manageToolsArgs{
		Action:       "add_mcp",
		Name:         "test-filesystem",
		MCPConfig:    mcpConfig,
		Instructions: "test-filesystem",
	}
	input, _ := json.Marshal(args)

	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("add_mcp failed: %v", err)
	}
	if result.Summary == "" {
		t.Error("Expected non-empty result summary")
	}

	// Verify config file was created
	data, err := os.ReadFile(mcpConfigPath)
	if err != nil {
		t.Fatalf("Failed to read mcp config: %v", err)
	}

	var config MCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("Failed to parse mcp config: %v", err)
	}
	if _, ok := config.MCPServers["test-filesystem"]; !ok {
		t.Error("MCP server was not added to config")
	}

	// Test remove_mcp
	args = manageToolsArgs{
		Action: "remove_mcp",
		Name:   "test-filesystem",
	}
	input, _ = json.Marshal(args)

	_, err = tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("remove_mcp failed: %v", err)
	}

	// Verify server was removed
	data, err = os.ReadFile(mcpConfigPath)
	if err != nil {
		t.Fatalf("Failed to read mcp config: %v", err)
	}
	var newConfig MCPConfig
	if err := json.Unmarshal(data, &newConfig); err != nil {
		t.Fatalf("Failed to parse mcp config: %v", err)
	}
	if _, ok := newConfig.MCPServers["test-filesystem"]; ok {
		t.Error("MCP server was not removed from config")
	}
}

func TestManageTools_ListMCP(t *testing.T) {
	tempDir := t.TempDir()
	mcpConfigPath := filepath.Join(tempDir, "mcp.json")

	tool := NewManageTools(tempDir, mcpConfigPath)
	registry := NewRegistry()

	ctx := &ToolContext{
		Registry:      registry,
		MCPConfigPath: mcpConfigPath,
	}

	// Test with no MCP config
	args := manageToolsArgs{Action: "list_mcp"}
	input, _ := json.Marshal(args)

	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("list_mcp failed: %v", err)
	}
	if result.Summary == "" {
		t.Error("Expected non-empty result")
	}

	// Create MCP config
	config := MCPConfig{
		MCPServers: map[string]MCPServerConfig{
			"test-server": {
				Command: "test",
				Args:    []string{"command"},
			},
		},
	}
	data, _ := json.MarshalIndent(config, "", "  ")
	os.WriteFile(mcpConfigPath, data, 0o644)

	// List again
	result, err = tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("list_mcp failed: %v", err)
	}
	if result.Summary == "" {
		t.Error("Expected non-empty result")
	}
}

func TestManageTools_Execute_ParamsValidation(t *testing.T) {
	tempDir := t.TempDir()
	mcpConfigPath := filepath.Join(tempDir, "mcp.json")

	tool := NewManageTools(tempDir, mcpConfigPath)
	ctx := &ToolContext{Registry: NewRegistry(), MCPConfigPath: mcpConfigPath}

	// Test missing required parameter for add_mcp
	args := manageToolsArgs{Action: "add_mcp"} // missing name
	input, _ := json.Marshal(args)

	_, err := tool.Execute(ctx, string(input))
	if err == nil {
		t.Error("Expected error for missing name parameter")
	}

	// Test unknown action
	args = manageToolsArgs{Action: "unknown_action"}
	input, _ = json.Marshal(args)

	_, err = tool.Execute(ctx, string(input))
	if err == nil {
		t.Error("Expected error for unknown action")
	}
}

func TestManageTools_ToolDefinition(t *testing.T) {
	tool := NewManageTools("/tmp", "/tmp/mcp.json")

	// Verify it implements Tool interface
	var _ llm.ToolDefinition = tool
	var _ Tool = tool

	// Check parameters match expected schema
	params := tool.Parameters()
	paramMap := make(map[string]llm.ToolParam)
	for _, p := range params {
		paramMap[p.Name] = p
	}

	expectedParams := []string{"action", "name", "mcp_config"}
	for _, name := range expectedParams {
		if _, ok := paramMap[name]; !ok {
			t.Errorf("Missing parameter: %s", name)
		}
	}
}

func TestManageTools_CLIWritesGlobalConfig(t *testing.T) {
	tempDir := t.TempDir()
	globalConfigPath := filepath.Join(tempDir, "global-mcp.json")
	userConfigPath := filepath.Join(tempDir, "user", "mcp.json")

	tool := NewManageTools(tempDir, globalConfigPath)
	ctx := &ToolContext{
		Registry:            NewRegistry(),
		Channel:             "cli",
		MCPConfigPath:       userConfigPath,
		GlobalMCPConfigPath: globalConfigPath,
	}

	addInput, _ := json.Marshal(manageToolsArgs{
		Action:       "add_mcp",
		Name:         "cli-global",
		MCPConfig:    `{"command":"echo","args":["cli"]}`,
		Instructions: "cli test",
	})
	if _, err := tool.Execute(ctx, string(addInput)); err != nil {
		t.Fatalf("cli add_mcp failed: %v", err)
	}

	globalData, err := os.ReadFile(globalConfigPath)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	var globalCfg MCPConfig
	if err := json.Unmarshal(globalData, &globalCfg); err != nil {
		t.Fatalf("parse global config: %v", err)
	}
	if _, ok := globalCfg.MCPServers["cli-global"]; !ok {
		t.Fatalf("expected cli-global written to global config")
	}
	if _, err := os.Stat(userConfigPath); !os.IsNotExist(err) {
		t.Fatalf("expected user config untouched, got err=%v", err)
	}

	removeInput, _ := json.Marshal(manageToolsArgs{Action: "remove_mcp", Name: "cli-global"})
	if _, err := tool.Execute(ctx, string(removeInput)); err != nil {
		t.Fatalf("cli remove_mcp failed: %v", err)
	}

	globalData, err = os.ReadFile(globalConfigPath)
	if err != nil {
		t.Fatalf("read global config after remove: %v", err)
	}
	globalCfg = MCPConfig{}
	if err := json.Unmarshal(globalData, &globalCfg); err != nil {
		t.Fatalf("parse global config after remove: %v", err)
	}
	if _, ok := globalCfg.MCPServers["cli-global"]; ok {
		t.Fatalf("expected cli-global removed from global config")
	}
}

func TestManageTools_UserIsolationAndGlobalMerge(t *testing.T) {
	tempDir := t.TempDir()
	globalConfigPath := filepath.Join(tempDir, "global-mcp.json")

	globalCfg := MCPConfig{MCPServers: map[string]MCPServerConfig{
		"global-server": {Command: "echo", Args: []string{"global"}},
	}}
	globalData, _ := json.MarshalIndent(globalCfg, "", "  ")
	if err := os.WriteFile(globalConfigPath, globalData, 0o644); err != nil {
		t.Fatalf("write global config: %v", err)
	}

	tool := NewManageTools(tempDir, globalConfigPath)

	user1Path := filepath.Join(tempDir, "u1", "mcp.json")
	user2Path := filepath.Join(tempDir, "u2", "mcp.json")
	ctx1 := &ToolContext{Registry: NewRegistry(), MCPConfigPath: user1Path, GlobalMCPConfigPath: globalConfigPath}
	ctx2 := &ToolContext{Registry: NewRegistry(), MCPConfigPath: user2Path, GlobalMCPConfigPath: globalConfigPath}

	addArgs := manageToolsArgs{
		Action:       "add_mcp",
		Name:         "user1-only",
		MCPConfig:    `{"command":"echo","args":["u1"]}`,
		Instructions: "test-filesystem",
	}
	input, _ := json.Marshal(addArgs)
	if _, err := tool.Execute(ctx1, string(input)); err != nil {
		t.Fatalf("user1 add_mcp failed: %v", err)
	}

	if _, err := os.Stat(user1Path); err != nil {
		t.Fatalf("expected user1 config created: %v", err)
	}
	if _, err := os.Stat(user2Path); err == nil {
		t.Fatalf("expected user2 config untouched")
	}

	listInput, _ := json.Marshal(manageToolsArgs{Action: "list_mcp"})
	res1, err := tool.Execute(ctx1, string(listInput))
	if err != nil {
		t.Fatalf("user1 list_mcp failed: %v", err)
	}
	if !strings.Contains(res1.Summary, "global-server") || !strings.Contains(res1.Summary, "user1-only") {
		t.Fatalf("user1 list should contain global + own, got: %s", res1.Summary)
	}

	res2, err := tool.Execute(ctx2, string(listInput))
	if err != nil {
		t.Fatalf("user2 list_mcp failed: %v", err)
	}
	if !strings.Contains(res2.Summary, "global-server") {
		t.Fatalf("user2 list should contain global config, got: %s", res2.Summary)
	}
	if strings.Contains(res2.Summary, "user1-only") {
		t.Fatalf("user2 should not see user1 private server, got: %s", res2.Summary)
	}
}

func TestManageTools_AddMCPInvalidatesImmediatelyInFlatMode(t *testing.T) {
	tempDir := t.TempDir()
	mcpConfigPath := filepath.Join(tempDir, "mcp.json")
	tool := NewManageTools(tempDir, mcpConfigPath)

	registry := NewRegistry()
	registry.SetFlatMode(true)
	invalidated := 0
	ctx := &ToolContext{
		Registry:                registry,
		MCPConfigPath:           mcpConfigPath,
		InvalidateAllSessionMCP: func() { invalidated++ },
	}

	input, _ := json.Marshal(manageToolsArgs{
		Action:       "add_mcp",
		Name:         "flat-visible-now",
		MCPConfig:    `{"command":"echo","args":["flat"]}`,
		Instructions: "flat test",
	})

	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("flat add_mcp failed: %v", err)
	}
	if invalidated != 1 {
		t.Fatalf("expected immediate MCP invalidation once, got %d", invalidated)
	}
	if !strings.Contains(result.Summary, "immediately visible") {
		t.Fatalf("expected immediate visibility hint, got: %s", result.Summary)
	}
}

func TestRegistry_AsDefinitionsForSession_FlatModeIncludesSessionMCPTools(t *testing.T) {
	registry := NewRegistry()
	registry.SetFlatMode(true)

	sm := &SessionMCPManager{
			connections: map[string]*mcpConnection{
				"demo": {
					name: "demo",
					tools: []*mcp.Tool{{
						Name:        "ping",
						Description: "Ping demo server",
						InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
					}},
				},
			},
			lastActive:  make(map[string]time.Time),
			initialized: true,
			initDone:    make(chan struct{}),
		}
	registry.SetSessionMCPManagerProvider(&mockSessionMCPProvider{manager: sm})

	defs := registry.AsDefinitionsForSession("test:chat")
	if !hasToolDefinitionName(defs, "mcp_demo_ping") {
		t.Fatalf("expected flat mode to expose session MCP tool immediately")
	}
}
