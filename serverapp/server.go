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

// resolveStaticDir returns the frontend static directory.
// Priority: explicit config → binary-relative web/dist → XBOT_HOME/web/dist.
func resolveStaticDir(cfg *config.Config) string {
	if cfg.Web.StaticDir != "" {
		return cfg.Web.StaticDir
	}
	// 1. Binary-relative: <exe_dir>/web/dist/ (Docker image layout)
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "web", "dist")
		if fi, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	// 2. XBOT_HOME-relative: ~/.xbot/web/dist/ (install script layout)
	if home := config.XbotHome(); home != "" {
		candidate := filepath.Join(home, "web", "dist")
		if fi, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !fi.IsDir() {
			return candidate
		}
	}
	return ""
}

// createChannelInstance creates a channel instance by name using current config.
// Returns nil for channels that require complex setup (e.g. web with DB/OSS).
// Used for dynamic channel start/stop without server restart.
func createChannelInstance(name string, cfg *config.Config, msgBus *bus.MessageBus) channel.Channel {
	switch name {
	case "feishu":
		return channel.NewFeishuChannel(channel.FeishuConfig{
			AppID:             cfg.Feishu.AppID,
			AppSecret:         cfg.Feishu.AppSecret,
			EncryptKey:        cfg.Feishu.EncryptKey,
			VerificationToken: cfg.Feishu.VerificationToken,
			AllowFrom:         cfg.Feishu.AllowFrom,
		}, msgBus)
	case "qq":
		return channel.NewQQChannel(channel.QQConfig{
			AppID:        cfg.QQ.AppID,
			ClientSecret: cfg.QQ.ClientSecret,
			AllowFrom:    cfg.QQ.AllowFrom,
		}, msgBus)
	case "napcat":
		return channel.NewNapCatChannel(channel.NapCatConfig{
			WSUrl:     cfg.NapCat.WSUrl,
			Token:     cfg.NapCat.Token,
			AllowFrom: cfg.NapCat.AllowFrom,
		}, msgBus)
	default:
		return nil
	}
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
			// Auto-detect frontend static files if not explicitly configured.
			staticDir := resolveStaticDir(cfg)
			if staticDir != "" {
				webCh.SetStaticDir(staticDir)
				log.WithField("static_dir", staticDir).Info("Frontend static files detected")
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
		if err := migrateConfigSubscriptions(cfg, subSvc, cliSenderID); err != nil {
			log.WithError(err).Warn("Failed to migrate config subscriptions to DB")
		}
		// Sync LLM client from DB's active subscription (not config.json).
		// After migration, DB is the source of truth.
		defSub, errDef := subSvc.GetDefault(cliSenderID)
		if errDef != nil {
			log.WithError(errDef).Error("GetDefault failed")
		} else if defSub == nil {
			log.Warn("GetDefault returned nil — no default subscription in DB")
		} else {
			log.WithFields(log.Fields{
				"id": defSub.ID, "name": defSub.Name, "model": defSub.Model,
				"provider": defSub.Provider, "max_output_tokens": defSub.MaxOutputTokens,
			}).Info("Default subscription from DB")
			cfg.LLM.Provider = defSub.Provider
			cfg.LLM.BaseURL = defSub.BaseURL
			cfg.LLM.APIKey = defSub.APIKey
			cfg.LLM.Model = defSub.Model
			cfg.LLM.MaxOutputTokens = defSub.MaxOutputTokens
			if newClient, err := createAdminLLM(cfg); err == nil {
				backend.LLMFactory().SetDefaults(newClient, defSub.Model)
				// SetDefaults clears all per-user caches. Re-populate them from
				// the default subscription so that GetMaxOutputTokens/GetLLM
				// return correct values for cli_user without waiting for a
				// SwitchSubscription call.
				backend.LLMFactory().SetUserMaxOutputTokens(cliSenderID, defSub.MaxOutputTokens)
				backend.LLMFactory().SetUserThinkingMode(cliSenderID, defSub.ThinkingMode)
				log.WithFields(log.Fields{"provider": defSub.Provider, "model": defSub.Model, "max_output_tokens": defSub.MaxOutputTokens}).Info("LLM client synced from DB default subscription")
			}
		}
	}

	// Clean up subscription-scoped keys that were migrated from user_settings
	// to user_llm_subscriptions. Stale rows in user_settings can overwrite
	// correct subscription values on startup (e.g. name→provider, max_output_tokens→8192).
	if ss := backend.SettingsService(); ss != nil {
		cleaned := 0
		for _, key := range []string{
			"llm_provider", "llm_api_key", "llm_model", "llm_base_url",
			"max_output_tokens", "thinking_mode",
		} {
			if err := ss.DeleteSetting("cli", cliSenderID, key); err == nil {
				cleaned++
			}
		}
		if cleaned > 0 {
			log.WithField("count", cleaned).Info("Cleaned subscription-scoped keys from user_settings")
		}
	}

	// Sync Agent runtime settings from DB (admin user).
	// DB is the source of truth — config.json may be stale after user changes.
	if ss := backend.SettingsService(); ss != nil {
		if vals, err := ss.GetSettings("cli", cliSenderID); err == nil {
			applyRuntimeSettings(cfg, backend, cliSenderID, vals)
			log.Info("Agent runtime settings synced from DB")
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

	// Build RPC table once at startup; per-request identity is passed via context.
	rpcTable := buildRPCTable(cfg, backend, disp, msgBus)

	// Wire RPC handler for CLI RemoteBackend clients (after disp/msgBus are available).
	if webCh != nil {
		webCh.SetRPCHandler(func(method string, params json.RawMessage, senderID string) (json.RawMessage, error) {
			return handleCLIRPC(rpcTable, method, params, senderID)
		})
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
		feishuCh.SetSettingsCallbacks(buildFeishuSettingsCallbacks(cfg, backend))

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
	pubURL := cfg.PublicWSAddr()
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

// saveServerConfig persists only the config sections the server actually modifies.
// It reads the current disk config first, overwrites ONLY LLM and Agent,
// then writes back — all other sections are preserved untouched.
//
// ⚠️ IMPORTANT: Do NOT add more sections here without careful review.
// Every field copied here must be one that the server actually modifies at runtime.
// Copying extra fields (Sandbox, CLI, Admin, Web, etc.) will overwrite user-set
// values with in-memory defaults, which is exactly the class of bug this function prevents.
func saveServerConfig(cfg *config.Config) error {
	path := config.ConfigFilePath()
	merged := config.LoadFromFile(path)
	if merged == nil {
		// Config file doesn't exist or has parse errors.
		// Refuse to overwrite — writing an empty config would destroy
		// all user settings (feishu, qq, web, etc.).
		// Only create a new file if it truly doesn't exist.
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			merged = &config.Config{}
		} else {
			log.WithField("path", path).Error("saveServerConfig: config file exists but cannot parse, refusing to overwrite")
			return fmt.Errorf("config file parse error, not overwriting")
		}
	}
	// Server only ever modifies these two sections:
	merged.LLM = cfg.LLM     // via applyRuntimeSetting / rebuildLLMFromSubscription
	merged.Agent = cfg.Agent // via applyRuntimeSetting (max_iterations, max_concurrency, etc.)
	return config.SaveToFile(path, merged)
}

// adminSenderID is the WS auth identity for admin users.
// Used ONLY for role-based access control (isAdmin checks).
// It is NOT a business senderID — never use it as a DB key for
// settings, subscriptions, token usage, or other per-user state.
const adminSenderID = "admin"

// cliSenderID is the fixed business sender ID for CLI channel.
// All CLI messages, settings, subscriptions, and per-user state use this ID.
// Server-side startup code uses this constant when seeding DB data.
const cliSenderID = "cli_user"

// isAdmin checks if the given WS auth senderID has admin privileges.
// Admin is a ROLE (authorization), not a business identity.
func isAdmin(authSenderID string) bool { return authSenderID == adminSenderID }

// sessionKeyOwner extracts the chatID (owner) from a session/full key.
// Key format: "channel:chatID/roleName[:instance]"
// Returns empty string if the format is invalid.
func sessionKeyOwner(key string) string {
	parts := strings.SplitN(key, ":", 2)
	if len(parts) < 2 {
		return ""
	}
	return strings.SplitN(parts[1], "/", 2)[0]
}

// senderIDFromParams extracts the business sender_id from RPC params.
// For admin users (WS auth identity "admin"), if params don't specify a sender_id,
// it defaults to cliSenderID — because admin is a ROLE, not a business identity.
// All CLI subscriptions, settings, and per-user state live under cliSenderID.
//
// For non-admin web users, falls back to their WS auth identity directly.
func senderIDFromParams(params json.RawMessage, authSenderID string) string {
	var p struct {
		SenderID string `json:"sender_id"`
	}
	if err := json.Unmarshal(params, &p); err == nil && p.SenderID != "" {
		return p.SenderID
	}
	if isAdmin(authSenderID) {
		return cliSenderID
	}
	return authSenderID
}

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
