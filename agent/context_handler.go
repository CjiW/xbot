package agent

import (
	"context"
	"fmt"

	"xbot/bus"
	"xbot/llm"
	"xbot/session"
)

// formatTokenCount formats a token count for display (e.g. 1234567 → "1.2M").
func formatTokenCount(n int64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// handleContextInfo 处理 /context info 命令：显示当前 token 数和组成
func (a *Agent) handleContextInfo(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	_, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	// 使用 buildPrompt 获取完整上下文（包含 system、skills、memory 等）
	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取上下文失败，请重试。",
		}, nil
	}

	// 获取工具定义并计算 token
	sessionKey := msg.Channel + ":" + msg.ChatID
	toolDefs := visibleToolDefs(a.tools.AsDefinitionsForSession(sessionKey), a.settingsSvc, msg.Channel, msg.SenderID)
	toolDefsTokens, _ := llm.CountToolsTokens(toolDefs, model)

	// Prefer API-returned prompt_tokens (authoritative) over local estimation.
	apiTokens := a.lastPromptTokens.Load()
	cm := a.GetContextManager()
	stats := cm.ContextInfo(messages, model, toolDefsTokens)

	// Override total with API value if available
	tokenSource := "估算"
	if apiTokens > 0 {
		stats.TotalTokens = int(apiTokens)
		tokenSource = "API"
	}

	content := fmt.Sprintf(`📊 上下文 Token 统计 (来源: %s)

| 角色 | Token | 占比 |
|------|-------|------|
| System | %d | %.1f%% |
| User | %d | %.1f%% |
| Assistant | %d | %.1f%% |
| Tool (消息) | %d | %.1f%% |
| Tool (定义) | %d | %.1f%% |
| **总计** | **%d** | 100%% |

⚙️ 配置:
- 最大上下文: %d tokens
- 压缩阈值: %d tokens (%.0f%%)
- 当前模式: %s`,
		tokenSource,
		stats.SystemTokens, float64(stats.SystemTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.UserTokens, float64(stats.UserTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.AssistantTokens, float64(stats.AssistantTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.ToolMsgTokens, float64(stats.ToolMsgTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.ToolDefTokens, float64(stats.ToolDefTokens)*100/float64(max(stats.TotalTokens, 1)),
		stats.TotalTokens,
		stats.MaxTokens,
		stats.Threshold,
		a.contextManagerConfig.CompressionThreshold*100,
		stats.Mode,
	)

	// 运行时覆盖信息
	if stats.IsRuntimeOverride {
		content += fmt.Sprintf("（运行时覆盖，默认为 %s）", stats.DefaultMode)
	}

	// Per-user cumulative token usage
	if a.multiSession != nil {
		usage, err := a.multiSession.GetUserTokenUsage(msg.SenderID)
		if err == nil && usage.TotalTokens > 0 {
			content += fmt.Sprintf(`

👤 用户累计用量 (%s):
- 总 Token: %s
  (输入 %s · 输出 %s)
- 对话轮次: %d
- LLM 调用: %d`,
				usage.SenderID,
				formatTokenCount(usage.TotalTokens),
				formatTokenCount(usage.InputTokens),
				formatTokenCount(usage.OutputTokens),
				usage.ConversationCount,
				usage.LLMCallCount,
			)
		}
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
	}, nil
}

// handleContextMode 处理 /context mode 子命令
func (a *Agent) handleContextMode(ctx context.Context, msg bus.InboundMessage, modeStr string) (*bus.OutboundMessage, error) {
	cfg := a.contextManagerConfig

	if modeStr == "" {
		// 仅查询当前模式
		stats := a.GetContextManager().ContextInfo(nil, "", 0)
		overrideInfo := ""
		if stats.IsRuntimeOverride {
			overrideInfo = fmt.Sprintf("（运行时覆盖，默认为 %s）", stats.DefaultMode)
		}
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("当前上下文模式: %s %s", cfg.EffectiveMode(), overrideInfo),
		}, nil
	}

	target := ContextMode(modeStr)
	if target == "default" {
		cfg.ResetRuntimeMode()
		a.SetContextManager(NewContextManager(cfg))
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("已恢复默认上下文模式: %s", cfg.DefaultMode),
		}, nil
	}

	if !IsValidContextMode(target) {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "无效模式。可选: phase1, none, default",
		}, nil
	}

	// 先设置配置，再替换 manager
	cfg.SetRuntimeMode(target)
	a.SetContextManager(NewContextManager(cfg))

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("已切换上下文模式: %s", target),
	}, nil
}
