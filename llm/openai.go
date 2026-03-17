package llm

import (
	"context"
	"encoding/json"
	"time"

	logrus "xbot/logger"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// OpenAILLM OpenAI LLM 实现
type OpenAILLM struct {
	client       *openai.Client
	models       []string // 可用模型列表
	defaultModel string   // 默认模型
}

// OpenAIConfig OpenAI 配置
type OpenAIConfig struct {
	BaseURL      string
	APIKey       string
	DefaultModel string // 默认模型（API 获取失败时的回退模型）
}

// NewOpenAILLM 创建 OpenAI LLM 实例
func NewOpenAILLM(cfg OpenAIConfig) *OpenAILLM {
	client := openai.NewClient(
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
	)

	o := &OpenAILLM{
		client:       &client,
		models:       nil,
		defaultModel: cfg.DefaultModel,
	}

	// 尝试从 API 加载模型列表
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.LoadModelsFromAPI(ctx); err != nil {
		logrus.WithError(err).Warn("[LLM] Failed to load models from OpenAI API")
		// API 获取失败，使用默认模型作为回退
		if cfg.DefaultModel != "" {
			o.models = []string{cfg.DefaultModel}
			logrus.WithField("fallback_model", cfg.DefaultModel).Info("[LLM] Using fallback model from config")
		}
	}

	return o
}

// ListModels 获取可用模型列表
func (o *OpenAILLM) ListModels() []string {
	result := make([]string, len(o.models))
	copy(result, o.models)
	return result
}

// GetDefaultModel 获取默认模型
func (o *OpenAILLM) GetDefaultModel() string {
	if o.defaultModel != "" {
		return o.defaultModel
	}
	if len(o.models) > 0 {
		return o.models[0]
	}
	return ""
}

// LoadModelsFromAPI 从 OpenAI API 加载可用模型列表
func (o *OpenAILLM) LoadModelsFromAPI(ctx context.Context) error {
	logrus.Debug("[LLM] Loading models from OpenAI API")

	// 使用 openai-go SDK 获取模型列表
	page, err := o.client.Models.List(ctx)
	if err != nil {
		return err
	}

	// 提取模型 ID
	models := make([]string, 0, len(page.Data))
	for _, model := range page.Data {
		models = append(models, model.ID)
	}

	if len(models) == 0 {
		logrus.Warn("[LLM] No models found from OpenAI API")
		return nil
	}

	// 更新模型列表
	o.models = models

	// 如果没有设置默认模型，使用第一个模型
	if o.defaultModel == "" && len(o.models) > 0 {
		o.defaultModel = o.models[0]
	}

	logrus.WithFields(logrus.Fields{
		"model_count":   len(o.models),
		"default_model": o.defaultModel,
	}).Info("[LLM] Models loaded from OpenAI API")

	return nil
}

// buildToolCallsParam 构建工具调用参数
func buildToolCallsParam(toolCalls []ToolCall) []openai.ChatCompletionMessageToolCallUnionParam {
	result := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(toolCalls))
	for _, tc := range toolCalls {
		result = append(result, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID: tc.ID,
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			},
		})
	}
	return result
}

// extractReasoningContent 从 OpenAI 响应的 ExtraFields 中提取 reasoning_content
func extractReasoningContent(msg openai.ChatCompletionMessage) string {
	if field, ok := msg.JSON.ExtraFields["reasoning_content"]; ok {
		raw := field.Raw()
		if raw != "" && raw != "null" {
			// 尝试解析为字符串
			var str string
			if err := json.Unmarshal([]byte(raw), &str); err == nil {
				return str
			}
			// 如果解析失败，返回原始值
			return raw
		}
	}
	return ""
}

// extractReasoningContentFromDelta 从流式响应的 Delta 中提取 reasoning_content
func extractReasoningContentFromDelta(delta openai.ChatCompletionChunkChoiceDelta) string {
	if field, ok := delta.JSON.ExtraFields["reasoning_content"]; ok {
		raw := field.Raw()
		if raw != "" && raw != "null" {
			// 尝试解析为字符串
			var str string
			if err := json.Unmarshal([]byte(raw), &str); err == nil {
				return str
			}
			// 如果解析失败，返回原始值
			return raw
		}
	}
	return ""
}

// assistantMessageWithReasoning 用于构建包含 reasoning_content 的 assistant message
type assistantMessageWithReasoning struct {
	Role             string `json:"role"`
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	ToolCalls        any    `json:"tool_calls,omitempty"`
}

// toOpenAIMessages 将业务消息转换为 OpenAI 消息格式
func toOpenAIMessages(messages []ChatMessage) []openai.ChatCompletionMessageParamUnion {
	result := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "system":
			result = append(result, openai.SystemMessage(msg.Content))
		case "user":
			result = append(result, openai.UserMessage(msg.Content))
		case "assistant":
			// 如果有 reasoning_content，使用原始 JSON 构建消息
			if msg.ReasoningContent != "" {
				// 使用 param.Override 构建包含 reasoning_content 的消息
				rawMsg := assistantMessageWithReasoning{
					Role:             "assistant",
					Content:          msg.Content,
					ReasoningContent: msg.ReasoningContent,
				}
				if len(msg.ToolCalls) > 0 {
					rawMsg.ToolCalls = buildToolCallsParamForJSON(msg.ToolCalls)
				}
				jsonData, _ := json.Marshal(rawMsg)
				overridden := param.Override[openai.ChatCompletionAssistantMessageParam](json.RawMessage(jsonData))
				result = append(result, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &overridden,
				})
			} else if len(msg.ToolCalls) > 0 {
				result = append(result, openai.ChatCompletionMessageParamUnion{
					OfAssistant: &openai.ChatCompletionAssistantMessageParam{
						Content:   openai.ChatCompletionAssistantMessageParamContentUnion{OfString: param.Opt[string]{Value: msg.Content}},
						ToolCalls: buildToolCallsParam(msg.ToolCalls),
					},
				})
			} else {
				result = append(result, openai.AssistantMessage(msg.Content))
			}
		case "tool":
			result = append(result, openai.ToolMessage(msg.Content, msg.ToolCallID))
		}
	}
	return result
}

// buildToolCallsParamForJSON 构建用于 JSON 序列化的 tool calls
func buildToolCallsParamForJSON(toolCalls []ToolCall) []map[string]any {
	result := make([]map[string]any, 0, len(toolCalls))
	for _, tc := range toolCalls {
		result = append(result, map[string]any{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": tc.Arguments,
			},
		})
	}
	return result
}

// toOpenAITools 将工具转换为 OpenAI 格式
func toOpenAITools(tools []ToolDefinition) []openai.ChatCompletionToolUnionParam {
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		properties := make(map[string]any)
		required := make([]string, 0)
		for _, p := range tool.Parameters() {
			properties[p.Name] = map[string]any{
				"type":        p.Type,
				"description": p.Description,
			}
			if p.Required {
				required = append(required, p.Name)
			}
		}
		params := map[string]any{
			"type":       "object",
			"properties": properties,
			"required":   required,
		}
		result = append(result, openai.ChatCompletionToolUnionParam{
			OfFunction: &openai.ChatCompletionFunctionToolParam{
				Function: openai.FunctionDefinitionParam{
					Name:        tool.Name(),
					Description: param.Opt[string]{Value: tool.Description()},
					Parameters:  params,
				},
			},
		})
	}
	return result
}

// buildParams 构建请求参数
func (o *OpenAILLM) buildParams(model string, messages []ChatMessage, tools []ToolDefinition) openai.ChatCompletionNewParams {
	openaiMessages := toOpenAIMessages(messages)

	p := openai.ChatCompletionNewParams{
		Model:    model,
		Messages: openaiMessages,
		N:        param.Opt[int64]{Value: 1},
	}
	if len(tools) > 0 {
		p.Tools = toOpenAITools(tools)
	}
	return p
}

// buildThinkingOptions 根据 thinkingMode 构建对应的 request options
// 支持多种模型的 thinking mode：
// - DeepSeek: {"thinking": {"type": "enabled"}}
// - 智谱 GLM: {"thinking": {"type": "enabled", "clear_thinking": false}}
// - 其他模型可扩展
//
// 参数格式：
// - "enabled" -> {"thinking": {"type": "enabled"}}
// - "disabled" -> {"thinking": {"type": "disabled"}} (不发送参数，让模型自己决定)
// - 自定义 JSON: 直接使用，如 {"type": "enabled", "clear_thinking": false}
func (o *OpenAILLM) buildThinkingOptions(thinkingMode string) []option.RequestOption {
	if thinkingMode == "" {
		return nil
	}

	var opts []option.RequestOption

	switch thinkingMode {
	case "enabled":
		// DeepSeek/GLM 标准格式
		opts = append(opts, option.WithJSONSet("thinking", map[string]any{"type": "enabled"}))
	case "disabled":
		// 显式禁用 thinking
		opts = append(opts, option.WithJSONSet("thinking", map[string]any{"type": "disabled"}))
	default:
		// 尝试解析为 JSON 对象（高级用法）
		// 支持两种格式：
		// 1. 完整 thinking 参数: {"type": "enabled", "clear_thinking": false}
		// 2. 直接设置字段: {"budget_tokens": 10000}
		if len(thinkingMode) > 0 && thinkingMode[0] == '{' {
			var customParams map[string]any
			if err := json.Unmarshal([]byte(thinkingMode), &customParams); err == nil {
				// 如果包含 type 字段，说明是完整的 thinking 参数
				if _, hasType := customParams["type"]; hasType {
					opts = append(opts, option.WithJSONSet("thinking", customParams))
				} else {
					// 否则直接设置字段（如 budget_tokens）
					for key, value := range customParams {
						opts = append(opts, option.WithJSONSet(key, value))
					}
				}
			}
		}
	}

	return opts
}

// Generate 生成 LLM 响应
func (o *OpenAILLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	// 如果未指定模型，使用默认模型
	if model == "" {
		model = o.GetDefaultModel()
	}

	logrus.Ctx(ctx).WithFields(logrus.Fields{
		"provider":      "openai",
		"model":         model,
		"stream":        false,
		"msg_count":     len(messages),
		"tools_count":   len(tools),
		"thinking_mode": thinkingMode,
	}).Info("[LLM] Starting non-stream request")

	startTime := time.Now()
	params := o.buildParams(model, messages, tools)

	data, _ := params.MarshalJSON()
	logrus.Ctx(ctx).Debugf("[LLM] Request params: %s", string(data))

	// 构建 thinking mode 相关的 request options
	opts := o.buildThinkingOptions(thinkingMode)
	if len(opts) > 0 {
		logrus.Ctx(ctx).Debugf("[LLM] Thinking mode options: %v", thinkingMode)
	}

	completion, err := o.client.Chat.Completions.New(ctx, params, opts...)
	if err != nil {
		logrus.Ctx(ctx).WithFields(logrus.Fields{
			"provider": "openai",
			"duration": time.Since(startTime).String(),
			"error":    err.Error(),
		}).Error("[LLM] Request failed")
		return nil, err
	}

	// 解析响应
	resp := &LLMResponse{}

	// 解析 token 使用统计
	resp.Usage = TokenUsage{
		PromptTokens:     completion.Usage.PromptTokens,
		CompletionTokens: completion.Usage.CompletionTokens,
		TotalTokens:      completion.Usage.TotalTokens,
	}

	if len(completion.Choices) > 0 {
		choice := completion.Choices[0]
		resp.Content = choice.Message.Content
		resp.FinishReason = FinishReason(choice.FinishReason)

		// 提取 reasoning_content（DeepSeek/OpenAI reasoning 模型）
		resp.ReasoningContent = extractReasoningContent(choice.Message)

		// 解析工具调用
		if len(choice.Message.ToolCalls) > 0 {
			resp.ToolCalls = make([]ToolCall, 0, len(choice.Message.ToolCalls))
			for _, tc := range choice.Message.ToolCalls {
				logrus.Ctx(ctx).WithFields(logrus.Fields{
					"provider":  "openai",
					"tool_id":   tc.ID,
					"tool_name": tc.Function.Name,
				}).Debug("[LLM] Tool call in response")
				resp.ToolCalls = append(resp.ToolCalls, ToolCall{
					ID:        tc.ID,
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				})
			}
		}
	}

	logrus.Ctx(ctx).WithFields(logrus.Fields{
		"provider":          "openai",
		"duration":          time.Since(startTime).String(),
		"content_len":       len(resp.Content),
		"reasoning_len":     len(resp.ReasoningContent),
		"tool_calls":        len(resp.ToolCalls),
		"finish_reason":     resp.FinishReason,
		"prompt_tokens":     resp.Usage.PromptTokens,
		"completion_tokens": resp.Usage.CompletionTokens,
		"total_tokens":      resp.Usage.TotalTokens,
	}).Info("[LLM] Request completed")

	return resp, nil
}

// GenerateStream 流式生成 LLM 响应
func (o *OpenAILLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	// 如果未指定模型，使用默认模型
	if model == "" {
		model = o.GetDefaultModel()
	}

	logrus.Ctx(ctx).WithFields(logrus.Fields{
		"provider":      "openai",
		"model":         model,
		"stream":        true,
		"msg_count":     len(messages),
		"tools_count":   len(tools),
		"thinking_mode": thinkingMode,
	}).Info("[LLM] Starting stream request")

	startTime := time.Now()
	params := o.buildParams(model, messages, tools)

	data, _ := params.MarshalJSON()
	logrus.Ctx(ctx).Debugf("[LLM] Stream request params: %s", string(data))

	// 构建 thinking mode 相关的 request options
	opts := o.buildThinkingOptions(thinkingMode)
	if len(opts) > 0 {
		logrus.Ctx(ctx).Debugf("[LLM] Thinking mode options: %v", thinkingMode)
	}

	// 创建流式请求
	stream := o.client.Chat.Completions.NewStreaming(ctx, params, opts...)

	// 创建事件 channel
	eventChan := make(chan StreamEvent, 100)

	// 启动 goroutine 处理流式响应
	go o.processStream(ctx, stream, eventChan, startTime)

	return eventChan, nil
}

// processStream 处理流式响应
func (o *OpenAILLM) processStream(ctx context.Context, stream *ssestream.Stream[openai.ChatCompletionChunk], eventChan chan<- StreamEvent, startTime time.Time) {
	defer close(eventChan)
	defer stream.Close()

	l := logrus.Ctx(ctx)
	chunkCount := 0
	var firstChunkTime time.Time
	var lastUsage *TokenUsage
	doneSent := false

	for stream.Next() {
		select {
		case <-ctx.Done():
			l.WithFields(logrus.Fields{
				"provider": "openai",
				"reason":   ctx.Err().Error(),
			}).Warn("[LLM] Stream cancelled")
			eventChan <- StreamEvent{
				Type:  EventError,
				Error: ctx.Err().Error(),
			}
			return
		default:
		}

		chunk := stream.Current()
		chunkCount++

		// 记录第一个 chunk 时间
		if chunkCount == 1 {
			firstChunkTime = time.Now()
			l.WithFields(logrus.Fields{
				"provider": "openai",
				"ttft":     firstChunkTime.Sub(startTime).String(),
			}).Debug("[LLM] First chunk received")
		}

		for _, choice := range chunk.Choices {
			// 处理 reasoning_content（DeepSeek/OpenAI reasoning 模型）
			if reasoningDelta := extractReasoningContentFromDelta(choice.Delta); reasoningDelta != "" {
				eventChan <- StreamEvent{
					Type:             EventReasoningContent,
					ReasoningContent: reasoningDelta,
				}
			}

			// 处理文本内容
			if choice.Delta.Content != "" {
				eventChan <- StreamEvent{
					Type:    EventContent,
					Content: choice.Delta.Content,
				}
			}

			// 处理工具调用
			for _, tc := range choice.Delta.ToolCalls {
				if tc.ID != "" || tc.Function.Name != "" {
					l.WithFields(logrus.Fields{
						"provider":  "openai",
						"tool_id":   tc.ID,
						"tool_name": tc.Function.Name,
						"index":     tc.Index,
					}).Debug("[LLM] Tool call started")
				}
				eventChan <- StreamEvent{
					Type: EventToolCall,
					ToolCall: &ToolCallDelta{
						Index:     int(tc.Index),
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}

			// 处理完成原因
			if choice.FinishReason != "" {
				doneSent = true
				eventChan <- StreamEvent{
					Type:         EventDone,
					FinishReason: FinishReason(choice.FinishReason),
				}
			}
		}

		// 收集 usage（通常在最后一个 chunk），不单独打日志，合并到 Stream completed
		if chunk.Usage.TotalTokens > 0 {
			lastUsage = &TokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
			eventChan <- StreamEvent{
				Type:  EventUsage,
				Usage: lastUsage,
			}
		}
	}

	// 检查错误
	if err := stream.Err(); err != nil {
		l.WithFields(logrus.Fields{
			"provider":    "openai",
			"chunk_count": chunkCount,
			"duration":    time.Since(startTime).String(),
			"error":       err.Error(),
		}).Error("[LLM] Stream error")
		eventChan <- StreamEvent{
			Type:  EventError,
			Error: err.Error(),
		}
		return
	}

	fields := logrus.Fields{
		"provider":       "openai",
		"chunk_count":    chunkCount,
		"total_duration": time.Since(startTime).String(),
		"ttft":           firstChunkTime.Sub(startTime).String(),
	}
	if lastUsage != nil {
		fields["prompt_tokens"] = lastUsage.PromptTokens
		fields["completion_tokens"] = lastUsage.CompletionTokens
		fields["total_tokens"] = lastUsage.TotalTokens
	}
	l.WithFields(fields).Info("[LLM] Stream completed")

	// 仅在未通过 finish_reason 发送过 Done 时补发
	if !doneSent {
		eventChan <- StreamEvent{
			Type: EventDone,
		}
	}
}
