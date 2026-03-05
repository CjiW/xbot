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
	"xbot/oauth"
	"xbot/oauth/providers"
	"xbot/storage"
	"xbot/tools"
	"xbot/tools/feishu_mcp"
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

	// OAuth 管理
	var oauthServer *oauth.Server
	var oauthManager *oauth.Manager
	var feishuProvider *providers.FeishuProvider
	if cfg.OAuth.Enable {
		// 创建 OAuth token 存储
		oauthDBPath := filepath.Join(xbotDir, "oauth_tokens.db")
		tokenStorage, err := oauth.NewSQLiteStorage(oauthDBPath)
		if err != nil {
			log.WithError(err).Fatal("Failed to create OAuth token storage")
		}

		// 创建 OAuth 管理器
		oauthManager = oauth.NewManager(tokenStorage)

		// 注册 Feishu OAuth provider
		feishuProvider = providers.NewFeishuProvider(
			cfg.Feishu.AppID,
			cfg.Feishu.AppSecret,
			cfg.OAuth.BaseURL+"/oauth/callback",
		)
		oauthManager.RegisterProvider(feishuProvider)

		// 创建 OAuth HTTP 服务器（SendFunc 稍后设置，需要在 Dispatcher 创建后）
		oauthServer = oauth.NewServer(oauth.Config{
			Enable:  true,
			Port:    cfg.OAuth.Port,
			BaseURL: cfg.OAuth.BaseURL,
		}, oauthManager)

		log.WithFields(log.Fields{
			"port":    cfg.OAuth.Port,
			"baseURL": cfg.OAuth.BaseURL,
		}).Info("OAuth server started")
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

	// 注册 OAuth 和 Feishu MCP 工具（如果启用）
	if cfg.OAuth.Enable && oauthManager != nil {
		// 注册 OAuth 工具
		oauthTool := &tools.OAuthTool{
			Manager: oauthManager,
			BaseURL: cfg.OAuth.BaseURL,
		}
		agentLoop.RegisterTool(oauthTool)

		// 注册 Feishu MCP 工具
		feishuMCP := feishu_mcp.NewFeishuMCP(oauthManager)
		if feishuProvider != nil {
			feishuMCP.SetLarkClient(feishuProvider.GetLarkClient())
		}
		agentLoop.RegisterTool(&feishu_mcp.ListAllBitablesTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.BitableFieldsTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.BitableRecordTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.BitableListTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.BatchCreateAppTableRecordTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.SendCardTool{MCP: feishuMCP})

		// Wiki tools
		agentLoop.RegisterTool(&feishu_mcp.WikiListSpacesTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.WikiListNodesTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.WikiGetNodeTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.WikiMoveNodeTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.WikiCreateNodeTool{MCP: feishuMCP})

		// Document tools
		agentLoop.RegisterTool(&feishu_mcp.DocxGetContentTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxListBlocksTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxCreateTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxWriteTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxGetBlockTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.DocxDeleteBlocksTool{MCP: feishuMCP})

		// Search tools
		agentLoop.RegisterTool(&feishu_mcp.SearchWikiTool{MCP: feishuMCP})

		// Drive tools
		agentLoop.RegisterTool(&feishu_mcp.UploadFileTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.ListFilesTool{MCP: feishuMCP})
		agentLoop.RegisterTool(&feishu_mcp.AddPermissionTool{MCP: feishuMCP})

		log.Info("OAuth and Feishu MCP tools registered")
	}

	// 创建消息分发器
	disp := channel.NewDispatcher(msgBus)

	// 注册飞书渠道
	var feishuCh *channel.FeishuChannel
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

	// 注入同步发送函数，使 Agent 可直接通过 Dispatcher 发送消息并获取 message_id
	agentLoop.SetDirectSend(disp.SendDirect)

	// 设置飞书渠道的 CardBuilder（用于卡片回调处理）
	if feishuCh != nil {
		feishuCh.SetCardBuilder(agentLoop.GetCardBuilder())
	}

	// 设置 OAuth 服务器的回调函数，使其能在授权完成后发送消息
	if oauthServer != nil {
		oauthServer.SendFunc = func(channel, chatID, content string) error {
			_, err := disp.SendDirect(bus.OutboundMessage{
				Channel: channel,
				ChatID:  chatID,
				Content: content,
			})
			return err
		}
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

	log.Info("xbot started successfully")
	fmt.Println("🤖 xbot is running. Press Ctrl+C to stop.")

	// 等待退出信号
	<-sigCh
	fmt.Println("\nShutting down...")

	// 停止 OAuth 服务器
	if oauthServer != nil {
		if err := oauthServer.Shutdown(context.Background()); err != nil {
			log.WithError(err).Warn("OAuth server shutdown error")
		}
	}

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
