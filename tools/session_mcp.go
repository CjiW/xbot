package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"

	"xbot/llm"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxMCPConnections = 20

// errNotInitialized indicates MCP config files don't exist yet.
// The caller should NOT set initialized=true so that the next access retries.
var errNotInitialized = fmt.Errorf("MCP config not found, will retry on next access")

// SessionMCPManager 管理单个会话的 MCP 连接
type SessionMCPManager struct {
	mu                sync.RWMutex
	sessionKey        string                    // "channel:chatID"
	userID            string                    // 用户 ID（用于沙箱容器标识）
	globalConfigPath  string                    // 全局 mcp.json 路径（只读）
	userConfigPath    string                    // 用户 mcp.json 路径（可写）
	workspaceRoot     string                    // 用户命令执行工作区
	connections       map[string]*mcpConnection // 懒加载的连接
	lastActive        map[string]time.Time      // 每个服务器的最后活跃时间
	sessionLastUsed   time.Time                 // 会话级别活跃时间
	inactivityTimeout time.Duration             // 不活跃超时配置
	initialized       bool                      // 是否已初始化配置加载
	initOnce          uint32                    // atomic state: 0=idle, 1=starting, 2=started (background goroutine launched)
	initDone          chan struct{}             // 后台初始化完成信号（closed = done）
	onChange          func()                    // 初始化完成后的回调（通知调用方重新索引）
}

// NewSessionMCPManager 创建会话 MCP 管理器
func NewSessionMCPManager(sessionKey, userID, globalConfigPath, userConfigPath, workspaceRoot string, inactivityTimeout time.Duration) *SessionMCPManager {
	return &SessionMCPManager{
		sessionKey:        sessionKey,
		userID:            userID,
		globalConfigPath:  globalConfigPath,
		userConfigPath:    userConfigPath,
		workspaceRoot:     workspaceRoot,
		connections:       make(map[string]*mcpConnection),
		lastActive:        make(map[string]time.Time),
		sessionLastUsed:   time.Now(),
		inactivityTimeout: inactivityTimeout,
		initDone:          make(chan struct{}),
	}
}

// UpdateScope 更新当前会话可见的用户配置与工作区。
func (sm *SessionMCPManager) UpdateScope(userID, userConfigPath, workspaceRoot string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.userID == userID && sm.userConfigPath == userConfigPath && sm.workspaceRoot == workspaceRoot {
		return
	}

	for _, conn := range sm.connections {
		sm.closeConnection(conn)
	}
	sm.connections = make(map[string]*mcpConnection)
	sm.lastActive = make(map[string]time.Time)
	sm.userID = userID
	sm.userConfigPath = userConfigPath
	sm.workspaceRoot = workspaceRoot
	sm.initialized = false
	sm.initOnce = 0
	sm.initDone = make(chan struct{})
}

// GetCatalog 返回此会话所有已连接 MCP Server 的目录信息。
// 首次调用时启动后台初始化（非阻塞），立即返回空 catalog。
// 后续调用在后台初始化完成后返回完整 catalog。
func (sm *SessionMCPManager) GetCatalog() []MCPServerCatalogEntry {
	sm.ensureInitAsync()

	sm.mu.RLock()
	defer sm.mu.RUnlock()

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

// GetCatalogBlocking blocks until initialization is complete, then returns the catalog.
// Use this when the caller needs the full catalog immediately and can tolerate blocking.
func (sm *SessionMCPManager) GetCatalogBlocking() []MCPServerCatalogEntry {
	sm.ensureInitAsync()
	<-sm.initDone
	return sm.GetCatalog()
}

// ensureInitAsync starts background initialization on first call (idempotent).
// On errNotInitialized, it resets so the next access retries.
func (sm *SessionMCPManager) ensureInitAsync() {
	// Fast path: already started
	if atomic.LoadUint32(&sm.initOnce) != 0 {
		return
	}
	// CAS from 0→1: we are the one to start the background goroutine
	if !atomic.CompareAndSwapUint32(&sm.initOnce, 0, 1) {
		return
	}
	go func() {
		sm.mu.Lock()
		if err := sm.loadAndConnect(context.Background()); err != nil {
			if err != errNotInitialized {
				log.WithError(err).WithField("session", sm.sessionKey).Warn("Failed to load MCP servers for catalog")
			}
			// Reset to idle so the next access retries (config may be created later).
			// Safe because only the goroutine that won CAS(0→1) can CAS back to 0,
			// and concurrent callers that saw 1 will return on the fast path above.
			atomic.StoreUint32(&sm.initOnce, 0)
			sm.mu.Unlock()
			return
		}
		sm.initialized = true
		// Mark as fully started (prevent any retry)
		atomic.StoreUint32(&sm.initOnce, 2)
		// Close initDone to signal completion
		if ch := sm.initDone; ch != nil {
			close(ch)
		}
		onChange := sm.onChange
		sm.mu.Unlock()
		if onChange != nil {
			onChange()
		}
	}()
}

// SetOnChange registers a callback invoked after background initialization completes.
// Must be called before GetCatalog to guarantee the callback fires.
func (sm *SessionMCPManager) SetOnChange(fn func()) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.onChange = fn
	// If already initialized, invoke immediately
	if sm.initialized {
		fn()
	}
}

// GetSessionTools 懒加载并返回此会话的 MCP 工具（非阻塞）。
// 首次调用时启动后台初始化，立即返回已有工具列表。
func (sm *SessionMCPManager) GetSessionTools() []Tool {
	sm.ensureInitAsync()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 标记会话为活跃
	sm.sessionLastUsed = time.Now()

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

	// 有服务器被卸载时重置 initialized，使下次访问时触发 loadAndConnect 重连
	if len(serversToUnload) > 0 {
		sm.initialized = false
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

	for _, conn := range sm.connections {
		sm.closeConnection(conn)
	}
	sm.connections = make(map[string]*mcpConnection)
	sm.lastActive = make(map[string]time.Time)
	sm.initialized = false
	sm.initOnce = 0
	sm.initDone = make(chan struct{})

	log.WithField("session", sm.sessionKey).Info("Session MCP invalidated, will reload on next use")
}

// loadAndConnect 加载配置并连接所有启用的 MCP Server（跳过已连接的服务器）
func (sm *SessionMCPManager) loadAndConnect(ctx context.Context) error {
	config, err := sm.loadConfig()
	if err != nil {
		if os.IsNotExist(err) {
			// 配置文件暂不存在，返回 errNotInitialized 让调用方不标记 initialized=true，
			// 以便下次调用时重试（配置可能稍后被 ManageTools 创建）。
			return errNotInitialized
		}
		return fmt.Errorf("load mcp config: %w", err)
	}

	for name, serverCfg := range config.MCPServers {
		if serverCfg.Enabled != nil && !*serverCfg.Enabled {
			continue
		}

		// 跳过已连接的服务器，避免重复连接
		if _, connected := sm.connections[name]; connected {
			continue
		}

		if err := sm.connectServer(ctx, name, serverCfg); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"session": sm.sessionKey,
				"server":  name,
			}).Warn("MCP server connection failed")
		}
	}

	return nil
}

// connectServer 连接单个 MCP Server
func (sm *SessionMCPManager) connectServer(ctx context.Context, name string, cfg MCPServerConfig) error {
	// 限制最大连接数，防止恶意或异常情况创建大量连接
	if len(sm.connections) >= maxMCPConnections {
		return fmt.Errorf("MCP connection limit reached (%d), cannot connect server %q", maxMCPConnections, name)
	}

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
		session, err = ConnectStdioServer(ctx, cfg, configPath, sm.workspaceRoot, sm.userID, name)
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

	// 优先使用服务器返回的 instructions，否则使用 config 中的 fallback
	instructions := initResult.Instructions
	if instructions == "" {
		instructions = cfg.Instructions
	}

	conn := &mcpConnection{
		name:         name,
		session:      session,
		tools:        initResult.Tools,
		instructions: instructions,
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
			if err := json.Unmarshal(data, &cfg); err != nil {
				log.Errorf("Failed to parse global MCP configuration JSON: path=%s, error=%v", sm.globalConfigPath, err)
			} else {
				for name, server := range cfg.MCPServers {
					merged.MCPServers[name] = server
				}
			}
		} else if !os.IsNotExist(err) {
			log.WithError(err).WithField("path", sm.globalConfigPath).Warn("Failed to read global MCP config")
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
	// Call load_tools to get parameter details before invoking this tool.
	return nil
}

// fullDescription returns the original server description (used by load_tools).
func (t *SessionMCPRemoteTool) fullDescription() string {
	return t.description
}

// fullParams returns the complete parameter list (used by load_tools).
func (t *SessionMCPRemoteTool) fullParams() []llm.ToolParam {
	return t.params
}

// mcpServerName returns the MCP server name this tool belongs to.
func (t *SessionMCPRemoteTool) mcpServerName() string {
	return t.serverName
}

func (t *SessionMCPRemoteTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	if t.sessionMCPMgr != nil {
		t.sessionMCPMgr.MarkActive(t.serverName)
	}

	// 检查 session 是否仍然有效（可能已被 Close/Invalidate 关闭）
	if t.session == nil {
		return nil, fmt.Errorf("MCP session for server %q has been closed", t.serverName)
	}

	args := map[string]any{}
	if input != "" {
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

// ---- MCP 工具激活机制 ----

// GetActivatedToolDefs 返回已激活 MCP 工具的 LLM 工具定义（含完整参数 schema）。
// activated 由 Registry.sessionActivated 提供，统一管理激活状态。
func (sm *SessionMCPManager) GetActivatedToolDefs(activated map[string]bool) []llm.ToolDefinition {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(activated) == 0 {
		return nil
	}

	var defs []llm.ToolDefinition
	for _, conn := range sm.connections {
		for _, tool := range conn.tools {
			fullName := fmt.Sprintf("mcp_%s_%s", conn.name, tool.Name)
			if !activated[fullName] {
				continue
			}
			params := convertMCPParams(tool)
			desc := tool.Description
			if desc == "" {
				desc = fmt.Sprintf("MCP tool from %s", conn.name)
			}
			defs = append(defs, &mcpToolDefinition{
				name:   fullName,
				desc:   fmt.Sprintf("[MCP:%s] %s", conn.name, desc),
				params: params,
			})
		}
	}
	return defs
}

// mcpToolDefinition 是已激活 MCP 工具的 LLM 工具定义（含完整参数 schema）。
type mcpToolDefinition struct {
	name   string
	desc   string
	params []llm.ToolParam
}

func (d *mcpToolDefinition) Name() string                { return d.name }
func (d *mcpToolDefinition) Description() string         { return d.desc }
func (d *mcpToolDefinition) Parameters() []llm.ToolParam { return d.params }
