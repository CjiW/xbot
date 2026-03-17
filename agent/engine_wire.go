package agent

import (
	"context"
	"fmt"
	"os"
	"time"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/memory/letta"
	"xbot/oauth"
	"xbot/session"
	"xbot/tools"
)

// buildMainRunConfig 为主 Agent 构建完整的 RunConfig。
// 从 processMessage / handleCardResponse 调用。
func (a *Agent) buildMainRunConfig(
	ctx context.Context,
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
		Messages:  messages,

		// 身份
		AgentID:    "main",
		Channel:    channel,
		ChatID:     chatID,
		SenderID:   senderID,
		SenderName: senderName,

		// 工作区 & 沙箱
		WorkingDir:       a.workDir,
		WorkspaceRoot:    tools.UserWorkspaceRoot(a.workDir, senderID),
		SandboxWorkDir:   "/workspace",
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    tools.UserMCPConfigPath(a.workDir, senderID),
		GlobalMCPConfig:  resolveDataPath(a.workDir, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   true,
		PreferredSandbox: "docker",

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

	llmClient, model, _, _ := a.llmFactory.GetLLM(senderID)

	return RunConfig{
		LLMClient:  llmClient,
		Model:      model,
		Tools:      a.tools,
		Messages:   messages,
		AgentID:    "main",
		Channel:    channel,
		ChatID:     chatID,
		SenderID:   senderID,
		SenderName: "",

		// 工作区 & 沙箱
		WorkingDir:       a.workDir,
		WorkspaceRoot:    tools.UserWorkspaceRoot(a.workDir, senderID),
		SandboxWorkDir:   "/workspace",
		ReadOnlyRoots:    a.globalSkillDirs,
		SkillsDirs:       a.globalSkillDirs,
		AgentsDir:        a.agentsDir,
		MCPConfigPath:    tools.UserMCPConfigPath(a.workDir, senderID),
		GlobalMCPConfig:  resolveDataPath(a.workDir, "mcp.json"),
		DataDir:          a.workDir,
		SandboxEnabled:   true,
		PreferredSandbox: "docker",

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

	// 构建子 Agent 的消息（注入工作目录信息）
	if parentCtx.SandboxEnabled && parentCtx.SandboxWorkDir != "" {
		systemPrompt += fmt.Sprintf("\n\nWorking directory: %s\n", parentCtx.SandboxWorkDir)
	} else if parentCtx.WorkspaceRoot != "" {
		systemPrompt += fmt.Sprintf("\n\nWorking directory: %s\n", parentCtx.WorkspaceRoot)
	}
	messages := []llm.ChatMessage{
		llm.NewSystemMessage(systemPrompt),
		llm.NewUserMessage(task),
	}

	subAgentID := parentAgentID + "/sub"

	// SubAgent 继承父 Agent 的 LLM 配置
	llmClient, model, _, _ := a.llmFactory.GetLLM(parentCtx.SenderID)

	cfg := RunConfig{
		LLMClient: llmClient,
		Model:     model,
		Tools:     subTools,
		Messages:  messages,
		AgentID:   subAgentID,
		Channel:   parentCtx.Channel,
		ChatID:    parentCtx.ChatID,
		SenderID:  parentCtx.SenderID,

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

	// Capability: memory — 注入 Letta 记忆系统
	if caps.Memory {
		cfg.ToolContextExtras = a.buildToolContextExtras(parentCtx.Channel, parentCtx.ChatID)
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
		wsRoot := tools.UserWorkspaceRoot(a.workDir, senderID)
		if err := os.MkdirAll(wsRoot, 0o755); err != nil {
			return nil, fmt.Errorf("create user workspace: %w", err)
		}

		// 5. 构建 ToolContext（统一路径）
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
			MCPConfigPath:    tools.UserMCPConfigPath(a.workDir, senderID),
			GlobalMCPConfig:  resolveDataPath(a.workDir, "mcp.json"),
			DataDir:          a.workDir,
			SandboxEnabled:   true,
			PreferredSandbox: "docker",

			InjectInbound: a.injectInbound,
			Tools:         a.tools,
		}

		// SpawnAgent
		cfg.SpawnAgent = func(spawnCtx context.Context, inMsg bus.InboundMessage) (*bus.OutboundMessage, error) {
			return a.spawnSubAgent(spawnCtx, inMsg)
		}

		// Letta 记忆
		cfg.ToolContextExtras = a.buildToolContextExtras(channel, chatID)

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

// spawnSubAgent 通过 Run() 创建并运行 SubAgent。
// 这是 SpawnAgent 回调的实现，将 InboundMessage 转换为 RunConfig 并调用 Run()。
func (a *Agent) spawnSubAgent(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	parentAgentID := msg.ParentAgentID
	task := msg.Content
	systemPrompt := msg.SystemPrompt
	allowedTools := msg.AllowedTools

	// 构建 parentCtx（从 InboundMessage 恢复）
	originChannel := msg.OriginChannel()
	originChatID := msg.OriginChatID()
	originSender := msg.OriginSenderID()
	if originChannel == "" {
		originChannel = msg.Channel
	}
	if originChatID == "" {
		originChatID = msg.ChatID
	}
	if originSender == "" {
		originSender = msg.SenderID
	}

	workspaceRoot := tools.UserWorkspaceRoot(a.workDir, originSender)
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to create sub-agent workspace")
	}

	parentCtx := &tools.ToolContext{
		Ctx:              ctx,
		WorkingDir:       a.workDir,
		WorkspaceRoot:    workspaceRoot,
		SandboxWorkDir:   "/workspace",
		ReadOnlyRoots:    a.globalSkillDirs,
		SandboxEnabled:   true,
		PreferredSandbox: "docker",
		AgentID:          parentAgentID,
		Channel:          originChannel,
		ChatID:           originChatID,
		SenderID:         originSender,
		SenderName:       msg.SenderName,
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"parent": parentAgentID,
		"task":   tools.Truncate(task, 80),
	}).Info("SubAgent started (via Run)")

	// 从 InboundMessage 恢复 capabilities
	caps := tools.CapabilitiesFromMap(msg.Capabilities)

	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, task, systemPrompt, allowedTools, caps)
	out := Run(ctx, cfg)

	log.Ctx(ctx).WithFields(log.Fields{
		"parent":    parentAgentID,
		"tools":     out.ToolsUsed,
		"has_error": out.Error != nil,
	}).Info("SubAgent completed (via Run)")

	return out, nil
}
