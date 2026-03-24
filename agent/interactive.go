package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/tools"
)

// interactiveAgent 封装一个 interactive SubAgent 会话。
// 存储在 parent Agent 的 interactiveSubAgents map 中。
type interactiveAgent struct {
	roleName     string            // 角色名
	messages     []llm.ChatMessage // 累积的对话历史（不含 system prompt）
	mu           sync.Mutex        // 保护 messages 并发访问
	systemPrompt llm.ChatMessage   // spawn 时的 system prompt（保持一致性，后续 send 不重建）
	cfg          *RunConfig        // RunConfig 模板（Messages=nil，复用于 send/unload）
	lastUsed     time.Time         // 最后访问时间，用于 TTL 清理
}

// interactiveSessionTTL 是 interactive SubAgent 会话的生存时间。
const interactiveSessionTTL = 30 * time.Minute

// cleanupExpiredSessions 清理所有过期的 interactive SubAgent 会话。
// sync.Map 本身并发安全，调用方不需要持有任何额外的锁。
func (a *Agent) cleanupExpiredSessions() {
	now := time.Now()
	a.interactiveSubAgents.Range(func(k, v interface{}) bool {
		ia, ok := v.(*interactiveAgent)
		if !ok || ia == nil {
			a.interactiveSubAgents.Delete(k)
			return true
		}
		// 读取 lastUsed 需要加锁，避免与 SendToInteractiveSession 的写入竞争
		ia.mu.Lock()
		lastUsed := ia.lastUsed
		ia.mu.Unlock()
		if now.Sub(lastUsed) > interactiveSessionTTL {
			key := k.(string)
			log.WithFields(log.Fields{
				"key":       key,
				"role":      ia.roleName,
				"idle_time": now.Sub(ia.lastUsed).String(),
			}).Info("Cleaning up expired interactive session")
			a.interactiveSubAgents.Delete(key)
		}
		return true
	})
}

// interactiveKey 生成 interactive session 在 map 中的 key。
// 使用 channel:chatID/roleName 保证同一个 chat + role 只有一个 session。
func interactiveKey(channel, chatID, roleName string) string {
	return channel + ":" + chatID + "/" + roleName
}

// SpawnInteractiveSession 创建一个新的 interactive SubAgent 会话并执行首次任务。
// 如果同名 role 的 session 已存在，返回 error。
//
// 锁策略：interactiveSubAgents 使用 sync.Map，本身并发安全，无需额外互斥锁。
// 使用 LoadOrStore 实现原子的 check-and-store，避免 spawn 竞态。
// 使用占位符模式：Store 一个最小占位符，Run() 完成后替换为完整数据。
// 任何错误路径都必须清理占位符，避免 session 卡死。
func (a *Agent) SpawnInteractiveSession(
	ctx context.Context,
	roleName string,
	msg bus.InboundMessage,
) (*bus.OutboundMessage, error) {
	originChannel, originChatID, originSender := resolveOriginIDs(msg)

	key := interactiveKey(originChannel, originChatID, roleName)

	// --- 阶段 1：原子 check-and-store ---
	// 先清理过期 session（sync.Map 并发安全，不需要额外锁）
	a.cleanupExpiredSessions()

	// 原子 check-and-store：如果 key 已存在，直接返回
	placeholder := &interactiveAgent{roleName: roleName, lastUsed: time.Now()}
	if _, loaded := a.interactiveSubAgents.LoadOrStore(key, placeholder); loaded {
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("interactive session for role %q already exists, use action=\"send\" to continue or action=\"unload\" to end it", roleName),
		}, nil
	}

	// --- 阶段 2：锁外构建 config（不需要锁） ---
	parentCtx := a.buildParentToolContext(ctx, originChannel, originChatID, originSender, msg)

	cc := CallChainFromContext(ctx)
	if err := cc.CanSpawn(roleName, a.maxSubAgentDepth); err != nil {
		a.interactiveSubAgents.Delete(key) // 清理占位符
		return &bus.OutboundMessage{Content: err.Error(), Error: err}, nil
	}
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	caps := tools.CapabilitiesFromMap(msg.Capabilities)
	cfg := a.buildSubAgentRunConfig(subCtx, parentCtx, msg.Content, msg.SystemPrompt, msg.AllowedTools, caps, roleName, true)

	// SubAgent 进度上报：优先使用父 Agent 注入的回调（避免并发 SubAgent 互相覆盖 patch），
	// 否则 fallback 到直接发送消息（非并行场景）。
	if cb, ok := SubAgentProgressFromContext(ctx); ok {
		rn := roleName
		cfg.ProgressNotifier = func(lines []string) {
			if len(lines) > 0 {
				// 只取最后一行，避免多行进度破坏父 Agent blockquote
				last := lines[len(lines)-1]
				if idx := strings.LastIndex(last, "\n"); idx >= 0 {
					last = last[idx+1:]
				}
				cb(rn, last)
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

	// --- 阶段 3：锁外执行 Run（嵌套 spawn 不会死锁） ---
	preLen := len(cfg.Messages)
	out := Run(subCtx, cfg)

	if out.Error != nil {
		a.interactiveSubAgents.Delete(key) // 清理占位符
		// BUG FIX: 在 Content 中附加错误标注，确保主 Agent LLM 能识别异常状态
		content := out.Content
		if content == "" {
			content = "⚠️ Interactive SubAgent 执行失败。"
		}
		content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
		out.Content = content
		return out.OutboundMessage, nil
	}

	// --- 阶段 4：替换占位符为完整 session 数据 ---
	var newMessages []llm.ChatMessage
	if len(out.Messages) > preLen {
		newMessages = append([]llm.ChatMessage(nil), out.Messages[preLen:]...)
	}

	ia := &interactiveAgent{
		roleName:     roleName,
		messages:     newMessages,
		systemPrompt: cfg.Messages[0],
		cfg:          &cfg,
		lastUsed:     time.Now(),
	}
	ia.cfg.Messages = nil // 避免与 ia.messages 重复（实际消息在 ia.messages 中）
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
	originChannel, originChatID, _ := resolveOriginIDs(msg)

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

	// 防护：占位符尚未被替换为完整数据（SpawnInteractiveSession 正在执行 Run()）。
	// 这在正常流程中不会发生（spawn 返回后才能 send），但防御性检查避免 panic。
	if ia.cfg == nil {
		return &bus.OutboundMessage{
			Content: fmt.Sprintf("interactive session for role %q is still initializing, please try again later", roleName),
		}, nil
	}

	// 更新最后访问时间
	ia.lastUsed = time.Now()

	// 复用存储的 RunConfig 模板，只更新 Messages 和刷新 LLM 配置。
	// 不重建工具集、记忆系统、system prompt 等，保持 session 一致性。
	// 注意：此处为浅拷贝，slice 字段（Messages, ReadOnlyRoots, SkillsDirs 等）
	// 与 ia.cfg 共享底层数组。当前安全因 mutex 保护且拷贝后仅做非 slice 字段覆盖，
	// 但如果需要修改 slice 内容，必须先深拷贝。
	cfg := *ia.cfg // copy
	// 使用 OriginUserID 获取 LLM 配置（SubAgent 应继承原始用户的配置）
	originUserID := cfg.OriginUserID
	if originUserID == "" {
		originUserID = cfg.SenderID // fallback：兼容旧数据
	}
	llmClient, model, _, thinkingMode := a.llmFactory.GetLLM(originUserID)
	cfg.LLMClient = llmClient
	cfg.Model = model
	cfg.ThinkingMode = thinkingMode

	// 重建消息：[system_prompt, 历史对话, 新的 user task]
	var newMessages []llm.ChatMessage
	newMessages = append(newMessages, ia.systemPrompt)                 // spawn 时的 system prompt
	newMessages = append(newMessages, ia.messages...)                  // 累积的对话历史
	newMessages = append(newMessages, llm.NewUserMessage(msg.Content)) // 新任务
	cfg.Messages = newMessages

	// 传递 CallChain
	cc := CallChainFromContext(ctx)
	subCtx := WithCallChain(ctx, cc.Spawn(roleName))

	// 记录新增消息的起点
	preLen := len(cfg.Messages)

	// 执行
	out := Run(subCtx, cfg)
	if out.Error != nil {
		// BUG FIX: 在 Content 中附加错误标注，确保主 Agent LLM 能识别异常状态
		content := out.Content
		if content == "" {
			content = "⚠️ Interactive SubAgent 执行失败。"
		}
		content += fmt.Sprintf("\n\n> ❌ SubAgent Error: %v", out.Error)
		out.Content = content
		return out.OutboundMessage, nil
	}

	// 追加新的对话消息到 ia.messages
	// 注意：Interactive SubAgent 的 cfg.Memory 为 nil，所以 out.Messages 可能为空。
	// 但 Run() 内部 messages 切片（局部变量）仍然包含了完整对话历史，
	// 不过 out.Messages 未被填充。我们需要从 Run() 的行为来推断新增消息。
	//
	// 策略：如果 out.Messages 非空，直接用差集；否则根据 Run 的行为手动构建。
	if len(out.Messages) > preLen {
		ia.messages = append(ia.messages, out.Messages[preLen:]...)
	} else {
		// out.Messages 为空（无 Memory），从 Run 的输出消息推断新增内容。
		// Run() 会向 messages append assistant + tool messages。
		// 我们需要重建：preLen 之前的消息已经在 ia.messages 中，
		// 新增的消息是 assistant reply 和所有 tool call 的结果。
		//
		// 安全做法：将 out.OutboundMessage.Content 转为 assistant message 追加。
		if out.Content != "" {
			ia.messages = append(ia.messages, llm.NewAssistantMessage(out.Content))
		}
	}

	log.WithFields(log.Fields{
		"role":       roleName,
		"new_msgs":   len(out.Messages) - preLen,
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
	// 防护：占位符尚未被替换为完整数据
	if ia.cfg == nil {
		ia.mu.Unlock()
		a.interactiveSubAgents.Delete(key)
		return nil
	}
	messages := make([]llm.ChatMessage, len(ia.messages))
	copy(messages, ia.messages)
	cfg := *ia.cfg // dereference pointer for consolidateSubAgentMemory
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
		SandboxWorkDir:      a.sandboxWorkDir(),
		ReadOnlyRoots:       a.globalSkillDirs,
		SkillsDirs:          a.globalSkillDirs,
		AgentsDir:           a.agentsDir,
		MCPConfigPath:       tools.UserMCPConfigPath(a.workDir, senderID),
		GlobalMCPConfigPath: resolveDataPath(a.workDir, "mcp.json"),
		DataDir:             a.workDir,
		SandboxEnabled:      a.sandboxMode == "docker",
		PreferredSandbox:    a.sandboxMode,
		AgentID:             msg.ParentAgentID,
		Channel:             channel,
		ChatID:              chatID,
		SenderID:            msg.ParentAgentID, // SubAgent 的父上下文：SenderID = 父 Agent ID
		OriginUserID:        senderID,          // 原始用户 ID
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
