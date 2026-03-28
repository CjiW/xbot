package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/cron"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// ErrLLMGenerate 表示 LLM 生成调用失败（网络、API 4xx/5xx 等）
var ErrLLMGenerate = errors.New("LLM generate failed")

// consolidateRequest 记忆整理请求
// 传递 key 而非 session 引用，避免排队期间 session 被修改或失效
type consolidateRequest struct {
	channel        string
	chatID         string
	senderID       string
	unconsolidated int // 触发时的未整理消息数，用于 worker 中验证
}

// assertNoSystemPersist 断言不得将 system 消息持久化到 session，否则会导致多条 system / 400 / 多人 sysprompt 混用。
func assertNoSystemPersist(m llm.ChatMessage) {
	if m.Role == "system" {
		log.WithField("message", m).Error("ASSERT: must not persist system message to session")
		panic("assert: must not persist system message to session")
	}
}

// formatErrorForUser 将错误格式化为对用户可见的提示
func formatErrorForUser(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrLLMGenerate) {
		return fmt.Sprintf("LLM 服务调用失败，请稍后重试或检查配置。\n错误详情: %v", err)
	}
	return fmt.Sprintf("处理消息时发生错误: %v", err)
}

// resolveDataPath 解析数据文件路径，优先使用 .xbot/ 目录，向后兼容工作目录根路径
// 读取时：优先新路径，不存在则回退旧路径
// 写入时：始终使用新路径
func resolveDataPath(workDir, filename string) string {
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	xbotDir := filepath.Join(workDir, ".xbot")
	newPath := filepath.Join(xbotDir, filename)
	oldPath := filepath.Join(workDir, filename)

	// 优先使用新路径
	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}
	// 新路径不存在，检查旧路径
	if _, err := os.Stat(oldPath); err == nil {
		return oldPath
	}
	// 都不存在，返回新路径（用于创建新文件）
	return newPath
}

func resolveGlobalSkillsDirs(legacySkillsDir string) []string {
	if legacySkillsDir == "" {
		return nil
	}
	abs, err := filepath.Abs(legacySkillsDir)
	if err != nil {
		return nil
	}
	return []string{abs}
}

// metaTools are tools that manage/search other tools — not useful to index.
var metaTools = map[string]bool{
	"search_tools": true,
	"load_tools":   true,
	"manage_tools": true,
}

// IndexGlobalTools indexes all global tools for semantic search:
// built-in registry tools, tool groups, and global MCP servers.
// Call after all tools are registered. Uses full-replace semantics
// so stale entries from removed tools are automatically cleaned up.
func (a *Agent) IndexGlobalTools() {
	registry := a.tools
	multiSession := a.multiSession
	globalMCPConfigPath := resolveDataPath(a.workDir, "mcp.json")

	ctx := context.Background()
	var toolEntries []memory.ToolIndexEntry
	indexed := make(map[string]bool) // track indexed tool names to avoid duplicates

	// 1. Index built-in tool groups (like Feishu tools)
	toolGroups := registry.GetToolGroups()
	for _, group := range toolGroups {
		for _, toolName := range group.ToolNames {
			tool, ok := registry.Get(toolName)
			desc := fmt.Sprintf("Built-in tool group: %s", group.Name)
			var channels []string
			if ok {
				if toolDesc := tool.Description(); toolDesc != "" {
					desc = fmt.Sprintf("Tool: %s. %s", toolName, toolDesc)
				}
				if cp, ok := tool.(tools.ChannelProvider); ok {
					channels = cp.SupportedChannels()
				}
			}
			if group.Instructions != "" {
				desc = fmt.Sprintf("%s. %s", desc, group.Instructions)
			}
			toolEntries = append(toolEntries, memory.ToolIndexEntry{
				Name:        toolName,
				ServerName:  group.Name,
				Source:      "global",
				Description: desc,
				Channels:    channels,
			})
			indexed[toolName] = true
		}
	}

	// 2. Index all registry tools not already covered by tool groups
	for _, tool := range registry.List() {
		name := tool.Name()
		if indexed[name] || metaTools[name] {
			continue
		}
		var channels []string
		if cp, ok := tool.(tools.ChannelProvider); ok {
			channels = cp.SupportedChannels()
		}
		toolEntries = append(toolEntries, memory.ToolIndexEntry{
			Name:        name,
			ServerName:  "builtin",
			Source:      "global",
			Description: tool.Description(),
			Channels:    channels,
		})
		indexed[name] = true
	}

	// 3. Index global MCP servers
	dummySessionKey := "indexing:dummy"
	mcpMgr := tools.NewSessionMCPManager(
		dummySessionKey,
		"system0",
		globalMCPConfigPath,
		"", "", 30*time.Minute,
	)
	if mcpMgr != nil {
		catalog := mcpMgr.GetCatalog()
		for _, entry := range catalog {
			for _, toolName := range entry.ToolNames {
				fullName := fmt.Sprintf("mcp_%s_%s", entry.Name, toolName)
				desc := fmt.Sprintf("MCP server: %s. Tool: %s", entry.Name, toolName)
				if entry.Instructions != "" {
					desc = fmt.Sprintf("%s. %s", desc, entry.Instructions)
				}
				toolEntries = append(toolEntries, memory.ToolIndexEntry{
					Name:        fullName,
					ServerName:  entry.Name,
					Source:      "global",
					Description: desc,
				})
			}
		}
		mcpMgr.Close()
	}

	if len(toolEntries) == 0 {
		log.Info("No tools to index")
		return
	}

	if err := multiSession.IndexToolsForTenant(ctx, 0, toolEntries); err != nil {
		log.WithError(err).Warn("Failed to index global tools")
		return
	}

	log.WithField("count", len(toolEntries)).Infof("Indexed %d global tools (registry + tool groups + MCP)", len(toolEntries))
}

// Agent 核心 Agent 引擎
type Agent struct {
	bus              *bus.MessageBus
	multiSession     *session.MultiTenantSession // Multi-tenant session manager
	tools            *tools.Registry
	maxIterations    int
	memoryWindow     int
	purgeOldMessages bool

	skills             *SkillStore
	agents             *AgentStore
	chatHistory        *tools.ChatHistoryStore // 聊天历史缓存
	cardBuilder        *tools.CardBuilder      // Card Builder MCP
	workDir            string
	promptLoader       *PromptLoader
	pipeline           *MessagePipeline // 消息构建管道（持有实例，支持运行时动态增删中间件）
	cronPipeline       *MessagePipeline // Cron 专用消息构建管道
	sandboxMode        string           // "none" or "docker"
	sandbox            tools.Sandbox    // Sandbox 实例引用（V4 新增）
	sandboxIdleTimeout time.Duration    // 沙箱空闲超时（0 禁用）
	singleUser         bool             // 单用户模式
	maxConcurrency     int              // 最大并发会话处理数
	globalSkillDirs    []string         // 全局 skill 目录（宿主机路径）
	agentsDir          string

	// 上下文管理配置
	contextManagerConfig *ContextManagerConfig
	contextManagerMu     sync.RWMutex // 保护 contextManager 的并发读写
	contextManager       ContextManager

	// SubAgent 深度控制
	maxSubAgentDepth int

	// Cron service and scheduler
	cronSvc *sqlite.CronService
	cronSch *cron.Scheduler

	// User LLM config service and factory
	llmConfigSvc *sqlite.UserLLMConfigService
	llmFactory   *LLMFactory

	// 用户级别的信号量：设置了自己的 LLM 配置的用户使用独立信号量
	// key: senderID, value: 用户独立的信号量（容量为1）
	userSemaphores sync.Map // map[string]chan struct{}

	// 记忆整理 channel 机制
	consolidateCh     chan consolidateRequest // 记忆整理请求 channel
	consolidateStopCh chan struct{}           // 停止信号
	consolidateWg     sync.WaitGroup          // 等待 worker 退出
	consolidatingMu   sync.Mutex
	consolidating     map[string]bool // key: "channel:chat_id", value: 是否正在进行记忆合并

	commands         *CommandRegistry                          // 指令注册表
	directSend       func(bus.OutboundMessage) (string, error) // 同步发送，绕过 bus 以获取 message_id
	sessionMsgIDs    sync.Map                                  // key: "channel:chatID" -> 当前 session 已发消息 ID（用于 Patch 更新）
	sessionReplyTo   sync.Map                                  // key: "channel:chatID" -> 用户入站消息 ID（用于首条回复的 reply 模式）
	sessionFinalSent sync.Map                                  // key: "channel:chatID" -> bool, 工具已发送最终回复（如卡片），后续 sendMessage 跳过

	// per-request cancel: 用于 /cancel 取消当前正在处理的请求
	// key: "channel:chatID:senderID" -> chan struct{} (buffered, cap=1)
	chatCancelCh sync.Map

	// interactiveSubAgents stores interactive SubAgent sessions
	// key: "channel:chatID/roleName" -> *interactiveAgent
	// sync.Map provides atomic Load/Store/Delete/LoadOrStore, no additional mutex needed
	interactiveSubAgents sync.Map

	// hookChain is the shared tool execution hook chain for this Agent and all SubAgents.
	hookChain *tools.HookChain

	// Phase 2: 智能触发状态（按 sessionKey 索引）
	triggerProviders sync.Map // map[string]*TriggerInfoProvider

	// OffloadStore manages large tool result offload to disk (Phase 2: Layer 1)
	offloadStore *OffloadStore

	// maskStore manages observation masking storage
	maskStore *ObservationMaskStore

	// contextEditor 管理上下文编辑（Context Editing 工具）
	contextEditor *ContextEditor

	// recallTracker 摘要精化追踪器（主 Agent 全局共享）
	recallTracker *RecallTracker

	// todoManager 管理当前会话的 TODO 列表
	todoManager *tools.TodoManager

	// TopicDetector for topic partition isolation (Phase 2.5, disabled by default)
	topicDetector        *TopicDetector
	enableTopicIsolation bool

	// channelPromptProviders channel 特化 prompt 提供者列表（由外部注入）
	channelPromptProviders []ChannelPromptProvider

	// RegistryManager for skill/agent sharing and marketplace
	registryManager *RegistryManager

	// SettingsService for per-user settings
	settingsSvc *SettingsService

	// channelFinder looks up a channel instance by name (injected from main.go).
	channelFinder func(name string) (channel.Channel, bool)
}

// SetRegistryManager sets the RegistryManager (for external injection or override).
func (a *Agent) SetRegistryManager(rm *RegistryManager) { a.registryManager = rm }

// SetSettingsService sets the SettingsService (for external injection or override).
func (a *Agent) SetSettingsService(svc *SettingsService) { a.settingsSvc = svc }

// LLMFactory returns the Agent's LLMFactory (for external injection of callbacks).
func (a *Agent) LLMFactory() *LLMFactory { return a.llmFactory }

// RegistryManager returns the Agent's RegistryManager (for external injection of callbacks).
func (a *Agent) RegistryManager() *RegistryManager { return a.registryManager }

// SettingsService returns the Agent's SettingsService (for external injection of callbacks).
func (a *Agent) SettingsService() *SettingsService { return a.settingsSvc }

// SetUserModel sets the model for a user's LLM configuration (used by settings card callback).
func (a *Agent) SetUserModel(senderID, model string) error {
	cfg, err := a.llmConfigSvc.GetConfig(senderID)
	if err != nil {
		return fmt.Errorf("get LLM config: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("user has no custom LLM config; use /set-llm first")
	}
	cfg.Model = model
	if err := a.llmConfigSvc.SetConfig(cfg); err != nil {
		return fmt.Errorf("save model: %w", err)
	}
	a.llmFactory.Invalidate(senderID)
	return nil
}

// SetChannelFinder sets the channel finder callback (for external injection).
// Also propagates to SettingsService so it can resolve channels by name.
func (a *Agent) SetChannelFinder(fn func(name string) (channel.Channel, bool)) {
	a.channelFinder = fn
	if a.settingsSvc != nil {
		a.settingsSvc.SetChannelFinder(fn)
	}
}

func buildToolMessageContent(result *tools.ToolResult) string {
	if result == nil {
		return ""
	}
	// 将 Summary + Detail + Tips 组合为纯文本，避免 JSON 序列化转义换行符。
	// 旧方案用 json.Marshal(result) 导致 Detail 中的 diff 换行被编码为 \n，
	// LLM 看到的是不可读的文本块而非格式化的 diff。
	var sb strings.Builder
	if result.Summary != "" {
		sb.WriteString(result.Summary)
	}
	if result.Detail != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(result.Detail)
	}
	if result.Tips != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(result.Tips)
	}
	return sb.String()
}

// Config Agent 配置
type Config struct {
	Bus            *bus.MessageBus
	LLM            llm.LLM
	Model          string
	MaxIterations  int           // 单次对话最大工具调用迭代次数
	MaxConcurrency int           // 最大并发会话处理数（默认 3）
	MemoryWindow   int           // 上下文窗口大小（保留的历史消息数）
	DBPath         string        // SQLite 数据库路径（空则使用默认路径）
	SkillsDir      string        // Skills 目录
	WorkDir        string        // 工作目录（所有文件相对此目录）
	PromptFile     string        // 系统提示词模板文件路径（空则使用内置默认值）
	SingleUser     bool          // 单用户模式：所有消息的 SenderID 归一化为 "default"
	SandboxMode    string        // 沙箱模式: "none" 或 "docker"（默认 "docker"）
	Sandbox        tools.Sandbox // Sandbox 实例引用（V4 新增）

	SandboxIdleTimeout time.Duration // 沙箱空闲超时（0 禁用）

	MemoryProvider     string // 记忆提供者: "flat" 或 "letta"
	EmbeddingProvider  string // 嵌入提供者: "openai"(默认) 或 "ollama"
	EmbeddingBaseURL   string // 嵌入向量服务地址
	EmbeddingAPIKey    string // 嵌入向量服务密钥
	EmbeddingModel     string // 嵌入模型名称
	EmbeddingMaxTokens int    // 嵌入模型最大 token 数

	// MCP 会话管理配置
	MCPInactivityTimeout time.Duration // MCP 不活跃超时时间
	MCPCleanupInterval   time.Duration // MCP 清理扫描间隔
	SessionCacheTimeout  time.Duration // 会话缓存超时

	// 上下文管理模式
	// 优先级：ContextMode > EnableAutoCompress 旧字段
	// 默认 ""，由 resolveContextMode 决定
	ContextMode ContextMode

	// 旧压缩配置（保留用于初始化 ContextManagerConfig，向后兼容 main.go 传参）
	MaxContextTokens     int     // 最大上下文 token 数（默认 100000）
	CompressionThreshold float64 // 触发压缩的 token 比例阈值（默认 0.7）
	EnableAutoCompress   bool    // 是否启用自动上下文压缩（默认 true，旧字段）

	// SubAgent 深度控制
	MaxSubAgentDepth int // SubAgent 最大嵌套深度（默认 6）

	// 话题分区隔离（Phase 2.5，默认关闭）
	EnableTopicIsolation     bool    `json:"enable_topic_isolation"`     // 是否启用话题分区隔离（默认 false）
	TopicMinSegmentSize      int     `json:"topic_min_segment_size"`     // 最小话题片段大小（默认 3）
	TopicSimilarityThreshold float64 `json:"topic_similarity_threshold"` // 话题相似度阈值（默认 0.3）

	// 压缩后清理旧消息
	PurgeOldMessages bool // 压缩后自动删除超出 MemoryWindow 的旧消息（默认 false）

}

// initStores 初始化各类存储和注册表，返回 skillStore, agentStore, chatHistory, registry, cardBuilder。

func initStores(cfg Config) (*SkillStore, *AgentStore, *tools.ChatHistoryStore, *tools.Registry, *tools.CardBuilder) {
	globalSkillDirs := resolveGlobalSkillsDirs(cfg.SkillsDir)

	skillStore := NewSkillStore(cfg.WorkDir, globalSkillDirs, cfg.Sandbox)

	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	agentsDir := filepath.Join(cfg.WorkDir, ".xbot", "agents")
	if err := tools.InitAgentRoles(agentsDir); err != nil {
		log.WithError(err).Warn("Failed to load agent roles, SubAgent will have no predefined roles")
	}
	agentStore := NewAgentStore(cfg.WorkDir, agentsDir, cfg.Sandbox)

	registry := tools.DefaultRegistry()

	// 创建聊天历史存储
	chatHistory := tools.NewChatHistoryStore(200) // 每个群组保留最近 200 条
	registry.Register(tools.NewChatHistoryTool(chatHistory))

	// MCP 配置路径：优先使用 .xbot/mcp.json，向后兼容 mcp.json
	mcpConfigPath := resolveDataPath(cfg.WorkDir, "mcp.json")

	// 注册 ManageTools tool（需要 skillStore 和 mcpConfigPath）
	registry.RegisterCore(tools.NewManageTools(cfg.WorkDir, mcpConfigPath))

	cardBuilder := tools.NewCardBuilder()
	for _, t := range tools.NewCardTools(cardBuilder) {
		registry.Register(t)
	}

	// Clean up expired waiting cards from previous runs (TTL: 24h)
	if n := cardBuilder.CleanupExpiredWaitingCards(24 * time.Hour); n > 0 {
		log.WithField("count", n).Info("Cleaned up expired waiting cards")
	}

	return skillStore, agentStore, chatHistory, registry, cardBuilder
}

// initSession 初始化多租户会话管理器。
func initSession(cfg Config) (*session.MultiTenantSession, error) {
	memoryProvider := cfg.MemoryProvider
	if memoryProvider == "" {
		memoryProvider = "flat"
	}
	multiSession, err := session.NewMultiTenant(
		cfg.DBPath,
		session.WithMCPTimeout(cfg.MCPInactivityTimeout),
		session.WithCleanupInterval(cfg.MCPCleanupInterval),
		session.WithSessionCacheTimeout(cfg.SessionCacheTimeout),
		session.WithMemoryProvider(memoryProvider),
		session.WithEmbeddingConfig(session.EmbeddingConfig{
			Provider:   cfg.EmbeddingProvider,
			BaseURL:    cfg.EmbeddingBaseURL,
			APIKey:     cfg.EmbeddingAPIKey,
			Model:      cfg.EmbeddingModel,
			MaxTokens:  cfg.EmbeddingMaxTokens,
			LLMClient:  cfg.LLM,
			LLMModel:   cfg.Model,
			TokenModel: cfg.Model,
		}),
	)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize multi-tenant session")
	}
	return multiSession, nil
}

// initServices 注册工具、初始化 cron/LLM/offload/registry/settings 等服务。
// 此方法直接修改 Agent 指针。
func initServices(a *Agent, cfg Config, multiSession *session.MultiTenantSession, registry *tools.Registry) {
	mcpConfigPath := resolveDataPath(cfg.WorkDir, "mcp.json")
	contextMode := resolveContextMode(cfg)

	multiSession.SetMCPConfigPath(mcpConfigPath)

	// 设置会话被清理时的回调，同步清理 Registry 中的 sessionActivated/sessionRound（C-09）
	registryRef := registry // capture for closure
	multiSession.SetOnSessionEvict(func(sessionKey string) { registryRef.DeactivateSession(sessionKey) })

	// 设置会话 MCP 管理器提供者
	registry.SetSessionMCPManagerProvider(multiSession)

	// 全局工具索引通过 IndexGlobalTools() 在所有工具注册完成后调用

	// 如果使用 Letta 记忆模式，注册记忆工具（核心工具，始终可用）
	memoryProvider := cfg.MemoryProvider
	if memoryProvider == "" {
		memoryProvider = "flat"
	}
	if memoryProvider == "letta" {
		for _, tool := range tools.LettaMemoryTools() {
			registry.RegisterCore(tool)
		}
		log.Info("Letta memory tools registered (core)")
	}

	// 初始化指令注册表
	a.commands = NewCommandRegistry()
	registerBuiltinCommands(a.commands)

	// 初始化消息构建管道
	a.initPipelines()

	// 初始化 Cron 服务和调度器
	cronSvc := sqlite.NewCronService(multiSession.DB())
	cronSch := cron.NewScheduler(cronSvc)

	// 从旧的 JSON 文件迁移数据（如果需要）
	if err := cronSvc.MigrateFromJSON(cfg.WorkDir); err != nil {
		log.WithError(err).Warn("Failed to migrate cron jobs from JSON")
	}

	// 注册 CronTool（核心工具，始终可用）
	registry.RegisterCore(tools.NewCronTool(cronSvc))

	a.cronSvc = cronSvc
	a.cronSch = cronSch

	// Initialize UserLLMConfigService
	a.llmConfigSvc = sqlite.NewUserLLMConfigService(multiSession.DB())
	a.llmFactory = NewLLMFactory(a.llmConfigSvc, cfg.LLM, cfg.Model)

	// 初始化上下文管理器
	a.contextManagerConfig = &ContextManagerConfig{
		MaxContextTokens:     cfg.MaxContextTokens,
		CompressionThreshold: cfg.CompressionThreshold,
		DefaultMode:          contextMode,
	}
	a.contextManager = NewContextManager(a.contextManagerConfig)

	// 初始化 OffloadStore（Phase 2: Layer 1 Offload）
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	offloadDir := filepath.Join(cfg.WorkDir, ".xbot", "offload_store")
	a.offloadStore = NewOffloadStore(OffloadConfig{
		StoreDir:        offloadDir,
		MaxResultTokens: 2000,
		MaxResultBytes:  10240,
		CleanupAgeDays:  7,
	})
	go a.offloadStore.CleanStale()

	// Inject sandbox into OffloadStore for remote mode file hash computation
	if a.sandbox != nil {
		a.offloadStore.SetSandbox(a.sandbox)
	}

	// 初始化 ObservationMaskStore（Phase 3: Observation Masking）
	maskStore := NewObservationMaskStore(200)
	a.maskStore = maskStore

	// 注册 offload_recall 工具（需要 OffloadStore 依赖注入）
	if a.offloadStore != nil {
		recallTool := &tools.OffloadRecallTool{Store: a.offloadStore}
		registry.RegisterCore(recallTool)
	}

	// 注册 recall_masked 工具（需要 MaskStore 依赖注入）
	if maskStore != nil {
		registry.RegisterCore(&tools.RecallMaskedTool{Store: maskStore})
	}

	// 初始化 ContextEditor（Context Editing 工具 — 精确编辑上下文）
	editStore := NewContextEditStore(100)
	contextEditor := NewContextEditor(editStore)
	a.contextEditor = contextEditor
	registry.RegisterCore(&tools.ContextEditTool{Handler: contextEditor})

	// 初始化 RecallTracker（摘要精化追踪器）
	a.recallTracker = NewRecallTracker()

	// 初始化并注册 TODO 管理工具
	todoMgr := tools.NewTodoManager()
	a.todoManager = todoMgr
	registry.RegisterCore(&tools.TodoWriteTool{Manager: todoMgr})
	registry.RegisterCore(&tools.TodoListTool{Manager: todoMgr})

	// Initialize SharedSkillRegistry
	sharedRegistry := sqlite.NewSharedSkillRegistry(multiSession.DB())

	// Initialize RegistryManager
	a.registryManager = NewRegistryManager(a.skills, a.agents, sharedRegistry, cfg.WorkDir, cfg.Sandbox)

	// Initialize UserSettingsService and SettingsService
	userSettingsSvc := sqlite.NewUserSettingsService(multiSession.DB())
	a.settingsSvc = NewSettingsService(userSettingsSvc)

	// Initialize LLMSemaphoreManager and inject dependencies
	llmSemMgr := llm.NewLLMSemaphoreManager()
	a.llmFactory.SetLLMSemaphoreManager(llmSemMgr)
	a.llmFactory.SetSettingsService(a.settingsSvc)
}

// New 创建 Agent
func New(cfg Config) *Agent {
	// 1. 设置配置默认值
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 100
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 3
	}
	if cfg.MemoryWindow == 0 {
		cfg.MemoryWindow = 50
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "."
	}
	if cfg.SkillsDir == "" {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		cfg.SkillsDir = filepath.Join(cfg.WorkDir, ".xbot", "skills")
	}
	if cfg.DBPath == "" {
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		cfg.DBPath = filepath.Join(cfg.WorkDir, ".xbot", "xbot.db")
	}
	if cfg.MCPInactivityTimeout == 0 {
		cfg.MCPInactivityTimeout = 30 * time.Minute
	}
	if cfg.MCPCleanupInterval == 0 {
		cfg.MCPCleanupInterval = 5 * time.Minute
	}
	if cfg.SessionCacheTimeout == 0 {
		cfg.SessionCacheTimeout = 24 * time.Hour
	}
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = 100000 // 默认 100k token
	}
	if cfg.CompressionThreshold == 0 {
		cfg.CompressionThreshold = 0.7
	}
	if cfg.MaxSubAgentDepth <= 0 {
		cfg.MaxSubAgentDepth = 6
	}

	// 2. 初始化存储和注册表
	skillStore, agentStore, chatHistory, registry, cardBuilder := initStores(cfg)

	// 3. 初始化会话管理器
	multiSession, _ := initSession(cfg)

	// 4. 构建 Agent 实例
	sandboxMode := cfg.SandboxMode
	if sandboxMode == "" {
		sandboxMode = "docker"
	}

	agent := &Agent{
		bus:              cfg.Bus,
		multiSession:     multiSession,
		tools:            registry,
		maxIterations:    cfg.MaxIterations,
		maxConcurrency:   cfg.MaxConcurrency,
		memoryWindow:     cfg.MemoryWindow,
		purgeOldMessages: cfg.PurgeOldMessages,

		skills:             skillStore,
		agents:             agentStore,
		chatHistory:        chatHistory,
		cardBuilder:        cardBuilder,
		workDir:            cfg.WorkDir,
		promptLoader:       NewPromptLoader(cfg.PromptFile),
		sandboxMode:        sandboxMode,
		sandbox:            cfg.Sandbox,
		sandboxIdleTimeout: cfg.SandboxIdleTimeout,
		singleUser:         cfg.SingleUser,
		globalSkillDirs:    resolveGlobalSkillsDirs(cfg.SkillsDir),
		maxSubAgentDepth:   cfg.MaxSubAgentDepth,
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		agentsDir:         filepath.Join(cfg.WorkDir, ".xbot", "agents"),
		consolidateCh:     make(chan consolidateRequest, 64),
		consolidateStopCh: make(chan struct{}),
		consolidating:     make(map[string]bool),

		hookChain: tools.NewHookChain(
			tools.NewLoggingHook(),
			tools.NewTimingHook(),
		),

		enableTopicIsolation: cfg.EnableTopicIsolation,
		topicDetector: func() *TopicDetector {
			d := NewTopicDetector()
			if cfg.TopicMinSegmentSize > 0 {
				d.MinSegmentSize = cfg.TopicMinSegmentSize
			}
			if cfg.TopicSimilarityThreshold > 0 {
				d.CosineThreshold = cfg.TopicSimilarityThreshold
			}
			return &d
		}(),
	}

	// 5. 初始化各类服务（修改 agent 指针）
	initServices(agent, cfg, multiSession, registry)

	return agent
}

// GetContextManager 获取当前上下文管理器（读锁保护）。
// 用于 buildMainRunConfig / buildSubAgentRunConfig / handleCompress 等场景。
func (a *Agent) GetContextManager() ContextManager {
	a.contextManagerMu.RLock()
	defer a.contextManagerMu.RUnlock()
	return a.contextManager
}

// SetContextManager 替换当前上下文管理器（写锁保护）。
// 用于 /context mode 命令运行时切换。
func (a *Agent) SetContextManager(cm ContextManager) {
	a.contextManagerMu.Lock()
	defer a.contextManagerMu.Unlock()
	a.contextManager = cm
}

// GetContextMode returns the current effective context mode.
func (a *Agent) GetContextMode() string {
	return string(a.contextManagerConfig.EffectiveMode())
}

// SetContextMode changes the runtime context mode and rebuilds the context manager.
// Pass "default" to reset to the default mode.
func (a *Agent) SetContextMode(mode string) error {
	cfg := a.contextManagerConfig
	target := ContextMode(mode)

	if target == "default" {
		cfg.ResetRuntimeMode()
		a.SetContextManager(NewContextManager(cfg))
		return nil
	}

	if !IsValidContextMode(target) {
		return fmt.Errorf("invalid mode %q; valid: phase1, none, default", mode)
	}

	cfg.SetRuntimeMode(target)
	a.SetContextManager(NewContextManager(cfg))
	return nil
}

// GetUserLLMConfig returns the user's LLM config summary (no API key), or nil if none.
func (a *Agent) GetUserLLMConfig(senderID string) (provider, baseURL, model string, ok bool) {
	cfg, err := a.llmConfigSvc.GetConfig(senderID)
	if err != nil || cfg == nil || (cfg.BaseURL == "" && cfg.APIKey == "") {
		return "", "", "", false
	}
	return cfg.Provider, cfg.BaseURL, cfg.Model, true
}

// SetUserLLM creates or replaces a user's full LLM config.
func (a *Agent) SetUserLLM(senderID, provider, baseURL, apiKey, model string) error {
	if provider == "" || baseURL == "" || apiKey == "" {
		return fmt.Errorf("provider, base_url, api_key 必填")
	}
	cfg := &sqlite.UserLLMConfig{
		SenderID: senderID,
		Provider: provider,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
	}
	if err := a.llmConfigSvc.SetConfig(cfg); err != nil {
		return err
	}
	a.llmFactory.Invalidate(senderID)
	a.llmFactory.InvalidateCustomLLMCache(senderID)
	return nil
}

// DeleteUserLLM removes a user's LLM config and reverts to global.
func (a *Agent) DeleteUserLLM(senderID string) error {
	if err := a.llmConfigSvc.DeleteConfig(senderID); err != nil {
		return err
	}
	a.llmFactory.Invalidate(senderID)
	a.llmFactory.InvalidateCustomLLMCache(senderID)
	return nil
}

// GetLLMConcurrency 获取用户个人 LLM 并发上限配置。
func (a *Agent) GetLLMConcurrency(senderID string) int {
	return a.llmFactory.GetLLMConcurrency(senderID)
}

// SetLLMConcurrency 设置用户个人 LLM 并发上限配置。
func (a *Agent) SetLLMConcurrency(senderID string, personal int) error {
	return a.llmFactory.SetLLMConcurrency(senderID, personal)
}

// SetDirectSend 注入同步发送函数（绕过 bus，用于消息更新跟踪）
func (a *Agent) SetDirectSend(fn func(bus.OutboundMessage) (string, error)) {
	a.directSend = fn
}

// SetChannelPromptProviders 设置 channel 特化 prompt 提供者。
// 调用后会重建 pipeline，将 ChannelPromptMiddleware 插入到管道中。
func (a *Agent) SetChannelPromptProviders(providers ...ChannelPromptProvider) {
	a.channelPromptProviders = providers
	a.pipeline.Use(NewChannelPromptMiddleware(providers...))
}

// ToolHookChain returns the Agent's shared hook chain for tool execution.
// Callers can use this to add/remove hooks at runtime.
func (a *Agent) ToolHookChain() *tools.HookChain {
	return a.hookChain
}

// getTriggerProvider 获取或创建指定 session 的 TriggerInfoProvider。
func (a *Agent) getTriggerProvider(sessionKey string) *TriggerInfoProvider {
	if v, ok := a.triggerProviders.Load(sessionKey); ok {
		return v.(*TriggerInfoProvider)
	}
	provider := NewTriggerInfoProvider()
	actual, _ := a.triggerProviders.LoadOrStore(sessionKey, provider)
	return actual.(*TriggerInfoProvider)
}

// GetCardBuilder returns the CardBuilder for card callback handling.
func (a *Agent) GetCardBuilder() *tools.CardBuilder {
	return a.cardBuilder
}

// getUserSemaphore 获取用户独立的信号量，用于有自定义 LLM 配置的用户
// 每个用户有独立的信号量（容量为1），确保该用户的请求串行处理
// 使用 LoadOrStore 原子操作避免并发创建多个信号量
func (a *Agent) getUserSemaphore(senderID string) chan struct{} {
	if val, ok := a.userSemaphores.Load(senderID); ok {
		return val.(chan struct{})
	}
	// LoadOrStore 原子操作：如果 key 不存在则存储，返回存储的值
	sem, _ := a.userSemaphores.LoadOrStore(senderID, make(chan struct{}, 1))
	return sem.(chan struct{})
}

// Close 关闭 Agent 及其所有资源
func (a *Agent) Close() error {
	// 先停止 consolidation worker（发送 stop 信号）
	close(a.consolidateStopCh)
	a.consolidateWg.Wait()
	log.Info("Consolidation worker stopped")

	// 先停止 cron 调度器，避免在数据库关闭后仍尝试访问
	if a.cronSch != nil {
		a.cronSch.Stop()
	}
	// 再关闭数据库连接
	if a.multiSession != nil {
		if err := a.multiSession.Close(); err != nil {
			log.WithError(err).Warn("MultiTenantSession close error")
		}
	}
	return nil
}

// NOTE: math/rand is intentionally used here for non-cryptographic random selection
// (picking a casual ack message). Go 1.20+ automatically seeds math/rand on package
// init, so there is no security concern and no explicit seeding is required.
var ackMessages = []string{
	"收到~",
	"好的，让我看看",
	"收到，处理中...",
	"了解，稍等~",
	"好的~",
	"嗯嗯，马上处理",
	"收到，稍等一下~",
	"OK，马上看看",
}

func (a *Agent) sendAck(channel, chatID string) {
	msg := ackMessages[rand.Intn(len(ackMessages))]
	if err := a.sendMessage(channel, chatID, msg); err != nil {
		log.WithError(err).Warn("Failed to send ack")
	}
}

// Run 启动 Agent 循环，持续消费入站消息。
// 消息按 chat (channel:chatID) 分组，同一 chat 内顺序处理，不同 chat 并行处理。
// 全局并发数由 AGENT_MAX_CONCURRENCY 控制（默认 3），避免 LLM 并发过高。
// 用户设置了自己的 LLM 配置后，该用户的请求使用独立的信号量，不再占用全局资源。
func (a *Agent) Run(ctx context.Context) error {
	log.WithFields(log.Fields{
		"max_concurrency": a.maxConcurrency,
		"single_user":     a.singleUser,
	}).Info("Agent loop started")

	a.multiSession.StartCleanupRoutine()

	// 启动记忆整理 worker
	a.consolidateWg.Add(1)
	go a.consolidationWorker(ctx)

	a.cronSch.SetInjectFunc(a.injectInbound)
	a.cronSch.StartDelayed(3 * time.Second)

	defer func() {
		a.cronSch.Stop()
		a.multiSession.StopCleanupRoutine()
	}()

	sem := make(chan struct{}, a.maxConcurrency)

	var mu sync.Mutex
	chatQueues := make(map[string]chan bus.InboundMessage)
	var wg sync.WaitGroup

	// getOrCreateQueue 为每个 chat 创建独立的消息队列和 worker
	// 信号量在每次处理消息时动态选择（支持用户中途设置/取消自定义 LLM）
	getOrCreateQueue := func(key string) chan bus.InboundMessage {
		mu.Lock()
		defer mu.Unlock()
		if q, ok := chatQueues[key]; ok {
			return q
		}
		q := make(chan bus.InboundMessage, 32)
		chatQueues[key] = q

		// 始终传入全局信号量，实际信号量在 chatWorker 内部动态选择
		wg.Go(func() {
			a.chatWorker(ctx, key, q, sem)
			mu.Lock()
			delete(chatQueues, key)
			mu.Unlock()
		})
		return q
	}

	for {
		select {
		case <-ctx.Done():
			log.Info("Agent loop stopping, draining chat workers...")
			mu.Lock()
			for _, q := range chatQueues {
				close(q)
			}
			mu.Unlock()
			wg.Wait()
			log.Info("Agent loop stopped")
			return ctx.Err()
		case msg := <-a.bus.Inbound:
			// 单用户模式：在 bus 入口统一归一化 SenderID
			msg.SenderID = a.NormalizeSenderID(msg.SenderID)

			// /cancel 拦截：不进入 chatWorker 队列，直接发 cancel 信号
			if strings.TrimSpace(strings.ToLower(msg.Content)) == "/cancel" {
				cancelKey := msg.Channel + ":" + msg.ChatID + ":" + msg.SenderID
				if ch, ok := a.chatCancelCh.Load(cancelKey); ok {
					select {
					case ch.(chan struct{}) <- struct{}{}:
						a.bus.Outbound <- bus.OutboundMessage{
							Channel: msg.Channel,
							ChatID:  msg.ChatID,
							Content: "✅ 已取消当前请求",
						}
					default:
						// cancel 信号已发过
					}
				} else {
					a.bus.Outbound <- bus.OutboundMessage{
						Channel: msg.Channel,
						ChatID:  msg.ChatID,
						Content: "当前没有正在处理的请求",
					}
				}
				continue
			}

			key := msg.Channel + ":" + msg.ChatID
			q := getOrCreateQueue(key)
			select {
			case q <- msg:
			default:
				log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": key}).Warn("Chat queue full, dropping message")
			}
		}
	}
}

// NormalizeSenderID returns the effective sender ID for the message.
// In single-user mode, all sender IDs are mapped to "default".
func (a *Agent) NormalizeSenderID(senderID string) string {
	if a.singleUser {
		return "default"
	}
	return senderID
}

// workspaceRoot returns the workspace root for the given sender.
// In single-user mode, returns workDir directly (no per-user subdirectory),
// skipping directory permission checks that are unnecessary for single-user deployments.
func (a *Agent) workspaceRoot(senderID string) string {
	if a.singleUser {
		return a.workDir
	}
	return tools.UserWorkspaceRoot(a.workDir, senderID)
}

// isRemoteUser checks whether the given user routes to a remote sandbox.
// Uses SandboxResolver for per-user routing instead of checking Name() on the
// global SandboxRouter (which returns "router", not "remote").
func (a *Agent) isRemoteUser(userID string) bool {
	return a.sandboxNameForUser(userID) == "remote"
}

// isDockerUser checks whether the given user routes to a docker sandbox.
func (a *Agent) isDockerUser(userID string) bool {
	return a.sandboxNameForUser(userID) == "docker"
}

// sandboxNameForUser resolves the sandbox name for a given user.
func (a *Agent) sandboxNameForUser(userID string) string {
	if a.sandbox == nil {
		return ""
	}
	if resolver, ok := a.sandbox.(tools.SandboxResolver); ok {
		return resolver.SandboxForUser(userID).Name()
	}
	return a.sandbox.Name()
}

// remoteWorkspace returns the remote runner's workspace for the given user.
// Returns "" if the user is not on a remote sandbox or has no active connection.
//
// Deprecated: use sandboxWorkspace instead for all sandbox file operations.
func (a *Agent) remoteWorkspace(userID string) string {
	if a.sandbox == nil {
		return ""
	}
	if resolver, ok := a.sandbox.(tools.SandboxResolver); ok {
		return resolver.SandboxForUser(userID).Workspace(userID)
	}
	if a.sandbox.Name() == "remote" {
		return a.sandbox.Workspace(userID)
	}
	return ""
}

// sandboxWorkspace returns the correct workspace path for sandbox file operations.
// For docker mode: returns "/workspace" (the container-internal mount point).
// For remote mode: returns the runner's registered workspace.
// For none/local mode: returns the host-side user workspace root.
func (a *Agent) sandboxWorkspace(userID string) string {
	if a.sandbox == nil {
		return a.workspaceRoot(userID)
	}
	sb := a.sandbox
	if resolver, ok := sb.(tools.SandboxResolver); ok {
		sb = resolver.SandboxForUser(userID)
	}
	switch sb.Name() {
	case "docker":
		return sb.Workspace(userID) // "/workspace"
	case "remote":
		return sb.Workspace(userID) // runner's workspace
	default:
		return a.workspaceRoot(userID)
	}
}

// ensureWorkspace ensures the workspace directory exists (sandbox-aware).
// Skipped for remote and docker sandboxes — they manage their own filesystems.
func (a *Agent) ensureWorkspace(ctx context.Context, dir, senderID string) error {
	if a.isRemoteUser(senderID) {
		return nil
	}
	// For docker mode, the workspace is inside the container (/workspace);
	// don't create host-side directories — the container manages its own FS.
	if a.isDockerUser(senderID) {
		return nil
	}
	if a.sandbox != nil {
		return a.sandbox.MkdirAll(ctx, dir, 0o755, senderID)
	}
	return os.MkdirAll(dir, 0o755)
}

// isGroupChat 判断是否为群聊
// 使用消息的 ChatType 字段：p2p 为私聊，group 为群聊
func (a *Agent) isGroupChat(msg bus.InboundMessage) bool {
	return msg.ChatType == "group"
}

// getSemaphoreForMessage 获取消息应该使用的信号量
// 私聊：用户有自定义 LLM 则使用独立信号量
// 群聊：始终使用全局信号量（因为群里有多人，使用独立信号量会导致其他人的消息也被阻塞）
func (a *Agent) getSemaphoreForMessage(msg bus.InboundMessage, globalSem chan struct{}) chan struct{} {
	senderID := msg.SenderID
	if senderID == "" {
		return globalSem
	}

	// 群聊使用全局信号量
	if a.isGroupChat(msg) {
		return globalSem
	}

	// 私聊：检查用户是否有自定义 LLM
	if a.llmFactory.HasCustomLLM(senderID) {
		return a.getUserSemaphore(senderID)
	}

	return globalSem
}

// chatWorker 处理单个 chat 的消息队列，保证同一 chat 内顺序处理。
// 通过信号量控制并发：获取信号量后才开始处理，处理完释放。
// 信号量在每次处理消息时动态选择，以支持用户中途设置/取消自定义 LLM。
// chatWorker 处理单个 chat 的消息队列。
// 主循环持续从 ch 取消息并分发：
//   - 指令消息（/version, /help 等）：独立 goroutine 立即执行，不阻塞
//   - 普通消息：发送到内部 msgCh，由专门的 goroutine 串行处理（带信号量 + cancel）
//
// 这样即使普通消息正在长时间处理（LLM 推理），主循环仍能取出并执行命令消息。
func (a *Agent) chatWorker(ctx context.Context, chatKey string, ch <-chan bus.InboundMessage, globalSem chan struct{}) {
	// 内部普通消息队列：主循环写入，processLoop 消费
	msgCh := make(chan bus.InboundMessage, 32)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.chatProcessLoop(ctx, chatKey, msgCh, globalSem)
	}()

	for msg := range ch {
		if ctx.Err() != nil {
			break
		}

		// 指令消息分发：根据 Concurrent() 决定执行方式
		if cmd := a.commands.Match(msg.Content); cmd != nil {
			if cmd.Concurrent() {
				// 无状态命令：独立 goroutine 处理，不占信号量，不阻塞
				go func(m bus.InboundMessage, c Command) {
					response, err := c.Execute(ctx, a, m)
					if err != nil {
						log.WithFields(log.Fields{"request_id": m.RequestID, "chat": chatKey}).WithError(err).Error("Error processing command")
						content := formatErrorForUser(err)
						if sendErr := a.sendMessage(m.Channel, m.ChatID, content); sendErr != nil {
							a.bus.Outbound <- bus.OutboundMessage{
								Channel: m.Channel,
								ChatID:  m.ChatID,
								Content: content,
							}
						}
						return
					}
					if response != nil {
						a.bus.Outbound <- *response
					}
				}(msg, cmd)
			} else {
				// 有状态命令（/new, /compress, /set-llm 等）：走串行队列，
				// 避免与正在处理的普通消息产生 session 数据竞态
				select {
				case msgCh <- msg:
				case <-ctx.Done():
				}
			}
			continue
		}

		// 普通消息：转发到内部队列，由 processLoop 串行处理
		select {
		case msgCh <- msg:
		case <-ctx.Done():
		}
	}

	close(msgCh)
	wg.Wait()
}

// chatProcessLoop 串行处理普通消息（非命令），带信号量控制和 per-request cancel 支持。
func (a *Agent) chatProcessLoop(ctx context.Context, chatKey string, ch <-chan bus.InboundMessage, globalSem chan struct{}) {
	var idleTimer *time.Timer
	defer func() {
		if idleTimer != nil {
			idleTimer.Stop()
		}
	}()

	var lastSenderID string // 记录最后活跃的 senderID

	for msg := range ch {
		if ctx.Err() != nil {
			return
		}

		// 停止上一次的 idle timer（收到新消息，重置计时）
		if idleTimer != nil {
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
		}

		sem := a.getSemaphoreForMessage(msg, globalSem)

		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}

		// 创建 per-request cancel context
		var response *bus.OutboundMessage
		var err error
		cancelCh := make(chan struct{}, 1)
		cancelKey := msg.Channel + ":" + msg.ChatID + ":" + msg.SenderID
		a.chatCancelCh.Store(cancelKey, cancelCh)

		reqCtx, reqCancel := context.WithCancel(ctx)

		// 监听 cancel 信号
		go func() {
			select {
			case <-cancelCh:
				reqCancel()
			case <-reqCtx.Done():
			}
		}()

		// 执行消息处理，完成后检查是否被取消
		// 注意：必须在 reqCancel() 调用前检查，否则 reqCtx.Err() 总是返回 Canceled
		wasCancelled := reqCtx.Err() == context.Canceled
		func() {
			defer func() {
				reqCancel()
				a.chatCancelCh.Delete(cancelKey)
				<-sem // 释放槽位
			}()

			// 沙箱正在 export+import 时，拒绝该用户所有请求
			if sb := tools.GetSandbox(); sb.IsExporting(msg.SenderID) {
				log.WithFields(log.Fields{"request_id": msg.RequestID, "sender": msg.SenderID}).Info("Request rejected: sandbox export in progress")
				a.sendMessage(msg.Channel, msg.ChatID, "⏳ 沙箱正在持久化中，请稍后再试...")
				return
			}

			response, err = a.processMessage(reqCtx, msg)
			// 在 defer 执行前检查是否被取消（processMessage 过程中用户可能 /cancel）
			if reqCtx.Err() == context.Canceled {
				wasCancelled = true
			}
		}()

		if wasCancelled && ctx.Err() == nil {
			// 请求被用户 /cancel 取消（而非全局 ctx 关闭）
			log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": chatKey}).Info("Request cancelled by user")
			continue
		}

		if err != nil {
			log.WithFields(log.Fields{"request_id": msg.RequestID, "chat": chatKey}).WithError(err).Error("Error processing message")
			// 走 sendMessage 与正常回复同一路径：可 Patch 已发出的进度条为错误内容，避免错误静默不达用户
			content := formatErrorForUser(err)
			if sendErr := a.sendMessage(msg.Channel, msg.ChatID, content); sendErr != nil {
				log.Ctx(ctx).WithError(sendErr).Warn("Failed to send error via sendMessage, fallback to bus")
				a.bus.Outbound <- bus.OutboundMessage{
					Channel: msg.Channel,
					ChatID:  msg.ChatID,
					Content: content,
				}
			}
			continue
		}
		if response != nil {
			a.bus.Outbound <- *response
		}

		// 更新最后活跃的 senderID
		lastSenderID = msg.SenderID

		// 处理完成后，如果启用了 idle timeout 且用户有 docker 沙箱，设置 timer
		// Remote sandbox 连接应保持常驻，不做 idle 清理
		if a.sandboxIdleTimeout > 0 && lastSenderID != "" {
			// Skip idle cleanup for remote sandbox — the runner connection should be persistent
			if !a.isRemoteUser(lastSenderID) {
				idleTimer = time.AfterFunc(a.sandboxIdleTimeout, func() {
					if err := a.sandbox.CloseForUser(lastSenderID); err != nil {
						log.WithError(err).Warnf("Idle sandbox cleanup failed for user %s", lastSenderID)
					} else {
						log.Infof("Idle sandbox cleaned up for user %s (timeout: %s)", lastSenderID, a.sandboxIdleTimeout)
					}
				})
			}
		}
	}
}

// processMessage 处理单条入站消息

func (a *Agent) processMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	// 使用消息携带的 requestID（在渠道收到消息时生成），如果没有则生成新的
	reqID := msg.RequestID
	if reqID == "" {
		reqID = log.NewRequestID()
	}
	ctx = log.WithRequestID(ctx, reqID)

	// 注入 senderID 到 context，用于 per-user human block（Letta 模式）
	// Recall/Memorize 会通过 letta.GetUserID(ctx) 获取 userID
	ctx = letta.WithUserID(ctx, msg.SenderID)

	preview := msg.Content
	if r := []rune(preview); len(r) > 80 {
		preview = string(r[:80]) + "..."
	}
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"sender":  msg.SenderID,
	}).Infof("Processing: %s", preview)

	// Cron 消息使用独立处理流程（不带历史上下文，不参与消息更新跟踪）
	if msg.IsCron {
		return a.processCronMessage(ctx, msg)
	}

	// 初始化 session 消息跟踪：清除旧的已发消息 ID，记录入站消息 ID 用于首条回复
	key := msg.Channel + ":" + msg.ChatID
	a.sessionMsgIDs.Delete(key)
	a.sessionFinalSent.Delete(key)
	if msg.Metadata != nil && msg.Metadata["message_id"] != "" {
		a.sessionReplyTo.Store(key, msg.Metadata["message_id"])
	} else {
		a.sessionReplyTo.Delete(key)
	}

	// 获取或创建租户会话（senderID 通过 context 传递，不在这里传）
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("get/create tenant session: %w", err)
	}

	// 缓存消息到聊天历史（用于 ChatHistory 工具查询）
	a.chatHistory.Add(msg.Channel, msg.ChatID, msg.SenderID, msg.Content)
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
		"sender":  msg.SenderID,
	}).Debug("Message cached to chat history")

	// 指令匹配：通过 CommandRegistry 统一分发
	if cmd := a.commands.Match(msg.Content); cmd != nil {
		log.Ctx(ctx).WithFields(log.Fields{
			"channel": msg.Channel,
			"command": cmd.Name(),
		}).Info("Command matched")
		return cmd.Execute(ctx, a, msg)
	}

	// 处理卡片响应（按钮点击、表单提交）
	if msg.Metadata != nil && msg.Metadata["card_response"] == "true" {
		return a.handleCardResponse(ctx, msg, tenantSession)
	}

	preReplyNotify := bus.ShouldPreReplyNotify(msg.Metadata)
	replyPolicy := bus.InboundReplyPolicy(msg.Metadata)

	// 立即发送随机确认回复
	if preReplyNotify {
		a.sendAck(msg.Channel, msg.ChatID)
	}

	// 检查是否需要触发自动记忆合并
	a.maybeConsolidate(ctx, tenantSession, msg.SenderID)

	// 构建 LLM 消息（注入长期记忆、skills）
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return nil, err
	}

	// 运行 Agent 循环（统一 Run）
	cfg := a.buildMainRunConfig(ctx, msg, messages, tenantSession, preReplyNotify)
	out := Run(ctx, cfg)
	if out.Error != nil {
		return nil, out.Error
	}
	finalContent := out.Content
	toolsUsed := out.ToolsUsed
	waitingUser := out.WaitingUser

	// 如果工具正在等待用户响应，不生成回复消息
	if waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, skipping reply")
		if msg.Metadata == nil || msg.Metadata["user_msg_eager_saved"] != "true" {
			userMsg := llm.NewUserMessage(msg.Content)
			if !msg.Time.IsZero() {
				userMsg.Timestamp = msg.Time
			}
			if err := tenantSession.AddMessage(userMsg); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
			}
		}
		// Persist engine-produced messages (assistant + tool) so the next
		// turn has full context of what happened before waiting.
		for _, em := range out.EngineMessages {
			assertNoSystemPersist(em)
			if err := tenantSession.AddMessage(em); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to save engine message during waiting")
			}
		}
		return nil, nil
	}

	// 如果最终内容为空且不是 Optional reply 策略，向用户发送提示
	if finalContent == "" && !waitingUser && replyPolicy != bus.ReplyPolicyOptional {
		log.Ctx(ctx).Warn("Run produced empty content without waiting for user input")
		if err := a.sendMessage(msg.Channel, msg.ChatID, "⚠️ 处理完成，但未生成回复内容。请尝试重新描述您的需求。"); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to send empty content notification")
		}
		return nil, nil
	}

	if finalContent == "" && replyPolicy == bus.ReplyPolicyOptional {
		if msg.Metadata == nil || msg.Metadata["user_msg_eager_saved"] != "true" {
			userMsg := llm.NewUserMessage(msg.Content)
			if !msg.Time.IsZero() {
				userMsg.Timestamp = msg.Time
			}
			if err := tenantSession.AddMessage(userMsg); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
			}
		}
		log.Ctx(ctx).WithFields(log.Fields{
			"channel":      msg.Channel,
			"chat_id":      msg.ChatID,
			"reply_policy": replyPolicy,
		}).Info("Optional reply policy: no final response generated, skipping outbound")
		return nil, nil
	}

	// 保存会话
	if msg.Metadata == nil || msg.Metadata["user_msg_eager_saved"] != "true" {
		userMsg := llm.NewUserMessage(msg.Content)
		if !msg.Time.IsZero() {
			userMsg.Timestamp = msg.Time
		}
		if err := tenantSession.AddMessage(userMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
		}
	}

	// Persist engine-produced messages (assistant + tool) for context continuity.
	// This ensures the next turn has full context of what happened, not just a summary.
	for _, em := range out.EngineMessages {
		assertNoSystemPersist(em)
		if err := tenantSession.AddMessage(em); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save engine message")
		}
	}

	assistantMsg := llm.NewAssistantMessage(finalContent)
	// Attach iteration history as JSON detail for UI display (not included in LLM context).
	if len(out.IterationHistory) > 0 {
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			assistantMsg.Detail = string(jsonBytes)
		}
	}
	if len(toolsUsed) > 0 {
		_ = toolsUsed
	}
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save assistant message")
	}

	// 通过 sendMessage 发送最终回复（复用 session 内的消息更新跟踪）
	sendMeta := map[string]string{}
	if assistantMsg.Detail != "" {
		sendMeta["progress_history"] = assistantMsg.Detail
	}
	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent, sendMeta); err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to send final response via sendMessage")
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: finalContent,
		}, nil
	}

	// 对用户原始消息添加表情回复，表示处理完成
	a.addReaction(msg)

	return nil, nil
}

// processCronMessage 处理 cron 触发消息（不带历史上下文，使用专用系统提示词）
func (a *Agent) processCronMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	// 注入 requestID（如果 processMessage 未注入）
	if log.RequestID(ctx) == "" {
		ctx = log.WithRequestID(ctx, log.NewRequestID())
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"channel":   msg.Channel,
		"chat_id":   msg.ChatID,
		"sender_id": msg.SenderID,
	}).Infof("Processing cron message: %s", tools.Truncate(msg.Content, 80))

	// 清除旧的 session 状态，确保 cron 消息可以正常发送
	key := msg.Channel + ":" + msg.ChatID
	a.sessionMsgIDs.Delete(key)
	a.sessionFinalSent.Delete(key)

	// 使用创建者的工作区路径
	senderID := msg.SenderID
	workspaceRoot := a.workspaceRoot(senderID)
	if err := a.ensureWorkspace(ctx, workspaceRoot, senderID); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to create cron user workspace")
	}

	// 构建 cron 专用消息（无历史上下文）
	mc := NewCronMessageContext(msg.Content)
	messages := a.cronPipeline.Run(mc)

	// 运行 Agent 循环（统一 Run，cron 不需要自动压缩和进度通知）
	cronMsg := msg
	cronMsg.SenderID = senderID
	cronCfg := a.buildCronRunConfig(ctx, cronMsg, messages)
	cronOut := Run(ctx, cronCfg)
	if cronOut.Error != nil {
		return nil, cronOut.Error
	}
	finalContent := cronOut.Content

	if finalContent == "" {
		finalContent = "定时任务已执行，但无输出内容。"
	}

	// 注意：不保存到会话历史
	// 保留原始消息 ID 以支持回复模式
	metadata := make(map[string]string)
	if msg.Metadata != nil {
		metadata = msg.Metadata
	}

	return &bus.OutboundMessage{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		Content:  finalContent,
		Metadata: metadata,
	}, nil
}

// buildPrompt 构建完整的 LLM 消息列表（共用逻辑：processMessage 和 handlePromptQuery 都调用）。
// 使用 Agent 持有的 pipeline 实例，通过 MessageContext.Extra 传递动态数据。
func (a *Agent) buildPrompt(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) ([]llm.ChatMessage, error) {
	history, err := tenantSession.GetHistory(a.memoryWindow)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to get history, using empty history")
		history = nil
	}
	workspaceRoot := a.workspaceRoot(msg.SenderID)
	if err := a.ensureWorkspace(ctx, workspaceRoot, msg.SenderID); err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}
	newTools, err := a.multiSession.ConfigureSessionMCP(msg.Channel, msg.ChatID, msg.SenderID, a.workDir)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to configure session MCP scope")
	}
	if len(newTools) > 0 {
		sessionKey := msg.Channel + ":" + msg.ChatID
		a.tools.ActivateTools(sessionKey, newTools)
		log.Ctx(ctx).WithField("tools", len(newTools)).Info("Auto-activated new personal MCP tools")
	}

	promptWorkDir := a.workDir
	if a.sandboxMode == "docker" {
		promptWorkDir = "/workspace"
	} else if ws := a.remoteWorkspace(msg.SenderID); ws != "" {
		promptWorkDir = ws
	}

	mc := NewMessageContext(
		letta.WithUserID(ctx, msg.SenderID),
		msg.Content,
		history,
		msg.Channel,
		promptWorkDir,
		msg.SenderName,
		msg.SenderID,
		msg.ChatID,
	)

	// 注入当前工作目录（CWD）到 prompt
	// sandbox 模式下 CWD 已经是 sandbox 内路径，无 cd 时默认为 promptWorkDir
	mc.CWD = tenantSession.GetCurrentDir()
	if mc.CWD == "" {
		mc.CWD = promptWorkDir
	}

	mc.SetExtra(ExtraKeySkillsCatalog, a.skills.GetSkillsCatalog(ctx, msg.SenderID))
	mc.SetExtra(ExtraKeyAgentsCatalog, a.agents.GetAgentsCatalog(ctx, msg.SenderID))
	mc.SetExtra(ExtraKeyMemoryProvider, tenantSession.Memory())

	mc.SetExtra(ExtraKeyTenantID, tenantSession.TenantID())

	return a.pipeline.Run(mc), nil
}

// max returns the larger of a and b.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// consolidationWorker 专门的记忆整理 goroutine，通过 channel 接收整理请求
// 避免每次整理都创建新的 goroutine，同时保证同一个 tenant 不会并发整理
// 支持优雅关闭：收到 stop 信号后 drain 剩余请求再退出
func (a *Agent) consolidationWorker(ctx context.Context) {
	log.Info("Memory consolidation worker started")
	defer a.consolidateWg.Done()

	for {
		select {
		case <-ctx.Done():
			log.Info("Memory consolidation worker: context done, draining remaining requests...")
			// Drain 剩余请求
			for {
				select {
				case req := <-a.consolidateCh:
					a.doConsolidate(ctx, req)
				default:
					log.Info("Memory consolidation worker stopped (drained)")
					return
				}
			}
		case <-a.consolidateStopCh:
			log.Info("Memory consolidation worker: stop signal received, draining remaining requests...")
			// Drain 剩余请求
			for {
				select {
				case req := <-a.consolidateCh:
					a.doConsolidate(ctx, req)
				default:
					log.Info("Memory consolidation worker stopped (drained)")
					return
				}
			}
		case req := <-a.consolidateCh:
			a.doConsolidate(ctx, req)
		}
	}
}

// doConsolidate 执行实际的记忆整理
// 从 channel 接收请求后，重新获取 session 并验证是否仍需整理
func (a *Agent) doConsolidate(ctx context.Context, req consolidateRequest) {
	tenantKey := req.channel + ":" + req.chatID

	defer func() {
		a.consolidatingMu.Lock()
		delete(a.consolidating, tenantKey)
		a.consolidatingMu.Unlock()
	}()

	// 重新获取 session（避免引用过期）
	tenantSession, err := a.multiSession.GetOrCreateSession(req.channel, req.chatID)
	if err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to get session for consolidation")
		return
	}

	// 验证是否仍需整理（可能已被其他路径整理过）
	length, err := tenantSession.Len()
	if err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to get session length for consolidation")
		return
	}
	lastConsolidated := tenantSession.LastConsolidated()
	currentUnconsolidated := length - lastConsolidated

	// 如果当前未整理数小于触发时的数量，说明已被其他路径整理过
	if currentUnconsolidated < req.unconsolidated {
		log.Ctx(ctx).WithFields(log.Fields{
			"tenant":                 tenantKey,
			"current_unconsolidated": currentUnconsolidated,
			"trigger_unconsolidated": req.unconsolidated,
		}).Debug("Consolidation already done by another path, skipping")
		return
	}

	// 获取所有消息用于整理
	messages, err := tenantSession.GetMessages()
	if err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to get messages for consolidation")
		return
	}
	if len(messages) == 0 {
		log.Ctx(ctx).Debug("No messages to consolidate")
		return
	}

	mem := tenantSession.Memory()
	llmClient, model, _, _ := a.llmFactory.GetLLM(req.senderID)

	result, err := mem.Memorize(ctx, memory.MemorizeInput{
		Messages:         messages,
		LastConsolidated: lastConsolidated,
		LLMClient:        llmClient,
		Model:            model,
		ArchiveAll:       false,
		MemoryWindow:     a.memoryWindow,
	})
	if err != nil {
		log.Ctx(ctx).WithError(err).WithField("tenant", tenantKey).Warn("Auto memory consolidation failed")
		return
	}
	if result.OK {
		if err := tenantSession.SetLastConsolidated(result.NewLastConsolidated); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to update last consolidated")
		}
		log.Ctx(ctx).WithFields(log.Fields{
			"tenant":           tenantKey,
			"lastConsolidated": result.NewLastConsolidated,
		}).Info("Auto memory consolidation completed")
	}
}

func (a *Agent) maybeConsolidate(ctx context.Context, tenantSession *session.TenantSession, senderID string) {
	tenantKey := tenantSession.Channel() + ":" + tenantSession.ChatID()
	length, err := tenantSession.Len()
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to get session length for consolidation check")
		return
	}

	lastConsolidated := tenantSession.LastConsolidated()
	unconsolidated := length - lastConsolidated
	if unconsolidated < a.memoryWindow {
		return
	}

	// 持锁期间完成检查 + 标记，避免 TOCTOU 竞态
	a.consolidatingMu.Lock()
	defer a.consolidatingMu.Unlock()

	if a.consolidating[tenantKey] {
		return
	}

	// 立即尝试发送，失败则跳过（简化锁逻辑，避免阻塞）
	select {
	case a.consolidateCh <- consolidateRequest{
		channel:        tenantSession.Channel(),
		chatID:         tenantSession.ChatID(),
		senderID:       senderID,
		unconsolidated: unconsolidated,
	}:
		// 发送成功，标记为正在整理
		a.consolidating[tenantKey] = true
	default:
		// channel 满，立即放弃（不阻塞）
		log.Ctx(ctx).WithField("tenant", tenantKey).Warn("Consolidation channel full, request dropped")
	}
}

// clearConsolidationState 清除指定 tenant 的记忆整理状态
// 用于多路径协调：当 /new 清空会话时，需要取消正在进行的整理任务
func (a *Agent) clearConsolidationState(tenantKey string) {
	a.consolidatingMu.Lock()
	defer a.consolidatingMu.Unlock()

	if a.consolidating[tenantKey] {
		log.WithField("tenant", tenantKey).Info("Clearing consolidation state for /new")
		delete(a.consolidating, tenantKey)
	}
}

// summarizeRetryError 将 LLM 错误简化为用户友好的描述。
func summarizeRetryError(err error) string {
	if err == nil {
		return "未知错误"
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "TLS handshake timeout"):
		return "网络超时"
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝"
	case strings.Contains(msg, "429") || strings.Contains(msg, "rate limit"):
		return "请求限流"
	case strings.Contains(msg, "502") || strings.Contains(msg, "503"):
		return "服务暂时不可用"
	case strings.Contains(msg, "500") || strings.Contains(msg, "504"):
		return "服务端错误"
	default:
		var netErr net.Error
		if errors.As(err, &netErr) {
			if netErr.Timeout() {
				return "网络超时"
			}
			return "网络错误"
		}
		return "临时错误"
	}
}

// runLoop 执行 Agent 迭代循环（LLM -> 工具调用 -> LLM ...）
// autoNotify 为 true 时，累积显示模型中间内容和工具调用状态，实时更新同一条消息
// tenantSession 用于自动压缩后持久化压缩结果（可传 nil）

// RegisterTool registers a tool to the agent's tool registry.
// This is useful for dynamically adding tools after agent creation.
func (a *Agent) RegisterTool(tool tools.Tool) {
	a.tools.Register(tool)
	log.WithField("tool", tool.Name()).Info("Tool registered")
}

func (a *Agent) RegisterCoreTool(tool tools.Tool) {
	a.tools.RegisterCore(tool)
	log.WithField("tool", tool.Name()).Info("Tool registered")
}

// 首次发送创建新消息（如有入站 message_id 则回复该消息），后续发送 Patch 更新同一条消息。
// 工具发送最终回复（如飞书卡片）时同样 Patch 更新，但标记 session 为"已完成"，后续调用自动跳过。
func (a *Agent) sendMessage(channel, chatID, content string, metadata ...map[string]string) error {
	key := channel + ":" + chatID

	// 工具已发送最终回复 → 跳过后续所有消息（进度更新、LLM 最终回复等）
	if _, sent := a.sessionFinalSent.Load(key); sent {
		return nil
	}

	msg := bus.OutboundMessage{
		Channel: channel,
		ChatID:  chatID,
		Content: content,
	}
	if len(metadata) > 0 && metadata[0] != nil {
		msg.Metadata = metadata[0]
	}

	isFinal := strings.HasPrefix(content, "__FEISHU_CARD__:")

	if a.directSend != nil {
		if msg.Metadata == nil {
			msg.Metadata = make(map[string]string)
		}

		// Always include update_message_id for patch support.
		// For cards: feishu.go will attempt patch first; if cross-type conflict occurs,
		// it falls back to creating a new message and deleting the old progress message.
		if existingID, ok := a.sessionMsgIDs.Load(key); ok {
			msg.Metadata["update_message_id"] = existingID.(string)
		}

		if replyTo, ok := a.sessionReplyTo.Load(key); ok {
			msg.Metadata["message_id"] = replyTo.(string)
		}

		msgID, err := a.directSend(msg)
		if err != nil {
			return err
		}
		if msgID != "" {
			a.sessionMsgIDs.Store(key, msgID)
		}
		if isFinal {
			a.sessionFinalSent.Store(key, true)
		}
		return nil
	}

	// 降级：directSend 不可用时走 bus（无消息更新跟踪）
	select {
	case a.bus.Outbound <- msg:
		return nil
	default:
		return fmt.Errorf("message bus outbound channel is full")
	}
}

// injectInbound 向入站队列注入消息，触发 Agent 完整处理循环
func (a *Agent) injectInbound(channel, chatID, senderID, content string) {
	a.bus.Inbound <- bus.InboundMessage{
		Channel:   channel,
		SenderID:  senderID,
		ChatID:    chatID,
		Content:   content,
		Time:      time.Now(),
		IsCron:    true,
		RequestID: log.NewRequestID(),
	}
}

// RunSubAgent 实现 tools.SubAgentManager 接口
// 创建一个独立的子 Agent 循环来执行任务，子 Agent 拥有自己的工具集但不能再创建子 Agent
// allowedTools 为工具白名单，为空时使用所有工具（除 SubAgent）
func (a *Agent) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, roleName string) (string, error) {
	cfg := a.buildSubAgentRunConfig(parentCtx.Ctx, parentCtx, task, systemPrompt, allowedTools, caps, roleName, false)
	out := Run(parentCtx.Ctx, cfg)
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// addReaction 对用户消息添加表情回复，表示处理完成
func (a *Agent) addReaction(msg bus.InboundMessage) {
	if a.directSend == nil {
		return
	}
	messageID := ""
	if msg.Metadata != nil {
		messageID = msg.Metadata["message_id"]
	}
	if messageID == "" {
		return
	}

	_, err := a.directSend(bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Metadata: map[string]string{
			"add_reaction":        "DONE",
			"reaction_message_id": messageID,
		},
	})
	if err != nil {
		log.WithError(err).Debug("Failed to add reaction")
	}
}

// ProcessDirect 直接处理一条消息（用于 CLI 模式）
func (a *Agent) ProcessDirect(ctx context.Context, content string) (string, error) {
	msg := bus.InboundMessage{
		Channel:   "cli",
		SenderID:  a.NormalizeSenderID("user"),
		ChatID:    "direct",
		Content:   content,
		Time:      time.Now(),
		RequestID: log.NewRequestID(),
	}
	resp, err := a.processMessage(ctx, msg)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return resp.Content, nil
}

// formatToolProgress generates a human-readable one-line summary of a tool call for progress display.
// It parses the JSON args and extracts the most important parameter(s) based on the tool name.
// Output is concise, max ~80 chars total.
func formatToolProgress(name string, args string) string {
	const maxLen = 80

	// Helper to get a string field from parsed JSON
	get := func(m map[string]interface{}, key string) string {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
			// Handle numeric types (e.g., limit as float64 from JSON)
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	// Try to parse JSON args
	var m map[string]interface{}
	parsed := json.Unmarshal([]byte(args), &m) == nil

	// Helper to truncate and format the final result (rune-safe for multibyte chars)
	truncate := func(s string, max int) string {
		runes := []rune(s)
		if len(runes) <= max {
			return s
		}
		return string(runes[:max-3]) + "..."
	}

	if !parsed {
		log.WithField("tool", name).WithField("raw_args", truncate(args, 200)).Debug("formatToolProgress: failed to parse tool args as JSON")
	}

	// Letta memory tools
	switch name {
	case "core_memory_append":
		block := get(m, "block")
		return truncate(fmt.Sprintf("core_memory_append: %s", block), maxLen)
	case "core_memory_replace":
		block := get(m, "block")
		return truncate(fmt.Sprintf("core_memory_replace: %s", block), maxLen)
	case "rethink":
		block := get(m, "block")
		return truncate(fmt.Sprintf("rethink: %s", block), maxLen)
	case "archival_memory_insert":
		return "archival_memory_insert"
	case "archival_memory_search":
		query := get(m, "query")
		return truncate(fmt.Sprintf("archival_memory_search: %q", query), maxLen)
	case "recall_memory_search":
		query := get(m, "query")
		startDate := get(m, "start_date")
		endDate := get(m, "end_date")
		parts := []string{}
		if query != "" {
			parts = append(parts, fmt.Sprintf("%q", query))
		}
		if startDate != "" || endDate != "" {
			parts = append(parts, fmt.Sprintf("%s~%s", startDate, endDate))
		}
		return truncate(fmt.Sprintf("recall_memory_search: %s", strings.Join(parts, " ")), maxLen)
	}

	if !parsed {
		// JSON parsing failed: show truncated raw args
		raw := truncate(args, maxLen-len(name)-2)
		if raw == "" {
			return name
		}
		return truncate(fmt.Sprintf("%s: %s", name, raw), maxLen)
	}

	var summary string
	switch name {
	case "Shell":
		summary = fmt.Sprintf("Shell: %s", get(m, "command"))
	case "Read":
		summary = fmt.Sprintf("Read: %s", get(m, "path"))
	case "Edit":
		path := get(m, "path")
		mode := get(m, "mode")
		if mode != "" {
			summary = fmt.Sprintf("Edit: %s (%s)", path, mode)
		} else {
			summary = fmt.Sprintf("Edit: %s", path)
		}
	case "Grep":
		pattern := get(m, "pattern")
		path := get(m, "path")
		include := get(m, "include")
		target := path
		if include != "" {
			if target != "" {
				target = include + " in " + target
			} else {
				target = include
			}
		}
		if target != "" {
			summary = fmt.Sprintf("Grep: %q in %s", pattern, target)
		} else {
			summary = fmt.Sprintf("Grep: %q", pattern)
		}
	case "Glob":
		summary = fmt.Sprintf("Glob: %s", get(m, "pattern"))
	case "WebSearch":
		summary = fmt.Sprintf("WebSearch: %q", get(m, "query"))
	case "Cron":
		summary = fmt.Sprintf("Cron: %s", get(m, "action"))
	case "SubAgent":
		role := get(m, "role")
		task := get(m, "task")
		if role != "" {
			summary = truncate(fmt.Sprintf("SubAgent [%s]: %s", role, task), maxLen)
		} else {
			summary = fmt.Sprintf("SubAgent: %s", task)
		}
	case "DownloadFile":
		summary = fmt.Sprintf("DownloadFile: %s", get(m, "output_path"))
	case "ChatHistory":
		limit := get(m, "limit")
		if limit != "" {
			summary = fmt.Sprintf("ChatHistory: limit=%s", limit)
		} else {
			summary = "ChatHistory"
		}
	case "ManageTools":
		action := get(m, "action")
		mName := get(m, "name")
		if mName != "" {
			summary = fmt.Sprintf("ManageTools: %s %s", action, mName)
		} else {
			summary = fmt.Sprintf("ManageTools: %s", action)
		}
	case "card_create":
		title := get(m, "title")
		if title != "" {
			summary = fmt.Sprintf("card_create: %q", title)
		} else {
			summary = "card_create"
		}
	default:
		// Unknown tools (including MCP tools): show first 60 chars of args
		raw := truncate(args, 60)
		summary = fmt.Sprintf("%s: %s", name, raw)
	}

	// 去掉换行符，避免引用块断裂（工具参数可能含多行内容）
	summary = strings.NewReplacer("\n", " ", "\r", "").Replace(summary)
	return truncate(summary, maxLen)
}
