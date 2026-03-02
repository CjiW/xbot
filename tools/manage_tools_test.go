package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"xbot/llm"
)

func TestManageTools_Name(t *testing.T) {
	tool := NewManageTools("/tmp/mcp.json", "/tmp/skills")
	if tool.Name() != "ManageTools" {
		t.Errorf("Expected name 'ManageTools', got '%s'", tool.Name())
	}
}

func TestManageTools_Description(t *testing.T) {
	tool := NewManageTools("/tmp/mcp.json", "/tmp/skills")
	desc := tool.Description()
	if desc == "" {
		t.Error("Description should not be empty")
	}
}

func TestManageTools_Parameters(t *testing.T) {
	tool := NewManageTools("/tmp/mcp.json", "/tmp/skills")
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

	tool := NewManageTools(mcpConfigPath, "/tmp/skills")
	registry := NewRegistry()

	ctx := &ToolContext{
		Registry: registry,
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

	tool := NewManageTools(mcpConfigPath, "/tmp/skills")
	registry := NewRegistry()

	ctx := &ToolContext{
		Registry: registry,
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

	tool := NewManageTools(mcpConfigPath, "/tmp/skills")
	ctx := &ToolContext{Registry: NewRegistry()}

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
	tool := NewManageTools("/tmp/mcp.json", "/tmp/skills")

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
