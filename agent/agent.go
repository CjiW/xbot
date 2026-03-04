package agent

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/oauth"
	"xbot/session"
	"xbot/tools"
)

// Agent 核心 Agent 引擎
type Agent struct {
	bus           *bus.MessageBus
	llmClient     llm.LLM
	model         string
	multiSession  *session.MultiTenantSession // Multi-tenant session manager
	tools         *tools.Registry
	maxIterations int
	memoryWindow  int
	skills        *SkillStore
	chatHistory   *tools.ChatHistoryStore // 聊天历史缓存
	cardBuilder   *tools.CardBuilder      // Card Builder MCP
	workDir       string
	promptLoader  *PromptLoader

	consolidatingMu sync.Mutex
	consolidating   map[string]bool // key: "channel:chat_id", value: 是否正在进行记忆合并

	directSend       func(bus.OutboundMessage) (string, error) // 同步发送，绕过 bus 以获取 message_id
	sessionMsgIDs    sync.Map                                  // key: "channel:chatID" -> 当前 session 已发消息 ID（用于 Patch 更新）
	sessionReplyTo   sync.Map                                  // key: "channel:chatID" -> 用户入站消息 ID（用于首条回复的 reply 模式）
	sessionFinalSent sync.Map                                  // key: "channel:chatID" -> bool, 工具已发送最终回复（如卡片），后续 sendMessage 跳过
}

// Config Agent 配置
type Config struct {
	Bus           *bus.MessageBus
	LLM           llm.LLM
	Model         string
	MaxIterations int    // 单次对话最大工具调用迭代次数
	MemoryWindow  int    // 上下文窗口大小（保留的历史消息数）
	DBPath        string // SQLite 数据库路径（空则使用默认路径）
	SkillsDir     string // Skills 目录
	WorkDir       string // 工作目录（所有文件相对此目录）
	PromptFile    string // 系统提示词模板文件路径（空则使用内置默认值）

	// MCP 会话管理配置
	MCPInactivityTimeout time.Duration // MCP 不活跃超时时间
	MCPCleanupInterval   time.Duration // MCP 清理扫描间隔
	SessionCacheTimeout  time.Duration // 会话缓存超时
}

// New 创建 Agent
func New(cfg Config) *Agent {
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 20
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

	skillStore := NewSkillStore(cfg.SkillsDir)

	registry := tools.DefaultRegistry()

	// 创建聊天历史存储
	chatHistory := tools.NewChatHistoryStore(20) // 每个群组保留最近 20 条
	registry.Register(tools.NewChatHistoryTool(chatHistory))

	// MCP 配置路径
	mcpConfigPath := filepath.Join(cfg.WorkDir, "mcp.json")

	// 注册 ManageTools tool（需要 skillStore 和 mcpConfigPath）
	registry.Register(tools.NewManageTools(mcpConfigPath))

	// Card Builder MCP: 仅注册 card_create（渐进上下文披露）
	cardBuilder := tools.NewCardBuilder()
	registry.Register(tools.NewCardCreateTool(cardBuilder))

	// 初始化多租户会话管理器（带 MCP 配置选项）
	multiSession, err := session.NewMultiTenant(
		cfg.DBPath,
		session.WithMCPTimeout(cfg.MCPInactivityTimeout),
		session.WithCleanupInterval(cfg.MCPCleanupInterval),
		session.WithSessionCacheTimeout(cfg.SessionCacheTimeout),
	)
	if err != nil {
		log.WithError(err).Fatal("Failed to initialize multi-tenant session")
	}
	multiSession.SetMCPConfigPath(mcpConfigPath)

	// 设置会话 MCP 管理器提供者
	registry.SetSessionMCPManagerProvider(multiSession)

	return &Agent{
		bus:           cfg.Bus,
		llmClient:     cfg.LLM,
		model:         cfg.Model,
		multiSession:  multiSession,
		tools:         registry,
		maxIterations: cfg.MaxIterations,
		memoryWindow:  cfg.MemoryWindow,
		skills:        skillStore,
		chatHistory:   chatHistory,
		cardBuilder:   cardBuilder,
		workDir:       cfg.WorkDir,
		promptLoader:  NewPromptLoader(cfg.PromptFile),
		consolidating: make(map[string]bool),
	}
}

// SetDirectSend 注入同步发送函数（绕过 bus，用于消息更新跟踪）
func (a *Agent) SetDirectSend(fn func(bus.OutboundMessage) (string, error)) {
	a.directSend = fn
}

// GetCardBuilder returns the CardBuilder for card callback handling.
func (a *Agent) GetCardBuilder() *tools.CardBuilder {
	return a.cardBuilder
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

// Run 启动 Agent 循环，持续消费入站消息
func (a *Agent) Run(ctx context.Context) error {
	log.Info("Agent loop started")
	// 启动后台清理协程（清理不活跃的 MCP 连接和会话缓存）
	a.multiSession.StartCleanupRoutine()
	defer func() {
		// 清理所有会话的 MCP 连接
		a.multiSession.StopCleanupRoutine()
	}()
	for {
		select {
		case <-ctx.Done():
			log.Info("Agent loop stopped")
			return ctx.Err()
		case msg := <-a.bus.Inbound:
			response, err := a.processMessage(ctx, msg)
			if err != nil {
				log.WithError(err).Error("Error processing message")
				a.bus.Outbound <- bus.OutboundMessage{
					Channel: msg.Channel,
					ChatID:  msg.ChatID,
					Content: fmt.Sprintf("处理消息时发生错误: %v", err),
				}
				continue
			}
			if response != nil {
				a.bus.Outbound <- *response
			}
		}
	}
}

// processMessage 处理单条入站消息
func (a *Agent) processMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	preview := msg.Content
	if len(preview) > 80 {
		preview = preview[:80] + "..."
	}
	log.WithFields(log.Fields{
		"channel": msg.Channel,
		"sender":  msg.SenderID,
	}).Infof("Processing: %s", preview)

	// Cron 消息使用独立处理流程（不带历史上下文，不参与消息更新跟踪）
	if msg.SenderID == "cron" {
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

	// 获取或创建租户会话
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("get/create tenant session: %w", err)
	}

	// 缓存消息到聊天历史（用于 ChatHistory 工具查询）
	a.chatHistory.Add(msg.Channel, msg.ChatID, msg.SenderID, msg.Content)
	log.WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
		"sender":  msg.SenderID,
	}).Debug("Message cached to chat history")

	// 斜杠命令（不参与消息更新跟踪，直接走 bus）
	cmd := strings.TrimSpace(strings.ToLower(msg.Content))
	if cmd == "/new" {
		return a.handleNewSession(ctx, msg, tenantSession)
	}
	if cmd == "/help" {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "xbot 命令:\n/new — 开始新对话（归档记忆后重置）\n/help — 显示帮助",
		}, nil
	}

	// 处理卡片响应（按钮点击、表单提交）
	if msg.Metadata != nil && msg.Metadata["card_response"] == "true" {
		return a.handleCardResponse(ctx, msg, tenantSession)
	}

	// 立即发送随机确认回复
	a.sendAck(msg.Channel, msg.ChatID)

	// 检查是否需要触发自动记忆合并
	a.maybeConsolidate(ctx, tenantSession)

	// 加载用户画像（跨 session 共享）和 bot 自身画像
	_, userProfile, _ := a.multiSession.GetUserProfile(msg.SenderID)
	_, selfProfile, _ := a.multiSession.GetUserProfile("__me__")

	// 构建 LLM 消息（注入长期记忆、skills、用户画像和自身画像）
	history, err := tenantSession.GetHistory(a.memoryWindow)
	if err != nil {
		log.WithError(err).Warn("Failed to get history, using empty history")
		history = nil
	}
	skillsCatalog := a.skills.GetSkillsCatalog()
	memory := tenantSession.Memory()
	messages := BuildMessages(history, msg.Content, msg.Channel, memory, a.workDir, skillsCatalog, a.promptLoader, msg.SenderName, userProfile, selfProfile)

	// 运行 Agent 循环
	finalContent, toolsUsed, waitingUser, err := a.runLoop(ctx, messages, msg.Channel, msg.ChatID, msg.SenderID, msg.SenderName, true)
	if err != nil {
		return nil, err
	}

	// 如果工具正在等待用户响应，不生成回复消息
	if waitingUser {
		log.Info("Tool is waiting for user response, skipping reply")
		if err := tenantSession.AddMessage(llm.NewUserMessage(msg.Content)); err != nil {
			log.WithError(err).Warn("Failed to save user message")
		}
		return nil, nil
	}

	if finalContent == "" {
		finalContent = "处理完成，但没有需要回复的内容。"
	}

	// 保存会话
	if err := tenantSession.AddMessage(llm.NewUserMessage(msg.Content)); err != nil {
		log.WithError(err).Warn("Failed to save user message")
	}
	assistantMsg := llm.NewAssistantMessage(finalContent)
	if len(toolsUsed) > 0 {
		_ = toolsUsed
	}
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.WithError(err).Warn("Failed to save assistant message")
	}

	// 通过 sendMessage 发送最终回复（复用 session 内的消息更新跟踪）
	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent); err != nil {
		log.WithError(err).Error("Failed to send final response via sendMessage")
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
	log.WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
	}).Infof("Processing cron message: %s", truncate(msg.Content, 80))

	// 构建 cron 专用消息（无历史上下文）
	messages := BuildCronMessages(msg.Content, a.workDir)

	// 运行 Agent 循环
	finalContent, _, _, err := a.runLoop(ctx, messages, msg.Channel, msg.ChatID, "", "", false)
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
	memory := tenantSession.Memory()

	// 取尚未合并的消息进行归档
	snapshot := messages
	if lastConsolidated < len(messages) {
		snapshot = messages[lastConsolidated:]
	}

	if len(snapshot) > 0 {
		log.WithField("tenant", tenantSession.String()).Infof("/new: archiving %d unconsolidated messages", len(snapshot))
		_, ok := memory.Consolidate(ctx, snapshot, 0, a.llmClient, a.model, true, a.memoryWindow)
		if !ok {
			return &bus.OutboundMessage{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "记忆归档失败，会话未重置，请重试。",
			}, nil
		}
	}

	if err := tenantSession.Clear(); err != nil {
		log.WithError(err).Warn("Failed to clear tenant session")
	}
	if err := tenantSession.SetLastConsolidated(0); err != nil {
		log.WithError(err).Warn("Failed to reset last consolidated")
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "会话已重置，记忆已归档。",
	}, nil
}

// handleCardResponse 处理卡片响应（按钮点击、表单提交）
func (a *Agent) handleCardResponse(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	cardID := msg.Metadata["card_id"]
	log.WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
		"card_id": cardID,
	}).Info("Processing card response")

	// 注入卡片上下文，让 LLM 理解用户在回应什么
	summary := msg.Content
	if desc := a.cardBuilder.GetDescription(cardID); desc != "" {
		summary = desc + "\nUser interaction:\n" + summary
	}

	_, userProfile, _ := a.multiSession.GetUserProfile(msg.SenderID)
	_, selfProfile, _ := a.multiSession.GetUserProfile("__me__")

	history, err := tenantSession.GetHistory(a.memoryWindow)
	if err != nil {
		log.WithError(err).Warn("Failed to get history, using empty history")
		history = nil
	}
	skillsCatalog := a.skills.GetSkillsCatalog()
	memory := tenantSession.Memory()
	messages := BuildMessages(history, summary, msg.Channel, memory, a.workDir, skillsCatalog, a.promptLoader, msg.SenderName, userProfile, selfProfile)

	finalContent, toolsUsed, waitingUser, err := a.runLoop(ctx, messages, msg.Channel, msg.ChatID, msg.SenderID, msg.SenderName, true)
	if err != nil {
		return nil, err
	}

	if waitingUser {
		log.Info("Tool is waiting for user response, skipping reply")
		return nil, nil
	}

	if finalContent == "" {
		finalContent = "处理完成，但没有需要回复的内容。"
	}

	if err := tenantSession.AddMessage(llm.NewUserMessage(summary)); err != nil {
		log.WithError(err).Warn("Failed to save user message")
	}
	assistantMsg := llm.NewAssistantMessage(finalContent)
	if len(toolsUsed) > 0 {
		_ = toolsUsed
	}
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.WithError(err).Warn("Failed to save assistant message")
	}

	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent); err != nil {
		log.WithError(err).Error("Failed to send card response via sendMessage")
		return &bus.OutboundMessage{
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Content:  finalContent,
			Metadata: msg.Metadata,
		}, nil
	}
	return nil, nil
}

// maybeConsolidate 检查并异步触发记忆合并
func (a *Agent) maybeConsolidate(ctx context.Context, tenantSession *session.TenantSession) {
	tenantKey := tenantSession.Channel() + ":" + tenantSession.ChatID()
	length, err := tenantSession.Len()
	if err != nil {
		log.WithError(err).Warn("Failed to get session length for consolidation check")
		return
	}

	if length <= a.memoryWindow {
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
			log.WithError(err).Error("Failed to get messages for consolidation")
			return
		}
		lastConsolidated := tenantSession.LastConsolidated()
		memory := tenantSession.Memory()

		newLC, ok := memory.Consolidate(ctx, messages, lastConsolidated, a.llmClient, a.model, false, a.memoryWindow)
		if ok {
			if err := tenantSession.SetLastConsolidated(newLC); err != nil {
				log.WithError(err).Warn("Failed to update last consolidated")
			}
			log.WithField("tenant", tenantSession.String()).Infof("Auto memory consolidation completed, lastConsolidated=%d", newLC)
		}
	}()
}

// runLoop 执行 Agent 迭代循环（LLM -> 工具调用 -> LLM ...）
// autoNotify 为 true 时，累积显示模型中间内容和工具调用状态，实时更新同一条消息
// 返回: (finalContent, toolsUsed, waitingUser, error)
func (a *Agent) runLoop(ctx context.Context, messages []llm.ChatMessage, channel, chatID, senderID, senderName string, autoNotify bool) (string, []string, bool, error) {
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
		_ = a.sendMessage(channel, chatID, strings.Join(lines, "\n"))
	}

	for i := 0; i < a.maxIterations; i++ {
		if autoNotify && i > 0 {
			notifyProgress("💭 思考中...")
		}

		// 使用会话特定的工具定义（包含会话的 MCP 工具）
		sessionKey := channel + ":" + chatID
		toolDefs := a.tools.AsDefinitionsForSession(sessionKey)
		response, err := a.llmClient.Generate(ctx, a.model, messages, toolDefs)
		if err != nil {
			return "", toolsUsed, false, fmt.Errorf("LLM generate failed: %w", err)
		}

		if !response.HasToolCalls() {
			content := strings.TrimSpace(response.Content)
			return content, toolsUsed, false, nil
		}

		// 模型的中间思考内容加入进度
		if autoNotify && strings.TrimSpace(response.Content) != "" {
			progressLines = append(progressLines, strings.TrimSpace(response.Content))
		}

		// 记录 assistant 消息（含 tool_calls）
		assistantMsg := llm.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		// 执行每个工具调用
		for _, tc := range response.ToolCalls {
			toolsUsed = append(toolsUsed, tc.Name)

			if autoNotify {
				progressLines = append(progressLines, fmt.Sprintf("⏳ %s ...", tc.Name))
				notifyProgress("")
			}

			argPreview := tc.Arguments
			if len(argPreview) > 200 {
				argPreview = argPreview[:200] + "..."
			}
			log.WithFields(log.Fields{
				"tool": tc.Name,
				"id":   tc.ID,
			}).Infof("Tool call: %s(%s)", tc.Name, argPreview)

			start := time.Now()
			result, execErr := a.executeTool(ctx, tc, channel, chatID, senderID, senderName)
			elapsed := time.Since(start)

			// 工具已发送最终回复（如飞书卡片）→ 进度消息已被替换，禁用后续进度更新
			sessionKey := channel + ":" + chatID
			if _, sent := a.sessionFinalSent.Load(sessionKey); sent {
				autoNotify = false
				progressLines = nil
			}

			content := ""
			if execErr != nil {
				// Check if this is a TokenNeededError from OAuth
				if oauth.IsTokenNeededError(execErr) {
					// TokenNeededError - automatically trigger oauth_authorize tool
					log.WithFields(log.Fields{
						"tool":    tc.Name,
						"elapsed": elapsed.Round(time.Millisecond),
					}).Info("OAuth token needed, auto-triggering oauth_authorize tool")

					// Try to execute oauth_authorize tool directly
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
							// Mark that we've sent a final card (OAuth card)
							a.sessionFinalSent.Store(sessionKey, true)
							autoNotify = false
							waitingUser = oauthResult.WaitingUser
						} else {
							content = "OAuth authorization required. Please configure OAUTH_ENABLE=true and OAUTH_BASE_URL in your environment."
							log.WithError(oauthErr).Error("Failed to execute oauth_authorize tool")
						}
					} else {
						content = "OAuth authorization required but oauth_authorize tool not found. Please enable OAuth in configuration."
					}
				} else {
					content = fmt.Sprintf("Error: %v\n\nPlease fix the issue and try again with corrected parameters.", execErr)
				}
				log.WithFields(log.Fields{
					"tool":    tc.Name,
					"elapsed": elapsed.Round(time.Millisecond),
				}).WithError(execErr).Warn("Tool failed")

				if autoNotify {
					progressLines[len(progressLines)-1] = fmt.Sprintf("❌ %s (%s)", tc.Name, elapsed.Round(time.Millisecond))
					notifyProgress("")
				}
			} else {
				content = result.Summary
				if result.WaitingUser {
					waitingUser = true
				}

				resultPreview := content
				if len(resultPreview) > 200 {
					resultPreview = resultPreview[:200] + "..."
				}
				log.WithFields(log.Fields{
					"tool":    tc.Name,
					"elapsed": elapsed.Round(time.Millisecond),
				}).Infof("Tool done: %s", resultPreview)

				if autoNotify {
					progressLines[len(progressLines)-1] = fmt.Sprintf("✅ %s (%s)", tc.Name, elapsed.Round(time.Millisecond))
					notifyProgress("")
				}
			}

			toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
			if result != nil && result.Detail != "" {
				toolMsg.Detail = result.Detail
			}
			messages = append(messages, toolMsg)
		}

		// 如果有任何工具标记为等待用户响应，则停止循环，不生成额外回复
		if waitingUser {
			log.Info("Tool is waiting for user response, ending loop without additional reply")
			return "", toolsUsed, true, nil
		}
	}

	return "已达到最大迭代次数，请重新描述你的需求。", toolsUsed, false, nil
}

// executeTool 执行单个工具调用
func (a *Agent) executeTool(ctx context.Context, tc llm.ToolCall, channel, chatID, senderID, senderName string) (*tools.ToolResult, error) {
	// 首先尝试从全局注册表查找工具
	tool, ok := a.tools.Get(tc.Name)

	// 如果全局注册表中找不到，尝试从会话的 MCP 管理器查找
	if !ok {
		sessionKey := channel + ":" + chatID
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
	}

	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", tc.Name)
	}

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
		Ctx:           execCtx,
		WorkingDir:    a.workDir,
		AgentID:       "main",
		Manager:       a,
		DataDir:       a.workDir,
		Channel:       channel,
		ChatID:        chatID,
		SenderID:      senderID,
		SenderName:    senderName,
		SendFunc:      a.sendMessage,
		InjectInbound: a.injectInbound,
		Registry:      a.tools,
		InvalidateAllSessionMCP: func() {
			a.multiSession.InvalidateAll()
		},
		SaveUserProfile: func(profile string) error {
			return a.multiSession.SaveUserProfile(senderID, senderName, profile)
		},
		SaveSelfProfile: func(profile string) error {
			return a.multiSession.SaveUserProfile("__me__", "xbot", profile)
		},
	}

	return tool.Execute(toolCtx, tc.Arguments)
}

// RegisterTool registers a tool to the agent's tool registry.
// This is useful for dynamically adding tools after agent creation.
func (a *Agent) RegisterTool(tool tools.Tool) {
	a.tools.Register(tool)
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
func (a *Agent) injectInbound(channel, chatID, content string) {
	a.bus.Inbound <- bus.InboundMessage{
		Channel:  channel,
		SenderID: "cron",
		ChatID:   chatID,
		Content:  content,
		Time:     time.Now(),
	}
}

// RunSubAgent 实现 tools.SubAgentManager 接口
// 创建一个独立的子 Agent 循环来执行任务，子 Agent 拥有自己的工具集但不能再创建子 Agent
func (a *Agent) RunSubAgent(ctx context.Context, parentAgentID string, task string, systemPrompt string) (string, error) {
	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// 子 Agent 工具集：除 SubAgent 外的所有标准工具（防止递归创建）
	subTools := a.tools.Clone()
	subTools.Unregister("SubAgent")

	// 构建子 Agent 的消息
	messages := []llm.ChatMessage{
		llm.NewSystemMessage(systemPrompt),
		llm.NewUserMessage(task),
	}

	log.WithFields(log.Fields{
		"parent": parentAgentID,
		"task":   truncate(task, 80),
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

		if !response.HasToolCalls() {
			content := strings.TrimSpace(response.Content)
			log.WithFields(log.Fields{
				"parent":    parentAgentID,
				"tools":     toolsUsed,
				"iteration": i + 1,
			}).Info("SubAgent completed")
			return content, nil
		}

		// 记录最新的中间内容，用于超时降级
		if trimmed := strings.TrimSpace(response.Content); trimmed != "" {
			lastContent = trimmed
		}

		assistantMsg := llm.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
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
				Ctx:        execCtx,
				WorkingDir: a.workDir,
				AgentID:    parentAgentID + "/sub",
			}

			result, execErr := tool.Execute(toolCtx, tc.Arguments)
			cancel()

			content := ""
			if execErr != nil {
				content = fmt.Sprintf("Error: %v", execErr)
			} else {
				content = result.Summary
			}

			toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
			messages = append(messages, toolMsg)
		}
	}

	return "Sub-agent reached maximum iterations.", nil
}

// truncate 截断字符串
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
		Channel:  "cli",
		SenderID: "user",
		ChatID:   "direct",
		Content:  content,
		Time:     time.Now(),
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
