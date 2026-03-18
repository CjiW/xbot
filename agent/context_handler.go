package agent

import (
	"context"
	"fmt"

	"xbot/bus"
	"xbot/llm"
	"xbot/session"
)

// handleContext 处理 /context 命令：显示当前 token 数和组成
func (a *Agent) handleContext(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
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
	toolDefs := a.tools.AsDefinitionsForSession(sessionKey)
	toolDefsTokens, _ := llm.CountToolsTokens(toolDefs, model)

	// 按角色统计 token 数
	var systemTokens, userTokens, assistantTokens, toolMsgTokens int

	for _, m := range messages {
		tokens, err := llm.CountMessagesTokens([]llm.ChatMessage{m}, model)
		if err != nil {
			continue
		}
		switch m.Role {
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

	total := systemTokens + userTokens + assistantTokens + toolMsgTokens + toolDefsTokens
	threshold := int(float64(a.maxContextTokens) * a.compressionThreshold)

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
- 压缩阈值: %d tokens (%.0f%%)`,
		systemTokens, float64(systemTokens)*100/float64(max(total, 1)),
		userTokens, float64(userTokens)*100/float64(max(total, 1)),
		assistantTokens, float64(assistantTokens)*100/float64(max(total, 1)),
		toolMsgTokens, float64(toolMsgTokens)*100/float64(max(total, 1)),
		toolDefsTokens, float64(toolDefsTokens)*100/float64(max(total, 1)),
		total,
		a.maxContextTokens,
		threshold,
		a.compressionThreshold*100,
	)

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
	}, nil
}
