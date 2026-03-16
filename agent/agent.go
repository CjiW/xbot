package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	"xbot/cron"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	"xbot/oauth"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// ErrLLMGenerate 表示 LLM 生成调用失败（网络、API 4xx/5xx 等）
var ErrLLMGenerate = errors.New("LLM generate failed")

// assertNoSystemPersist 断言不得将 system 消息持久化到 session，否则会导致多条 system / 400 / 多人 sysprompt 混用。
func assertNoSystemPersist(m llm.ChatMessage) {
	if m.Role == "system" {
		log.WithField("message", m).Fatal("assert: must not persist system message to session")
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

func resolveGlobalSkillsDirs(workDir, legacySkillsDir string) []string {
	dirs := []string{
		filepath.Join(workDir, ".claude", "skills"),
	}
	if legacySkillsDir != "" {
		dirs = append(dirs, legacySkillsDir)
	}

	seen := make(map[string]struct{})
	result := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if _, ok := seen[abs]; ok {
			continue
		}
		seen[abs] = struct{}{}
		result = append(result, abs)
	}
	return result
}

// indexGlobalMCPTools indexes global MCP tools and built-in tool groups for search
func indexGlobalMCPTools(registry *tools.Registry, multiSession *session.MultiTenantSession, globalMCPConfigPath string) {
	ctx := context.Background()
	var toolEntries []memory.ToolIndexEntry

	// 1. Index built-in tool groups (like Feishu) - use each tool's own description
	toolGroups := registry.GetToolGroups()
	for _, group := range toolGroups {
		for _, toolName := range group.ToolNames {
			// Get the actual tool to get its description and channel support
			tool, ok := registry.Get(toolName)
			desc := fmt.Sprintf("Built-in tool group: %s", group.Name)
			var channels []string
			if ok {
				toolDesc := tool.Description()
				if toolDesc != "" {
					desc = fmt.Sprintf("Tool: %s. %s", toolName, toolDesc)
				}
				// Get supported channels
				if cp, ok := tool.(tools.ChannelProvider); ok {
					channels = cp.SupportedChannels()
				}
			}
			if group.Instructions != "" {
				desc = fmt.Sprintf("%s. %s", desc, group.Instructions)
			}
			// Store the name as-is (already has correct format like feishu_search_wiki)
			toolEntries = append(toolEntries, memory.ToolIndexEntry{
				Name:        toolName, // This is the loadable name
				ServerName:  group.Name,
				Source:      "global",
				Description: desc,
				Channels:    channels,
			})
		}
	}

	// 2. Index global MCP servers
	// Create a dummy MCP manager to load global MCP servers
	dummySessionKey := "indexing:dummy"
	mcpMgr := tools.NewSessionMCPManager(
		dummySessionKey,
		"__system__",        // system userID (avoids invalid Docker image name "xbot-:latest")
		globalMCPConfigPath, // global config (read-only)
		"",                  // no user config
		"",                  // no workspace root needed
		30*time.Minute,      // inactivity timeout (won't matter)
	)
	if mcpMgr != nil {
		// Trigger MCP connection to get the catalog (this connects to servers)
		catalog := mcpMgr.GetCatalog()
		for _, entry := range catalog {
			for _, toolName := range entry.ToolNames {
				// MCP tools need mcp_{server}_{toolName} format
				fullName := fmt.Sprintf("mcp_%s_%s", entry.Name, toolName)
				desc := fmt.Sprintf("MCP server: %s. Tool: %s", entry.Name, toolName)
				if entry.Instructions != "" {
					desc = fmt.Sprintf("%s. %s", desc, entry.Instructions)
				}

				toolEntries = append(toolEntries, memory.ToolIndexEntry{
					Name:        fullName, // This is the loadable name with mcp_ prefix
					ServerName:  entry.Name,
					Source:      "global",
					Description: desc,
				})
			}
		}
		// Close MCP manager to prevent resource leak
		mcpMgr.Close()
	}

	if len(toolEntries) == 0 {
		log.Info("No tools to index")
		return
	}

	// Index for tenant 0 (special global tenant ID)
	if err := multiSession.IndexToolsForTenant(ctx, 0, toolEntries); err != nil {
		log.WithError(err).Warn("Failed to index global tools")
		return
	}

	log.WithField("count", len(toolEntries)).Infof("Indexed %d global tools (MCP + built-in)", len(toolEntries))
}

// Agent 核心 Agent 引擎
type Agent struct {
	bus             *bus.MessageBus
	llmClient       llm.LLM
	model           string
	multiSession    *session.MultiTenantSession // Multi-tenant session manager
	tools           *tools.Registry
	maxIterations   int
	memoryWindow    int
	skills          *SkillStore
	agents          *AgentStore
	chatHistory     *tools.ChatHistoryStore // 聊天历史缓存
	cardBuilder     *tools.CardBuilder      // Card Builder MCP
	workDir         string
	promptLoader    *PromptLoader
	pipeline        *MessagePipeline // 消息构建管道（持有实例，支持运行时动态增删中间件）
	cronPipeline    *MessagePipeline // Cron 专用消息构建管道
	sandboxMode     string           // "none" or "docker"
	maxConcurrency  int              // 最大并发会话处理数
	globalSkillDirs []string         // 全局 skill 目录（宿主机路径）
	agentsDir       string           // 全局 agents 目录（宿主机路径）

	// 上下文压缩配置
	maxContextTokens     int
	compressionThreshold float64
	enableAutoCompress   bool

	// Cron service and scheduler
	cronSvc *sqlite.CronService
	cronSch *cron.Scheduler

	// User LLM config service and factory
	llmConfigSvc *sqlite.UserLLMConfigService
	llmFactory   *LLMFactory

	// 用户级别的信号量：设置了自己的 LLM 配置的用户使用独立信号量
	// key: senderID, value: 用户独立的信号量（容量为1）
	userSemaphores sync.Map // map[string]chan struct{}

	consolidatingMu sync.Mutex
	consolidating   map[string]bool // key: "channel:chat_id", value: 是否正在进行记忆合并

	commands         *CommandRegistry                          // 指令注册表
	directSend       func(bus.OutboundMessage) (string, error) // 同步发送，绕过 bus 以获取 message_id
	sessionMsgIDs    sync.Map                                  // key: "channel:chatID" -> 当前 session 已发消息 ID（用于 Patch 更新）
	sessionReplyTo   sync.Map                                  // key: "channel:chatID" -> 用户入站消息 ID（用于首条回复的 reply 模式）
	sessionFinalSent sync.Map                                  // key: "channel:chatID" -> bool, 工具已发送最终回复（如卡片），后续 sendMessage 跳过

	// per-request cancel: 用于 /cancel 取消当前正在处理的请求
	// key: "channel:chatID:senderID" -> chan struct{} (buffered, cap=1)
	chatCancelCh sync.Map
}

func buildToolMessageContent(result *tools.ToolResult) string {
	if result == nil {
		return ""
	}
	b, err := json.Marshal(result)
	if err != nil {
		return strings.TrimSpace(result.Summary)
	}
	return string(b)
}

// Config Agent 配置
type Config struct {
	Bus            *bus.MessageBus
	LLM            llm.LLM
	Model          string
	MaxIterations  int    // 单次对话最大工具调用迭代次数
	MaxConcurrency int    // 最大并发会话处理数（默认 2）
	MemoryWindow   int    // 上下文窗口大小（保留的历史消息数）
	DBPath         string // SQLite 数据库路径（空则使用默认路径）
	SkillsDir      string // Skills 目录
	WorkDir        string // 工作目录（所有文件相对此目录）
	PromptFile     string // 系统提示词模板文件路径（空则使用内置默认值）

	MemoryProvider   string // 记忆提供者: "flat" 或 "letta"
	EmbeddingBaseURL string // 嵌入向量服务地址
	EmbeddingAPIKey  string // 嵌入向量服务密钥
	EmbeddingModel   string // 嵌入模型名称

	// MCP 会话管理配置
	MCPInactivityTimeout time.Duration // MCP 不活跃超时时间
	MCPCleanupInterval   time.Duration // MCP 清理扫描间隔
	SessionCacheTimeout  time.Duration // 会话缓存超时

	// 上下文压缩配置
	MaxContextTokens     int     // 最大上下文 token 数（默认 8000，0 表示不限制）
	CompressionThreshold float64 // 触发压缩的 token 比例阈值（默认 0.8，即 80% 时触发）
	EnableAutoCompress   bool    // 是否启用自动上下文压缩（默认 false）
}

// New 创建 Agent
func New(cfg Config) *Agent {
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 100
	}
	if cfg.MaxConcurrency <= 0 {
		cfg.MaxConcurrency = 2
	}
	if cfg.MemoryWindow == 0 {
		cfg.MemoryWindow = 50
	}
	if cfg.WorkDir == "" {
		cfg.WorkDir = "."
	}
	if cfg.SkillsDir == "" {
		cfg.SkillsDir = filepath.Join(cfg.WorkDir, ".xbot", "skills")
	}
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(cfg.WorkDir, ".xbot", "xbot.db")
	}
	// 设置 MCP 配置默认值
	if cfg.MCPInactivityTimeout == 0 {
		cfg.MCPInactivityTimeout = 30 * time.Minute
	}
	if cfg.MCPCleanupInterval == 0 {
		cfg.MCPCleanupInterval = 5 * time.Minute
	}
	if cfg.SessionCacheTimeout == 0 {
		cfg.SessionCacheTimeout = 24 * time.Hour
	}
	// 设置上下文压缩配置默认值
	if cfg.MaxContextTokens == 0 {
		cfg.MaxContextTokens = 100000 // 默认 100k token
	}
	if cfg.CompressionThreshold == 0 {
		cfg.CompressionThreshold = 0.8
	}

	globalSkillDirs := resolveGlobalSkillsDirs(cfg.WorkDir, cfg.SkillsDir)
	skillStore := NewSkillStore(cfg.WorkDir, globalSkillDirs)

	// 加载 agent 角色定义（从 .xbot/agents/ 目录）
	agentsDir := filepath.Join(cfg.WorkDir, ".xbot", "agents")
	if err := tools.InitAgentRoles(agentsDir); err != nil {
		log.WithError(err).Warn("Failed to load agent roles, SubAgent will have no predefined roles")
	}
	agentStore := NewAgentStore(cfg.WorkDir, agentsDir)

	registry := tools.DefaultRegistry()

	// 创建聊天历史存储
	chatHistory := tools.NewChatHistoryStore(20) // 每个群组保留最近 20 条
	registry.Register(tools.NewChatHistoryTool(chatHistory))

	// MCP 配置路径：优先使用 .xbot/mcp.json，向后兼容 mcp.json
	mcpConfigPath := resolveDataPath(cfg.WorkDir, "mcp.json")

	// 注册 ManageTools tool（需要 skillStore 和 mcpConfigPath）
	registry.RegisterCore(tools.NewManageTools(cfg.WorkDir, mcpConfigPath))

	// Card Builder MCP: 仅注册 card_create（渐进上下文披露）
	cardBuilder := tools.NewCardBuilder()
	registry.Register(tools.NewCardCreateTool(cardBuilder))

	// 初始化多租户会话管理器（带 MCP 配置选项）
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
			BaseURL:    cfg.EmbeddingBaseURL,
			APIKey:     cfg.EmbeddingAPIKey,
			Model:      cfg.EmbeddingModel,
			LLMClient:  cfg.LLM,
			LLMModel:   cfg.Model,
			TokenModel: cfg.Model,
		}),
	)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize multi-tenant session")
	}
	multiSession.SetMCPConfigPath(mcpConfigPath)

	// 设置会话 MCP 管理器提供者
	registry.SetSessionMCPManagerProvider(multiSession)

	// 异步索引全局 MCP 工具（在后台进行，不阻塞启动）
	go func() {
		// 等待 MCP 配置加载完成
		time.Sleep(2 * time.Second)
		indexGlobalMCPTools(registry, multiSession, mcpConfigPath)
	}()

	// 如果使用 Letta 记忆模式，注册记忆工具（核心工具，始终可用）
	if memoryProvider == "letta" {
		for _, tool := range tools.LettaMemoryTools() {
			registry.RegisterCore(tool)
		}
		log.Info("Letta memory tools registered (core)")
	}

	sandboxMode := os.Getenv("SANDBOX_MODE")
	if sandboxMode == "" {
		sandboxMode = "docker"
	}

	agent := &Agent{
		bus:                  cfg.Bus,
		llmClient:            cfg.LLM,
		model:                cfg.Model,
		multiSession:         multiSession,
		tools:                registry,
		maxIterations:        cfg.MaxIterations,
		maxConcurrency:       cfg.MaxConcurrency,
		memoryWindow:         cfg.MemoryWindow,
		skills:               skillStore,
		agents:               agentStore,
		chatHistory:          chatHistory,
		cardBuilder:          cardBuilder,
		workDir:              cfg.WorkDir,
		promptLoader:         NewPromptLoader(cfg.PromptFile),
		sandboxMode:          sandboxMode,
		globalSkillDirs:      globalSkillDirs,
		maxContextTokens:     cfg.MaxContextTokens,
		compressionThreshold: cfg.CompressionThreshold,
		enableAutoCompress:   cfg.EnableAutoCompress,
		agentsDir:            agentsDir,
		consolidating:        make(map[string]bool),
	}

	// 初始化指令注册表
	agent.commands = NewCommandRegistry()
	registerBuiltinCommands(agent.commands)

	// 初始化消息构建管道
	agent.initPipelines()

	// 初始化 Cron 服务和调度器
	cronSvc := sqlite.NewCronService(multiSession.DB())
	cronSch := cron.NewScheduler(cronSvc)

	// 从旧的 JSON 文件迁移数据（如果需要）
	if err := cronSvc.MigrateFromJSON(cfg.WorkDir); err != nil {
		log.WithError(err).Warn("Failed to migrate cron jobs from JSON")
	}

	// 注册 CronTool（核心工具，始终可用）
	registry.RegisterCore(tools.NewCronTool(cronSvc))

	agent.cronSvc = cronSvc
	agent.cronSch = cronSch

	// Initialize UserLLMConfigService
	agent.llmConfigSvc = sqlite.NewUserLLMConfigService(multiSession.DB())
	agent.llmFactory = NewLLMFactory(agent.llmConfigSvc, cfg.LLM, cfg.Model)

	return agent
}

// SetDirectSend 注入同步发送函数（绕过 bus，用于消息更新跟踪）
func (a *Agent) SetDirectSend(fn func(bus.OutboundMessage) (string, error)) {
	a.directSend = fn
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
	log.WithField("max_concurrency", a.maxConcurrency).Info("Agent loop started")

	a.multiSession.StartCleanupRoutine()
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
	for msg := range ch {
		if ctx.Err() != nil {
			return
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

		// defer 确保 cancelCh 清理：即使 processMessage panic 也不会泄漏
		func() {
			defer func() {
				reqCancel()
				a.chatCancelCh.Delete(cancelKey)
				<-sem // 释放槽位
			}()
			response, err = a.processMessage(reqCtx, msg)
		}()

		if reqCtx.Err() == context.Canceled && ctx.Err() == nil {
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
	a.maybeConsolidate(ctx, tenantSession)

	// 构建 LLM 消息（注入长期记忆、skills）
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return nil, err
	}

	// 运行 Agent 循环
	finalContent, toolsUsed, waitingUser, err := a.runLoop(ctx, messages, msg.Channel, msg.ChatID, msg.SenderID, msg.SenderName, preReplyNotify, tenantSession)
	if err != nil {
		return nil, err
	}

	// 如果工具正在等待用户响应，不生成回复消息
	if waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, skipping reply")
		userMsg := llm.NewUserMessage(msg.Content)
		if !msg.Time.IsZero() {
			userMsg.Timestamp = msg.Time
		}
		if err := tenantSession.AddMessage(userMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
		}
		return nil, nil
	}

	if finalContent == "" && replyPolicy == bus.ReplyPolicyOptional {
		userMsg := llm.NewUserMessage(msg.Content)
		if !msg.Time.IsZero() {
			userMsg.Timestamp = msg.Time
		}
		if err := tenantSession.AddMessage(userMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
		}
		log.Ctx(ctx).WithFields(log.Fields{
			"channel":      msg.Channel,
			"chat_id":      msg.ChatID,
			"reply_policy": replyPolicy,
		}).Info("Optional reply policy: no final response generated, skipping outbound")
		return nil, nil
	}

	// 保存会话
	userMsg := llm.NewUserMessage(msg.Content)
	if !msg.Time.IsZero() {
		userMsg.Timestamp = msg.Time
	}
	if err := tenantSession.AddMessage(userMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
	}
	assistantMsg := llm.NewAssistantMessage(finalContent)
	if len(toolsUsed) > 0 {
		_ = toolsUsed
	}
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save assistant message")
	}

	// 通过 sendMessage 发送最终回复（复用 session 内的消息更新跟踪）
	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent); err != nil {
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
	workspaceRoot := tools.UserWorkspaceRoot(a.workDir, senderID)
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to create cron user workspace")
	}

	// 构建 cron 专用消息（无历史上下文）
	mc := NewCronMessageContext(msg.Content)
	messages := a.cronPipeline.Run(mc)

	// 运行 Agent 循环（传入创建者 senderID 而非空值，cron 不需要自动压缩，传 nil）
	finalContent, _, _, err := a.runLoop(ctx, messages, msg.Channel, msg.ChatID, senderID, "", false, nil)
	if err != nil {
		return nil, err
	}

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
	workspaceRoot := tools.UserWorkspaceRoot(a.workDir, msg.SenderID)
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
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
	mc.SetExtra(ExtraKeySkillsCatalog, a.skills.GetSkillsCatalog(msg.SenderID))
	mc.SetExtra(ExtraKeyAgentsCatalog, a.agents.GetAgentsCatalog(msg.SenderID))
	mc.SetExtra(ExtraKeyMemoryProvider, tenantSession.Memory())

	return a.pipeline.Run(mc), nil
}

// handlePromptQuery 构建完整提示词并写入文件发送给用户（dryrun，不调用 LLM）
func (a *Agent) handlePromptQuery(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	// 提取 /prompt 之后的 query 内容（先 trim 再截取，与 cmd 解析对齐）
	trimmed := strings.TrimSpace(msg.Content)
	query := strings.TrimSpace(trimmed[len("/prompt"):])
	if query == "" {
		query = "(empty query)"
	}

	// 替换 msg.Content 为 query，复用 buildPrompt
	dryMsg := msg
	dryMsg.Content = query
	messages, err := a.buildPrompt(ctx, dryMsg, tenantSession)
	if err != nil {
		return nil, err
	}

	// 获取工具定义
	sessionKey := msg.Channel + ":" + msg.ChatID
	toolDefs := a.tools.AsDefinitionsForSession(sessionKey)

	// 格式化输出
	var buf strings.Builder
	buf.WriteString("=== Prompt Dry Run ===\n\n")
	for i, m := range messages {
		fmt.Fprintf(&buf, "--- [%d] role: %s ---\n", i, m.Role)
		buf.WriteString(m.Content)
		buf.WriteString("\n\n")
	}

	fmt.Fprintf(&buf, "--- Tools (%d) ---\n", len(toolDefs))
	for _, td := range toolDefs {
		fmt.Fprintf(&buf, "- %s: %s\n", td.Name(), td.Description())
		for _, p := range td.Parameters() {
			req := ""
			if p.Required {
				req = " (required)"
			}
			fmt.Fprintf(&buf, "    %s (%s)%s: %s\n", p.Name, p.Type, req, p.Description)
		}
	}

	fmt.Fprintf(&buf, "\n--- Total messages: %d ---\n", len(messages))

	// 写入文件并发送
	workspaceRoot := tools.UserWorkspaceRoot(a.workDir, msg.SenderID)
	promptFile := filepath.Join(workspaceRoot, "prompt-dryrun.md")
	if err := os.WriteFile(promptFile, []byte(buf.String()), 0o644); err != nil {
		return nil, fmt.Errorf("write prompt file: %w", err)
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("[prompt-dryrun.md](%s)", promptFile),
	}, nil
}

// handleNewSession 处理 /new 命令：先归档记忆，再清空会话
func (a *Agent) handleNewSession(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	messages, err := tenantSession.GetMessages()
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取会话消息失败，请重试。",
		}, nil
	}
	lastConsolidated := tenantSession.LastConsolidated()
	mem := tenantSession.Memory()

	// 取尚未合并的消息进行归档
	snapshot := messages
	if lastConsolidated < len(messages) {
		snapshot = messages[lastConsolidated:]
	}

	if len(snapshot) > 0 {
		log.Ctx(ctx).WithField("tenant", tenantSession.String()).Infof("/new: archiving %d unconsolidated messages", len(snapshot))
		result, _ := mem.Memorize(ctx, memory.MemorizeInput{
			Messages:         snapshot,
			LastConsolidated: 0,
			LLMClient:        a.llmClient,
			Model:            a.model,
			ArchiveAll:       true,
			MemoryWindow:     a.memoryWindow,
		})
		if !result.OK {
			return &bus.OutboundMessage{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "记忆归档失败，会话未重置，请重试。",
			}, nil
		}
	}

	if err := tenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to clear tenant session")
	}
	if err := tenantSession.SetLastConsolidated(0); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to reset last consolidated")
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "会话已重置，记忆已归档。",
	}, nil
}

// handleCompress 处理 /compress 命令：手动触发上下文压缩
func (a *Agent) handleCompress(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	// 注意：手动 /compress 命令不受 enableAutoCompress 开关限制
	// 用户可能不想自动压缩但偶尔需要手动压缩一下

	// 使用 buildPrompt 获取完整上下文（包含 system、skills、memory 等）
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("构建上下文失败: %v", err),
		}, nil
	}

	if len(messages) == 0 {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "当前没有消息需要压缩。",
		}, nil
	}

	// 计算完整上下文的 token 数
	tokenCount, err := llm.CountMessagesTokens(messages, a.model)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to count tokens for compression")
		// 用户手动触发压缩时，计数失败应该强制执行或报错，而不是静默跳过
	}

	// 检查是否需要压缩（计数失败时也执行，用户明确要求压缩）
	threshold := int(float64(a.maxContextTokens) * a.compressionThreshold)
	if err == nil && tokenCount < threshold {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("当前上下文 token 数 (%d) 未达到压缩阈值 (%d)，无需压缩。", tokenCount, threshold),
		}, nil
	}

	// 发送压缩开始进度
	_ = a.sendMessage(msg.Channel, msg.ChatID, "🔄 开始压缩上下文...")

	// 执行压缩
	compressed, err := a.compressContext(ctx, messages, a.model)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩失败: %v", err),
		}, nil
	}

	// 替换会话消息
	// 先收集，全部成功才持久化，避免部分写入导致数据损坏
	if err := tenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to clear session for compression")
		// Clear 失败时只返回压缩结果，不持久化，避免数据损坏
		newTokenCount, _ := llm.CountMessagesTokens(compressed, a.model)
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩完成 (内存): %d → %d tokens (%d 条消息)", tokenCount, newTokenCount, len(compressed)),
		}, nil
	}
	allOk := true
	systemSkipped := 0
	for _, msg := range compressed {
		if msg.Role == "system" {
			systemSkipped++
			continue
		}
		assertNoSystemPersist(msg)
		if err := tenantSession.AddMessage(msg); err != nil {
			log.Ctx(ctx).WithError(err).Error("Partial write during compression, session may be corrupted")
			allOk = false
			break
		}
	}

	newTokenCount, _ := llm.CountMessagesTokens(compressed, a.model)
	if allOk {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩完成: %d → %d tokens (%d 条消息)", tokenCount, newTokenCount, len(compressed)),
		}, nil
	}
	// 部分写入失败，只返回内存结果
	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("上下文压缩完成 (内存): %d → %d tokens (%d 条消息)", tokenCount, newTokenCount, len(compressed)),
	}, nil
}

// handleContext 处理 /context 命令：显示当前 token 数和组成
func (a *Agent) handleContext(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	// 使用 buildPrompt 获取完整上下文（包含 system、skills、memory 等）
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取上下文失败，请重试。",
		}, nil
	}

	// 获取工具定义并计算 token
	sessionKey := msg.Channel + ":" + msg.ChatID
	toolDefs := a.tools.AsDefinitionsForSession(sessionKey)
	toolDefsTokens, _ := llm.CountToolsTokens(toolDefs, a.model)

	// 按角色统计 token 数
	var systemTokens, userTokens, assistantTokens, toolMsgTokens int

	for _, m := range messages {
		tokens, err := llm.CountMessagesTokens([]llm.ChatMessage{m}, a.model)
		if err != nil {
			continue
		}
		switch m.Role {
		case "system":
			systemTokens += tokens
		case "user":
			userTokens += tokens
		case "assistant":
			assistantTokens += tokens
		case "tool":
			toolMsgTokens += tokens
		}
	}

	total := systemTokens + userTokens + assistantTokens + toolMsgTokens + toolDefsTokens
	threshold := int(float64(a.maxContextTokens) * a.compressionThreshold)

	content := fmt.Sprintf(`📊 上下文 Token 统计

| 角色 | Token | 占比 |
|------|-------|------|
| System | %d | %.1f%% |
| User | %d | %.1f%% |
| Assistant | %d | %.1f%% |
| Tool (消息) | %d | %.1f%% |
| Tool (定义) | %d | %.1f%% |
| **总计** | **%d** | 100%% |

⚙️ 配置:
- 最大上下文: %d tokens
- 压缩阈值: %d tokens (%.0f%%)`,
		systemTokens, float64(systemTokens)*100/float64(max(total, 1)),
		userTokens, float64(userTokens)*100/float64(max(total, 1)),
		assistantTokens, float64(assistantTokens)*100/float64(max(total, 1)),
		toolMsgTokens, float64(toolMsgTokens)*100/float64(max(total, 1)),
		toolDefsTokens, float64(toolDefsTokens)*100/float64(max(total, 1)),
		total,
		a.maxContextTokens,
		threshold,
		a.compressionThreshold*100,
	)

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
	}, nil
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// thinTail 精简尾部旧工具组，保留最近 keepGroups 组完整内容。
// 一个"工具组"= 一条 assistant(tool_calls) + 紧随其后的所有 tool result 消息。
// 对更早的组：截断 Content/Arguments，strip think blocks，保留消息结构不变（API 兼容）。
func thinTail(tail []llm.ChatMessage, keepGroups int) []llm.ChatMessage {
	const (
		thinContentMax = 300
		thinArgsMax    = 200
	)
	if keepGroups <= 0 {
		keepGroups = 3
	}

	// 识别工具组边界：每个 assistant(tool_calls) 开始一个新组，后续 tool 消息属于该组
	type toolGroup struct{ start, end int }
	var groups []toolGroup

	for i := range tail {
		if tail[i].Role == "assistant" && len(tail[i].ToolCalls) > 0 {
			g := toolGroup{start: i, end: i}
			for j := i + 1; j < len(tail) && tail[j].Role == "tool"; j++ {
				g.end = j
			}
			groups = append(groups, g)
		}
	}

	thinCount := len(groups) - keepGroups
	if thinCount <= 0 {
		return tail
	}

	result := make([]llm.ChatMessage, len(tail))
	copy(result, tail)

	for g := range thinCount {
		grp := groups[g]
		for j := grp.start; j <= grp.end; j++ {
			msg := result[j] // copy struct
			switch msg.Role {
			case "assistant":
				msg.Content = llm.StripThinkBlocks(msg.Content)
				msg.Content = truncateRunes(msg.Content, thinContentMax)
				if len(msg.ToolCalls) > 0 {
					tcs := make([]llm.ToolCall, len(msg.ToolCalls))
					copy(tcs, msg.ToolCalls)
					for k := range tcs {
						tcs[k].Arguments = truncateRunes(tcs[k].Arguments, thinArgsMax)
					}
					msg.ToolCalls = tcs
				}
			case "tool":
				msg.Content = truncateRunes(msg.Content, thinContentMax)
				msg.ToolArguments = truncateRunes(msg.ToolArguments, thinArgsMax)
			}
			result[j] = msg
		}
	}

	return result
}

func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "...[truncated]"
}

// compressContext 使用 LLM 压缩对话历史（Claude 风格）
// 核心原则：
// 1. 保留所有 tool 消息（tool_calls 和 tool result 必须配对，否则 API 报错）
// 2. 把压缩后的摘要作为 user prompt 直接调用 LLM
// 3. 保留 system 消息和最近的对话轮次
func (a *Agent) compressContext(ctx context.Context, messages []llm.ChatMessage, model string) ([]llm.ChatMessage, error) {
	// 第一步：找到尾部安全切割点
	// API 要求：assistant 的 tool_calls 必须紧跟对应的 tool result 消息
	// 从后往前扫描，找到最后一个"安全切割点"：
	//   - user 消息是安全切割点
	//   - 不带 tool_calls 的 assistant 消息是安全切割点
	//   - tool result 或 assistant(tool_calls) 必须成对保留，继续往前找
	tailStart := len(messages) // 默认不保留任何尾部消息
	for i := len(messages) - 1; i >= 1; i-- {
		msg := messages[i]
		if msg.Role == "user" {
			tailStart = i
			break
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			tailStart = i
			break
		}
		// tool result 或 assistant(tool_calls)：继续往前找，确保配对完整
		// 如果一直找到 index 1（紧接 system message），说明整个对话都是 tool 链，
		// 全部保留以避免破坏配对
		if i == 1 {
			tailStart = 1
		}
	}

	// 第二步：精简尾部旧工具组（保留最近 3 组完整，截断更早的组）
	var thinnedTail []llm.ChatMessage
	if tailStart < len(messages) {
		thinnedTail = thinTail(messages[tailStart:], 3)
	}

	// 第三步：分离消息
	// system 消息单独保留，tailStart 之前的非 system 消息需要压缩
	var systemMsgs []llm.ChatMessage
	var toCompress []llm.ChatMessage

	for i, msg := range messages {
		if i >= tailStart {
			break
		}
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		} else {
			toCompress = append(toCompress, msg)
		}
	}

	// 如果没有需要压缩的内容，返回 system + thinned tail（仍可因 tail thinning 减少 token）
	if len(toCompress) == 0 {
		var result []llm.ChatMessage
		result = append(result, systemMsgs...)
		result = append(result, thinnedTail...)
		return result, nil
	}

	// 第四步：构建压缩 prompt
	var historyText strings.Builder
	for _, msg := range toCompress {
		role := strings.ToUpper(msg.Role)
		content := msg.Content
		// 对 tool_calls 也记录函数名，帮助 LLM 理解上下文
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			var toolNames []string
			for _, tc := range msg.ToolCalls {
				toolNames = append(toolNames, tc.Name)
			}
			content += fmt.Sprintf(" [called tools: %s]", strings.Join(toolNames, ", "))
		}
		// 按 rune 截断，避免中文乱码
		if len([]rune(content)) > 800 {
			content = string([]rune(content)[:800]) + "..."
		}
		fmt.Fprintf(&historyText, "[%s] %s\n\n", role, content)
	}

	compressionPrompt := `You are a context compression expert. Your task is to compress the conversation history into a concise summary while retaining ALL important information.

## Compression Rules
1. Retain ALL key facts, decisions, and important details
2. Keep track of what the user has asked for and what has been done
3. Preserve any file paths, code snippets, or technical details
4. Maintain the logical flow and context of the conversation
5. Note any errors or issues that were encountered

## Important
- This is NOT a summary - it's a compressed version that preserves context
- Include specific details like file names, function names, variable names
- Note what tools were used and their results if relevant

## Conversation History (to compress)
` + historyText.String() + `

Output the compressed content directly, preserving as much context as possible.`

	// 第五步：调用 LLM 压缩
	resp, err := a.llmClient.Generate(ctx, model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a context compression expert."),
		llm.NewUserMessage(compressionPrompt),
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("LLM compress failed: %w", err)
	}

	compressed := llm.StripThinkBlocks(resp.Content)

	// 第六步：构建压缩后的消息结构
	// 结构：[system, user(压缩摘要), 尾部原始消息(tool_calls/tool_result 配对完整)]
	// assert: buildPrompt 应只产出一条 system，否则会导致 LLM 400 / 多人 sysprompt 混用
	if len(systemMsgs) > 1 {
		panic("assert: at most one system message in compress input; got " + fmt.Sprint(len(systemMsgs)))
	}
	var result []llm.ChatMessage

	// 保留 system 消息（仅用于本次 runLoop 内后续 LLM 调用；持久化时不得写入 session，见 runLoop/handleCompress）
	result = append(result, systemMsgs...)

	// 将压缩摘要作为 user 消息插入
	result = append(result, llm.NewUserMessage("[Previous conversation context]\n\n"+compressed))

	// 保留精简后的尾部消息（tool_calls/tool_result 配对完整，旧组已截断）
	result = append(result, thinnedTail...)

	return result, nil
}

// handleCardResponse 处理卡片响应（按钮点击、表单提交）
func (a *Agent) handleCardResponse(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	cardID := msg.Metadata["card_id"]
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
		"card_id": cardID,
	}).Info("Processing card response")

	// 注入卡片上下文，让 LLM 理解用户在回应什么
	summary := msg.Content
	if desc := a.cardBuilder.GetDescription(cardID); desc != "" {
		summary = desc + "\nUser interaction:\n" + summary
	}

	// 复用 buildPrompt，替换 Content 为卡片摘要
	cardMsg := msg
	cardMsg.Content = summary
	messages, err := a.buildPrompt(ctx, cardMsg, tenantSession)
	if err != nil {
		return nil, err
	}

	finalContent, toolsUsed, waitingUser, err := a.runLoop(ctx, messages, msg.Channel, msg.ChatID, msg.SenderID, msg.SenderName, true, tenantSession)
	if err != nil {
		return nil, err
	}

	if waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, skipping reply")
		return nil, nil
	}

	cardUserMsg := llm.NewUserMessage(summary)
	if !msg.Time.IsZero() {
		cardUserMsg.Timestamp = msg.Time
	}
	if err := tenantSession.AddMessage(cardUserMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
	}
	assistantMsg := llm.NewAssistantMessage(finalContent)
	if len(toolsUsed) > 0 {
		_ = toolsUsed
	}
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save assistant message")
	}

	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent); err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to send card response via sendMessage")
		return &bus.OutboundMessage{
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Content:  finalContent,
			Metadata: msg.Metadata,
		}, nil
	}
	return nil, nil
}

// maybeConsolidate 检查并异步触发记忆合并。
// 只在未合并消息数达到 memoryWindow 时触发，避免每轮对话都调用 LLM。
func (a *Agent) maybeConsolidate(ctx context.Context, tenantSession *session.TenantSession) {
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

	a.consolidatingMu.Lock()
	if a.consolidating[tenantKey] {
		a.consolidatingMu.Unlock()
		return
	}
	a.consolidating[tenantKey] = true
	a.consolidatingMu.Unlock()

	// 异步执行合并，不阻塞当前消息处理
	go func() {
		defer func() {
			a.consolidatingMu.Lock()
			a.consolidating[tenantKey] = false
			a.consolidatingMu.Unlock()
		}()

		messages, err := tenantSession.GetMessages()
		if err != nil {
			log.Ctx(ctx).WithError(err).Error("Failed to get messages for consolidation")
			return
		}
		lastConsolidated := tenantSession.LastConsolidated()
		mem := tenantSession.Memory()

		result, _ := mem.Memorize(ctx, memory.MemorizeInput{
			Messages:         messages,
			LastConsolidated: lastConsolidated,
			LLMClient:        a.llmClient,
			Model:            a.model,
			ArchiveAll:       false,
			MemoryWindow:     a.memoryWindow,
		})
		if result.OK {
			if err := tenantSession.SetLastConsolidated(result.NewLastConsolidated); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to update last consolidated")
			}
			log.Ctx(ctx).WithField("tenant", tenantSession.String()).Infof("Auto memory consolidation completed, lastConsolidated=%d", result.NewLastConsolidated)
		}
	}()
}

// runLoop 执行 Agent 迭代循环（LLM -> 工具调用 -> LLM ...）
// autoNotify 为 true 时，累积显示模型中间内容和工具调用状态，实时更新同一条消息
// tenantSession 用于自动压缩后持久化压缩结果（可传 nil）
// 返回: (finalContent, toolsUsed, waitingUser, error)
func (a *Agent) runLoop(ctx context.Context, messages []llm.ChatMessage, channel, chatID, senderID, senderName string, autoNotify bool, tenantSession *session.TenantSession) (string, []string, bool, error) {
	var toolsUsed []string
	var waitingUser bool
	var progressLines []string

	notifyProgress := func(extra string) {
		if !autoNotify {
			return
		}
		lines := progressLines
		if extra != "" {
			lines = append(append([]string{}, progressLines...), extra)
		}
		// 在非引用行和引用行之间插入空行，避免飞书 markdown 渲染粘连
		var buf strings.Builder
		for i, line := range lines {
			if i > 0 {
				prev := lines[i-1]
				prevIsQuote := strings.HasPrefix(prev, "> ")
				currIsQuote := strings.HasPrefix(line, "> ")
				if prevIsQuote != currIsQuote {
					buf.WriteByte('\n') // 额外空行分隔引用块和正文
				}
			}
			buf.WriteString(line)
			if i < len(lines)-1 {
				buf.WriteByte('\n')
			}
		}
		_ = a.sendMessage(channel, chatID, buf.String())
	}

	// 获取用户特定的 LLM 客户端
	llmClient, model, userMaxCtx := a.llmFactory.GetLLM(senderID)
	if model == "" {
		model = a.model
	}
	// 用户设置了 max_context 时覆盖全局值
	maxContextTokens := a.maxContextTokens
	if userMaxCtx > 0 {
		maxContextTokens = userMaxCtx
	}

	// 推进 round 计数，自动清理长期未使用的工具激活
	sessionKey := channel + ":" + chatID
	a.tools.TickSession(sessionKey)

	// 压缩检测和执行的辅助函数：在每次 LLM 调用前检测并执行压缩
	maybeCompress := func() {
		if !a.enableAutoCompress || len(messages) <= 3 {
			return
		}

		// 计算 messages token
		msgTokens, err := llm.CountMessagesTokens(messages, model)
		if err != nil {
			return
		}

		// 计算 tools token
		toolDefs := a.tools.AsDefinitionsForSession(sessionKey)
		toolTokens, _ := llm.CountToolsTokens(toolDefs, model)

		// 总 token = messages + tools
		tokenCount := msgTokens + toolTokens

		threshold := int(float64(maxContextTokens) * a.compressionThreshold)
		if tokenCount < threshold {
			return
		}

		// 发送压缩进度信息
		if autoNotify {
			progressLines = append(progressLines, fmt.Sprintf("> 📦 上下文过大 (%d tokens)，正在压缩...", tokenCount))
			notifyProgress("")
		}

		log.Ctx(ctx).WithFields(log.Fields{
			"tokens":    tokenCount,
			"threshold": threshold,
		}).Info("Auto context compression triggered")

		compressed, compressErr := a.compressContext(ctx, messages, model)
		if compressErr == nil {
			messages = compressed

			// 发送压缩完成进度信息
			newTokenCount, _ := llm.CountMessagesTokens(compressed, model)
			if autoNotify {
				progressLines = append(progressLines, fmt.Sprintf("> ✅ 上下文压缩完成: %d → %d tokens", tokenCount, newTokenCount))
				notifyProgress("")
			}
			log.Ctx(ctx).WithFields(log.Fields{
				"old_tokens": tokenCount,
				"new_tokens": newTokenCount,
			}).Info("Auto context compression completed")

			// 持久化压缩结果到 session（不写入 system 消息，否则下次 GetHistory 会带回 system，导致多条 system + 400 + 不同人 sysprompt 混用）
			if tenantSession != nil {
				if err := tenantSession.Clear(); err != nil {
					log.Ctx(ctx).WithError(err).Warn("Failed to clear session for auto compression, skipping persistence")
				} else {
					allOk := true
					systemSkipped := 0
					for _, msg := range compressed {
						if msg.Role == "system" {
							systemSkipped++
							continue
						}
						assertNoSystemPersist(msg)
						if err := tenantSession.AddMessage(msg); err != nil {
							log.Ctx(ctx).WithError(err).Error("Partial write during auto compression, session may be corrupted")
							allOk = false
							break
						}
					}
					if allOk {
						log.Ctx(ctx).Info("Auto compression persisted to session")
					} else {
						log.Ctx(ctx).Warn("Auto compression persistence failed, using in-memory result only")
					}
				}
			}
		} else {
			log.Ctx(ctx).WithError(compressErr).Warn("Auto context compression failed")
		}
	}

	for i := 0; i < a.maxIterations; i++ {
		// 每次 LLM 调用前检测是否需要压缩
		maybeCompress()

		if autoNotify && i > 0 {
			notifyProgress("> 💭 思考中...")
		}

		// 使用会话特定的工具定义（包含会话的 MCP 工具）
		toolDefs := a.tools.AsDefinitionsForSession(sessionKey)
		// assert: 发给 LLM 的消息必须恰好一条 system，否则易触发 400 / 行为异常
		var systemCount int
		for _, m := range messages {
			if m.Role == "system" {
				systemCount++
			}
		}
		if systemCount != 1 {
			panic("assert: LLM messages must have exactly one system message; got " + fmt.Sprint(systemCount))
		}
		response, err := llmClient.Generate(ctx, model, messages, toolDefs)
		if err != nil {
			return "", toolsUsed, false, fmt.Errorf("%w: %w", ErrLLMGenerate, err)
		}

		if !response.HasToolCalls() {
			// 返回给用户的内容需要过滤掉think块
			content := llm.StripThinkBlocks(response.Content)
			return content, toolsUsed, false, nil
		}

		// 过滤掉think块，用于用户展示和进度通知
		cleanContent := llm.StripThinkBlocks(response.Content)

		// 模型的中间思考内容加入进度（不加引用前缀，保留原始 markdown 格式）
		if autoNotify && cleanContent != "" {
			progressLines = append(progressLines, cleanContent)
		}

		// 记录 assistant 消息（含 tool_calls），保留原始content（包括think块）
		// 重要：根据 MiniMax 文档，think块需要完整保留在消息历史中才能发挥模型最佳性能
		assistantMsg := llm.ChatMessage{
			Role:      "assistant",
			Content:   response.Content, // 保留原始content，包含think块
			ToolCalls: response.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		// 将工具调用分为只读和写操作两组：只读并行执行，写操作串行执行
		readOnlyTools := map[string]bool{
			"Read": true, "Grep": true, "Glob": true,
			"WebSearch": true, "ChatHistory": true,
		}

		type toolCallEntry struct {
			index int
			tc    llm.ToolCall
		}
		var readOps, writeOps []toolCallEntry
		for idx, tc := range response.ToolCalls {
			entry := toolCallEntry{index: idx, tc: tc}
			if readOnlyTools[tc.Name] {
				readOps = append(readOps, entry)
			} else {
				writeOps = append(writeOps, entry)
			}
		}

		// 预分配结果槽位（按原始顺序）
		type toolExecResult struct {
			content    string
			llmContent string
			result     *tools.ToolResult
			err        error
			elapsed    time.Duration
		}
		execResults := make([]toolExecResult, len(response.ToolCalls))

		// 为所有工具调用添加进度行占位符
		progressStartIdx := len(progressLines)
		for _, tc := range response.ToolCalls {
			toolsUsed = append(toolsUsed, tc.Name)
			toolLabel := formatToolProgress(tc.Name, tc.Arguments)
			if autoNotify {
				progressLines = append(progressLines, fmt.Sprintf("> ⏳ %s ...", toolLabel))
			}
		}
		if autoNotify {
			notifyProgress("")
		}

		// execOne 执行单个工具并记录结果
		execOne := func(entry toolCallEntry) {
			tc := entry.tc
			argPreview := tc.Arguments
			if r := []rune(argPreview); len(r) > 200 {
				argPreview = string(r[:200]) + "..."
			}
			log.Ctx(ctx).WithFields(log.Fields{
				"tool": tc.Name,
				"id":   tc.ID,
			}).Infof("Tool call: %s(%s)", tc.Name, argPreview)

			start := time.Now()
			result, execErr := a.executeTool(ctx, tc, channel, chatID, senderID, senderName)
			elapsed := time.Since(start)
			execResults[entry.index] = toolExecResult{err: execErr, result: result, elapsed: elapsed}

			toolLabel := formatToolProgress(tc.Name, tc.Arguments)
			if execErr != nil {
				log.Ctx(ctx).WithFields(log.Fields{
					"tool":    tc.Name,
					"elapsed": elapsed.Round(time.Millisecond),
				}).WithError(execErr).Warn("Tool failed")
				execResults[entry.index].content = fmt.Sprintf("Error: %v\n\nPlease fix the issue and try again with corrected parameters.", execErr)
				execResults[entry.index].llmContent = execResults[entry.index].content

				if autoNotify {
					progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> ❌ %s (%s)", toolLabel, elapsed.Round(time.Millisecond))
				}
			} else {
				execResults[entry.index].content = result.Summary
				execResults[entry.index].llmContent = buildToolMessageContent(result)

				resultPreview := result.Summary
				if r := []rune(resultPreview); len(r) > 200 {
					resultPreview = string(r[:200]) + "..."
				}
				log.Ctx(ctx).WithFields(log.Fields{
					"tool":    tc.Name,
					"elapsed": elapsed.Round(time.Millisecond),
				}).Infof("Tool done: %s", resultPreview)

				if autoNotify {
					progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> ✅ %s (%s)", toolLabel, elapsed.Round(time.Millisecond))
				}
			}
		}

		// Phase 1: 只读操作并行执行（限制并发数，避免资源耗尽）
		if len(readOps) > 0 {
			const maxParallel = 8
			sem := make(chan struct{}, maxParallel)
			var wg sync.WaitGroup
			for _, entry := range readOps {
				wg.Add(1)
				sem <- struct{}{} // 获取信号量
				go func(e toolCallEntry) {
					defer wg.Done()
					defer func() { <-sem }() // 释放信号量
					execOne(e)
				}(entry)
			}
			wg.Wait()
			if autoNotify {
				notifyProgress("")
			}
		}

		// Phase 2: 写操作串行执行
		for _, entry := range writeOps {
			execOne(entry)
			if autoNotify {
				notifyProgress("")
			}
		}

		// 按原始顺序处理结果：OAuth 处理、sessionFinalSent 检查、构建 tool messages
		sessionKey = channel + ":" + chatID
		for idx, tc := range response.ToolCalls {
			r := execResults[idx]
			content := r.llmContent

			// OAuth 自动触发（已触发过则跳过，避免重复 OAuth 状态）
			if r.err != nil && oauth.IsTokenNeededError(r.err) {
				if _, sent := a.sessionFinalSent.Load(sessionKey); sent {
					log.Ctx(ctx).WithFields(log.Fields{
						"tool":   tc.Name,
						"reason": "sessionFinalSent already set, skipping duplicate oauth_authorize",
					}).Info("Skip duplicate OAuth auto-trigger")
					content = "OAuth authorization already in progress."
				} else {
					log.Ctx(ctx).WithFields(log.Fields{
						"tool":    tc.Name,
						"elapsed": r.elapsed.Round(time.Millisecond),
					}).Info("OAuth token needed, auto-triggering oauth_authorize tool")

					if oauthTool, ok := a.tools.Get("oauth_authorize"); ok {
						oauthInput := fmt.Sprintf(`{"provider": "feishu", "reason": "needed to access %s"}`, tc.Name)
						oauthCtx := &tools.ToolContext{
							Ctx:      ctx,
							Channel:  channel,
							ChatID:   chatID,
							SenderID: senderID,
							SendFunc: a.sendMessage,
						}
						oauthResult, oauthErr := oauthTool.Execute(oauthCtx, oauthInput)
						if oauthErr == nil && oauthResult != nil {
							content = oauthResult.Summary
							a.sessionFinalSent.Store(sessionKey, true)
							autoNotify = false
							waitingUser = oauthResult.WaitingUser
						} else {
							content = "OAuth authorization required. Please configure OAUTH_ENABLE=true and OAUTH_BASE_URL in your environment."
							log.Ctx(ctx).WithError(oauthErr).Error("Failed to execute oauth_authorize tool")
						}
					} else {
						content = "OAuth authorization required but oauth_authorize tool not found. Please enable OAuth in configuration."
					}
				}
			}

			// 检查 sessionFinalSent
			if _, sent := a.sessionFinalSent.Load(sessionKey); sent {
				autoNotify = false
				progressLines = nil
			}

			if r.result != nil && r.result.WaitingUser {
				waitingUser = true
			}

			toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
			if r.result != nil && r.result.Detail != "" {
				toolMsg.Detail = r.result.Detail
			}
			messages = append(messages, toolMsg)
		}

		// 如果有任何工具标记为等待用户响应，则停止循环，不生成额外回复
		if waitingUser {
			log.Ctx(ctx).Info("Tool is waiting for user response, ending loop without additional reply")
			return "", toolsUsed, true, nil
		}
	}

	return "已达到最大迭代次数，请重新描述你的需求。", toolsUsed, false, nil
}

// executeTool 执行单个工具调用
func (a *Agent) executeTool(ctx context.Context, tc llm.ToolCall, channel, chatID, senderID, senderName string) (*tools.ToolResult, error) {
	// 会话级别的工具优先级高于全局注册表
	sessionKey := channel + ":" + chatID
	var tool tools.Tool
	ok := false

	// 首先从会话的 MCP 管理器查找
	if mcpMgr := a.multiSession.GetSessionMCPManager(sessionKey); mcpMgr != nil {
		sessionTools := mcpMgr.GetSessionTools()
		for _, st := range sessionTools {
			if st.Name() == tc.Name {
				tool = st
				ok = true
				break
			}
		}
	}

	// 会话中找不到，再从全局注册表查找
	if !ok {
		tool, ok = a.tools.Get(tc.Name)
	}

	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", tc.Name)
	}

	// 拦截未激活工具的调用：返回提示而非执行
	if !a.tools.IsToolActive(sessionKey, tc.Name) {
		return &tools.ToolResult{
			Summary: fmt.Sprintf("Tool %q is not loaded yet. Call load_tools(tools=\"%s\") first to load it before use.", tc.Name, tc.Name),
		}, nil
	}

	// 刷新工具最后使用 round，延长激活有效期
	a.tools.TouchTool(sessionKey, tc.Name)

	var execCtx context.Context
	var cancel context.CancelFunc
	if tc.Name == "SubAgent" {
		execCtx = ctx
		cancel = func() {} // no-op: SubAgent manages its own timeouts internally
	} else {
		timeout := 120 * time.Second
		execCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()

	toolCtx := &tools.ToolContext{
		Ctx:                 execCtx,
		WorkingDir:          a.workDir,
		WorkspaceRoot:       tools.UserWorkspaceRoot(a.workDir, senderID),
		SandboxWorkDir:      "/workspace",
		ReadOnlyRoots:       a.globalSkillDirs,
		SkillsDirs:          a.globalSkillDirs,
		AgentsDir:           a.agentsDir,
		MCPConfigPath:       tools.UserMCPConfigPath(a.workDir, senderID),
		GlobalMCPConfigPath: resolveDataPath(a.workDir, "mcp.json"),
		SandboxEnabled:      true,
		PreferredSandbox:    "docker",
		AgentID:             "main",
		Manager:             a,
		DataDir:             a.workDir,
		Channel:             channel,
		ChatID:              chatID,
		SenderID:            senderID,
		SenderName:          senderName,
		SendFunc:            a.sendMessage,
		InjectInbound:       a.injectInbound,
		Registry:            a.tools,
		InvalidateAllSessionMCP: func() {
			a.multiSession.InvalidateAll()
		},
	}

	if err := os.MkdirAll(toolCtx.WorkspaceRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}

	// Wire Letta memory fields if the session uses LettaMemory
	if ts, err := a.multiSession.GetOrCreateSession(channel, chatID); err == nil {
		if lm, ok := ts.Memory().(*letta.LettaMemory); ok {
			toolCtx.TenantID = lm.TenantID()
			toolCtx.CoreMemory = lm.CoreService()
			toolCtx.ArchivalMemory = lm.ArchivalService()
			toolCtx.MemorySvc = lm.MemoryService()
			toolCtx.RecallTimeRange = a.multiSession.RecallTimeRangeFunc()
			toolCtx.ToolIndexer = lm
		}
	}

	return tool.Execute(toolCtx, tc.Arguments)
}

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
func (a *Agent) sendMessage(channel, chatID, content string) error {
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

	isFinal := strings.HasPrefix(content, "__FEISHU_CARD__:")
	isCard := strings.HasPrefix(content, "__FEISHU_CARD__:")

	if a.directSend != nil {
		msg.Metadata = make(map[string]string)

		// Cards should always create new messages, not patch existing ones
		// This avoids schema version conflicts (schemaV2 card vs schemaV1 message)
		if !isCard {
			if existingID, ok := a.sessionMsgIDs.Load(key); ok {
				msg.Metadata["update_message_id"] = existingID.(string)
			}
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
func (a *Agent) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string) (string, error) {
	ctx := parentCtx.Ctx
	parentAgentID := parentCtx.AgentID
	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// 子 Agent 工具集：除 SubAgent 外的所有标准工具（防止递归创建）
	subTools := a.tools.Clone()
	subTools.Unregister("SubAgent")

	// 如果指定了工具白名单，只保留白名单中的工具
	if len(allowedTools) > 0 {
		allowed := make(map[string]bool, len(allowedTools))
		for _, name := range allowedTools {
			allowed[name] = true
		}
		for _, tool := range subTools.List() {
			if !allowed[tool.Name()] {
				subTools.Unregister(tool.Name())
			}
		}
	}

	// 构建子 Agent 的消息（注入工作目录信息，让 LLM 知道文件操作的上下文）
	if parentCtx.SandboxEnabled && parentCtx.SandboxWorkDir != "" {
		systemPrompt += fmt.Sprintf("\n\nWorking directory: %s\n", parentCtx.SandboxWorkDir)
	} else if parentCtx.WorkspaceRoot != "" {
		systemPrompt += fmt.Sprintf("\n\nWorking directory: %s\n", parentCtx.WorkspaceRoot)
	}
	messages := []llm.ChatMessage{
		llm.NewSystemMessage(systemPrompt),
		llm.NewUserMessage(task),
	}

	log.WithFields(log.Fields{
		"parent": parentAgentID,
		"task":   tools.Truncate(task, 80),
	}).Info("SubAgent started")

	// 子 Agent 迭代循环（与主 Agent 的 runLoop 类似，但使用独立工具集）
	maxIter := 100                 // SubAgent 最大 100 轮（不用 a.maxIterations）
	llmTimeout := 3 * time.Minute  // 单轮 LLM 超时
	toolTimeout := 2 * time.Minute // 单个工具超时
	var toolsUsed []string
	var lastContent string

	for i := 0; i < maxIter; i++ {
		// LLM 调用加超时
		llmCtx, llmCancel := context.WithTimeout(ctx, llmTimeout)
		response, err := a.llmClient.Generate(llmCtx, a.model, messages, subTools.AsDefinitions())
		llmCancel()
		if err != nil {
			// 父 context 被取消时，返回已有结果
			if ctx.Err() != nil {
				return "Sub-agent was cancelled.", ctx.Err()
			}
			// LLM 超时或其他错误：优雅降级，返回已有结果而不是报错
			if lastContent != "" {
				log.WithFields(log.Fields{
					"parent":    parentAgentID,
					"iteration": i + 1,
				}).Warnf("SubAgent LLM failed, returning partial result: %v", err)
				return lastContent, nil
			}
			return fmt.Sprintf("Sub-agent LLM failed at iteration %d: %v", i+1, err), nil
		}

		// 过滤掉think块，用于用户展示
		cleanContent := llm.StripThinkBlocks(response.Content)

		if !response.HasToolCalls() {
			log.WithFields(log.Fields{
				"parent":    parentAgentID,
				"tools":     toolsUsed,
				"iteration": i + 1,
			}).Info("SubAgent completed")
			return cleanContent, nil
		}

		// 记录最新的中间内容，用于超时降级
		if cleanContent != "" {
			lastContent = cleanContent
		}

		// 记录 assistant 消息（含 tool_calls），保留原始content（包括think块）
		// 重要：根据 MiniMax 文档，think块需要完整保留在消息历史中才能发挥模型最佳性能
		assistantMsg := llm.ChatMessage{
			Role:      "assistant",
			Content:   response.Content, // 保留原始content，包含think块
			ToolCalls: response.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		for _, tc := range response.ToolCalls {
			toolsUsed = append(toolsUsed, tc.Name)

			tool, ok := subTools.Get(tc.Name)
			if !ok {
				toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, fmt.Sprintf("Error: unknown tool: %s", tc.Name))
				messages = append(messages, toolMsg)
				continue
			}

			execCtx, cancel := context.WithTimeout(ctx, toolTimeout)

			toolCtx := &tools.ToolContext{
				Ctx:              execCtx,
				WorkingDir:       parentCtx.WorkingDir,
				WorkspaceRoot:    parentCtx.WorkspaceRoot,
				SandboxWorkDir:   "/workspace",
				ReadOnlyRoots:    parentCtx.ReadOnlyRoots,
				SandboxEnabled:   parentCtx.SandboxEnabled,
				PreferredSandbox: parentCtx.PreferredSandbox,
				AgentID:          parentAgentID + "/sub",
			}

			result, execErr := tool.Execute(toolCtx, tc.Arguments)
			cancel()

			content := ""
			if execErr != nil {
				content = fmt.Sprintf("Error: %v", execErr)
			} else {
				content = buildToolMessageContent(result)
			}

			toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
			messages = append(messages, toolMsg)
		}
	}

	return "Sub-agent reached maximum iterations.", nil
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
		SenderID:  "user",
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
		summary = fmt.Sprintf("SubAgent: %s", get(m, "task"))
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
