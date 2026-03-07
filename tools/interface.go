package tools

import (
	"context"
	"sort"
	"sync"
	"xbot/llm"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
)

// SessionMCPManagerProvider 会话 MCP 管理器提供者接口
type SessionMCPManagerProvider interface {
	GetSessionMCPManager(sessionKey string) *SessionMCPManager
}

// ToolContext 工具执行上下文
type ToolContext struct {
	Ctx                     context.Context                             // 可取消的上下文，用于响应 stop 信号
	WorkingDir              string                                      // Agent 的工作目录
	AgentID                 string                                      // 当前 Agent 的 ID
	Manager                 SubAgentManager                             // Agent 管理器引用（用于创建 SubAgent）
	DataDir                 string                                      // 数据持久化目录
	Channel                 string                                      // 当前消息来源渠道
	ChatID                  string                                      // 当前消息来源会话
	SenderID                string                                      // 当前消息发送者 ID
	SenderName              string                                      // 当前消息发送者姓名
	SendFunc                func(channel, chatID, content string) error // 向 IM 渠道发送消息（不经过 Agent），返回错误
	InjectInbound           func(channel, chatID, content string)       // 注入入站消息，触发 Agent 完整处理循环
	Registry                *Registry                                   // 工具注册表引用（用于动态注册工具）
	InvalidateAllSessionMCP func()                                      // 使所有会话的 MCP 连接失效

	// Letta memory fields (nil when memory provider is not letta)
	TenantID        int64                        // 当前租户 ID
	CoreMemory      *sqlite.CoreMemoryService    // 核心记忆存储
	ArchivalMemory  *vectordb.ArchivalService    // 归档记忆存储（chromem-go 向量数据库）
	MemorySvc       *sqlite.MemoryService        // 事件历史存储（用于 rethink 日志）
	RecallTimeRange vectordb.RecallTimeRangeFunc // 时间范围会话历史搜索
}

// SubAgentManager SubAgent 管理接口，避免循环依赖
type SubAgentManager interface {
	// RunSubAgent 创建并运行一个 SubAgent，返回最终响应文本
	// allowedTools 为工具白名单，为空时使用所有工具（除 SubAgent）
	RunSubAgent(ctx context.Context, parentAgentID string, task string, systemPrompt string, allowedTools []string) (string, error)
}

// ToolResult 工具执行结果
type ToolResult struct {
	Summary     string // 精简结果，进入 LLM 上下文
	Detail      string // 详细内容（如 diff），仅推前端展示，不进入上下文
	Tips        string // 操作指引，帮助 LLM 理解下一步操作
	WaitingUser bool   // 标记：是否等待用户响应（如果为 true，Agent 不应生成额外回复）
}

// NewResult 创建 Summary == Detail 的简单结果
func NewResult(content string) *ToolResult {
	return &ToolResult{Summary: content}
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

// Registry 工具注册表
type Registry struct {
	mu            sync.RWMutex
	globalTools   map[string]Tool           // 非MCP工具（全局共享）
	sessionMCPMgr SessionMCPManagerProvider // 会话MCP管理器提供者
}

// NewRegistry 创建工具注册表
func NewRegistry() *Registry {
	return &Registry{
		globalTools: make(map[string]Tool),
	}
}

// Register 注册工具
func (r *Registry) Register(tool Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.globalTools[tool.Name()] = tool
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

// AsDefinitions 转换为 LLM 工具定义列表（按名称排序，保证顺序稳定以优化 KV-cache）
func (r *Registry) AsDefinitions() []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]llm.ToolDefinition, 0, len(r.globalTools))
	for _, tool := range r.globalTools {
		defs = append(defs, tool)
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

// AsDefinitionsForSession 获取特定会话的工具定义（包含全局工具 + 会话 MCP 工具）
func (r *Registry) AsDefinitionsForSession(sessionKey string) []llm.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 收集全局工具
	defs := make([]llm.ToolDefinition, 0, len(r.globalTools))
	for _, tool := range r.globalTools {
		defs = append(defs, tool)
	}

	// 如果有会话 MCP 管理器提供者，获取会话特定的 MCP 工具
	if r.sessionMCPMgr != nil {
		sessionMCP := r.sessionMCPMgr.GetSessionMCPManager(sessionKey)
		if sessionMCP != nil {
			sessionTools := sessionMCP.GetSessionTools()
			for _, tool := range sessionTools {
				defs = append(defs, tool)
			}
		}
	}

	// 按名称排序，保证顺序稳定以优化 KV-cache
	sort.Slice(defs, func(i, j int) bool {
		return defs[i].Name() < defs[j].Name()
	})

	return defs
}

// Clone 复制工具注册表
func (r *Registry) Clone() *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := NewRegistry()
	for name, tool := range r.globalTools {
		clone.globalTools[name] = tool
	}
	return clone
}

// DefaultRegistry 创建包含默认工具的注册表
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(&ShellTool{})
	r.Register(&GlobTool{})
	r.Register(&GrepTool{})
	r.Register(&ReadTool{})
	r.Register(&EditTool{})
	r.Register(NewWebSearchTool())
	r.Register(&SubAgentTool{})
	r.Register(NewCronTool())
	// r.Register(&NotifyTool{})
	r.Register(&DownloadFileTool{})
	return r
}
