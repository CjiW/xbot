package tools

import (
	"context"
	"sort"
	"sync"
	"xbot/llm"
	"xbot/memory"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
)

// SessionMCPManagerProvider 会话 MCP 管理器提供者接口
type SessionMCPManagerProvider interface {
	GetSessionMCPManager(sessionKey string) *SessionMCPManager
}

// ToolContext 工具执行上下文
type ToolContext struct {
	Ctx                     context.Context                                 // 可取消的上下文，用于响应 stop 信号
	WorkingDir              string                                          // Agent 的工作目录
	WorkspaceRoot           string                                          // 当前用户可读写工作区根目录（宿主机路径）
	SandboxWorkDir          string                                          // 沙箱内工作目录（如 Docker 为 /workspace，非沙箱时与 WorkspaceRoot 相同）
	ReadOnlyRoots           []string                                        // 当前用户额外可读目录（只读）
	SkillsDirs              []string                                        // 全局 skill 目录列表（宿主机路径，同步源）
	AgentsDir               string                                          // 全局 agents 目录（宿主机路径，同步源）
	MCPConfigPath           string                                          // 当前用户 MCP 配置路径
	GlobalMCPConfigPath     string                                          // 全局 MCP 配置路径（只读）
	SandboxEnabled          bool                                            // 是否启用命令沙箱
	PreferredSandbox        string                                          // 沙箱优先级（docker 优先）
	AgentID                 string                                          // 当前 Agent 的 ID
	Manager                 SubAgentManager                                 // Agent 管理器引用（用于创建 SubAgent）
	DataDir                 string                                          // 数据持久化目录
	Channel                 string                                          // 当前消息来源渠道
	ChatID                  string                                          // 当前消息来源会话
	SenderID                string                                          // 当前消息发送者 ID
	SenderName              string                                          // 当前消息发送者姓名
	SendFunc                func(channel, chatID, content string) error     // 向 IM 渠道发送消息（不经过 Agent），返回错误
	InjectInbound           func(channel, chatID, senderID, content string) // 注入入站消息，触发 Agent 完整处理循环
	Registry                *Registry                                       // 工具注册表引用（用于动态注册工具）
	InvalidateAllSessionMCP func()                                          // 使所有会话的 MCP 连接失效

	// Letta memory fields (nil when memory provider is not letta)
	TenantID        int64                        // 当前租户 ID
	CoreMemory      *sqlite.CoreMemoryService    // 核心记忆存储
	ArchivalMemory  *vectordb.ArchivalService    // 归档记忆存储（chromem-go 向量数据库）
	MemorySvc       *sqlite.MemoryService        // 事件历史存储（用于 rethink 日志）
	RecallTimeRange vectordb.RecallTimeRangeFunc // 时间范围会话历史搜索
	ToolIndexer     memory.ToolIndexer           // 工具索引服务（Letta 模式下可用）
}

// SubAgentManager SubAgent 管理接口，避免循环依赖
type SubAgentManager interface {
	// RunSubAgent 创建并运行一个 SubAgent，返回最终响应文本
	// allowedTools 为工具白名单，为空时使用所有工具（除 SubAgent）
	// caps 声明 SubAgent 可获得的能力（memory、send_message 等）
	RunSubAgent(parentCtx *ToolContext, task string, systemPrompt string, allowedTools []string, caps SubAgentCapabilities, roleName string) (string, error)
}

// ToolResult 工具执行结果
type ToolResult struct {
	Summary     string `json:"summary,omitempty"` // 精简结果，log用
	Detail      string `json:"detail,omitempty"`  // 详细内容
	Tips        string `json:"tips,omitempty"`    // 操作指引，帮助 LLM 理解下一步操作
	WaitingUser bool   `json:"-"`                 // 控制字段：是否等待用户响应（不进入 LLM 上下文）
	IsError     bool   `json:"-"`                 // 控制字段：工具本身执行成功但底层操作失败（如 shell 非零退出码），影响进度图标
}

// NewResult 创建 Summary == Detail 的简单结果
func NewResult(content string) *ToolResult {
	return &ToolResult{Summary: content}
}

// NewErrorResult 创建表示底层操作失败的结果（如 shell 非零退出码）
// 区别于返回 error：工具本身执行成功（JSON 解析、沙箱启动等正常），但命令/操作失败
func NewErrorResult(content string) *ToolResult {
	return &ToolResult{Summary: content, IsError: true}
}

// NewResultWithUserResponse 创建结果并标记为等待用户响应
func NewResultWithUserResponse(summary string) *ToolResult {
	return &ToolResult{Summary: summary, WaitingUser: true}
}

// NewResultWithDetail 创建带详情的结果
func NewResultWithDetail(summary, detail string) *ToolResult {
	return &ToolResult{Summary: summary, Detail: detail}
}

// NewResultWithTips 创建带指引的结果
func NewResultWithTips(summary, tips string) *ToolResult {
	return &ToolResult{Summary: summary, Tips: tips}
}

func (r *ToolResult) WithDetail(detail string) *ToolResult {
	r.Detail = detail
	return r
}

func (r *ToolResult) WithTips(tips string) *ToolResult {
	r.Tips = tips
	return r
}

// Tool 工具接口
type Tool interface {
	llm.ToolDefinition
	Execute(ctx *ToolContext, input string) (*ToolResult, error)
}

const defaultMaxIdleRounds int64 = 5

// Registry 工具注册表
type Registry struct {
	mu               sync.RWMutex
	globalTools      map[string]Tool             // 所有工具（全局共享）
	coreTools        map[string]bool             // 核心工具名（始终在 tool definitions 中）
	sessionActivated map[string]map[string]int64 // sessionKey → toolName → lastUsedRound
	sessionRound     map[string]int64            // sessionKey → 当前 round 计数
	maxIdleRounds    int64                       // 连续多少轮未使用后自动失效
	sessionMCPMgr    SessionMCPManagerProvider   // 会话MCP管理器提供者
	globalMCPCatalog []MCPServerCatalogEntry     // 全局 MCP Server 目录（由 MCPManager.RegisterTools 设置）
}

// NewRegistry 创建工具注册表
func NewRegistry() *Registry {
	return &Registry{
		globalTools:      make(map[string]Tool),
		coreTools:        make(map[string]bool),
		sessionActivated: make(map[string]map[string]int64),
		sessionRound:     make(map[string]int64),
		maxIdleRounds:    defaultMaxIdleRounds,
	}
}

// Register 注册工具（非核心，需通过 load_tools 激活后才出现在 tool definitions 中）
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalTools[tool.Name()] = tool
}

// RegisterCore 注册核心工具（始终出现在 tool definitions 中，无需激活）
func (r *Registry) RegisterCore(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalTools[tool.Name()] = tool
	r.coreTools[tool.Name()] = true
}

// Unregister 注销工具
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.globalTools, name)
}

// Get 获取工具
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.globalTools[name]
	return tool, ok
}

// List 列出所有工具（按名称排序，保证顺序稳定以优化 KV-cache）
func (r *Registry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tools := make([]Tool, 0, len(r.globalTools))
	for _, tool := range r.globalTools {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Name() < tools[j].Name()
	})
	return tools
}

// AsDefinitions 转换为 LLM 工具定义列表（仅核心工具，按名称排序）
func (r *Registry) AsDefinitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var defs []llm.ToolDefinition
	for _, tool := range r.globalTools {
		if r.coreTools[tool.Name()] {
			defs = append(defs, tool)
		}
	}
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name() < defs[j].Name()
	})
	return defs
}

// SetSessionMCPManagerProvider 设置会话 MCP 管理器提供者
func (r *Registry) SetSessionMCPManagerProvider(provider SessionMCPManagerProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionMCPMgr = provider
}

// AsDefinitionsForSession 获取特定会话的工具定义：
//   - 核心工具始终包含
//   - 非核心工具仅在激活且未过期（maxIdleRounds 内有使用）时才包含
//   - 全局 MCP 工具激活后以完整参数 schema 加入（而非 stub 模式的空 params）
func (r *Registry) AsDefinitionsForSession(sessionKey string) []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	active := r.activeToolSet(sessionKey)

	var defs []llm.ToolDefinition
	for _, tool := range r.globalTools {
		if mcp, isMCP := tool.(mcpSchemaProvider); isMCP {
			// 全局 MCP 工具：仅在激活后以完整参数 schema 加入
			if active[tool.Name()] {
				defs = append(defs, &mcpToolDefinition{
					name:   tool.Name(),
					desc:   tool.Description(),
					params: mcp.fullParams(),
				})
			}
			continue
		}
		if r.coreTools[tool.Name()] || active[tool.Name()] {
			defs = append(defs, tool)
		}
	}

	// 追加已激活的会话 MCP 工具（带完整参数 schema）
	if r.sessionMCPMgr != nil {
		if sm := r.sessionMCPMgr.GetSessionMCPManager(sessionKey); sm != nil {
			defs = append(defs, sm.GetActivatedToolDefs(active)...)
		}
	}

	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name() < defs[j].Name()
	})

	return defs
}

// activeToolSet 返回指定会话中未过期的已激活工具名集合（调用方需持有 r.mu 读锁）
func (r *Registry) activeToolSet(sessionKey string) map[string]bool {
	toolRounds := r.sessionActivated[sessionKey]
	if len(toolRounds) == 0 {
		return nil
	}
	curRound := r.sessionRound[sessionKey]
	active := make(map[string]bool, len(toolRounds))
	for name, lastRound := range toolRounds {
		if curRound-lastRound <= r.maxIdleRounds {
			active[name] = true
		}
	}
	return active
}

// TickSession 推进会话 round 计数（每次处理新用户消息时调用），同时清理已过期的工具。
// 返回新的 round 编号。
func (r *Registry) TickSession(sessionKey string) int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessionRound[sessionKey]++
	curRound := r.sessionRound[sessionKey]

	// 清理过期工具，防止 map 无限增长
	if toolRounds := r.sessionActivated[sessionKey]; len(toolRounds) > 0 {
		for name, lastRound := range toolRounds {
			if curRound-lastRound > r.maxIdleRounds {
				delete(toolRounds, name)
			}
		}
	}

	return curRound
}

// ActivateTools 激活指定会话的工具，记录当前 round（内置 + MCP 均通过此方法）
func (r *Registry) ActivateTools(sessionKey string, toolNames []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.sessionActivated[sessionKey]
	if m == nil {
		m = make(map[string]int64, len(toolNames))
		r.sessionActivated[sessionKey] = m
	}
	curRound := r.sessionRound[sessionKey]
	for _, name := range toolNames {
		m[name] = curRound
	}
}

// TouchTool 刷新工具的最后使用 round（在工具实际执行时调用）
func (r *Registry) TouchTool(sessionKey, toolName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.coreTools[toolName] {
		return
	}
	if m := r.sessionActivated[sessionKey]; m != nil {
		if _, exists := m[toolName]; exists {
			m[toolName] = r.sessionRound[sessionKey]
		}
	}
}

// IsToolActive 检查工具是否对指定会话可用（核心工具始终返回 true，已过期的返回 false）
func (r *Registry) IsToolActive(sessionKey, toolName string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.coreTools[toolName] {
		return true
	}
	lastRound, ok := r.sessionActivated[sessionKey][toolName]
	if !ok {
		return false
	}
	return r.sessionRound[sessionKey]-lastRound <= r.maxIdleRounds
}

// DeactivateSession 清理指定会话的全部激活状态和 round 计数
func (r *Registry) DeactivateSession(sessionKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessionActivated, sessionKey)
	delete(r.sessionRound, sessionKey)
}

// Clone 复制工具注册表
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := NewRegistry()
	for name, tool := range r.globalTools {
		clone.globalTools[name] = tool
	}
	for name := range r.coreTools {
		clone.coreTools[name] = true
	}
	return clone
}

// mcpSchemaProvider 内部接口，MCPRemoteTool 和 SessionMCPRemoteTool 都实现此接口
// 用于 load_tools 获取完整参数信息
type mcpSchemaProvider interface {
	fullDescription() string
	fullParams() []llm.ToolParam
	mcpServerName() string
}

// ToolGroupProvider 工具组提供者接口，用于将工具分组显示
// 实现此接口的工具将显示在独立的工具组中，而非 Built-in 分组
type ToolGroupProvider interface {
	GroupName() string         // 工具组名称（如 "Feishu"）
	GroupInstructions() string // 工具组使用说明
}

// ChannelProvider 渠道提供者接口，用于限制工具仅在特定渠道可用
// 未实现此接口的工具在所有渠道可用
type ChannelProvider interface {
	SupportedChannels() []string // 返回支持的渠道列表，空则表示所有渠道
}

// IsChannelSupported 检查工具是否支持指定渠道
// 如果工具未实现 ChannelProvider 接口，则默认支持所有渠道
func IsChannelSupported(tool Tool, channel string) bool {
	if cp, ok := tool.(ChannelProvider); ok {
		channels := cp.SupportedChannels()
		if len(channels) == 0 {
			return true // 空列表 = 所有渠道
		}
		for _, c := range channels {
			if c == channel {
				return true
			}
		}
		return false
	}
	return true // 未实现接口 = 所有渠道可用
}

// ToolGroupEntry 工具组条目
type ToolGroupEntry struct {
	Name         string   // 工具组名称
	Instructions string   // 工具组使用说明
	ToolNames    []string // 工具名称列表
}

// ToolSchema 工具完整 schema 信息（供 load_tools 使用）
type ToolSchema struct {
	ToolName    string
	ServerName  string // 内置工具为空，MCP 工具为 server 名
	Description string
	Params      []llm.ToolParam
}

// GetBuiltinToolNames 返回所有内置（非 MCP、非工具组）工具的名称列表（按名称排序）
// 内置工具不实现 mcpSchemaProvider 接口，也不实现 ToolGroupProvider 接口
func (r *Registry) GetBuiltinToolNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var names []string
	for name, tool := range r.globalTools {
		if _, isMCP := tool.(mcpSchemaProvider); isMCP {
			continue
		}
		if _, hasGroup := tool.(ToolGroupProvider); hasGroup {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetToolGroups returns all tool groups (sorted by name).
// Each group contains name, instructions, and tool names.
// Equivalent to GetToolGroupsForChannel("") with no channel filtering.
func (r *Registry) GetToolGroups() []ToolGroupEntry {
	return r.GetToolGroupsForChannel("")
}

// GetToolGroupsForChannel 返回指定渠道可用的工具组（按组名排序）
// channel 为空时不进行渠道过滤，返回所有工具组
func (r *Registry) GetToolGroupsForChannel(channel string) []ToolGroupEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	groups := make(map[string]*ToolGroupEntry)
	for _, tool := range r.globalTools {
		// 渠道过滤（空渠道不过滤）
		if channel != "" && !IsChannelSupported(tool, channel) {
			continue
		}
		if groupProvider, ok := tool.(ToolGroupProvider); ok {
			groupName := groupProvider.GroupName()
			if groups[groupName] == nil {
				groups[groupName] = &ToolGroupEntry{
					Name:         groupName,
					Instructions: groupProvider.GroupInstructions(),
					ToolNames:    []string{},
				}
			}
			groups[groupName].ToolNames = append(groups[groupName].ToolNames, tool.Name())
		}
	}

	// 转换为切片并排序
	result := make([]ToolGroupEntry, 0, len(groups))
	for _, entry := range groups {
		sort.Strings(entry.ToolNames)
		result = append(result, *entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// SetGlobalMCPCatalog 设置全局 MCP Server 目录（由 MCPManager.RegisterTools 调用）
func (r *Registry) SetGlobalMCPCatalog(catalog []MCPServerCatalogEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// 防御性复制，避免调用方修改切片导致竞争条件
	r.globalMCPCatalog = append([]MCPServerCatalogEntry{}, catalog...)
}

// GetMCPCatalog 获取完整 MCP Server 目录（全局 + 会话特定）
func (r *Registry) GetMCPCatalog(sessionKey string) []MCPServerCatalogEntry {
	r.mu.RLock()
	global := append([]MCPServerCatalogEntry{}, r.globalMCPCatalog...)
	r.mu.RUnlock()

	if r.sessionMCPMgr != nil {
		sessionMCP := r.sessionMCPMgr.GetSessionMCPManager(sessionKey)
		if sessionMCP != nil {
			sessionCatalog := sessionMCP.GetCatalog()
			global = append(global, sessionCatalog...)
		}
	}
	return global
}

// GetToolSchemas 获取指定工具的完整 schema 信息（参数定义、描述等）
// 支持内置工具和 MCP 工具。toolNames 为工具全名列表；传入 nil 返回所有可加载工具的 schema。
// 如果 channel 不为空，则过滤掉不支持该渠道的工具。
func (r *Registry) GetToolSchemas(sessionKey string, toolNames []string) []ToolSchema {
	return r.GetToolSchemasForChannel(sessionKey, toolNames, "")
}

// GetToolSchemasForChannel 获取指定渠道可用的工具 schema 信息
// channel 为空时不进行渠道过滤
func (r *Registry) GetToolSchemasForChannel(sessionKey string, toolNames []string, channel string) []ToolSchema {
	nameSet := make(map[string]bool, len(toolNames))
	matchAll := len(toolNames) == 0
	for _, n := range toolNames {
		nameSet[n] = true
	}

	var schemas []ToolSchema

	r.mu.RLock()
	for name, tool := range r.globalTools {
		if !matchAll && !nameSet[name] {
			continue
		}
		// 渠道过滤
		if channel != "" && !IsChannelSupported(tool, channel) {
			continue
		}
		if p, ok := tool.(mcpSchemaProvider); ok {
			schemas = append(schemas, ToolSchema{
				ToolName:    name,
				ServerName:  p.mcpServerName(),
				Description: p.fullDescription(),
				Params:      p.fullParams(),
			})
		} else if !r.coreTools[name] {
			schemas = append(schemas, ToolSchema{
				ToolName:    name,
				Description: tool.Description(),
				Params:      tool.Parameters(),
			})
		}
	}
	r.mu.RUnlock()

	// 扫描会话 MCP 工具
	if r.sessionMCPMgr != nil {
		if sm := r.sessionMCPMgr.GetSessionMCPManager(sessionKey); sm != nil {
			for _, tool := range sm.GetSessionTools() {
				if !matchAll && !nameSet[tool.Name()] {
					continue
				}
				// 会话 MCP 工具暂不做渠道过滤（MCP 工具通常是通用的）
				if p, ok := tool.(mcpSchemaProvider); ok {
					schemas = append(schemas, ToolSchema{
						ToolName:    tool.Name(),
						ServerName:  p.mcpServerName(),
						Description: p.fullDescription(),
						Params:      p.fullParams(),
					})
				}
			}
		}
	}

	return schemas
}

// DefaultRegistry 创建包含默认工具的注册表
// 核心工具（RegisterCore）始终在 tool definitions 中；其余需通过 load_tools 激活。
// 注意：CronTool 需要依赖注入，不在默认注册表中，需单独注册
func DefaultRegistry() *Registry {
	r := NewRegistry()
	// 核心工具：基础文件/系统操作 + 工具加载器，始终可用
	r.RegisterCore(&ShellTool{})
	r.RegisterCore(&GlobTool{})
	r.RegisterCore(&GrepTool{})
	r.RegisterCore(&ReadTool{})
	r.RegisterCore(&EditTool{})
	r.RegisterCore(&LoadToolsTool{})
	r.RegisterCore(&SubAgentTool{})
	r.RegisterCore(&SkillTool{})
	r.RegisterCore(&SearchToolsTool{})
	// CronTool 需要依赖注入，需在 agent 初始化后单独注册
	r.RegisterCore(&DownloadFileTool{})
	// WebSearch: always available (requires TAVILY_API_KEY)
	r.RegisterCore(NewWebSearchTool())
	r.RegisterCore(NewFetchTool())
	return r
}
