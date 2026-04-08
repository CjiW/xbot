package tools

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"xbot/llm"
)

// ManageTools allows the bot to add/update/remove MCP servers dynamically
type ManageTools struct {
	workDir             string
	globalMCPConfigPath string
}

// NewManageTools creates a new ManageTools tool
func NewManageTools(workDir, globalMCPConfigPath string) *ManageTools {
	return &ManageTools{
		workDir:             workDir,
		globalMCPConfigPath: globalMCPConfigPath,
	}
}

func (t *ManageTools) Name() string {
	return "ManageTools"
}

func (t *ManageTools) Description() string {
	return "Manage the bot's MCP servers. Can add, remove, list MCP servers, and reload configurations. This tool is not related to specified tools, if you want anything related to tools, use `search_tools` or `load_tools` instead."
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
		{
			Name:        "instructions",
			Type:        "string",
			Description: "Brief description of what this MCP server does and when to use its tools (required for add_mcp)",
			Required:    false,
		},
	}
}

type manageToolsArgs struct {
	Action       string `json:"action"`
	Name         string `json:"name"`
	MCPConfig    string `json:"mcp_config"`
	Instructions string `json:"instructions"`
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
	if err := sanitizeMCPName(args.Name); err != nil {
		return nil, err
	}
	if args.MCPConfig == "" {
		return nil, fmt.Errorf("mcp_config is required for add_mcp")
	}
	if args.Instructions == "" {
		return nil, fmt.Errorf("instructions is required for add_mcp - please provide a brief description of what this MCP server does and when to use its tools")
	}

	// Parse MCP config
	var cfg MCPServerConfig
	if err := json.Unmarshal([]byte(args.MCPConfig), &cfg); err != nil {
		return nil, fmt.Errorf("parse mcp_config: %w", err)
	}

	// Set instructions from args
	cfg.Instructions = args.Instructions

	configPath := t.resolveWritableMCPConfigPath(ctx)
	config, err := t.loadMCPConfig(configPath)
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
	if err := t.saveMCPConfig(configPath, config); err != nil {
		return nil, fmt.Errorf("save mcp config: %w", err)
	}

	results := []string{fmt.Sprintf("MCP server '%s' has been added.", args.Name)}
	if ctx != nil && ctx.InvalidateAllSessionMCP != nil {
		ctx.InvalidateAllSessionMCP()
		if ctx.Registry != nil && ctx.Registry.IsFlatMode() {
			results = append(results, "Flat memory: MCP config invalidated, new tools are immediately visible.")
		} else {
			results = append(results, "Use ManageTools' 'reload' action to connect to it.")
		}
	} else {
		results = append(results, "Use ManageTools' 'reload' action to connect to it.")
	}

	return NewResult(strings.Join(results, " ")), nil
}

func (t *ManageTools) removeMCP(ctx *ToolContext, args manageToolsArgs) (*ToolResult, error) {
	if args.Name == "" {
		return nil, fmt.Errorf("name is required for remove_mcp")
	}
	if err := sanitizeMCPName(args.Name); err != nil {
		return nil, err
	}

	configPath := t.resolveWritableMCPConfigPath(ctx)
	config, err := t.loadMCPConfig(configPath)
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
	if err := t.saveMCPConfig(configPath, config); err != nil {
		return nil, fmt.Errorf("save mcp config: %w", err)
	}

	results := []string{fmt.Sprintf("MCP server '%s' has been removed.", args.Name)}
	if ctx != nil && ctx.InvalidateAllSessionMCP != nil {
		ctx.InvalidateAllSessionMCP()
		if ctx.Registry != nil && ctx.Registry.IsFlatMode() {
			results = append(results, "Flat memory: MCP config invalidated, tool removal is immediately visible.")
		} else {
			results = append(results, "Use 'reload' action to apply changes.")
		}
	} else {
		results = append(results, "Use 'reload' action to apply changes.")
	}

	return NewResult(strings.Join(results, " ")), nil
}

type mcpServerInfo struct {
	Name         string   `json:"name"`
	Enabled      bool     `json:"enabled"`
	Protocol     string   `json:"protocol"` // "http" or "stdio"
	Command      string   `json:"command,omitempty"`
	Args         []string `json:"args,omitempty"`
	URL          string   `json:"url,omitempty"`
	Headers      any      `json:"headers,omitempty"` // map[string]string, marshaled as any for flexibility
	Instructions string   `json:"instructions,omitempty"`
}

func (t *ManageTools) listMCP(ctx *ToolContext) (*ToolResult, error) {
	globalPath := t.resolveGlobalMCPConfigPath(ctx)
	userPath := t.resolveUserMCPConfigPath(ctx)
	config, err := t.loadMergedMCPConfig(globalPath, userPath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewResult("[]"), nil
		}
		return nil, fmt.Errorf("load mcp config: %w", err)
	}

	if config == nil || len(config.MCPServers) == 0 {
		return NewResult("[]"), nil
	}

	servers := make([]mcpServerInfo, 0, len(config.MCPServers))
	for name, cfg := range config.MCPServers {
		enabled := true
		if cfg.Enabled != nil {
			enabled = *cfg.Enabled
		}

		protocol := "stdio"
		if cfg.URL != "" {
			protocol = "http"
		}

		var headers any
		if len(cfg.Headers) > 0 {
			headers = cfg.Headers
		}

		servers = append(servers, mcpServerInfo{
			Name:         name,
			Enabled:      enabled,
			Protocol:     protocol,
			Command:      cfg.Command,
			Args:         cfg.Args,
			URL:          cfg.URL,
			Headers:      headers,
			Instructions: cfg.Instructions,
		})
	}

	data, err := json.Marshal(servers)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}

	return &ToolResult{Summary: string(data)}, nil
}

func (t *ManageTools) reload(ctx *ToolContext) (*ToolResult, error) {
	results := []string{}

	// 使所有会话的 MCP 连接失效，强制重新加载配置
	if ctx.InvalidateAllSessionMCP != nil {
		ctx.InvalidateAllSessionMCP()
		results = append(results, "MCP: All session connections invalidated, will reload on next use")
	} else {
		results = append(results, "MCP: Per-session lazy loading enabled - new sessions will load updated config")
	}

	return NewResult(strings.Join(results, "\n")), nil
}

func (t *ManageTools) resolveUserMCPConfigPath(ctx *ToolContext) string {
	if ctx != nil && ctx.MCPConfigPath != "" {
		return ctx.MCPConfigPath
	}
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	return filepath.Join(t.workDir, ".xbot", "users", "anonymous", "mcp.json")
}

func (t *ManageTools) resolveWritableMCPConfigPath(ctx *ToolContext) string {
	if ctx != nil && ctx.Channel == "cli" {
		if globalPath := t.resolveGlobalMCPConfigPath(ctx); globalPath != "" {
			return globalPath
		}
	}
	return t.resolveUserMCPConfigPath(ctx)
}

func (t *ManageTools) resolveGlobalMCPConfigPath(ctx *ToolContext) string {
	if ctx != nil && ctx.GlobalMCPConfigPath != "" {
		return ctx.GlobalMCPConfigPath
	}
	return t.globalMCPConfigPath
}

func (t *ManageTools) loadMCPConfig(configPath string) (*MCPConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config MCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

func (t *ManageTools) loadMergedMCPConfig(globalPath, userPath string) (*MCPConfig, error) {
	merged := &MCPConfig{MCPServers: map[string]MCPServerConfig{}}

	if globalPath != "" {
		if cfg, err := t.loadMCPConfig(globalPath); err == nil && cfg != nil {
			for name, server := range cfg.MCPServers {
				merged.MCPServers[name] = server
			}
		}
	}

	if userPath != "" {
		cfg, err := t.loadMCPConfig(userPath)
		if err != nil {
			if os.IsNotExist(err) {
				return merged, nil
			}
			return nil, err
		}
		for name, server := range cfg.MCPServers {
			merged.MCPServers[name] = server
		}
	}

	return merged, nil
}

func (t *ManageTools) saveMCPConfig(configPath string, config *MCPConfig) error {
	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	// Atomic write: write to temp file first, then rename to final path.
	// On the same filesystem, os.Rename is an atomic operation,
	// preventing concurrent writes from corrupting the config file.
	dir := filepath.Dir(configPath)
	tmpFile, err := os.CreateTemp(dir, ".mcp-config-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// sanitizeMCPName cleans MCP server name to prevent path traversal and injection.
// Rejects names containing path separators, URL-encoded separators, or "..".
func sanitizeMCPName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("name cannot contain path separators ('/' or '\\')")
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("name cannot contain '..'")
	}
	// Also reject URL-encoded path separators
	decoded, err := url.PathUnescape(name)
	if err != nil {
		return fmt.Errorf("name contains invalid URL encoding: %w", err)
	}
	if strings.ContainsAny(decoded, "/\\") || strings.Contains(decoded, "..") {
		return fmt.Errorf("name cannot contain path separators or '..' (including URL-encoded)")
	}
	return nil
}
