package agent

import (
	"context"

	"xbot/llm"
	log "xbot/logger"
)

// phase2Manager Phase 2 三层渐进压缩管理器，实现 SmartCompressor 接口。
// 压缩流程：Offload（大 tool result 落盘）→ Evict（信息密度驱逐）→ Compact（LLM 摘要）
type phase2Manager struct {
	config   *ContextManagerConfig
	provider *TriggerInfoProvider
}

func newPhase2Manager(cfg *ContextManagerConfig) *phase2Manager {
	return &phase2Manager{
		config:   cfg,
		provider: NewTriggerInfoProvider(),
	}
}

// TriggerProvider 返回 TriggerInfoProvider（SmartCompressor 接口）。
func (m *phase2Manager) TriggerProvider() *TriggerInfoProvider {
	return m.provider
}

// SetTriggerProvider 设置触发信息提供者（由 Agent 在构建 RunConfig 时注入）。
func (m *phase2Manager) SetTriggerProvider(p *TriggerInfoProvider) {
	m.provider = p
}

// ShouldCompressDynamic 使用三因子动态阈值判断是否需要压缩（SmartCompressor 接口）。
func (m *phase2Manager) ShouldCompressDynamic(info TriggerInfo) bool {
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

func (m *phase2Manager) Mode() ContextMode { return ContextModePhase2 }

func (m *phase2Manager) ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool {
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

// Compress 实现 Evict → Compact 两阶段流水线。
// Phase 1: Evict（信息密度驱逐）将上下文降到 70% MaxContextTokens。
// Phase 2: Compact（如果 Evict 后仍超阈值）使用 LLM 压缩。
func (m *phase2Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	targetTokens := int(float64(m.config.MaxContextTokens) * 0.7)

	// Phase 1: Evict（信息密度驱逐）
	evicted := evictByDensity(messages, 3, targetTokens, model)
	evictTokens, _ := llm.CountMessagesTokens(evicted, model)

	// Phase 2: Compact（如果 Evict 后仍超阈值）
	if evictTokens >= int(float64(m.config.MaxContextTokens)*m.config.CompressionThreshold) {
		// 压缩前提取指纹用于质量校验
		fp := ExtractFingerprint(messages)
		originalTokens, _ := llm.CountMessagesTokens(messages, model)

		result, err := compressMessages(ctx, evicted, client, model)
		if err != nil {
			return nil, err
		}

		// 质量校验：评估压缩结果
		compressedText := joinMessages(result.SessionView)
		compressedTokens, _ := llm.CountMessagesTokens(result.LLMView, model)
		quality := EvaluateQuality(originalTokens, compressedTokens, fp, compressedText)

		// 检查关键信息保留率
		_, lostItems := ValidateCompression(messages, result.SessionView, fp)
		retentionRate := 1.0
		if totalItems := len(fp.FilePaths) + len(fp.Identifiers) + len(fp.Errors) + len(fp.Decisions); totalItems > 0 {
			retentionRate = float64(totalItems-len(lostItems)) / float64(totalItems)
		}

		// 低质量重新压缩（最多1次）
		if quality < 0.6 && retentionRate < 0.8 && client != nil {
			log.Ctx(ctx).WithFields(map[string]interface{}{
				"quality_score":  quality,
				"retention_rate": retentionRate,
				"lost_items":     len(lostItems),
			}).Warn("Phase 2 compress quality low, retrying with enhanced prompt")
			// compressMessages 内部已使用结构化 prompt，重试一次
			retryResult, retryErr := compressMessages(ctx, evicted, client, model)
			if retryErr == nil {
				retryText := joinMessages(retryResult.SessionView)
				retryTokens, _ := llm.CountMessagesTokens(retryResult.LLMView, model)
				retryQuality := EvaluateQuality(originalTokens, retryTokens, fp, retryText)
				if retryQuality > quality {
					quality = retryQuality
					result = retryResult
				}
			}
		}

		// 标记完整性检测
		missingMarkers := ValidateMarkers(compressedText, fp)
		if len(missingMarkers) > 0 {
			log.Ctx(ctx).WithFields(map[string]interface{}{
				"missing_markers": len(missingMarkers),
				"quality_score":   quality,
			}).Warn("Phase 2 compress missing structured markers")
		}

		log.Ctx(ctx).WithFields(map[string]interface{}{
			"quality_score":  quality,
			"retention_rate": retentionRate,
			"markers":        countStructuredMarkers(compressedText),
		}).Info("Phase 2 compress quality report")
		return result, nil
	}

	// Evict 后已足够，构建双视图
	return buildCompressResultFromEvicted(evicted, ctx, model)
}

// ManualCompress 手动压缩走完整流水线（忽略阈值检查）。
func (m *phase2Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	return compressMessages(ctx, messages, client, model)
}

func (m *phase2Manager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	return newPhase1Manager(m.config).ContextInfo(messages, model, toolTokens)
}

func (m *phase2Manager) SessionHook() SessionCompressHook { return nil }
