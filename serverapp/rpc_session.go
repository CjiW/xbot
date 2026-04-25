package serverapp

import (
	"context"
	"fmt"
	"time"

	"xbot/agent"
	log "xbot/logger"
)

// --- Parameter types ---

type channelChatIDParams struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

type clearMemoryParams struct {
	Channel    string `json:"channel"`
	ChatID     string `json:"chat_id"`
	TargetType string `json:"target_type"`
}

type dailyTokenUsageParams struct {
	Days     int    `json:"days"`
	SenderID string `json:"sender_id"`
}

type inspectSessionParams struct {
	Role      string `json:"role"`
	Channel   string `json:"channel"`
	ChatID    string `json:"chat_id"`
	Instance  string `json:"instance"`
	TailCount int    `json:"tail_count"`
}

type sessionMessagesParams struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	Role     string `json:"role"`
	Instance string `json:"instance"`
}

type sessionDumpByFullKeyParams struct {
	FullKey string `json:"full_key"`
}

type historyParams struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

type trimHistoryParams struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
	Cutoff  string `json:"cutoff"`
}

// --- Handlers ---

func (h *rpcContext) clearMemory(p clearMemoryParams) error {
	if h.backend.MultiSession() == nil {
		return fmt.Errorf("multi-session not available")
	}
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return err
	}
	if p.ChatID == "" {
		p.ChatID = h.bizID
	}
	return h.backend.MultiSession().ClearMemory(context.Background(), p.Channel, p.ChatID, p.TargetType, h.bizID)
}

func (h *rpcContext) getMemoryStats(p channelChatIDParams) (interface{}, error) {
	if h.backend.MultiSession() == nil {
		return nil, fmt.Errorf("multi-session not available")
	}
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return nil, err
	}
	if p.ChatID == "" {
		p.ChatID = h.bizID
	}
	return h.backend.MultiSession().GetMemoryStats(context.Background(), p.Channel, p.ChatID, h.bizID), nil
}

func (h *rpcContext) getUserTokenUsage() (interface{}, error) {
	if h.backend.MultiSession() == nil {
		return nil, fmt.Errorf("multi-session not available")
	}
	return h.backend.MultiSession().GetUserTokenUsage(h.bizID)
}

func (h *rpcContext) getDailyTokenUsage(p dailyTokenUsageParams) (interface{}, error) {
	if h.backend.MultiSession() == nil {
		return nil, fmt.Errorf("multi-session not available")
	}
	return h.backend.MultiSession().GetDailyTokenUsage(h.bizID, p.Days)
}

func (h *rpcContext) countInteractiveSessions(p channelChatIDParams) (int, error) {
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return 0, err
	}
	if !isAdmin(h.authSenderID) && p.ChatID == "" {
		p.ChatID = h.bizID
	}
	return h.backend.CountInteractiveSessions(p.Channel, p.ChatID), nil
}

func (h *rpcContext) listInteractiveSessions(p channelChatIDParams) (interface{}, error) {
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return nil, err
	}
	if !isAdmin(h.authSenderID) && p.ChatID == "" {
		p.ChatID = h.bizID
	}
	return h.backend.ListInteractiveSessions(p.Channel, p.ChatID), nil
}

func (h *rpcContext) inspectInteractiveSession(p inspectSessionParams) (string, error) {
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return "", err
	}
	if p.ChatID == "" {
		p.ChatID = h.bizID
	}
	return h.backend.InspectInteractiveSession(context.Background(), p.Role, p.Channel, p.ChatID, p.Instance, p.TailCount)
}

func (h *rpcContext) getSessionMessages(p sessionMessagesParams) (interface{}, error) {
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return nil, err
	}
	if p.ChatID == "" {
		p.ChatID = h.bizID
	}
	msgs, _ := h.backend.GetSessionMessages(p.Channel, p.ChatID, p.Role, p.Instance)
	if msgs == nil {
		msgs = []agent.SessionMessage{}
	}
	return msgs, nil
}

func (h *rpcContext) getAgentSessionDump(p sessionMessagesParams) (interface{}, error) {
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return nil, err
	}
	if p.ChatID == "" {
		p.ChatID = h.bizID
	}
	dump, _ := h.backend.GetAgentSessionDump(p.Channel, p.ChatID, p.Role, p.Instance)
	if dump == nil {
		dump = &agent.AgentSessionDump{}
	}
	return dump, nil
}

func (h *rpcContext) getAgentSessionDumpByFullKey(p sessionDumpByFullKeyParams) (interface{}, error) {
	if p.FullKey == "" {
		return nil, fmt.Errorf("full_key is required")
	}
	if owner := sessionKeyOwner(p.FullKey); owner != "" {
		if !isAdmin(h.authSenderID) && owner != h.bizID {
			return nil, fmt.Errorf("access denied")
		}
	}
	dump, _ := h.backend.GetAgentSessionDumpByFullKey(p.FullKey)
	if dump == nil {
		dump = &agent.AgentSessionDump{}
	}
	return dump, nil
}

func (h *rpcContext) getHistory(p historyParams) (interface{}, error) {
	if p.Channel == "" {
		p.Channel = "web"
	}
	if p.ChatID == "" {
		p.ChatID = h.bizID
	}
	if !isAdmin(h.authSenderID) && p.ChatID != h.bizID && p.Channel != "agent" {
		return nil, fmt.Errorf("access denied")
	}
	history, err := h.backend.GetHistory(p.Channel, p.ChatID)
	if err != nil {
		return nil, err
	}
	log.WithFields(log.Fields{"channel": p.Channel, "chat_id": p.ChatID, "count": len(history), "rpc_sender": h.authSenderID}).Info("RPC get_history")
	return history, nil
}

func (h *rpcContext) trimHistory(p trimHistoryParams) error {
	if p.Channel == "" {
		p.Channel = "web"
	}
	if p.ChatID == "" {
		p.ChatID = h.bizID
	}
	if !isAdmin(h.authSenderID) && p.ChatID != h.bizID {
		return fmt.Errorf("access denied")
	}
	var cutoff time.Time
	if p.Cutoff != "" {
		var err error
		cutoff, err = time.Parse(time.RFC3339, p.Cutoff)
		if err != nil {
			return fmt.Errorf("invalid cutoff format: %w", err)
		}
	}
	return h.backend.TrimHistory(p.Channel, p.ChatID, cutoff)
}

func (h *rpcContext) isProcessing(p channelChatIDParams) (bool, error) {
	if p.Channel == "" {
		p.Channel = "web"
	}
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return false, err
	}
	return h.backend.IsProcessing(p.Channel, p.ChatID), nil
}

func (h *rpcContext) getActiveProgress(p channelChatIDParams) (interface{}, error) {
	if p.Channel == "" {
		p.Channel = "web"
	}
	if !isAdmin(h.authSenderID) && p.ChatID != h.bizID && p.Channel != "agent" {
		return nil, fmt.Errorf("access denied")
	}
	return h.backend.GetActiveProgress(p.Channel, p.ChatID), nil
}

func (h *rpcContext) getChannelConfigs() (interface{}, error) {
	return h.backend.GetChannelConfigs()
}
