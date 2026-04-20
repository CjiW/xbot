package serverapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/event"
	llm_pkg "xbot/llm"
	log "xbot/logger"
	"xbot/oauth"
	"xbot/oauth/providers"
	"xbot/storage"
	"xbot/storage/sqlite"
	"xbot/tools"
	"xbot/tools/feishu_mcp"
	"xbot/version"
)

// injectProxyLLM checks if the user's active runner has local LLM configured,
// and if so, injects a ProxyLLM into the agent's LLM factory.
func injectProxyLLM(userID string, backend agent.AgentBackend) {
	db := tools.GetRunnerTokenDB()
	if db == nil {
		return
	}
	store := tools.NewRunnerTokenStore(db)
	activeName, err := store.GetActiveRunner(userID)
	if err != nil || activeName == "" {
		return
	}
	runners, err := store.ListRunners(userID)
	if err != nil {
		return
	}
	for _, r := range runners {
		if r.Name == activeName {
			llm := r.LLMSettings()
			if llm.HasLLM() {
				sb := tools.GetSandbox()
				if sb == nil {
					return
				}
				router, ok := sb.(*tools.SandboxRouter)
				if !ok || router.Remote() == nil {
					return
				}
				rs := router.Remote()
				proxy := &llm_pkg.ProxyLLM{
					GenerateFunc: func(ctx context.Context, _, model string, messages []llm_pkg.ChatMessage, tools []llm_pkg.ToolDefinition, thinkingMode string) (*llm_pkg.LLMResponse, error) {
						return rs.LLMGenerate(ctx, userID, model, messages, tools, thinkingMode)
					},
					ListModelsFunc: func() []string {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						models, err := rs.LLMModels(ctx, userID)
						if err != nil {
							return nil
						}
						return models
					},
				}
				model := llm.Model
				if model == "" {
					model = backend.GetDefaultModel()
				}
				backend.SetProxyLLM(userID, proxy, model)
				log.Infof("ProxyLLM injected for user=%s runner=%s provider=%s", userID, activeName, llm.Provider)
			} else {
				backend.ClearProxyLLM(userID)
			}
			return
		}
	}
}

// setupLogging initializes the logger.
func setupLogging(cfg *config.Config) {
	if err := setupLogger(cfg.Log, cfg.Agent.WorkDir); err != nil {
		log.WithError(err).Fatal("Failed to setup logger")
	}
}

// setupLLM creates the LLM client.
func setupLLM(cfg *config.Config) (llm_pkg.LLM, error) {
	return createLLM(cfg.LLM, llm_pkg.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    cfg.Agent.LLMRetryDelay,
		MaxDelay: cfg.Agent.LLMRetryMaxDelay,
		Timeout:  cfg.Agent.LLMRetryTimeout,
	})
}

// setupOAuth creates OAuth server and manager.
func setupOAuth(cfg *config.Config, dbPath string) (*oauth.Server, *oauth.Manager, *providers.FeishuProvider, *sqlite.DB, error) {
	if !cfg.OAuth.Enable {
		return nil, nil, nil, nil, nil
	}

	sharedDB, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to open shared database for OAuth: %w", err)
	}
	tokenStorage, err := oauth.NewSQLiteStorage(sharedDB)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("failed to create OAuth token storage: %w", err)
	}

	oauthManager := oauth.NewManager(tokenStorage)
	feishuProvider := providers.NewFeishuProvider(cfg.Feishu.AppID, cfg.Feishu.AppSecret, cfg.OAuth.BaseURL+"/oauth/callback")
	oauthManager.RegisterProvider(feishuProvider)
	oauthServer := oauth.NewServer(oauth.Config{Enable: true, Host: cfg.OAuth.Host, Port: cfg.OAuth.Port, BaseURL: cfg.OAuth.BaseURL}, oauthManager)
	log.WithFields(log.Fields{"port": cfg.OAuth.Port, "baseURL": cfg.OAuth.BaseURL}).Info("OAuth server started")
	return oauthServer, oauthManager, feishuProvider, sharedDB, nil
}

// maskAPIKey masks an API key for safe transport over WS RPC.
// Shows first 4 chars + "****" so users can identify the key.
func maskAPIKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return key[:4] + "****"
}

// createAdminLLM creates a new LLM client from the admin config.
func createAdminLLM(cfg *config.Config) (llm_pkg.LLM, error) {
	switch cfg.LLM.Provider {
	case "openai":
		return llm_pkg.NewOpenAILLM(llm_pkg.OpenAIConfig{
			BaseURL:      cfg.LLM.BaseURL,
			APIKey:       cfg.LLM.APIKey,
			DefaultModel: cfg.LLM.Model,
			MaxTokens:    cfg.LLM.MaxOutputTokens,
		}), nil
	case "anthropic":
		return llm_pkg.NewAnthropicLLM(llm_pkg.AnthropicConfig{
			BaseURL:      cfg.LLM.BaseURL,
			APIKey:       cfg.LLM.APIKey,
			DefaultModel: cfg.LLM.Model,
			MaxTokens:    cfg.LLM.MaxOutputTokens,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.LLM.Provider)
	}
}

func adminAddSubscription(cfg *config.Config, backend agent.AgentBackend, sub channel.Subscription) error {
	subSvc := backend.LLMFactory().GetSubscriptionSvc()
	if subSvc == nil {
		return fmt.Errorf("subscription service not available")
	}
	return subSvc.Add(&sqlite.LLMSubscription{
		SenderID:  "admin",
		Name:      sub.Name,
		Provider:  sub.Provider,
		BaseURL:   sub.BaseURL,
		APIKey:    sub.APIKey,
		Model:     sub.Model,
		IsDefault: sub.Active,
	})
}

func adminUpdateSubscription(cfg *config.Config, backend agent.AgentBackend, id string, sub channel.Subscription) error {
	subSvc := backend.LLMFactory().GetSubscriptionSvc()
	if subSvc == nil {
		return fmt.Errorf("subscription service not available")
	}
	existing, err := subSvc.Get(id)
	if err != nil {
		return fmt.Errorf("subscription %s not found: %w", id, err)
	}
	existing.Name = sub.Name
	existing.Provider = sub.Provider
	existing.BaseURL = sub.BaseURL
	if !strings.HasSuffix(sub.APIKey, "****") {
		existing.APIKey = sub.APIKey
	}
	existing.Model = sub.Model
	if err := subSvc.Update(existing); err != nil {
		return err
	}
	// If this is the default subscription, rebuild LLM client
	if existing.IsDefault {
		return rebuildLLMFromSubscription(cfg, backend, existing)
	}
	return nil
}

func adminRemoveSubscription(cfg *config.Config, backend agent.AgentBackend, id string) error {
	subSvc := backend.LLMFactory().GetSubscriptionSvc()
	if subSvc == nil {
		return fmt.Errorf("subscription service not available")
	}
	return subSvc.Remove(id)
}

func adminSetDefaultSubscription(cfg *config.Config, backend agent.AgentBackend, id string) error {
	subSvc := backend.LLMFactory().GetSubscriptionSvc()
	if subSvc == nil {
		return fmt.Errorf("subscription service not available")
	}
	if err := subSvc.SetDefault(id); err != nil {
		return fmt.Errorf("set default subscription in DB: %w", err)
	}
	// Rebuild LLM client from the new default subscription
	sub, err := subSvc.Get(id)
	if err != nil {
		return fmt.Errorf("get subscription: %w", err)
	}
	return rebuildLLMFromSubscription(cfg, backend, sub)
}

func adminRenameSubscription(cfg *config.Config, backend agent.AgentBackend, id, name string) error {
	subSvc := backend.LLMFactory().GetSubscriptionSvc()
	if subSvc == nil {
		return fmt.Errorf("subscription service not available")
	}
	return subSvc.Rename(id, name)
}

func adminSetSubscriptionModel(cfg *config.Config, backend agent.AgentBackend, id, model string) error {
	subSvc := backend.LLMFactory().GetSubscriptionSvc()
	if subSvc == nil {
		return fmt.Errorf("subscription service not available")
	}
	if err := subSvc.SetModel(id, model); err != nil {
		return err
	}
	// If this is the default subscription, rebuild LLM client
	def, _ := subSvc.GetDefault("admin")
	if def != nil && def.ID == id {
		sub, _ := subSvc.Get(id)
		if sub != nil {
			return rebuildLLMFromSubscription(cfg, backend, sub)
		}
	}
	return nil
}

// rebuildLLMFromSubscription creates a new LLM client from a DB subscription
// and sets it as the factory default.
func rebuildLLMFromSubscription(cfg *config.Config, backend agent.AgentBackend, sub *sqlite.LLMSubscription) error {
	// Temporarily set cfg.LLM from subscription for createAdminLLM
	cfg.LLM.Provider = sub.Provider
	cfg.LLM.BaseURL = sub.BaseURL
	cfg.LLM.APIKey = sub.APIKey
	cfg.LLM.Model = sub.Model
	cfg.LLM.MaxOutputTokens = sub.MaxOutputTokens
	newClient, err := createAdminLLM(cfg)
	if err != nil {
		return fmt.Errorf("create LLM from subscription: %w", err)
	}
	if factory := backend.LLMFactory(); factory != nil {
		factory.SetDefaults(newClient, sub.Model)
		factory.SetModelTiers(cfg.LLM)
		factory.Invalidate(adminSenderID)
	}
	return nil
}

// handleCLIRPC dispatches RPC requests from CLI RemoteBackend clients
// to the server's LocalBackend. This is the server-side counterpart of
// RemoteBackend.callRPC().
func handleCLIRPC(cfg *config.Config, backend agent.AgentBackend, method string, params json.RawMessage, senderID string) (json.RawMessage, error) {
	switch method {
	// --- Context / settings ---
	case "get_context_mode":
		return json.Marshal(backend.GetContextMode())
	case "set_context_mode":
		var p struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, backend.SetContextMode(p.Mode)
	case "set_cwd":
		var p struct {
			Channel string `json:"channel"`
			ChatID  string `json:"chat_id"`
			Dir     string `json:"dir"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, backend.SetCWD(p.Channel, p.ChatID, p.Dir)
	case "get_settings":
		var p struct {
			Namespace string `json:"namespace"`
			SenderID  string `json:"sender_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		effectiveSenderID := senderID
		if p.SenderID != "" {
			effectiveSenderID = p.SenderID
		}
		if effectiveSenderID == "admin" {
			result, err := getGlobalCLISettings(cfg)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		}
		if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, backend, p.Namespace, effectiveSenderID); err != nil {
			return nil, err
		}
		if backend.SettingsService() == nil {
			return nil, fmt.Errorf("settings service not available")
		}
		result, err := backend.SettingsService().GetSettings(p.Namespace, effectiveSenderID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	case "set_setting":
		var p struct {
			Namespace string `json:"namespace"`
			SenderID  string `json:"sender_id"`
			Key       string `json:"key"`
			Value     string `json:"value"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		effectiveSenderID := senderID
		if p.SenderID != "" {
			effectiveSenderID = p.SenderID
		}
		if effectiveSenderID == "admin" {
			if err := setGlobalCLISetting(cfg, backend, p.Key, p.Value); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, backend, p.Namespace, effectiveSenderID); err != nil {
			return nil, err
		}
		if backend.SettingsService() == nil {
			return nil, fmt.Errorf("settings service not available")
		}
		return nil, backend.SettingsService().SetSetting(p.Namespace, effectiveSenderID, p.Key, p.Value)

	// --- Max iterations / concurrency / context tokens ---
	case "set_max_iterations":
		var p struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		backend.SetMaxIterations(p.N)
		return nil, nil
	case "set_max_concurrency":
		var p struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		backend.SetMaxConcurrency(p.N)
		return nil, nil
	case "set_max_context_tokens":
		var p struct {
			N int `json:"n"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		backend.SetMaxContextTokens(p.N)
		return nil, nil

	// --- LLM ---
	case "get_default_model":
		model := ""
		if subSvc := backend.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
			effectiveSenderID := senderID
			if isAdmin(senderID) {
				effectiveSenderID = "admin"
			}
			if sub, err := subSvc.GetDefault(effectiveSenderID); err == nil && sub != nil && sub.Model != "" {
				model = sub.Model
			}
		}
		if model == "" {
			_, m, _, _ := backend.LLMFactory().GetLLM(senderID)
			model = m
		}
		log.WithField("sender_id", senderID).WithField("model", model).Info("RPC get_default_model")
		return json.Marshal(model)
	case "set_user_model":
		var p struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, backend.SetUserModel(senderID, p.Model)
	case "switch_model":
		var p struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		log.WithField("sender_id", senderID).WithField("model", p.Model).Info("RPC switch_model")
		backend.SwitchModel(senderID, p.Model)
		if subSvc := backend.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
			if sub, err := subSvc.GetDefault(senderID); err == nil && sub != nil {
				if err := subSvc.SetModel(sub.ID, p.Model); err != nil {
					log.WithError(err).Warn("RPC switch_model: SetModel failed")
				} else {
					log.WithField("sub_id", sub.ID).Debug("RPC switch_model: persisted")
				}
			} else {
				log.WithField("sender_id", senderID).Warn("RPC switch_model: no default subscription")
			}
		}
		return nil, nil
	case "get_user_max_context":
		return json.Marshal(backend.GetUserMaxContext(senderID))
	case "set_user_max_context":
		var p struct {
			MaxContext int `json:"max_context"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, backend.SetUserMaxContext(senderID, p.MaxContext)
	case "get_user_max_output_tokens":
		return json.Marshal(backend.GetUserMaxOutputTokens(senderID))
	case "set_user_max_output_tokens":
		var p struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, backend.SetUserMaxOutputTokens(senderID, p.MaxTokens)
	case "get_user_thinking_mode":
		return json.Marshal(backend.GetUserThinkingMode(senderID))
	case "set_user_thinking_mode":
		var p struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, backend.SetUserThinkingMode(senderID, p.Mode)
	case "get_llm_concurrency":
		return json.Marshal(backend.GetLLMConcurrency(senderID))
	case "set_llm_concurrency":
		var p struct {
			Personal int `json:"personal"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return nil, backend.SetLLMConcurrency(senderID, p.Personal)
	case "set_default_thinking_mode":
		var p struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		backend.LLMFactory().SetDefaultThinkingMode(p.Mode)
		return nil, nil
	case "list_models":
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		return json.Marshal(backend.LLMFactory().ListModels())
	case "list_all_models":
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		return json.Marshal(backend.LLMFactory().ListAllModelsForUser(senderID))
	case "set_model_tiers":
		if !isAdmin(senderID) {
			return nil, fmt.Errorf("admin only")
		}
		var llmCfg config.LLMConfig
		if err := json.Unmarshal(params, &llmCfg); err != nil {
			return nil, err
		}
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		backend.LLMFactory().SetModelTiers(llmCfg)
		return nil, nil
	case "set_proxy_llm":
		var p struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		// CLI remote mode uses this RPC to switch the server-side model.
		// SetProxyLLM(nil) would store a nil client and crash GetLLM,
		// so use SwitchModel instead which only updates the cached model name.
		if backend.LLMFactory() != nil {
			backend.LLMFactory().SwitchModel(senderID, p.Model)
		}
		return nil, nil
	case "clear_proxy_llm":
		backend.ClearProxyLLM(senderID)
		return nil, nil

	// --- Memory ---
	case "clear_memory":
		var p struct {
			Channel    string `json:"channel"`
			ChatID     string `json:"chat_id"`
			TargetType string `json:"target_type"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if backend.MultiSession() == nil {
			return nil, fmt.Errorf("multi-session not available")
		}
		// Non-admin users can only clear their own memory
		if !isAdmin(senderID) && p.ChatID != "" && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		if p.ChatID == "" {
			p.ChatID = senderID
		}
		return nil, backend.MultiSession().ClearMemory(context.Background(), p.Channel, p.ChatID, p.TargetType, senderID)
	case "get_memory_stats":
		var p struct {
			Channel string `json:"channel"`
			ChatID  string `json:"chat_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if backend.MultiSession() == nil {
			return nil, fmt.Errorf("multi-session not available")
		}
		// Non-admin users can only view their own memory stats
		if !isAdmin(senderID) && p.ChatID != "" && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		if p.ChatID == "" {
			p.ChatID = senderID
		}
		result := backend.MultiSession().GetMemoryStats(context.Background(), p.Channel, p.ChatID, senderID)
		return json.Marshal(result)
	case "get_user_token_usage":
		if backend.MultiSession() == nil {
			return nil, fmt.Errorf("multi-session not available")
		}
		usage, err := backend.MultiSession().GetUserTokenUsage(senderID)
		if err != nil {
			return nil, err
		}
		return json.Marshal(usage)
	case "get_daily_token_usage":
		var p struct {
			Days int `json:"days"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if backend.MultiSession() == nil {
			return nil, fmt.Errorf("multi-session not available")
		}
		daily, err := backend.MultiSession().GetDailyTokenUsage(senderID, p.Days)
		if err != nil {
			return nil, err
		}
		return json.Marshal(daily)

		// --- Sub-agents ---
	case "count_interactive_sessions":
		var p struct {
			Channel string `json:"channel"`
			ChatID  string `json:"chat_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if !isAdmin(senderID) && p.ChatID != "" && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		if p.ChatID == "" {
			p.ChatID = senderID
		}
		return json.Marshal(backend.CountInteractiveSessions(p.Channel, p.ChatID))
	case "list_interactive_sessions":
		var p struct {
			Channel string `json:"channel"`
			ChatID  string `json:"chat_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if !isAdmin(senderID) && p.ChatID != "" && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		if p.ChatID == "" {
			p.ChatID = senderID
		}
		return json.Marshal(backend.ListInteractiveSessions(p.Channel, p.ChatID))
	case "inspect_interactive_session":
		var p struct {
			Role      string `json:"role"`
			Channel   string `json:"channel"`
			ChatID    string `json:"chat_id"`
			Instance  string `json:"instance"`
			TailCount int    `json:"tail_count"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if !isAdmin(senderID) && p.ChatID != "" && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		if p.ChatID == "" {
			p.ChatID = senderID
		}
		result, err := backend.InspectInteractiveSession(context.Background(), p.Role, p.Channel, p.ChatID, p.Instance, p.TailCount)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)

	// --- Background tasks ---
	case "get_bg_task_count":
		var p struct {
			SessionKey string `json:"session_key"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if backend.BgTaskManager() == nil {
			return json.Marshal(0)
		}
		return json.Marshal(len(backend.BgTaskManager().List(p.SessionKey)))

	// --- History ---
	case "get_history":
		var p struct {
			Channel string `json:"channel"`
			ChatID  string `json:"chat_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		// If client doesn't send channel/chatID, fall back to auth context.
		if p.Channel == "" {
			p.Channel = "web"
		}
		if p.ChatID == "" {
			p.ChatID = senderID
		}
		if !isAdmin(senderID) && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		history, err := backend.GetHistory(p.Channel, p.ChatID)
		if err != nil {
			return nil, err
		}
		log.WithFields(log.Fields{"channel": p.Channel, "chat_id": p.ChatID, "count": len(history), "rpc_sender": senderID}).Info("RPC get_history")
		return json.Marshal(history)
	case "trim_history":
		var p struct {
			Channel string `json:"channel"`
			ChatID  string `json:"chat_id"`
			Cutoff  string `json:"cutoff"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Channel == "" {
			p.Channel = "web"
		}
		if p.ChatID == "" {
			p.ChatID = senderID
		}
		if !isAdmin(senderID) && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		var cutoff time.Time
		if p.Cutoff != "" {
			var err error
			cutoff, err = time.Parse(time.RFC3339, p.Cutoff)
			if err != nil {
				return nil, fmt.Errorf("invalid cutoff format: %w", err)
			}
		}
		return nil, backend.TrimHistory(p.Channel, p.ChatID, cutoff)

	case "is_processing":
		var p struct {
			Channel string `json:"channel"`
			ChatID  string `json:"chat_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Channel == "" {
			p.Channel = "web"
		}
		if !isAdmin(senderID) && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		return json.Marshal(backend.IsProcessing(p.Channel, p.ChatID))

	case "get_active_progress":
		var p struct {
			Channel string `json:"channel"`
			ChatID  string `json:"chat_id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if p.Channel == "" {
			p.Channel = "web"
		}
		if !isAdmin(senderID) && p.ChatID != senderID {
			return nil, fmt.Errorf("access denied")
		}
		progress := backend.GetActiveProgress(p.Channel, p.ChatID)
		if progress == nil {
			return json.Marshal(nil)
		}
		return json.Marshal(progress)

		// --- Subscriptions ---
	case "list_subscriptions":
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		svc := backend.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return json.Marshal([]channel.Subscription{})
		}
		effectiveSenderID := senderID
		if isAdmin(senderID) {
			effectiveSenderID = "admin"
		}
		subs, err := svc.List(effectiveSenderID)
		if err != nil {
			return nil, err
		}
		result := make([]channel.Subscription, len(subs))
		for i, s := range subs {
			result[i] = channel.Subscription{
				ID: s.ID, Name: s.Name, Provider: s.Provider,
				BaseURL: s.BaseURL, APIKey: maskAPIKey(s.APIKey),
				Model: s.Model, Active: s.IsDefault,
			}
		}
		return json.Marshal(result)
	case "get_default_subscription":
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		svc := backend.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return nil, nil
		}
		effectiveSenderID := senderID
		if isAdmin(senderID) {
			effectiveSenderID = "admin"
		}
		sub, err := svc.GetDefault(effectiveSenderID)
		if err != nil || sub == nil {
			return nil, err
		}
		return json.Marshal(channel.Subscription{
			ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
			BaseURL: sub.BaseURL, APIKey: maskAPIKey(sub.APIKey),
			Model: sub.Model, Active: sub.IsDefault,
		})
	case "add_subscription":
		var p struct {
			Sub sqlite.LLMSubscription `json:"sub"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if isAdmin(senderID) {
			return nil, adminAddSubscription(cfg, backend, channel.Subscription{
				ID: p.Sub.ID, Name: p.Sub.Name, Provider: p.Sub.Provider,
				BaseURL: p.Sub.BaseURL, APIKey: p.Sub.APIKey, Model: p.Sub.Model, Active: p.Sub.IsDefault,
			})
		}
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		svc := backend.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return nil, fmt.Errorf("subscription service not available")
		}
		p.Sub.SenderID = senderID
		return nil, svc.Add(&p.Sub)
	case "update_subscription":
		var p struct {
			ID  string                 `json:"id"`
			Sub sqlite.LLMSubscription `json:"sub"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if isAdmin(senderID) {
			return nil, adminUpdateSubscription(cfg, backend, p.ID, channel.Subscription{
				ID: p.ID, Name: p.Sub.Name, Provider: p.Sub.Provider,
				BaseURL: p.Sub.BaseURL, APIKey: p.Sub.APIKey, Model: p.Sub.Model, Active: p.Sub.IsDefault,
			})
		}
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		svc := backend.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return nil, fmt.Errorf("subscription service not available")
		}
		existing, err := svc.Get(p.ID)
		if err != nil {
			return nil, err
		}
		if !isAdmin(senderID) && existing.SenderID != senderID {
			return nil, fmt.Errorf("subscription not found")
		}
		p.Sub.ID = p.ID
		p.Sub.SenderID = existing.SenderID
		if strings.HasSuffix(p.Sub.APIKey, "****") {
			p.Sub.APIKey = existing.APIKey
		}
		if err := svc.Update(&p.Sub); err != nil {
			return nil, err
		}
		backend.LLMFactory().Invalidate(existing.SenderID)
		return nil, nil
	case "remove_subscription":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if isAdmin(senderID) {
			return nil, adminRemoveSubscription(cfg, backend, p.ID)
		}
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		svc := backend.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return nil, fmt.Errorf("subscription service not available")
		}
		sub, err := svc.Get(p.ID)
		if err != nil {
			return nil, err
		}
		if !isAdmin(senderID) && sub.SenderID != senderID {
			return nil, fmt.Errorf("subscription not found")
		}
		if err := svc.Remove(p.ID); err != nil {
			return nil, err
		}
		backend.LLMFactory().Invalidate(sub.SenderID)
		return nil, nil
	case "set_default_subscription":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if isAdmin(senderID) {
			return nil, adminSetDefaultSubscription(cfg, backend, p.ID)
		}
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		svc := backend.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return nil, fmt.Errorf("subscription service not available")
		}
		sub, err := svc.Get(p.ID)
		if err != nil {
			return nil, err
		}
		if !isAdmin(senderID) && sub.SenderID != senderID {
			return nil, fmt.Errorf("subscription not found")
		}
		if err := svc.SetDefault(p.ID); err != nil {
			return nil, err
		}
		backend.LLMFactory().Invalidate(sub.SenderID)
		return nil, nil
	case "rename_subscription":
		var p struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if isAdmin(senderID) {
			return nil, adminRenameSubscription(cfg, backend, p.ID, p.Name)
		}
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		svc := backend.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return nil, fmt.Errorf("subscription service not available")
		}
		sub, err := svc.Get(p.ID)
		if err != nil {
			return nil, err
		}
		if !isAdmin(senderID) && sub.SenderID != senderID {
			return nil, fmt.Errorf("subscription not found")
		}
		return nil, svc.Rename(p.ID, p.Name)

	case "set_subscription_model":
		var p struct {
			ID    string `json:"id"`
			Model string `json:"model"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if isAdmin(senderID) {
			return nil, adminSetSubscriptionModel(cfg, backend, p.ID, p.Model)
		}
		if backend.LLMFactory() == nil {
			return nil, fmt.Errorf("LLM factory not available")
		}
		svc := backend.LLMFactory().GetSubscriptionSvc()
		if svc == nil {
			return nil, fmt.Errorf("subscription service not available")
		}
		sub, err := svc.Get(p.ID)
		if err != nil {
			return nil, err
		}
		if !isAdmin(senderID) && sub.SenderID != senderID {
			return nil, fmt.Errorf("subscription not found")
		}
		return nil, svc.SetModel(p.ID, p.Model)

	case "reset_token_state":
		backend.ResetTokenState()
		return nil, nil

	default:
		return nil, fmt.Errorf("unknown RPC method: %s", method)
	}
}

// buildWebCallbacks creates WebCallbacks with all Runner/Registry closures.
func buildWebCallbacks(cfg *config.Config, backend agent.AgentBackend, webDB *sql.DB) channel.WebCallbacks {
	callbacks := channel.WebCallbacks{
		RunnerTokenGet: func(senderID string) string {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return ""
			}
			entry := tools.NewRunnerTokenStore(db).Get(senderID)
			if entry == nil {
				return ""
			}
			return buildRunnerConnectCmd(cfg, entry)
		},
		RunnerTokenGenerate: func(senderID, mode, dockerImage, workspace string) (string, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return "", fmt.Errorf("remote sandbox not configured")
			}
			entry, err := tools.NewRunnerTokenStore(db).Generate(senderID, tools.RunnerTokenSettings{
				Mode:        mode,
				DockerImage: dockerImage,
				Workspace:   workspace,
			})
			if err != nil {
				return "", fmt.Errorf("generate token: %w", err)
			}
			return buildRunnerConnectCmd(cfg, entry), nil
		},
		RunnerTokenRevoke: func(senderID string) error {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return fmt.Errorf("remote sandbox not configured")
			}
			tools.NewRunnerTokenStore(db).Revoke(senderID)
			return nil
		},
		RunnerList: func(senderID string) ([]tools.RunnerInfo, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return nil, fmt.Errorf("runner management not configured")
			}
			store := tools.NewRunnerTokenStore(db)
			runners, err := store.ListRunners(senderID)
			if err != nil {
				return nil, err
			}
			// Populate online status from SandboxRouter
			if sb := tools.GetSandbox(); sb != nil {
				if router, ok := sb.(*tools.SandboxRouter); ok {
					for i := range runners {
						runners[i].Online = router.IsRunnerOnline(senderID, runners[i].Name)
					}
				}
			}
			// Inject built-in docker sandbox if available
			if sb := tools.GetSandbox(); sb != nil {
				if router, ok := sb.(*tools.SandboxRouter); ok && router.HasDocker() {
					dockerEntry := tools.RunnerInfo{
						Name:        tools.BuiltinDockerRunnerName,
						Mode:        "docker",
						DockerImage: router.DockerImage(),
						Online:      true,
					}
					runners = append([]tools.RunnerInfo{dockerEntry}, runners...)
				}
			}
			return runners, nil
		},
		RunnerCreate: func(senderID, name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return "", fmt.Errorf("runner management not configured")
			}
			store := tools.NewRunnerTokenStore(db)
			token, _, err := store.CreateRunner(senderID, name, mode, dockerImage, workspace, llm)
			if err != nil {
				return "", err
			}
			pubURL := cfg.Sandbox.PublicURL
			if pubURL == "" {
				pubURL = fmt.Sprintf("ws://%s:%d", cfg.Server.Host, cfg.Server.Port)
			}
			cmd := fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
			if mode == "docker" && dockerImage != "" {
				cmd += fmt.Sprintf(" --mode docker --docker-image %s", dockerImage)
			}
			if workspace != "" {
				cmd += fmt.Sprintf(" --workspace %s", workspace)
			}
			if llm.HasLLM() {
				cmd += fmt.Sprintf(" --llm-provider %s --llm-api-key %s --llm-model %s", llm.Provider, llm.APIKey, llm.Model)
				if llm.BaseURL != "" {
					cmd += fmt.Sprintf(" --llm-base-url %s", llm.BaseURL)
				}
			}
			return cmd, nil
		},
		RunnerDelete: func(senderID, name string) error {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return fmt.Errorf("runner management not configured")
			}
			// Disconnect runner if online
			if sb := tools.GetSandbox(); sb != nil {
				if router, ok := sb.(*tools.SandboxRouter); ok {
					router.DisconnectRunner(senderID, name)
				}
			}
			return tools.NewRunnerTokenStore(db).DeleteRunner(senderID, name)
		},
		RunnerGetActive: func(senderID string) (string, error) {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return "", fmt.Errorf("runner management not configured")
			}
			return tools.NewRunnerTokenStore(db).GetActiveRunner(senderID)
		},
		RunnerSetActive: func(senderID, name string) error {
			db := tools.GetRunnerTokenDB()
			if db == nil {
				return fmt.Errorf("runner management not configured")
			}
			return tools.NewRunnerTokenStore(db).SetActiveRunner(senderID, name)
		},

		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return backend.RegistryManager().Browse(entryType, limit, offset)
		},
		RegistryInstall: func(entryType string, id int64, senderID string) error {
			return backend.RegistryManager().Install(entryType, id, senderID)
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return backend.RegistryManager().ListMy(senderID, entryType)
		},
		RegistryPublish: func(entryType, name, senderID string) error {
			return backend.RegistryManager().Publish(entryType, name, senderID)
		},
		RegistryUnpublish: func(entryType, name, senderID string) error {
			return backend.RegistryManager().Unpublish(entryType, name, senderID)
		},

		RegistryUninstall: func(entryType, name, senderID string) error {
			return backend.RegistryManager().Uninstall(entryType, name, senderID)
		},
		LLMList: func(senderID string) ([]string, string) {
			llmClient, currentModel, _, _ := backend.LLMFactory().GetLLM(senderID)
			return llmClient.ListModels(), currentModel
		},
		LLMSet: func(senderID, model string) error {
			return backend.SetUserModel(senderID, model)
		},
		LLMGetMaxContext: func(senderID string) int {
			return backend.GetUserMaxContext(senderID)
		},
		LLMSetMaxContext: func(senderID string, maxContext int) error {
			return backend.SetUserMaxContext(senderID, maxContext)
		},

		SandboxWriteFile: func(senderID string, sandboxRelPath string, data []byte, perm os.FileMode) (string, error) {
			sandbox := tools.GetSandbox()
			if sandbox == nil {
				return "", fmt.Errorf("no sandbox available")
			}
			// Resolve per-user sandbox (e.g., remote runner vs docker)
			resolver, ok := sandbox.(tools.SandboxResolver)
			if !ok {
				return "", fmt.Errorf("sandbox does not support per-user resolution")
			}
			userSbx := resolver.SandboxForUser(senderID)
			if userSbx == nil || userSbx.Name() == "none" {
				return "", fmt.Errorf("no sandbox available for user %s", senderID)
			}
			// Build absolute path inside sandbox (e.g., /workspace/uploads/file.txt)
			ws := userSbx.Workspace(senderID)
			absPath := filepath.Join(ws, sandboxRelPath)
			// Ensure parent directory exists
			dir := filepath.Dir(absPath)
			if err := userSbx.MkdirAll(context.Background(), dir, 0755, senderID); err != nil {
				log.WithError(err).Warn("Failed to create directory in sandbox")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := userSbx.WriteFile(ctx, absPath, data, perm, senderID); err != nil {
				return "", err
			}
			// Return the workspace path prefix so the caller can build the display path
			return ws, nil
		},
	}
	// Wire RPC handler for CLI RemoteBackend clients
	callbacks.RPCHandler = func(method string, params json.RawMessage, senderID string) (json.RawMessage, error) {
		return handleCLIRPC(cfg, backend, method, params, senderID)
	}
	// Wire IsProcessing — check if agent is actively processing a request for the user.
	// In WebChannel, senderID == chatID.
	callbacks.IsProcessing = func(senderID string) bool {
		return backend.IsProcessing("web", senderID)
	}
	// Wire GetActiveProgress — returns latest progress snapshot for mid-session reconnect.
	callbacks.GetActiveProgress = func(channel, chatID string) *channel.CLIProgressPayload {
		return backend.GetActiveProgress(channel, chatID)
	}
	// Wire SessionsList — returns interactive SubAgent sessions for the user as ChatRooms.
	callbacks.SessionsList = func(senderID string) []channel.SessionInfo {
		sessions := backend.ListInteractiveSessions("web", senderID)
		result := make([]channel.SessionInfo, len(sessions))
		for i, s := range sessions {
			result[i] = channel.ChatRoom{
				ID:       s.Role + "/" + s.Instance,
				Type:     "subagent",
				Label:    s.Role + "/" + s.Instance,
				Role:     s.Role,
				Instance: s.Instance,
				Running:  s.Running,
				Preview:  s.Preview,
				Members:  "Agent ↔ " + s.Role,
			}
		}
		return result
	}
	// Wire SessionMessages — returns conversation messages for a SubAgent session.
	callbacks.SessionMessages = func(senderID, roleName, instance string) ([]channel.SessionChatMessage, bool) {
		msgs, ok := backend.GetSessionMessages("web", senderID, roleName, instance)
		if !ok {
			return nil, false
		}
		result := make([]channel.SessionChatMessage, len(msgs))
		for i, m := range msgs {
			result[i] = channel.SessionChatMessage{Role: m.Role, Content: m.Content}
		}
		return result, true
	}

	// Wire ChatList — list user's chatrooms
	callbacks.ChatList = func(senderID, currentChatID string) ([]channel.UserChatWithPreview, error) {
		if webDB == nil {
			return nil, nil
		}
		cs := sqlite.NewChatService(webDB)
		chats, err := cs.ListUserChats("web", senderID, currentChatID)
		if err != nil {
			return nil, err
		}
		result := make([]channel.UserChatWithPreview, len(chats))
		for i, c := range chats {
			result[i] = channel.UserChatWithPreview{
				ChatID:     c.ChatID,
				Label:      c.Label,
				LastActive: c.LastActive.Format(time.RFC3339),
				Preview:    c.Preview,
				IsCurrent:  c.IsCurrent,
			}
		}
		return result, nil
	}

	// Wire ChatCreate — create new chatroom
	callbacks.ChatCreate = func(senderID, label string) (string, error) {
		if webDB == nil {
			return "", fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.CreateChat("web", senderID, label)
	}

	// Wire ChatDelete — delete chatroom
	callbacks.ChatDelete = func(senderID, chatID string) error {
		if webDB == nil {
			return fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.DeleteChat("web", senderID, chatID)
	}

	// Wire ChatRename — rename chatroom
	callbacks.ChatRename = func(senderID, chatID, label string) error {
		if webDB == nil {
			return fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.RenameChat("web", senderID, chatID, label)
	}
	return callbacks
}

// registerChannels creates and registers all channels.
func registerChannels(disp *channel.Dispatcher, cfg *config.Config, msgBus *bus.MessageBus, backend agent.AgentBackend, webDB *sql.DB, workDir string) (*channel.FeishuChannel, *channel.WebChannel, error) {
	var feishuCh *channel.FeishuChannel
	var webCh *channel.WebChannel
	if cfg.Feishu.Enabled {
		feishuCh = channel.NewFeishuChannel(channel.FeishuConfig{
			AppID:             cfg.Feishu.AppID,
			AppSecret:         cfg.Feishu.AppSecret,
			EncryptKey:        cfg.Feishu.EncryptKey,
			VerificationToken: cfg.Feishu.VerificationToken,
			AllowFrom:         cfg.Feishu.AllowFrom,
		}, msgBus)
		disp.Register(feishuCh)

	}

	// 注册 QQ 渠道
	if cfg.QQ.Enabled {
		qqCh := channel.NewQQChannel(channel.QQConfig{
			AppID:        cfg.QQ.AppID,
			ClientSecret: cfg.QQ.ClientSecret,
			AllowFrom:    cfg.QQ.AllowFrom,
		}, msgBus)
		disp.Register(qqCh)
	}

	// 注册 NapCat (OneBot 11) 渠道
	if cfg.NapCat.Enabled {
		napcatCh := channel.NewNapCatChannel(channel.NapCatConfig{
			WSUrl:     cfg.NapCat.WSUrl,
			Token:     cfg.NapCat.Token,
			AllowFrom: cfg.NapCat.AllowFrom,
		}, msgBus)
		disp.Register(napcatCh)
	}

	if cfg.Web.Enable {
		if webDB != nil {
			webCh = channel.NewWebChannel(channel.WebChannelConfig{
				Host:       cfg.Web.Host,
				Port:       cfg.Web.Port,
				DB:         webDB,
				AdminToken: cfg.Admin.Token,
				InviteOnly: cfg.Web.InviteOnly,
				PublicURL:  cfg.Sandbox.PublicURL,
			}, msgBus)
			if cfg.Web.StaticDir != "" {
				webCh.SetStaticDir(cfg.Web.StaticDir)
			}
			// Web file uploads go through cloud OSS only — no local storage
			webCh.SetWorkDir(workDir)
			// Set OSS provider for file storage
			if cfg.OSS.Provider == "qiniu" {
				ossProvider, err := channel.NewOSSProvider(
					cfg.OSS.Provider,
					"",
					channel.QiniuConfig{
						AccessKey: cfg.OSS.QiniuAccessKey,
						SecretKey: cfg.OSS.QiniuSecretKey,
						Bucket:    cfg.OSS.QiniuBucket,
						Domain:    cfg.OSS.QiniuDomain,
						Region:    cfg.OSS.QiniuRegion,
					},
				)
				if err != nil {
					log.WithError(err).Error("Failed to create Qiniu OSS provider")
				} else {
					webCh.SetOSSProvider(ossProvider)
					log.Info("OSS provider configured: qiniu")
				}
			}

			webCh.SetCallbacks(buildWebCallbacks(cfg, backend, webDB))
			// Wire up RemoteSandbox callbacks to push real-time status to WebChannel.
			// In WebChannel, senderID == chatID (see handleWS: client.userID = senderID, chatID := c.userID).
			sb := tools.GetSandbox()
			if sb != nil {
				if router, ok := sb.(*tools.SandboxRouter); ok {
					if remote := router.Remote(); remote != nil {
						remote.OnRunnerStatusChange = func(userID, runnerName string, online bool) {
							webCh.PushRunnerStatus(userID, runnerName, online)
							// When a runner with local LLM connects/disconnects, update ProxyLLM.
							if online {
								injectProxyLLM(userID, backend)
							} else {
								backend.ClearProxyLLM(userID)
							}
						}
						remote.OnSyncProgress = func(userID, phase, message string) {
							webCh.PushSyncProgress(userID, phase, message)
						}
					}
				}
			}
			disp.Register(webCh)
		} else {
			log.Warn("Web channel enabled but no database available, skipping")
		}
	}

	return feishuCh, webCh, nil
}

func Run(args []string) error {
	// Parse --config flag before loading config.
	// Usage: xbot --config /path/to/config.json
	var configPath string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--config" || args[i] == "-config") && i+1 < len(args) {
			configPath = args[i+1]
			i++
		} else if len(args[i]) > 9 && args[i][:9] == "--config=" {
			configPath = args[i][9:]
		}
	}

	var cfg *config.Config
	if configPath != "" {
		cfg = config.LoadFromFile(configPath)
		if cfg == nil {
			return fmt.Errorf("load config from %s", configPath)
		}
	} else {
		cfg = config.Load()
	}

	setupLogging(cfg)
	defer log.Close()

	llmClient, err := setupLLM(cfg)
	if err != nil {
		log.WithError(err).Fatal("Failed to create LLM client")
	}
	log.WithFields(log.Fields{"provider": cfg.LLM.Provider, "model": cfg.LLM.Model}).Info("LLM client created")

	msgBus := bus.NewMessageBus()

	workDir := cfg.Agent.WorkDir
	xbotDir := config.XbotHome()
	dbPath := config.DBFilePath()

	if err := storage.MigrateIfNeeded(context.Background(), workDir, dbPath); err != nil {
		log.WithError(err).Fatal("Failed to migrate data to SQLite")
	}

	oauthServer, oauthManager, feishuProvider, sharedDB, err := setupOAuth(cfg, dbPath)
	if err != nil {
		log.WithError(err).Fatal("Failed to setup OAuth")
	}

	// 初始化沙箱
	tools.InitSandbox(cfg.Sandbox, workDir)

	bc := agent.BackendConfig{
		Cfg:              cfg,
		LLM:              llmClient,
		Bus:              msgBus,
		DBPath:           dbPath,
		WorkDir:          workDir,
		XbotHome:         xbotDir,
		PersonaIsolation: cfg.Web.PersonaIsolation,
	}
	backend, err := agent.NewLocalBackend(bc.AgentConfig())
	if err != nil {
		log.WithError(err).Fatal("Failed to create local backend")
	}

	// Migrate config.json subscriptions into DB for the admin user.
	// This ensures admin is a normal DB user with real subscriptions,
	// so model switches persist across restarts.
	if subSvc := backend.LLMFactory().GetSubscriptionSvc(); subSvc != nil {
		if err := migrateConfigSubscriptions(cfg, subSvc, adminSenderID); err != nil {
			log.WithError(err).Warn("Failed to migrate config subscriptions to DB")
		}
		// Sync LLM client from DB's active subscription (not config.json).
		// After migration, DB is the source of truth.
		if defSub, err := subSvc.GetDefault(adminSenderID); err == nil && defSub != nil {
			cfg.LLM.Provider = defSub.Provider
			cfg.LLM.BaseURL = defSub.BaseURL
			cfg.LLM.APIKey = defSub.APIKey
			cfg.LLM.Model = defSub.Model
			cfg.LLM.MaxOutputTokens = defSub.MaxOutputTokens
			if newClient, err := createAdminLLM(cfg); err == nil {
				backend.LLMFactory().SetDefaults(newClient, defSub.Model)
				log.WithFields(log.Fields{"provider": defSub.Provider, "model": defSub.Model}).Info("LLM client synced from DB default subscription")
			}
		}
	}

	// 注册 OAuth 和 Feishu MCP 工具（如果启用）
	if cfg.OAuth.Enable && oauthManager != nil {
		// 注册 OAuth 工具
		oauthTool := &tools.OAuthTool{
			Manager: oauthManager,
			BaseURL: cfg.OAuth.BaseURL,
		}
		backend.RegisterCoreTool(oauthTool)

		// 注册 Feishu MCP 工具
		feishuMCP := feishu_mcp.NewFeishuMCP(oauthManager, cfg.Feishu.AppID, cfg.Feishu.AppSecret)
		if feishuProvider != nil {
			feishuMCP.SetLarkClient(feishuProvider.GetLarkClient())
		}
		backend.RegisterTool(&feishu_mcp.ListAllBitablesTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.BitableFieldsTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.BitableRecordTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.BitableListTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.BatchCreateAppTableRecordTool{MCP: feishuMCP})

		// Wiki tools
		backend.RegisterTool(&feishu_mcp.WikiListSpacesTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.WikiListNodesTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.WikiGetNodeTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.WikiMoveNodeTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.WikiCreateNodeTool{MCP: feishuMCP})

		// Document tools
		backend.RegisterTool(&feishu_mcp.DocxGetContentTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.DocxListBlocksTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.DocxCreateTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.DocxInsertBlockTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.DocxGetBlockTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.DocxDeleteBlocksTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.DocxFindBlockTool{MCP: feishuMCP})

		// Search tools
		backend.RegisterTool(&feishu_mcp.SearchWikiTool{MCP: feishuMCP})

		// Drive tools
		backend.RegisterTool(&feishu_mcp.UploadFileTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.ListFilesTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.AddPermissionTool{MCP: feishuMCP})

		// Message resource tools
		backend.RegisterTool(&feishu_mcp.DownloadFileTool{MCP: feishuMCP})
		backend.RegisterTool(&feishu_mcp.SendFileTool{MCP: feishuMCP})

		log.Info("OAuth and Feishu MCP tools registered")
	}

	// 注册 DownloadFile 工具（支持 Web/OSS 和飞书两种来源）
	backend.RegisterCoreTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	backend.RegisterTool(tools.NewDownloadFileTool(cfg.Feishu.AppID, cfg.Feishu.AppSecret))
	backend.RegisterCoreTool(tools.NewWebSearchTool(cfg.TavilyAPIKey))

	// 注册 Logs 工具（仅管理员可用）
	adminChatID := cfg.Admin.ChatID
	if adminChatID != "" {
		logsTool := tools.NewLogsTool(adminChatID)
		backend.RegisterCoreTool(logsTool)
		log.WithField("admin_chat_id", adminChatID).Info("Logs tool registered (admin only)")
	}

	// 初始化事件触发系统（Event Trigger System）
	triggerSvc := sqlite.NewTriggerService(backend.MultiSession().DB())
	eventRouter := event.NewRouter(triggerSvc)
	backend.SetEventRouter(eventRouter)

	webhookBaseURL := cfg.EventWebhook.BaseURL
	if webhookBaseURL == "" {
		webhookBaseURL = fmt.Sprintf("http://%s:%d", cfg.EventWebhook.Host, cfg.EventWebhook.Port)
	}
	backend.RegisterCoreTool(tools.NewEventTriggerTool(eventRouter, webhookBaseURL))

	var webhookServer *event.WebhookServer
	if cfg.EventWebhook.Enable {
		webhookServer = event.NewWebhookServer(eventRouter, event.WebhookConfig{
			Host:        cfg.EventWebhook.Host,
			Port:        cfg.EventWebhook.Port,
			BaseURL:     webhookBaseURL,
			MaxBodySize: cfg.EventWebhook.MaxBodySize,
			RateLimit:   cfg.EventWebhook.RateLimit,
		})
	}

	// 所有工具注册完成，索引全局工具（用于 search_tools 语义搜索）
	backend.IndexGlobalTools()
	backend.LLMFactory().SetModelTiers(cfg.LLM)
	backend.LLMFactory().SetRetryConfig(llm_pkg.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    cfg.Agent.LLMRetryDelay,
		MaxDelay: cfg.Agent.LLMRetryMaxDelay,
		Timeout:  cfg.Agent.LLMRetryTimeout,
	})

	tokenDB, err := sqlite.Open(dbPath)
	if err != nil {
		log.WithError(err).Warn("Failed to open token database, runner tokens disabled")
	} else {
		tools.SetRunnerTokenDB(tokenDB.Conn())
	}

	disp := channel.NewDispatcher(msgBus)

	var webDB *sql.DB
	if tokenDB != nil {
		webDB = tokenDB.Conn()
	}
	feishuCh, webCh, err := registerChannels(disp, cfg, msgBus, backend, webDB, workDir)
	if err != nil {
		log.WithError(err).Fatal("Failed to register channels")
	}

	// Register virtual CLI channel for remote mode (CLI→WS→server).
	// This makes the dispatcher aware of channel=cli so all outbound messages
	// (including raw bus.Outbound calls) route correctly to WS clients.
	if webCh != nil {
		disp.Register(channel.NewRemoteCLIChannel(webCh.Hub()))
	}

	backend.SetDirectSend(func(msg bus.OutboundMessage) (string, error) {
		return disp.SendDirect(msg)
	})
	backend.SetChannelFinder(disp.GetChannel)
	backend.Agent().SetMessageSender(disp)
	backend.Agent().SetAgentChannelRegistry(
		func(name string, runFn bus.RunFn) error {
			ac := channel.NewAgentChannel(name, runFn)
			if err := ac.Start(); err != nil {
				return fmt.Errorf("start AgentChannel %s: %w", name, err)
			}
			disp.Register(ac)
			return nil
		},
		func(name string) {
			disp.Unregister(name)
		},
	)

	// 设置飞书渠道的 CardBuilder（用于卡片回调处理）
	if feishuCh != nil {
		feishuCh.SetCardBuilder(backend.GetCardBuilder())
		if hook := backend.ToolHookChain().Get("approval"); hook != nil {
			if ah, ok := hook.(*tools.ApprovalHook); ok {
				feishuCh.SetApprovalHook(ah)
			}
		}

		// 传递 admin chatID 和 web DB（用于 admin 命令如 !webadd）
		if adminChatID != "" {
			feishuCh.SetAdminChatID(adminChatID)
		}
		if webDB != nil {
			feishuCh.SetWebDB(webDB)
		}

		// 注入设置卡片回调（让飞书渠道能访问 Agent 的 LLM/Registry/Settings 功能）
		feishuCh.SetSettingsCallbacks(channel.SettingsCallbacks{
			LLMList: func(senderID string) ([]string, string) {
				llmClient, currentModel, _, _ := backend.LLMFactory().GetLLM(senderID)
				if llmClient == nil {
					return nil, currentModel
				}
				return llmClient.ListModels(), currentModel
			},
			LLMSet: func(senderID, model string) error {
				return backend.SetUserModel(senderID, model)
			},
			LLMGetMaxContext: func(senderID string) int {
				return backend.GetUserMaxContext(senderID)
			},
			LLMSetMaxContext: func(senderID string, maxContext int) error {
				return backend.SetUserMaxContext(senderID, maxContext)
			},
			LLMGetMaxOutputTokens: func(senderID string) int {
				return backend.GetUserMaxOutputTokens(senderID)
			},
			LLMSetMaxOutputTokens: func(senderID string, maxTokens int) error {
				return backend.SetUserMaxOutputTokens(senderID, maxTokens)
			},
			LLMGetThinkingMode: func(senderID string) string {
				return backend.GetUserThinkingMode(senderID)
			},
			LLMSetThinkingMode: func(senderID string, mode string) error {
				return backend.SetUserThinkingMode(senderID, mode)
			},
			LLMGetModelTier: func(tier string) string {
				switch tier {
				case "vanguard":
					return cfg.LLM.VanguardModel
				case "balance":
					return cfg.LLM.BalanceModel
				case "swift":
					return cfg.LLM.SwiftModel
				default:
					return ""
				}
			},
			LLMSetModelTier: func(tier, model string) error {
				switch tier {
				case "vanguard":
					cfg.LLM.VanguardModel = model
				case "balance":
					cfg.LLM.BalanceModel = model
				case "swift":
					cfg.LLM.SwiftModel = model
				default:
					return fmt.Errorf("unknown tier: %s", tier)
				}
				backend.LLMFactory().SetModelTiers(cfg.LLM)
				return config.SaveToFile(config.ConfigFilePath(), cfg)
			},
			LLMListAllModels: func() []string {
				return backend.LLMFactory().ListAllModelsForUser("")
			},
			LLMListSubscriptions: func(senderID string) ([]channel.Subscription, error) {
				subs, err := backend.LLMFactory().GetSubscriptionSvc().List(senderID)
				if err != nil {
					return nil, err
				}
				result := make([]channel.Subscription, len(subs))
				for i, s := range subs {
					result[i] = channel.Subscription{
						ID:       s.ID,
						Name:     s.Name,
						Provider: s.Provider,
						BaseURL:  s.BaseURL,
						APIKey:   s.APIKey,
						Model:    s.Model,
						Active:   s.IsDefault,
					}
				}
				return result, nil
			},
			LLMGetDefaultSubscription: func(senderID string) (*channel.Subscription, error) {
				sub, err := backend.LLMFactory().GetSubscriptionSvc().GetDefault(senderID)
				if err != nil || sub == nil {
					return nil, err
				}
				return &channel.Subscription{
					ID:       sub.ID,
					Name:     sub.Name,
					Provider: sub.Provider,
					BaseURL:  sub.BaseURL,
					APIKey:   sub.APIKey,
					Model:    sub.Model,
					Active:   sub.IsDefault,
				}, nil
			},
			LLMAddSubscription: func(senderID string, sub *channel.Subscription) error {
				svc := backend.LLMFactory().GetSubscriptionSvc()
				err := svc.Add(&sqlite.LLMSubscription{
					SenderID: senderID,
					Name:     sub.Name,
					Provider: sub.Provider,
					BaseURL:  sub.BaseURL,
					APIKey:   sub.APIKey,
					Model:    sub.Model,
				})
				if err == nil {
					backend.LLMFactory().Invalidate(senderID)
				}
				return err
			},
			LLMRemoveSubscription: func(id string) error {
				svc := backend.LLMFactory().GetSubscriptionSvc()
				// Get senderID before removing for cache invalidation
				sub, err := svc.Get(id)
				if err != nil {
					return err
				}
				if err := svc.Remove(id); err != nil {
					return err
				}
				backend.LLMFactory().Invalidate(sub.SenderID)
				return nil
			},
			LLMSetDefaultSubscription: func(id string) error {
				svc := backend.LLMFactory().GetSubscriptionSvc()
				if err := svc.SetDefault(id); err != nil {
					return err
				}
				// Invalidate LLM cache for the subscription owner so GetLLM
				// picks up the new default on next request.
				sub, err := svc.Get(id)
				if err == nil && sub != nil {
					backend.LLMFactory().Invalidate(sub.SenderID)
				}
				return nil
			},
			LLMRenameSubscription: func(id, name string) error {
				return backend.LLMFactory().GetSubscriptionSvc().Rename(id, name)
			},
			ContextModeGet: func() string {
				return backend.GetContextMode()
			},
			ContextModeSet: func(mode string) error {
				return backend.SetContextMode(mode)
			},
			RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
				return backend.RegistryManager().Browse(entryType, limit, offset)
			},
			RegistryInstall: func(entryType string, id int64, senderID string) error {
				return backend.RegistryManager().Install(entryType, id, senderID)
			},
			RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
				return backend.RegistryManager().ListMy(senderID, entryType)
			},
			RegistryPublish: func(entryType, name, senderID string) error {
				return backend.RegistryManager().Publish(entryType, name, senderID)
			},
			RegistryUnpublish: func(entryType, name, senderID string) error {
				return backend.RegistryManager().Unpublish(entryType, name, senderID)
			},
			RegistryDelete: func(entryType, name, senderID string) error {
				return backend.RegistryManager().Uninstall(entryType, name, senderID)
			},
			MetricsGet: func() string {
				return agent.GlobalMetrics.Snapshot().FormatMarkdown()
			},
			SandboxCleanupTrigger: func(senderID string) error {
				sb := tools.GetSandbox()
				if sb == nil {
					return fmt.Errorf("sandbox not initialized")
				}
				return sb.ExportAndImport(senderID)
			},
			SandboxIsExporting: func(senderID string) bool {
				sb := tools.GetSandbox()
				if sb == nil {
					return false
				}
				return sb.IsExporting(senderID)
			},
			LLMGetPersonalConcurrency: func(senderID string) int {
				return backend.GetLLMConcurrency(senderID)
			},
			LLMSetPersonalConcurrency: func(senderID string, personal int) error {
				return backend.SetLLMConcurrency(senderID, personal)
			},
			RunnerConnectCmdGet: func(senderID string) string {
				token := cfg.Sandbox.AuthToken
				if token == "" {
					return ""
				}
				pubURL := cfg.Sandbox.PublicURL
				if pubURL == "" {
					// Fallback: use the xbot server address directly.
					pubURL = fmt.Sprintf("ws://%s:%d", cfg.Server.Host, cfg.Server.Port)
				}
				return fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
			},
			RunnerTokenGet: func(senderID string) string {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return ""
				}
				entry := tools.NewRunnerTokenStore(db).Get(senderID)
				if entry == nil {
					return ""
				}
				return buildRunnerConnectCmd(cfg, entry)
			},
			RunnerTokenGenerate: func(senderID, mode, dockerImage, workspace string) (string, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", fmt.Errorf("remote sandbox not configured")
				}
				entry, err := tools.NewRunnerTokenStore(db).Generate(senderID, tools.RunnerTokenSettings{
					Mode:        mode,
					DockerImage: dockerImage,
					Workspace:   workspace,
				})
				if err != nil {
					return "", fmt.Errorf("generate token: %w", err)
				}
				return buildRunnerConnectCmd(cfg, entry), nil
			},
			RunnerTokenRevoke: func(senderID string) error {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return fmt.Errorf("remote sandbox not configured")
				}
				tools.NewRunnerTokenStore(db).Revoke(senderID)
				return nil
			},
			RunnerList: func(senderID string) ([]tools.RunnerInfo, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return nil, fmt.Errorf("runner management not configured")
				}
				store := tools.NewRunnerTokenStore(db)
				runners, err := store.ListRunners(senderID)
				if err != nil {
					return nil, err
				}
				// Populate online status from SandboxRouter
				if sb := tools.GetSandbox(); sb != nil {
					if router, ok := sb.(*tools.SandboxRouter); ok {
						for i := range runners {
							runners[i].Online = router.IsRunnerOnline(senderID, runners[i].Name)
						}
					}
					// Inject built-in docker sandbox if available
					if sb := tools.GetSandbox(); sb != nil {
						if router, ok := sb.(*tools.SandboxRouter); ok && router.HasDocker() {
							dockerEntry := tools.RunnerInfo{
								Name:        tools.BuiltinDockerRunnerName,
								Mode:        "docker",
								DockerImage: router.DockerImage(),
								Online:      true,
							}
							runners = append([]tools.RunnerInfo{dockerEntry}, runners...)
						}
					}
				}
				return runners, nil
			},
			RunnerCreate: func(senderID, name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", fmt.Errorf("runner management not configured")
				}
				store := tools.NewRunnerTokenStore(db)
				token, _, err := store.CreateRunner(senderID, name, mode, dockerImage, workspace, llm)
				if err != nil {
					return "", err
				}
				pubURL := cfg.Sandbox.PublicURL
				if pubURL == "" {
					pubURL = fmt.Sprintf("ws://%s:%d", cfg.Server.Host, cfg.Server.Port)
				}
				cmd := fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
				if mode == "docker" && dockerImage != "" {
					cmd += fmt.Sprintf(" --mode docker --docker-image %s", dockerImage)
				}
				if workspace != "" {
					cmd += fmt.Sprintf(" --workspace %s", workspace)
				}
				if llm.HasLLM() {
					cmd += fmt.Sprintf(" --llm-provider %s --llm-api-key %s --llm-model %s", llm.Provider, llm.APIKey, llm.Model)
					if llm.BaseURL != "" {
						cmd += fmt.Sprintf(" --llm-base-url %s", llm.BaseURL)
					}
				}
				return cmd, nil
			},
			RunnerDelete: func(senderID, name string) error {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return fmt.Errorf("runner management not configured")
				}
				if sb := tools.GetSandbox(); sb != nil {
					if router, ok := sb.(*tools.SandboxRouter); ok {
						router.DisconnectRunner(senderID, name)
					}
				}
				return tools.NewRunnerTokenStore(db).DeleteRunner(senderID, name)
			},
			RunnerGetActive: func(senderID string) (string, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", fmt.Errorf("runner management not configured")
				}
				return tools.NewRunnerTokenStore(db).GetActiveRunner(senderID)
			},
			RunnerSetActive: func(senderID, name string) error {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return fmt.Errorf("runner management not configured")
				}
				return tools.NewRunnerTokenStore(db).SetActiveRunner(senderID, name)
			},
			FeishuWebLink: func(feishuUserID, username, password string) (string, error) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", fmt.Errorf("web linking not enabled")
				}
				return channel.FeishuLinkUser(db, feishuUserID, username, password)
			},
			FeishuWebGetLinked: func(feishuUserID string) (string, bool) {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return "", false
				}
				return channel.FeishuGetLinkedUser(db, feishuUserID)
			},
			FeishuWebUnlink: func(feishuUserID string) error {
				db := tools.GetRunnerTokenDB()
				if db == nil {
					return fmt.Errorf("web linking not enabled")
				}
				return channel.FeishuUnlinkUser(db, feishuUserID)
			},
			// ── 记忆管理（危险区） ──
			MemoryClear: func(senderID, chatID, targetType string) error {
				return backend.MultiSession().ClearMemory(context.Background(), "feishu", chatID, targetType, senderID)
			},
			MemoryGetStats: func(senderID, chatID string) map[string]string {
				return backend.MultiSession().GetMemoryStats(context.Background(), "feishu", chatID, senderID)
			},
		})

		// 注入飞书渠道特化 prompt 提供者
		backend.SetChannelPromptProviders(&feishuPromptAdapter{ch: feishuCh})
	}

	// 设置优雅退出（提前声明 ctx，供 OAuth Manager cleanup goroutine 使用）
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 设置 OAuth 服务器的回调函数，使其能在授权完成后发送消息
	if oauthServer != nil {
		// 启动 OAuth flow 定期清理 goroutine
		oauthManager.Start(ctx)

		oauthServer.SetSendFunc(func(channel, chatID, content string) error {
			_, err := disp.SendDirect(bus.OutboundMessage{
				Channel: channel,
				ChatID:  chatID,
				Content: content,
			})
			return err
		})
		// 现在启动 OAuth HTTP 服务器
		if err := oauthServer.Start(); err != nil {
			log.WithError(err).Fatal("Failed to start OAuth server")
		}
		log.WithFields(log.Fields{
			"port":    cfg.OAuth.Port,
			"baseURL": cfg.OAuth.BaseURL,
		}).Info("OAuth server started")
	}

	channels := disp.EnabledChannels()
	if len(channels) == 0 {
		log.Warn("No channels enabled. Set FEISHU_ENABLED=true and configure FEISHU_APP_ID/FEISHU_APP_SECRET.")
		log.Info("Starting in agent-only mode (no IM channels)")
	} else {
		log.WithField("channels", channels).Info("Channels enabled")
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 启动出站消息分发
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("panic", r).Error("Dispatcher panicked\n" + string(debug.Stack()))
			}
		}()
		disp.Run()
	}()

	// 启动所有渠道
	for name, ch := range getChannels(disp) {
		go func(n string, c channel.Channel) {
			defer func() {
				if r := recover(); r != nil {
					log.WithFields(log.Fields{"channel": n, "panic": r}).Error("Channel goroutine panicked\n" + string(debug.Stack()))
				}
			}()
			log.WithField("channel", n).Info("Starting channel...")
			if err := c.Start(); err != nil {
				log.WithError(err).WithField("channel", n).Error("Channel failed")
			}
		}(name, ch)
	}

	// 启动 Webhook 事件服务器
	if webhookServer != nil {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.WithField("panic", r).Error("Webhook server panicked\n" + string(debug.Stack()))
				}
			}()
			if err := webhookServer.Start(); err != nil {
				log.WithError(err).Error("Webhook server failed")
			}
		}()
		log.WithFields(log.Fields{
			"host":     cfg.EventWebhook.Host,
			"port":     cfg.EventWebhook.Port,
			"base_url": webhookBaseURL,
		}).Info("Webhook event server started")
	}

	// 启动 Agent 循环
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("panic", r).Error("Agent loop panicked\n" + string(debug.Stack()))
				// 触发优雅退出，避免僵尸进程
				sigCh <- syscall.SIGTERM
			}
		}()
		if err := backend.Run(ctx); err != nil && ctx.Err() == nil {
			log.WithError(err).Error("Agent loop exited with error")
		}
	}()

	log.Info("xbot started successfully")
	fmt.Println("🤖 xbot is running. Press Ctrl+C to stop.")

	// 启动后发送上线通知
	if cfg.StartupNotify.Channel != "" && cfg.StartupNotify.ChatID != "" {
		go sendStartupNotify(disp, cfg)
	}

	// 等待退出信号
	sig := <-sigCh
	log.WithField("signal", sig.String()).Warn("Received shutdown signal")
	fmt.Println("\nShutting down...")

	// 先取消 context，让 agent.Run() 退出（其 defer 会清理 cron 和 cleanup routine）
	cancel()

	// 关闭 Webhook 事件服务器
	if webhookServer != nil {
		webhookServer.Stop()
	}

	// 等待 agent loop 退出后再继续关闭
	if backend != nil {
		backend.Close()
	}

	// 关闭沙箱（清理 Docker 容器等资源）
	// export/import 可能耗时较长（大容器数分钟），不设超时，必须等待完成。
	if sandbox := tools.GetSandbox(); sandbox != nil {
		if err := sandbox.Close(); err != nil {
			log.WithError(err).Warn("Sandbox close error")
		}
	}

	// 停止 OAuth 服务器
	if oauthServer != nil {
		if err := oauthServer.Shutdown(context.Background()); err != nil {
			log.WithError(err).Warn("OAuth server shutdown error")
		}
	}
	// 停止 OAuth Manager 的定期清理 goroutine
	if oauthManager != nil {
		oauthManager.Close()
	}

	// 关闭 OAuth 共享数据库连接
	if sharedDB != nil {
		if err := sharedDB.Close(); err != nil {
			log.WithError(err).Warn("OAuth shared DB close error")
		}
	}

	// 关闭 runner token 数据库连接
	if tokenDB != nil {
		if err := tokenDB.Close(); err != nil {
			log.WithError(err).Warn("Token DB close error")
		}
	}

	disp.Stop()
	log.Info("xbot stopped")
	return nil
}

// createLLM 根据配置创建 LLM 客户端（带重试、指数退避和随机抖动）
func createLLM(cfg config.LLMConfig, retryCfg llm_pkg.RetryConfig) (llm_pkg.LLM, error) {
	var inner llm_pkg.LLM
	switch cfg.Provider {
	case "openai":
		inner = llm_pkg.NewOpenAILLM(llm_pkg.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		})
	case "anthropic":
		inner = llm_pkg.NewAnthropicLLM(llm_pkg.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		})
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", cfg.Provider)
	}

	return llm_pkg.NewRetryLLM(inner, retryCfg), nil
}

// setupLogger 配置日志
func setupLogger(cfg config.LogConfig, workDir string) error {
	return log.Setup(log.SetupConfig{
		Level:   cfg.Level,
		Format:  cfg.Format,
		WorkDir: workDir,
		MaxAge:  7, // 保留 7 天日志
	})
}

// getChannels 获取分发器中的所有渠道（辅助函数）
func getChannels(disp *channel.Dispatcher) map[string]channel.Channel {
	result := make(map[string]channel.Channel)
	for _, name := range disp.EnabledChannels() {
		if ch, ok := disp.GetChannel(name); ok {
			result[name] = ch
		}
	}
	return result
}

// sendStartupNotify 发送启动上线通知
func sendStartupNotify(disp *channel.Dispatcher, cfg *config.Config) {
	// 等待渠道 WebSocket 连接建立（轮询，最多 10 秒）
	const maxWait = 10 * time.Second
	const pollInterval = 500 * time.Millisecond
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		channels := disp.EnabledChannels()
		if len(channels) > 0 {
			// Give channels a moment to fully initialize
			time.Sleep(1 * time.Second)
			break
		}
		time.Sleep(pollInterval)
	}

	content := fmt.Sprintf("🟢 **xbot 已上线**\n- 版本：%s\n- 时间：%s\n- 模型：%s\n- 沙箱：%s\n- 记忆：%s",
		version.Info(),
		time.Now().Format("2006-01-02 15:04:05 MST"),
		cfg.LLM.Model,
		cfg.Sandbox.Mode,
		cfg.Agent.MemoryProvider,
	)

	for i := 0; i < 3; i++ {
		_, err := disp.SendDirect(bus.OutboundMessage{
			Channel: cfg.StartupNotify.Channel,
			ChatID:  cfg.StartupNotify.ChatID,
			Content: content,
		})
		if err == nil {
			log.WithFields(log.Fields{
				"channel": cfg.StartupNotify.Channel,
				"chat_id": cfg.StartupNotify.ChatID,
			}).Info("Startup notification sent")
			return
		}
		log.WithError(err).Warn("Failed to send startup notification, retrying...")
		time.Sleep(2 * time.Second)
	}
	log.Error("Failed to send startup notification after 3 attempts")
}

// feishuPromptAdapter 将 FeishuChannel 桥接为 agent.ChannelPromptProvider 接口。
// 避免在 agent 包中直接依赖 channel 包。
type feishuPromptAdapter struct {
	ch *channel.FeishuChannel
}

func (a *feishuPromptAdapter) ChannelPromptName() string {
	return a.ch.Name()
}

func (a *feishuPromptAdapter) ChannelSystemParts(ctx context.Context, chatID, senderID string) map[string]string {
	return a.ch.ChannelSystemParts(ctx, chatID, senderID)
}

// buildRunnerConnectCmd constructs the xbot-runner CLI command from a token entry.
func buildRunnerConnectCmd(cfg *config.Config, entry *tools.RunnerTokenEntry) string {
	pubURL := cfg.Sandbox.PublicURL
	if pubURL == "" {
		pubURL = fmt.Sprintf("ws://%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	cmd := fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, entry.UserID, entry.Token)
	if entry.Settings.Mode == "docker" {
		cmd += " --mode docker"
		if entry.Settings.DockerImage != "" {
			cmd += fmt.Sprintf(" --docker-image %s", entry.Settings.DockerImage)
		}
	}
	if entry.Settings.Workspace != "" && entry.Settings.Workspace != "/workspace" {
		cmd += fmt.Sprintf(" --workspace %s", entry.Settings.Workspace)
	}
	return cmd
}

func userScopedSettingsFromGlobalCLI(cfg *config.Config) map[string]string {
	vals := map[string]string{
		"context_mode":       cfg.Agent.ContextMode,
		"max_iterations":     fmt.Sprintf("%d", cfg.Agent.MaxIterations),
		"max_concurrency":    fmt.Sprintf("%d", cfg.Agent.MaxConcurrency),
		"max_context_tokens": fmt.Sprintf("%d", cfg.Agent.MaxContextTokens),
		"theme":              "midnight",
	}
	if cfg.Agent.EnableAutoCompress != nil {
		vals["enable_auto_compress"] = fmt.Sprintf("%t", *cfg.Agent.EnableAutoCompress)
	} else {
		vals["enable_auto_compress"] = "true"
	}
	return vals
}

func migrateCLIUserSettingsFromGlobalIfNeeded(cfg *config.Config, backend agent.AgentBackend, namespace, senderID string) error {
	if senderID == "" || backend.SettingsService() == nil {
		return nil
	}
	existing, err := backend.SettingsService().GetSettings(namespace, senderID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}
	for k, v := range userScopedSettingsFromGlobalCLI(cfg) {
		if strings.TrimSpace(v) == "" {
			continue
		}
		if err := backend.SettingsService().SetSetting(namespace, senderID, k, v); err != nil {
			return fmt.Errorf("seed user setting %s: %w", k, err)
		}
	}
	return nil
}

func getGlobalCLISettings(cfg *config.Config) (map[string]string, error) {
	vals := map[string]string{
		"llm_provider":       cfg.LLM.Provider,
		"llm_api_key":        cfg.LLM.APIKey,
		"llm_model":          cfg.LLM.Model,
		"llm_base_url":       cfg.LLM.BaseURL,
		"vanguard_model":     cfg.LLM.VanguardModel,
		"balance_model":      cfg.LLM.BalanceModel,
		"swift_model":        cfg.LLM.SwiftModel,
		"sandbox_mode":       cfg.Sandbox.Mode,
		"memory_provider":    cfg.Agent.MemoryProvider,
		"tavily_api_key":     cfg.TavilyAPIKey,
		"context_mode":       cfg.Agent.ContextMode,
		"max_iterations":     fmt.Sprintf("%d", cfg.Agent.MaxIterations),
		"max_concurrency":    fmt.Sprintf("%d", cfg.Agent.MaxConcurrency),
		"max_context_tokens": fmt.Sprintf("%d", cfg.Agent.MaxContextTokens),
		"theme":              "midnight",
	}
	if cfg.Agent.EnableAutoCompress != nil {
		vals["enable_auto_compress"] = fmt.Sprintf("%t", *cfg.Agent.EnableAutoCompress)
	} else {
		vals["enable_auto_compress"] = "true"
	}
	if vals["sandbox_mode"] == "" {
		vals["sandbox_mode"] = "none"
	}
	if vals["memory_provider"] == "" {
		vals["memory_provider"] = "flat"
	}
	return vals, nil
}

func setGlobalCLISetting(cfg *config.Config, backend agent.AgentBackend, key, value string) error {
	switch key {
	case "llm_provider":
		cfg.LLM.Provider = value
	case "llm_api_key":
		cfg.LLM.APIKey = value
	case "llm_model":
		cfg.LLM.Model = value
	case "llm_base_url":
		cfg.LLM.BaseURL = value
	case "vanguard_model":
		cfg.LLM.VanguardModel = value
	case "balance_model":
		cfg.LLM.BalanceModel = value
	case "swift_model":
		cfg.LLM.SwiftModel = value
	case "sandbox_mode":
		cfg.Sandbox.Mode = value
	case "memory_provider":
		cfg.Agent.MemoryProvider = value
	case "tavily_api_key":
		cfg.TavilyAPIKey = value
	case "context_mode":
		cfg.Agent.ContextMode = value
		backend.SetContextMode(value)
	case "max_iterations":
		cfg.Agent.MaxIterations = mustParseInt(value, cfg.Agent.MaxIterations)
		backend.SetMaxIterations(cfg.Agent.MaxIterations)
	case "max_concurrency":
		cfg.Agent.MaxConcurrency = mustParseInt(value, cfg.Agent.MaxConcurrency)
		backend.SetMaxConcurrency(cfg.Agent.MaxConcurrency)
	case "max_context_tokens":
		cfg.Agent.MaxContextTokens = mustParseInt(value, cfg.Agent.MaxContextTokens)
		backend.SetMaxContextTokens(cfg.Agent.MaxContextTokens)
	case "enable_auto_compress":
		b := strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "yes")
		cfg.Agent.EnableAutoCompress = &b
	default:
		return nil
	}
	if backend.LLMFactory() != nil {
		backend.LLMFactory().SetModelTiers(cfg.LLM)
	}
	return config.SaveToFile(config.ConfigFilePath(), cfg)
}

func mustParseInt(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return def
	}
	return n
}

// adminSenderID is the sender ID for the server admin (config-level user).
// Use this constant instead of the literal "admin" string to prevent typos.
const adminSenderID = "admin"

// isAdmin checks if the given senderID belongs to the server admin.
func isAdmin(senderID string) bool { return senderID == adminSenderID }

// migrateConfigSubscriptions seeds config.json subscriptions into the DB for a given user.
// Idempotent — skips if the user already has DB subscriptions.
func migrateConfigSubscriptions(cfg *config.Config, subSvc *sqlite.LLMSubscriptionService, senderID string) error {
	if len(cfg.Subscriptions) == 0 {
		return nil
	}
	// Skip if user already has DB subscriptions
	existing, err := subSvc.List(senderID)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	if len(existing) > 0 {
		return nil
	}
	for i, s := range cfg.Subscriptions {
		sub := &sqlite.LLMSubscription{
			SenderID:        senderID,
			Name:            s.Name,
			Provider:        s.Provider,
			BaseURL:         s.BaseURL,
			APIKey:          s.APIKey,
			Model:           s.Model,
			MaxOutputTokens: s.MaxOutputTokens,
			ThinkingMode:    s.ThinkingMode,
			IsDefault:       s.Active || (i == 0 && !hasActiveSub(cfg)),
		}
		if s.ID != "" {
			sub.ID = s.ID
		}
		if err := subSvc.Add(sub); err != nil {
			return fmt.Errorf("add subscription %s: %w", s.Name, err)
		}
		log.WithFields(log.Fields{"name": s.Name, "sender_id": senderID}).Info("Migrated config subscription to DB")
	}
	return nil
}

func hasActiveSub(cfg *config.Config) bool {
	for _, s := range cfg.Subscriptions {
		if s.Active {
			return true
		}
	}
	return false
}
