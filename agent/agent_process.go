package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
	"xbot/tools"
)

// injectSystemNotes appends runtime state notes (background tasks, interactive
// agents, active groups) to the last user message in the slice. The message
// struct is copied to avoid mutating session data.
func (a *Agent) injectSystemNotes(messages []llm.ChatMessage, channel, chatID string) []llm.ChatMessage {
	var systemNotes []string

	// Background tasks
	if a.bgTaskMgr != nil {
		sessionKey := channel + ":" + chatID
		running := a.bgTaskMgr.ListRunning(sessionKey)
		if len(running) > 0 {
			var ids []string
			for _, t := range running {
				ids = append(ids, t.ID)
			}
			systemNotes = append(systemNotes, fmt.Sprintf("Running background tasks: %s", strings.Join(ids, ", ")))
		}
	}

	// Interactive agent sessions
	sessions := a.ListInteractiveSessions(channel, chatID)
	if len(sessions) > 0 {
		var agentParts []string
		for _, s := range sessions {
			status := "idle"
			if s.Running {
				status = "running"
			}
			mode := "fg"
			if s.Background {
				mode = "bg"
			}
			agentParts = append(agentParts, fmt.Sprintf("%s/%s(%s,%s)", s.Role, s.Instance, mode, status))
		}
		systemNotes = append(systemNotes, fmt.Sprintf("Active interactive agents: %s", strings.Join(agentParts, ", ")))
	}

	// Active group chats
	groups := tools.ListGroups()
	if len(groups) > 0 {
		var groupParts []string
		for _, g := range groups {
			status := "open"
			if g.Closed {
				status = "closed"
			}
			members := strings.Join(g.Members, ",")
			groupParts = append(groupParts, fmt.Sprintf("%s(%s, %d members: %s)", g.Name, status, len(g.Members), members))
		}
		systemNotes = append(systemNotes, fmt.Sprintf("Groups: %s", strings.Join(groupParts, "; ")))
	}

	if len(systemNotes) > 0 {
		info := "\n[System] " + strings.Join(systemNotes, " | ")
		// Append to a copy of the last user message to avoid mutating session data
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				m := messages[i] // shallow copy
				m.Content += info
				messages[i] = m
				break
			}
		}
	}
	return messages
}

// wireBgNotificationDrain creates a DrainBgNotifications callback for Run()
// that returns only notifications matching the given session key, putting
// others back into the pending list to prevent cross-session contamination.
func (a *Agent) wireBgNotificationDrain(sessionKey string) func() []tools.BgNotification {
	return func() []tools.BgNotification {
		a.bgRunPendingMu.Lock()
		pending := a.bgRunPending
		a.bgRunPending = nil
		a.bgRunPendingMu.Unlock()

		var mine []tools.BgNotification
		var others []tools.BgNotification
		for _, n := range pending {
			if n.SessionKey() == sessionKey {
				mine = append(mine, n)
			} else {
				others = append(others, n)
			}
		}
		// Put other sessions' notifications back
		if len(others) > 0 {
			a.bgRunPendingMu.Lock()
			a.bgRunPending = append(a.bgRunPending, others...)
			a.bgRunPendingMu.Unlock()
		}
		return mine
	}
}

// drainRemainingBgNotifications drains any bg notifications that arrived
// after Run's last iteration and processes them as user messages (idle path).
func (a *Agent) drainRemainingBgNotifications() {
	a.bgRunPendingMu.Lock()
	remaining := a.bgRunPending
	a.bgRunPending = nil
	a.bgRunPendingMu.Unlock()
	for _, notif := range remaining {
		switch n := notif.(type) {
		case *tools.BackgroundTask:
			go a.processBgNotification(n)
		case *tools.SubAgentBgNotify:
			go a.processSubAgentBgNotification(n)
		}
	}
}

// handleCancelledRun persists un-saved engine messages and iteration history
// when a Run is cancelled, then returns a minimal OutboundMessage so the
// channel knows processing ended.
func (a *Agent) handleCancelledRun(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	// Save any un-persisted engine messages from the interrupted iteration.
	for _, em := range out.EngineMessages {
		if err := assertNoSystemPersist(em); err != nil {
			continue
		}
		if err := tenantSession.AddMessage(em); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save engine message on cancel")
		}
	}
	if len(out.EngineMessages) > 0 {
		log.Ctx(ctx).Infof("Cancelled: persisted %d un-persisted engine messages", len(out.EngineMessages))
	}
	// Save iteration history as an assistant message with detail,
	// so web UI can restore it on page refresh without showing "loading".
	if len(out.IterationHistory) > 0 {
		cancelMsg := llm.NewAssistantMessage("")
		cancelMsg.DisplayOnly = true
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			cancelMsg.Detail = string(jsonBytes)
		}
		if err := tenantSession.AddMessage(cancelMsg); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to save cancelled iteration history")
		}
	}
	// Send a minimal outbound so the web channel knows processing ended.
	meta := map[string]string{"cancelled": "true"}
	if len(out.IterationHistory) > 0 {
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			meta["progress_history"] = string(jsonBytes)
		}
	}
	return &bus.OutboundMessage{
		Channel:  msg.Channel,
		ChatID:   msg.ChatID,
		Content:  "",
		Metadata: meta,
	}, nil
}

// handleRunOutput processes the successful result of a Run() call:
// - WaitingUser: send WaitingUser outbound
// - Empty content with mandatory reply: send warning
// - Empty content with optional reply: clear progress state
// - Normal: persist assistant message, send, add reaction
func (a *Agent) handleRunOutput(ctx context.Context, msg bus.InboundMessage, out *RunOutput, tenantSession *session.TenantSession, replyPolicy string) (*bus.OutboundMessage, error) {
	finalContent := out.Content
	waitingUser := out.WaitingUser

	// If a tool is waiting for user response, send WaitingUser outbound
	if waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, sending WaitingUser outbound")
		meta := map[string]string{}
		for k, v := range out.Metadata {
			meta[k] = v
		}
		waitOut := &bus.OutboundMessage{
			Channel:     msg.Channel,
			ChatID:      msg.ChatID,
			Content:     finalContent,
			WaitingUser: true,
			Metadata:    meta,
		}
		return waitOut, nil
	}

	// Empty content without waiting for user and not optional reply
	if finalContent == "" && replyPolicy != bus.ReplyPolicyOptional {
		log.Ctx(ctx).Warn("Run produced empty content without waiting for user input")
		if err := a.sendMessage(msg.Channel, msg.ChatID, "⚠️ 处理完成，但未生成回复内容。请尝试重新描述您的需求。"); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to send empty content notification")
		}
		return nil, nil
	}

	if finalContent == "" && replyPolicy == bus.ReplyPolicyOptional {
		log.Ctx(ctx).WithFields(log.Fields{
			"channel":      msg.Channel,
			"chat_id":      msg.ChatID,
			"reply_policy": replyPolicy,
		}).Info("Optional reply policy: no final response generated, skipping outbound")
		// Send an empty outbound to clear TUI progress state.
		if ch, ok := a.channelFinder(msg.Channel); ok {
			ch.Send(bus.OutboundMessage{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "",
			})
		}
		return nil, nil
	}

	// Persist the final assistant reply
	assistantMsg := llm.NewAssistantMessage(finalContent)
	assistantMsg.ReasoningContent = out.ReasoningContent
	if len(out.IterationHistory) > 0 {
		if jsonBytes, err := json.Marshal(out.IterationHistory); err == nil {
			assistantMsg.Detail = string(jsonBytes)
		}
	}
	if err := tenantSession.AddMessage(assistantMsg); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to save assistant message")
	}

	// Send via sendMessage (reuses session message tracking)
	sendMeta := map[string]string{}
	if assistantMsg.Detail != "" {
		sendMeta["progress_history"] = assistantMsg.Detail
	}
	if err := a.sendMessage(msg.Channel, msg.ChatID, finalContent, sendMeta); err != nil {
		log.Ctx(ctx).WithError(err).Error("Failed to send final response via sendMessage")
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: finalContent,
		}, nil
	}

	// Add reaction to user's original message
	a.addReaction(msg)

	return nil, nil
}
