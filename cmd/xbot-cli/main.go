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
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
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
	"xbot/serverapp"
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
	backend   agent.AgentBackend
	workDir   string
	xbotHome  string

	// Remote-mode async cache for agent info (avoid RPC from event loop → deadlock)
	agentCacheMu    sync.RWMutex
	agentCacheCount int
	agentCacheList  []channel.AgentPanelEntry
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

// isLocalServer returns true if the server URL points to a local/loopback address.
func isLocalServer(serverURL string) bool {
	u, err := url.Parse(serverURL)
	if err != nil {
		return false
	}
	h := strings.Split(u.Host, ":")[0] // strip port
	return h == "127.0.0.1" || h == "localhost" || h == "::1" || h == ""
}

// newCLIApp 执行公共初始化：加载配置、创建 Backend。
// If serverURL is non-empty, creates a RemoteBackend (agent runs on server).
// Otherwise creates a LocalBackend (agent runs in-process).
func newCLIApp(serverURL, token string, forceLocal bool) *cliApp {
	cfg := config.Load()

	// Derive cfg.LLM from active subscription (single source of truth)
	syncLLMFromActiveSub(cfg)

	// If --server was not specified on the command line, fall back to config.
	// --local disables this fallback and forces legacy in-process mode.
	if !forceLocal {
		if serverURL == "" && cfg.CLI.ServerURL != "" {
			serverURL = cfg.CLI.ServerURL
		}
		if token == "" && cfg.CLI.Token != "" {
			token = cfg.CLI.Token
		}
	}

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

	// Migrate flat memory from SQLite tables to MD files (if needed)
	storage.MigrateMemoryToFiles(dbPath)

	db, err := sqlite.Open(dbPath)
	if err != nil {
		log.WithError(err).Warn("Failed to open token database, runner tokens disabled")
	} else {
		tools.SetRunnerTokenDB(db.Conn())
	}

	tools.InitSandbox(cfg.Sandbox, workDir)

	var backend agent.AgentBackend
	if serverURL != "" {
		// Remote mode: agent loop runs on the server
		log.WithField("server", serverURL).Info("Using remote backend")
		backend = agent.NewRemoteBackend(agent.RemoteBackendConfig{
			ServerURL: serverURL,
			Token:     token,
		})
	} else {
		// Local mode: agent loop runs in-process
		bc := agent.BackendConfig{
			Cfg:             cfg,
			LLM:             llmClient,
			Bus:             msgBus,
			DBPath:          dbPath,
			WorkDir:         workDir,
			XbotHome:        xbotHome,
			DirectWorkspace: workDir, // CLI: workspace = workDir directly (no per-user subdirectory)
		}
		backend, err = agent.NewLocalBackend(bc.AgentConfig())
		if err != nil {
			log.WithError(err).Fatal("Failed to create local backend")
		}
		backend.RegisterCoreTool(tools.NewWebSearchTool(cfg.TavilyAPIKey))
		backend.IndexGlobalTools()
		backend.LLMFactory().SetModelTiers(cfg.LLM)
		backend.LLMFactory().SetConfigSubs(func() []config.SubscriptionConfig { return cfg.Subscriptions })
		backend.LLMFactory().SetRetryConfig(llm.RetryConfig{
			Attempts: uint(cfg.Agent.LLMRetryAttempts),
			Delay:    cfg.Agent.LLMRetryDelay,
			MaxDelay: cfg.Agent.LLMRetryMaxDelay,
			Timeout:  cfg.Agent.LLMRetryTimeout,
		})
	}

	return &cliApp{
		cfg:       cfg,
		llmClient: llmClient,
		msgBus:    msgBus,
		db:        db,
		backend:   backend,
		workDir:   workDir,
		xbotHome:  xbotHome,
	}
}

// Close 释放资源。
func (app *cliApp) Close() {
	if app.backend != nil {
		app.backend.Stop()
	}
	if app.db != nil {
		app.db.Close()
	}
	log.Close()
}

func main() {
	xbotHome := config.XbotHome()
	defer func() {
		if r := recover(); r != nil {
			appendCLIPanicLog(xbotHome, r)
			panic(r)
		}
	}()
	fmt.Printf("xbot CLI %s\n", version.Version)

	printHelp := func() {
		fmt.Println("Usage: xbot-cli [options] [prompt]")
		fmt.Println()
		fmt.Println("Modes:")
		fmt.Println("  default             Auto mode: use remote server if cli.server_url is configured")
		fmt.Println("  --local             Force legacy local mode (in-process agent, old behavior)")
		fmt.Println("  --server <ws-url>   Force remote mode and connect to server")
		fmt.Println("  serve               Run server mode in the same binary")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  --help, -h          Show this help")
		fmt.Println("  --new               Start a new session")
		fmt.Println("  --resume            Resume last session (default)")
		fmt.Println("  -p <prompt>         Non-interactive single prompt")
		fmt.Println("  --token <token>     Token for remote server")
		fmt.Println("  --workspace <path>  Override workspace")
	}

	// Sub-commands: handled before flag parsing.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "install":
			fmt.Println("install 子命令已不再主推，请使用 scripts/install.sh")
			fmt.Println("例如: curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash")
			return
		case "serve":
			if err := serverapp.Run(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
				os.Exit(1)
			}
			return
		case "--help", "-h", "help":
			printHelp()
			return
		}
	}

	// 解析命令行标志
	prompt := ""
	newSession := false
	var (
		flagServer     string // --server ws://host:port (RemoteBackend: agent runs on server)
		flagShare      string // --share ws://host:port/ws/userID (Runner mode: tools run locally)
		flagToken      string // --token xxx
		flagWorkspace  string // --workspace /path (overrides config)
		flagLocal      bool   // --local force legacy in-process mode
		flagDebug      bool   // --debug enable UI capture + key injection via SIGUSR1
		flagDebugInput string // --debug-input "1,enter,ctrl+c" auto-inject key sequence after startup
		flagDebugCapMs int    // --debug-capture-ms 200  UI capture interval in ms (default 1000)
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
		case "--server":
			if len(os.Args) > i+1 {
				flagServer = os.Args[i+1]
				i++
			}
		case "--local":
			flagLocal = true
		case "--debug":
			flagDebug = true
		case "--debug-input":
			if len(os.Args) > i+1 {
				flagDebugInput = os.Args[i+1]
				i++
				flagDebug = true // auto-enable debug mode
			}
		case "--debug-capture-ms":
			if len(os.Args) > i+1 {
				n, err := strconv.Atoi(os.Args[i+1])
				if err == nil && n >= 50 {
					flagDebugCapMs = n
				}
				i++
			}
		case "--help", "-h":
			printHelp()
			return
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
		fmt.Println("Mode: new session (--new)")
	} else {
		fmt.Println("Mode: resuming last session (use --new for new session)")
	}
	fmt.Println("Starting...")

	if flagLocal {
		flagServer = ""
	}
	app := newCLIApp(flagServer, flagToken, flagLocal)
	if flagLocal {
		fmt.Println("Backend: legacy local mode (--local)")
	} else if app.backend != nil && app.backend.IsRemote() {
		fmt.Println("Backend: remote server mode")
	} else {
		fmt.Println("Backend: local mode")
	}
	defer app.Close()

	disp := channel.NewDispatcher(app.msgBus)

	// 用工作目录绝对路径作为 ChatID，不同目录有不同的会话
	absWorkDir, _ := filepath.Abs(app.workDir)

	_, isRemoteBackend := app.backend.(*agent.RemoteBackend)
	remoteServerURL := ""
	if rb, ok := app.backend.(*agent.RemoteBackend); ok {
		remoteServerURL = rb.ServerURL()
	}
	cliCfg := channel.CLIChannelConfig{
		WorkDir:         app.workDir,
		ChatID:          absWorkDir,
		RemoteMode:      isRemoteBackend,
		RemoteServerURL: remoteServerURL,
		DebugMode:       flagDebug,
		DebugInput:      flagDebugInput,
		DebugCaptureMs:  flagDebugCapMs,
		IsFirstRun:      firstRun,
		GetCurrentValues: func() map[string]string {
			// In remote mode, read current values from server via RPC.
			if app.backend != nil && app.backend.IsRemote() {
				vals := make(map[string]string)
				// Settings from server (contains most config values)
				if sv, err := app.backend.GetSettings("cli", "cli_user"); err == nil {
					for k, v := range sv {
						vals[k] = v
					}
				}
				// Model from server — must be AFTER settings to override stale llm_model in DB
				vals["llm_model"] = app.backend.GetDefaultModel()
				// Context mode from server
				vals["context_mode"] = app.backend.GetContextMode()
				// Defaults for fields not in settings
				if _, ok := vals["sandbox_mode"]; !ok {
					vals["sandbox_mode"] = "none"
				}
				if _, ok := vals["memory_provider"]; !ok {
					vals["memory_provider"] = "flat"
				}
				if _, ok := vals["max_iterations"]; !ok {
					vals["max_iterations"] = "30"
				}
				if _, ok := vals["max_concurrency"]; !ok {
					vals["max_concurrency"] = "3"
				}
				if _, ok := vals["max_context_tokens"]; !ok {
					vals["max_context_tokens"] = "0"
				}
				if _, ok := vals["max_output_tokens"]; !ok {
					vals["max_output_tokens"] = "8192"
				}
				if _, ok := vals["enable_auto_compress"]; !ok {
					vals["enable_auto_compress"] = "true"
				}
				if _, ok := vals["theme"]; !ok {
					vals["theme"] = "midnight"
				}
				return vals
			}
			// Local mode: read from config
			return map[string]string{
				"llm_provider":   app.cfg.LLM.Provider,
				"llm_api_key":    app.cfg.LLM.APIKey,
				"llm_model":      app.cfg.LLM.Model,
				"llm_base_url":   app.cfg.LLM.BaseURL,
				"vanguard_model": app.cfg.LLM.VanguardModel,
				"balance_model":  app.cfg.LLM.BalanceModel,
				"swift_model":    app.cfg.LLM.SwiftModel,
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
					if app.backend != nil {
						if ss := app.backend.SettingsService(); ss != nil {
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
					if app.backend != nil {
						if ss := app.backend.SettingsService(); ss != nil {
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
			if app.backend == nil {
				return
			}

			// ── Remote mode: all settings go to server, skip config.json ──
			if app.backend.IsRemote() {
				// Persist every setting to server via RPC
				for k, v := range values {
					_ = app.backend.SetSetting("cli", "cli_user", k, v)
				}
				// Push runtime state to server
				// enable_auto_compress is a legacy alias for context_mode.
				// Only apply it if context_mode is NOT also being set.
				if v, ok := values["enable_auto_compress"]; ok {
					if _, hasContextMode := values["context_mode"]; !hasContextMode {
						if v == "true" {
							_ = app.backend.SetContextMode("auto")
						} else {
							_ = app.backend.SetContextMode("none")
						}
					}
				}
				if v, ok := values["context_mode"]; ok && v != "" {
					_ = app.backend.SetContextMode(v)
				}
				if v, ok := values["max_iterations"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						app.backend.SetMaxIterations(n)
					}
				}
				if v, ok := values["max_concurrency"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						app.backend.SetMaxConcurrency(n)
					}
				}
				if v, ok := values["max_context_tokens"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n >= 0 {
						app.backend.SetMaxContextTokens(n)
					}
				}
				if v, ok := values["max_output_tokens"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n >= 0 {
						_ = app.backend.SetUserMaxOutputTokens("cli_user", n)
					}
				}
				if v, ok := values["thinking_mode"]; ok {
					_ = app.backend.SetUserThinkingMode("cli_user", v)
				}
				return
			}

			// ── Local mode: write config.json + apply runtime ──
			_, llmChanged := values["llm_provider"]
			_, keyChanged := values["llm_api_key"]
			_, modelChanged := values["llm_model"]
			_, urlChanged := values["llm_base_url"]
			_, vanguardChanged := values["vanguard_model"]
			_, balanceChanged := values["balance_model"]
			_, swiftChanged := values["swift_model"]
			if llmChanged || keyChanged || modelChanged || urlChanged {
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
				syncLLMFromActiveSub(app.cfg)
			}
			if v, ok := values["vanguard_model"]; ok {
				app.cfg.LLM.VanguardModel = strings.TrimSpace(v)
			}
			if v, ok := values["balance_model"]; ok {
				app.cfg.LLM.BalanceModel = strings.TrimSpace(v)
			}
			if v, ok := values["swift_model"]; ok {
				app.cfg.LLM.SwiftModel = strings.TrimSpace(v)
			}
			if app.backend != nil && (vanguardChanged || balanceChanged || swiftChanged) {
				app.backend.LLMFactory().SetModelTiers(app.cfg.LLM)
			}
			if v, ok := values["sandbox_mode"]; ok && v != "" {
				app.cfg.Sandbox.Mode = v
				tools.ReinitSandbox(app.cfg.Sandbox, app.workDir)
				if app.backend != nil {
					app.backend.SetSandbox(tools.GetSandbox(), v)
				}
			}
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
					app.cfg.LLM.MaxOutputTokens = n
					if app.backend != nil {
						if newClient, err := createLLM(app.cfg.LLM, llm.DefaultRetryConfig()); err == nil {
							app.llmClient = newClient
							app.backend.LLMFactory().SetDefaults(newClient, app.cfg.LLM.Model)
							app.backend.LLMFactory().SetModelTiers(app.cfg.LLM)
						} else {
							log.Warnf("Failed to rebuild LLM client: %v", err)
						}
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
				if app.backend != nil {
					app.backend.LLMFactory().SetDefaultThinkingMode(v)
				}
			}
			if v, ok := values["enable_auto_compress"]; ok {
				b := v == "true"
				app.cfg.Agent.EnableAutoCompress = &b
			}
			if err := config.SaveToFile(config.ConfigFilePath(), app.cfg); err != nil {
				log.Warnf("Failed to save config.json: %v", err)
			}
			if theme, ok := values["theme"]; ok && theme != "" && app.backend != nil {
				if ss := app.backend.SettingsService(); ss != nil {
					_ = ss.SetSetting("cli", "cli_user", "theme", theme)
				}
			}
			if llmChanged || keyChanged || modelChanged || urlChanged {
				if app.backend != nil {
					if newClient, err := createLLM(app.cfg.LLM, llm.DefaultRetryConfig()); err == nil {
						app.llmClient = newClient
						app.backend.LLMFactory().SetDefaults(newClient, app.cfg.LLM.Model)
						app.backend.LLMFactory().SetModelTiers(app.cfg.LLM)
					} else {
						log.Warnf("Failed to rebuild LLM client: %v", err)
					}
				}
			}
			if app.backend != nil {
				if v, ok := values["context_mode"]; ok && v != "" {
					_ = app.backend.SetContextMode(v)
				}
				if v, ok := values["max_iterations"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						app.backend.SetMaxIterations(n)
					}
				}
				if v, ok := values["max_concurrency"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n > 0 {
						app.backend.SetMaxConcurrency(n)
					}
				}
				if v, ok := values["max_context_tokens"]; ok {
					if n, err := strconv.Atoi(v); err == nil && n >= 0 {
						app.backend.SetMaxContextTokens(n)
					}
				}
				if v, ok := values["enable_auto_compress"]; ok {
					if v == "true" {
						_ = app.backend.SetContextMode("auto")
					} else {
						_ = app.backend.SetContextMode("none")
					}
				}
			}
		},
		ClearMemory: func(targetType string) error {
			if app.backend == nil {
				return fmt.Errorf("agent not initialized")
			}
			return app.backend.ClearMemory(context.Background(), "cli", absWorkDir, targetType, "cli_user")
		},
		GetMemoryStats: func() map[string]string {
			if app.backend == nil {
				return map[string]string{}
			}
			return app.backend.GetMemoryStats(context.Background(), "cli", absWorkDir, "cli_user")
		},
		SwitchLLM: func(provider, baseURL, apiKey, model string) error {
			llmCfg := config.LLMConfig{
				Provider: provider,
				BaseURL:  baseURL,
				APIKey:   apiKey,
				Model:    model,
			}
			client, err := createLLM(llmCfg, llm.DefaultRetryConfig())
			if err != nil {
				return fmt.Errorf("create LLM: %w", err)
			}
			app.llmClient = client
			if app.backend != nil {
				if factory := app.backend.LLMFactory(); factory != nil {
					factory.SetDefaults(client, model)
					factory.SetModelTiers(app.cfg.LLM)
				}
			}
			return nil
		},
		UsageQuery: func(senderID string, days int) (*sqlite.UserTokenUsage, []sqlite.DailyTokenUsage, error) {
			if app.backend == nil {
				return nil, nil, fmt.Errorf("agent not initialized")
			}
			if app.backend.IsRemote() {
				// Remote mode: get data via RPC and convert from map to struct
				cumMap, err := app.backend.GetUserTokenUsage(senderID)
				if err != nil {
					return nil, nil, err
				}
				var cumulative *sqlite.UserTokenUsage
				if cumMap != nil {
					var u sqlite.UserTokenUsage
					if b, _ := json.Marshal(cumMap); len(b) > 0 {
						_ = json.Unmarshal(b, &u)
					}
					cumulative = &u
				}
				dailyMaps, err := app.backend.GetDailyTokenUsage(senderID, days)
				if err != nil {
					return nil, nil, err
				}
				var daily []sqlite.DailyTokenUsage
				for _, dm := range dailyMaps {
					var d sqlite.DailyTokenUsage
					if b, _ := json.Marshal(dm); len(b) > 0 {
						_ = json.Unmarshal(b, &d)
					}
					daily = append(daily, d)
				}
				return cumulative, daily, nil
			}
			ms := app.backend.MultiSession()
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
			if app.backend == nil {
				return 0
			}
			if app.backend.IsRemote() {
				app.agentCacheMu.RLock()
				defer app.agentCacheMu.RUnlock()
				return app.agentCacheCount
			}
			return app.backend.CountInteractiveSessions("cli", absWorkDir)
		},
		AgentList: func() []channel.AgentPanelEntry {
			if app.backend == nil {
				return nil
			}
			if app.backend.IsRemote() {
				app.agentCacheMu.RLock()
				defer app.agentCacheMu.RUnlock()
				return app.agentCacheList
			}
			sessions := app.backend.ListInteractiveSessions("cli", absWorkDir)
			entries := make([]channel.AgentPanelEntry, len(sessions))
			for i, s := range sessions {
				entries[i] = channel.AgentPanelEntry{
					Role:       s.Role,
					Instance:   s.Instance,
					Running:    s.Running,
					Background: s.Background,
					Task:       s.Task,
					Preview:    s.Preview,
				}
			}
			return entries
		},
		AgentInspect: func(roleName, instance string, tailCount int) (string, error) {
			if app.backend == nil {
				return "", fmt.Errorf("agent not initialized")
			}
			return app.backend.InspectInteractiveSession(context.Background(), roleName, "cli", absWorkDir, instance, tailCount)
		},
		AgentMessages: func(roleName, instance string) []channel.SessionChatMessage {
			if app.backend == nil {
				return nil
			}
			msgs, _ := app.backend.GetSessionMessages("cli", absWorkDir, roleName, instance)
			if msgs == nil {
				return nil
			}
			result := make([]channel.SessionChatMessage, len(msgs))
			for i, m := range msgs {
				result[i] = channel.SessionChatMessage{Role: m.Role, Content: m.Content}
			}
			return result
		},
	}

	// 设置历史消息加载器（会话恢复）
	var cliTenantID int64
	var cliSessionSvc *sqlite.SessionService
	var tenantSvc *sqlite.TenantService
	if !app.backend.IsRemote() && app.db != nil {
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
	// Remote mode: history loaded after backend.Start() via cliCh.LoadHistory()
	// (HistoryLoader runs during NewCLIChannel, before WS is connected)

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
	if app.backend != nil {
		if app.backend.IsRemote() {
			// Remote mode: use RPC-backed adapters
			cliCh.SetSettingsService(newRemoteSettingsService(app.backend))
			cliCh.SetModelLister(newRemoteModelLister(app.backend))
			// Forward user messages to server instead of local bus
			cliCh.SetSendInboundFn(func(msg bus.InboundMessage) bool {
				go func() {
					if err := app.backend.SendInbound(msg); err != nil {
						log.WithError(err).Warn("Failed to forward message to remote server")
						// For /cancel specifically, show a toast so the user knows it failed.
						if strings.TrimSpace(strings.ToLower(msg.Content)) == "/cancel" {
							cliCh.SendToast("Failed to cancel: "+err.Error(), "✗")
						}
					}
				}()
				return true
			})
			// Forward server responses directly to CLI channel (skip dispatcher
			// since there's no local agent loop — dispatcher would not match "remote" channel)
			app.backend.OnOutbound(func(msg bus.OutboundMessage) {
				cliCh.Send(msg)
			})
			// Register OnProgress callback for streaming progress from server
			app.backend.OnProgress(func(p *channel.CLIProgressPayload) {
				cliCh.SendProgress(cliCfg.ChatID, p)
			})
			// Inject TrimHistoryFn for Ctrl+K session truncation (RPC-backed)
			cliCh.SetTrimHistoryFn(func(cutoff time.Time) error {
				return app.backend.TrimHistory("cli", cliCfg.ChatID, cutoff)
			})
			cliCh.SetResetTokenStateFn(func() {
				app.backend.ResetTokenState()
			})
		} else {
			// Local mode: use local service objects directly
			if ss := app.backend.SettingsService(); ss != nil {
				cliCh.SetSettingsService(ss)
			}
			cliCh.SetModelLister(&cliModelLister{
				factory: app.backend.LLMFactory(),
				cfg:     app.cfg,
			})
			// Inject BgTaskManager for background task display
			bgSessionKey := "cli:" + cliCfg.ChatID
			cliCh.SetBgTaskManager(app.backend.BgTaskManager(), bgSessionKey)
			// Inject ApprovalHook for permission control approval dialog
			if hook := app.backend.ToolHookChain().Get("approval"); hook != nil {
				if ah, ok := hook.(*tools.ApprovalHook); ok {
					cliCh.SetApprovalHook(ah)
				}
			}
			// Inject CheckpointHook for Ctrl+K rewind file rollback
			checkpointDir := filepath.Join(os.Getenv("HOME"), ".xbot", "checkpoints", "cli-default")
			if cpStore, err := tools.NewCheckpointStore(checkpointDir); err == nil {
				cpHook := tools.NewCheckpointHook(cpStore)
				if err := app.backend.ToolHookChain().Use(cpHook); err != nil {
					log.WithError(err).Warn("Failed to register checkpoint hook")
				} else {
					cliCh.SetCheckpointHook(cpHook)
					defer cpStore.Cleanup()
				}
			} else {
				log.WithError(err).Warn("Failed to create checkpoint store")
			}
			// Inject TrimHistoryFn for Ctrl+K session truncation
			if cliTenantID != 0 && cliSessionSvc != nil {
				cliCh.SetTrimHistoryFn(func(cutoff time.Time) error {
					if cutoff.IsZero() {
						return nil
					}
					_, err := cliSessionSvc.PurgeNewerThanOrEqual(cliTenantID, cutoff)
					return err
				})
			} else {
				log.WithFields(log.Fields{"tenantID": cliTenantID, "hasSessionSvc": cliSessionSvc != nil, "hasDB": app.db != nil}).Warn("TrimHistoryFn NOT registered — DB truncation will not work")
			}
			// Reset cached token state after rewind to prevent stale compress trigger
			cliCh.SetResetTokenStateFn(func() {
				app.backend.ResetTokenState()
			})
		}
	}

	// Apply saved theme at startup.
	// Local mode can read settings immediately; remote mode must wait until backend.Start()
	// establishes the WS/RPC connection, otherwise theme fetch races and the UI keeps default
	// colors until the user re-saves settings.
	if app.backend != nil && !app.backend.IsRemote() {
		if ss := app.backend.SettingsService(); ss != nil {
			if vals, err := ss.GetSettings("cli", "cli_user"); err == nil {
				if t, ok := vals["theme"]; ok && t != "" {
					channel.ApplyTheme(t)
				}
			}
		}
	}

	// 注入 channelFinder 以启用结构化进度事件（工具调用、思考过程等）
	app.backend.SetDirectSend(disp.SendDirect)
	app.backend.SetChannelFinder(disp.GetChannel)
	if lb, ok := app.backend.(*agent.LocalBackend); ok {
		lb.Agent().SetMessageSender(disp)
		lb.Agent().SetAgentChannelRegistry(
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
	}

	// 注入 CLI 渠道特化 prompt 提供者
	app.backend.SetChannelPromptProviders(&channel.CliPromptProvider{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Remote mode: connect to server with retry loop before starting TUI.
	// Shows progress to the user instead of silently failing.
	if app.backend.IsRemote() {
		fmt.Fprintf(os.Stderr, "\n  Connecting to remote server %s ...\n", app.cfg.CLI.ServerURL)
		const maxRetries = 5
		var connectErr error
		for attempt := 0; attempt < maxRetries; attempt++ {
			connectErr = app.backend.Start(ctx)
			if connectErr == nil {
				fmt.Fprintln(os.Stderr, "  Connected.")
				break
			}
			delay := time.Duration(1<<uint(attempt)) * time.Second
			if attempt < maxRetries-1 {
				fmt.Fprintf(os.Stderr, "  Connection failed: %v\n  Retrying in %vs (%d/%d)...\n", connectErr, delay, attempt+1, maxRetries)
				select {
				case <-ctx.Done():
					fmt.Fprintln(os.Stderr, "\n  Cancelled.")
					app.Close()
					return
				case <-time.After(delay):
				}
			}
		}
		if connectErr != nil {
			fmt.Fprintf(os.Stderr, "\n  %s\n  Could not connect to server after %d attempts. Please check:\n    1. Server is running (xbot-cli serve)\n    2. Port matches in config (%s)\n    3. Token is correct\n  %s\n\n",
				red("ERROR: "+connectErr.Error()),
				maxRetries,
				config.ConfigFilePath(),
				red("Exiting."))
			app.Close()
			return
		}
	} else {
		if err := app.backend.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start backend: %v\n", err)
			app.Close()
			return
		}
	}
	go disp.Run()

	// Remote mode: load history from server after WS connection is established.
	// Use the original CLI tenant key so remote mode can resume the same session
	// as legacy local mode: channel=cli, chat_id=absWorkDir.
	if app.backend.IsRemote() {
		if vals, err := app.backend.GetSettings("cli", "cli_user"); err == nil {
			if t, ok := vals["theme"]; ok && t != "" {
				channel.ApplyTheme(t)
			}
		}
		remoteChatID, _ := filepath.Abs(app.workDir)

		// Auto-set CWD: if connected to a local server (127.0.0.1/localhost),
		// sync the CLI's actual cwd to the server session so the agent uses
		// the correct directory regardless of where the server was started.
		if isLocalServer(app.cfg.CLI.ServerURL) {
			if cwd, err := os.Getwd(); err == nil {
				if err := app.backend.SetCWD("cli", remoteChatID, cwd); err != nil {
					log.WithError(err).Warn("Failed to sync CWD to server")
				} else {
					log.WithField("cwd", cwd).Info("Synced CLI CWD to local server")
				}
			}
		}

		if history, err := app.backend.GetHistory("cli", remoteChatID); err != nil {
			log.WithError(err).WithField("chat_id", remoteChatID).Warn("Failed to load remote session history")
		} else {
			log.WithFields(log.Fields{"chat_id": remoteChatID, "count": len(history)}).Info("CLI loaded remote history via RPC")
			if len(history) > 0 {
				cliCh.LoadHistory(history)
			}
		}
		// Check if server has an active agent turn for this chat (mid-session reconnect).
		// Run in goroutine to avoid blocking TUI startup on RPC timeout.
		go func() {
			if progress := app.backend.GetActiveProgress("cli", remoteChatID); progress != nil {
				cliCh.SendProgress(cliCfg.ChatID, progress)
				cliCh.SetProcessing(true)
			}
		}()

		// Wire reconnect callback to reload history on WS reconnect.
		if rb, ok := app.backend.(interface{ OnReconnect(func()) }); ok {
			rb.OnReconnect(func() {
				// Re-sync CWD on reconnect (server may have restarted, losing in-memory cwd)
				if isLocalServer(app.cfg.CLI.ServerURL) {
					if cwd, err := os.Getwd(); err == nil {
						_ = app.backend.SetCWD("cli", remoteChatID, cwd)
					}
				}
				if history, err := app.backend.GetHistory("cli", remoteChatID); err != nil {
					log.WithError(err).Warn("Failed to reload history after reconnect")
				} else {
					cliCh.LoadHistory(history)
				}
				// Re-check processing state after reconnect.
				if app.backend.IsProcessing("cli", remoteChatID) {
					cliCh.SetProcessing(true)
				} else {
					cliCh.SetProcessing(false)
				}
			})
		}
		// Wire connection state change callback for header bar indicator.
		if csc, ok := app.backend.(interface{ OnConnStateChange(func(string)) }); ok {
			csc.OnConnStateChange(func(state string) {
				cliCh.SetConnState(state)
			})
		}
		// Background goroutine: periodically refresh agent count/list cache
		// (RPC calls must not happen from BubbleTea event loop → deadlock)
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if app.backend == nil {
						return
					}
					count := app.backend.CountInteractiveSessions("web", "")
					sessions := app.backend.ListInteractiveSessions("web", "")
					entries := make([]channel.AgentPanelEntry, len(sessions))
					for i, s := range sessions {
						entries[i] = channel.AgentPanelEntry{
							Role:       s.Role,
							Instance:   s.Instance,
							Running:    s.Running,
							Background: s.Background,
							Task:       s.Task,
							Preview:    s.Preview,
						}
					}
					app.agentCacheMu.Lock()
					app.agentCacheCount = count
					app.agentCacheList = entries
					app.agentCacheMu.Unlock()
				}
			}
		}()
	}

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
		log.Info("Received shutdown signal, shutting down...")
		// Stop backend first (closes WS, unblocks pending RPCs)
		if app.backend != nil {
			app.backend.Stop()
		}
		// Wait for pending saves with timeout (avoid blocking forever on hung RPC)
		done := make(chan struct{})
		go func() {
			saveWg.Wait()
			close(done)
		}()
		select {
		case <-done:
			log.Info("All saves complete")
		case <-time.After(2 * time.Second):
			log.Warn("Timeout waiting for pending saves, forcing shutdown")
		}
		cancel()
		// Quit BubbleTea program so cliCh.Start() returns
		cliCh.Stop()
	}()

	// Runner Bridge: inject LLM client, model list and provider for runner use
	if !app.backend.IsRemote() {
		cliCh.SetRunnerLLM(app.llmClient, func() []string {
			if app.backend != nil {
				return app.backend.LLMFactory().ListModels()
			}
			return nil
		}(), app.cfg.LLM.Provider)
	}

	// Multi-subscription support
	if app.backend.IsRemote() {
		// Remote mode: use RPC-backed subscription manager
		cliCh.SetSubscriptionManager(newRemoteSubscriptionManager(app.backend))
		cliCh.SetLLMSubscriber(newRemoteLLMSubscriber(app.backend))
	} else {
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
		cliCh.SetSubscriptionManager(newConfigSubscriptionManager(app.cfg, saveConfig, func(llmCfg config.LLMConfig) {
			if app.backend != nil {
				app.backend.LLMFactory().SetModelTiers(llmCfg)
			}
		}))
		cliCh.SetLLMSubscriber(newConfigLLMSubscriber(app.cfg, app.backend.LLMFactory(), saveConfig))
	}

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

// cliModelLister wraps LLMFactory + config to implement channel.ModelLister.
// ListAllModels collects models from default LLM + all config subscriptions.
type cliModelLister struct {
	factory *agent.LLMFactory
	cfg     *config.Config
}

func (l *cliModelLister) ListModels() []string {
	return l.factory.ListModels()
}

func (l *cliModelLister) ListAllModels() []string {
	seen := make(map[string]bool)
	var result []string
	for _, m := range l.factory.ListModels() {
		if !seen[m] {
			seen[m] = true
			result = append(result, m)
		}
	}
	for _, sub := range l.cfg.Subscriptions {
		if sub.Model != "" && !seen[sub.Model] {
			seen[sub.Model] = true
			result = append(result, sub.Model)
		}
	}
	return result
}

// configSubscriptionManager manages CLI subscriptions in config.json (no database).
type configSubscriptionManager struct {
	cfg      *config.Config
	saveFn   func() error           // persists config to disk
	tierSync func(config.LLMConfig) // called after subscription switch to re-sync tier models
}

func newConfigSubscriptionManager(cfg *config.Config, saveFn func() error, tierSync func(config.LLMConfig)) *configSubscriptionManager {
	return &configSubscriptionManager{cfg: cfg, saveFn: saveFn, tierSync: tierSync}
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
	// Re-sync model tiers (tier fields are global, not per-subscription)
	if m.tierSync != nil {
		m.tierSync(m.cfg.LLM)
	}
	return m.saveFn()
}

func (m *configSubscriptionManager) SetModel(id, model string) error {
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Model = model
			// If modifying active subscription, sync cfg.LLM
			if m.cfg.Subscriptions[i].Active {
				syncLLMFromActiveSub(m.cfg)
				if m.tierSync != nil {
					m.tierSync(m.cfg.LLM)
				}
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

func (m *configSubscriptionManager) Update(id string, sub *channel.Subscription) error {
	for i := range m.cfg.Subscriptions {
		if m.cfg.Subscriptions[i].ID == id {
			m.cfg.Subscriptions[i].Name = sub.Name
			m.cfg.Subscriptions[i].Provider = sub.Provider
			m.cfg.Subscriptions[i].BaseURL = sub.BaseURL
			m.cfg.Subscriptions[i].APIKey = sub.APIKey
			m.cfg.Subscriptions[i].Model = sub.Model
			// If modifying active subscription, sync cfg.LLM
			if m.cfg.Subscriptions[i].Active {
				syncLLMFromActiveSub(m.cfg)
				if m.tierSync != nil {
					m.tierSync(m.cfg.LLM)
				}
			}
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
// It only writes the 6 subscription-derived fields; tier fields (VanguardModel/BalanceModel/SwiftModel)
// are global and NOT touched here.
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
			client, err := createLLM(llmCfg, llm.DefaultRetryConfig())
			if err != nil {
				return fmt.Errorf("create LLM for subscription: %w", err)
			}
			s.factory.SetDefaults(client, sc.Model)
			s.factory.SetDefaultThinkingMode(sc.ThinkingMode)
			s.factory.SetModelTiers(s.cfg.LLM)
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
	s.factory.SetModelTiers(s.cfg.LLM)
	if err := s.saveFn(); err != nil {
		log.WithError(err).Warn("Failed to persist model switch")
	}
}

func (s *configLLMSubscriber) GetDefaultModel() string {
	return s.factory.GetDefaultModel()
}

// red wraps text in ANSI red for terminal error output.
func red(s string) string {
	return "\033[0;31m" + s + "\033[0m"
}

// executeNonInteractive 非交互模式：单次执行 prompt 并输出到 stdout。
func executeNonInteractive(prompt string) {
	app := newCLIApp("", "", true) // non-interactive always uses local backend
	defer app.Close()

	absWorkDir, _ := filepath.Abs(app.workDir)

	nonIntCh := channel.NewNonInteractiveChannel(app.msgBus)
	disp := channel.NewDispatcher(app.msgBus)
	disp.Register(nonIntCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = app.backend.Start(ctx)
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
			OnModelsLoadError: func(err error) {
				select {
				case channel.ModelsLoadErrorCh() <- err:
				default:
				}
			},
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

// ---------------------------------------------------------------------------
// Remote backend adapters — implement CLI interfaces via RPC
// ---------------------------------------------------------------------------

// remoteSettingsService implements channel.SettingsService via RPC.
type remoteSettingsService struct {
	backend agent.AgentBackend
}

func newRemoteSettingsService(backend agent.AgentBackend) *remoteSettingsService {
	return &remoteSettingsService{backend: backend}
}

func (s *remoteSettingsService) GetSettings(namespace, senderID string) (map[string]string, error) {
	return s.backend.GetSettings(namespace, senderID)
}

func (s *remoteSettingsService) SetSetting(namespace, senderID, key, value string) error {
	return s.backend.SetSetting(namespace, senderID, key, value)
}

// remoteModelLister implements channel.ModelLister via RPC.
type remoteModelLister struct {
	backend agent.AgentBackend
}

func newRemoteModelLister(backend agent.AgentBackend) *remoteModelLister {
	return &remoteModelLister{backend: backend}
}

func (l *remoteModelLister) ListModels() []string {
	return l.backend.ListModels()
}

func (l *remoteModelLister) ListAllModels() []string {
	return l.backend.ListAllModels()
}

// remoteSubscriptionManager implements channel.SubscriptionManager via RPC.
type remoteSubscriptionManager struct {
	backend agent.AgentBackend
}

func newRemoteSubscriptionManager(backend agent.AgentBackend) *remoteSubscriptionManager {
	return &remoteSubscriptionManager{backend: backend}
}

func (m *remoteSubscriptionManager) List(senderID string) ([]channel.Subscription, error) {
	return m.backend.ListSubscriptions(senderID)
}

func (m *remoteSubscriptionManager) GetDefault(senderID string) (*channel.Subscription, error) {
	return m.backend.GetDefaultSubscription(senderID)
}

func (m *remoteSubscriptionManager) Add(sub *channel.Subscription) error {
	return m.backend.AddSubscription("cli_user", *sub)
}

func (m *remoteSubscriptionManager) Remove(id string) error {
	return m.backend.RemoveSubscription(id)
}

func (m *remoteSubscriptionManager) SetDefault(id string) error {
	return m.backend.SetDefaultSubscription(id)
}

func (m *remoteSubscriptionManager) SetModel(id, model string) error {
	return m.backend.SetSubscriptionModel(id, model)
}

func (m *remoteSubscriptionManager) Rename(id, name string) error {
	return m.backend.RenameSubscription(id, name)
}

func (m *remoteSubscriptionManager) Update(id string, sub *channel.Subscription) error {
	return m.backend.UpdateSubscription(id, *sub)
}

// remoteLLMSubscriber implements channel.LLMSubscriber via RPC.
type remoteLLMSubscriber struct {
	backend agent.AgentBackend
}

func newRemoteLLMSubscriber(backend agent.AgentBackend) *remoteLLMSubscriber {
	return &remoteLLMSubscriber{backend: backend}
}

func (s *remoteLLMSubscriber) SwitchSubscription(senderID string, sub *channel.Subscription) error {
	if sub == nil {
		return nil
	}
	// Server-side set_default_subscription invalidates the LLM cache so
	// the next GetLLM call picks up the new subscription's provider/model/credentials.
	// Do NOT call SetUserModel here — it would create a conflicting LLMConfig
	// that overrides the subscription's model.
	return s.backend.SetDefaultSubscription(sub.ID)
}

func (s *remoteLLMSubscriber) SwitchModel(senderID, model string) {
	if err := s.backend.SwitchModel(senderID, model); err != nil {
		log.WithError(err).Warn("remoteLLMSubscriber: SwitchModel RPC failed")
	}
}

func (s *remoteLLMSubscriber) GetDefaultModel() string {
	return s.backend.GetDefaultModel()
}

func appendCLIPanicLog(xbotHome string, recovered any) {
	logDir := filepath.Join(xbotHome, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return
	}
	path := filepath.Join(logDir, "cli-panic.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "\n==== %s panic ====\n%v\n%s\n", time.Now().Format(time.RFC3339), recovered, debug.Stack())
}
