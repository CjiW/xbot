package agent

import (
	"context"
	"fmt"
	"strings"

	"xbot/bus"
	"xbot/storage/sqlite"
)

const setLLMUsage = `用法: /set-llm provider=<provider> base_url=<url> api_key=<key> [model=<model>]

参数说明:
  provider    - LLM 提供商: codebuddy、anthropic 或 openai/deepseek/siliconflow 等 OpenAI 兼容服务
  base_url    - API 基础地址
  api_key     - API 密钥
  model       - 模型名称（可选）

CodeBuddy 额外参数:
  user_id       - 用户 ID
  enterprise_id - 企业 ID
  domain        - 域名

示例:
  # OpenAI 格式（适用于 OpenAI、DeepSeek、SiliconFlow 等）
  /set-llm provider=openai base_url=https://api.openai.com/v1 api_key=sk-xxx model=gpt-4
  /set-llm provider=deepseek base_url=https://api.deepseek.com/v1 api_key=sk-xxx model=deepseek-chat

  # Anthropic Claude
  /set-llm provider=anthropic base_url=https://api.anthropic.com api_key=sk-ant-xxx model=claude-3-5-sonnet-20241022

  # CodeBuddy（专有 API）
  /set-llm provider=codebuddy base_url=https://codebuddy.xxx.com api_key=xxx user_id=123 enterprise_id=456

注意: API Key 会被加密存储，查询时只显示前4位。`

// handleSetLLM handles /set-llm command to set user's LLM configuration
func (a *Agent) handleSetLLM(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	// Parse command arguments
	trimmed := strings.TrimSpace(msg.Content)
	args := strings.TrimSpace(trimmed[len("/set-llm"):])

	if args == "" {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: setLLMUsage,
		}, nil
	}

	// Parse key=value pairs
	cfg := &sqlite.UserLLMConfig{
		SenderID: msg.SenderID,
	}

	parts := strings.Fields(args)
	parseErrors := false
	for _, part := range parts {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			parseErrors = true
			continue
		}
		key := strings.ToLower(kv[0])
		value := kv[1]

		switch key {
		case "provider":
			cfg.Provider = value
		case "base_url":
			cfg.BaseURL = value
		case "api_key":
			cfg.APIKey = value
		case "model":
			cfg.Model = value
		case "user_id":
			cfg.UserID = value
		case "enterprise_id":
			cfg.EnterpriseID = value
		case "domain":
			cfg.Domain = value
		}
	}

	// Validate required fields
	if cfg.Provider == "" || cfg.BaseURL == "" || cfg.APIKey == "" {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("错误: 必须提供 provider, base_url 和 api_key 参数。\n\n%s", setLLMUsage),
		}, nil
	}

	// Warn about parse errors
	var warning string
	if parseErrors {
		warning = "\n⚠️ 注意: 部分参数格式不正确，已被忽略。"
	}

	// Save configuration
	if err := a.llmConfigSvc.SetConfig(cfg); err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("保存配置失败: %v", err),
		}, nil
	}

	// Invalidate cached LLM client and HasCustomLLM cache
	a.llmFactory.Invalidate(msg.SenderID)
	a.llmFactory.InvalidateCustomLLMCache(msg.SenderID)

	// Mask API key for display
	maskedKey := maskAPIKey(cfg.APIKey)

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("LLM 配置已保存:\n- Provider: %s\n- Base URL: %s\n- API Key: %s\n- Model: %s%s",
			cfg.Provider, cfg.BaseURL, maskedKey, cfg.Model, warning),
	}, nil
}

// handleGetLLM handles /llm command to show current user's LLM configuration
func (a *Agent) handleGetLLM(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	cfg, err := a.llmConfigSvc.GetConfig(msg.SenderID)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("查询配置失败: %v", err),
		}, nil
	}

	if cfg == nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "当前未配置自定义 LLM，使用系统默认配置。\n\n使用 /set-llm 命令设置你的专属 LLM 配置。",
		}, nil
	}

	// Mask API key for display
	maskedKey := maskAPIKey(cfg.APIKey)

	var extraFields string
	if cfg.UserID != "" {
		extraFields += fmt.Sprintf("\n- User ID: %s", cfg.UserID)
	}
	if cfg.EnterpriseID != "" {
		extraFields += fmt.Sprintf("\n- Enterprise ID: %s", cfg.EnterpriseID)
	}
	if cfg.Domain != "" {
		extraFields += fmt.Sprintf("\n- Domain: %s", cfg.Domain)
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("当前 LLM 配置:\n- Provider: %s\n- Base URL: %s\n- API Key: %s\n- Model: %s%s",
			cfg.Provider, cfg.BaseURL, maskedKey, cfg.Model, extraFields),
	}, nil
}

// maskAPIKey masks API key, showing only first 4 characters
func maskAPIKey(key string) string {
	if len(key) <= 4 {
		return "****"
	}
	return key[:4] + "****"
}
