package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
)

// ManageTools allows the bot to add/update/remove MCP servers dynamically
type ManageTools struct {
	mcpConfigPath string
}

// NewManageTools creates a new ManageTools tool
func NewManageTools(mcpConfigPath, skillsDir string) *ManageTools {
	return &ManageTools{
		mcpConfigPath: mcpConfigPath,
	}
}

func (t *ManageTools) Name() string {
	return "ManageTools"
}

func (t *ManageTools) Description() string {
	return "Manage the bot's MCP servers. Can add, remove, list MCP servers, and reload configurations."
}

func (t *ManageTools) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "action",
			Type:        "string",
			Description: "Action to perform: 'add_mcp', 'remove_mcp', 'list_mcp', 'reload'",
			Required:    true,
		},
		{
			Name:        "name",
			Type:        "string",
			Description: "Name of the MCP server",
			Required:    false,
		},
		{
			Name:        "mcp_config",
			Type:        "string",
			Description: "MCP server configuration as JSON (for add_mcp). Example: {\"command\":\"npx\",\"args\":[\"-y\",\"@modelcontextprotocol/server-filesystem\",\"/path\"]}",
			Required:    false,
		},
	}
}

type manageToolsArgs struct {
	Action    string `json:"action"`
	Name      string `json:"name"`
	MCPConfig string `json:"mcp_config"`
}

func (t *ManageTools) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args manageToolsArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	switch args.Action {
	case "add_mcp":
		return t.addMCP(ctx, args)
	case "remove_mcp":
		return t.removeMCP(ctx, args)
	case "list_mcp":
		return t.listMCP(ctx)
	case "reload":
		return t.reload(ctx)
	default:
		return nil, fmt.Errorf("unknown action: %s (valid: add_mcp, remove_mcp, list_mcp, reload)", args.Action)
	}
}

func (t *ManageTools) addMCP(ctx *ToolContext, args manageToolsArgs) (*ToolResult, error) {
	if args.Name == "" {
		return nil, fmt.Errorf("name is required for add_mcp")
	}
	if args.MCPConfig == "" {
		return nil, fmt.Errorf("mcp_config is required for add_mcp")
	}

	// Parse MCP config
	var cfg MCPServerConfig
	if err := json.Unmarshal([]byte(args.MCPConfig), &cfg); err != nil {
		return nil, fmt.Errorf("parse mcp_config: %w", err)
	}

	// Load existing config
	config, err := t.loadMCPConfig()
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load mcp config: %w", err)
	}

	if config == nil {
		config = &MCPConfig{
			MCPServers: make(map[string]MCPServerConfig),
		}
	}

	// Add/update server
	config.MCPServers[args.Name] = cfg

	// Save config
	if err := t.saveMCPConfig(config); err != nil {
		return nil, fmt.Errorf("save mcp config: %w", err)
	}

	return NewResult(fmt.Sprintf("MCP server '%s' has been added. Use 'reload' action to connect to it.", args.Name)), nil
}

func (t *ManageTools) removeMCP(ctx *ToolContext, args manageToolsArgs) (*ToolResult, error) {
	if args.Name == "" {
		return nil, fmt.Errorf("name is required for remove_mcp")
	}

	// Load existing config
	config, err := t.loadMCPConfig()
	if err != nil {
		return nil, fmt.Errorf("load mcp config: %w", err)
	}

	if config == nil {
		return NewResult(fmt.Sprintf("MCP server '%s' not found (no config file).", args.Name)), nil
	}

	// Remove server
	if _, exists := config.MCPServers[args.Name]; !exists {
		return NewResult(fmt.Sprintf("MCP server '%s' not found.", args.Name)), nil
	}

	delete(config.MCPServers, args.Name)

	// Save config
	if err := t.saveMCPConfig(config); err != nil {
		return nil, fmt.Errorf("save mcp config: %w", err)
	}

	return NewResult(fmt.Sprintf("MCP server '%s' has been removed. Use 'reload' action to apply changes.", args.Name)), nil
}

func (t *ManageTools) listMCP(ctx *ToolContext) (*ToolResult, error) {
	config, err := t.loadMCPConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return NewResult("No MCP servers configured."), nil
		}
		return nil, fmt.Errorf("load mcp config: %w", err)
	}

	if config == nil || len(config.MCPServers) == 0 {
		return NewResult("No MCP servers configured."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d MCP server(s):\n\n", len(config.MCPServers))
	for name, cfg := range config.MCPServers {
		enabled := "enabled"
		if cfg.Enabled != nil && !*cfg.Enabled {
			enabled = "disabled"
		}
		if cfg.URL != "" {
			fmt.Fprintf(&sb, "- **%s** (%s, HTTP): %s\n", name, enabled, cfg.URL)
		} else {
			fmt.Fprintf(&sb, "- **%s** (%s, stdio): %s %v\n", name, enabled, cfg.Command, cfg.Args)
		}
	}

	return &ToolResult{Summary: sb.String()}, nil
}

func (t *ManageTools) reload(ctx *ToolContext) (*ToolResult, error) {
	results := []string{}

	// MCP is now per-session and lazily loaded
	// Sessions will reconnect on next tool use after configuration changes
	results = append(results, "MCP: Per-session lazy loading enabled - sessions will reload MCP tools on next use")

	return NewResult(strings.Join(results, "\n")), nil
}

func (t *ManageTools) loadMCPConfig() (*MCPConfig, error) {
	data, err := os.ReadFile(t.mcpConfigPath)
	if err != nil {
		return nil, err
	}

	var config MCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (t *ManageTools) saveMCPConfig(config *MCPConfig) error {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(t.mcpConfigPath), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(t.mcpConfigPath, data, 0o644)
}
