package serverapp

import (
	"fmt"
	"strings"

	"xbot/channel"
	log "xbot/logger"
	"xbot/storage/sqlite"
)

// --- Parameter types for subscription RPCs ---

type addSubscriptionParams struct {
	Sub sqlite.LLMSubscription `json:"sub"`
}

type updateSubscriptionParams struct {
	ID  string                 `json:"id"`
	Sub sqlite.LLMSubscription `json:"sub"`
}

type removeSubscriptionParams struct {
	ID string `json:"id"`
}

type setDefaultSubscriptionParams struct {
	ID     string `json:"id"`
	ChatID string `json:"chat_id"`
}

type renameSubscriptionParams struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type setSubscriptionModelParams struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}

// --- Helpers ---

// subToChannel converts a DB subscription to the channel-facing type (with masked API key).
func subToChannel(s *sqlite.LLMSubscription) channel.Subscription {
	return channel.Subscription{
		ID: s.ID, Name: s.Name, Provider: s.Provider,
		BaseURL: s.BaseURL, APIKey: maskAPIKey(s.APIKey),
		Model: s.Model, Active: s.IsDefault,
		MaxOutputTokens: s.MaxOutputTokens, ThinkingMode: s.ThinkingMode,
	}
}

func (h *rpcContext) requireLLMFactory() error {
	if h.backend.LLMFactory() == nil {
		return fmt.Errorf("LLM factory not available")
	}
	return nil
}

func (h *rpcContext) requireSubscriptionSvc() (*sqlite.LLMSubscriptionService, error) {
	if err := h.requireLLMFactory(); err != nil {
		return nil, err
	}
	svc := h.backend.LLMFactory().GetSubscriptionSvc()
	if svc == nil {
		return nil, fmt.Errorf("subscription service not available")
	}
	return svc, nil
}

// --- Handlers ---

func (h *rpcContext) listSubscriptions() ([]channel.Subscription, error) {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return []channel.Subscription{}, nil
	}
	subs, err := svc.List(h.bizID)
	if err != nil {
		return nil, err
	}
	result := make([]channel.Subscription, len(subs))
	for i, s := range subs {
		result[i] = subToChannel(s)
	}
	return result, nil
}

func (h *rpcContext) getDefaultSubscription() (*channel.Subscription, error) {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return nil, nil
	}
	sub, err := svc.GetDefault(h.bizID)
	if err != nil {
		log.WithError(err).WithField("biz_id", h.bizID).Error("[RPC] get_default_subscription: GetDefault error")
		return nil, err
	}
	if sub == nil {
		log.WithField("biz_id", h.bizID).Warn("[RPC] get_default_subscription: no default subscription")
		return nil, nil
	}
	ch := subToChannel(sub)
	return &ch, nil
}

func (h *rpcContext) addSubscription(p addSubscriptionParams) error {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	p.Sub.SenderID = h.bizID
	return svc.Add(&p.Sub)
}

func (h *rpcContext) updateSubscription(p updateSubscriptionParams) error {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	existing, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(h.authSenderID) && existing.SenderID != h.bizID {
		return fmt.Errorf("subscription not found")
	}
	p.Sub.ID = p.ID
	p.Sub.SenderID = existing.SenderID
	p.Sub.IsDefault = existing.IsDefault // preserve is_default
	if strings.HasSuffix(p.Sub.APIKey, "****") && len(p.Sub.APIKey) <= 20 {
		log.WithField("sub_id", p.ID).Warn("[RPC] update_subscription: preserving existing API key (received masked)")
		p.Sub.APIKey = existing.APIKey
	}
	if err := svc.Update(&p.Sub); err != nil {
		return err
	}
	h.backend.LLMFactory().Invalidate(existing.SenderID)
	if existing.IsDefault {
		h.backend.LLMFactory().SwitchSubscription(h.bizID, &p.Sub, "")
	}
	return nil
}

func (h *rpcContext) removeSubscription(p removeSubscriptionParams) error {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(h.authSenderID) && sub.SenderID != h.bizID {
		return fmt.Errorf("subscription not found")
	}
	if err := svc.Remove(p.ID); err != nil {
		return err
	}
	h.backend.LLMFactory().Invalidate(sub.SenderID)
	return nil
}

func (h *rpcContext) setDefaultSubscription(p setDefaultSubscriptionParams) error {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(h.authSenderID) && sub.SenderID != h.bizID {
		return fmt.Errorf("subscription not found")
	}
	if err := svc.SetDefault(p.ID); err != nil {
		return err
	}
	h.backend.LLMFactory().Invalidate(h.bizID)
	if err := h.backend.LLMFactory().SwitchSubscription(h.bizID, sub, p.ChatID); err != nil {
		return err
	}
	return nil
}

func (h *rpcContext) renameSubscription(p renameSubscriptionParams) error {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(h.authSenderID) && sub.SenderID != h.bizID {
		return fmt.Errorf("subscription not found")
	}
	return svc.Rename(p.ID, p.Name)
}

func (h *rpcContext) setSubscriptionModel(p setSubscriptionModelParams) error {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(h.authSenderID) && sub.SenderID != h.bizID {
		return fmt.Errorf("subscription not found")
	}
	if err := svc.SetModel(p.ID, p.Model); err != nil {
		return err
	}
	updated, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if updated != nil {
		def, _ := svc.GetDefault(updated.SenderID)
		if def != nil && def.ID == updated.ID {
			h.backend.LLMFactory().Invalidate(updated.SenderID)
			if err := h.backend.LLMFactory().SwitchSubscription(updated.SenderID, updated, ""); err != nil {
				return err
			}
		}
	}
	return nil
}
