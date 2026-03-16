package agent

import (
	"context"
	"fmt"
	"time"

	"xbot/bus"
	"xbot/llm"
	"xbot/memory"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
	"xbot/tools"
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

	// Memory 记忆提供者（nil = 无记忆）
	Memory memory.MemoryProvider

	// ToolContextExtras 额外的 ToolContext 字段注入
	ToolContextExtras *ToolContextExtras

	// SpawnAgent SubAgent 创建能力（nil = 不能创建子 Agent）
	// 输入输出都是统一消息：InboundMessage → OutboundMessage
	SpawnAgent func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)

	// OAuthHandler OAuth 自动触发处理器（nil = 不处理 OAuth）
	// 返回 (content, handled)：handled=true 时用 content 替换工具错误
	OAuthHandler func(ctx context.Context, tc llm.ToolCall, execErr error) (content string, handled bool)
}

// CompressConfig 自动压缩配置。
type CompressConfig struct {
	MaxContextTokens     int
	CompressionThreshold float64
	CompressFunc         func(ctx context.Context, messages []llm.ChatMessage, model string) ([]llm.ChatMessage, error)
}

// ToolContextExtras Letta 记忆相关的 ToolContext 扩展字段。
// 主 Agent 和 SubAgent 按需注入不同的字段。
type ToolContextExtras struct {
	TenantID                int64
	CoreMemory              *sqlite.CoreMemoryService
	ArchivalMemory          *vectordb.ArchivalService
	MemorySvc               *sqlite.MemoryService
	RecallTimeRange         vectordb.RecallTimeRangeFunc
	ToolIndexer             memory.ToolIndexer
	InjectInbound           func(channel, chatID, senderID, content string)
	Registry                *tools.Registry
	InvalidateAllSessionMCP func()
}

// DefaultMaxIterations 默认最大迭代次数。
const DefaultMaxIterations = 100

// Run 统一的 Agent 循环。
//
// 输入：RunConfig（从 InboundMessage 构建）
// 输出：*OutboundMessage（可直接发送到 IM 或返回给父 Agent）
//
// 当前为接口占位，Phase 1 实现时将从 runLoop + RunSubAgent 提取合并。
// TODO(#127): 实现统一循环，替换 runLoop 和 RunSubAgent
func Run(ctx context.Context, cfg RunConfig) *bus.OutboundMessage {
	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = DefaultMaxIterations
	}

	_ = maxIter // will be used when implemented

	// placeholder: 直接返回错误，表示尚未实现
	return &bus.OutboundMessage{
		Channel: cfg.Channel,
		ChatID:  cfg.ChatID,
		Content: "unified agent engine not yet implemented",
		Error:   fmt.Errorf("Run() not yet implemented (issue #127)"),
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
func (a *spawnAgentAdapter) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string) (string, error) {
	// 构造统一的 InboundMessage
	metadata := map[string]string{
		"origin_channel": a.channel,
		"origin_chat_id": a.chatID,
		"origin_sender":  a.senderID,
	}

	msg := bus.InboundMessage{
		// 统一寻址
		From: bus.NewIMAddress(a.channel, a.senderID),
		To:   bus.NewAgentAddress(a.parentID), // 目标是父 Agent 下的 SubAgent

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
//
// TODO(#127): Phase 1 实现时，从 executeTool 和 RunSubAgent 中提取公共逻辑。
func buildToolContext(ctx context.Context, cfg *RunConfig) *tools.ToolContext {
	tc := &tools.ToolContext{
		Ctx:        ctx,
		AgentID:    cfg.AgentID,
		Channel:    cfg.Channel,
		ChatID:     cfg.ChatID,
		SenderID:   cfg.SenderID,
		SenderName: cfg.SenderName,
		SendFunc:   cfg.SendFunc,
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

	// 注入 Letta 记忆字段
	if ext := cfg.ToolContextExtras; ext != nil {
		tc.TenantID = ext.TenantID
		tc.CoreMemory = ext.CoreMemory
		tc.ArchivalMemory = ext.ArchivalMemory
		tc.MemorySvc = ext.MemorySvc
		tc.RecallTimeRange = ext.RecallTimeRange
		tc.ToolIndexer = ext.ToolIndexer
		tc.InjectInbound = ext.InjectInbound
		tc.Registry = ext.Registry
		tc.InvalidateAllSessionMCP = ext.InvalidateAllSessionMCP
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
	// 检查链中是否已有同名角色（防止 A→B→A 或 A→A 循环）
	// 每个 chain entry 的最后一段是角色名（如 "main/code-reviewer" → "code-reviewer"）
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
