package agent

import (
	"context"

	"xbot/llm"
	log "xbot/logger"
)

// phase2Manager Phase 2 智能压缩管理器，实现 SmartCompressor 接口。
// 压缩流程：Offload（大 tool result 落盘）→ Compact（LLM 摘要，含 fingerprint 引导与活跃文件保护）
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

// Compress 智能压缩：提取指纹 → LLM 压缩 → 质量校验 → 低质量重试。
func (m *phase2Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	originalTokens, _ := llm.CountMessagesTokens(messages, model)

	log.Ctx(ctx).WithFields(map[string]interface{}{
		"original_tokens": originalTokens,
		"max_tokens":      m.config.MaxContextTokens,
	}).Info("Phase 1.5 compress: starting")

	// 步骤1：提取指纹（含活跃文件）
	fp := ExtractFingerprint(messages)

	// 步骤2：带 fingerprint 引导的 LLM 压缩
	result, err := compressMessagesWithFingerprint(ctx, messages, fp, client, model)
	if err != nil {
		return nil, err
	}

	// 步骤3：质量校验
	compressedText := joinMessages(result.SessionView)
	compressedTokens, _ := llm.CountMessagesTokens(result.LLMView, model)
	quality := EvaluateQuality(originalTokens, compressedTokens, fp, compressedText)

	_, lostItems := ValidateCompression(messages, result.SessionView, fp)
	retentionRate := 1.0
	if totalItems := len(fp.FilePaths) + len(fp.Identifiers) + len(fp.Errors) + len(fp.Decisions); totalItems > 0 {
		retentionRate = float64(totalItems-len(lostItems)) / float64(totalItems)
	}

	// 步骤4：低质量重试
	if quality < 0.6 && retentionRate < 0.8 && client != nil && len(lostItems) > 0 {
		retryResult, retryErr := compressMessagesWithFingerprintAndLostItems(ctx, messages, fp, lostItems, client, model)
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

	// 步骤5：标记完整性检测
	missingMarkers := ValidateMarkers(compressedText, fp)
	totalItems := len(fp.FilePaths) + len(fp.Errors) + len(fp.Decisions)
	if totalItems > 0 && float64(len(missingMarkers))/float64(totalItems) > 0.5 {
		log.Ctx(ctx).WithFields(map[string]interface{}{
			"missing_markers":  len(missingMarkers),
			"missing_ratio":    float64(len(missingMarkers)) / float64(totalItems),
			"quality_score":    quality,
		}).Warn("Phase 1.5 compress missing markers (>50%)")
	}

	log.Ctx(ctx).WithFields(map[string]interface{}{
		"quality_score":  quality,
		"retention_rate": retentionRate,
		"new_tokens":     compressedTokens,
	}).Info("Phase 1.5 compress quality report")

	return result, nil
}

// ManualCompress 手动压缩：带 fingerprint 引导的 LLM 压缩，降级到不带指纹的压缩。
func (m *phase2Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	fp := ExtractFingerprint(messages)
	result, err := compressMessagesWithFingerprint(ctx, messages, fp, client, model)
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
