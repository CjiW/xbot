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

// SessionMCPManager 管理单个会话的 MCP 连接
type SessionMCPManager struct {
	mu                sync.RWMutex
	sessionKey        string                    // "channel:chatID"
	globalConfigPath  string                    // 全局 mcp.json 路径（只读）
	userConfigPath    string                    // 用户 mcp.json 路径（可写）
	workspaceRoot     string                    // 用户命令执行工作区
	connections       map[string]*mcpConnection // 懒加载的连接
	lastActive        map[string]time.Time      // 每个服务器的最后活跃时间
	sessionLastUsed   time.Time                 // 会话级别活跃时间
	inactivityTimeout time.Duration             // 不活跃超时配置
	initialized       bool                      // 是否已初始化配置加载
}

// NewSessionMCPManager 创建会话 MCP 管理器
func NewSessionMCPManager(sessionKey, globalConfigPath, userConfigPath, workspaceRoot string, inactivityTimeout time.Duration) *SessionMCPManager {
	return &SessionMCPManager{
		sessionKey:        sessionKey,
		globalConfigPath:  globalConfigPath,
		userConfigPath:    userConfigPath,
		workspaceRoot:     workspaceRoot,
		connections:       make(map[string]*mcpConnection),
		lastActive:        make(map[string]time.Time),
		sessionLastUsed:   time.Now(),
		inactivityTimeout: inactivityTimeout,
	}
}

// UpdateScope 更新当前会话可见的用户配置与工作区。
func (sm *SessionMCPManager) UpdateScope(userConfigPath, workspaceRoot string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.userConfigPath == userConfigPath && sm.workspaceRoot == workspaceRoot {
		return
	}

	for _, conn := range sm.connections {
		sm.closeConnection(conn)
	}
	sm.connections = make(map[string]*mcpConnection)
	sm.lastActive = make(map[string]time.Time)
	sm.userConfigPath = userConfigPath
	sm.workspaceRoot = workspaceRoot
	sm.initialized = false
}

// GetCatalog 返回此会话所有已连接 MCP Server 的目录信息（需在锁外调用，内部加锁）
func (sm *SessionMCPManager) GetCatalog() []MCPServerCatalogEntry {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 首次调用时确保配置已加载
	if !sm.initialized {
		if err := sm.loadAndConnect(context.Background()); err != nil {
			log.WithError(err).WithField("session", sm.sessionKey).Warn("Failed to load MCP servers for catalog")
			sm.initialized = true
			return nil
		}
		sm.initialized = true
	}

	var catalog []MCPServerCatalogEntry
	for _, conn := range sm.connections {
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

// GetSessionTools 懒加载并返回此会话的 MCP 工具
func (sm *SessionMCPManager) GetSessionTools() []Tool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 标记会话为活跃
	sm.sessionLastUsed = time.Now()

	// 首次调用时加载配置
	if !sm.initialized {
		if err := sm.loadAndConnect(context.Background()); err != nil {
			log.WithError(err).WithField("session", sm.sessionKey).Warn("Failed to load MCP servers for session")
			sm.initialized = true // 标记为已尝试，避免重复尝试
			return nil
		}
		sm.initialized = true
	}

	// 收集所有 MCP 工具
	var tools []Tool
	for _, conn := range sm.connections {
		for _, tool := range conn.tools {
			remoteTool := newSessionMCPRemoteTool(conn.name, tool, conn.session, sm)
			tools = append(tools, remoteTool)
		}
	}

	return tools
}

// MarkActive 标记服务器为活跃状态
func (sm *SessionMCPManager) MarkActive(serverName string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.lastActive[serverName] = time.Now()
	sm.sessionLastUsed = time.Now()
}

// UnloadInactiveServers 卸载超时不活跃的服务器
// 返回会话最后活跃时间（用于判断会话是否需要从缓存中移除）
func (sm *SessionMCPManager) UnloadInactiveServers() time.Time {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	var serversToUnload []string

	// 检查每个服务器的活跃状态
	for name, lastActive := range sm.lastActive {
		if now.Sub(lastActive) > sm.inactivityTimeout {
			serversToUnload = append(serversToUnload, name)
		}
	}

	// 卸载不活跃的服务器
	for _, name := range serversToUnload {
		if conn, ok := sm.connections[name]; ok {
			sm.closeConnection(conn)
			delete(sm.connections, name)
			delete(sm.lastActive, name)
			log.WithFields(log.Fields{
				"session": sm.sessionKey,
				"server":  name,
			}).Info("Unloaded inactive MCP server")
		}
	}

	return sm.sessionLastUsed
}

// Close 关闭所有连接
func (sm *SessionMCPManager) Close() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for name, conn := range sm.connections {
		sm.closeConnection(conn)
		log.WithFields(log.Fields{
			"session": sm.sessionKey,
			"server":  name,
		}).Debug("Closed MCP connection")
	}

	sm.connections = make(map[string]*mcpConnection)
	sm.lastActive = make(map[string]time.Time)
}

// Invalidate 重置初始化标志，强制下次调用时重新加载配置
func (sm *SessionMCPManager) Invalidate() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 关闭所有现有连接
	for _, conn := range sm.connections {
		sm.closeConnection(conn)
	}
	sm.connections = make(map[string]*mcpConnection)
	sm.lastActive = make(map[string]time.Time)

	// 重置初始化标志
	sm.initialized = false

	log.WithField("session", sm.sessionKey).Info("Session MCP invalidated, will reload on next use")
}

// loadAndConnect 加载配置并连接所有启用的 MCP Server
func (sm *SessionMCPManager) loadAndConnect(ctx context.Context) error {
	config, err := sm.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 没有 mcp.json 不是错误
		}
		return fmt.Errorf("load mcp config: %w", err)
	}

	for name, serverCfg := range config.MCPServers {
		if serverCfg.Enabled != nil && !*serverCfg.Enabled {
			continue
		}

		// 尝试连接，最多 3 次
		var lastErr error
		for attempt := 1; attempt <= 3; attempt++ {
			if err := sm.connectServer(ctx, name, serverCfg); err != nil {
				lastErr = err
				time.Sleep(time.Duration(attempt) * 2 * time.Second)
				continue
			}
			lastErr = nil
			break
		}
		if lastErr != nil {
			log.WithError(lastErr).WithFields(log.Fields{
				"session": sm.sessionKey,
				"server":  name,
			}).Warn("Failed to connect MCP server after 3 attempts")
		}
	}

	return nil
}

// connectServer 连接单个 MCP Server
func (sm *SessionMCPManager) connectServer(ctx context.Context, name string, cfg MCPServerConfig) error {
	var (
		session *mcp.ClientSession
		err     error
	)

	// 优先使用 HTTP transport（如果配置了 URL）
	if cfg.URL != "" {
		session, err = ConnectHTTPServer(ctx, cfg)
	} else if cfg.Command != "" {
		configPath := sm.globalConfigPath
		if configPath == "" {
			configPath = sm.userConfigPath
		}
		session, err = ConnectStdioServer(ctx, cfg, configPath, sm.workspaceRoot)
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

	sm.connections[name] = conn
	sm.lastActive[name] = time.Now() // 初始化时标记为活跃

	toolNames := make([]string, len(conn.tools))
	for i, t := range conn.tools {
		toolNames[i] = t.Name
	}

	log.WithFields(log.Fields{
		"session": sm.sessionKey,
		"server":  name,
		"tools":   toolNames,
	}).Infof("MCP server connected for session (%d tools)", len(conn.tools))

	return nil
}

// closeConnection 关闭单个连接
func (sm *SessionMCPManager) closeConnection(conn *mcpConnection) {
	if conn != nil && conn.session != nil {
		if err := conn.session.Close(); err != nil {
			if !IsProcessExitError(err) {
				log.WithError(err).Debug("Error closing MCP session")
			}
		}
	}
}

// loadConfig 从 JSON 文件加载 MCP 配置
func (sm *SessionMCPManager) loadConfig() (*MCPConfig, error) {
	merged := &MCPConfig{MCPServers: map[string]MCPServerConfig{}}

	if sm.globalConfigPath != "" {
		if data, err := os.ReadFile(sm.globalConfigPath); err == nil {
			var cfg MCPConfig
			if err := json.Unmarshal(data, &cfg); err == nil {
				for name, server := range cfg.MCPServers {
					merged.MCPServers[name] = server
				}
			}
		}
	}

	if sm.userConfigPath == "" {
		if len(merged.MCPServers) == 0 {
			return nil, os.ErrNotExist
		}
		return merged, nil
	}

	data, err := os.ReadFile(sm.userConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			if len(merged.MCPServers) == 0 {
				return nil, err
			}
			return merged, nil
		}
		return nil, err
	}

	var userConfig MCPConfig
	if err := json.Unmarshal(data, &userConfig); err != nil {
		return nil, fmt.Errorf("parse mcp.json: %w", err)
	}
	for name, server := range userConfig.MCPServers {
		merged.MCPServers[name] = server
	}

	return merged, nil
}

// ---- SessionMCPRemoteTool: 会话感知的 MCP 远程工具 ----

// SessionMCPRemoteTool 封装一个远程 MCP 工具为 xbot Tool（会话感知）
type SessionMCPRemoteTool struct {
	serverName    string
	tool          *mcp.Tool
	session       *mcp.ClientSession
	sessionMCPMgr *SessionMCPManager // 会话 MCP 管理器
	params        []llm.ToolParam
	description   string
}

// newSessionMCPRemoteTool 创建 SessionMCPRemoteTool
func newSessionMCPRemoteTool(serverName string, tool *mcp.Tool, session *mcp.ClientSession, sessionMCPMgr *SessionMCPManager) *SessionMCPRemoteTool {
	params := convertMCPParams(tool)
	desc := tool.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool from %s", serverName)
	}

	return &SessionMCPRemoteTool{
		serverName:    serverName,
		tool:          tool,
		session:       session,
		sessionMCPMgr: sessionMCPMgr,
		params:        params,
		description:   desc,
	}
}

func (t *SessionMCPRemoteTool) Name() string {
	return fmt.Sprintf("mcp_%s_%s", t.serverName, t.tool.Name)
}

func (t *SessionMCPRemoteTool) Description() string {
	return fmt.Sprintf("[MCP:%s] %s", t.serverName, t.description)
}

func (t *SessionMCPRemoteTool) Parameters() []llm.ToolParam {
	// Stub mode: return nil so full schemas are not loaded into LLM context.
	// Call load_mcp_tools_usage to get parameter details before invoking this tool.
	return nil
}

// fullDescription returns the original server description (used by load_mcp_tools_usage).
func (t *SessionMCPRemoteTool) fullDescription() string {
	return t.description
}

// fullParams returns the complete parameter list (used by load_mcp_tools_usage).
func (t *SessionMCPRemoteTool) fullParams() []llm.ToolParam {
	return t.params
}

// mcpServerName returns the MCP server name this tool belongs to.
func (t *SessionMCPRemoteTool) mcpServerName() string {
	return t.serverName
}

func (t *SessionMCPRemoteTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	// 标记服务器为活跃
	if t.sessionMCPMgr != nil {
		t.sessionMCPMgr.MarkActive(t.serverName)
	}

	// 解析 JSON 参数为 map
	var args map[string]any
	if input != "" && input != "{}" {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return nil, fmt.Errorf("invalid arguments: %w", err)
		}
	}

	// 调用远程工具
	result, err := t.session.CallTool(ctx.Ctx, &mcp.CallToolParams{
		Name:      t.tool.Name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("MCP call %s/%s: %w", t.serverName, t.tool.Name, err)
	}

	// 将 MCP 结果转为字符串
	content := formatMCPResult(result)

	if result.IsError {
		return nil, fmt.Errorf("MCP tool error: %s", content)
	}

	return NewResult(content), nil
}
