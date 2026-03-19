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
	originalTokens, _ := llm.CountMessagesTokens(messages, model)
	targetTokens := int(float64(m.config.MaxContextTokens) * 0.7)

	log.Ctx(ctx).WithFields(map[string]interface{}{
		"original_tokens": originalTokens,
		"target_tokens":   targetTokens,
		"max_tokens":      m.config.MaxContextTokens,
	}).Info("Phase 2 compress: starting eviction")

	// Phase 1: Evict（信息密度驱逐）
	// BUG FIX: keepGroups 从 3 降到 1。
	// 之前保留最后 3 组完整，当 conversation 只有 ≤3 组 tool 时直接返回 0 驱逐。
	// 保留 1 组足够 LLM 继续当前工具对话，其余全走密度评分驱逐。
	evicted := evictByDensity(messages, 1, targetTokens, model)
	evictTokens, _ := llm.CountMessagesTokens(evicted, model)
	evictedCount := countEvictedMessages(messages, evicted)

	log.Ctx(ctx).WithFields(map[string]interface{}{
		"original_tokens": originalTokens,
		"evict_tokens":    evictTokens,
		"tokens_saved":    originalTokens - evictTokens,
		"evicted_msgs":    evictedCount,
	}).Info("Phase 2 compress: eviction complete")

	// Phase 2: Compact（如果 Evict 后仍超阈值）
	if evictTokens >= int(float64(m.config.MaxContextTokens)*m.config.CompressionThreshold) {
		log.Ctx(ctx).Info("Phase 2 compress: eviction insufficient, proceeding to LLM compact")

		// 压缩前提取指纹用于质量校验
		fp := ExtractFingerprint(messages)

		// 使用 evicted 消息进行 LLM 压缩：
		// evicted 消息中低密度 tool result 已被 [evicted] marker 替换（token 节省），
		// 同时通过指纹引导（compressMessagesWithFingerprint 内部注入）确保关键信息被保留。
		// 指纹从原始 messages 提取（而非 evicted），保证信息完整度。
		result, err := compressMessagesWithFingerprint(ctx, evicted, fp, client, model)
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
		if quality < 0.6 && retentionRate < 0.8 && client != nil && len(lostItems) > 0 {
			log.Ctx(ctx).WithFields(map[string]interface{}{
				"quality_score":  quality,
				"retention_rate": retentionRate,
				"lost_items":     len(lostItems),
			}).Warn("Phase 2 compress quality low, retrying with lost items injected")
			// 使用增强函数重试：将丢失的关键信息注入 prompt，引导 LLM 补全
			retryResult, retryErr := compressMessagesWithFingerprintAndLostItems(ctx, evicted, fp, lostItems, client, model)
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
			"new_tokens":     compressedTokens,
		}).Info("Phase 2 compress quality report")
		return result, nil
	}

	// Evict 后已足够，构建双视图
	log.Ctx(ctx).WithFields(map[string]interface{}{
		"evict_tokens": evictTokens,
		"tokens_saved": originalTokens - evictTokens,
	}).Info("Phase 2 compress: eviction sufficient, no LLM compact needed")
	return buildCompressResultFromEvicted(evicted, ctx, model)
}

// ManualCompress 手动压缩走完整流水线（Evict → Compact）。
// BUG FIX: 之前直接调用 compressMessages 跳过了 eviction，浪费 LLM tokens。
// 现在手动压缩也先执行 eviction，不足时再 LLM compress。
func (m *phase2Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	originalTokens, _ := llm.CountMessagesTokens(messages, model)
	targetTokens := int(float64(m.config.MaxContextTokens) * 0.7)

	// Phase 1: Evict
	evicted := evictByDensity(messages, 1, targetTokens, model)
	evictTokens, _ := llm.CountMessagesTokens(evicted, model)

	log.Ctx(ctx).WithFields(map[string]interface{}{
		"original_tokens": originalTokens,
		"evict_tokens":    evictTokens,
		"tokens_saved":    originalTokens - evictTokens,
	}).Info("Phase 2 manual compress: eviction complete")

	// 如果 eviction 后低于阈值，直接返回 evicted 结果
	if evictTokens < int(float64(m.config.MaxContextTokens)*m.config.CompressionThreshold) {
		return buildCompressResultFromEvicted(evicted, ctx, model)
	}

	// Phase 2: Compact（使用 evicted messages + 指纹引导）
	fp := ExtractFingerprint(messages)
	result, err := compressMessagesWithFingerprint(ctx, evicted, fp, client, model)
	if err != nil {
		// 降级到不带指纹的压缩
		return compressMessages(ctx, messages, client, model)
	}
	return result, nil
}

func (m *phase2Manager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	return newPhase1Manager(m.config).ContextInfo(messages, model, toolTokens)
}

func (m *phase2Manager) SessionHook() SessionCompressHook { return nil }
