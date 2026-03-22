package agent

import (
	"context"

	"xbot/llm"
	log "xbot/logger"
)

// phase1Manager Phase 1 双视图压缩管理器，实现 SmartCompressor 接口。
// 压缩流程：LLM 摘要（不带 fingerprint）+ ineffective 检测。
// mechanicalTruncate 保留但不再自动调用（会截断丢信息），compressMessages 内部保证缩减效果。
type phase1Manager struct {
	config        *ContextManagerConfig
	provider      *TriggerInfoProvider
	topicDetector *TopicDetector
}

func newPhase1Manager(cfg *ContextManagerConfig) *phase1Manager {
	return &phase1Manager{
		config:   cfg,
		provider: NewTriggerInfoProvider(),
	}
}

// TriggerProvider 返回 TriggerInfoProvider（SmartCompressor 接口）。
func (m *phase1Manager) TriggerProvider() *TriggerInfoProvider {
	return m.provider
}

// SetTopicDetector 设置话题分区检测器，启用后压缩将按话题分区操作。
func (m *phase1Manager) SetTopicDetector(td *TopicDetector) {
	m.topicDetector = td
}

// SetTriggerProvider 设置触发信息提供者（由 Agent 在构建 RunConfig 时注入）。
func (m *phase1Manager) SetTriggerProvider(p *TriggerInfoProvider) {
	m.provider = p
}

// ShouldCompressDynamic 使用三因子动态阈值判断是否需要压缩（SmartCompressor 接口）。
func (m *phase1Manager) ShouldCompressDynamic(info TriggerInfo) bool {
	if info.CurrentTokens == 0 || info.MaxTokens == 0 {
		return false
	}
	if !m.provider.Cooldown.ShouldTrigger(info.IterationCount) {
		return false
	}
	threshold := calculateDynamicThreshold(info)
	ratio := float64(info.CurrentTokens) / float64(info.MaxTokens)
	return ratio >= threshold
}

func (m *phase1Manager) Mode() ContextMode { return ContextModePhase1 }

func (m *phase1Manager) ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool {
	if len(messages) <= 3 {
		return false
	}
	msgTokens, err := llm.CountMessagesTokens(messages, model)
	if err != nil {
		return false
	}
	tokenCount := msgTokens + toolTokens
	threshold := int(float64(m.config.MaxContextTokens) * m.config.CompressionThreshold)
	return tokenCount >= threshold
}

// Compress 压缩：LLM 压缩 + ineffective 检测 + 日志告警。
// BUG FIX: 不再使用 mechanicalTruncate 兜底（会截断丢信息）。
// compressMessages 内部已保证缩减效果：通过 LLM 输出长度限制 + aggressiveThinTail + shortenSummary。
func (m *phase1Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	originalTokens, _ := llm.CountMessagesTokens(messages, model)

	log.Ctx(ctx).WithFields(map[string]interface{}{
		"original_tokens": originalTokens,
		"max_tokens":      m.config.MaxContextTokens,
	}).Info("Phase 1 compress: starting")

	// 步骤1：LLM 压缩（不带 fingerprint）
	result, err := compressMessages(ctx, messages, client, model, m.topicDetector)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Phase 1 compress: LLM compression failed")
		return nil, err
	}

	// 步骤2：有效性检测 + 日志（不做 mechanicalTruncate，compressMessages 已保证有效性）
	newTokens, _ := llm.CountMessagesTokens(result.LLMView, model)
	reductionRate := 0.0
	if originalTokens > 0 {
		reductionRate = 1.0 - float64(newTokens)/float64(originalTokens)
	}

	if reductionRate < 0.10 {
		log.Ctx(ctx).WithFields(map[string]interface{}{
			"reduction_rate":  reductionRate,
			"new_tokens":      newTokens,
			"original_tokens": originalTokens,
		}).Warn("Phase 1 compress: still ineffective (reduction<10%) after internal retries")
	}

	// 步骤3：质量报告
	log.Ctx(ctx).WithFields(map[string]interface{}{
		"reduction_rate": reductionRate,
		"new_tokens":     newTokens,
	}).Info("Phase 1 compress quality report")

	return result, nil
}

// ManualCompress 手动压缩（/compress 命令使用）。
func (m *phase1Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	return compressMessages(ctx, messages, client, model, m.topicDetector)
}

func (m *phase1Manager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	msgTokens, err := llm.CountMessagesTokens(messages, model)
	if err != nil {
		msgTokens = 0
	}
	tokenCount := msgTokens + toolTokens
	threshold := int(float64(m.config.MaxContextTokens) * m.config.CompressionThreshold)

	return &ContextStats{
		SystemTokens: msgTokens, // 简化：不单独计算 system token
		TotalTokens:  tokenCount,
		MaxTokens:    m.config.MaxContextTokens,
		Threshold:    threshold,
		Mode:         ContextModePhase1,
	}
}

func (m *phase1Manager) SessionHook() SessionCompressHook { return nil }
