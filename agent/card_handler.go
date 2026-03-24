package agent

import (
	"context"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
)

// handleCardResponse 处理卡片响应（按钮点击、表单提交）
func (a *Agent) handleCardResponse(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	cardID := msg.Metadata["card_id"]
	log.Ctx(ctx).WithFields(log.Fields{
		"channel": msg.Channel,
		"chat_id": msg.ChatID,
		"card_id": cardID,
	}).Info("Processing card response")

	// 注入卡片上下文，让 LLM 理解用户在回应什么
	summary := msg.Content
	if desc := a.cardBuilder.GetDescription(cardID); desc != "" {
		summary = desc + "\nUser interaction:\n" + summary
	}

	// Cleanup card metadata after callback is processed
	defer a.cardBuilder.CleanupCard(cardID)

	// 复用 buildPrompt，替换 Content 为卡片摘要
	cardMsg := msg
	cardMsg.Content = summary
	messages, err := a.buildPrompt(ctx, cardMsg, tenantSession)
	if err != nil {
		return nil, err
	}

	// 运行 Agent 循环（统一 Run）
	cardCfg := a.buildMainRunConfig(ctx, msg, messages, tenantSession, true)
	cardOut := Run(ctx, cardCfg)
	if cardOut.Error != nil {
		return nil, cardOut.Error
	}
	finalContent := cardOut.Content
	waitingUser := cardOut.WaitingUser

	if waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, skipping reply")
		return nil, nil
	}

	cardUserMsg := llm.NewUserMessage(summary)
	if !msg.Time.IsZero() {
		cardUserMsg.Timestamp = msg.Time
	}
	if err := tenantSession.AddMessage(cardUserMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save user message")
	}
	assistantMsg := llm.NewAssistantMessage(finalContent)
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save assistant message")
	}

	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent); err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to send card response via sendMessage")
		return &bus.OutboundMessage{
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Content:  finalContent,
			Metadata: msg.Metadata,
		}, nil
	}
	return nil, nil
}
