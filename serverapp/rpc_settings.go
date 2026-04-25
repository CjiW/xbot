package serverapp

import (
	"fmt"
)

var errSettingsUnavailable = fmt.Errorf("settings service not available")

// --- Parameter types for settings RPCs ---

type setCWDParams struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
	Dir     string `json:"dir"`
}

type getSettingsParams struct {
	Namespace string `json:"namespace"`
	SenderID  string `json:"sender_id"`
}

type setSettingParams struct {
	Namespace string `json:"namespace"`
	SenderID  string `json:"sender_id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
}

type setContextModeParams struct {
	Mode string `json:"mode"`
}

type setNParams struct {
	N int `json:"n"`
}

// --- Handlers ---

func (h *rpcContext) getContextMode() string {
	return h.backend.GetContextMode()
}

func (h *rpcContext) setContextMode(p setContextModeParams) error {
	return h.backend.SetContextMode(p.Mode)
}

func (h *rpcContext) setCWD(p setCWDParams) (interface{}, error) {
	if err := h.ownOrAdmin(p.ChatID); err != nil {
		return nil, err
	}
	return nil, h.backend.SetCWD(p.Channel, p.ChatID, p.Dir)
}

func (h *rpcContext) getSettings(p getSettingsParams) (interface{}, error) {
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(h.cfg, h.backend, p.Namespace, h.bizID); err != nil {
		return nil, err
	}
	if h.backend.SettingsService() == nil {
		return nil, errSettingsUnavailable
	}
	result, err := h.backend.SettingsService().GetSettings(p.Namespace, h.bizID)
	if err != nil {
		return nil, err
	}
	// Remove LLM keys from settings response — they come from user_llm_subscriptions.
	delete(result, "llm_provider")
	delete(result, "llm_api_key")
	delete(result, "llm_model")
	delete(result, "llm_base_url")
	return result, nil
}

func (h *rpcContext) setSetting(p setSettingParams) error {
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(h.cfg, h.backend, p.Namespace, h.bizID); err != nil {
		return err
	}
	// LLM fields are managed exclusively via update_subscription RPC.
	switch p.Key {
	case "llm_provider", "llm_api_key", "llm_model", "llm_base_url":
		return nil
	}
	if h.backend.SettingsService() == nil {
		return errSettingsUnavailable
	}
	if err := h.backend.SettingsService().SetSetting(p.Namespace, h.bizID, p.Key, p.Value); err != nil {
		return err
	}
	// Apply runtime changes for admin
	if isAdmin(h.authSenderID) {
		applyRuntimeSetting(h.cfg, h.backend, h.bizID, p.Key, p.Value)
	}
	return nil
}

func (h *rpcContext) setMaxIterations(p setNParams) error {
	h.backend.SetMaxIterations(p.N)
	return nil
}

func (h *rpcContext) setMaxConcurrency(p setNParams) error {
	h.backend.SetMaxConcurrency(p.N)
	return nil
}

func (h *rpcContext) setMaxContextTokens(p setNParams) error {
	h.backend.SetMaxContextTokens(p.N)
	return nil
}
