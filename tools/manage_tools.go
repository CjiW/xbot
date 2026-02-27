package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// ManageTools allows the bot to add/update/remove skills and MCP servers dynamically
type ManageTools struct {
	mcpConfigPath string
	skillsDir     string
}

// NewManageTools creates a new ManageTools tool
func NewManageTools(mcpConfigPath, skillsDir string) *ManageTools {
	return &ManageTools{
		mcpConfigPath: mcpConfigPath,
		skillsDir:     skillsDir,
	}
}

func (t *ManageTools) Name() string {
	return "ManageTools"
}

func (t *ManageTools) Description() string {
	return "Manage the bot's skills and MCP servers. Can add, update, remove skills and MCP servers, and reload configurations."
}

func (t *ManageTools) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "action",
			Type:        "string",
			Description: "Action to perform: 'add_skill', 'update_skill', 'delete_skill', 'list_skills', 'add_mcp', 'remove_mcp', 'list_mcp', 'reload'",
			Required:    true,
		},
		{
			Name:        "name",
			Type:        "string",
			Description: "Name of the skill or MCP server",
			Required:    false,
		},
		{
			Name:        "content",
			Type:        "string",
			Description: "Skill content (for add/update_skill). Format: markdown with optional YAML frontmatter (name, description)",
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
	Content   string `json:"content"`
	MCPConfig string `json:"mcp_config"`
}

func (t *ManageTools) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args manageToolsArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	switch args.Action {
	case "add_skill", "update_skill":
		return t.addUpdateSkill(ctx, args)
	case "delete_skill":
		return t.deleteSkill(ctx, args)
	case "list_skills":
		return t.listSkills(ctx)
	case "add_mcp":
		return t.addMCP(ctx, args)
	case "remove_mcp":
		return t.removeMCP(ctx, args)
	case "list_mcp":
		return t.listMCP(ctx)
	case "reload":
		return t.reload(ctx)
	default:
		return nil, fmt.Errorf("unknown action: %s (valid: add_skill, update_skill, delete_skill, list_skills, add_mcp, remove_mcp, list_mcp, reload)", args.Action)
	}
}

func (t *ManageTools) addUpdateSkill(ctx *ToolContext, args manageToolsArgs) (*ToolResult, error) {
	if args.Name == "" {
		return nil, fmt.Errorf("name is required for %s", args.Action)
	}
	if args.Content == "" {
		return nil, fmt.Errorf("content is required for %s", args.Action)
	}

	if ctx.SkillStore == nil {
		return nil, fmt.Errorf("SkillStore not available")
	}

	// Save the skill
	if err := ctx.SkillStore.SaveSkill(args.Name, args.Content); err != nil {
		return nil, fmt.Errorf("save skill: %w", err)
	}

	// Activate the skill
	if err := ctx.SkillStore.Activate(args.Name); err != nil {
		return nil, fmt.Errorf("activate skill: %w", err)
	}

	return NewResult(fmt.Sprintf("Skill '%s' has been %s successfully. The skill is now active.", args.Name, strings.TrimPrefix(args.Action, "update_"))), nil
}

func (t *ManageTools) deleteSkill(ctx *ToolContext, args manageToolsArgs) (*ToolResult, error) {
	if args.Name == "" {
		return nil, fmt.Errorf("name is required for delete_skill")
	}

	if ctx.SkillStore == nil {
		return nil, fmt.Errorf("SkillStore not available")
	}

	if err := ctx.SkillStore.DeleteSkill(args.Name); err != nil {
		return nil, fmt.Errorf("delete skill: %w", err)
	}

	return NewResult(fmt.Sprintf("Skill '%s' has been deleted.", args.Name)), nil
}

func (t *ManageTools) listSkills(ctx *ToolContext) (*ToolResult, error) {
	if ctx.SkillStore == nil {
		return nil, fmt.Errorf("SkillStore not available")
	}

	skills, err := ctx.SkillStore.ListSkills()
	if err != nil {
		return nil, fmt.Errorf("list skills: %w", err)
	}

	if len(skills) == 0 {
		return NewResult("No skills found."), nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d skills:\n\n", len(skills)))
	for _, skill := range skills {
		status := "inactive"
		if skill.Active {
			status = "active"
		}
		sb.WriteString(fmt.Sprintf("- **%s** (%s): %s\n", skill.Name, status, skill.Description))
	}

	return &ToolResult{Summary: sb.String()}, nil
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
	sb.WriteString(fmt.Sprintf("Found %d MCP server(s):\n\n", len(config.MCPServers)))
	for name, cfg := range config.MCPServers {
		enabled := "enabled"
		if cfg.Enabled != nil && !*cfg.Enabled {
			enabled = "disabled"
		}
		if cfg.URL != "" {
			sb.WriteString(fmt.Sprintf("- **%s** (%s, HTTP): %s\n", name, enabled, cfg.URL))
		} else {
			sb.WriteString(fmt.Sprintf("- **%s** (%s, stdio): %s %v\n", name, enabled, cfg.Command, cfg.Args))
		}
	}

	return &ToolResult{Summary: sb.String()}, nil
}

func (t *ManageTools) reload(ctx *ToolContext) (*ToolResult, error) {
	results := []string{}

	// Reload MCP
	if ctx.MCPManager != nil {
		// Close existing connections
		ctx.MCPManager.Close()

		// Remove old MCP tools (prefix is "mcp_")
		if ctx.Registry != nil {
			for _, tool := range ctx.Registry.List() {
				if strings.HasPrefix(tool.Name(), "mcp_") {
					ctx.Registry.Unregister(tool.Name())
				}
			}
		}

		// Reconnect with timeout to avoid hanging on slow MCP servers (e.g. npx)
		reloadCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		if err := ctx.MCPManager.LoadAndConnect(reloadCtx); err != nil {
			log.WithError(err).Warn("MCP reload had errors")
			results = append(results, fmt.Sprintf("MCP reload completed with errors: %v", err))
		} else {
			count := ctx.MCPManager.ServerCount()
			results = append(results, fmt.Sprintf("MCP reloaded: %d server(s) connected", count))
		}

		// Re-register MCP tools
		if ctx.Registry != nil {
			ctx.MCPManager.RegisterTools(ctx.Registry)
			results = append(results, "MCP tools re-registered")
		}
	} else {
		results = append(results, "MCPManager not available, skipped MCP reload")
	}

	// Skills are auto-loaded from disk, no explicit reload needed
	if ctx.SkillStore != nil {
		active := ctx.SkillStore.ActiveNames()
		results = append(results, fmt.Sprintf("Skills: %d active skill(s) - %s", len(active), strings.Join(active, ", ")))
	}

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
