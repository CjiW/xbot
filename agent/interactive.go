package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/tools"
)

// interactiveAgent 封装一个 interactive SubAgent 会话。
// 存储在 parent Agent 的 interactiveSubAgents map 中。
type interactiveAgent struct {
	roleName string            // 角色名
	messages []llm.ChatMessage // 累积的对话历史（不含 system prompt）
	mu       sync.Mutex        // 保护 messages 并发访问
	cfg      RunConfig         // SubAgent 的 RunConfig（保留 LLM/Memory 等配置，用于后续 send/unload）
}

// interactiveKey 生成 interactive session 在 map 中的 key。
// 使用 channel:chatID/roleName 保证同一个 chat + role 只有一个 session。
func interactiveKey(channel, chatID, roleName string) string {
	return channel + ":" + chatID + "/" + roleName
}

// SpawnInteractiveSession 创建一个新的 interactive SubAgent 会话并执行首次任务。
// 如果同名 role 的 session 已存在，返回 error。
func (a *Agent) SpawnInteractiveSession(
	ctx context.Context,
	roleName string,
	msg bus.InboundMessage,
) (*bus.OutboundMessage, error) {
	originChannel, originChatID, originSender := resolveOriginIDs(msg)

	key := interactiveKey(originChannel, originChatID, roleName)

	// 检查是否已存在
	if _, loaded := a.interactiveSubAgents.LoadOrStore(key, (*interactiveAgent)(nil)); loaded {
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("interactive session for role %q already exists, use action=\"send\" to continue or action=\"unload\" to end it", roleName),
		}, nil
	}
	a.interactiveSubAgents.Delete(key)

	// 构建 parentCtx
	parentCtx := a.buildParentToolContext(ctx, originChannel, originChatID, originSender, msg)

	// CallChain 检查
	cc := CallChainFromContext(ctx)
	if err := cc.CanSpawn(roleName); err != nil {
		return &bus.OutboundMessage{Content: err.Error(), Error: err}, nil
	}
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	// 构建 SubAgent RunConfig
	caps := tools.CapabilitiesFromMap(msg.Capabilities)
	cfg := a.buildSubAgentRunConfig(subCtx, parentCtx, msg.Content, msg.SystemPrompt, msg.AllowedTools, caps, roleName)

	// 记录 spawn 前的消息数量，用于提取本次任务的对话
	preLen := len(cfg.Messages)

	// 执行
	out := Run(subCtx, cfg)
	if out.Error != nil {
		a.interactiveSubAgents.Delete(key)
		return out.OutboundMessage, nil
	}

	// 创建 interactiveAgent 并保存（保存 system 之后的所有消息）
	ia := &interactiveAgent{
		roleName: roleName,
		messages: append([]llm.ChatMessage(nil), out.Messages[preLen:]...),
		cfg:      cfg,
	}
	a.interactiveSubAgents.Store(key, ia)

	log.WithFields(log.Fields{
		"role":     roleName,
		"messages": len(ia.messages),
	}).Info("Interactive session spawned")

	return out.OutboundMessage, nil
}

// SendToInteractiveSession 向已有的 interactive session 发送新消息。
func (a *Agent) SendToInteractiveSession(
	ctx context.Context,
	roleName string,
	msg bus.InboundMessage,
) (*bus.OutboundMessage, error) {
	originChannel, originChatID, originSender := resolveOriginIDs(msg)

	key := interactiveKey(originChannel, originChatID, roleName)

	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("no active interactive session for role %q, use interactive=true to create one first", roleName),
		}, nil
	}

	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		a.interactiveSubAgents.Delete(key)
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("corrupted interactive session for role %q", roleName),
		}, nil
	}

	ia.mu.Lock()
	defer ia.mu.Unlock()

	// 构建 parentCtx
	parentCtx := a.buildParentToolContext(ctx, originChannel, originChatID, originSender, msg)

	// 重新构建 RunConfig（获取最新 LLM 配置等）
	caps := tools.CapabilitiesFromMap(msg.Capabilities)
	cfg := a.buildSubAgentRunConfig(ctx, parentCtx, msg.Content, msg.SystemPrompt, msg.AllowedTools, caps, roleName)

	// 把历史消息插入到 system prompt 之后、新 task 之前
	// cfg.Messages 的结构: [system_prompt, user_task]
	var newMessages []llm.ChatMessage
	newMessages = append(newMessages, cfg.Messages[0]) // system prompt
	newMessages = append(newMessages, ia.messages...)  // 历史对话
	if len(cfg.Messages) > 1 {
		newMessages = append(newMessages, cfg.Messages[1:]...) // 新的 user task
	}
	cfg.Messages = newMessages

	// 传递 CallChain
	cc := CallChainFromContext(ctx)
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	// 记录新增消息的起点
	preLen := len(cfg.Messages)

	// 执行
	out := Run(subCtx, cfg)
	if out.Error != nil {
		return out.OutboundMessage, nil
	}

	// 追加新的对话消息
	ia.messages = append(ia.messages, out.Messages[preLen:]...)
	ia.cfg = cfg

	log.WithFields(log.Fields{
		"role":       roleName,
		"new_msgs":   len(out.Messages[preLen:]),
		"total_msgs": len(ia.messages),
	}).Info("Interactive session: sent message")

	return out.OutboundMessage, nil
}

// UnloadInteractiveSession 结束 interactive session：巩固记忆并清理。
func (a *Agent) UnloadInteractiveSession(
	ctx context.Context,
	roleName string,
	channel, chatID string,
) error {
	key := interactiveKey(channel, chatID, roleName)

	val, ok := a.interactiveSubAgents.Load(key)
	if !ok {
		return fmt.Errorf("no active interactive session for role %q", roleName)
	}

	ia, ok := val.(*interactiveAgent)
	if !ok || ia == nil {
		a.interactiveSubAgents.Delete(key)
		return nil
	}

	ia.mu.Lock()
	messages := make([]llm.ChatMessage, len(ia.messages))
	copy(messages, ia.messages)
	cfg := ia.cfg
	ia.mu.Unlock()

	// 巩固记忆
	if cfg.Memory != nil && len(messages) > 0 {
		a.consolidateSubAgentMemory(ctx, cfg, messages, "interactive session cleanup", roleName, cfg.AgentID)
	}

	// 清理
	a.interactiveSubAgents.Delete(key)

	log.WithField("role", roleName).Info("Interactive session unloaded")
	return nil
}

// buildParentToolContext 从 InboundMessage 构建 SubAgent 需要的 parent ToolContext。
// 与 spawnSubAgent 中的 parentCtx 构建保持一致。
func (a *Agent) buildParentToolContext(ctx context.Context, channel, chatID, senderID string, msg bus.InboundMessage) *tools.ToolContext {
	workspaceRoot := tools.UserWorkspaceRoot(a.workDir, senderID)
	_ = os.MkdirAll(workspaceRoot, 0o755)

	return &tools.ToolContext{
		Ctx:                 ctx,
		WorkingDir:          a.workDir,
		WorkspaceRoot:       workspaceRoot,
		SandboxWorkDir:      "/workspace",
		ReadOnlyRoots:       a.globalSkillDirs,
		SkillsDirs:          a.globalSkillDirs,
		AgentsDir:           a.agentsDir,
		MCPConfigPath:       tools.UserMCPConfigPath(a.workDir, senderID),
		GlobalMCPConfigPath: resolveDataPath(a.workDir, "mcp.json"),
		DataDir:             a.workDir,
		SandboxEnabled:      true,
		PreferredSandbox:    "docker",
		AgentID:             msg.ParentAgentID,
		Channel:             channel,
		ChatID:              chatID,
		SenderID:            senderID,
		SenderName:          msg.SenderName,
	}
}

// GetActiveInteractiveRoles 返回当前 session 下所有活跃的 interactive SubAgent role 名。
func (a *Agent) GetActiveInteractiveRoles(channel, chatID string) []string {
	var roles []string
	prefix := channel + ":" + chatID + "/"
	a.interactiveSubAgents.Range(func(k, v interface{}) bool {
		key := k.(string)
		if strings.HasPrefix(key, prefix) {
			role := strings.TrimPrefix(key, prefix)
			if ia, ok := v.(*interactiveAgent); ok && ia != nil {
				roles = append(roles, role)
			}
		}
		return true
	})
	return roles
}

// CleanupInteractiveSessions 清理指定 session 下所有 interactive sessions。
func (a *Agent) CleanupInteractiveSessions(ctx context.Context, channel, chatID string) {
	roles := a.GetActiveInteractiveRoles(channel, chatID)
	for _, role := range roles {
		_ = a.UnloadInteractiveSession(ctx, role, channel, chatID)
	}
	if len(roles) > 0 {
		log.WithFields(log.Fields{
			"session": channel + ":" + chatID,
			"roles":   roles,
		}).Info("Cleaned up all interactive sessions")
	}
}

// resolveOriginIDs 从 InboundMessage 中提取 origin channel/chatID/senderID，
// 带有 fallback 到顶层字段的逻辑。
func resolveOriginIDs(msg bus.InboundMessage) (channel, chatID, sender string) {
	channel = msg.OriginChannel()
	chatID = msg.OriginChatID()
	sender = msg.OriginSenderID()
	if channel == "" {
		channel = msg.Channel
	}
	if chatID == "" {
		chatID = msg.ChatID
	}
	if sender == "" {
		sender = msg.SenderID
	}
	return
}
