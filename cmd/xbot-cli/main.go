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
	"os"
	"os/signal"
	"path/filepath"
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
		MemoryWindow:         cfg.Agent.MemoryWindow,
		DBPath:               dbPath,
		SkillsDir:            filepath.Join(xbotHome, "skills"),
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
		WorkDir: app.workDir,
		ChatID:  absWorkDir,
	}

	// 设置历史消息加载器（会话恢复）
	if app.db != nil {
		tenantSvc := sqlite.NewTenantService(app.db)
		sessionSvc := sqlite.NewSessionService(app.db)
		tenantID, err := tenantSvc.GetOrCreateTenantID("cli", absWorkDir)
		if err == nil {
			cliCfg.HistoryLoader = func() ([]channel.HistoryMessage, error) {
				msgs, err := sessionSvc.GetAllMessages(tenantID)
				if err != nil {
					return nil, err
				}
				var history []channel.HistoryMessage
				for _, m := range msgs {
					switch m.Role {
					case "tool":
						continue
					case "assistant":
						if m.Detail != "" {
							var snaps []agent.IterationSnapshot
							if jsonErr := json.Unmarshal([]byte(m.Detail), &snaps); jsonErr == nil {
								iters := make([]channel.HistoryIteration, 0, len(snaps))
								for _, snap := range snaps {
									toolList := make([]channel.CLIToolProgress, len(snap.Tools))
									for i, t := range snap.Tools {
										label := t.Label
										if label == "" {
											label = t.Name
										}
										toolList[i] = channel.CLIToolProgress{
											Name:    t.Name,
											Label:   label,
											Status:  t.Status,
											Elapsed: t.ElapsedMS,
										}
									}
									iters = append(iters, channel.HistoryIteration{
										Iteration: snap.Iteration,
										Thinking:  snap.Thinking,
										Tools:     toolList,
									})
								}
								if len(iters) > 0 {
									history = append(history, channel.HistoryMessage{
										Role:       "tool_summary",
										Timestamp:  m.Timestamp,
										Iterations: iters,
									})
								}
							}
							if m.Content != "" {
								history = append(history, channel.HistoryMessage{
									Role:      "assistant",
									Content:   m.Content,
									Timestamp: m.Timestamp,
								})
							}
						} else if len(m.ToolCalls) > 0 {
							continue
						} else if m.Content != "" {
							history = append(history, channel.HistoryMessage{
								Role:      "assistant",
								Content:   m.Content,
								Timestamp: m.Timestamp,
							})
						}
					default:
						if m.Content != "" {
							history = append(history, channel.HistoryMessage{
								Role:      m.Role,
								Content:   m.Content,
								Timestamp: m.Timestamp,
							})
						}
					}
				}
				return history, nil
			}
		}
	}

	cliCh := channel.NewCLIChannel(cliCfg, app.msgBus)
	disp.Register(cliCh)

	// 注入 channelFinder 以启用结构化进度事件（工具调用、思考过程等）
	app.agentLoop.SetDirectSend(disp.SendDirect)
	app.agentLoop.SetChannelFinder(disp.GetChannel)

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

	if err := cliCh.Start(); err != nil {
		log.WithError(err).Fatal("CLI channel error")
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
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		})
	default:
		return nil, fmt.Errorf("unsupported LLM provider: %s", cfg.Provider)
	}
	return llm.NewRetryLLM(inner, retryCfg), nil
}
