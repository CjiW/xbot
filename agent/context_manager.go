package agent

import (
	"context"
	"fmt"
	"sync"

	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
)

// ContextMode 上下文管理模式
type ContextMode string

const (
	// ContextModePhase1 Phase 1 双视图架构（当前默认）
	ContextModePhase1 ContextMode = "phase1"
	// ContextModePhase2 Phase 2 三层渐进压缩
	ContextModePhase2 ContextMode = "phase2"
	// ContextModeNone 禁用自动上下文压缩
	ContextModeNone ContextMode = "none"
)

// ValidContextModes 所有可能的上下文模式
var ValidContextModes = []ContextMode{ContextModePhase1, ContextModePhase2, ContextModeNone}

// IsValidContextMode 检查是否为有效的上下文模式
func IsValidContextMode(mode ContextMode) bool {
	for _, m := range ValidContextModes {
		if m == mode {
			return true
		}
	}
	return false
}

// ContextManager 上下文管理器统一接口。
// 所有压缩策略实现此接口，通过策略模式实现新旧架构可切换。
type ContextManager interface {
	// Mode 返回当前管理模式标识。
	Mode() ContextMode

	// ShouldCompress 判断是否需要触发自动压缩。
	// 参数：
	//   - messages: 当前上下文消息
	//   - model: LLM 模型名（用于 token 计数）
	//   - toolTokens: 工具定义占用的 token 数
	ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool

	// Compress 执行上下文压缩。
	Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)

	// ManualCompress 手动压缩（/compress 命令使用）。
	// 关键契约：无论当前模式如何，ManualCompress 都应尽力执行压缩。
	// 即使 auto=false 的 noopManager，ManualCompress 也降级到 Phase1 执行，
	// 保留 /compress 手动命令始终可用的现有语义。
	ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)

	// ContextInfo 返回上下文统计信息（/context info 命令使用）。
	ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats

	// SessionHook 返回压缩后的 session 持久化钩子（可选，返回 nil 表示无特殊处理）。
	// Phase 2 可能需要在此钩子中做额外操作（如更新话题分区索引）。
	SessionHook() SessionCompressHook
}

// ContextStats 上下文统计信息
type ContextStats struct {
	SystemTokens      int
	UserTokens        int
	AssistantTokens   int
	ToolMsgTokens     int
	ToolDefTokens     int
	TotalTokens       int
	MaxTokens         int
	Threshold         int
	Mode              ContextMode
	IsRuntimeOverride bool // 是否为运行时覆盖
	DefaultMode       ContextMode
}

// SessionCompressHook 压缩后的 session 处理钩子
type SessionCompressHook interface {
	// AfterPersist 在 session 持久化压缩结果后调用
	AfterPersist(ctx context.Context, tenantSession *session.TenantSession, result *CompressResult)
}

// ContextManagerConfig 上下文管理器配置。
// 包含全局配置（环境变量/Agent.Config）和运行时开关（命令行切换）。
// 所有读写操作通过 sync.RWMutex 保护，确保并发安全。
type ContextManagerConfig struct {
	mu sync.RWMutex

	// MaxContextTokens 最大上下文 token 数（默认 100000）
	MaxContextTokens int
	// CompressionThreshold 触发压缩的 token 比例阈值（默认 0.7）
	CompressionThreshold float64

	// DefaultMode 默认压缩模式（启动时决定，来自环境变量或 Agent.Config）
	DefaultMode ContextMode

	// runtimeMode 运行时模式覆盖（通过 /context mode 命令切换）
	// 空值表示使用 DefaultMode，非空值覆盖 DefaultMode
	runtimeMode ContextMode
}

// EffectiveMode 返回当前生效的模式（RuntimeMode 优先）。
// 读锁保护。
func (c *ContextManagerConfig) EffectiveMode() ContextMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.runtimeMode != "" {
		return c.runtimeMode
	}
	return c.DefaultMode
}

// RuntimeMode 返回当前运行时覆盖模式（无覆盖时返回空字符串）。
// 读锁保护。
func (c *ContextManagerConfig) RuntimeMode() ContextMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.runtimeMode
}

// SetRuntimeMode 设置运行时模式覆盖。
// 写锁保护。
func (c *ContextManagerConfig) SetRuntimeMode(mode ContextMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runtimeMode = mode
}

// ResetRuntimeMode 清除运行时覆盖，恢复默认模式。
// 写锁保护。
func (c *ContextManagerConfig) ResetRuntimeMode() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runtimeMode = ""
}

// noopManager 禁用自动压缩的管理器。
// ShouldCompress 始终返回 false，但 ManualCompress 仍可执行（降级到 Phase 1）。
type noopManager struct {
	config *ContextManagerConfig
	phase1 *phase1Manager // 内嵌 Phase1 用于 ManualCompress 和 ContextInfo
}

func newNoopManager(cfg *ContextManagerConfig) *noopManager {
	return &noopManager{
		config: cfg,
		phase1: newPhase1Manager(cfg),
	}
}

func (m *noopManager) Mode() ContextMode { return ContextModeNone }

func (m *noopManager) ShouldCompress([]llm.ChatMessage, string, int) bool {
	return false // 自动压缩始终禁用
}

func (m *noopManager) Compress(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error) {
	// 自动路径不应到达这里（ShouldCompress 返回 false）
	return nil, fmt.Errorf("auto compression is disabled (mode=none)")
}

func (m *noopManager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	// /compress 手动命令：降级到 Phase 1 执行，保持命令始终可用
	return m.phase1.ManualCompress(ctx, messages, client, model)
}

func (m *noopManager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	// 仍返回完整统计信息（复用 Phase1 的实现）
	stats := m.phase1.ContextInfo(messages, model, toolTokens)
	stats.Mode = ContextModeNone
	return stats
}

func (m *noopManager) SessionHook() SessionCompressHook { return nil }

// SmartCompressor 智能压缩接口（Phase 2 扩展）。
// phase2Manager 实现此接口，Run() 中的 maybeCompress 通过类型断言检测。
// Phase1 和 noopManager 不实现此接口，走原有 ShouldCompress 路径。
type SmartCompressor interface {
	ContextManager
	ShouldCompressDynamic(info TriggerInfo) bool
	TriggerProvider() *TriggerInfoProvider
}

// TriggerInfoProvider 提供压缩触发所需的状态追踪器。
type TriggerInfoProvider struct {
	GrowthTracker *TokenGrowthTracker
	Cooldown      *CompressCooldown
}

// NewTriggerInfoProvider 创建带默认配置的 TriggerInfoProvider。
func NewTriggerInfoProvider() *TriggerInfoProvider {
	return &TriggerInfoProvider{
		GrowthTracker: NewTokenGrowthTracker(10),
		Cooldown:      NewCompressCooldown(3),
	}
}

// Reset 重置所有追踪器状态。
func (p *TriggerInfoProvider) Reset() {
	p.GrowthTracker.Reset()
	p.Cooldown.Reset()
}

// NewContextManager 根据配置创建对应的 ContextManager 实例。
func NewContextManager(cfg *ContextManagerConfig) ContextManager {
	mode := cfg.EffectiveMode()
	switch mode {
	case ContextModePhase2:
		// Phase 2: 三层渐进压缩（Offload → Evict → Compact）
		log.WithField("mode", mode).Info("Using Phase 2 smart compression (Offload → Evict → Compact)")
		return newPhase2Manager(cfg)
	case ContextModeNone:
		return newNoopManager(cfg)
	case ContextModePhase1, "":
		return newPhase1Manager(cfg)
	default:
		log.WithField("mode", mode).Warnf("Unknown context mode %q, falling back to Phase 1", mode)
		return newPhase1Manager(cfg)
	}
}

// resolveContextMode 根据配置确定上下文管理模式。
// 优先级：ContextMode > 默认 phase1
func resolveContextMode(cfg Config) ContextMode {
	// 1. 优先使用新配置
	if cfg.ContextMode != "" {
		if IsValidContextMode(cfg.ContextMode) {
			return cfg.ContextMode
		}
		log.WithField("mode", cfg.ContextMode).Warn("Invalid AGENT_CONTEXT_MODE, ignoring")
	}
	// 2. 向后兼容旧字段：EnableAutoCompress=false 时禁用压缩
	if !cfg.EnableAutoCompress {
		return ContextModeNone
	}
	// 3. 默认 phase1
	return ContextModePhase1
}
