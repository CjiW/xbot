package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	log "xbot/logger"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// rpcContext holds shared dependencies for RPC handlers.
// It is created once at server startup and reused for every request.
// Per-request identity (authSenderID, bizID) is passed via context.Context,
// NOT stored here — this avoids rebuilding the entire table per request.
type rpcContext struct {
	cfg     *config.Config
	backend agent.AgentBackend
	disp    *channel.Dispatcher
	msgBus  *bus.MessageBus
}

func (h *rpcContext) requireAdmin(next rpcHandler) rpcHandler {
	return func(ctx context.Context, params json.RawMessage) (json.RawMessage, error) {
		if !isAdmin(rpcAuthID(ctx)) {
			return nil, fmt.Errorf("admin only")
		}
		return next(ctx, params)
	}
}

// ownOrAdmin checks that the caller owns the resource or has admin privileges.
// Empty chatID is treated as self-access (defaults to caller's bizID).
func ownOrAdmin(ctx context.Context, chatID string) error {
	if isAdmin(rpcAuthID(ctx)) || chatID == "" || chatID == rpcBizID(ctx) {
		return nil
	}
	return fmt.Errorf("access denied")
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

func (h *rpcContext) requireMultiSession() error {
	if h.backend.MultiSession() == nil {
		return fmt.Errorf("multi-session not available")
	}
	return nil
}

// resolveChatID checks ownership and defaults empty chatID to the caller's bizID.
func resolveChatID(ctx context.Context, chatID string) (string, error) {
	if err := ownOrAdmin(ctx, chatID); err != nil {
		return "", err
	}
	if chatID == "" {
		return rpcBizID(ctx), nil
	}
	return chatID, nil
}

// buildRPCTable constructs the complete RPC dispatch table.
// The table is built once at startup and reused for every request;
// per-request identity is injected via context, so no authSenderID/bizID is needed here.
func buildRPCTable(cfg *config.Config, backend agent.AgentBackend, disp *channel.Dispatcher, msgBus *bus.MessageBus) rpcTable {
	h := &rpcContext{cfg: cfg, backend: backend, disp: disp, msgBus: msgBus}
	t := make(rpcTable, 70)
	registerSettingsHandlers(t, h)
	registerLLMHandlers(t, h)
	registerSubscriptionHandlers(t, h)
	registerSessionHandlers(t, h)
	registerTaskHandlers(t, h)
	registerAdminHandlers(t, h)
	return t
}

// ── Context / settings / cwd / max-iterations / concurrency / context-tokens ──

func registerSettingsHandlers(t rpcTable, h *rpcContext) {
	t["get_context_mode"] = rpc0(func(ctx context.Context) any {
		return h.backend.GetContextMode()
	})
	t["set_context_mode"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Mode string `json:"mode"`
	}) error {
		return h.backend.SetContextMode(p.Mode)
	}))
	t["set_cwd"] = rpc1void(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
		Dir     string `json:"dir"`
	}) error {
		if err := ownOrAdmin(ctx, p.ChatID); err != nil {
			return err
		}
		return h.backend.SetCWD(p.Channel, p.ChatID, p.Dir)
	})
	t["get_settings"] = rpc1(func(ctx context.Context, p struct {
		Namespace string `json:"namespace"`
		SenderID  string `json:"sender_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if err := migrateCLIUserSettingsFromGlobalIfNeeded(h.cfg, h.backend, p.Namespace, bizID); err != nil {
			return nil, err
		}
		if h.backend.SettingsService() == nil {
			return nil, errSettingsUnavailable
		}
		result, err := h.backend.SettingsService().GetSettings(p.Namespace, bizID)
		if err != nil {
			return nil, err
		}
		for _, k := range []string{"llm_provider", "llm_api_key", "llm_model", "llm_base_url"} {
			delete(result, k)
		}
		// Inject config defaults for keys not present in user_settings.
		// This ensures remote CLI clients see the actual runtime values
		// (e.g. max_context_tokens=200000) even when the user never
		// explicitly saved those settings.
		if _, ok := result["max_context_tokens"]; !ok {
			result["max_context_tokens"] = fmt.Sprintf("%d", h.cfg.Agent.MaxContextTokens)
		}
		if _, ok := result["max_iterations"]; !ok {
			result["max_iterations"] = fmt.Sprintf("%d", h.cfg.Agent.MaxIterations)
		}
		if _, ok := result["max_concurrency"]; !ok {
			result["max_concurrency"] = fmt.Sprintf("%d", h.cfg.Agent.MaxConcurrency)
		}
		if _, ok := result["context_mode"]; !ok {
			result["context_mode"] = h.cfg.Agent.ContextMode
		}
		return result, nil
	})
	t["set_setting"] = rpc1void(func(ctx context.Context, p struct {
		Namespace string `json:"namespace"`
		SenderID  string `json:"sender_id"`
		Key       string `json:"key"`
		Value     string `json:"value"`
	}) error {
		bizID := rpcBizID(ctx)
		if err := migrateCLIUserSettingsFromGlobalIfNeeded(h.cfg, h.backend, p.Namespace, bizID); err != nil {
			return err
		}
		switch p.Key {
		case "llm_provider", "llm_api_key", "llm_model", "llm_base_url":
			return nil
		}
		// Global-scoped keys (sandbox_mode) are server-level config, not per-user.
		// Apply runtime effect but don't persist to user_settings DB —
		// the source of truth is config.json.
		if channel.IsGlobalScopedSettingKey(p.Key) {
			if isAdmin(rpcAuthID(ctx)) {
				applyRuntimeSetting(h.cfg, h.backend, bizID, p.Key, p.Value)
			}
			return nil
		}
		if h.backend.SettingsService() == nil {
			return errSettingsUnavailable
		}
		if err := h.backend.SettingsService().SetSetting(p.Namespace, bizID, p.Key, p.Value); err != nil {
			return err
		}
		if isAdmin(rpcAuthID(ctx)) {
			applyRuntimeSetting(h.cfg, h.backend, bizID, p.Key, p.Value)
		}
		return nil
	})

	// ── Max iterations / concurrency / context tokens ──
	t["set_max_iterations"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		N int `json:"n"`
	}) error {
		h.backend.SetMaxIterations(p.N)
		return nil
	}))
	t["set_max_concurrency"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		N int `json:"n"`
	}) error {
		h.backend.SetMaxConcurrency(p.N)
		return nil
	}))
	t["set_max_context_tokens"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		N int `json:"n"`
	}) error {
		h.backend.SetMaxContextTokens(p.N)
		return nil
	}))
}

// ── LLM / model / tier / proxy handlers ──

func registerLLMHandlers(t rpcTable, h *rpcContext) {
	backend := h.backend
	t["get_default_model"] = rpc0(func(ctx context.Context) string {
		bizID := rpcBizID(ctx)
		model := ""
		if subSvc := backend.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
			if sub, err := subSvc.GetDefault(bizID); err == nil && sub != nil && sub.Model != "" {
				model = sub.Model
			}
		}
		if model == "" {
			_, m, _, _ := backend.LLMFactory().GetLLM(bizID)
			model = m
		}
		log.WithField("sender_id", bizID).WithField("model", model).Debug("RPC get_default_model")
		return model
	})
	t["set_user_model"] = rpc1void(func(ctx context.Context, p struct {
		Model string `json:"model"`
	}) error {
		return backend.SetUserModel(rpcBizID(ctx), p.Model)
	})
	t["switch_model"] = rpc1void(func(ctx context.Context, p struct {
		Model string `json:"model"`
	}) error {
		bizID := rpcBizID(ctx)
		log.WithField("sender_id", bizID).WithField("model", p.Model).Info("RPC switch_model")
		backend.SwitchModel(bizID, p.Model)
		if subSvc := backend.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
			if sub, err := subSvc.GetDefault(bizID); err == nil && sub != nil {
				if err := subSvc.SetModel(sub.ID, p.Model); err != nil {
					log.WithError(err).Warn("RPC switch_model: SetModel failed")
				}
			}
		}
		return nil
	})
	t["get_user_max_context"] = rpc0(func(ctx context.Context) int { return backend.GetUserMaxContext(rpcBizID(ctx)) })
	t["set_user_max_context"] = rpc1void(func(ctx context.Context, p struct {
		MaxContext int `json:"max_context"`
	}) error {
		return backend.SetUserMaxContext(rpcBizID(ctx), p.MaxContext)
	})
	t["get_user_max_output_tokens"] = rpc0(func(ctx context.Context) int { return backend.GetUserMaxOutputTokens(rpcBizID(ctx)) })
	t["set_user_max_output_tokens"] = rpc1void(func(ctx context.Context, p struct {
		MaxTokens int `json:"max_tokens"`
	}) error {
		return backend.SetUserMaxOutputTokens(rpcBizID(ctx), p.MaxTokens)
	})
	t["get_user_thinking_mode"] = rpc0(func(ctx context.Context) string { return backend.GetUserThinkingMode(rpcBizID(ctx)) })
	t["set_user_thinking_mode"] = rpc1void(func(ctx context.Context, p struct {
		Mode string `json:"mode"`
	}) error {
		return backend.SetUserThinkingMode(rpcBizID(ctx), p.Mode)
	})
	t["get_llm_concurrency"] = rpc0(func(ctx context.Context) int { return backend.GetLLMConcurrency(rpcBizID(ctx)) })
	t["set_llm_concurrency"] = rpc1void(func(ctx context.Context, p struct {
		Personal int `json:"personal"`
	}) error {
		return backend.SetLLMConcurrency(rpcBizID(ctx), p.Personal)
	})
	t["set_default_thinking_mode"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Mode string `json:"mode"`
	}) error {
		if backend.LLMFactory() == nil {
			return fmt.Errorf("LLM factory not available")
		}
		backend.LLMFactory().SetDefaultThinkingMode(p.Mode)
		return nil
	}))
	t["list_models"] = rpc0err(func(ctx context.Context) ([]string, error) {
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		client, _, _, _ := backend.LLMFactory().GetLLM(rpcBizID(ctx))
		return client.ListModels(), nil
	})
	t["list_all_models"] = rpc0err(func(ctx context.Context) ([]string, error) {
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		models := backend.LLMFactory().ListAllModelsForUser(rpcBizID(ctx))
		log.WithField("count", len(models)).Debug("RPC list_all_models")
		return models, nil
	})
	t["set_model_tiers"] = h.requireAdmin(rpc1void(func(ctx context.Context, p config.LLMConfig) error {
		if backend.LLMFactory() == nil {
			return fmt.Errorf("LLM factory not available")
		}
		backend.LLMFactory().SetModelTiers(p)
		return nil
	}))
	t["set_proxy_llm"] = rpc1void(func(ctx context.Context, p struct {
		Model string `json:"model"`
	}) error {
		if backend.LLMFactory() != nil {
			backend.LLMFactory().SwitchModel(rpcBizID(ctx), p.Model)
		}
		return nil
	})
	t["clear_proxy_llm"] = rpc0void(func(ctx context.Context) error { backend.ClearProxyLLM(rpcBizID(ctx)); return nil })
}

// ── Subscription CRUD ──

func registerSubscriptionHandlers(t rpcTable, h *rpcContext) {
	backend := h.backend
	t["list_subscriptions"] = rpc0err(h.listSubscriptions)
	t["get_default_subscription"] = rpc0err(h.getDefaultSubscription)
	t["add_subscription"] = rpc1void(func(ctx context.Context, p struct {
		Sub sqlite.LLMSubscription `json:"sub"`
	}) error {
		svc, err := h.requireSubscriptionSvc()
		if err != nil {
			return err
		}
		p.Sub.SenderID = rpcBizID(ctx)
		return svc.Add(&p.Sub)
	})
	t["update_subscription"] = rpc1void(h.updateSubscription)
	t["remove_subscription"] = rpc1void(func(ctx context.Context, p struct {
		ID string `json:"id"`
	}) error {
		svc, err := h.requireSubscriptionSvc()
		if err != nil {
			return err
		}
		sub, err := svc.Get(p.ID)
		if err != nil {
			return err
		}
		if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != rpcBizID(ctx) {
			return fmt.Errorf("subscription not found")
		}
		if err := svc.Remove(p.ID); err != nil {
			return err
		}
		backend.LLMFactory().Invalidate(sub.SenderID)
		return nil
	})
	t["set_default_subscription"] = rpc1void(h.setDefaultSubscription)
	t["rename_subscription"] = rpc1void(func(ctx context.Context, p struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}) error {
		svc, err := h.requireSubscriptionSvc()
		if err != nil {
			return err
		}
		sub, err := svc.Get(p.ID)
		if err != nil {
			return err
		}
		if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != rpcBizID(ctx) {
			return fmt.Errorf("subscription not found")
		}
		return svc.Rename(p.ID, p.Name)
	})
	t["set_subscription_model"] = rpc1void(h.setSubscriptionModel)
}

// ── Memory / session / history / status ──

func registerSessionHandlers(t rpcTable, h *rpcContext) {
	backend := h.backend
	t["clear_memory"] = rpc1void(func(ctx context.Context, p struct {
		Channel    string `json:"channel"`
		ChatID     string `json:"chat_id"`
		TargetType string `json:"target_type"`
	}) error {
		if err := h.requireMultiSession(); err != nil {
			return err
		}
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return err
		}
		return backend.MultiSession().ClearMemory(context.Background(), p.Channel, chatID, p.TargetType, rpcBizID(ctx))
	})
	t["get_memory_stats"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		if err := h.requireMultiSession(); err != nil {
			return nil, err
		}
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return nil, err
		}
		return backend.MultiSession().GetMemoryStats(context.Background(), p.Channel, chatID, rpcBizID(ctx)), nil
	})
	t["get_user_token_usage"] = rpc0err(func(ctx context.Context) (any, error) {
		if err := h.requireMultiSession(); err != nil {
			return nil, err
		}
		return backend.MultiSession().GetUserTokenUsage(rpcBizID(ctx))
	})
	t["get_daily_token_usage"] = rpc1(func(ctx context.Context, p struct {
		Days     int    `json:"days"`
		SenderID string `json:"sender_id"`
	}) (any, error) {
		if err := h.requireMultiSession(); err != nil {
			return nil, err
		}
		return backend.MultiSession().GetDailyTokenUsage(rpcBizID(ctx), p.Days)
	})

	// ── Sub-agents / sessions ──
	t["count_interactive_sessions"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (int, error) {
		if err := ownOrAdmin(ctx, p.ChatID); err != nil {
			return 0, err
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID == "" {
			p.ChatID = rpcBizID(ctx)
		}
		return backend.CountInteractiveSessions(p.Channel, p.ChatID), nil
	})
	t["list_interactive_sessions"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		if err := ownOrAdmin(ctx, p.ChatID); err != nil {
			return nil, err
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID == "" {
			p.ChatID = rpcBizID(ctx)
		}
		return backend.ListInteractiveSessions(p.Channel, p.ChatID), nil
	})
	t["inspect_interactive_session"] = rpc1(func(ctx context.Context, p struct {
		Role      string `json:"role"`
		Channel   string `json:"channel"`
		ChatID    string `json:"chat_id"`
		Instance  string `json:"instance"`
		TailCount int    `json:"tail_count"`
	}) (string, error) {
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return "", err
		}
		return backend.InspectInteractiveSession(context.Background(), p.Role, p.Channel, chatID, p.Instance, p.TailCount)
	})
	t["get_session_messages"] = rpc1(func(ctx context.Context, p struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		Role     string `json:"role"`
		Instance string `json:"instance"`
	}) (any, error) {
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return nil, err
		}
		msgs, _ := backend.GetSessionMessages(p.Channel, chatID, p.Role, p.Instance)
		if msgs == nil {
			msgs = []agent.SessionMessage{}
		}
		return msgs, nil
	})
	t["get_agent_session_dump"] = rpc1(func(ctx context.Context, p struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chat_id"`
		Role     string `json:"role"`
		Instance string `json:"instance"`
	}) (any, error) {
		chatID, err := resolveChatID(ctx, p.ChatID)
		if err != nil {
			return nil, err
		}
		dump, _ := backend.GetAgentSessionDump(p.Channel, chatID, p.Role, p.Instance)
		if dump == nil {
			dump = &agent.AgentSessionDump{}
		}
		return dump, nil
	})
	t["get_agent_session_dump_by_full_key"] = rpc1(func(ctx context.Context, p struct {
		FullKey string `json:"full_key"`
	}) (any, error) {
		if p.FullKey == "" {
			return nil, fmt.Errorf("full_key is required")
		}
		if owner := sessionKeyOwner(p.FullKey); owner != "" {
			if !isAdmin(rpcAuthID(ctx)) && owner != rpcBizID(ctx) {
				return nil, fmt.Errorf("access denied")
			}
		}
		dump, _ := backend.GetAgentSessionDumpByFullKey(p.FullKey)
		if dump == nil {
			dump = &agent.AgentSessionDump{}
		}
		return dump, nil
	})

	// ── History ──
	t["get_history"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "web"
		}
		if p.ChatID == "" {
			p.ChatID = bizID
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != bizID && p.Channel != "agent" {
			return nil, fmt.Errorf("access denied")
		}
		history, err := backend.GetHistory(p.Channel, p.ChatID)
		if err != nil {
			return nil, err
		}
		log.WithFields(log.Fields{"channel": p.Channel, "chat_id": p.ChatID, "count": len(history)}).Info("RPC get_history")
		return history, nil
	})
	t["get_token_state"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "cli"
		}
		if p.ChatID == "" {
			p.ChatID = bizID
		}
		ms := backend.MultiSession()
		if ms == nil {
			return map[string]int64{"prompt_tokens": 0, "completion_tokens": 0}, nil
		}
		sess, err := ms.GetOrCreateSession(p.Channel, p.ChatID)
		if err != nil {
			return nil, err
		}
		memSvc := sess.MemoryService()
		if memSvc == nil {
			return map[string]int64{"prompt_tokens": 0, "completion_tokens": 0}, nil
		}
		pt, ct, err := memSvc.GetTokenState(ctx, sess.TenantID())
		if err != nil {
			return nil, err
		}
		return map[string]int64{"prompt_tokens": pt, "completion_tokens": ct}, nil
	})
	t["trim_history"] = rpc1void(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
		Cutoff  string `json:"cutoff"`
	}) error {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "web"
		}
		if p.ChatID == "" {
			p.ChatID = bizID
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != bizID {
			return fmt.Errorf("access denied")
		}
		var cutoff time.Time
		if p.Cutoff != "" {
			var err error
			if cutoff, err = time.Parse(time.RFC3339, p.Cutoff); err != nil {
				return fmt.Errorf("invalid cutoff format: %w", err)
			}
		}
		return backend.TrimHistory(p.Channel, p.ChatID, cutoff)
	})

	// ── Status ──
	t["is_processing"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (bool, error) {
		if p.Channel == "" {
			p.Channel = "web"
		}
		// is_processing requires explicit chatID or admin.
		// Unlike other handlers, empty chatID does NOT default to self.
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != rpcBizID(ctx) {
			return false, fmt.Errorf("access denied")
		}
		return backend.IsProcessing(p.Channel, p.ChatID), nil
	})
	t["get_active_progress"] = rpc1(func(ctx context.Context, p struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if p.Channel == "" {
			p.Channel = "web"
		}
		if !isAdmin(rpcAuthID(ctx)) && p.ChatID != bizID && p.Channel != "agent" {
			return nil, fmt.Errorf("access denied")
		}
		return backend.GetActiveProgress(p.Channel, p.ChatID), nil
	})
}

// ── Background tasks / tenants ──

func registerTaskHandlers(t rpcTable, h *rpcContext) {
	backend := h.backend
	t["get_bg_task_count"] = rpc1(func(ctx context.Context, p struct {
		SessionKey string `json:"session_key"`
	}) (int, error) {
		bizID := rpcBizID(ctx)
		if !isAdmin(rpcAuthID(ctx)) && p.SessionKey != "" {
			if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != bizID {
				return 0, fmt.Errorf("access denied")
			}
		}
		if backend.BgTaskManager() == nil {
			return 0, nil
		}
		return len(backend.BgTaskManager().ListRunning(p.SessionKey)), nil
	})
	t["list_bg_tasks"] = rpc1(func(ctx context.Context, p struct {
		SessionKey string `json:"session_key"`
	}) (any, error) {
		bizID := rpcBizID(ctx)
		if !isAdmin(rpcAuthID(ctx)) && p.SessionKey != "" {
			if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != bizID {
				return nil, fmt.Errorf("access denied")
			}
		}
		if backend.BgTaskManager() == nil {
			return []struct{}{}, nil
		}
		return marshalBgTasks(backend.BgTaskManager().ListAllForSession(p.SessionKey)), nil
	})
	t["kill_bg_task"] = rpc1void(func(ctx context.Context, p struct {
		TaskID string `json:"task_id"`
	}) error {
		bizID := rpcBizID(ctx)
		if backend.BgTaskManager() == nil {
			return fmt.Errorf("background tasks not available")
		}
		if !isAdmin(rpcAuthID(ctx)) {
			task, err := backend.BgTaskManager().Status(p.TaskID)
			if err != nil {
				return fmt.Errorf("access denied: task not found")
			}
			if owner := sessionKeyOwner(task.SessionKey()); owner != "" && owner != bizID {
				return fmt.Errorf("access denied")
			}
		}
		return backend.BgTaskManager().Kill(p.TaskID)
	})
	t["cleanup_completed_bg_tasks"] = rpc1(func(ctx context.Context, p struct {
		SessionKey string `json:"session_key"`
	}) (bool, error) {
		bizID := rpcBizID(ctx)
		if !isAdmin(rpcAuthID(ctx)) && p.SessionKey != "" {
			if owner := sessionKeyOwner(p.SessionKey); owner != "" && owner != bizID {
				return false, fmt.Errorf("access denied")
			}
		}
		if backend.BgTaskManager() != nil {
			backend.BgTaskManager().RemoveCompletedTasks(p.SessionKey)
		}
		return true, nil
	})

	// ── Tenants ──
	t["list_tenants"] = rpc0err(h.listTenants)
}

// ── Admin-only handlers ──

func registerAdminHandlers(t rpcTable, h *rpcContext) {
	cfg := h.cfg
	backend := h.backend
	disp := h.disp
	msgBus := h.msgBus

	t["reset_token_state"] = h.requireAdmin(rpc0void(func(ctx context.Context) error { backend.ResetTokenState(); return nil }))
	t["get_channel_config"] = h.requireAdmin(rpc0err(func(ctx context.Context) (any, error) {
		return backend.GetChannelConfigs()
	}))
	t["set_channel_config"] = h.requireAdmin(rpc1(func(ctx context.Context, p struct {
		Channel string            `json:"channel"`
		Values  map[string]string `json:"values"`
	}) (any, error) {
		if err := backend.SetChannelConfig(p.Channel, p.Values); err != nil {
			return nil, err
		}
		if enabledVal, ok := p.Values["enabled"]; ok && disp != nil && msgBus != nil {
			enabled, _ := strconv.ParseBool(enabledVal)
			_, alreadyRunning := disp.GetChannel(p.Channel)
			if enabled && !alreadyRunning {
				if ch := createChannelInstance(p.Channel, cfg, msgBus); ch != nil {
					disp.Register(ch)
					go func(n string, c channel.Channel) {
						defer func() {
							if r := recover(); r != nil {
								log.WithField("channel", n).Error("Dynamic channel start panicked\n" + string(debug.Stack()))
							}
						}()
						if err := c.Start(); err != nil {
							log.WithError(err).WithField("channel", n).Error("Dynamic channel failed")
						}
					}(ch.Name(), ch)
				}
			} else if !enabled && alreadyRunning {
				disp.Unregister(p.Channel)
			}
		}
		return nil, nil
	}))

	// ── Web user management (admin only) ──
	t["create_web_user"] = h.requireAdmin(rpc1(func(ctx context.Context, p struct {
		Username string `json:"username"`
	}) (any, error) {
		_, password, err := channel.CreateWebUser(backend.MultiSession().DB().Conn(), p.Username)
		if err != nil {
			return nil, err
		}
		return map[string]string{"password": password}, nil
	}))
	t["list_web_users"] = h.requireAdmin(rpc0err(func(ctx context.Context) (any, error) {
		return channel.ListWebUsers(backend.MultiSession().DB().Conn())
	}))
	t["delete_web_user"] = h.requireAdmin(rpc1void(func(ctx context.Context, p struct {
		Username string `json:"username"`
	}) error {
		return channel.DeleteWebUser(backend.MultiSession().DB().Conn(), p.Username)
	}))
}

// handleCLIRPC dispatches RPC requests from CLI RemoteBackend clients.
func handleCLIRPC(table rpcTable, method string, params json.RawMessage, senderID string) (json.RawMessage, error) {
	bizID := senderIDFromParams(params, senderID)
	ctx := withRPCCtx(context.Background(), senderID, bizID)
	return table.dispatch(ctx, method, params)
}

// ── Complex subscription handlers (extracted for readability) ──

func (h *rpcContext) listSubscriptions(ctx context.Context) ([]channel.Subscription, error) {
	bizID := rpcBizID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return []channel.Subscription{}, nil
	}
	subs, err := svc.List(bizID)
	if err != nil {
		return nil, err
	}
	result := make([]channel.Subscription, len(subs))
	for i, s := range subs {
		result[i] = subToChannel(s)
	}
	return result, nil
}

func (h *rpcContext) getDefaultSubscription(ctx context.Context) (*channel.Subscription, error) {
	bizID := rpcBizID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return nil, nil
	}
	sub, err := svc.GetDefault(bizID)
	if err != nil {
		return nil, err
	}
	if sub == nil {
		return nil, nil
	}
	ch := subToChannel(sub)
	return &ch, nil
}

func (h *rpcContext) updateSubscription(ctx context.Context, p struct {
	ID  string                 `json:"id"`
	Sub sqlite.LLMSubscription `json:"sub"`
}) error {
	bizID := rpcBizID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	existing, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(rpcAuthID(ctx)) && existing.SenderID != bizID {
		return fmt.Errorf("subscription not found")
	}
	p.Sub.ID = p.ID
	p.Sub.SenderID = existing.SenderID
	p.Sub.IsDefault = existing.IsDefault
	if strings.HasSuffix(p.Sub.APIKey, "****") && len(p.Sub.APIKey) <= 20 {
		log.WithField("sub_id", p.ID).Warn("[RPC] update_subscription: preserving existing API key (received masked)")
		p.Sub.APIKey = existing.APIKey
	}
	if err := svc.Update(&p.Sub); err != nil {
		return err
	}
	h.backend.LLMFactory().Invalidate(existing.SenderID)
	if existing.IsDefault {
		h.backend.LLMFactory().SwitchSubscription(bizID, &p.Sub, "")
	}
	return nil
}

func (h *rpcContext) setDefaultSubscription(ctx context.Context, p struct {
	ID     string `json:"id"`
	ChatID string `json:"chat_id"`
}) error {
	bizID := rpcBizID(ctx)
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != bizID {
		return fmt.Errorf("subscription not found")
	}
	if err := svc.SetDefault(p.ID); err != nil {
		return err
	}
	h.backend.LLMFactory().Invalidate(bizID)
	return h.backend.LLMFactory().SwitchSubscription(bizID, sub, p.ChatID)
}

func (h *rpcContext) setSubscriptionModel(ctx context.Context, p struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}) error {
	svc, err := h.requireSubscriptionSvc()
	if err != nil {
		return err
	}
	sub, err := svc.Get(p.ID)
	if err != nil {
		return err
	}
	if !isAdmin(rpcAuthID(ctx)) && sub.SenderID != rpcBizID(ctx) {
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
		if def, _ := svc.GetDefault(updated.SenderID); def != nil && def.ID == updated.ID {
			h.backend.LLMFactory().Invalidate(updated.SenderID)
			if err := h.backend.LLMFactory().SwitchSubscription(updated.SenderID, updated, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *rpcContext) listTenants(ctx context.Context) (any, error) {
	bizID := rpcBizID(ctx)
	if h.backend.MultiSession() == nil {
		return []struct{}{}, nil
	}
	db := h.backend.MultiSession().DB()
	if db == nil {
		return []struct{}{}, nil
	}
	tenants, err := sqlite.NewTenantService(db).ListTenants()
	if err != nil {
		return nil, err
	}
	if !isAdmin(rpcAuthID(ctx)) {
		var userTenants []sqlite.TenantInfo
		for _, t := range tenants {
			if t.ChatID == bizID {
				userTenants = append(userTenants, t)
			}
		}
		tenants = userTenants
	}
	var filtered []sqlite.TenantInfo
	for _, t := range tenants {
		if t.Channel != "agent" {
			filtered = append(filtered, t)
		}
	}
	type tenantJSON struct {
		ID           int64  `json:"id"`
		Channel      string `json:"channel"`
		ChatID       string `json:"chat_id"`
		CreatedAt    string `json:"created_at"`
		LastActiveAt string `json:"last_active_at"`
	}
	result := make([]tenantJSON, len(filtered))
	for i, t := range filtered {
		result[i] = tenantJSON{t.ID, t.Channel, t.ChatID, t.CreatedAt.Format(time.RFC3339), t.LastActiveAt.Format(time.RFC3339)}
	}
	return result, nil
}

// ── Helpers ──

func subToChannel(s *sqlite.LLMSubscription) channel.Subscription {
	return channel.Subscription{
		ID: s.ID, Name: s.Name, Provider: s.Provider,
		BaseURL: s.BaseURL, APIKey: maskAPIKey(s.APIKey),
		Model: s.Model, Active: s.IsDefault,
		MaxOutputTokens: s.MaxOutputTokens, ThinkingMode: s.ThinkingMode,
	}
}

type bgTaskJSON struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
}

func marshalBgTasks(tasks []*tools.BackgroundTask) []bgTaskJSON {
	result := make([]bgTaskJSON, len(tasks))
	for i, t := range tasks {
		result[i] = bgTaskJSON{t.ID, t.Command, string(t.Status), t.StartedAt.Format(time.RFC3339), "", t.Output, t.ExitCode, t.Error}
		if t.FinishedAt != nil {
			result[i].FinishedAt = t.FinishedAt.Format(time.RFC3339)
		}
	}
	return result
}
