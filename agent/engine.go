package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	"xbot/llm"
	"xbot/memory"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
	"xbot/tools"

	log "xbot/logger"
)

// RunConfig 统一的 Agent 运行配置。
// 主 Agent 和 SubAgent 使用同一个 Run() 方法，差异通过配置注入。
type RunConfig struct {
	// === 必需 ===
	LLMClient llm.LLM
	Model     string
	Tools     *tools.Registry
	Messages  []llm.ChatMessage

	// === 身份（从 InboundMessage 提取） ===
	AgentID    string // "main", "main/code-reviewer"
	Channel    string // 原始 IM 渠道（用于 ToolContext）
	ChatID     string // 原始 IM 会话
	SenderID   string // 原始发送者
	SenderName string

	// === 工作区 & 沙箱 ===
	WorkingDir       string   // Agent 工作目录（宿主机）
	WorkspaceRoot    string   // 用户可读写工作区根目录（宿主机路径）
	SandboxWorkDir   string   // 沙箱内工作目录（如 /workspace）
	ReadOnlyRoots    []string // 额外只读目录
	SkillsDirs       []string // 全局 skill 目录列表
	AgentsDir        string   // 全局 agents 目录
	MCPConfigPath    string   // 用户 MCP 配置路径
	GlobalMCPConfig  string   // 全局 MCP 配置路径（只读）
	DataDir          string   // 数据持久化目录
	SandboxEnabled   bool     // 是否启用命令沙箱
	PreferredSandbox string   // 沙箱类型（docker 优先）

	// === 循环控制 ===
	MaxIterations int // 0 = 使用默认值 100

	// === 可选能力（nil = 不启用） ===

	// Session 持久化（nil = 纯内存，不持久化）
	Session *session.TenantSession

	// SessionKey 工具激活的 session key（为空时从 Channel+ChatID 生成）
	SessionKey string

	// ProgressNotifier 进度通知回调（nil = 不通知）
	ProgressNotifier func(lines []string)

	// AutoCompress 自动上下文压缩配置（nil = 不压缩）
	AutoCompress *CompressConfig

	// SendFunc 向 IM 渠道发送消息（nil = 不能发消息）
	SendFunc func(channel, chatID, content string) error

	// InjectInbound 注入入站消息，触发 Agent 完整处理循环（nil = 不支持）
	InjectInbound func(channel, chatID, senderID, content string)

	// Memory 记忆提供者（nil = 无记忆）
	Memory memory.MemoryProvider

	// ToolContextExtras Letta 记忆相关的 ToolContext 扩展字段
	ToolContextExtras *ToolContextExtras

	// SpawnAgent SubAgent 创建能力（nil = 不能创建子 Agent）
	// 输入输出都是统一消息：InboundMessage → OutboundMessage
	SpawnAgent func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)

	// OAuthHandler OAuth 自动触发处理器（nil = 不处理 OAuth）
	// 返回 (content, handled)：handled=true 时用 content 替换工具错误
	OAuthHandler func(ctx context.Context, tc llm.ToolCall, execErr error) (content string, handled bool)

	// ToolExecutor 工具执行函数。
	// 主 Agent 注入带 session MCP、激活检查、Letta memory 的完整版本；
	// SubAgent 使用 nil（defaultToolExecutor 从 cfg.Tools 查找并执行）。
	ToolExecutor func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error)

	// LLMTimeout 单次 LLM 调用超时（0 = 不设超时）
	LLMTimeout time.Duration

	// ToolTimeout 单个工具调用超时（0 = 使用默认 120s）
	ToolTimeout time.Duration

	// EnableReadWriteSplit 启用读写分离并行执行（默认 false = 全部串行）
	EnableReadWriteSplit bool

	// SessionFinalSentCallback 工具发送最终回复时的回调（如飞书卡片）。
	// 返回 true 表示已发送最终回复，后续进度通知应停止。
	SessionFinalSentCallback func() bool
}

// CompressConfig 自动压缩配置。
type CompressConfig struct {
	MaxContextTokens     int
	CompressionThreshold float64
	CompressFunc         func(ctx context.Context, messages []llm.ChatMessage, model string) ([]llm.ChatMessage, error)
}

// ToolContextExtras Letta 记忆相关的 ToolContext 扩展字段。
// 仅包含 Letta memory 特有的字段，通用字段（InjectInbound、Registry 等）
// 已迁移到 RunConfig 中。
type ToolContextExtras struct {
	TenantID                int64
	CoreMemory              *sqlite.CoreMemoryService
	ArchivalMemory          *vectordb.ArchivalService
	MemorySvc               *sqlite.MemoryService
	RecallTimeRange         vectordb.RecallTimeRangeFunc
	ToolIndexer             memory.ToolIndexer
	InvalidateAllSessionMCP func()
}

// DefaultMaxIterations 默认最大迭代次数。
const DefaultMaxIterations = 100

// readOnlyTools 只读工具集合，用于读写分离并行执行。
var readOnlyTools = map[string]bool{
	"Read": true, "Grep": true, "Glob": true,
	"WebSearch": true, "ChatHistory": true,
}

// Run 统一的 Agent 循环。
//
// 输入：RunConfig（从 InboundMessage 构建）
// 输出：*bus.OutboundMessage（可直接发送到 IM 或返回给父 Agent）
//
// 主 Agent 和 SubAgent 使用同一个 Run()，差异通过 RunConfig 注入：
//   - 主 Agent: ToolExecutor=executeTool, ProgressNotifier=sendMessage, AutoCompress=enabled, ...
//   - SubAgent: ToolExecutor=simpleExecutor, ProgressNotifier=nil, AutoCompress=nil, ...
func Run(ctx context.Context, cfg RunConfig) *bus.OutboundMessage {
	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = DefaultMaxIterations
	}

	sessionKey := cfg.SessionKey
	if sessionKey == "" && cfg.Channel != "" {
		sessionKey = cfg.Channel + ":" + cfg.ChatID
	}

	toolExecutor := cfg.ToolExecutor
	if toolExecutor == nil {
		toolExecutor = defaultToolExecutor(&cfg)
	}

	toolTimeout := cfg.ToolTimeout
	if toolTimeout == 0 {
		toolTimeout = 120 * time.Second
	}

	messages := cfg.Messages
	var toolsUsed []string
	var waitingUser bool
	var progressLines []string
	var lastContent string // 用于 LLM 错误时的降级返回

	autoNotify := cfg.ProgressNotifier != nil

	// --- 进度通知 ---
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
					buf.WriteByte('\n')
				}
			}
			buf.WriteString(line)
			if i < len(lines)-1 {
				buf.WriteByte('\n')
			}
		}
		cfg.ProgressNotifier([]string{buf.String()})
	}

	// --- 自动压缩 ---
	maybeCompress := func() {
		cc := cfg.AutoCompress
		if cc == nil || len(messages) <= 3 {
			return
		}

		msgTokens, err := llm.CountMessagesTokens(messages, cfg.Model)
		if err != nil {
			return
		}

		toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)
		toolTokens, _ := llm.CountToolsTokens(toolDefs, cfg.Model)
		tokenCount := msgTokens + toolTokens

		threshold := int(float64(cc.MaxContextTokens) * cc.CompressionThreshold)
		if tokenCount < threshold {
			return
		}

		if autoNotify {
			progressLines = append(progressLines, fmt.Sprintf("> 📦 上下文过大 (%d tokens)，正在压缩...", tokenCount))
			notifyProgress("")
		}

		log.Ctx(ctx).WithFields(log.Fields{
			"tokens":    tokenCount,
			"threshold": threshold,
		}).Info("Auto context compression triggered")

		compressed, compressErr := cc.CompressFunc(ctx, messages, cfg.Model)
		if compressErr != nil {
			log.Ctx(ctx).WithError(compressErr).Warn("Auto context compression failed")
			return
		}

		messages = compressed

		newTokenCount, _ := llm.CountMessagesTokens(compressed, cfg.Model)
		if autoNotify {
			progressLines = append(progressLines, fmt.Sprintf("> ✅ 上下文压缩完成: %d → %d tokens", tokenCount, newTokenCount))
			notifyProgress("")
		}
		log.Ctx(ctx).WithFields(log.Fields{
			"old_tokens": tokenCount,
			"new_tokens": newTokenCount,
		}).Info("Auto context compression completed")

		// 持久化压缩结果到 session
		if cfg.Session != nil {
			if err := cfg.Session.Clear(); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to clear session for auto compression, skipping persistence")
			} else {
				allOk := true
				for _, msg := range compressed {
					if msg.Role == "system" {
						continue
					}
					assertNoSystemPersist(msg)
					if err := cfg.Session.AddMessage(msg); err != nil {
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
	}

	// 推进 round 计数，自动清理长期未使用的工具激活
	if sessionKey != "" {
		cfg.Tools.TickSession(sessionKey)
	}

	// --- 主循环 ---
	for i := 0; i < maxIter; i++ {
		maybeCompress()

		if autoNotify && i > 0 {
			notifyProgress("> 💭 思考中...")
		}

		// assert: 发给 LLM 的消息必须恰好一条 system
		var systemCount int
		for _, m := range messages {
			if m.Role == "system" {
				systemCount++
			}
		}
		if systemCount != 1 {
			log.Ctx(ctx).WithField("system_count", systemCount).Error("assert: LLM messages must have exactly one system message")
			return &bus.OutboundMessage{
				Channel: cfg.Channel,
				ChatID:  cfg.ChatID,
				Content: "内部错误：system 消息数量异常",
				Error:   fmt.Errorf("assert: LLM messages must have exactly one system message; got %d", systemCount),
			}
		}

		// 使用会话特定的工具定义
		toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)

		// LLM 调用（可选超时）
		var llmCtx context.Context
		var llmCancel context.CancelFunc
		if cfg.LLMTimeout > 0 {
			llmCtx, llmCancel = context.WithTimeout(ctx, cfg.LLMTimeout)
		} else {
			llmCtx, llmCancel = ctx, func() {}
		}

		response, err := cfg.LLMClient.Generate(llmCtx, cfg.Model, messages, toolDefs)
		llmCancel()

		if err != nil {
			if ctx.Err() != nil {
				return &bus.OutboundMessage{
					Channel:   cfg.Channel,
					ChatID:    cfg.ChatID,
					Content:   "Agent was cancelled.",
					Error:     ctx.Err(),
					ToolsUsed: toolsUsed,
				}
			}
			// LLM 错误时优雅降级：如果有之前的中间内容，返回它
			if lastContent != "" {
				log.Ctx(ctx).WithFields(log.Fields{
					"agent_id":  cfg.AgentID,
					"iteration": i + 1,
				}).Warnf("LLM failed, returning partial result: %v", err)
				return &bus.OutboundMessage{
					Channel:   cfg.Channel,
					ChatID:    cfg.ChatID,
					Content:   lastContent,
					ToolsUsed: toolsUsed,
				}
			}
			return &bus.OutboundMessage{
				Channel:   cfg.Channel,
				ChatID:    cfg.ChatID,
				Error:     fmt.Errorf("%w: %w", ErrLLMGenerate, err),
				ToolsUsed: toolsUsed,
			}
		}

		// 过滤 think 块
		cleanContent := llm.StripThinkBlocks(response.Content)

		if !response.HasToolCalls() {
			return &bus.OutboundMessage{
				Channel:     cfg.Channel,
				ChatID:      cfg.ChatID,
				Content:     cleanContent,
				ToolsUsed:   toolsUsed,
				WaitingUser: waitingUser,
			}
		}

		// 记录最新的中间内容，用于 LLM 错误时降级
		if cleanContent != "" {
			lastContent = cleanContent
		}

		// 模型的中间思考内容加入进度
		if autoNotify && cleanContent != "" {
			progressLines = append(progressLines, cleanContent)
		}

		// 记录 assistant 消息（含 tool_calls），保留原始 content（包括 think 块）
		assistantMsg := llm.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		// --- 工具执行 ---
		type toolCallEntry struct {
			index int
			tc    llm.ToolCall
		}

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

		// 预分配结果槽位
		type toolExecResult struct {
			content    string
			llmContent string
			result     *tools.ToolResult
			err        error
			elapsed    time.Duration
		}
		execResults := make([]toolExecResult, len(response.ToolCalls))

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

			// 工具执行加超时（SubAgent 工具不加超时）
			var execCtx context.Context
			var cancel context.CancelFunc
			if tc.Name == "SubAgent" {
				execCtx = ctx
				cancel = func() {}
			} else {
				execCtx, cancel = context.WithTimeout(ctx, toolTimeout)
			}

			start := time.Now()
			// 临时替换 ToolExecutor 的 ctx
			result, execErr := toolExecutor(execCtx, tc)
			elapsed := time.Since(start)
			cancel()

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

		// 读写分离并行执行
		if cfg.EnableReadWriteSplit {
			var readOps, writeOps []toolCallEntry
			for idx, tc := range response.ToolCalls {
				entry := toolCallEntry{index: idx, tc: tc}
				if readOnlyTools[tc.Name] {
					readOps = append(readOps, entry)
				} else {
					writeOps = append(writeOps, entry)
				}
			}

			// Phase 1: 只读操作并行执行
			if len(readOps) > 0 {
				const maxParallel = 8
				sem := make(chan struct{}, maxParallel)
				var wg sync.WaitGroup
				for _, entry := range readOps {
					wg.Add(1)
					sem <- struct{}{}
					go func(e toolCallEntry) {
						defer wg.Done()
						defer func() { <-sem }()
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
		} else {
			// 全部串行执行
			for idx, tc := range response.ToolCalls {
				execOne(toolCallEntry{index: idx, tc: tc})
				if autoNotify {
					notifyProgress("")
				}
			}
		}

		// 按原始顺序处理结果
		for idx, tc := range response.ToolCalls {
			r := execResults[idx]
			content := r.llmContent

			// OAuth 自动触发
			if r.err != nil && cfg.OAuthHandler != nil {
				if oauthContent, handled := cfg.OAuthHandler(ctx, tc, r.err); handled {
					content = oauthContent
					autoNotify = false
					if r.result != nil && r.result.WaitingUser {
						waitingUser = true
					}
				}
			}

			// 检查 sessionFinalSent
			if cfg.SessionFinalSentCallback != nil && cfg.SessionFinalSentCallback() {
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

		// 如果有任何工具标记为等待用户响应，则停止循环
		if waitingUser {
			log.Ctx(ctx).Info("Tool is waiting for user response, ending loop without additional reply")
			return &bus.OutboundMessage{
				Channel:     cfg.Channel,
				ChatID:      cfg.ChatID,
				ToolsUsed:   toolsUsed,
				WaitingUser: true,
			}
		}
	}

	return &bus.OutboundMessage{
		Channel:   cfg.Channel,
		ChatID:    cfg.ChatID,
		Content:   "已达到最大迭代次数，请重新描述你的需求。",
		ToolsUsed: toolsUsed,
	}
}

// defaultToolExecutor 创建默认的工具执行器（从 Registry 查找并执行）。
// 用于 SubAgent 等不需要 session MCP / 激活检查的场景。
func defaultToolExecutor(cfg *RunConfig) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		tool, ok := cfg.Tools.Get(tc.Name)
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tc.Name)
		}

		toolCtx := buildToolContext(ctx, cfg)
		return tool.Execute(toolCtx, tc.Arguments)
	}
}

// spawnAgentAdapter 将 SpawnAgent 函数适配为 SubAgentManager 接口。
// 核心职责：将 (task, prompt, tools) 函数签名转换为统一的 InboundMessage。
//
// 这使得 SubAgentTool 零改动：它仍然调用 SubAgentManager.RunSubAgent()，
// 而 adapter 内部完成 string ↔ InboundMessage/OutboundMessage 转换。
type spawnAgentAdapter struct {
	spawnFn  func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	parentID string
	channel  string
	chatID   string
	senderID string
}

// RunSubAgent 实现 tools.SubAgentManager 接口。
func (a *spawnAgentAdapter) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities) (string, error) {
	// 构造统一的 InboundMessage
	metadata := map[string]string{
		"origin_channel": a.channel,
		"origin_chat_id": a.chatID,
		"origin_sender":  a.senderID,
	}

	msg := bus.InboundMessage{
		// 统一寻址
		From: bus.NewIMAddress(a.channel, a.senderID),
		To:   bus.NewAgentAddress(a.parentID),

		// 旧字段（兼容）
		Channel:    bus.SchemeAgent,
		Content:    task,
		SenderID:   parentCtx.SenderID,
		SenderName: parentCtx.SenderName,
		ChatID:     parentCtx.ChatID,
		ChatType:   "agent",
		Time:       time.Now(),

		// Agent 间通信
		ParentAgentID: a.parentID,
		SystemPrompt:  systemPrompt,
		AllowedTools:  allowedTools,
		Capabilities:  caps.ToMap(),
		Metadata:      metadata,
	}

	out, err := a.spawnFn(parentCtx.Ctx, msg)
	if err != nil {
		return "", err
	}
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// buildToolContext 统一构建 ToolContext。
// 从 RunConfig 中提取所有字段，主 Agent 和 SubAgent 使用同一个构建路径。
func buildToolContext(ctx context.Context, cfg *RunConfig) *tools.ToolContext {
	tc := &tools.ToolContext{
		Ctx:        ctx,
		AgentID:    cfg.AgentID,
		Channel:    cfg.Channel,
		ChatID:     cfg.ChatID,
		SenderID:   cfg.SenderID,
		SenderName: cfg.SenderName,
		SendFunc:   cfg.SendFunc,

		// 工作区 & 沙箱
		WorkingDir:          cfg.WorkingDir,
		WorkspaceRoot:       cfg.WorkspaceRoot,
		SandboxWorkDir:      cfg.SandboxWorkDir,
		ReadOnlyRoots:       cfg.ReadOnlyRoots,
		SkillsDirs:          cfg.SkillsDirs,
		AgentsDir:           cfg.AgentsDir,
		MCPConfigPath:       cfg.MCPConfigPath,
		GlobalMCPConfigPath: cfg.GlobalMCPConfig,
		SandboxEnabled:      cfg.SandboxEnabled,
		PreferredSandbox:    cfg.PreferredSandbox,
		DataDir:             cfg.DataDir,

		// 注入入站消息
		InjectInbound: cfg.InjectInbound,

		// 工具注册表
		Registry: cfg.Tools,
	}

	// 注入 SpawnAgent（包装为 SubAgentManager 接口）
	if cfg.SpawnAgent != nil {
		tc.Manager = &spawnAgentAdapter{
			spawnFn:  cfg.SpawnAgent,
			parentID: cfg.AgentID,
			channel:  cfg.Channel,
			chatID:   cfg.ChatID,
			senderID: cfg.SenderID,
		}
	}

	// 注入 Letta 记忆字段（覆盖上面的默认值）
	if ext := cfg.ToolContextExtras; ext != nil {
		tc.TenantID = ext.TenantID
		tc.CoreMemory = ext.CoreMemory
		tc.ArchivalMemory = ext.ArchivalMemory
		tc.MemorySvc = ext.MemorySvc
		tc.RecallTimeRange = ext.RecallTimeRange
		tc.ToolIndexer = ext.ToolIndexer
		if ext.InvalidateAllSessionMCP != nil {
			tc.InvalidateAllSessionMCP = ext.InvalidateAllSessionMCP
		}
	}

	return tc
}

// CallChain 调用链上下文，用于追踪 Agent 间调用关系和防止递归。
type CallChain struct {
	Chain []string // 调用链: ["main", "main/code-reviewer"]
}

// MaxSubAgentDepth 最大 SubAgent 嵌套深度。
const MaxSubAgentDepth = 3

type callChainKey struct{}

// CallChainFromContext 从 context 中提取调用链。
func CallChainFromContext(ctx context.Context) *CallChain {
	if cc, ok := ctx.Value(callChainKey{}).(*CallChain); ok {
		return cc
	}
	return &CallChain{Chain: []string{"main"}}
}

// WithCallChain 将调用链注入 context。
func WithCallChain(ctx context.Context, cc *CallChain) context.Context {
	return context.WithValue(ctx, callChainKey{}, cc)
}

// CanSpawn 检查是否可以创建指定角色的 SubAgent。
// 返回 nil 表示可以，返回 error 表示不可以（深度超限或循环调用）。
func (cc *CallChain) CanSpawn(targetRole string) error {
	if len(cc.Chain) >= MaxSubAgentDepth {
		return fmt.Errorf("max SubAgent depth %d reached (chain: %v)", MaxSubAgentDepth, cc.Chain)
	}
	for _, id := range cc.Chain {
		role := id
		if idx := lastIndexByte(id, '/'); idx >= 0 {
			role = id[idx+1:]
		}
		if role == targetRole {
			return fmt.Errorf("circular SubAgent call: role %q already in chain %v", targetRole, cc.Chain)
		}
	}
	return nil
}

// lastIndexByte returns the index of the last instance of c in s, or -1.
func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// Spawn 创建新的调用链（追加目标角色）。
func (cc *CallChain) Spawn(targetRole string) *CallChain {
	currentID := cc.Chain[len(cc.Chain)-1]
	newChain := make([]string, len(cc.Chain)+1)
	copy(newChain, cc.Chain)
	newChain[len(cc.Chain)] = currentID + "/" + targetRole
	return &CallChain{Chain: newChain}
}

// Depth 返回当前调用深度。
func (cc *CallChain) Depth() int {
	return len(cc.Chain)
}

// Current 返回当前 Agent ID。
func (cc *CallChain) Current() string {
	if len(cc.Chain) == 0 {
		return "main"
	}
	return cc.Chain[len(cc.Chain)-1]
}
