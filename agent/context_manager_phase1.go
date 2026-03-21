package agent

import (
	"context"

	"xbot/llm"
	log "xbot/logger"
)

// phase1Manager Phase 1 双视图压缩管理器，实现 SmartCompressor 接口。
// 压缩流程：LLM 摘要（不带 fingerprint）+ ineffective 检测 + mechanicalTruncate 兜底。
type phase1Manager struct {
	config   *ContextManagerConfig
	provider *TriggerInfoProvider
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

// Compress 压缩：LLM 压缩 + ineffective 检测 + mechanicalTruncate 兜底。
func (m *phase1Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	originalTokens, _ := llm.CountMessagesTokens(messages, model)

	log.Ctx(ctx).WithFields(map[string]interface{}{
		"original_tokens": originalTokens,
		"max_tokens":      m.config.MaxContextTokens,
	}).Info("Phase 1 compress: starting")

	// 步骤1：LLM 压缩（不带 fingerprint）
	result, err := compressMessages(ctx, messages, client, model)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Phase 1 compress: LLM compression failed, trying mechanical truncation")
		mechResult := m.mechanicalTruncate(messages, model)
		return mechResult, nil
	}

	// 步骤2：ineffective 检测 + mechanicalTruncate 兜底
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
		}).Warn("Phase 1 compress: ineffective (reduction<10%), trying mechanical truncation")

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
		}).Info("Phase 1 compress: mechanical truncation result")

		if mechReduction > reductionRate {
			result = mechResult
			reductionRate = mechReduction
		}
	}

	// 步骤3：质量报告
	finalTokens, _ := llm.CountMessagesTokens(result.LLMView, model)
	if originalTokens > 0 {
		reductionRate = 1.0 - float64(finalTokens)/float64(originalTokens)
	}
	log.Ctx(ctx).WithFields(map[string]interface{}{
		"reduction_rate": reductionRate,
		"new_tokens":     finalTokens,
	}).Info("Phase 1 compress quality report")

	return result, nil
}

// mechanicalTruncate 机械截断：当 LLM 压缩无效时的最后手段。
// 保留 system message + 最近 N 条消息，对更早的消息做激进截断。
// 保证：result tokens < original tokens * 0.5
func (m *phase1Manager) mechanicalTruncate(messages []llm.ChatMessage, model string) *CompressResult {
	originalTokens, _ := llm.CountMessagesTokens(messages, model)
	targetTokens := int(float64(originalTokens) * 0.5)

	// 找尾部切割点
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
	noticeTokens := 20

	headBudget := targetTokens - systemTokens - tailTokens - noticeTokens
	if headBudget < 0 {
		headBudget = 0
	}

	headTokens, _ := llm.CountMessagesTokens(thinnedHead, model)
	for headTokens > headBudget && len(thinnedHead) > 1 {
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

// ManualCompress 手动压缩（/compress 命令使用）。
func (m *phase1Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	return compressMessages(ctx, messages, client, model)
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
