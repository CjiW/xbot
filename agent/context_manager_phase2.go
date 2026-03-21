package agent

import (
	"context"
	"fmt"

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

// Compress 智能压缩：提取指纹 → LLM 压缩 → 质量校验 → 低质量重试 → fallback 兜底。
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

	// 步骤2.5：LLM 压缩失败 → 降级到简单压缩
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Phase 1.5 compress: fingerprint-guided compression failed, falling back to simple compression")
		result, err = compressMessages(ctx, messages, client, model)
		if err != nil {
			return nil, fmt.Errorf("all LLM compression strategies failed: %w", err)
		}
	}

	// 辅助函数：从结果中计算 reduction rate
	calcReduction := func(r *CompressResult) float64 {
		tokens, _ := llm.CountMessagesTokens(r.LLMView, model)
		if originalTokens > 0 {
			return 1.0 - float64(tokens)/float64(originalTokens)
		}
		return 0.0
	}

	// 步骤3：质量校验
	compressedText := joinMessages(result.SessionView)
	compressedTokens, _ := llm.CountMessagesTokens(result.LLMView, model)
	reductionRate := 0.0
	if originalTokens > 0 {
		reductionRate = 1.0 - float64(compressedTokens)/float64(originalTokens)
	}
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
				result = retryResult
				reductionRate = calcReduction(result)
			}
		}
	}

	// 步骤5：标记完整性检测 + reduction rate 检查
	missingMarkers := ValidateMarkers(compressedText, fp)
	markerTotal := len(fp.FilePaths) + len(fp.Errors) + len(fp.Decisions)
	missingRatio := 0.0
	if markerTotal > 0 {
		missingRatio = float64(len(missingMarkers)) / float64(markerTotal)
	}

	// 步骤6：当标记缺失率过高 + 压缩率不足时，启用 fallback 策略
	// （这是修复"压缩偶尔完全无效"的核心逻辑）
	if missingRatio > 0.5 && reductionRate < 0.10 {
		log.Ctx(ctx).WithFields(map[string]interface{}{
			"missing_markers": len(missingMarkers),
			"missing_ratio":   missingRatio,
			"reduction_rate":  reductionRate,
			"quality_score":   quality,
		}).Warn("Phase 1.5 compress: poor quality (missing>50% AND reduction<10%), trying fallback strategies")

		// 策略1：尝试不带 fingerprint 的简单压缩（减少 prompt 干扰）
		if client != nil {
			simpleResult, simpleErr := compressMessages(ctx, messages, client, model)
			if simpleErr == nil {
				simpleReduction := calcReduction(simpleResult)
				if simpleReduction > reductionRate {
					log.Ctx(ctx).WithFields(map[string]interface{}{
						"simple_reduction": simpleReduction,
						"fp_reduction":     reductionRate,
					}).Info("Phase 1.5 compress: simple compression achieved better reduction, using it")
					result = simpleResult
					reductionRate = simpleReduction
				}
			}
		}

		// 策略2：如果仍然压缩不足，使用机械截断兜底
		if reductionRate < 0.10 {
			log.Ctx(ctx).Warn("Phase 1.5 compress: all LLM strategies insufficient, using mechanical truncation")
			mechResult := m.mechanicalTruncate(messages, model)
			mechTokens, _ := llm.CountMessagesTokens(mechResult.LLMView, model)
			mechReduction := 0.0
			if originalTokens > 0 {
				mechReduction = 1.0 - float64(mechTokens)/float64(originalTokens)
			}
			log.Ctx(ctx).WithFields(map[string]interface{}{
				"mech_reduction":  mechReduction,
				"mech_tokens":     mechTokens,
				"original_tokens": originalTokens,
			}).Info("Phase 1.5 compress: mechanical truncation result")

			if mechReduction > reductionRate {
				result = mechResult
				reductionRate = mechReduction
			}
		}
	}

	// 步骤7：质量报告（使用最终结果）
	compressedTokens, _ = llm.CountMessagesTokens(result.LLMView, model)
	if originalTokens > 0 {
		reductionRate = 1.0 - float64(compressedTokens)/float64(originalTokens)
	}
	finalMissing := ValidateMarkers(joinMessages(result.SessionView), fp)
	finalMarkerTotal := len(fp.FilePaths) + len(fp.Errors) + len(fp.Decisions)
	finalMissingRatio := 0.0
	if finalMarkerTotal > 0 {
		finalMissingRatio = float64(len(finalMissing)) / float64(finalMarkerTotal)
	}

	log.Ctx(ctx).WithFields(map[string]interface{}{
		"quality_score":   quality,
		"retention_rate":  retentionRate,
		"reduction_rate":  reductionRate,
		"new_tokens":      compressedTokens,
		"missing_markers": len(finalMissing),
		"missing_ratio":   finalMissingRatio,
	}).Info("Phase 1.5 compress quality report")

	return result, nil
}

// mechanicalTruncate 机械截断：当所有 LLM 压缩策略都失败时的最后手段。
// 保留 system message + 最近 N 条消息，对更早的消息做激进截断。
// 保证：result tokens < original tokens * 0.5
func (m *phase2Manager) mechanicalTruncate(messages []llm.ChatMessage, model string) *CompressResult {
	originalTokens, _ := llm.CountMessagesTokens(messages, model)
	targetTokens := int(float64(originalTokens) * 0.5)

	// 找尾部切割点（与 compressMessagesWithFingerprint 相同逻辑）
	tailStart := len(messages)
	for i := len(messages) - 1; i >= 1; i-- {
		msg := messages[i]
		if msg.Role == "user" {
			tailStart = i
			break
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			tailStart = i
			break
		}
		if i == 1 {
			tailStart = 1
		}
	}

	// 分离 system / head / tail
	var systemMsgs []llm.ChatMessage
	var head []llm.ChatMessage
	var tail []llm.ChatMessage

	for i, msg := range messages {
		if i < tailStart {
			if msg.Role == "system" {
				systemMsgs = append(systemMsgs, msg)
			} else {
				head = append(head, msg)
			}
		} else {
			tail = append(tail, msg)
		}
	}

	activeFiles := ExtractActiveFiles(messages, 3)

	// 对 head 使用 aggressiveThinTail 极限截断（keepGroups=0 会被函数内部 clamp 到 1）
	thinnedHead := aggressiveThinTail(head, 0, activeFiles)

	// 对 tail 使用 aggressiveThinTail（保留 1 组）
	thinnedTail := aggressiveThinTail(tail, 1, activeFiles)

	// 如果 head 截断后仍然太大，逐步丢弃最早的 head 消息
	systemTokens, _ := llm.CountMessagesTokens(systemMsgs, model)
	tailTokens, _ := llm.CountMessagesTokens(thinnedTail, model)
	noticeTokens := 20 // "[Earlier context truncated due to compression failure]" 大约 10 tokens

	headBudget := targetTokens - systemTokens - tailTokens - noticeTokens
	if headBudget < 0 {
		headBudget = 0
	}

	headTokens, _ := llm.CountMessagesTokens(thinnedHead, model)
	for headTokens > headBudget && len(thinnedHead) > 1 {
		// 移除最早的一条非 system 消息
		removed := false
		for i, msg := range thinnedHead {
			if msg.Role != "system" {
				thinnedHead = append(thinnedHead[:i], thinnedHead[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			break
		}
		headTokens, _ = llm.CountMessagesTokens(thinnedHead, model)
	}

	// 构建结果：system + "[Earlier context truncated]" + thinned head + thinned tail
	truncationNotice := llm.NewUserMessage("[Earlier context truncated due to compression failure]")

	llmView := make([]llm.ChatMessage, 0, len(systemMsgs)+1+len(thinnedHead)+len(thinnedTail))
	llmView = append(llmView, systemMsgs...)
	llmView = append(llmView, truncationNotice)
	llmView = append(llmView, thinnedHead...)
	llmView = append(llmView, thinnedTail...)

	// Session View：只含 user/assistant 消息
	sessionView := make([]llm.ChatMessage, 0, 1+len(thinnedHead)+len(thinnedTail))
	sessionView = append(sessionView, truncationNotice)
	for _, msg := range thinnedHead {
		if msg.Role == "user" || msg.Role == "assistant" {
			sessionView = append(sessionView, msg)
		}
	}
	sessionView = append(sessionView, extractDialogueFromTail(thinnedTail)...)

	return &CompressResult{
		LLMView:     llmView,
		SessionView: sessionView,
	}
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
