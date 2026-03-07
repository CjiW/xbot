package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xbot/llm"
)

func TestManageTools_Name(t *testing.T) {
	tool := NewManageTools("/tmp", "/tmp/mcp.json")
	if tool.Name() != "ManageTools" {
		t.Errorf("Expected name 'ManageTools', got '%s'", tool.Name())
	}
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
		Action:    "add_mcp",
		Name:      "test-filesystem",
		MCPConfig: mcpConfig,
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
		Action:    "add_mcp",
		Name:      "user1-only",
		MCPConfig: `{"command":"echo","args":["u1"]}`,
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
