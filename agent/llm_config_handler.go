package agent

import (
	"context"
	"fmt"
	"strings"

	"xbot/bus"
	"xbot/storage/sqlite"
)

const setLLMUsage = `用法: /set-llm provider=<provider> base_url=<url> api_key=<key> [model=<model>] [max_context=<tokens>] [thinking_mode=<mode>]

参数说明:
  provider      - LLM 提供商: codebuddy、anthropic 或 openai/deepseek/zhipu 等 OpenAI 兼容服务
  base_url      - API 基础地址
  api_key       - API 密钥
  model         - 模型名称（可选）
  max_context   - 最大上下文 token 数（可选，0 表示不限制）
  thinking_mode - 思考模式（可选，各厂商格式不同）:
                  DeepSeek/OpenAI reasoning:
                    - enabled: 强制开启
                    - disabled: 强制关闭
                  智谱 GLM:
                    - {"type":"enabled","clear_thinking":false}: 保留式思考（多轮推理连贯）
                  Anthropic Claude:
                    - enabled: 手动模式（需配合 budget_tokens）
                    - adaptive: 自适应模式（Opus 4.6/Sonnet 4.6）
                    - {"type":"enabled","budget_tokens":10000}
                    - {"type":"adaptive","effort":"high"}  (low/medium/high)

CodeBuddy 额外参数:
  user_id       - 用户 ID
  enterprise_id - 企业 ID
  domain        - 域名

示例:
  # OpenAI 格式（适用于 OpenAI、DeepSeek、SiliconFlow 等）
  /set-llm provider=openai base_url=https://api.openai.com/v1 api_key=sk-xxx model=gpt-4
  /set-llm provider=deepseek base_url=https://api.deepseek.com/v1 api_key=sk-xxx model=deepseek-chat

  # DeepSeek R1 (Thinking Mode)
  /set-llm provider=deepseek base_url=https://api.deepseek.com/v1 api_key=sk-xxx model=deepseek-reasoner thinking_mode=enabled

  # 智谱 GLM-5/GLM-4.7 (深度思考)
  /set-llm provider=openai base_url=https://open.bigmodel.cn/api/paas/v4 api_key=xxx model=glm-5 thinking_mode=enabled

  # GLM 保留式思考（多轮对话保持推理连贯性）
  /set-llm provider=openai base_url=https://open.bigmodel.cn/api/paas/v4 api_key=xxx model=glm-4.7 thinking_mode={"type":"enabled","clear_thinking":false}

  # Anthropic Claude
  /set-llm provider=anthropic base_url=https://api.anthropic.com api_key=sk-ant-xxx model=claude-3-5-sonnet-20241022

  # Anthropic Claude Extended Thinking (手动模式)
  /set-llm provider=anthropic base_url=https://api.anthropic.com api_key=sk-ant-xxx model=claude-3-5-sonnet-20241022 thinking_mode={"type":"enabled","budget_tokens":10000}

  # Anthropic Claude Adaptive Thinking (Opus 4.6/Sonnet 4.6)
  /set-llm provider=anthropic base_url=https://api.anthropic.com api_key=sk-ant-xxx model=claude-sonnet-4-20250514 thinking_mode=adaptive

  # CodeBuddy（专有 API）
  /set-llm provider=codebuddy base_url=https://codebuddy.xxx.com api_key=xxx user_id=123 enterprise_id=456

  # 限制上下文大小
  /set-llm provider=openai base_url=https://api.openai.com/v1 api_key=sk-xxx model=gpt-4 max_context=8000

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
		case "max_context":
			var maxCtx int
			if _, err := fmt.Sscanf(value, "%d", &maxCtx); err == nil {
				cfg.MaxContext = maxCtx
			} else {
				parseErrors = true
			}
		case "user_id":
			cfg.UserID = value
		case "enterprise_id":
			cfg.EnterpriseID = value
		case "domain":
			cfg.Domain = value
		case "thinking_mode":
			// 支持: enabled, disabled, adaptive, 自定义 JSON 字符串
			if value == "enabled" || value == "disabled" || value == "adaptive" || (len(value) > 0 && value[0] == '{') {
				cfg.ThinkingMode = value
			} else {
				cfg.ThinkingMode = "" // 空/无效值表示不发送参数
			}
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

	var maxContextStr string
	if cfg.MaxContext > 0 {
		maxContextStr = fmt.Sprintf("\n- Max Context: %d", cfg.MaxContext)
	}

	var thinkingModeStr string
	if cfg.ThinkingMode != "" {
		thinkingModeStr = fmt.Sprintf("\n- Thinking Mode: %s", cfg.ThinkingMode)
	} else {
		thinkingModeStr = "\n- Thinking Mode: auto"
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("LLM 配置已保存:\n- Provider: %s\n- Base URL: %s\n- API Key: %s\n- Model: %s%s%s%s",
			cfg.Provider, cfg.BaseURL, maskedKey, cfg.Model, maxContextStr, thinkingModeStr, warning),
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
	if cfg.MaxContext > 0 {
		extraFields += fmt.Sprintf("\n- Max Context: %d", cfg.MaxContext)
	}
	if cfg.ThinkingMode != "" {
		extraFields += fmt.Sprintf("\n- Thinking Mode: %s", cfg.ThinkingMode)
	} else {
		extraFields += "\n- Thinking Mode: auto"
	}
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

// handleUnsetLLM handles /unset-llm command to remove user's LLM configuration
func (a *Agent) handleUnsetLLM(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	// Check if user has a custom config
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
			Content: "当前未配置自定义 LLM，无需清除。",
		}, nil
	}

	// Delete the config
	if err := a.llmConfigSvc.DeleteConfig(msg.SenderID); err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("清除配置失败: %v", err),
		}, nil
	}

	// Invalidate cached LLM client and HasCustomLLM cache
	a.llmFactory.Invalidate(msg.SenderID)
	a.llmFactory.InvalidateCustomLLMCache(msg.SenderID)

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "已清除自定义 LLM 配置，将使用系统默认配置。",
	}, nil
}

// handleModels handles /models command to list available models for current user's LLM
func (a *Agent) handleModels(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	// Get user's LLM client
	llmClient, currentModel, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	// Get available models
	models := llmClient.ListModels()
	if len(models) == 0 {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "当前 API 未返回可用模型列表。\n\n如果你使用自定义 LLM，请确保 /set-llm 配置正确。",
		}, nil
	}

	// Build response
	var sb strings.Builder
	sb.WriteString("可用模型列表:\n")
	for _, m := range models {
		if m == currentModel {
			fmt.Fprintf(&sb, "• %s (当前)\n", m)
		} else {
			fmt.Fprintf(&sb, "• %s\n", m)
		}
	}

	fmt.Fprintf(&sb, "\n共 %d 个模型。使用 /set-model <model> 切换模型。", len(models))

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: sb.String(),
	}, nil
}

// handleSetModel handles /set-model command to change the model for user's LLM
func (a *Agent) handleSetModel(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	// Parse command arguments
	trimmed := strings.TrimSpace(msg.Content)
	args := strings.TrimSpace(trimmed[len("/set-model"):])

	if args == "" {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "用法: /set-model <model>\n\n示例:\n  /set-model gpt-4\n  /set-model deepseek-chat\n  /set-model claude-3-5-sonnet-20241022\n\n使用 /models 查看可用模型列表。",
		}, nil
	}

	// Get current config
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
			Content: "当前未配置自定义 LLM。\n\n请先使用 /set-llm 设置你的 LLM 配置。",
		}, nil
	}

	// Update model
	oldModel := cfg.Model
	cfg.Model = strings.TrimSpace(args)

	// Save configuration
	if err := a.llmConfigSvc.SetConfig(cfg); err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("保存配置失败: %v", err),
		}, nil
	}

	// Invalidate cached LLM client
	a.llmFactory.Invalidate(msg.SenderID)

	if oldModel == "" {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("模型已设置为: %s", cfg.Model),
		}, nil
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("模型已从 %s 切换为: %s", oldModel, cfg.Model),
	}, nil
}
