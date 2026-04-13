package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xbot/bus"
	channelpkg "xbot/channel"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	"xbot/oauth"
	"xbot/session"
	"xbot/tools"
)

// todoManagerAdapter wraps tools.TodoManager to implement TodoManagerProvider.
type todoManagerAdapter struct {
	mgr *tools.TodoManager
}

func (a *todoManagerAdapter) GetTodoSummary(sessionKey string) string {
	return a.mgr.GetTodoSummary(sessionKey)
}

func (a *todoManagerAdapter) GetTodoItems(sessionKey string) []TodoProgressItem {
	items := a.mgr.GetTodos(sessionKey)
	result := make([]TodoProgressItem, len(items))
	for i, item := range items {
		result[i] = TodoProgressItem{ID: item.ID, Text: item.Text, Done: item.Done}
	}
	return result
}

func (a *todoManagerAdapter) ClearTodos(sessionKey string) {
	a.mgr.SetTodos(sessionKey, nil)
}

// applyUserMaxContext 如果用户在 Settings 中设置了 max_context，
// 创建一个新的 ContextManagerConfig 副本并覆盖 MaxContextTokens，
// 避免污染 Agent 级别的原始配置（含 sync.RWMutex）。
func applyUserMaxContext(base *ContextManagerConfig, userMaxCtx int) *ContextManagerConfig {
	if userMaxCtx <= 0 || base == nil {
		return base
	}
	return &ContextManagerConfig{
		MaxContextTokens:     userMaxCtx,
		CompressionThreshold: base.CompressionThreshold,
		DefaultMode:          base.DefaultMode,
	}
}

// buildBaseRunConfig 构建主 Agent（main/cron）共用的基础 RunConfig。
// 包含 LLM、身份、工作区、工具执行器、循环控制、HookChain 等公共字段。
// 返回 (RunConfig, userMaxContext) — userMaxContext 为用户在 Settings 中设置的值，0 表示未设置。
func (a *Agent) buildBaseRunConfig(
	channel, chatID, senderID string,
	messages []llm.ChatMessage,
	senderName string,
	sandboxUserID string,
) (RunConfig, int) {
	sessionKey := channel + ":" + chatID

	llmClient, model, userMaxCtx, thinkingMode := a.llmFactory.GetLLM(senderID)

	// LLM 并发限流回调（per-tenant）
	llmSemAcquire := a.llmFactory.LLMSemAcquireForUser(senderID)
	subAgentSem := a.llmFactory.SubAgentSemAcquireForUser(senderID)

	return RunConfig{
		// 必需
		LLMClient:    llmClient,
		Model:        model,
		ThinkingMode: thinkingMode,
		Tools:        a.tools,
		Messages:     messages,

		// 身份
		AgentID:      "main",
		Channel:      channel,
		ChatID:       chatID,
		SenderID:     senderID,      // 直接调用者 = 原始用户（用于消息路由 + settings/usage 存储 key）
		OriginUserID: sandboxUserID, // 沙箱/工作区用户（飞书身份登录 web 时为飞书 ou_xxx）
		SenderName:   senderName,

		// 工作区 & 沙箱
		WorkingDir:       a.workDir,
		WorkspaceRoot:    a.workspaceRoot(sandboxUserID),
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    tools.UserMCPConfigPath(a.workDir, sandboxUserID),
		GlobalMCPConfig:  filepath.Join(a.xbotHome, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   a.sandboxMode != "none",
		PreferredSandbox: a.sandboxMode,
		Sandbox:          resolveSandbox(a.sandbox, sandboxUserID),
		SandboxMode:      a.sandboxMode,

		// 循环控制
		MaxIterations:   a.maxIterations,
		MaxOutputTokens: a.llmFactory.GetMaxOutputTokens(senderID),

		// Session
		SessionKey: sessionKey,

		// 发送
		SendFunc:      a.sendMessage,
		InjectInbound: a.injectInbound,

		// 工具执行
		ToolExecutor: a.buildToolExecutor(channel, chatID, senderID, senderName, sandboxUserID),
		// ToolTimeout: no longer used. Tools manage their own timeouts.

		// 读写分离（主 Agent 始终启用）
		EnableReadWriteSplit: true,

		// SessionFinalSent 回调
		SessionFinalSentCallback: func() bool {
			_, sent := a.sessionFinalSent.Load(sessionKey)
			return sent
		},

		// Letta 记忆字段
		ToolContextExtras: a.buildToolContextExtras(channel, chatID),

		// HookChain — inherit from Agent
		HookChain: a.hookChain,

		// SettingsSvc — inherit from Agent
		SettingsSvc: a.settingsSvc,

		// LLM 并发限流回调（per-tenant）
		LLMSemAcquire:             llmSemAcquire,
		EnableConcurrentSubAgents: true,
		SubAgentSem:               subAgentSem,
	}, userMaxCtx
}

// buildMainRunConfig 为主 Agent 构建完整的 RunConfig。
// 从 processMessage / handleCardResponse 调用。
func (a *Agent) buildMainRunConfig(
	_ context.Context,
	msg bus.InboundMessage,
	messages []llm.ChatMessage,
	tenantSession *session.TenantSession,
	autoNotify bool,
) RunConfig {
	channel, chatID, senderID, senderName := msg.Channel, msg.ChatID, msg.SenderID, msg.SenderName
	sessionKey := channel + ":" + chatID

	// 飞书身份登录 web 时，用飞书用户 ID 作为沙箱用户 ID，
	// 确保 web 端与飞书端共用同一个 Docker 容器和工作区。
	feishuUserID := msg.Metadata["feishu_user_id"]
	sandboxUserID := senderID
	if feishuUserID != "" {
		sandboxUserID = feishuUserID
	}

	cfg, userMaxCtx := a.buildBaseRunConfig(channel, chatID, senderID, messages, senderName, sandboxUserID)

	// 保留 FeishuUserID 供 buildToolContext 等处使用
	cfg.FeishuUserID = feishuUserID

	// 主 Agent 特有字段
	cfg.Session = tenantSession

	// Token 状态持久化：Run() 结束后写入 DB，重启后恢复
	if extras := cfg.ToolContextExtras; extras != nil && extras.MemorySvc != nil && extras.TenantID != 0 {
		memSvc := extras.MemorySvc
		tenantID := extras.TenantID
		cfg.SaveTokenState = func(promptTokens, completionTokens int64) {
			if err := memSvc.SetTokenState(context.Background(), tenantID, promptTokens, completionTokens); err != nil {
				log.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to persist token state")
			}
		}
	}

	// OAuth 处理
	cfg.OAuthHandler = a.buildOAuthHandler(channel, chatID, senderID, sessionKey)

	// 进度通知
	// Web 渠道始终启用结构化进度，但不发送文本进度消息
	if channel == "web" || channel == "cli" {
		// Web: no-op notifier — structured progress goes via ProgressEventHandler
		// Setting ProgressNotifier to non-nil enables autoNotify in engine.Run()
		cfg.ProgressNotifier = func(lines []string) {}
	} else if autoNotify {
		cfg.ProgressNotifier = func(lines []string) {
			if len(lines) > 0 {
				_ = a.sendMessage(channel, chatID, lines[0])
			}
		}
	}

	// 结构化进度事件推送（web 和 cli 渠道）
	if (channel == "web" || channel == "cli") && a.channelFinder != nil {
		// CLI 渠道进度处理
		switch channel {
		case "cli":
			if ch, ok := a.channelFinder("cli"); ok {
				if cc, ok := ch.(*channelpkg.CLIChannel); ok {
					cfg.ProgressEventHandler = func(event *ProgressEvent) {
						if event == nil || event.Structured == nil {
							return
						}
						s := event.Structured
						payload := &channelpkg.CLIProgressPayload{
							Phase:     string(s.Phase),
							Iteration: s.Iteration,
							Thinking:  s.ThinkingContent,
							Reasoning: s.ReasoningContent,
						}
						for _, t := range s.ActiveTools {
							payload.ActiveTools = append(payload.ActiveTools, channelpkg.CLIToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Iteration: t.Iteration,
								Summary:   t.Summary,
							})
						}
						for _, t := range s.CompletedTools {
							payload.CompletedTools = append(payload.CompletedTools, channelpkg.CLIToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Iteration: t.Iteration,
								Summary:   t.Summary,
							})
						}
						// Parse sub-agent tree from progress lines
						if len(event.Lines) > 0 {
							subAgents := ExtractSubAgentTree(event.Lines)
							if len(subAgents) > 0 {
								cliSubAgents := make([]channelpkg.CLISubAgent, len(subAgents))
								for i, sa := range subAgents {
									cliSubAgents[i] = channelpkg.CLISubAgent{
										Role:     sa.Role,
										Status:   sa.Status,
										Desc:     sa.Desc,
										Children: convertCLISubAgentTree(sa.Children),
									}
								}
								payload.SubAgents = cliSubAgents
							}
						}
						// Copy todo items for CLI display
						if len(s.Todos) > 0 {
							payload.Todos = make([]channelpkg.CLITodoItem, len(s.Todos))
							for i, td := range s.Todos {
								payload.Todos[i] = channelpkg.CLITodoItem{
									ID:   td.ID,
									Text: td.Text,
									Done: td.Done,
								}
							}
						}
						// §18 传递 Token 使用量快照
						if s.TokenUsage != nil {
							payload.TokenUsage = &channelpkg.CLITokenUsage{
								PromptTokens:     s.TokenUsage.PromptTokens,
								CompletionTokens: s.TokenUsage.CompletionTokens,
								TotalTokens:      s.TokenUsage.TotalTokens,
								CacheHitTokens:   s.TokenUsage.CacheHitTokens,
							}
						}
						cc.SendProgress(chatID, payload)
					}
				} else {
					log.WithField("channel", channel).Warn("CLI channel found but type assertion failed, skipping ProgressEventHandler")
				}
			}
		case "web":
			if ch, ok := a.channelFinder("web"); ok {
				if wc, ok := ch.(*channelpkg.WebChannel); ok {
					cfg.ProgressEventHandler = func(event *ProgressEvent) {
						if event == nil || event.Structured == nil {
							return
						}
						s := event.Structured
						payload := &channelpkg.WsProgressPayload{
							Phase:     string(s.Phase),
							Iteration: s.Iteration,
							Thinking:  s.ThinkingContent,
						}
						for _, t := range s.ActiveTools {
							payload.ActiveTools = append(payload.ActiveTools, channelpkg.WsToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Summary:   t.Summary,
								Iteration: t.Iteration,
							})
						}
						for _, t := range s.CompletedTools {
							payload.CompletedTools = append(payload.CompletedTools, channelpkg.WsToolProgress{
								Name:      t.Name,
								Label:     t.Label,
								Status:    string(t.Status),
								Elapsed:   t.Elapsed.Milliseconds(),
								Summary:   t.Summary,
								Iteration: t.Iteration,
							})
						}
						// Parse sub-agent tree from progress lines
						if len(event.Lines) > 0 {
							subAgents := ExtractSubAgentTree(event.Lines)
							if len(subAgents) > 0 {
								wsSubAgents := make([]channelpkg.WsSubAgent, len(subAgents))
								for i, sa := range subAgents {
									wsSubAgents[i] = channelpkg.WsSubAgent{
										Role:     sa.Role,
										Status:   sa.Status,
										Desc:     sa.Desc,
										Children: convertWsSubAgentTree(sa.Children),
									}
								}
								payload.SubAgents = wsSubAgents
							}
						}
						// Copy todo items for web display
						if len(s.Todos) > 0 {
							payload.Todos = make([]channelpkg.WsTodoItem, len(s.Todos))
							for i, td := range s.Todos {
								payload.Todos[i] = channelpkg.WsTodoItem{
									ID:   td.ID,
									Text: td.Text,
									Done: td.Done,
								}
							}
						}
						// Pass token usage snapshot
						if s.TokenUsage != nil {
							payload.TokenUsage = &channelpkg.WsTokenUsage{
								PromptTokens:     s.TokenUsage.PromptTokens,
								CompletionTokens: s.TokenUsage.CompletionTokens,
								TotalTokens:      s.TokenUsage.TotalTokens,
								CacheHitTokens:   s.TokenUsage.CacheHitTokens,
							}
						}

						// Keep event order stable for frontend rendering. SendProgress itself is non-blocking.
						wc.SendProgress(chatID, payload)
					}
				} else {
					log.WithField("channel", channel).Warn("Web channel found but type assertion failed, skipping ProgressEventHandler")
				}
			}
		}
	}

	// 注入 ContextManager
	cfg.ContextManager = a.GetContextManager()
	cfg.ContextManagerConfig = applyUserMaxContext(a.contextManagerConfig, userMaxCtx)

	// Per-user token usage tracking (persisted to SQLite)
	cfg.RecordUserTokenUsage = func(senderID, model string, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount int) {
		if err := a.multiSession.RecordUserTokenUsage(senderID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount); err != nil {
			log.WithError(err).WithField("sender_id", senderID).Warn("Failed to record user token usage")
		}
	}

	// SpawnAgent（主 Agent 可以创建 SubAgent）
	cfg.SpawnAgent = func(ctx context.Context, inMsg bus.InboundMessage) (*bus.OutboundMessage, error) {
		return a.spawnSubAgent(ctx, inMsg)
	}

	// OffloadStore — Layer 1 offload
	cfg.OffloadStore = a.offloadStore

	// MaskStore — Observation Masking（默认开启，可通过 settings 的 enable_masking 关闭）
	cfg.MaskStore = a.maskStore
	if a.settingsSvc != nil {
		if vals, err := a.settingsSvc.GetSettings(channel, senderID); err == nil {
			if vals["enable_masking"] == "false" {
				cfg.MaskStore = nil
			}
			if vals["enable_stream"] == "true" {
				cfg.Stream = true
			}
		}
	}

	// ContextEditor — Context Editing（精确编辑上下文）
	cfg.ContextEditor = a.contextEditor

	// TodoManager — TODO 状态查询
	if a.todoManager != nil {
		cfg.TodoManager = &todoManagerAdapter{mgr: a.todoManager}
	}

	// InteractiveCallbacks — interactive SubAgent 支持
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, channel, chatID, instance, tail)
		},
	}

	// Memory tools for compaction — allows the compaction LLM to archive
	// important context into core/archival memory before it gets compacted away.
	// Uses the real tool registry instead of hand-written execution logic.
	if defs, exec := a.buildMemoryToolSetup(channel, chatID); defs != nil {
		cfg.MemoryToolDefs = defs
		cfg.MemoryToolExec = exec
	}

	return cfg
}

// buildCronRunConfig 为 Cron 消息构建 RunConfig。
// Cron 消息不需要自动压缩、进度通知、session 持久化。
func (a *Agent) buildCronRunConfig(
	_ context.Context,
	msg bus.InboundMessage,
	messages []llm.ChatMessage,
) RunConfig {
	channel, chatID, senderID := msg.Channel, msg.ChatID, msg.SenderID

	cfg, _ := a.buildBaseRunConfig(channel, chatID, senderID, messages, "", senderID)
	return cfg
}

// buildSubAgentRunConfig 为 SubAgent 构建 RunConfig。
// SubAgent 使用独立工具集、无 session、有压缩（独立 ContextManager）、无进度通知。
// Phase 2: SubAgent 通过 RunConfig 继承父 Agent 的工作区配置，
// 使用统一的 defaultToolExecutor + buildToolContext 构建 ToolContext。
func (a *Agent) buildSubAgentRunConfig(
	ctx context.Context,
	parentCtx *tools.ToolContext,
	task string,
	systemPrompt string,
	allowedTools []string,
	caps tools.SubAgentCapabilities,
	roleName string,
	interactive bool,
	model string, // 可选：角色指定的模型，为空时继承主 Agent
) RunConfig {
	parentAgentID := parentCtx.AgentID

	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// 子 Agent 工具集：根据 capabilities 决定是否保留 SubAgent 工具
	subTools := a.tools.Clone()
	if !caps.SpawnAgent {
		subTools.Unregister("SubAgent")
	}

	// 如果指定了工具白名单，只保留白名单中的工具
	// 以下工具永久可用，不受白名单限制：
	//   - SubAgent（如果 caps.SpawnAgent=true）
	//   - offload_recall、recall_masked（SubAgent 需要访问父 Agent 的 offload/mask 数据）
	if len(allowedTools) > 0 {
		allowed := make(map[string]bool, len(allowedTools))
		for _, name := range allowedTools {
			allowed[name] = true
		}
		for _, tool := range subTools.List() {
			toolName := tool.Name()
			// SubAgent 工具：如果 SpawnAgent=true，始终保留
			if toolName == "SubAgent" && caps.SpawnAgent {
				continue
			}
			// offload_recall / recall_masked：SubAgent 始终可用
			if toolName == "offload_recall" || toolName == "recall_masked" {
				continue
			}
			if !allowed[toolName] {
				subTools.Unregister(toolName)
			}
		}
	}

	// 构建 SubAgent 的 system prompt：通用模板 + 角色专有能力描述
	workDir := parentCtx.WorkspaceRoot
	if parentCtx.Sandbox != nil && parentCtx.Sandbox.Name() != "none" {
		workDir = parentCtx.Sandbox.Workspace(parentCtx.OriginUserID)
	}
	now := time.Now().Format("2006-01-02 15:04:05 MST")

	// CWD 继承父 Agent 的当前目录，无则默认 workDir
	cwd := parentCtx.CurrentDir
	if cwd == "" {
		cwd = workDir
	}
	cwdPart := "\n- 当前目录：" + cwd

	// role.SystemPrompt 作为角色专有能力描述（非通用 prompt）
	rolePrompt := strings.TrimSpace(systemPrompt)
	if rolePrompt == "" {
		rolePrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// 通用模板 + 角色描述（有白名单时使用精简模板）
	var sysPrompt string
	if len(allowedTools) > 0 {
		sysPrompt = fmt.Sprintf(subagentSystemPromptTemplateConcise, workDir, cwdPart, roleName, parentAgentID, now)
	} else {
		sysPrompt = fmt.Sprintf(subagentSystemPromptTemplate, workDir, cwdPart, roleName, parentAgentID, now)
	}
	if interactive {
		sysPrompt += subagentExecutionModeInteractive
	} else {
		sysPrompt += subagentExecutionModeOneShot
	}
	sysPrompt += "\n## 角色描述\n\n" + rolePrompt + "\n"

	// 注入可用 agent 目录（只在 spawn_agent=true 时注入）
	if caps.SpawnAgent {
		if agentsCatalog := a.agents.GetAgentsCatalog(ctx, parentCtx.SenderID); agentsCatalog != "" {
			sysPrompt += "\n" + agentsCatalog
		}
	}

	// 注入 skills 目录（SubAgent 可使用 Skill 工具加载 skill）
	originUserID := parentCtx.OriginUserID
	if originUserID == "" {
		originUserID = parentCtx.SenderID
	}
	if skillsCatalog := a.skills.GetSkillsCatalog(ctx, originUserID); skillsCatalog != "" {
		sysPrompt += "\n" + skillsCatalog
	}

	// Pre-compute parentExtras once (shared between Phase 4 and buildSubAgentMemory)
	parentExtras := a.buildToolContextExtras(parentCtx.Channel, parentCtx.ChatID)

	// Phase 4: Inject project context from AGENT.md in current working directory
	if projectCtx := LoadProjectContextFile(a.workDir); projectCtx != "" {
		sysPrompt += projectCtx
	}

	// Phase 5: Inject user language preference into SubAgent prompt.
	// Only inject if not already present in the inherited system prompt
	// (LanguageMiddleware on the main Agent already adds it via SystemParts).
	if a.settingsSvc != nil {
		if vals, err := a.settingsSvc.GetSettings(parentCtx.Channel, originUserID); err == nil {
			if lang, ok := vals["language"]; ok && lang != "" {
				// Check if language instruction is already in sysPrompt (inherited from main Agent)
				if !strings.Contains(sysPrompt, "## Language") {
					sysPrompt += "\n" + LanguageInstruction(lang)
				}
			}
		}
	}

	messages := []llm.ChatMessage{
		llm.NewSystemMessage(sysPrompt),
		llm.NewUserMessage(task),
	}

	subAgentID := parentAgentID + "/" + roleName

	// SubAgent 继承父 Agent 的 LLM 配置（使用 OriginUserID 获取原始用户的配置）
	// 如果角色指定了模型，则通过 GetLLMForModel 智能查找对应的订阅
	var llmClient llm.LLM
	var subModel string
	var userMaxCtx int
	var thinkingMode string
	if model != "" {
		llmClient, subModel, userMaxCtx, thinkingMode, _ = a.llmFactory.GetLLMForModel(originUserID, model)
	} else {
		llmClient, subModel, userMaxCtx, thinkingMode = a.llmFactory.GetLLM(originUserID)
	}

	// Stream — 从用户设置继承（与 buildMainRunConfig 一致）
	stream := false
	if a.settingsSvc != nil {
		if vals, err := a.settingsSvc.GetSettings(parentCtx.Channel, originUserID); err == nil {
			if vals["enable_stream"] == "true" {
				stream = true
			}
		}
	}

	cfg := RunConfig{
		LLMClient:       llmClient,
		Model:           subModel,
		ThinkingMode:    thinkingMode,
		Stream:          stream,
		MaxOutputTokens: a.llmFactory.GetMaxOutputTokens(originUserID),
		Tools:           subTools,
		Messages:        messages,
		AgentID:         subAgentID,
		Channel:         parentCtx.Channel,
		ChatID:          parentCtx.ChatID,
		SenderID:        parentAgentID, // SubAgent: 直接调用者 = 父 Agent
		OriginUserID:    originUserID,  // SubAgent: 继承原始用户 ID

		// 从父 Agent 继承工作区 & 沙箱配置
		WorkingDir:       parentCtx.WorkingDir,
		WorkspaceRoot:    parentCtx.WorkspaceRoot,
		ReadOnlyRoots:    parentCtx.ReadOnlyRoots,
		SkillsDirs:       parentCtx.SkillsDirs,
		AgentsDir:        parentCtx.AgentsDir,
		MCPConfigPath:    parentCtx.MCPConfigPath,
		GlobalMCPConfig:  parentCtx.GlobalMCPConfigPath,
		DataDir:          parentCtx.DataDir,
		SandboxEnabled:   parentCtx.Sandbox != nil && parentCtx.Sandbox.Name() != "none",
		PreferredSandbox: parentCtx.PreferredSandbox,
		Sandbox:          parentCtx.Sandbox,
		SandboxMode: func() string {
			if parentCtx.Sandbox != nil {
				return parentCtx.Sandbox.Name()
			}
			return "none"
		}(),
		InitialCWD: parentCtx.CurrentDir, // 继承父 Agent 的 CWD

		MaxIterations: a.maxIterations, // 继承主 Agent 配置
		// SubAgent 不设独立超时，直接使用父 context 携带的 deadline

		// LLM 并发限流：继承父 Agent 的 per-tenant 信号量
		LLMSemAcquire: a.llmFactory.LLMSemAcquireForUser(originUserID),

		// ToolExecutor = nil → 使用 defaultToolExecutor（统一 buildToolContext）
	}

	// Per-user token usage tracking：SubAgent 的 token 消耗归属原始用户
	cfg.RecordUserTokenUsage = func(senderID, model string, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount int) {
		if err := a.multiSession.RecordUserTokenUsage(originUserID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"sender_id":    originUserID,
				"sub_agent_id": subAgentID,
			}).Warn("Failed to record SubAgent token usage")
		}
	}

	// 独立 sessionKey：使用 subAgentID 确保与父 Agent 隔离，
	// 避免工具激活、OffloadStore、MaskStore 等按 sessionKey 索引的数据污染。
	cfg.SessionKey = subAgentID

	// RootSessionKey：记录顶层 Agent（主 Agent）的 session key，
	// 用于 offload_recall 等需要访问父 session 数据的场景（如 SubAgent 回忆父 Agent 的 offload 数据）。
	rootKey := parentCtx.RootSessionKey
	if rootKey == "" {
		rootKey = parentCtx.Channel + ":" + parentCtx.ChatID
	}
	cfg.RootSessionKey = rootKey

	// === Context Mask 统一机制：注入 6 个缺失字段 ===
	// SubAgent 与主 Agent 共享同一 Run() 循环，context mask（offload/mask/context-edit）
	// 依赖这些字段才能正确触发。之前缺失导致 SubAgent 上下文压缩/遮罩永不生效。

	// 1. ContextManager：创建独立实例（不共享父 Agent 的触发器，避免计数交叉）
	//    从 caps.Memory 条件中移出，所有 SubAgent 都需要压缩能力。
	if a.contextManagerConfig != nil {
		cmCfg := applyUserMaxContext(a.contextManagerConfig, userMaxCtx)
		cfg.ContextManager = newPhase1Manager(cmCfg)
		cfg.ContextManagerConfig = cmCfg
	}

	// 2. OffloadStore：共享父 Agent 实例（按 sessionKey 隔离，完全安全）
	cfg.OffloadStore = a.offloadStore

	// 3. MaskStore：共享父 Agent 实例（通过随机 ID 查找，容量共享但 SubAgent 生命周期短影响可忽略）
	cfg.MaskStore = a.maskStore

	// 4. ContextEditor：创建独立实例（每个 Agent 需要自己的 messages 引用和编辑历史）
	cfg.ContextEditor = NewContextEditor(NewContextEditStore(100))

	// Capability: send_message — 允许 SubAgent 向 IM 渠道发送消息
	if caps.SendMessage {
		cfg.SendFunc = a.sendMessage
	}

	// Capability: memory — 创建独立记忆系统
	// SubAgent 的会话 = 与调用者 Agent 的私有聊天。调用者是 "user"，SubAgent 是 "xbot"。
	// 通过 deriveSubAgentTenantID 隔离：每个 (parentTenantID, parentAgentID, roleName) 组合
	// 产生唯一的 tenantID，确保 SubAgent 和父 Agent 读写完全不同的记忆数据。
	if caps.Memory {
		extras, mem := a.buildSubAgentMemory(ctx, parentCtx, parentExtras, parentAgentID, roleName)
		if extras != nil && mem != nil {
			cfg.ToolContextExtras = extras
			cfg.Memory = mem

			// 注入记忆使用指南到 system prompt
			messages[0].Content += subagentMemorySection

			// 注入记忆到 system prompt（SubAgent 不使用 pipeline，需手动调用 Recall）
			subSenderID := subAgentHumanBlockSenderID(parentAgentID)
			memCtx := letta.WithUserID(ctx, subSenderID)
			if recallText, err := mem.Recall(memCtx, task); err == nil && recallText != "" {
				messages[0].Content += "\n\n" + recallText
			}

		}
	} else {
		// 无 memory 能力时，移除记忆工具，避免 SubAgent 尝试调用后失败
		subTools.Unregister("core_memory_append")
		subTools.Unregister("core_memory_replace")
		subTools.Unregister("rethink")
		subTools.Unregister("archival_memory_insert")
		subTools.Unregister("archival_memory_search")
		subTools.Unregister("recall_memory_search")
	}

	// Capability: spawn_agent — 允许 SubAgent 创建子 Agent
	if caps.SpawnAgent {
		cfg.SpawnAgent = func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return a.spawnSubAgent(ctx, msg)
		}
	}
	// HookChain — SubAgent inherits parent Agent's hook chain
	cfg.HookChain = a.hookChain
	cfg.SettingsSvc = a.settingsSvc

	// Interactive 回调独立注入，不依赖 SpawnAgent
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, parentCtx.Channel, parentCtx.ChatID, instance, tail)
		},
	}

	return cfg
}

// buildToolExecutor 构建主 Agent 的工具执行器。
// 包含 session MCP 查找、激活检查、工具使用追踪等完整逻辑。
// 这是主 Agent 和 Cron 使用的执行器，SubAgent 使用 defaultToolExecutor。
func (a *Agent) buildToolExecutor(channel, chatID, senderID, senderName, sandboxUserID string) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	sessionKey := channel + ":" + chatID

	// Pre-build RunConfig outside closure to avoid reallocating on every tool call.
	// Only ctx (from the caller) changes per-call; all config fields are stable.
	wsRoot := a.workspaceRoot(sandboxUserID)
	isRemote := a.isRemoteUser(sandboxUserID)
	// For remote users, leave WorkspaceRoot/WorkingDir empty — the runner
	// manages its own filesystem. Keep SkillsDirs/AgentsDir as host paths
	// for server-side sync (EnsureSynced reads global skills from host).
	var workspaceRoot, workingDir string
	if !isRemote {
		workspaceRoot = wsRoot
		workingDir = a.workDir
	}
	cfg := &RunConfig{
		AgentID:      "main",
		Channel:      channel,
		ChatID:       chatID,
		SenderID:     senderID,      // 主 Agent: 直接调用者（用于消息路由）
		OriginUserID: sandboxUserID, // 沙箱/工作区用户（飞书身份登录 web 时为飞书 ou_xxx）
		SenderName:   senderName,
		SendFunc:     a.sendMessage,

		WorkingDir:       workingDir,
		WorkspaceRoot:    workspaceRoot,
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    tools.UserMCPConfigPath(a.workDir, sandboxUserID),
		GlobalMCPConfig:  filepath.Join(a.xbotHome, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   a.sandboxMode != "none",
		PreferredSandbox: a.sandboxMode,
		Sandbox:          resolveSandbox(a.sandbox, sandboxUserID),
		SandboxMode:      a.sandboxMode,
		InjectInbound:    a.injectInbound,
		Tools:            a.tools,
		BgTaskManager:    a.bgTaskMgr,
	}

	cfg.SpawnAgent = func(spawnCtx context.Context, inMsg bus.InboundMessage) (*bus.OutboundMessage, error) {
		return a.spawnSubAgent(spawnCtx, inMsg)
	}

	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName, instance string) error {
			return a.UnloadInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InterruptFn: func(ctx context.Context, roleName, instance string) error {
			return a.InterruptInteractiveSession(ctx, roleName, channel, chatID, instance)
		},
		InspectFn: func(ctx context.Context, roleName, instance string, tail int) (string, error) {
			return a.InspectInteractiveSession(ctx, roleName, channel, chatID, instance, tail)
		},
	}

	// Pre-build Letta memory extras (involves GetOrCreateSession + LettaMemory lookup).
	cfg.ToolContextExtras = a.buildToolContextExtras(channel, chatID)

	// Inherit hook chain from Agent.
	cfg.HookChain = a.hookChain
	cfg.SettingsSvc = a.settingsSvc

	return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		// Lazy-inject session so buildToolContext can persist CWD across tool calls.
		// Without this, Cd stores CWD in a ToolContext that is discarded on next call.
		if cfg.Session == nil {
			if sess, err := a.multiSession.GetOrCreateSession(channel, chatID); err == nil {
				cfg.Session = sess
			}
		}

		// 1. 工具查找：session MCP 优先，然后全局注册表
		var tool tools.Tool
		ok := false

		if mcpMgr := a.multiSession.GetSessionMCPManager(sessionKey); mcpMgr != nil {
			for _, st := range mcpMgr.GetSessionTools() {
				if st.Name() == tc.Name {
					tool = st
					ok = true
					break
				}
			}
		}
		if !ok {
			tool, ok = a.tools.Get(tc.Name)
		}
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tc.Name)
		}

		// 2. 激活检查：未激活的工具返回提示
		if !a.tools.IsToolActive(sessionKey, tc.Name) {
			return &tools.ToolResult{
				Summary: fmt.Sprintf("Tool %q is not loaded yet. Call load_tools(tools=%q) first to load it before use.", tc.Name, tc.Name),
			}, nil
		}

		// 3. 刷新工具最后使用 round，延长激活有效期
		a.tools.TouchTool(sessionKey, tc.Name)

		// 4. 确保用户工作目录存在（remote 模式跳过，runner 自行管理文件系统）
		if !a.isRemoteUser(senderID) {
			if err := os.MkdirAll(wsRoot, 0o755); err != nil {
				return nil, fmt.Errorf("create user workspace: %w", err)
			}
		}

		toolExecCtx := withApprovalTarget(ctx, cfg.ChatID, cfg.OriginUserID)
		if cfg.SettingsSvc != nil {
			permUsers := cfg.SettingsSvc.GetPermUsers(cfg.Channel, cfg.OriginUserID)
			if permUsers != nil {
				toolExecCtx = tools.WithPermUsers(toolExecCtx, permUsers.DefaultUser, permUsers.PrivilegedUser)
			}
		}

		// 5. Run pre-tool hooks (inject perm users into context for ApprovalHook)
		if cfg.HookChain != nil {
			hookCtx := toolExecCtx
			if err := cfg.HookChain.RunPre(hookCtx, tc.Name, tc.Arguments); err != nil {
				return nil, fmt.Errorf("pre-tool hook blocked %q: %w", tc.Name, err)
			}
		}

		// 6. 构建 ToolContext（统一路径，只有 ctx 变化）
		toolCtx := buildToolContext(toolExecCtx, cfg)

		// 7. Execute tool with timing
		start := time.Now()
		result, err := tool.Execute(toolCtx, tc.Arguments)
		elapsed := time.Since(start)

		// 8. Run post-tool hooks (always, even on error)
		if cfg.HookChain != nil {
			cfg.HookChain.RunPost(ctx, tc.Name, tc.Arguments, result, err, elapsed)
		}

		return result, err
	}
}

// buildOAuthHandler 构建 OAuth 自动触发处理器。
func (a *Agent) buildOAuthHandler(channel, chatID, senderID, sessionKey string) func(ctx context.Context, tc llm.ToolCall, execErr error) (string, bool) {
	return func(ctx context.Context, tc llm.ToolCall, execErr error) (string, bool) {
		if !oauth.IsTokenNeededError(execErr) {
			return "", false
		}

		// 已触发过则跳过，避免重复 OAuth 状态
		if _, sent := a.sessionFinalSent.Load(sessionKey); sent {
			log.Ctx(ctx).WithFields(log.Fields{
				"tool":   tc.Name,
				"reason": "sessionFinalSent already set, skipping duplicate oauth_authorize",
			}).Info("Skip duplicate OAuth auto-trigger")
			return "OAuth authorization already in progress.", true
		}

		log.Ctx(ctx).WithFields(log.Fields{
			"tool": tc.Name,
		}).Info("OAuth token needed, auto-triggering oauth_authorize tool")

		oauthTool, ok := a.tools.Get("oauth_authorize")
		if !ok {
			return "OAuth authorization required but oauth_authorize tool not found. Please enable OAuth in configuration.", true
		}

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
			a.sessionFinalSent.Store(sessionKey, true)
			return oauthResult.Summary, true
		}

		log.Ctx(ctx).WithError(oauthErr).Error("Failed to execute oauth_authorize tool")
		return "OAuth authorization required. Please configure OAUTH_ENABLE=true and OAUTH_BASE_URL in your environment.", true
	}
}

// buildMemoryToolSetup returns tool definitions and executor for memory tools during compaction.
// Uses the real tool registry instead of hand-written execution logic,
// ensuring tool behavior stays in sync with the main agent loop.
// Returns (nil, nil) if memory tools are not available.
func (a *Agent) buildMemoryToolSetup(channel, chatID string) ([]llm.ToolDefinition, func(ctx context.Context, tc llm.ToolCall) (string, error)) {
	extras := a.buildToolContextExtras(channel, chatID)
	if extras == nil || extras.CoreMemory == nil {
		return nil, nil
	}

	memToolNames := []string{
		"core_memory_append", "core_memory_replace", "rethink",
		"archival_memory_insert", "archival_memory_search",
	}
	var defs []llm.ToolDefinition
	for _, name := range memToolNames {
		if t, ok := a.tools.Get(name); ok {
			defs = append(defs, t)
		}
	}
	if len(defs) == 0 {
		return nil, nil
	}

	// Minimal RunConfig for building ToolContext — memory tools only need ToolContextExtras.
	memCfg := &RunConfig{
		Channel:           channel,
		ChatID:            chatID,
		ToolContextExtras: extras,
	}

	exec := func(ctx context.Context, tc llm.ToolCall) (string, error) {
		tool, ok := a.tools.Get(tc.Name)
		if !ok {
			return "Unknown tool: " + tc.Name, nil
		}
		toolCtx := buildToolContext(ctx, memCfg)
		result, err := tool.Execute(toolCtx, tc.Arguments)
		if err != nil {
			return fmt.Sprintf("Error: %v", err), nil
		}
		return result.Summary, nil
	}

	return defs, exec
}

// buildToolContextExtras 构建 Letta 记忆相关的 ToolContext 扩展字段。
// 通用字段（InjectInbound、Registry）已迁移到 RunConfig，此处仅处理 Letta memory。
func (a *Agent) buildToolContextExtras(channel, chatID string) *ToolContextExtras {
	extras := &ToolContextExtras{
		InvalidateAllSessionMCP: func() { a.multiSession.InvalidateAll() },
	}

	// Wire Letta memory fields if the session uses LettaMemory
	ts, err := a.multiSession.GetOrCreateSession(channel, chatID)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"channel": channel,
			"chat_id": chatID,
		}).Warn("buildToolContextExtras: GetOrCreateSession failed, Letta memory fields will be empty")
	} else {
		if lm, ok := ts.Memory().(*letta.LettaMemory); ok {
			extras.TenantID = lm.TenantID()
			extras.CoreMemory = lm.CoreService()
			extras.ArchivalMemory = lm.ArchivalService()
			extras.MemorySvc = lm.MemoryService()
			extras.RecallTimeRange = a.multiSession.RecallTimeRangeFunc()
			extras.ToolIndexer = lm
		}
	}

	return extras
}

// buildSubAgentMemory 为 SubAgent 构建独立的记忆系统。
//
// 核心设计：SubAgent 的会话 = 与调用者 Agent 的私有聊天。
// 调用者是 "user"，SubAgent 是 "xbot"。这保持了高度一致的 agent 逻辑抽象。
//
// 隔离策略：
//   - tenantID: 通过 deriveSubAgentTenantID(parentTenantID, parentAgentID, roleName) 生成
//   - persona: 完全独立（SubAgent 自己的身份，不从父级继承）
//   - human: 通过 parentAgentID 隔离（记录调用者 agent 的特征，而非原始终端用户）
//   - archival memory / working_context: 通过 tenantID 自动隔离
//
// 返回 (ToolContextExtras, MemoryProvider)。如果创建失败，返回 nil, nil 并记录警告。
func (a *Agent) buildSubAgentMemory(
	ctx context.Context,
	parentCtx *tools.ToolContext,
	parentExtras *ToolContextExtras,
	parentAgentID, roleName string,
) (*ToolContextExtras, memory.MemoryProvider) {
	// 1. 获取父 Agent 的 tenantID（用于推导 SubAgent 的 tenantID）
	if parentExtras.TenantID == 0 {
		log.Ctx(ctx).WithField("parent", parentAgentID).Warn("SubAgent memory: parent tenantID is 0, skipping memory setup")
		return nil, nil
	}

	// 2. 推导 SubAgent 的独立 tenantID
	subTenantID := deriveSubAgentTenantID(parentExtras.TenantID, parentAgentID, roleName)

	// 3. 获取共享服务（通过 multiSession 访问）
	coreSvc := a.multiSession.CoreMemoryService()
	archivalSvc := a.multiSession.ArchivalService()
	memorySvc := a.multiSession.MemoryService()

	// 4. 初始化 SubAgent 的 core memory blocks（persona + human）
	//    persona: 空的，由 SubAgent 通过 memorize 自行积累（不预填 systemPrompt，避免重复注入）
	//    human: 以 parentAgentID 为 senderID 隔离
	subSenderID := subAgentHumanBlockSenderID(parentAgentID)
	if err := coreSvc.InitBlocks(subTenantID, subSenderID); err != nil {
		log.Ctx(ctx).WithError(err).WithFields(log.Fields{
			"tenant_id":     subTenantID,
			"parent_agent":  parentAgentID,
			"role":          roleName,
			"sub_sender_id": subSenderID,
		}).Warn("SubAgent memory: failed to init core blocks")
		return nil, nil
	}

	// 5. 创建独立的 LettaMemory 实例
	toolIndexSvc := a.multiSession.ToolIndexService()
	mem := letta.New(subTenantID, coreSvc, archivalSvc, memorySvc, toolIndexSvc)

	// 6. 构建 ToolContextExtras（供 SubAgent 的工具使用）
	extras := &ToolContextExtras{
		TenantID:                subTenantID,
		CoreMemory:              coreSvc,
		ArchivalMemory:          archivalSvc,
		MemorySvc:               memorySvc,
		RecallTimeRange:         a.multiSession.RecallTimeRangeFunc(),
		ToolIndexer:             mem,
		InvalidateAllSessionMCP: func() { a.multiSession.InvalidateAll() },
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"sub_tenant_id": subTenantID,
		"parent_agent":  parentAgentID,
		"role":          roleName,
		"sub_sender_id": subSenderID,
	}).Info("SubAgent memory: created independent memory system")

	return extras, mem
}

// subAgentHumanBlockSenderID returns the virtual senderID used for the SubAgent's
// human block. This isolates SubAgent's human block from the parent's by using
// parentAgentID as the key, so each SubAgent role sees a different "user".
func subAgentHumanBlockSenderID(parentAgentID string) string {
	return "agent:" + parentAgentID
}

// consolidateSubAgentMemory runs a lightweight memorize pass after SubAgent exits.
// It extracts key information from the SubAgent's conversation messages and
// persists them to the SubAgent's independent memory via Memorize().
func (a *Agent) consolidateSubAgentMemory(
	ctx context.Context,
	cfg RunConfig,
	messages []llm.ChatMessage,
	task string,
	roleName string,
	parentAgentID string,
) {
	mem := cfg.Memory
	extras := cfg.ToolContextExtras
	if mem == nil || extras == nil {
		return
	}

	// Build memorize input with all conversation messages and LLM client
	memInput := memory.MemorizeInput{
		Messages:  messages,
		LLMClient: cfg.LLMClient,
		Model:     cfg.Model,
	}

	// Call Memorize with the SubAgent's virtual senderID context
	subSenderID := subAgentHumanBlockSenderID(parentAgentID)
	memCtx := letta.WithUserID(ctx, subSenderID)

	if _, err := mem.Memorize(memCtx, memInput); err != nil {
		log.Ctx(ctx).WithError(err).WithFields(log.Fields{
			"role":      roleName,
			"tenant_id": extras.TenantID,
		}).Warn("SubAgent memory consolidation failed")
	}
}

// spawnSubAgent 通过 Run() 创建并运行 SubAgent。
// 这是 SpawnAgent 回调的实现，将 InboundMessage 转换为 RunConfig 并调用 Run()。
func (a *Agent) spawnSubAgent(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	parentAgentID := msg.ParentAgentID
	task := msg.Content
	systemPrompt := msg.SystemPrompt
	allowedTools := msg.AllowedTools
	roleName := msg.RoleName

	// --- CallChain 深度 & 循环检查 ---
	cc := CallChainFromContext(ctx)
	if roleName != "" {
		if err := cc.CanSpawn(roleName, a.maxSubAgentDepth); err != nil {
			log.Ctx(ctx).WithFields(log.Fields{
				"parent": parentAgentID,
				"role":   roleName,
				"chain":  cc.Chain,
			}).Warn("SubAgent spawn blocked by CallChain")
			return &bus.OutboundMessage{
				Channel: "",
				ChatID:  "",
				Content: err.Error(),
				Error:   err,
			}, nil
		}
	}

	// 构建 parentCtx（从 InboundMessage 恢复）
	originChannel, originChatID, originSender := resolveOriginIDs(msg)
	parentCtx := a.buildParentToolContext(ctx, originChannel, originChatID, originSender, msg)

	log.Ctx(ctx).WithFields(log.Fields{
		"parent": parentAgentID,
		"role":   roleName,
		"task":   tools.Truncate(task, 80),
	}).Info("SubAgent started (via Run)")

	// 从 InboundMessage 恢复 capabilities
	caps := tools.CapabilitiesFromMap(msg.Capabilities)

	// 从 InboundMessage 元数据中获取角色指定的模型
	subModel := ""
	if msg.Metadata != nil {
		subModel = msg.Metadata["model"]
	}

	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, task, systemPrompt, allowedTools, caps, roleName, false, subModel)

	// SubAgent 进度上报：优先使用父 Agent 注入的回调（避免并发 SubAgent 互相覆盖 patch），
	// 否则 fallback 到直接发送消息（非并行场景）。
	// 进度穿透：子 Agent 的 ProgressNotifier 不仅上报自身进度，还注入回调到 subCtx
	// 让更深层 SubAgent 也能递归穿透进度到最顶层。
	if cb, ok := SubAgentProgressFromContext(ctx); ok {
		rn := roleName
		myDepth := cc.Depth() + 1
		myPath := cc.Spawn(rn).Chain
		cfg.ProgressNotifier = func(lines []string) {
			if len(lines) > 0 {
				cb(SubAgentProgressDetail{
					Path:  myPath,
					Lines: lines,
					Depth: myDepth,
				})
			}
		}
	} else if originChannel != "" && originChatID != "" {
		rn := roleName // 闭包捕获
		cfg.ProgressNotifier = func(lines []string) {
			if len(lines) > 0 {
				last := lines[len(lines)-1]
				if idx := strings.LastIndex(last, "\n"); idx >= 0 {
					last = last[idx+1:]
				}
				prefixed := "📋 subagent: [" + rn + "] " + last + "\n"
				_ = a.sendMessage(originChannel, originChatID, prefixed)
			}
		}
	}

	// 传递 CallChain 给子 Agent
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	// 注入穿透回调到 subCtx，让子 Agent 的 execOne 能获取并递归上报进度到父 Agent
	if cb, ok := SubAgentProgressFromContext(ctx); ok {
		myDepth := cc.Depth() + 1
		myPath := cc.Spawn(roleName).Chain
		subCtx = WithSubAgentProgress(subCtx, func(detail SubAgentProgressDetail) {
			detail.Depth = myDepth + detail.Depth
			if len(detail.Path) == 0 {
				detail.Path = myPath
			}
			cb(detail)
		})
	}

	// Register one-shot subagent in interactiveSubAgents so it's visible
	// in the task & agents panel. Removed immediately after Run completes.
	oneshotInstance := fmt.Sprintf("oneshot-%s-%d", roleName, time.Now().UnixNano())
	oneshotKey := interactiveKey(originChannel, originChatID, roleName, oneshotInstance)
	oneshotIA := &interactiveAgent{
		roleName:   roleName,
		instance:   oneshotInstance,
		lastUsed:   time.Now(),
		running:    true,
		background: false,
		task:       task,
	}
	a.interactiveSubAgents.Store(oneshotKey, oneshotIA)

	out := Run(subCtx, cfg)

	// Populate iteration history so inspect can show results after completion
	oneshotIA.running = false
	oneshotIA.lastReply = out.Content
	if len(out.IterationHistory) > 0 {
		oneshotIA.iterationHistory = append(oneshotIA.iterationHistory, out.IterationHistory...)
	}
	// Don't delete — let inactivity cleanup remove it so user can inspect results

	log.Ctx(ctx).WithFields(log.Fields{
		"parent":    parentAgentID,
		"role":      roleName,
		"tools":     out.ToolsUsed,
		"has_error": out.Error != nil,
	}).Info("SubAgent completed (via Run)")

	// BUG FIX: 当 SubAgent 遇到错误时，确保错误信息在 Content 中可见。
	// spawnSubAgent 返回 (out.OutboundMessage, nil)，Go error 始终为 nil。
	// 虽然 adapter 会检查 OutboundMessage.Error 并传播，但为了确保主 Agent LLM
	// 能清晰识别 SubAgent 的异常状态，在 Content 中也附加错误标注。
	if out.Error != nil {
		content := out.Content
		if content == "" {
			content = "⚠️ SubAgent 执行失败，未产生任何输出。"
		}
		content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
		out.Content = content
	}

	// SubAgent 记忆整合：将本次对话的关键信息写入 SubAgent 的独立记忆
	// 同步执行，确保记忆写入完成后再返回，避免 session 被 unload 导致记忆丢失。
	if cfg.Memory != nil && len(out.Messages) > 0 {
		a.consolidateSubAgentMemory(ctx, cfg, out.Messages, task, roleName, parentAgentID)
	}

	return out.OutboundMessage, nil
}

// convertWsSubAgentTree 将 agent.SubAgentNode 转换为 channelpkg.WsSubAgent 树。
func convertWsSubAgentTree(nodes []SubAgentNode) []channelpkg.WsSubAgent {
	if len(nodes) == 0 {
		return nil
	}
	result := make([]channelpkg.WsSubAgent, len(nodes))
	for i, n := range nodes {
		result[i] = channelpkg.WsSubAgent{
			Role:     n.Role,
			Status:   n.Status,
			Desc:     n.Desc,
			Children: convertWsSubAgentTree(n.Children),
		}
	}
	return result
}

// convertCLISubAgentTree 将 agent.SubAgentNode 转换为 channelpkg.CLISubAgent 树。
func convertCLISubAgentTree(nodes []SubAgentNode) []channelpkg.CLISubAgent {
	if len(nodes) == 0 {
		return nil
	}
	result := make([]channelpkg.CLISubAgent, len(nodes))
	for i, n := range nodes {
		result[i] = channelpkg.CLISubAgent{
			Role:     n.Role,
			Status:   n.Status,
			Desc:     n.Desc,
			Children: convertCLISubAgentTree(n.Children),
		}
	}
	return result
}
