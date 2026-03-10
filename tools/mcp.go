package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	log "xbot/logger"

	"xbot/llm"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPManager 管理所有 MCP Server 连接
type MCPManager struct {
	mu          sync.RWMutex
	connections map[string]*mcpConnection
	configPath  string
}

// NewMCPManager 创建 MCPManager
func NewMCPManager(configPath string) *MCPManager {
	return &MCPManager{
		connections: make(map[string]*mcpConnection),
		configPath:  configPath,
	}
}

// LoadAndConnect 加载配置并连接所有 MCP Server
func (m *MCPManager) LoadAndConnect(ctx context.Context) error {
	config, err := m.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug("No mcp.json found, skipping MCP initialization")
			return nil
		}
		return fmt.Errorf("load mcp config: %w", err)
	}

	for name, serverCfg := range config.MCPServers {
		if serverCfg.Enabled != nil && !*serverCfg.Enabled {
			log.WithField("server", name).Info("MCP server disabled, skipping")
			continue
		}

		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			if err := m.connectServer(ctx, name, serverCfg); err != nil {
				lastErr = err
				log.WithError(err).WithFields(log.Fields{
					"server":  name,
					"attempt": attempt,
				}).Warn("Failed to connect MCP server, retrying...")
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
				continue
			}
			lastErr = nil
			break
		}
		if lastErr != nil {
			log.WithError(lastErr).WithField("server", name).Error("Failed to connect MCP server after 3 attempts")
		}
	}

	return nil
}

// connectServer 连接单个 MCP Server
func (m *MCPManager) connectServer(ctx context.Context, name string, cfg MCPServerConfig) error {
	var (
		session *mcp.ClientSession
		err     error
	)

	// 优先使用 HTTP transport（如果配置了 URL）
	if cfg.URL != "" {
		session, err = ConnectHTTPServer(ctx, cfg)
	} else if cfg.Command != "" {
		ws := resolveWorkspaceRoot(m.configPath)
		session, err = ConnectStdioServer(ctx, cfg, m.configPath, ws, name)
	} else {
		return fmt.Errorf("mcp server config must have either 'url' or 'command'")
	}

	if err != nil {
		return err
	}

	// 获取可用工具列表和服务器说明 (session is already initialized by Connect)
	initResult, err := InitializeMCPClient(ctx, session)
	if err != nil {
		_ = session.Close()
		return err
	}

	conn := &mcpConnection{
		name:         name,
		session:      session,
		tools:        initResult.Tools,
		instructions: initResult.Instructions,
	}

	m.mu.Lock()
	m.connections[name] = conn
	m.mu.Unlock()

	toolNames := make([]string, len(conn.tools))
	for i, t := range conn.tools {
		toolNames[i] = t.Name
	}

	log.WithFields(log.Fields{
		"server": name,
		"tools":  toolNames,
	}).Infof("MCP server connected (%d tools)", len(conn.tools))

	return nil
}

// GetCatalog 返回所有已连接 MCP Server 的目录信息（服务器名、说明、工具列表）
func (m *MCPManager) GetCatalog() []MCPServerCatalogEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.buildCatalogLocked()
}

// buildCatalogLocked 构建目录（必须在持有读锁时调用）
func (m *MCPManager) buildCatalogLocked() []MCPServerCatalogEntry {
	var catalog []MCPServerCatalogEntry
	for _, conn := range m.connections {
		toolNames := make([]string, len(conn.tools))
		for i, t := range conn.tools {
			toolNames[i] = t.Name
		}
		catalog = append(catalog, MCPServerCatalogEntry{
			Name:         conn.name,
			Instructions: conn.instructions,
			ToolNames:    toolNames,
		})
	}
	return catalog
}

// RegisterTools 将所有 MCP 远程工具注册到 Registry，并更新目录信息
func (m *MCPManager) RegisterTools(registry *Registry) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, conn := range m.connections {
		for _, tool := range conn.tools {
			remoteTool := newMCPRemoteTool(conn.name, tool, conn.session)
			registry.Register(remoteTool)
		}
	}

	// 更新 Registry 中的全局 MCP 目录信息
	catalog := m.buildCatalogLocked()
	registry.SetGlobalMCPCatalog(catalog)
}

// ReconnectServer 重新连接指定 MCP Server（原子替换：先连新再清旧，无工具空窗期）
func (m *MCPManager) ReconnectServer(ctx context.Context, name string, registry *Registry) error {
	config, err := m.loadConfig()
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}

	serverCfg, ok := config.MCPServers[name]
	if !ok {
		return fmt.Errorf("server %q not found in config", name)
	}

	if serverCfg.Enabled != nil && !*serverCfg.Enabled {
		return fmt.Errorf("server %q is disabled", name)
	}

	m.mu.RLock()
	oldConn := m.connections[name]
	m.mu.RUnlock()

	if err := m.connectServer(ctx, name, serverCfg); err != nil {
		return fmt.Errorf("reconnect %s: %w", name, err)
	}

	m.mu.RLock()
	newConn := m.connections[name]
	m.mu.RUnlock()

	if newConn != nil {
		for _, tool := range newConn.tools {
			remoteTool := newMCPRemoteTool(newConn.name, tool, newConn.session)
			registry.Register(remoteTool)
		}
	}

	if oldConn != nil {
		newTools := make(map[string]bool)
		if newConn != nil {
			for _, t := range newConn.tools {
				newTools[fmt.Sprintf("mcp_%s_%s", name, t.Name)] = true
			}
		}
		for _, t := range oldConn.tools {
			toolName := fmt.Sprintf("mcp_%s_%s", name, t.Name)
			if !newTools[toolName] {
				registry.Unregister(toolName)
			}
		}
		_ = oldConn.session.Close()
	}

	log.WithField("server", name).Info("MCP server reconnected")

	// 更新 Registry 中的全局 MCP 目录信息（含新 server 的工具列表）
	registry.SetGlobalMCPCatalog(m.GetCatalog())

	return nil
}

// Close 关闭所有 MCP 连接（并发关闭，带超时）
func (m *MCPManager) Close() {
	m.mu.Lock()
	conns := m.connections
	m.connections = make(map[string]*mcpConnection)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for name, conn := range conns {
		wg.Add(1)
		go func(nm string, c *mcpConnection) {
			defer wg.Done()
			if err := c.session.Close(); err != nil {
				if !IsProcessExitError(err) {
					log.WithError(err).WithField("server", nm).Warn("Error closing MCP connection")
				} else {
					log.WithField("server", nm).Debug("MCP connection closed (process exited)")
				}
			}
		}(name, conn)
	}
	wg.Wait()
}

// ServerCount 返回已连接的 MCP Server 数量
func (m *MCPManager) ServerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.connections)
}

// loadConfig 从 JSON 文件加载 MCP 配置
func (m *MCPManager) loadConfig() (*MCPConfig, error) {
	return LoadMCPConfig(m.configPath)
}

// ---- MCPRemoteTool: 将 MCP 远程工具适配为 xbot Tool 接口 ----

// MCPRemoteTool 封装一个远程 MCP 工具为 xbot Tool
type MCPRemoteTool struct {
	serverName  string
	tool        *mcp.Tool
	session     *mcp.ClientSession
	params      []llm.ToolParam
	description string
}

// newMCPRemoteTool 创建 MCPRemoteTool
func newMCPRemoteTool(serverName string, tool *mcp.Tool, session *mcp.ClientSession) *MCPRemoteTool {
	params := convertMCPParams(tool)
	desc := tool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s", serverName)
	}

	return &MCPRemoteTool{
		serverName:  serverName,
		tool:        tool,
		session:     session,
		params:      params,
		description: desc,
	}
}

func (t *MCPRemoteTool) Name() string {
	// 添加 server 前缀避免工具名冲突
	return fmt.Sprintf("mcp_%s_%s", t.serverName, t.tool.Name)
}

func (t *MCPRemoteTool) Description() string {
	return fmt.Sprintf("[MCP:%s] %s", t.serverName, t.description)
}

func (t *MCPRemoteTool) Parameters() []llm.ToolParam {
	// Stub mode: return nil so full schemas are not loaded into LLM context.
	// Call load_mcp_tools_usage to get parameter details before invoking this tool.
	return nil
}

// fullDescription returns the original server description (used by load_mcp_tools_usage).
func (t *MCPRemoteTool) fullDescription() string {
	return t.description
}

// fullParams returns the complete parameter list (used by load_mcp_tools_usage).
func (t *MCPRemoteTool) fullParams() []llm.ToolParam {
	return t.params
}

// mcpServerName returns the MCP server name this tool belongs to.
func (t *MCPRemoteTool) mcpServerName() string {
	return t.serverName
}

func (t *MCPRemoteTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args map[string]any
	if input != "" && input != "{}" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}

	result, err := t.session.CallTool(ctx.Ctx, &mcp.CallToolParams{
		Name:      t.tool.Name,
		Arguments: args,
	})
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"server": t.serverName,
			"tool":   t.tool.Name,
		}).Warn("MCP tool call failed")
		return nil, fmt.Errorf("MCP call %s/%s: %w", t.serverName, t.tool.Name, err)
	}

	content := formatMCPResult(result)

	if result.IsError {
		log.WithFields(log.Fields{
			"server": t.serverName,
			"tool":   t.tool.Name,
		}).Warnf("MCP tool returned error: %s", content)
		return NewResult("Error: " + content), nil
	}

	return NewResult(content), nil
}
