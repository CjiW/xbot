package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	log "xbot/logger"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/packages/ssestream"
)

// OpenAILLM OpenAI LLM 实现
type OpenAILLM struct {
	client       *openai.Client
	mu           sync.RWMutex // 保护 models 和 defaultModel 的并发读写（C-12）
	models       []string     // 可用模型列表
	defaultModel string       // 默认模型
	maxTokens    int          // 最大生成 token 数

	// maxTokensUpgrade tracks models that reject the legacy max_tokens param
	// and need the newer max_completion_tokens. Learned at runtime via 400 errors.
	maxTokensUpgrade sync.Map // model -> bool
}

// OpenAIConfig OpenAI 配置
type OpenAIConfig struct {
	BaseURL      string
	APIKey       string
	DefaultModel string // 默认模型（API 获取失败时的回退模型）
	MaxTokens    int    // 最大生成 token 数（默认 8192）
	UserAgent    string // 自定义 User-Agent（留空使用默认值）
}

// defaultMaxTokens 默认最大生成 token 数
const defaultMaxTokens = 8192

// NewOpenAILLM 创建 OpenAI LLM 实例
func NewOpenAILLM(cfg OpenAIConfig) *OpenAILLM {
	if cfg.MaxTokens <= 0 {
		cfg.MaxTokens = defaultMaxTokens
	}

	opts := []option.RequestOption{
		option.WithBaseURL(cfg.BaseURL),
		option.WithAPIKey(cfg.APIKey),
	}

	// Set custom User-Agent if provided, otherwise use default (Cursor).
	ua := cfg.UserAgent
	if ua == "" {
		ua = DefaultOpenAIUserAgent
	}
	opts = append(opts, option.WithHeader("User-Agent", ua))

	client := openai.NewClient(opts...)

	o := &OpenAILLM{
		client:       &client,
		models:       nil,
		defaultModel: cfg.DefaultModel,
		maxTokens:    cfg.MaxTokens,
	}

	// 尝试从 API 加载模型列表
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := o.LoadModelsFromAPI(ctx); err != nil {
		log.WithError(err).Warn("[LLM] Failed to load models from OpenAI API")
		// API 获取失败，使用默认模型作为回退
		if cfg.DefaultModel != "" {
			o.mu.Lock()
			o.models = []string{cfg.DefaultModel}
			o.mu.Unlock()
			log.WithField("fallback_model", cfg.DefaultModel).Info("[LLM] Using fallback model from config")
		}
	}

	return o
}

// DefaultOpenAIUserAgent is the default User-Agent for OpenAI-compatible clients.
// Masquerades as Cursor to avoid coding-agent rate limits on providers like Zhipu.
const DefaultOpenAIUserAgent = "cursor/0.45.3"

// ListModels 获取可用模型列表
func (o *OpenAILLM) ListModels() []string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	result := make([]string, len(o.models))
	copy(result, o.models)
	return result
}

// GetDefaultModel 获取默认模型
func (o *OpenAILLM) GetDefaultModel() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.defaultModel != "" {
		return o.defaultModel
	}
	if len(o.models) > 0 {
		return o.models[0]
	}
	return ""
}

// MaxTokens returns the configured max output token limit.
func (o *OpenAILLM) MaxTokens() int {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.maxTokens
}

// LoadModelsFromAPI 从 OpenAI API 加载可用模型列表
func (o *OpenAILLM) LoadModelsFromAPI(ctx context.Context) error {
	log.Debug("[LLM] Loading models from OpenAI API")

	// 使用 openai-go SDK 获取模型列表
	page, err := o.client.Models.List(ctx)
	if err != nil {
		return fmt.Errorf("openai models list: %w", err)
	}

	// 提取模型 ID
	models := make([]string, 0, len(page.Data))
	for _, model := range page.Data {
		models = append(models, model.ID)
	}

	if len(models) == 0 {
		log.Warn("[LLM] No models found from OpenAI API")
		return nil
	}

	// 更新模型列表
	o.mu.Lock()
	o.models = models
	if o.defaultModel == "" && len(o.models) > 0 {
		o.defaultModel = o.models[0]
	}
	modelCount := len(o.models)
	defaultModel := o.defaultModel
	o.mu.Unlock()

	log.WithFields(log.Fields{
		"model_count":   modelCount,
		"default_model": defaultModel,
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
	Content          any    `json:"content"` // string or null — must be present per OpenAI protocol
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
			// Check for embedded images (data: URLs in markdown image syntax)
			parts := parseEmbeddedImages(msg.Content)
			if len(parts) > 1 {
				// Multi-part message with images
				var contentParts []openai.ChatCompletionContentPartUnionParam
				for _, p := range parts {
					switch p.Type {
					case "text":
						contentParts = append(contentParts, openai.TextContentPart(p.Text))
					case "image":
						contentParts = append(contentParts, openai.ImageContentPart(
							openai.ChatCompletionContentPartImageImageURLParam{URL: p.URL},
						))
					}
				}
				result = append(result, openai.ChatCompletionMessageParamUnion{
					OfUser: &openai.ChatCompletionUserMessageParam{
						Content: openai.ChatCompletionUserMessageParamContentUnion{
							OfArrayOfContentParts: contentParts,
						},
					},
				})
			} else {
				result = append(result, openai.UserMessage(msg.Content))
			}
		case "assistant":
			// 如果有 reasoning_content，使用原始 JSON 构建消息
			if msg.ReasoningContent != "" {
				// 使用 param.Override 构建包含 reasoning_content 的消息
				var content any = msg.Content // 空字符串序列化为 "content":""
				if msg.Content == "" {
					content = nil // OpenAI 协议: 空 content 用 null
				}
				rawMsg := assistantMessageWithReasoning{
					Role:             "assistant",
					Content:          content,
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

// embeddedImageRe matches markdown image syntax with data: URLs: ![alt](data:...)
var embeddedImageRe = regexp.MustCompile(`!\[([^\]]*)\]\((data:[^)]+)\)`)

// imageContentPart represents a parsed content segment (text or image).
type imageContentPart struct {
	Type string // "text" or "image"
	Text string // for text parts
	URL  string // for image parts
}

// parseEmbeddedImages splits content containing embedded data-URL images into parts.
// Returns a single text part if no images found.
func parseEmbeddedImages(content string) []imageContentPart {
	if !strings.Contains(content, "data:") {
		return []imageContentPart{{Type: "text", Text: content}}
	}

	locs := embeddedImageRe.FindAllStringSubmatchIndex(content, -1)
	if len(locs) == 0 {
		return []imageContentPart{{Type: "text", Text: content}}
	}

	var parts []imageContentPart
	lastIdx := 0
	for _, loc := range locs {
		// loc[0:2] = full match, loc[2:4] = alt text group, loc[4:6] = URL group
		// Add text before this image
		if loc[0] > lastIdx {
			text := strings.TrimSpace(content[lastIdx:loc[0]])
			if text != "" {
				parts = append(parts, imageContentPart{Type: "text", Text: text})
			}
		}
		// Add the image part
		url := content[loc[4]:loc[5]]
		parts = append(parts, imageContentPart{Type: "image", URL: url})
		lastIdx = loc[1]
	}
	// Add remaining text after last image
	if lastIdx < len(content) {
		text := strings.TrimSpace(content[lastIdx:])
		if text != "" {
			parts = append(parts, imageContentPart{Type: "text", Text: text})
		}
	}

	if len(parts) == 0 {
		return []imageContentPart{{Type: "text", Text: content}}
	}
	return parts
}

// toOpenAITools 将工具转换为 OpenAI 格式
func toOpenAITools(tools []ToolDefinition) []openai.ChatCompletionToolUnionParam {
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, tool := range tools {
		properties := make(map[string]any)
		required := make([]string, 0)
		for _, p := range tool.Parameters() {
			prop := map[string]any{
				"type":        p.Type,
				"description": p.Description,
			}
			if p.Items != nil {
				prop["items"] = p.Items
			}
			properties[p.Name] = prop
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

	// OpenAI API has two mutually exclusive params for max output tokens:
	//   - max_completion_tokens (new, required by o1/o3/o4/gpt-5.4+)
	//   - max_tokens (legacy, broadly compatible across all providers)
	//
	// Strategy: default to max_tokens (works everywhere including GLM, Claude
	// proxies, etc.). If a model rejects it with a 400 error, we learn at
	// runtime and switch to max_completion_tokens (see isMaxTokensParamError).
	// Note: some providers (GLM) silently ignore max_completion_tokens without
	// error, so max_tokens is the safer default.
	if _, useNew := o.maxTokensUpgrade.Load(model); useNew {
		p.MaxCompletionTokens = param.Opt[int64]{Value: int64(o.maxTokens)}
	} else {
		p.MaxTokens = param.Opt[int64]{Value: int64(o.maxTokens)}
	}

	if len(tools) > 0 {
		p.Tools = toOpenAITools(tools)
	}
	return p
}

// isMaxTokensParamError checks if a 400 error is caused by the wrong
// max_tokens / max_completion_tokens parameter choice.
// Returns:
//   - "use_legacy" if the model rejects max_completion_tokens (need max_tokens)
//   - "use_new" if the model rejects max_tokens (need max_completion_tokens)
//   - "" if the error is unrelated
//
// Strategy: the rejected parameter name appears first in the error message.
// e.g. "'max_tokens' is not supported ... Use 'max_completion_tokens' instead"
// → "max_tokens" appears first → it's the rejected one → return "use_new".
func isMaxTokensParamError(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	idxMT := strings.Index(msg, "max_tokens")
	idxMCT := strings.Index(msg, "max_completion_tokens")
	if idxMT < 0 && idxMCT < 0 {
		return ""
	}
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "not supported") && !strings.Contains(lower, "unsupported") {
		return ""
	}
	// "max_tokens" is NOT a substring of "max_completion_tokens", so Index is unambiguous.
	// The rejected param appears first; the suggested alternative appears later.
	if idxMT >= 0 && (idxMCT < 0 || idxMT < idxMCT) {
		return "use_new"
	}
	if idxMCT >= 0 && (idxMT < 0 || idxMCT < idxMT) {
		return "use_legacy"
	}
	return ""
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
		// 不发送任何 thinking 参数，让模型自己决定
	default:
		// JSON 格式的 thinking 参数
		if len(thinkingMode) > 0 && thinkingMode[0] == '{' {
			var customParams map[string]any
			if err := json.Unmarshal([]byte(thinkingMode), &customParams); err == nil {
				if _, hasType := customParams["type"]; hasType {
					opts = append(opts, option.WithJSONSet("thinking", customParams))
				} else {
					for key, value := range customParams {
						opts = append(opts, option.WithJSONSet(key, value))
					}
				}
			} else {
				log.WithFields(log.Fields{
					"thinking_mode": thinkingMode,
					"error":         err.Error(),
				}).Warn("[LLM] Failed to parse thinking mode as JSON, ignoring")
			}
		} else {
			log.WithField("thinking_mode", thinkingMode).Warn("[LLM] Unknown thinking mode is not valid JSON, ignoring")
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

	log.Ctx(ctx).WithFields(log.Fields{
		"provider":      "openai",
		"model":         model,
		"stream":        false,
		"msg_count":     len(messages),
		"tools_count":   len(tools),
		"thinking_mode": thinkingMode,
		"max_tokens":    o.maxTokens,
	}).Info("[LLM] Starting non-stream request")

	startTime := time.Now()
	params := o.buildParams(model, messages, tools)

	// 构建 thinking mode 相关的 request options
	opts := o.buildThinkingOptions(thinkingMode)
	if len(opts) > 0 {
		log.Ctx(ctx).Debugf("[LLM] Thinking mode options: %v", thinkingMode)
	}

	completion, err := o.client.Chat.Completions.New(ctx, params, opts...)
	if err != nil {
		// Auto-detect max_tokens vs max_completion_tokens mismatch and retry
		if verdict := isMaxTokensParamError(err); verdict != "" {
			if verdict == "use_new" {
				o.maxTokensUpgrade.Store(model, true)
				log.Ctx(ctx).WithField("model", model).Info("[LLM] Model requires max_completion_tokens, retrying")
			} else {
				o.maxTokensUpgrade.Delete(model)
				log.Ctx(ctx).WithField("model", model).Info("[LLM] Model requires legacy max_tokens, retrying")
			}
			params = o.buildParams(model, messages, tools)
			completion, err = o.client.Chat.Completions.New(ctx, params, opts...)
		}
	}
	if err != nil {
		log.Ctx(ctx).WithFields(log.Fields{
			"provider": "openai",
			"duration": time.Since(startTime).String(),
			"error":    err.Error(),
		}).Error("[LLM] Request failed")
		return nil, fmt.Errorf("openai chat completion: %w", err)
	}

	// 解析响应
	resp := &LLMResponse{}

	// 解析 token 使用统计
	resp.Usage = TokenUsage{
		PromptTokens:     completion.Usage.PromptTokens,
		CompletionTokens: completion.Usage.CompletionTokens,
		TotalTokens:      completion.Usage.TotalTokens,
	}
	// OpenAI prompt_tokens_details.cached_tokens
	if ptd := completion.Usage.PromptTokensDetails; ptd.CachedTokens > 0 {
		resp.Usage.CacheHitTokens = ptd.CachedTokens
	}

	if len(completion.Choices) > 0 {
		choice := completion.Choices[0]
		resp.Content = choice.Message.Content
		resp.FinishReason = FinishReason(choice.FinishReason)

		// 提取 reasoning_content（DeepSeek/OpenAI reasoning 模型）
		resp.ReasoningContent = extractReasoningContent(choice.Message)

		// BUG 5 fix: 某些 provider (DeepSeek) 会在 Content 中重复包含 reasoning_content。
		// 如果 reasoning_content 非空且 Content 以 reasoning_content 开头，去除重复部分。
		// 使用 TrimSpace 比较，避免前导空白（如 \n）导致漏判。
		if resp.ReasoningContent != "" && resp.Content != "" {
			if strings.TrimSpace(resp.Content) == strings.TrimSpace(resp.ReasoningContent) {
				resp.Content = ""
			} else if strings.HasPrefix(resp.Content, resp.ReasoningContent) {
				resp.Content = strings.TrimSpace(resp.Content[len(resp.ReasoningContent):])
			}
		}

		// 解析工具调用
		if len(choice.Message.ToolCalls) > 0 {
			resp.ToolCalls = make([]ToolCall, 0, len(choice.Message.ToolCalls))
			for _, tc := range choice.Message.ToolCalls {
				log.Ctx(ctx).WithFields(log.Fields{
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

	// Infer finish_reason from actual response data.
	// Some providers send "stop" instead of "tool_calls" even when tool_calls are present.
	if resp.FinishReason == "" && len(resp.ToolCalls) > 0 {
		resp.FinishReason = FinishReasonToolCalls
	}

	fields := log.Fields{
		"provider":          "openai",
		"duration":          time.Since(startTime).String(),
		"choices_count":     len(completion.Choices),
		"content_len":       len(resp.Content),
		"reasoning_len":     len(resp.ReasoningContent),
		"tool_calls":        len(resp.ToolCalls),
		"finish_reason":     resp.FinishReason,
		"prompt_tokens":     resp.Usage.PromptTokens,
		"completion_tokens": resp.Usage.CompletionTokens,
		"total_tokens":      resp.Usage.TotalTokens,
	}
	if isNearEmptyResponse(resp) {
		addNearEmptyResponseDebugFields(fields, messages, model, tools, thinkingMode)
		log.Ctx(ctx).WithFields(fields).Warn("[LLM] Request completed with near-empty response")
	} else {
		log.Ctx(ctx).WithFields(fields).Debug("[LLM] Request completed")
	}

	return resp, nil
}

// GenerateStream 流式生成 LLM 响应
func (o *OpenAILLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	// 如果未指定模型，使用默认模型
	if model == "" {
		model = o.GetDefaultModel()
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"provider":      "openai",
		"model":         model,
		"stream":        true,
		"msg_count":     len(messages),
		"tools_count":   len(tools),
		"thinking_mode": thinkingMode,
		"max_tokens":    o.maxTokens,
	}).Info("[LLM] Starting stream request")

	startTime := time.Now()

	// 构建 thinking mode 相关的 request options
	opts := o.buildThinkingOptions(thinkingMode)
	if len(opts) > 0 {
		log.Ctx(ctx).Debugf("[LLM] Thinking mode options: %v", thinkingMode)
	}

	stream, err := o.newStreamingWithRetry(ctx, model, messages, tools, opts)
	if err != nil {
		return nil, fmt.Errorf("openai stream completion: %w", err)
	}

	// 创建事件 channel
	eventChan := make(chan StreamEvent, 100)

	// 启动 goroutine 处理流式响应
	go o.processStream(ctx, stream, eventChan, startTime, messages, model, tools, thinkingMode)

	return eventChan, nil
}

func (o *OpenAILLM) newStreamingWithRetry(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, opts []option.RequestOption) (*ssestream.Stream[openai.ChatCompletionChunk], error) {
	params := o.buildParams(model, messages, tools)
	stream := o.client.Chat.Completions.NewStreaming(ctx, params, opts...)
	if err := stream.Err(); err != nil {
		if verdict := isMaxTokensParamError(err); verdict != "" {
			if verdict == "use_new" {
				o.maxTokensUpgrade.Store(model, true)
				log.Ctx(ctx).WithField("model", model).Info("[LLM] Stream: model requires max_completion_tokens, retrying")
			} else {
				o.maxTokensUpgrade.Delete(model)
				log.Ctx(ctx).WithField("model", model).Info("[LLM] Stream: model requires legacy max_tokens, retrying")
			}
			stream.Close()
			params = o.buildParams(model, messages, tools)
			stream = o.client.Chat.Completions.NewStreaming(ctx, params, opts...)
			if retryErr := stream.Err(); retryErr != nil {
				stream.Close()
				return nil, retryErr
			}
			return stream, nil
		}
		stream.Close()
		return nil, err
	}
	return stream, nil
}

// processStream 处理流式响应
func (o *OpenAILLM) processStream(ctx context.Context, stream *ssestream.Stream[openai.ChatCompletionChunk], eventChan chan<- StreamEvent, startTime time.Time, messages []ChatMessage, model string, tools []ToolDefinition, thinkingMode string) {
	defer close(eventChan)
	defer stream.Close()

	l := log.Ctx(ctx)
	chunkCount := 0
	var firstChunkTime time.Time
	var lastUsage *TokenUsage
	var lastFinishReason FinishReason
	var hasToolCalls bool // track if any tool call deltas were seen

	for stream.Next() {
		select {
		case <-ctx.Done():
			l.WithFields(log.Fields{
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
			l.WithFields(log.Fields{
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

			// 处理工具调用 — 跳过全空 delta（BUG 4 fix）
			for _, tc := range choice.Delta.ToolCalls {
				if tc.ID == "" && tc.Function.Name == "" && tc.Function.Arguments == "" {
					continue
				}
				if tc.ID != "" || tc.Function.Name != "" {
					l.WithFields(log.Fields{
						"provider":  "openai",
						"tool_id":   tc.ID,
						"tool_name": tc.Function.Name,
						"index":     tc.Index,
					}).Debug("[LLM] Tool call started")
				}
				hasToolCalls = true
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

			// 记录 finish_reason 但不立即发送 EventDone（BUG 1 fix）
			// 只在 stream 循环结束后统一发送，防止中间 chunk 的 finish_reason
			// 导致消费方提前终止，丢失后续 content/tool_calls。
			if choice.FinishReason != "" {
				lastFinishReason = FinishReason(choice.FinishReason)
			}
		}

		// 收集 usage（通常在最后一个 chunk），不单独打日志，合并到 Stream completed
		hasUsage := chunk.Usage.TotalTokens > 0 || chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0
		if hasUsage {
			lastUsage = &TokenUsage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
			if ptd := chunk.Usage.PromptTokensDetails; ptd.CachedTokens > 0 {
				lastUsage.CacheHitTokens = ptd.CachedTokens
			}
		}
	}

	// 检查错误
	if err := stream.Err(); err != nil {
		// Learn max_tokens preference for future requests (mid-stream safety net;
		// HTTP-level 400 is handled by newStreamingWithRetry before reaching here).
		if verdict := isMaxTokensParamError(err); verdict != "" {
			if verdict == "use_new" {
				o.maxTokensUpgrade.Store(model, true)
				l.WithField("model", model).Info("[LLM] Stream: mid-stream max_tokens error, updated preference for future requests")
			} else {
				o.maxTokensUpgrade.Delete(model)
				l.WithField("model", model).Info("[LLM] Stream: mid-stream max_tokens error, updated preference for future requests")
			}
		}
		l.WithFields(log.Fields{
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

	// BUG 2 fix: 先发 Usage，再发 Done。确保消费方在处理 Done 之前拿到 usage。
	if lastUsage != nil {
		eventChan <- StreamEvent{
			Type:  EventUsage,
			Usage: lastUsage,
		}
	}

	// BUG 1 fix: 统一在 stream 结束后发送 EventDone。
	// 如果 provider 没有发送 finish_reason，但有 tool_calls，推断为 tool_calls。
	if lastFinishReason == "" && hasToolCalls {
		lastFinishReason = FinishReasonToolCalls
	}
	eventChan <- StreamEvent{
		Type:         EventDone,
		FinishReason: lastFinishReason,
	}

	fields := log.Fields{
		"provider":       "openai",
		"chunk_count":    chunkCount,
		"total_duration": time.Since(startTime).String(),
		"ttft":           firstChunkTime.Sub(startTime).String(),
		"finish_reason":  lastFinishReason,
	}
	if lastUsage != nil {
		fields["prompt_tokens"] = lastUsage.PromptTokens
		fields["completion_tokens"] = lastUsage.CompletionTokens
		fields["total_tokens"] = lastUsage.TotalTokens
	}
	// Debug: 当 chunk_count 极低（空响应）时打印详细请求信息
	if chunkCount <= 1 {
		addNearEmptyResponseDebugFields(fields, messages, model, tools, thinkingMode)
		l.WithFields(fields).Warn("[LLM] Stream completed with near-empty response")
	} else {
		l.WithFields(fields).Debug("[LLM] Stream completed")
	}
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func isNearEmptyResponse(resp *LLMResponse) bool {
	if resp == nil {
		return true
	}
	return resp.Content == "" && len(resp.ToolCalls) == 0
}

func addNearEmptyResponseDebugFields(fields log.Fields, messages []ChatMessage, model string, tools []ToolDefinition, thinkingMode string) {
	fields["msg_count"] = len(messages)
	fields["tools_count"] = len(tools)
	fields["model"] = model
	fields["thinking_mode"] = thinkingMode
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			fields["last_user_msg_preview"] = truncateStr(messages[i].Content, 100)
			break
		}
	}
}
