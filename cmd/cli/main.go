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

func main() {
	// 打印版本信息
	fmt.Printf("xbot CLI %s\n", version.Version)

	// 检测非交互模式
	prompt := ""
	// 解析命令行标志
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

	// 如果是非交互模式，执行单次 prompt 并退出
	if prompt != "" {
		executeNonInteractive(prompt)
		return
	}

	// 显示会话模式
	if newSession {
		fmt.Println("模式: 新会话 (--new)")
	} else {
		fmt.Println("模式: 恢复上次会话 (使用 --new 开始新会话)")
	}

	fmt.Println("Starting...")

	// 加载配置
	cfg := config.Load()

	// 配置日志
	workDir := cfg.Agent.WorkDir
	if err := setupLogger(cfg.Log, workDir); err != nil {
		log.WithError(err).Fatal("Failed to setup logger")
	}
	defer log.Close()

	// 创建 LLM 客户端
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

	// 创建消息总线
	msgBus := bus.NewMessageBus()

	// 准备数据库路径
	xbotDir := filepath.Join(workDir, ".xbot")
	dbPath := filepath.Join(xbotDir, "xbot.db")

	// 数据迁移（如果需要）
	if err := storage.MigrateIfNeeded(context.Background(), workDir, dbPath); err != nil {
		log.WithError(err).Fatal("Failed to migrate data to SQLite")
	}

	// 设置 runner token 数据库
	db, err := sqlite.Open(dbPath)
	if err != nil {
		log.WithError(err).Warn("Failed to open token database, runner tokens disabled")
	} else {
		tools.SetRunnerTokenDB(db.Conn())
	}

	// 嵌入向量配置
	embBaseURL := cfg.Embedding.BaseURL
	if embBaseURL == "" {
		embBaseURL = cfg.LLM.BaseURL
	}
	embAPIKey := cfg.Embedding.APIKey
	if embAPIKey == "" {
		embAPIKey = cfg.LLM.APIKey
	}

	// 初始化沙箱
	tools.InitSandbox(cfg.Sandbox, workDir)

	// 创建 Agent
	agentLoop := agent.New(agent.Config{
		Bus:                  msgBus,
		LLM:                  llmClient,
		Model:                cfg.LLM.Model,
		MaxIterations:        cfg.Agent.MaxIterations,
		MaxConcurrency:       cfg.Agent.MaxConcurrency,
		MemoryWindow:         cfg.Agent.MemoryWindow,
		DBPath:               dbPath,
		SkillsDir:            filepath.Join(xbotDir, "skills"),
		WorkDir:              workDir,
		PromptFile:           cfg.Agent.PromptFile,
		SingleUser:           true, // CLI 模式强制单用户
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
		EnableAutoCompress:   cfg.Agent.EnableAutoCompress,
		MaxContextTokens:     cfg.Agent.MaxContextTokens,
		CompressionThreshold: cfg.Agent.CompressionThreshold,
		ContextMode:          agent.ContextMode(cfg.Agent.ContextMode),
		MaxSubAgentDepth:     cfg.Agent.MaxSubAgentDepth,
	})

	// 索引全局工具
	agentLoop.IndexGlobalTools()

	// 创建消息分发器
	disp := channel.NewDispatcher(msgBus)

	// 创建并注册 CLI 渠道
	cliCfg := channel.CLIChannelConfig{
		WorkDir: workDir,
	}
	// 设置历史消息加载器（会话恢复）
	// 过滤中间迭代消息（含 tool_calls 的 assistant + tool），
	// 只保留 user 消息 + tool_summary（从 Detail 重建）+ 最终 assistant 回复
	if db != nil {
		tenantSvc := sqlite.NewTenantService(db)
		sessionSvc := sqlite.NewSessionService(db)
		tenantID, err := tenantSvc.GetOrCreateTenantID("cli", "cli_user")
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
						// 跳过 LLM 内部的 tool 消息
						continue
					case "assistant":
						if m.Detail != "" {
							// 最终 assistant 消息：Detail 包含 IterationHistory JSON
							var snaps []agent.IterationSnapshot
							if jsonErr := json.Unmarshal([]byte(m.Detail), &snaps); jsonErr == nil {
								// 按迭代分组生成 tool_summary
								iters := make([]channel.HistoryIteration, 0, len(snaps))
								for _, snap := range snaps {
									tools := make([]channel.CLIToolProgress, len(snap.Tools))
									for i, t := range snap.Tools {
										label := t.Label
										if label == "" {
											label = t.Name
										}
										tools[i] = channel.CLIToolProgress{
											Name:    t.Name,
											Label:   label,
											Status:  t.Status,
											Elapsed: t.ElapsedMS,
										}
									}
									iters = append(iters, channel.HistoryIteration{
										Iteration: snap.Iteration,
										Thinking:  snap.Thinking,
										Tools:     tools,
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
							// 最终 assistant 回复单独一条消息
							if m.Content != "" {
								history = append(history, channel.HistoryMessage{
									Role:      "assistant",
									Content:   m.Content,
									Timestamp: m.Timestamp,
								})
							}
						} else if len(m.ToolCalls) > 0 {
							// 中间迭代 assistant 消息（有 tool_calls 但无 Detail），跳过
							continue
						} else if m.Content != "" {
							// 无 Detail 也无 ToolCalls 的 assistant（防御性处理）
							history = append(history, channel.HistoryMessage{
								Role:      "assistant",
								Content:   m.Content,
								Timestamp: m.Timestamp,
							})
						}
					default:
						// user 消息等
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
	cliCh := channel.NewCLIChannel(cliCfg, msgBus)
	disp.Register(cliCh)

	// 注入 channelFinder 以启用结构化进度事件（工具调用、思考过程等）
	agentLoop.SetDirectSend(disp.SendDirect)
	agentLoop.SetChannelFinder(disp.GetChannel)

	// 启动 Agent（需要 context）
	ctx, cancel := context.WithCancel(context.Background())
	go agentLoop.Run(ctx)

	// 启动分发器（处理 outbound 消息）
	go disp.Run()

	// §7 会话恢复：根据参数发送初始化命令
	cliMeta := map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional}
	if newSession {
		// --new 模式：发送 /new 清除上次会话
		msgBus.Inbound <- bus.InboundMessage{
			Channel:    "cli",
			SenderID:   "cli_user",
			ChatID:     "cli_user",
			ChatType:   "p2p",
			Content:    "/new",
			SenderName: "CLI User",
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
			Metadata:   cliMeta,
		}
	}

	// 处理信号 - 优雅退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// CLI 退出时清理
	go func() {
		sig := <-sigCh
		fmt.Printf("\n收到信号 %v，正在关闭...\n", sig)

		// 1. 停止 CLI 渠道（触发 Bubble Tea 退出）
		cliCh.Stop()

		// 2. 取消 Agent context
		cancel()

		// 3. 停止分发器
		disp.Stop()

		// 4. 关闭数据库
		if db != nil {
			db.Close()
		}

		log.Info("CLI shutdown complete")
	}()

	// 启动 CLI（阻塞）
	if err := cliCh.Start(); err != nil {
		log.WithError(err).Fatal("CLI channel error")
	}
}

// setupLogger 配置日志（CLI 模式：仅文件输出，不干扰终端 TUI）
func setupLogger(cfg config.LogConfig, workDir string) error {
	return log.Setup(log.SetupConfig{
		Level:    cfg.Level,
		Format:   cfg.Format,
		WorkDir:  workDir,
		MaxAge:   7,
		FileOnly: true,
	})
}

// createLLM 根据配置创建 LLM 客户端（带重试、指数退避和随机抖动）
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
		return nil, fmt.Errorf("unknown LLM provider: %s", cfg.Provider)
	}

	return llm.NewRetryLLM(inner, retryCfg), nil
}

// executeNonInteractive 非交互模式：单次执行 prompt 并输出到 stdout
func executeNonInteractive(prompt string) {
	// 加载配置
	cfg := config.Load()

	// 配置日志
	workDir := cfg.Agent.WorkDir
	if err := setupLogger(cfg.Log, workDir); err != nil {
		log.WithError(err).Fatal("Failed to setup logger")
	}
	defer log.Close()

	// 创建 LLM 客户端
	llmClient, err := createLLM(cfg.LLM, llm.RetryConfig{
		Attempts: uint(cfg.Agent.LLMRetryAttempts),
		Delay:    cfg.Agent.LLMRetryDelay,
		MaxDelay: cfg.Agent.LLMRetryMaxDelay,
		Timeout:  cfg.Agent.LLMRetryTimeout,
	})
	if err != nil {
		log.WithError(err).Fatal("Failed to create LLM client")
	}

	// 创建消息总线
	msgBus := bus.NewMessageBus()

	// 数据库
	xbotDir := filepath.Join(workDir, ".xbot")
	dbPath := filepath.Join(xbotDir, "xbot.db")
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

	// 创建 Agent
	agentLoop := agent.New(agent.Config{
		Bus:                  msgBus,
		LLM:                  llmClient,
		Model:                cfg.LLM.Model,
		MaxIterations:        cfg.Agent.MaxIterations,
		MaxConcurrency:       cfg.Agent.MaxConcurrency,
		MemoryWindow:         cfg.Agent.MemoryWindow,
		DBPath:               dbPath,
		SkillsDir:            filepath.Join(xbotDir, "skills"),
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
		EnableAutoCompress:   cfg.Agent.EnableAutoCompress,
		MaxContextTokens:     cfg.Agent.MaxContextTokens,
		CompressionThreshold: cfg.Agent.CompressionThreshold,
		ContextMode:          agent.ContextMode(cfg.Agent.ContextMode),
		MaxSubAgentDepth:     cfg.Agent.MaxSubAgentDepth,
	})
	agentLoop.IndexGlobalTools()

	// 创建 NonInteractiveChannel 并注册
	nonIntCh := channel.NewNonInteractiveChannel(msgBus)
	disp := channel.NewDispatcher(msgBus)
	disp.Register(nonIntCh)

	// 启动 Agent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go agentLoop.Run(ctx)
	go disp.Run()

	// 发送 prompt
	msgBus.Inbound <- bus.InboundMessage{
		Channel:    "cli",
		SenderID:   "cli_user",
		ChatID:     "cli_user",
		ChatType:   "p2p",
		Content:    prompt,
		SenderName: "CLI User",
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
	}

	// 等待回复完成
	nonIntCh.WaitDone()

	if db != nil {
		db.Close()
	}
}
