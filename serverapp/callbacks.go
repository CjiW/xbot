package serverapp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"xbot/agent"
	"xbot/channel"
	"xbot/config"
	log "xbot/logger"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// runnerCallbacks builds the shared Runner callback closures.
// Used by both WebCallbacks and SettingsCallbacks to avoid duplication.
func runnerCallbacks(cfg *config.Config) channel.RunnerCallbacks {
	return channel.RunnerCallbacks{
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
			populateRunnerOnlineStatus(runners, senderID)
			runners = injectBuiltinDocker(runners)
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
			return buildRunnerConnectCmdFromToken(cfg, senderID, token, mode, dockerImage, workspace, llm), nil
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
	}
}

// registryCallbacks builds the shared Registry callback closures.
func registryCallbacks(backend agent.AgentBackend) channel.RegistryCallbacks {
	return channel.RegistryCallbacks{
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
	}
}

// llmCallbacks builds the shared LLM callback closures.
func llmCallbacks(backend agent.AgentBackend) channel.LLMCallbacks {
	return channel.LLMCallbacks{
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
		LLMGetPersonalConcurrency: func(senderID string) int {
			return backend.GetLLMConcurrency(senderID)
		},
		LLMSetPersonalConcurrency: func(senderID string, personal int) error {
			return backend.SetLLMConcurrency(senderID, personal)
		},
	}
}

// populateRunnerOnlineStatus fills the Online field for each runner.
func populateRunnerOnlineStatus(runners []tools.RunnerInfo, senderID string) {
	if sb := tools.GetSandbox(); sb != nil {
		if router, ok := sb.(*tools.SandboxRouter); ok {
			for i := range runners {
				runners[i].Online = router.IsRunnerOnline(senderID, runners[i].Name)
			}
		}
	}
}

// injectBuiltinDocker prepends the built-in docker sandbox runner if available.
func injectBuiltinDocker(runners []tools.RunnerInfo) []tools.RunnerInfo {
	if sb := tools.GetSandbox(); sb != nil {
		if router, ok := sb.(*tools.SandboxRouter); ok && router.HasDocker() {
			dockerEntry := tools.RunnerInfo{
				Name:        tools.BuiltinDockerRunnerName,
				Mode:        "docker",
				DockerImage: router.DockerImage(),
				Online:      true,
			}
			return append([]tools.RunnerInfo{dockerEntry}, runners...)
		}
	}
	return runners
}

// buildRunnerConnectCmdFromToken builds the xbot-runner CLI command from token + settings.
func buildRunnerConnectCmdFromToken(cfg *config.Config, senderID, token, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) string {
	pubURL := cfg.PublicWSAddr()
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
	return cmd
}

// buildWebCallbacks creates WebCallbacks using shared callback builders.
func buildWebCallbacks(cfg *config.Config, backend agent.AgentBackend, webDB *sql.DB) channel.WebCallbacks {
	rc := runnerCallbacks(cfg)
	regc := registryCallbacks(backend)
	llmc := llmCallbacks(backend)

	callbacks := channel.WebCallbacks{
		// Runner callbacks
		RunnerTokenGet:      rc.RunnerTokenGet,
		RunnerTokenGenerate: rc.RunnerTokenGenerate,
		RunnerTokenRevoke:   rc.RunnerTokenRevoke,
		RunnerList:          rc.RunnerList,
		RunnerCreate:        rc.RunnerCreate,
		RunnerDelete:        rc.RunnerDelete,
		RunnerGetActive:     rc.RunnerGetActive,
		RunnerSetActive:     rc.RunnerSetActive,

		// Registry callbacks
		RegistryBrowse:    regc.RegistryBrowse,
		RegistryInstall:   regc.RegistryInstall,
		RegistryListMy:    regc.RegistryListMy,
		RegistryPublish:   regc.RegistryPublish,
		RegistryUnpublish: regc.RegistryUnpublish,
		RegistryUninstall: regc.RegistryUninstall,

		// LLM callbacks (Web channel exposes only basic model/max-context via HTTP API;
		// ThinkingMode/MaxOutputTokens/PersonalConcurrency are CLI-only via RPC.)
		LLMList:          llmc.LLMList,
		LLMSet:           llmc.LLMSet,
		LLMGetMaxContext: llmc.LLMGetMaxContext,
		LLMSetMaxContext: llmc.LLMSetMaxContext,

		// SandboxWriteFile — Web-specific
		SandboxWriteFile: func(senderID string, sandboxRelPath string, data []byte, perm os.FileMode) (string, error) {
			sandbox := tools.GetSandbox()
			if sandbox == nil {
				return "", fmt.Errorf("no sandbox available")
			}
			resolver, ok := sandbox.(tools.SandboxResolver)
			if !ok {
				return "", fmt.Errorf("sandbox does not support per-user resolution")
			}
			userSbx := resolver.SandboxForUser(senderID)
			if userSbx == nil || userSbx.Name() == "none" {
				return "", fmt.Errorf("no sandbox available for user %s", senderID)
			}
			ws := userSbx.Workspace(senderID)
			absPath := filepath.Join(ws, sandboxRelPath)
			dir := filepath.Dir(absPath)
			if err := userSbx.MkdirAll(context.Background(), dir, 0755, senderID); err != nil {
				log.WithError(err).Warn("Failed to create directory in sandbox")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := userSbx.WriteFile(ctx, absPath, data, perm, senderID); err != nil {
				return "", err
			}
			return ws, nil
		},
	}

	// Wire IsProcessing
	callbacks.IsProcessing = func(senderID string) bool {
		return backend.IsProcessing("web", senderID)
	}
	// Wire GetActiveProgress
	callbacks.GetActiveProgress = func(channel, chatID string) *channel.CLIProgressPayload {
		return backend.GetActiveProgress(channel, chatID)
	}
	// Wire SessionsList
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
	// Wire SessionMessages
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
	// Wire Chat CRUD
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
	callbacks.ChatCreate = func(senderID, label string) (string, error) {
		if webDB == nil {
			return "", fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.CreateChat("web", senderID, label)
	}
	callbacks.ChatDelete = func(senderID, chatID string) error {
		if webDB == nil {
			return fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.DeleteChat("web", senderID, chatID)
	}
	callbacks.ChatRename = func(senderID, chatID, label string) error {
		if webDB == nil {
			return fmt.Errorf("database not available")
		}
		cs := sqlite.NewChatService(webDB)
		return cs.RenameChat("web", senderID, chatID, label)
	}
	return callbacks
}

// buildFeishuSettingsCallbacks builds SettingsCallbacks for Feishu using shared builders.
func buildFeishuSettingsCallbacks(cfg *config.Config, backend agent.AgentBackend) channel.SettingsCallbacks {
	rc := runnerCallbacks(cfg)
	regc := registryCallbacks(backend)
	llmc := llmCallbacks(backend)

	return channel.SettingsCallbacks{
		// LLM basic callbacks
		LLMList:                   llmc.LLMList,
		LLMSet:                    llmc.LLMSet,
		LLMGetMaxContext:          llmc.LLMGetMaxContext,
		LLMSetMaxContext:          llmc.LLMSetMaxContext,
		LLMGetMaxOutputTokens:     llmc.LLMGetMaxOutputTokens,
		LLMSetMaxOutputTokens:     llmc.LLMSetMaxOutputTokens,
		LLMGetThinkingMode:        llmc.LLMGetThinkingMode,
		LLMSetThinkingMode:        llmc.LLMSetThinkingMode,
		LLMGetPersonalConcurrency: llmc.LLMGetPersonalConcurrency,
		LLMSetPersonalConcurrency: llmc.LLMSetPersonalConcurrency,

		// LLM config (Feishu-specific — uses channel.Subscription directly)
		LLMGetConfig: func(senderID string) (provider, baseURL, model string, ok bool) {
			return "", "", "", false
		},
		LLMSetConfig: func(senderID, provider, baseURL, apiKey, model string, maxOutputTokens int, thinkingMode string) error {
			return fmt.Errorf("not supported in server mode")
		},
		LLMDelete: func(senderID string) error {
			return fmt.Errorf("not supported in server mode")
		},

		// Subscription management
		LLMListSubscriptions: func(senderID string) ([]channel.Subscription, error) {
			subs, err := backend.LLMFactory().GetSubscriptionSvc().List(senderID)
			if err != nil {
				return nil, err
			}
			result := make([]channel.Subscription, len(subs))
			for i, s := range subs {
				result[i] = subToChannel(s)
			}
			return result, nil
		},
		LLMGetDefaultSubscription: func(senderID string) (*channel.Subscription, error) {
			sub, err := backend.LLMFactory().GetSubscriptionSvc().GetDefault(senderID)
			if err != nil || sub == nil {
				return nil, err
			}
			// Return raw APIKey (not masked) — this is used for editing,
			// and matches the original master behavior.
			ch := channel.Subscription{
				ID: sub.ID, Name: sub.Name, Provider: sub.Provider,
				BaseURL: sub.BaseURL, APIKey: sub.APIKey,
				Model: sub.Model, Active: sub.IsDefault,
				MaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,
			}
			return &ch, nil
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
			sub, err := svc.Get(id)
			if err == nil && sub != nil {
				backend.LLMFactory().Invalidate(sub.SenderID)
			}
			return nil
		},
		LLMRenameSubscription: func(id, name string) error {
			return backend.LLMFactory().GetSubscriptionSvc().Rename(id, name)
		},

		// Model tier
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
			return saveServerConfig(cfg)
		},
		LLMListAllModels: func() []string {
			return backend.LLMFactory().ListAllModelsForUser("")
		},

		// Context mode
		ContextModeGet: func() string {
			return backend.GetContextMode()
		},
		ContextModeSet: func(mode string) error {
			return backend.SetContextMode(mode)
		},

		// Registry
		RegistryBrowse:    regc.RegistryBrowse,
		RegistryInstall:   regc.RegistryInstall,
		RegistryListMy:    regc.RegistryListMy,
		RegistryPublish:   regc.RegistryPublish,
		RegistryUnpublish: regc.RegistryUnpublish,
		RegistryDelete:    regc.RegistryUninstall,

		// Metrics
		MetricsGet: func() string {
			return agent.GlobalMetrics.Snapshot().FormatMarkdown()
		},

		// Sandbox
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

		// Runner callbacks
		RunnerConnectCmdGet: func(senderID string) string {
			token := cfg.Sandbox.AuthToken
			if token == "" {
				return ""
			}
			pubURL := cfg.PublicWSAddr()
			return fmt.Sprintf("./xbot-runner --server %s/ws/%s --token %s", pubURL, senderID, token)
		},
		RunnerTokenGet:      rc.RunnerTokenGet,
		RunnerTokenGenerate: rc.RunnerTokenGenerate,
		RunnerTokenRevoke:   rc.RunnerTokenRevoke,
		RunnerList:          rc.RunnerList,
		RunnerCreate:        rc.RunnerCreate,
		RunnerDelete:        rc.RunnerDelete,
		RunnerGetActive:     rc.RunnerGetActive,
		RunnerSetActive:     rc.RunnerSetActive,

		// Feishu-Web linking
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

		// Memory
		MemoryClear: func(senderID, chatID, targetType string) error {
			return backend.MultiSession().ClearMemory(context.Background(), "feishu", chatID, targetType, senderID)
		},
		MemoryGetStats: func(senderID, chatID string) map[string]string {
			return backend.MultiSession().GetMemoryStats(context.Background(), "feishu", chatID, senderID)
		},
	}
}
