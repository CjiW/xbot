package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/llm"

	log "github.com/sirupsen/logrus"
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
	agentLoop := agent.New(agent.Config{
		Bus:           msgBus,
		LLM:           llmClient,
		Model:         cfg.LLM.Model,
		MaxIterations: cfg.Agent.MaxIterations,
		MemoryWindow:  cfg.Agent.MemoryWindow,
		SessionPath:   "data/session.jsonl",
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
