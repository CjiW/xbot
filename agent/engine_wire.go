package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
	"xbot/oauth"
	"xbot/session"
	"xbot/tools"
)

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

	// 获取用户特定的 LLM 客户端
	llmClient, model, userMaxCtx, thinkingMode := a.llmFactory.GetLLM(senderID)
	maxContextTokens := a.maxContextTokens
	if userMaxCtx > 0 {
		maxContextTokens = userMaxCtx
	}

	cfg := RunConfig{
		// 必需
		LLMClient:    llmClient,
		Model:        model,
		ThinkingMode: thinkingMode,
		Tools:        a.tools,
		Messages:     messages,

		// 身份
		AgentID:    "main",
		Channel:    channel,
		ChatID:     chatID,
		SenderID:   senderID,
		SenderName: senderName,

		// 工作区 & 沙箱
		WorkingDir:       a.workDir,
		WorkspaceRoot:    a.resolveWorkspaceRoot(senderID),
		SandboxWorkDir:   "/workspace",
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    a.resolveMCPConfigPath(senderID),
		GlobalMCPConfig:  resolveDataPath(a.workDir, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   true,
		PreferredSandbox: a.sandboxMode,

		// 循环控制
		MaxIterations: a.maxIterations,

		// Session
		Session:    tenantSession,
		SessionKey: sessionKey,

		// 发送
		SendFunc:      a.sendMessage,
		InjectInbound: a.injectInbound,

		// 工具执行
		ToolExecutor: a.buildToolExecutor(channel, chatID, senderID, senderName),
		ToolTimeout:  120 * time.Second,

		// 读写分离（主 Agent 始终启用）
		EnableReadWriteSplit: true,

		// SessionFinalSent 回调
		SessionFinalSentCallback: func() bool {
			_, sent := a.sessionFinalSent.Load(sessionKey)
			return sent
		},

		// OAuth 处理
		OAuthHandler: a.buildOAuthHandler(channel, chatID, senderID, sessionKey),

		// Letta 记忆字段
		ToolContextExtras: a.buildToolContextExtras(channel, chatID),
	}

	// 进度通知
	if autoNotify {
		cfg.ProgressNotifier = func(lines []string) {
			if len(lines) > 0 {
				_ = a.sendMessage(channel, chatID, lines[0])
			}
		}
	}

	// 自动压缩
	if a.enableAutoCompress {
		cfg.AutoCompress = &CompressConfig{
			MaxContextTokens:     maxContextTokens,
			CompressionThreshold: a.compressionThreshold,
			CompressFunc:         a.compressContext,
		}
	}

	// SpawnAgent（主 Agent 可以创建 SubAgent）
	cfg.SpawnAgent = func(ctx context.Context, inMsg bus.InboundMessage) (*bus.OutboundMessage, error) {
		return a.spawnSubAgent(ctx, inMsg)
	}

	// InteractiveCallbacks — interactive SubAgent 支持
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		SpawnFn: a.SpawnInteractiveSession,
		SendFn:  a.SendToInteractiveSession,
		UnloadFn: func(ctx context.Context, roleName string) error {
			return a.UnloadInteractiveSession(ctx, roleName, channel, chatID)
		},
	}

	return cfg
}

// buildCronRunConfig 为 Cron 消息构建 RunConfig。
// Cron 消息不需要自动压缩、进度通知、session 持久化。
func (a *Agent) buildCronRunConfig(
	ctx context.Context,
	msg bus.InboundMessage,
	messages []llm.ChatMessage,
) RunConfig {
	channel, chatID, senderID := msg.Channel, msg.ChatID, msg.SenderID
	sessionKey := channel + ":" + chatID

	llmClient, model, _, thinkingMode := a.llmFactory.GetLLM(senderID)

	return RunConfig{
		LLMClient:    llmClient,
		Model:        model,
		ThinkingMode: thinkingMode,
		Tools:        a.tools,
		Messages:     messages,
		AgentID:      "main",
		Channel:      channel,
		ChatID:       chatID,
		SenderID:     senderID,
		SenderName:   "",

		// 工作区 & 沙箱
		WorkingDir:       a.workDir,
		WorkspaceRoot:    a.resolveWorkspaceRoot(senderID),
		SandboxWorkDir:   "/workspace",
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    a.resolveMCPConfigPath(senderID),
		GlobalMCPConfig:  resolveDataPath(a.workDir, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   true,
		PreferredSandbox: a.sandboxMode,

		MaxIterations: a.maxIterations,
		SessionKey:    sessionKey,
		SendFunc:      a.sendMessage,
		InjectInbound: a.injectInbound,

		ToolExecutor:         a.buildToolExecutor(channel, chatID, senderID, ""),
		ToolTimeout:          120 * time.Second,
		EnableReadWriteSplit: true,

		SessionFinalSentCallback: func() bool {
			_, sent := a.sessionFinalSent.Load(sessionKey)
			return sent
		},

		ToolContextExtras: a.buildToolContextExtras(channel, chatID),
	}
}

// buildSubAgentRunConfig 为 SubAgent 构建 RunConfig。
// SubAgent 使用独立工具集、无 session、无压缩、无进度通知。
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

	// 构建 SubAgent 的 system prompt：通用模板 + 角色专有能力描述
	workDir := parentCtx.SandboxWorkDir
	if workDir == "" {
		workDir = parentCtx.WorkspaceRoot
	}
	now := time.Now().Format("2006-01-02 15:04:05 MST")

	// role.SystemPrompt 作为角色专有能力描述（非通用 prompt）
	rolePrompt := strings.TrimSpace(systemPrompt)
	if rolePrompt == "" {
		rolePrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// 通用模板 + 角色描述
	sysPrompt := fmt.Sprintf(subagentSystemPromptTemplate, workDir, roleName, now)
	sysPrompt += "\n## 角色描述\n\n" + rolePrompt + "\n"

	messages := []llm.ChatMessage{
		llm.NewSystemMessage(sysPrompt),
		llm.NewUserMessage(task),
	}

	subAgentID := parentAgentID + "/" + roleName

	// SubAgent 继承父 Agent 的 LLM 配置
	llmClient, model, _, thinkingMode := a.llmFactory.GetLLM(parentCtx.SenderID)

	cfg := RunConfig{
		LLMClient:    llmClient,
		Model:        model,
		ThinkingMode: thinkingMode,
		Tools:        subTools,
		Messages:     messages,
		AgentID:      subAgentID,
		Channel:      parentCtx.Channel,
		ChatID:       parentCtx.ChatID,
		SenderID:     parentCtx.SenderID,

		// 从父 Agent 继承工作区 & 沙箱配置
		WorkingDir:       parentCtx.WorkingDir,
		WorkspaceRoot:    parentCtx.WorkspaceRoot,
		SandboxWorkDir:   "/workspace",
		ReadOnlyRoots:    parentCtx.ReadOnlyRoots,
		SkillsDirs:       parentCtx.SkillsDirs,
		AgentsDir:        parentCtx.AgentsDir,
		MCPConfigPath:    parentCtx.MCPConfigPath,
		GlobalMCPConfig:  parentCtx.GlobalMCPConfigPath,
		DataDir:          parentCtx.DataDir,
		SandboxEnabled:   parentCtx.SandboxEnabled,
		PreferredSandbox: parentCtx.PreferredSandbox,

		MaxIterations: 100,
		LLMTimeout:    3 * time.Minute,
		ToolTimeout:   2 * time.Minute,

		// ToolExecutor = nil → 使用 defaultToolExecutor（统一 buildToolContext）
	}

	// Capability: send_message — 允许 SubAgent 向 IM 渠道发送消息
	if caps.SendMessage {
		cfg.SendFunc = a.sendMessage
	}

	// Capability: memory — 创建独立记忆系统
	// SubAgent 的会话 = 与调用者 Agent 的私有聊天。调用者是 "user"，SubAgent 是 "xbot"。
	// 通过 deriveSubAgentTenantID 隔离：每个 (parentTenantID, parentAgentID, roleName) 组合
	// 产生唯一的 tenantID，确保 SubAgent 和父 Agent 读写完全不同的记忆数据。
	if caps.Memory {
		extras, mem := a.buildSubAgentMemory(ctx, parentCtx, parentAgentID, roleName)
		if extras != nil && mem != nil {
			cfg.ToolContextExtras = extras
			cfg.Memory = mem

			// 注入记忆到 system prompt（SubAgent 不使用 pipeline，需手动调用 Recall）
			subSenderID := subAgentHumanBlockSenderID(parentAgentID)
			memCtx := letta.WithUserID(ctx, subSenderID)
			if recallText, err := mem.Recall(memCtx, task); err == nil && recallText != "" {
				messages[0].Content += "\n\n" + recallText
			}

			// 启用上下文压缩（与主 Agent 相同配置，但无进度通知）
			if a.enableAutoCompress {
				cfg.AutoCompress = &CompressConfig{
					MaxContextTokens:     a.maxContextTokens,
					CompressionThreshold: a.compressionThreshold,
					CompressFunc:         a.compressContext,
				}
			}
		}
	}

	// Capability: spawn_agent — 允许 SubAgent 创建子 Agent
	if caps.SpawnAgent {
		cfg.SpawnAgent = func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return a.spawnSubAgent(ctx, msg)
		}
	}

	return cfg
}

// buildToolExecutor 构建主 Agent 的工具执行器。
// 包含 session MCP 查找、激活检查、工具使用追踪等完整逻辑。
// 这是主 Agent 和 Cron 使用的执行器，SubAgent 使用 defaultToolExecutor。
func (a *Agent) buildToolExecutor(channel, chatID, senderID, senderName string) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	sessionKey := channel + ":" + chatID

	// Pre-build RunConfig outside closure to avoid reallocating on every tool call.
	// Only ctx (from the caller) changes per-call; all config fields are stable.
	wsRoot := a.resolveWorkspaceRoot(senderID)
	cfg := &RunConfig{
		AgentID:    "main",
		Channel:    channel,
		ChatID:     chatID,
		SenderID:   senderID,
		SenderName: senderName,
		SendFunc:   a.sendMessage,

		WorkingDir:       a.workDir,
		WorkspaceRoot:    wsRoot,
		SandboxWorkDir:   "/workspace",
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    a.resolveMCPConfigPath(senderID),
		GlobalMCPConfig:  resolveDataPath(a.workDir, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   true,
		PreferredSandbox: a.sandboxMode,

		InjectInbound: a.injectInbound,
		Tools:         a.tools,
	}

	cfg.SpawnAgent = func(spawnCtx context.Context, inMsg bus.InboundMessage) (*bus.OutboundMessage, error) {
		return a.spawnSubAgent(spawnCtx, inMsg)
	}

	// Pre-build Letta memory extras (involves GetOrCreateSession + LettaMemory lookup).
	cfg.ToolContextExtras = a.buildToolContextExtras(channel, chatID)

	return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
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

		// 4. 确保用户工作目录存在
		if err := os.MkdirAll(wsRoot, 0o755); err != nil {
			return nil, fmt.Errorf("create user workspace: %w", err)
		}

		// 5. 构建 ToolContext（统一路径，只有 ctx 变化）
		toolCtx := buildToolContext(ctx, cfg)
		return tool.Execute(toolCtx, tc.Arguments)
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

// buildToolContextExtras 构建 Letta 记忆相关的 ToolContext 扩展字段。
// 通用字段（InjectInbound、Registry）已迁移到 RunConfig，此处仅处理 Letta memory。
func (a *Agent) buildToolContextExtras(channel, chatID string) *ToolContextExtras {
	extras := &ToolContextExtras{
		InvalidateAllSessionMCP: func() { a.multiSession.InvalidateAll() },
	}

	// Wire Letta memory fields if the session uses LettaMemory
	if ts, err := a.multiSession.GetOrCreateSession(channel, chatID); err == nil {
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
	parentAgentID, roleName string,
) (*ToolContextExtras, memory.MemoryProvider) {
	// 1. 获取父 Agent 的 tenantID（用于推导 SubAgent 的 tenantID）
	parentExtras := a.buildToolContextExtras(parentCtx.Channel, parentCtx.ChatID)
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
		if err := cc.CanSpawn(roleName); err != nil {
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

	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, task, systemPrompt, allowedTools, caps, roleName)

	// 传递 CallChain 给子 Agent
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	out := Run(subCtx, cfg)

	log.Ctx(ctx).WithFields(log.Fields{
		"parent":    parentAgentID,
		"role":      roleName,
		"tools":     out.ToolsUsed,
		"has_error": out.Error != nil,
	}).Info("SubAgent completed (via Run)")

	// SubAgent 记忆整合：将本次对话的关键信息写入 SubAgent 的独立记忆
	// 异步执行，避免 Memorize() (调用 LLM 做摘要) 阻塞父 Agent 的工具执行循环。
	if cfg.Memory != nil && len(out.Messages) > 0 {
		memMessages := make([]llm.ChatMessage, len(out.Messages))
		copy(memMessages, out.Messages)
		go func() {
			bgCtx := context.WithoutCancel(ctx)
			a.consolidateSubAgentMemory(bgCtx, cfg, memMessages, task, roleName, parentAgentID)
		}()
	}

	return out.OutboundMessage, nil
}
