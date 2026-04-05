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

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
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
func newCLIApp() *cliApp {
	cfg := config.Load()

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
		SingleUser:           true,
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

	// 解析命令行标志
	prompt := ""
	newSession := false
	var (
		flagShare     string // --share ws://host:port/ws/userID
		flagToken     string // --token xxx
		flagWorkspace string // --workspace /path (overrides config)
	)
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--resume":
			// 保留兼容性，行为与默认相同
		case "--new":
			newSession = true
		case "-p":
			if len(os.Args) > i+1 {
				prompt = os.Args[i+1]
			}
		case "--share":
			if len(os.Args) > i+1 {
				flagShare = os.Args[i+1]
				i++
			}
		case "--token":
			if len(os.Args) > i+1 {
				flagToken = os.Args[i+1]
				i++
			}
		case "--workspace":
			if len(os.Args) > i+1 {
				flagWorkspace = os.Args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(os.Args[i], "-") {
				prompt = os.Args[i]
			}
		}
	}
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
		executeNonInteractive(prompt)
		return
	}

	if newSession {
		fmt.Println("模式: 新会话 (--new)")
	} else {
		fmt.Println("模式: 恢复上次会话 (使用 --new 开始新会话)")
	}
	fmt.Println("Starting...")

	app := newCLIApp()
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
				"llm_provider":       app.cfg.LLM.Provider,
				"llm_api_key":        app.cfg.LLM.APIKey,
				"llm_model":          app.cfg.LLM.Model,
				"llm_base_url":       app.cfg.LLM.BaseURL,
				"sandbox_mode":       app.cfg.Sandbox.Mode,
				"memory_provider":    app.cfg.Agent.MemoryProvider,
				"tavily_api_key":     app.cfg.TavilyAPIKey,
				"context_mode":       app.cfg.Agent.ContextMode,
				"max_iterations":     fmt.Sprintf("%d", app.cfg.Agent.MaxIterations),
				"max_concurrency":    fmt.Sprintf("%d", app.cfg.Agent.MaxConcurrency),
				"max_context_tokens": fmt.Sprintf("%d", app.cfg.Agent.MaxContextTokens),
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
			// Apply LLM settings
			if v, ok := values["llm_provider"]; ok && v != "" {
				app.cfg.LLM.Provider = v
				// Auto-set default base URL when switching provider
				if _, urlSet := values["llm_base_url"]; !urlSet {
					switch v {
					case "anthropic":
						app.cfg.LLM.BaseURL = "https://api.anthropic.com"
					case "openai":
						if app.cfg.LLM.BaseURL == "https://api.anthropic.com" {
							app.cfg.LLM.BaseURL = "https://api.openai.com/v1"
						}
					}
				}
			}
			if v, ok := values["llm_api_key"]; ok && v != "" {
				app.cfg.LLM.APIKey = v
			}
			if v, ok := values["llm_model"]; ok && v != "" {
				app.cfg.LLM.Model = v
			}
			if v, ok := values["llm_base_url"]; ok && v != "" {
				app.cfg.LLM.BaseURL = v
			}
			// Apply Sandbox settings
			if v, ok := values["sandbox_mode"]; ok && v != "" {
				app.cfg.Sandbox.Mode = v
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
			_, llmChanged := values["llm_provider"]
			_, keyChanged := values["llm_api_key"]
			_, modelChanged := values["llm_model"]
			_, urlChanged := values["llm_base_url"]
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

	// /su 动态历史加载器：根据目标 channel/chatID 查找 tenant 并加载历史
	if tenantSvc != nil && cliSessionSvc != nil {
		cliCfg.DynamicHistoryLoader = func(channelName, chatID string) ([]channel.HistoryMessage, error) {
			tid, err := tenantSvc.GetOrCreateTenantID(channelName, chatID)
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

	// /su: when TUI switches to web user identity, register CLI as "web" channel
	// so dispatcher SendDirect and outbound loop can find it.
	cliCh.OnSuChange = func(targetChannel string, enable bool) {
		if enable {
			disp.RegisterAs(targetChannel, cliCh)
		} else {
			disp.Unregister(targetChannel)
		}
	}

	// Inject SettingsService for interactive /settings panel
	if app.agentLoop != nil {
		if ss := app.agentLoop.GetSettingsService(); ss != nil {
			cliCh.SetSettingsService(ss)
		}
		cliCh.SetModelLister(app.agentLoop.LLMFactory())
		// Inject BgTaskManager for background task display
		bgSessionKey := "cli:" + cliCfg.ChatID
		cliCh.SetBgTaskManager(app.agentLoop.BgTaskManager(), bgSessionKey)
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
		log.Info("Received shutdown signal")
		cancel()
	}()

	// Runner Bridge: inject LLM client, model list and provider for runner use
	cliCh.SetRunnerLLM(app.llmClient, func() []string {
		if app.agentLoop != nil {
			return app.agentLoop.LLMFactory().ListModels()
		}
		return nil
	}(), app.cfg.LLM.Provider)

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
			log.WithError(err).Fatal("CLI channel error")
		}
	}
}

// executeNonInteractive 非交互模式：单次执行 prompt 并输出到 stdout。
func executeNonInteractive(prompt string) {
	app := newCLIApp()
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
		})
	case "anthropic":
		inner = llm.NewAnthropicLLM(llm.AnthropicConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		})
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.Provider)
	}
	return llm.NewRetryLLM(inner, retryCfg), nil
}
