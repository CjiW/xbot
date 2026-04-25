package serverapp

import (
	"fmt"

	"xbot/config"
	log "xbot/logger"
)

// --- Parameter types for LLM RPCs ---

type setUserModelParams struct {
	Model string `json:"model"`
}

type setUserMaxContextParams struct {
	MaxContext int `json:"max_context"`
}

type setUserMaxOutputTokensParams struct {
	MaxTokens int `json:"max_tokens"`
}

type setUserThinkingModeParams struct {
	Mode string `json:"mode"`
}

type setLLMConcurrencyParams struct {
	Personal int `json:"personal"`
}

type setDefaultThinkingModeParams struct {
	Mode string `json:"mode"`
}

type setModelTiersParams = config.LLMConfig // reuse config struct directly

type setProxyLLMParams struct {
	Model string `json:"model"`
}

// --- Handlers ---

func (h *rpcContext) getDefaultModel() string {
	model := ""
	if subSvc := h.backend.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
		if sub, err := subSvc.GetDefault(h.bizID); err == nil && sub != nil && sub.Model != "" {
			model = sub.Model
		}
	}
	if model == "" {
		_, m, _, _ := h.backend.LLMFactory().GetLLM(h.bizID)
		model = m
	}
	log.WithField("sender_id", h.bizID).WithField("model", model).Debug("RPC get_default_model")
	return model
}

func (h *rpcContext) setUserModel(p setUserModelParams) error {
	return h.backend.SetUserModel(h.bizID, p.Model)
}

func (h *rpcContext) switchModel(p setUserModelParams) error {
	log.WithField("sender_id", h.bizID).WithField("model", p.Model).Info("RPC switch_model")
	h.backend.SwitchModel(h.bizID, p.Model)
	if subSvc := h.backend.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
		if sub, err := subSvc.GetDefault(h.bizID); err == nil && sub != nil {
			if err := subSvc.SetModel(sub.ID, p.Model); err != nil {
				log.WithError(err).Warn("RPC switch_model: SetModel failed")
			}
		}
	}
	return nil
}

func (h *rpcContext) getUserMaxContext() int {
	return h.backend.GetUserMaxContext(h.bizID)
}

func (h *rpcContext) setUserMaxContext(p setUserMaxContextParams) error {
	return h.backend.SetUserMaxContext(h.bizID, p.MaxContext)
}

func (h *rpcContext) getUserMaxOutputTokens() int {
	return h.backend.GetUserMaxOutputTokens(h.bizID)
}

func (h *rpcContext) setUserMaxOutputTokens(p setUserMaxOutputTokensParams) error {
	return h.backend.SetUserMaxOutputTokens(h.bizID, p.MaxTokens)
}

func (h *rpcContext) getUserThinkingMode() string {
	return h.backend.GetUserThinkingMode(h.bizID)
}

func (h *rpcContext) setUserThinkingMode(p setUserThinkingModeParams) error {
	return h.backend.SetUserThinkingMode(h.bizID, p.Mode)
}

func (h *rpcContext) getLLMConcurrency() int {
	return h.backend.GetLLMConcurrency(h.bizID)
}

func (h *rpcContext) setLLMConcurrency(p setLLMConcurrencyParams) error {
	return h.backend.SetLLMConcurrency(h.bizID, p.Personal)
}

func (h *rpcContext) setDefaultThinkingMode(p setDefaultThinkingModeParams) error {
	if h.backend.LLMFactory() == nil {
		return fmt.Errorf("LLM factory not available")
	}
	h.backend.LLMFactory().SetDefaultThinkingMode(p.Mode)
	return nil
}

func (h *rpcContext) listModels() ([]string, error) {
	if h.backend.LLMFactory() == nil {
		return nil, fmt.Errorf("LLM factory not available")
	}
	client, _, _, _ := h.backend.LLMFactory().GetLLM(h.bizID)
	return client.ListModels(), nil
}

func (h *rpcContext) listAllModels() ([]string, error) {
	if h.backend.LLMFactory() == nil {
		return nil, fmt.Errorf("LLM factory not available")
	}
	models := h.backend.LLMFactory().ListAllModelsForUser(h.bizID)
	log.WithField("count", len(models)).Debug("RPC list_all_models")
	return models, nil
}

func (h *rpcContext) setModelTiers(p setModelTiersParams) error {
	if h.backend.LLMFactory() == nil {
		return fmt.Errorf("LLM factory not available")
	}
	h.backend.LLMFactory().SetModelTiers(p)
	return nil
}

func (h *rpcContext) setProxyLLM(p setProxyLLMParams) error {
	if h.backend.LLMFactory() != nil {
		h.backend.LLMFactory().SwitchModel(h.bizID, p.Model)
	}
	return nil
}

func (h *rpcContext) clearProxyLLM() error {
	h.backend.ClearProxyLLM(h.bizID)
	return nil
}
