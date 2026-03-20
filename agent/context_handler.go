package agent

import (
	"context"
	"fmt"

	"xbot/bus"
	"xbot/llm"
	"xbot/session"
)

// handleContextInfo 处理 /context info 命令：显示当前 token 数和组成
func (a *Agent) handleContextInfo(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	_, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	// 使用 buildPrompt 获取完整上下文（包含 system、skills、memory 等）
	messages, err := a.buildPrompt(ctx, msg, tenantSession, false)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "获取上下文失败，请重试。",
		}, nil
	}

	// 获取工具定义并计算 token
	sessionKey := msg.Channel + ":" + msg.ChatID
	toolDefs := a.tools.AsDefinitionsForSession(sessionKey)
	toolDefsTokens, _ := llm.CountToolsTokens(toolDefs, model)

	// 通过 ContextManager 获取统计信息
	cm := a.GetContextManager()
	stats := cm.ContextInfo(messages, model, toolDefsTokens)

	content := fmt.Sprintf(`📊 上下文 Token 统计

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
			Content: "无效模式。可选: phase1, phase2, none, default",
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
