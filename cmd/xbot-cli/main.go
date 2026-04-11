// xbot CLI entry point
// Standalone terminal-based chat interface
//
// Usage:
//   xbot-cli               恢复上次会话（默认）
//   xbot-cli --resume      恢复会话并显示当前状态
//   xbot-cli --new         开始新会话
//   xbot-cli <prompt>      非交互模式执行单次 prompt
//   xbot-cli -p <prompt>   非交互模式执行单次 prompt
//   echo "hello" | xbot-cli  管道模式
//   xbot-cli --config <path>  指定配置文件路径

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/storage"
	"xbot/storage/sqlite"
	"xbot/tools"
	"xbot/version"

	"github.com/google/uuid"
	"github.com/mattn/go-isatty"
)

// saveWg tracks in-flight config saves so SIGINT can wait for them.
var saveWg sync.WaitGroup

// cliApp 封装 CLI 的公共初始化逻辑，供交互和非交互模式共享。
type cliApp struct {
	cfg       *config.Config
	llmClient llm.LLM
	msgBus    *bus.MessageBus
	db        *sqlite.DB
	agentLoop *agent.Agent
	workDir   string
	xbotHome  string
}

// isFirstRun 检测是否是首次运行（config.json 不存在或 API Key 未配置）
func isFirstRun() bool {
	configPath := config.ConfigFilePath()
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return true
	}
	cfg := config.LoadFromFile(configPath)
	if cfg == nil {
		return true
	}
	return cfg.LLM.APIKey == ""
}

// newCLIApp 执行公共初始化：加载配置、创建 LLM/DB/Agent。
func newCLIApp(configPath string) *cliApp {
	var cfg *config.Config
	if configPath != "" {
		cfg = config.LoadFromPath(configPath)
	} else {
		cfg = config.Load()
	}

	// Derive cfg.LLM from active subscription (single source of truth)
	syncLLMFromActiveSub(cfg)

	workDir := cfg.Agent.WorkDir
	xbotHome := config.XbotHome()
	dbPath := config.DBFilePath()

	if err := setupLogger(cfg.Log, xbotHome); err != nil {
		log.WithError(err).Fatal("Failed to setup logger")
	}

	llmClient, err := createLLM(cfg.LLM, llm.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    cfg.Agent.LLMRetryDelay,
		MaxDelay: cfg.Agent.LLMRetryMaxDelay,
		Timeout:  cfg.Agent.LLMRetryTimeout,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to create LLM client")
	}
	log.WithFields(log.Fields{
		"provider": cfg.LLM.Provider,
		"model":    cfg.LLM.Model,
	}).Info("LLM client created")

	msgBus := bus.NewMessageBus()

	if err := storage.MigrateIfNeeded(context.Background(), workDir, dbPath); err != nil {
		log.WithError(err).Fatal("Failed to migrate data to SQLite")
	}

	db, err := sqlite.Open(dbPath)
	if err != nil {
		log.WithError(err).Warn("Failed to open token database, runner tokens disabled")
	} else {
		tools.SetRunnerTokenDB(db.Conn())
	}

	embBaseURL := cfg.Embedding.BaseURL
	if embBaseURL == "" {
		embBaseURL = cfg.LLM.BaseURL
	}
	embAPIKey := cfg.Embedding.APIKey
	if embAPIKey == "" {
		embAPIKey = cfg.LLM.APIKey
	}

	tools.InitSandbox(cfg.Sandbox, workDir)

	agentLoop := agent.New(agent.Config{
		Bus:                  msgBus,
		LLM:                  llmClient,
		Model:                cfg.LLM.Model,
		MaxIterations:        cfg.Agent.MaxIterations,
		MaxConcurrency:       cfg.Agent.MaxConcurrency,
		DBPath:               dbPath,
		SkillsDir:            filepath.Join(xbotHome, "skills"),
		AgentsDir:            filepath.Join(xbotHome, "agents"),
		WorkDir:              workDir,
		PromptFile:           cfg.Agent.PromptFile,
		DirectWorkspace:      workDir, // CLI: workspace = workDir directly (no per-user subdirectory)
		SandboxMode:          cfg.Sandbox.Mode,
		Sandbox:              tools.GetSandbox(),
		MemoryProvider:       cfg.Agent.MemoryProvider,
		EmbeddingProvider:    cfg.Embedding.Provider,
		EmbeddingBaseURL:     embBaseURL,
		EmbeddingAPIKey:      embAPIKey,
		EmbeddingModel:       cfg.Embedding.Model,
		EmbeddingMaxTokens:   cfg.Embedding.MaxTokens,
		MCPInactivityTimeout: cfg.Agent.MCPInactivityTimeout,
		MCPCleanupInterval:   cfg.Agent.MCPCleanupInterval,
		SessionCacheTimeout:  cfg.Agent.SessionCacheTimeout,
		EnableAutoCompress:   cfg.Agent.EffectiveEnableAutoCompress(),
		MaxContextTokens:     cfg.Agent.MaxContextTokens,
		CompressionThreshold: cfg.Agent.CompressionThreshold,
		ContextMode:          agent.ContextMode(cfg.Agent.ContextMode),
		MaxSubAgentDepth:     cfg.Agent.MaxSubAgentDepth,
		OffloadDir:           filepath.Join(xbotHome, "offload_store"),
	})
	agentLoop.RegisterCoreTool(tools.NewWebSearchTool(cfg.TavilyAPIKey))
	agentLoop.IndexGlobalTools()

	return &cliApp{
		cfg:       cfg,
		llmClient: llmClient,
		msgBus:    msgBus,
		db:        db,
		agentLoop: agentLoop,
		workDir:   workDir,
		xbotHome:  xbotHome,
	}
}

// Close 释放资源。
func (app *cliApp) Close() {
	if app.db != nil {
		app.db.Close()
	}
	log.Close()
}

func main() {
	fmt.Printf("xbot CLI %s\n", version.Version)

	// 命令行标志
	var (
		configPath    string
		newSession    bool
		prompt        string
		flagShare     string
		flagToken     string
		flagWorkspace string
	)

	flag.StringVar(&configPath, "config", "", "path to config file (default: $XBOT_HOME/config.json)")
	flag.BoolVar(&newSession, "new", false, "start a new session")
	flag.BoolVar(&newSession, "resume", false, "resume last session (default behavior)")
	flag.StringVar(&flagShare, "share", "", "WebSocket URL for shared runner mode")
	flag.StringVar(&flagToken, "token", "", "auth token for shared runner mode")
	flag.StringVar(&flagWorkspace, "workspace", "", "override working directory")
	flag.StringVar(&prompt, "p", "", "one-shot prompt (non-interactive mode)")
	flag.Parse()

	// 位置参数作为 prompt
	if prompt == "" && flag.NArg() > 0 {
		prompt = flag.Arg(0)
	}

	// stdin 管道模式
	if prompt == "" && !isatty.IsTerminal(os.Stdin.Fd()) {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			log.WithError(err).Fatal("Failed to read from stdin")
		}
		prompt = strings.TrimSpace(string(data))
	}

	// 首次运行检测（仅在交互模式下，传给 TUI 做 setup panel）
	firstRun := prompt == "" && isFirstRun()

	// 非交互模式
	if prompt != "" {
		executeNonInteractive(prompt, configPath)
		return
	}

	if newSession {
		fmt.Println("Mode: new session (--new)")
	} else {
		fmt.Println("Mode: resuming last session (use --new for new session)")
	}
	fmt.Println("Starting...")

	app := newCLIApp(configPath)
	defer app.Close()

	disp := channel.NewDispatcher(app.msgBus)

	// 用工作目录绝对路径作为 ChatID，不同目录有不同的会话
	absWorkDir, _ := filepath.Abs(app.workDir)

	cliCfg := channel.CLIChannelConfig{
		WorkDir:    app.workDir,
		ChatID:     absWorkDir,
		IsFirstRun: firstRun,
		GetCurrentValues: func() map[string]string {
			return map[string]string{
				"llm_provider": app.cfg.LLM.Provider,
				"llm_api_key":  app.cfg.LLM.APIKey,
				"llm_model":    app.cfg.LLM.Model,
				"llm_base_url": app.cfg.LLM.BaseURL,
				"sandbox_mode": func() string {
					if app.cfg.Sandbox.Mode != "" {
						return app.cfg.Sandbox.Mode
					}
					return "none"
				}(),
				"memory_provider":    app.cfg.Agent.MemoryProvider,
				"tavily_api_key":     app.cfg.TavilyAPIKey,
				"context_mode":       app.cfg.Agent.ContextMode,
				"max_iterations":     fmt.Sprintf("%d", app.cfg.Agent.MaxIterations),
				"max_concurrency":    fmt.Sprintf("%d", app.cfg.Agent.MaxConcurrency),
				"max_context_tokens": fmt.Sprintf("%d", app.cfg.Agent.MaxContextTokens),
				"max_output_tokens": func() string {
					for _, sub := range app.cfg.Subscriptions {
						if sub.Active {
							if sub.MaxOutputTokens > 0 {
								return fmt.Sprintf("%d", sub.MaxOutputTokens)
							}
							break
						}
					}
					return "8192" // default value used in llm/openai.go
				}(),
				"thinking_mode": func() string {
					for _, sub := range app.cfg.Subscriptions {
						if sub.Active {
							return sub.ThinkingMode
						}
					}
					return ""
				}(),
				"enable_auto_compress": func() string {
					if app.cfg.Agent.EnableAutoCompress == nil || *app.cfg.Agent.EnableAutoCompress {
						return "true"
					}
					return "false"
				}(),
				"theme": func() string {
					// Read persisted theme from settings, default to dark
					if app.agentLoop != nil {
						if ss := app.agentLoop.GetSettingsService(); ss != nil {
							if vals, err := ss.GetSettings("cli", "cli_user"); err == nil {
								if t, ok := vals["theme"]; ok && t != "" {
									return t
								}
							}
						}
					}
					return "midnight"
				}(),
				"language": func() string {
					if app.agentLoop != nil {
						if ss := app.agentLoop.GetSettingsService(); ss != nil {
							if vals, err := ss.GetSettings("cli", "cli_user"); err == nil {
								if l, ok := vals["language"]; ok {
									return l
								}
							}
						}
					}
					return ""
				}(),
			}
		},
		ApplySettings: func(values map[string]string) {
			// Apply LLM settings — write to active subscription (single source of truth)
			_, llmChanged := values["llm_provider"]
			_, keyChanged := values["llm_api_key"]
			_, modelChanged := values["llm_model"]
			_, urlChanged := values["llm_base_url"]
			if llmChanged || keyChanged || modelChanged || urlChanged {
				// Write to active subscription
				for i := range app.cfg.Subscriptions {
					if app.cfg.Subscriptions[i].Active {
						if v, ok := values["llm_provider"]; ok && v != "" {
							app.cfg.Subscriptions[i].Provider = v
						}
						if v, ok := values["llm_api_key"]; ok && v != "" {
							app.cfg.Subscriptions[i].APIKey = v
						}
						if v, ok := values["llm_model"]; ok && v != "" {
							app.cfg.Subscriptions[i].Model = v
						}
						if v, ok := values["llm_base_url"]; ok && v != "" {
							app.cfg.Subscriptions[i].BaseURL = v
						}
						break
					}
				}
				// Auto-set default base URL when switching provider
				if v, ok := values["llm_provider"]; ok && v != "" {
					if _, urlSet := values["llm_base_url"]; !urlSet {
						switch v {
						case "anthropic":
							for i := range app.cfg.Subscriptions {
								if app.cfg.Subscriptions[i].Active {
									app.cfg.Subscriptions[i].BaseURL = "https://api.anthropic.com"
									break
								}
							}
						case "openai":
							for i := range app.cfg.Subscriptions {
								if app.cfg.Subscriptions[i].Active {
									if app.cfg.Subscriptions[i].BaseURL == "https://api.anthropic.com" {
										app.cfg.Subscriptions[i].BaseURL = "https://api.openai.com/v1"
									}
									break
								}
							}
						}
					}
				}
				// Derive cfg.LLM from active subscription
				syncLLMFromActiveSub(app.cfg)
			}
			// Apply Sandbox settings
			if v, ok := values["sandbox_mode"]; ok && v != "" {
				app.cfg.Sandbox.Mode = v
				// Reinitialize sandbox so the new mode takes effect immediately
				tools.ReinitSandbox(app.cfg.Sandbox, app.workDir)
				// Sync the new sandbox into the agent loop
				if app.agentLoop != nil {
					app.agentLoop.SetSandbox(tools.GetSandbox(), v)
				}
			}
			// Apply Agent settings
			if v, ok := values["memory_provider"]; ok && v != "" {
				app.cfg.Agent.MemoryProvider = v
			}
			if v, ok := values["tavily_api_key"]; ok {
				app.cfg.TavilyAPIKey = v
			}
			if v, ok := values["context_mode"]; ok && v != "" {
				app.cfg.Agent.ContextMode = v
			}
			if v, ok := values["max_iterations"]; ok {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					app.cfg.Agent.MaxIterations = n
				}
			}
			if v, ok := values["max_concurrency"]; ok {
				if n, err := strconv.Atoi(v); err == nil && n > 0 {
					app.cfg.Agent.MaxConcurrency = n
				}
			}
			if v, ok := values["max_context_tokens"]; ok {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 {
					app.cfg.Agent.MaxContextTokens = n
				}
			}
			if v, ok := values["max_output_tokens"]; ok {
				if n, err := strconv.Atoi(v); err == nil && n >= 0 {
					for i := range app.cfg.Subscriptions {
						if app.cfg.Subscriptions[i].Active {
							app.cfg.Subscriptions[i].MaxOutputTokens = n
							break
						}
					}
					// Sync to cfg.LLM so createLLM picks it up on rebuild
					app.cfg.LLM.MaxOutputTokens = n
					// Rebuild LLM client with new max_output_tokens
					if newClient, err := createLLM(app.cfg.LLM, llm.RetryConfig{
						Attempts: 5,
						Delay:    1 * time.Second,
						MaxDelay: 30 * time.Second,
					}); err == nil {
						app.llmClient = newClient
						if app.agentLoop != nil {
							app.agentLoop.LLMFactory().SetDefaults(newClient, app.cfg.LLM.Model)
						}
					} else {
						log.Warnf("Failed to rebuild LLM client: %v", err)
					}
				}
			}
			if v, ok := values["thinking_mode"]; ok {
				for i := range app.cfg.Subscriptions {
					if app.cfg.Subscriptions[i].Active {
						app.cfg.Subscriptions[i].ThinkingMode = v
						break
					}
				}
				// Sync to factory so next GetLLM picks up the change
				if app.agentLoop != nil {
					app.agentLoop.LLMFactory().SetDefaultThinkingMode(v)
				}
			}
			if v, ok := values["enable_auto_compress"]; ok {
				b := v == "true"
				app.cfg.Agent.EnableAutoCompress = &b
			}
			// Persist to config.json
			if err := config.SaveToFile(config.ConfigFilePath(), app.cfg); err != nil {
				log.Warnf("Failed to save config.json: %v", err)
			}
			// Persist theme to settings service (theme is CLI-specific, not in config.json)
			if theme, ok := values["theme"]; ok && theme != "" && app.agentLoop != nil {
				if ss := app.agentLoop.GetSettingsService(); ss != nil {
					_ = ss.SetSetting("cli", "cli_user", "theme", theme)
				}
			}
			// Rebuild LLM client and update agent runtime when LLM config changed
			if llmChanged || keyChanged || modelChanged || urlChanged {
				if newClient, err := createLLM(app.cfg.LLM, llm.RetryConfig{
					Attempts: 5,
					Delay:    1 * time.Second,
					MaxDelay: 30 * time.Second,
				}); err == nil {
					app.llmClient = newClient
					if app.agentLoop != nil {
						app.agentLoop.LLMFactory().SetDefaults(newClient, app.cfg.LLM.Model)
					}
				} else {
					log.Warnf("Failed to rebuild LLM client: %v", err)
				}
			}
			// Update agent runtime state
			if app.agentLoop != nil {
				if v, ok := values["context_mode"]; ok && v != "" {
					_ = app.agentLoop.SetContextMode(v)
				}
				if v, ok := values["max_iterations"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						app.agentLoop.SetMaxIterations(n)
					}
				}
				if v, ok := values["max_concurrency"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						app.agentLoop.SetMaxConcurrency(n)
					}
				}
				if v, ok := values["max_context_tokens"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n >= 0 {
						app.agentLoop.SetMaxContextTokens(n)
					}
				}
				// enable_auto_compress maps to context_mode: true→auto, false→none
				if v, ok := values["enable_auto_compress"]; ok {
					if v == "true" {
						_ = app.agentLoop.SetContextMode("auto")
					} else {
						_ = app.agentLoop.SetContextMode("none")
					}
				}
			}
		},
		ClearMemory: func(targetType string) error {
			if app.agentLoop == nil {
				return fmt.Errorf("agent not initialized")
			}
			return app.agentLoop.MultiSession().ClearMemory(context.Background(), "cli", absWorkDir, targetType, "cli_user")
		},
		GetMemoryStats: func() map[string]string {
			if app.agentLoop == nil {
				return map[string]string{}
			}
			return app.agentLoop.MultiSession().GetMemoryStats(context.Background(), "cli", absWorkDir, "cli_user")
		},
		SwitchLLM: func(provider, baseURL, apiKey, model string) error {
			llmCfg := config.LLMConfig{
				Provider: provider,
				BaseURL:  baseURL,
				APIKey:   apiKey,
				Model:    model,
			}
			client, err := createLLM(llmCfg, llm.RetryConfig{
				Attempts: 5,
				Delay:    1 * time.Second,
				MaxDelay: 30 * time.Second,
			})
			if err != nil {
				return fmt.Errorf("create LLM: %w", err)
			}
			app.llmClient = client
			if app.agentLoop != nil {
				app.agentLoop.LLMFactory().SetDefaults(client, model)
			}
			return nil
		},
		UsageQuery: func(senderID string, days int) (*sqlite.UserTokenUsage, []sqlite.DailyTokenUsage, error) {
			if app.agentLoop == nil {
				return nil, nil, fmt.Errorf("agent not initialized")
			}
			ms := app.agentLoop.MultiSession()
			cumulative, err := ms.GetUserTokenUsage(senderID)
			if err != nil {
				return nil, nil, err
			}
			daily, err := ms.GetDailyTokenUsage(senderID, days)
			if err != nil {
				return nil, nil, err
			}
			return cumulative, daily, nil
		},
		AgentCount: func() int {
			if app.agentLoop == nil {
				return 0
			}
			return app.agentLoop.CountInteractiveSessions("cli", absWorkDir)
		},
		AgentList: func() []channel.AgentPanelEntry {
			if app.agentLoop == nil {
				return nil
			}
			sessions := app.agentLoop.ListInteractiveSessions("cli", absWorkDir)
			entries := make([]channel.AgentPanelEntry, len(sessions))
			for i, s := range sessions {
				entries[i] = channel.AgentPanelEntry{
					Role:       s.Role,
					Instance:   s.Instance,
					Running:    s.Running,
					Background: s.Background,
				}
			}
			return entries
		},
		AgentInspect: func(roleName, instance string, tailCount int) (string, error) {
			return app.agentLoop.InspectInteractiveSession(context.Background(), roleName, "cli", absWorkDir, instance, tailCount)
		},
	}

	// 设置历史消息加载器（会话恢复）
	var cliTenantID int64
	var cliSessionSvc *sqlite.SessionService
	var tenantSvc *sqlite.TenantService
	if app.db != nil {
		tenantSvc = sqlite.NewTenantService(app.db)
		cliSessionSvc = sqlite.NewSessionService(app.db)
		tenantID, err := tenantSvc.GetOrCreateTenantID("cli", absWorkDir)
		if err == nil {
			cliTenantID = tenantID
			cliCfg.HistoryLoader = func() ([]channel.HistoryMessage, error) {
				msgs, err := cliSessionSvc.GetAllMessages(cliTenantID)
				if err != nil {
					return nil, err
				}
				return channel.ConvertMessagesToHistory(msgs), nil
			}
		}
	}

	// /su 动态历史加载器：从 web tenant 加载目标用户历史
	if tenantSvc != nil && cliSessionSvc != nil {
		cliCfg.DynamicHistoryLoader = func(_, chatID string) ([]channel.HistoryMessage, error) {
			tid, err := tenantSvc.GetOrCreateTenantID("web", chatID)
			if err != nil {
				return nil, fmt.Errorf("get tenant: %w", err)
			}
			msgs, err := cliSessionSvc.GetAllMessages(tid)
			if err != nil {
				return nil, err
			}
			return channel.ConvertMessagesToHistory(msgs), nil
		}
	}

	cliCh := channel.NewCLIChannel(cliCfg, app.msgBus)
	disp.Register(cliCh)

	// Inject SettingsService for interactive /settings panel
	if app.agentLoop != nil {
		if ss := app.agentLoop.GetSettingsService(); ss != nil {
			cliCh.SetSettingsService(ss)
		}
		cliCh.SetModelLister(app.agentLoop.LLMFactory())
		// Inject BgTaskManager for background task display
		bgSessionKey := "cli:" + cliCfg.ChatID
		cliCh.SetBgTaskManager(app.agentLoop.BgTaskManager(), bgSessionKey)
		// Inject ApprovalHook for permission control approval dialog
		if hook := app.agentLoop.ToolHookChain().Get("approval"); hook != nil {
			if ah, ok := hook.(*tools.ApprovalHook); ok {
				cliCh.SetApprovalHook(ah)
			}
		}
		// Inject TrimHistoryFn for Ctrl+K session truncation
		if cliTenantID != 0 && cliSessionSvc != nil {
			cliCh.SetTrimHistoryFn(func(keepCount int) error {
				if keepCount <= 0 {
					return nil
				}
				_, err := cliSessionSvc.PurgeOldMessages(cliTenantID, keepCount)
				return err
			})
		}
	}

	// Apply saved theme at startup
	if ss := app.agentLoop.GetSettingsService(); ss != nil {
		if vals, err := ss.GetSettings("cli", "cli_user"); err == nil {
			if t, ok := vals["theme"]; ok && t != "" {
				channel.ApplyTheme(t)
			}
		}
	}

	// 注入 channelFinder 以启用结构化进度事件（工具调用、思考过程等）
	app.agentLoop.SetDirectSend(disp.SendDirect)
	app.agentLoop.SetChannelFinder(disp.GetChannel)

	// 注入 CLI 渠道特化 prompt 提供者
	app.agentLoop.SetChannelPromptProviders(&channel.CliPromptProvider{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.agentLoop.Run(ctx)
	go disp.Run()

	if newSession {
		app.msgBus.Inbound <- bus.InboundMessage{
			Channel:    "cli",
			SenderID:   "cli_user",
			ChatID:     absWorkDir,
			ChatType:   "p2p",
			Content:    "/new",
			SenderName: "CLI User",
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		}
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info("Received shutdown signal, waiting for pending saves...")
		saveWg.Wait()
		log.Info("All saves complete, shutting down")
		cancel()
	}()

	// Runner Bridge: inject LLM client, model list and provider for runner use
	cliCh.SetRunnerLLM(app.llmClient, func() []string {
		if app.agentLoop != nil {
			return app.agentLoop.LLMFactory().ListModels()
		}
		return nil
	}(), app.cfg.LLM.Provider)

	// Multi-subscription support (config-based, no database)
	if len(app.cfg.Subscriptions) == 0 {
		// Migration: create first subscription from current LLM config
		app.cfg.Subscriptions = []config.SubscriptionConfig{{
			ID:       "default",
			Name:     app.cfg.LLM.Provider,
			Provider: app.cfg.LLM.Provider,
			BaseURL:  app.cfg.LLM.BaseURL,
			APIKey:   app.cfg.LLM.APIKey,
			Model:    app.cfg.LLM.Model,
			Active:   true,
		}}
		if err := config.SaveToFile(config.ConfigFilePath(), app.cfg); err != nil {
			log.WithError(err).Warn("Failed to save migrated subscriptions")
		}
	}
	saveConfig := func() error {
		saveWg.Add(1)
		defer saveWg.Done()
		return config.SaveToFile(config.ConfigFilePath(), app.cfg)
	}
	cliCh.SetSubscriptionManager(newConfigSubscriptionManager(app.cfg, saveConfig))
	cliCh.SetLLMSubscriber(newConfigLLMSubscriber(app.cfg, app.agentLoop.LLMFactory(), saveConfig))

	// --share flag: auto-connect as runner after TUI starts
	if flagShare != "" {
		shareURL := flagShare
		shareToken := flagToken
		shareWorkspace := flagWorkspace
		if shareWorkspace == "" {
			shareWorkspace = app.workDir
		}
		cliCh.StartWithRunner(shareURL, shareToken, shareWorkspace)
	} else {
		if err := cliCh.Start(); err != nil {
			log.WithError(err).Error("CLI channel error")
			app.Close()
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Adapters: bridge config/types to CLI interfaces
// ---------------------------------------------------------------------------

// configSubscriptionManager manages CLI subscriptions in config.json (no database).
type configSubscriptionManager struct {
	cfg    *config.Config
	saveFn func() error // persists config to disk
}

func newConfigSubscriptionManager(cfg *config.Config, saveFn func() error) *configSubscriptionManager {
	return &configSubscriptionManager{cfg: cfg, saveFn: saveFn}
}

func (m *configSubscriptionManager) List(_ string) ([]channel.Subscription, error) {
	result := make([]channel.Subscription, len(m.cfg.Subscriptions))
	for i, s := range m.cfg.Subscriptions {
		result[i] = channel.Subscription{
			ID:       s.ID,
			Name:     s.Name,
			Provider: s.Provider,
			BaseURL:  s.BaseURL,
			APIKey:   s.APIKey,
			Model:    s.Model,
			Active:   s.Active,
		}
	}
	return result, nil
}

func (m *configSubscriptionManager) GetDefault(_ string) (*channel.Subscription, error) {
	for _, s := range m.cfg.Subscriptions {
		if s.Active {
			return &channel.Subscription{
				ID:       s.ID,
				Name:     s.Name,
				Provider: s.Provider,
				Model:    s.Model,
				Active:   true,
			}, nil
		}
	}
	return nil, nil
}

func (m *configSubscriptionManager) Add(sub *channel.Subscription) error {
	m.cfg.Subscriptions = append(m.cfg.Subscriptions, config.SubscriptionConfig{
		ID:       sub.ID,
		Name:     sub.Name,
		Provider: sub.Provider,
		BaseURL:  sub.BaseURL,
		APIKey:   sub.APIKey,
		Model:    sub.Model,
		Active:   sub.Active,
	})
	return m.saveFn()
}

func (m *configSubscriptionManager) Remove(id string) error {
	filtered := m.cfg.Subscriptions[:0]
	for _, s := range m.cfg.Subscriptions {
		if s.ID != id {
			filtered = append(filtered, s)
		}
	}
	if len(filtered) == len(m.cfg.Subscriptions) {
		return fmt.Errorf("subscription %s not found", id)
	}
	m.cfg.Subscriptions = filtered
	return m.saveFn()
}

func (m *configSubscriptionManager) SetDefault(id string) error {
	found := false
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Active = true
			found = true
		} else {
			m.cfg.Subscriptions[i].Active = false
		}
	}
	if !found {
		return fmt.Errorf("subscription %s not found", id)
	}
	// Derive cfg.LLM from new active subscription
	syncLLMFromActiveSub(m.cfg)
	return m.saveFn()
}

func (m *configSubscriptionManager) SetModel(id, model string) error {
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Model = model
			// If modifying active subscription, sync cfg.LLM
			if m.cfg.Subscriptions[i].Active {
				syncLLMFromActiveSub(m.cfg)
			}
			return m.saveFn()
		}
	}
	return fmt.Errorf("subscription %s not found", id)
}

func (m *configSubscriptionManager) Rename(id, name string) error {
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Name = name
			return m.saveFn()
		}
	}
	return fmt.Errorf("subscription %s not found", id)
}

// configLLMSubscriber switches LLM at runtime using config-based subscriptions.
// Single source of truth: cfg.Subscriptions[active].Model/Provider/BaseURL/APIKey.
// cfg.LLM.* is a read-only view derived from the active subscription.
type configLLMSubscriber struct {
	cfg     *config.Config
	factory *agent.LLMFactory
	saveFn  func() error
}

func newConfigLLMSubscriber(cfg *config.Config, factory *agent.LLMFactory, saveFn func() error) *configLLMSubscriber {
	// On startup, derive cfg.LLM from active subscription
	syncLLMFromActiveSub(cfg)
	return &configLLMSubscriber{cfg: cfg, factory: factory, saveFn: saveFn}
}

// syncLLMFromActiveSub derives cfg.LLM.* from the active subscription.
// This is the ONLY place that writes cfg.LLM fields.
func syncLLMFromActiveSub(cfg *config.Config) {
	for _, sc := range cfg.Subscriptions {
		if sc.Active {
			cfg.LLM.Provider = sc.Provider
			cfg.LLM.BaseURL = sc.BaseURL
			cfg.LLM.APIKey = sc.APIKey
			cfg.LLM.Model = sc.Model
			cfg.LLM.MaxOutputTokens = sc.MaxOutputTokens
			cfg.LLM.ThinkingMode = sc.ThinkingMode
			return
		}
	}
	// No active subscription — keep cfg.LLM as-is (single-subscription or migration case)
}

func (s *configLLMSubscriber) SwitchSubscription(senderID string, sub *channel.Subscription) error {
	// Find full config (with API key) for this subscription
	for i := range s.cfg.Subscriptions {
		if s.cfg.Subscriptions[i].ID == sub.ID {
			sc := &s.cfg.Subscriptions[i]
			// Inherit from global config if not specified per-subscription
			provider := sc.Provider
			baseURL := sc.BaseURL
			apiKey := sc.APIKey
			if provider == "" {
				provider = s.cfg.LLM.Provider
			}
			if baseURL == "" {
				baseURL = s.cfg.LLM.BaseURL
			}
			if apiKey == "" {
				apiKey = s.cfg.LLM.APIKey
			}
			llmCfg := config.LLMConfig{
				Provider:        provider,
				BaseURL:         baseURL,
				APIKey:          apiKey,
				Model:           sc.Model,
				MaxOutputTokens: sc.MaxOutputTokens,
			}
			client, err := createLLM(llmCfg, llm.RetryConfig{
				Attempts: 5,
				Delay:    1 * time.Second,
				MaxDelay: 30 * time.Second,
			})
			if err != nil {
				return fmt.Errorf("create LLM for subscription: %w", err)
			}
			s.factory.SetDefaults(client, sc.Model)
			s.factory.SetDefaultThinkingMode(sc.ThinkingMode)
			// Set active flag + derive cfg.LLM + save (all in one place)
			for j := range s.cfg.Subscriptions {
				s.cfg.Subscriptions[j].Active = (s.cfg.Subscriptions[j].ID == sub.ID)
			}
			syncLLMFromActiveSub(s.cfg)
			return s.saveFn()
		}
	}
	return fmt.Errorf("subscription %s not found in config", sub.ID)
}

func (s *configLLMSubscriber) SwitchModel(senderID, model string) {
	s.factory.SwitchModel(senderID, model)
	// Single source of truth: update active subscription's model, then derive cfg.LLM
	for i := range s.cfg.Subscriptions {
		if s.cfg.Subscriptions[i].Active {
			s.cfg.Subscriptions[i].Model = model
			break
		}
	}
	syncLLMFromActiveSub(s.cfg)
	if err := s.saveFn(); err != nil {
		log.WithError(err).Warn("Failed to persist model switch")
	}
}

func (s *configLLMSubscriber) GetDefaultModel() string {
	return s.factory.GetDefaultModel()
}

// executeNonInteractive 非交互模式：单次执行 prompt 并输出到 stdout。
func executeNonInteractive(prompt, configPath string) {
	app := newCLIApp(configPath)
	defer app.Close()

	absWorkDir, _ := filepath.Abs(app.workDir)

	nonIntCh := channel.NewNonInteractiveChannel(app.msgBus)
	disp := channel.NewDispatcher(app.msgBus)
	disp.Register(nonIntCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.agentLoop.Run(ctx)
	go disp.Run()

	app.msgBus.Inbound <- bus.InboundMessage{
		Channel:    "cli",
		SenderID:   "cli_user",
		ChatID:     absWorkDir,
		ChatType:   "p2p",
		Content:    prompt,
		SenderName: "CLI User",
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
	}

	nonIntCh.WaitDone()
}

// setupLogger 配置日志（CLI 模式：仅文件输出，不干扰终端 TUI）。
// 日志写入全局 xbotHome/logs 目录。
func setupLogger(cfg config.LogConfig, xbotHome string) error {
	logDir := filepath.Join(xbotHome, "logs")
	return log.Setup(log.SetupConfig{
		Level:    cfg.Level,
		Format:   cfg.Format,
		LogDir:   logDir,
		MaxAge:   7,
		FileOnly: true,
	})
}

// createLLM 根据配置创建 LLM 客户端（带重试、指数退避和随机抖动）。
func createLLM(cfg config.LLMConfig, retryCfg llm.RetryConfig) (llm.LLM, error) {
	var inner llm.LLM
	switch cfg.Provider {
	case "openai":
		inner = llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
			MaxTokens:    cfg.MaxOutputTokens,
		})
	case "anthropic":
		inner = llm.NewAnthropicLLM(llm.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
			MaxTokens:    cfg.MaxOutputTokens,
		})
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.Provider)
	}
	return llm.NewRetryLLM(inner, retryCfg), nil
}
