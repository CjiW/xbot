package agent

import (
	"context"

	"xbot/llm"
)

// phase1Manager Phase 1 双视图压缩管理器。
// 封装现有 compress.go 中的逻辑，行为与现有完全一致。
// 不持有 *Agent 引用，仅依赖配置和独立函数。
type phase1Manager struct {
	config *ContextManagerConfig
}

func newPhase1Manager(cfg *ContextManagerConfig) *phase1Manager {
	return &phase1Manager{config: cfg}
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

func (m *phase1Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	return compressMessages(ctx, messages, client, model)
}

func (m *phase1Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	return compressMessages(ctx, messages, client, model)
}

func (m *phase1Manager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	cfg := m.config
	var systemTokens, userTokens, assistantTokens, toolMsgTokens int

	for _, msg := range messages {
		tokens, err := llm.CountMessagesTokens([]llm.ChatMessage{msg}, model)
		if err != nil {
			continue
		}
		switch msg.Role {
		case "system":
			systemTokens += tokens
		case "user":
			userTokens += tokens
		case "assistant":
			assistantTokens += tokens
		case "tool":
			toolMsgTokens += tokens
		}
	}

	total := systemTokens + userTokens + assistantTokens + toolMsgTokens + toolTokens
	threshold := int(float64(cfg.MaxContextTokens) * cfg.CompressionThreshold)

	return &ContextStats{
		SystemTokens:    systemTokens,
		UserTokens:      userTokens,
		AssistantTokens: assistantTokens,
		ToolMsgTokens:   toolMsgTokens,
		ToolDefTokens:   toolTokens,
		TotalTokens:     total,
		MaxTokens:       cfg.MaxContextTokens,
		Threshold:       threshold,
		Mode:            cfg.EffectiveMode(),
		IsRuntimeOverride: cfg.RuntimeMode() != "",
		DefaultMode:     cfg.DefaultMode,
	}
}

func (m *phase1Manager) SessionHook() SessionCompressHook { return nil }
