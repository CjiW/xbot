package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/llm"
	log "xbot/logger"
	"xbot/storage"
)

func main() {
	cfg := config.Load()

	// 配置日志
	setupLogger(cfg.Log)

	// 创建 LLM 客户端
	llmClient, err := createLLM(cfg.LLM)
	if err != nil {
		log.WithError(err).Fatal("Failed to create LLM client")
	}
	log.WithFields(log.Fields{
		"provider": cfg.LLM.Provider,
		"model":    cfg.LLM.Model,
	}).Info("LLM client created")

	// 创建消息总线
	msgBus := bus.NewMessageBus()

	// 创建 Agent
	workDir := cfg.Agent.WorkDir
	xbotDir := filepath.Join(workDir, ".xbot")
	dbPath := filepath.Join(xbotDir, "xbot.db")

	// 检测并执行数据迁移（如果需要）
	if err := storage.MigrateIfNeeded(context.Background(), workDir, dbPath); err != nil {
		log.WithError(err).Fatal("Failed to migrate data to SQLite")
	}

	agentLoop := agent.New(agent.Config{
		Bus:                  msgBus,
		LLM:                  llmClient,
		Model:                cfg.LLM.Model,
		MaxIterations:        cfg.Agent.MaxIterations,
		MemoryWindow:         cfg.Agent.MemoryWindow,
		DBPath:               dbPath,
		SkillsDir:            filepath.Join(xbotDir, "skills"),
		WorkDir:              workDir,
		PromptFile:           cfg.Agent.PromptFile,
		MCPInactivityTimeout: cfg.Agent.MCPInactivityTimeout,
		MCPCleanupInterval:   cfg.Agent.MCPCleanupInterval,
		SessionCacheTimeout:  cfg.Agent.SessionCacheTimeout,
	})

	// 创建消息分发器
	disp := channel.NewDispatcher(msgBus)

	// 注册飞书渠道
	if cfg.Feishu.Enabled {
		feishuCh := channel.NewFeishuChannel(channel.FeishuConfig{
			AppID:             cfg.Feishu.AppID,
			AppSecret:         cfg.Feishu.AppSecret,
			EncryptKey:        cfg.Feishu.EncryptKey,
			VerificationToken: cfg.Feishu.VerificationToken,
			AllowFrom:         cfg.Feishu.AllowFrom,
		}, msgBus)
		disp.Register(feishuCh)
	}

	// 注入同步发送函数，使 Agent 可直接通过 Dispatcher 发送消息并获取 message_id
	agentLoop.SetDirectSend(disp.SendDirect)

	channels := disp.EnabledChannels()
	if len(channels) == 0 {
		log.Warn("No channels enabled. Set FEISHU_ENABLED=true and configure FEISHU_APP_ID/FEISHU_APP_SECRET.")
		log.Info("Starting in agent-only mode (no IM channels)")
	} else {
		log.WithField("channels", channels).Info("Channels enabled")
	}

	// 设置优雅退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 启动出站消息分发
	go disp.Run()

	// 启动所有渠道
	for name, ch := range getChannels(disp) {
		go func(n string, c channel.Channel) {
			log.WithField("channel", n).Info("Starting channel...")
			if err := c.Start(); err != nil {
				log.WithError(err).WithField("channel", n).Error("Channel failed")
			}
		}(name, ch)
	}

	// 启动 Agent 循环
	go func() {
		if err := agentLoop.Run(ctx); err != nil && ctx.Err() == nil {
			log.WithError(err).Error("Agent loop exited with error")
		}
	}()

	// 启动飞书 UAT 自动刷新
	tokenFilePath := filepath.Join(xbotDir, "feishu_tokens.json")
	uat, rt := os.Getenv("FEISHU_UAT"), os.Getenv("FEISHU_REFRESH_TOKEN")
	if fileUAT, fileRT, err := channel.LoadFeishuTokens(tokenFilePath); err != nil {
		log.WithError(err).Warn("Failed to load feishu_tokens.json, using .env values")
	} else if fileUAT != "" && fileRT != "" {
		uat, rt = fileUAT, fileRT
		log.Info("Loaded Feishu tokens from feishu_tokens.json")
	}
	if uat != "" && rt != "" {
		tokenRefresher := channel.NewFeishuTokenRefresher(channel.FeishuTokenConfig{
			AppID:         cfg.Feishu.AppID,
			AppSecret:     cfg.Feishu.AppSecret,
			UAT:           uat,
			RefreshToken:  rt,
			MCPConfigPath: filepath.Join(cfg.Agent.WorkDir, "mcp.json"),
			TokenFilePath: tokenFilePath,
		})
		// TODO: Update token refresher callback to work with per-session MCP managers
		// The old approach of reconnecting a single MCP manager no longer works.
		// Need to implement session-aware MCP reconnection or invalidate sessions to trigger lazy reload.
		tokenRefresher.SetOnRefresh(func(newUAT string) {
			log.Info("Feishu UAT refreshed, sessions will reconnect MCP on next use")
		})
		go tokenRefresher.Start(ctx)
		log.Info("Feishu UAT auto-refresh enabled (every 1h)")
	}

	log.Info("xbot started successfully")
	fmt.Println("🤖 xbot is running. Press Ctrl+C to stop.")

	// 等待退出信号
	<-sigCh
	fmt.Println("\nShutting down...")
	cancel()
	disp.Stop()
	log.Info("xbot stopped")
}

// createLLM 根据配置创建 LLM 客户端
func createLLM(cfg config.LLMConfig) (llm.LLM, error) {
	switch cfg.Provider {
	case "openai":
		return llm.NewOpenAILLM(llm.OpenAIConfig{
			BaseURL:      cfg.BaseURL,
			APIKey:       cfg.APIKey,
			DefaultModel: cfg.Model,
		}), nil
	case "codebuddy":
		return llm.NewCodeBuddyLLM(llm.CodeBuddyConfig{
			BaseURL:      cfg.BaseURL,
			Token:        cfg.APIKey,
			UserID:       cfg.UserID,
			EnterpriseID: cfg.EnterpriseID,
			Domain:       cfg.Domain,
			DefaultModel: cfg.Model,
		}), nil
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", cfg.Provider)
	}
}

// setupLogger 配置日志
func setupLogger(cfg config.LogConfig) {
	switch cfg.Format {
	case "json":
		log.SetFormatter(&log.JSONFormatter{})
	default:
		log.SetFormatter(&log.TextFormatter{
			FullTimestamp: true,
		})
	}

	level, err := log.ParseLevel(cfg.Level)
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)
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
