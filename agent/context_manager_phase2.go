package agent

import (
	"context"
	"fmt"

	"xbot/llm"
)

// phase2Manager Phase 2 三层渐进压缩管理器。
// 目前为空壳实现，Phase 2 实现时填充。
// NewContextManager() 中会自动降级到 Phase 1。
type phase2Manager struct {
	config *ContextManagerConfig
	// Phase 2 专用字段（未来实现）：
	// topicPartitioner  *TopicPartitioner
	// qualityChecker    *QualityChecker
	// progressiveLevels [3]CompressionLevel
}

func newPhase2Manager(cfg *ContextManagerConfig) *phase2Manager {
	return &phase2Manager{config: cfg}
}

func (m *phase2Manager) Mode() ContextMode { return ContextModePhase2 }

func (m *phase2Manager) ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool {
	// Phase 2 智能触发逻辑（未来实现）
	// 临时 fallback：与 Phase 1 相同的阈值判断
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

func (m *phase2Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	// TODO: Phase 2 三层渐进压缩实现
	return nil, fmt.Errorf("phase 2 compression not yet implemented")
}

func (m *phase2Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	// ManualCompress 契约：无论模式如何，都尽力执行。
	// Phase 2 未实现时，降级到 compressMessages（Phase 1 逻辑）。
	return compressMessages(ctx, messages, client, model)
}

func (m *phase2Manager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	// 复用 Phase 1 的统计逻辑（统计方式相同）
	return newPhase1Manager(m.config).ContextInfo(messages, model, toolTokens)
}

func (m *phase2Manager) SessionHook() SessionCompressHook { return nil }
