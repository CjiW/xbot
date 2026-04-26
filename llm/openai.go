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
	client            *openai.Client
	mu                sync.RWMutex   // 保护 models 和 defaultModel 的并发读写（C-12）
	models            []string       // 可用模型列表
	defaultModel      string         // 默认模型
	maxTokens         int            // 最大生成 token 数（用户配置值，作为上限）
	onModelsLoaded    func([]string) // callback after models loaded from API
	onModelsLoadError func(error)    // callback after models load fails
	modelsLoaded      bool           // true after first ListModels() triggers async fetch

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

	// OnModelsLoadError is called when the async model list API call fails.
	// Used by CLI to show a toast notification.
	OnModelsLoadError func(err error)

	// OnModelsLoaded is called when the async model list API call succeeds.
	// Receives the full list of model names. Used to cache models in DB.
	OnModelsLoaded func(models []string)

	// SubscriptionID identifies the subscription that owns this client.
	// Used by OnModelsLoaded to know which subscription to update.
	SubscriptionID string
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

	// Strip Go SDK fingerprint headers — TypeScript clients (opencode, cursor)
	// never send these. Options run AFTER requestconfig sets defaults, so
	// WithHeaderDel correctly removes them.
	for _, h := range stainlessHeaders {
		opts = append(opts, option.WithHeaderDel(h))
	}

	// Set custom User-Agent if provided, otherwise use default (opencode).
	ua := cfg.UserAgent
	if ua == "" {
		ua = DefaultOpenAIUserAgent
	}
	opts = append(opts, option.WithHeader("User-Agent", ua))

	client := openai.NewClient(opts...)

	o := &OpenAILLM{
		client:         &client,
		models:         nil,
		defaultModel:   cfg.DefaultModel,
		maxTokens:      cfg.MaxTokens,
		onModelsLoaded: cfg.OnModelsLoaded,
	}

	// Set fallback model immediately so ListModels() always returns something.
	if cfg.DefaultModel != "" {
		o.mu.Lock()
		o.models = []string{cfg.DefaultModel}
		o.mu.Unlock()
	}

	// Lazy: don't fire LoadModelsFromAPI in constructor.
	// Trigger on first ListModels() call instead, so unused clients
	// (e.g. subscriptions for users who haven't sent a message yet)
	// don't spam the API on startup.
	o.onModelsLoadError = cfg.OnModelsLoadError

	return o
}

// DefaultOpenAIUserAgent is the default User-Agent for OpenAI-compatible clients.
// Matches opencode's User-Agent to avoid coding-agent fingerprinting.
const DefaultOpenAIUserAgent = "opencode/1.14.17"

// stainlessHeaders are headers injected by the OpenAI Go SDK that TypeScript
// clients (opencode, cursor) never send. They must be stripped to avoid
// client fingerprinting.
var stainlessHeaders = []string{
	"X-Stainless-Lang",
	"X-Stainless-Package-Version",
	"X-Stainless-OS",
	"X-Stainless-Arch",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
}

// ListModels 获取可用模型列表
// Triggers an async model list fetch on the first call (lazy loading).
func (o *OpenAILLM) ListModels() []string {
	o.mu.RLock()
	result := make([]string, len(o.models))
	copy(result, o.models)
	loaded := o.modelsLoaded
	o.mu.RUnlock()

	// Lazy load: trigger async fetch on first call.
	if !loaded {
		o.triggerModelLoad()
	}

	return result
}

// triggerModelLoad fires a one-time async model list fetch.
// Subsequent calls are no-ops once modelsLoaded is set.
func (o *OpenAILLM) triggerModelLoad() {
	o.mu.Lock()
	if o.modelsLoaded {
		o.mu.Unlock()
		return
	}
	o.modelsLoaded = true
	onError := o.onModelsLoadError
	o.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := o.LoadModelsFromAPI(ctx); err != nil {
			log.WithError(err).Warn("[LLM] Failed to load models from OpenAI API")
			if onError != nil {
				onError(err)
			}
		}
	}()
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

	// Use ListAutoPaging to fetch ALL models across pages.
	// OpenAI may return >100 models; a single List() call only returns the first page.
	pager := o.client.Models.ListAutoPaging(ctx)
	models := make([]string, 0)
	for pager.Next() {
		models = append(models, pager.Current().ID)
	}
	if err := pager.Err(); err != nil {
		return fmt.Errorf("openai models list: %w", err)
	}

	if len(models) == 0 {
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

	// Notify callback (e.g. to cache models in DB)
	if o.onModelsLoaded != nil {
		o.onModelsLoaded(models)
	}

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

// assistantMessageWithReasoning 用于构建包含 reasoning_content 的 assistant message。
// 注意：ReasoningContent 不使用 omitempty——当 thinking mode 开启时，即使值为空字符串也必须传给 API（DeepSeek 要求）。
// 此结构体仅在显式决定需要 reasoning_content 字段时才序列化（thinking mode 开启或有实际 reasoning 内容），
// 所以不带 omitempty 不会影响非 thinking 模式下的行为。
type assistantMessageWithReasoning struct {
	Role             string `json:"role"`
	Content          any    `json:"content"` // string or null — must be present per OpenAI protocol
	ReasoningContent string `json:"reasoning_content"`
	ToolCalls        any    `json:"tool_calls,omitempty"`
}

func hasAssistantReasoningHistory(messages []ChatMessage) bool {
	for _, msg := range messages {
		if msg.Role == "assistant" && msg.ReasoningContent != "" {
			return true
		}
	}
	return false
}

// toOpenAIMessages 将业务消息转换为 OpenAI 消息格式。
// 显式开启 thinking mode 时，所有 assistant 消息都会包含 reasoning_content 字段。
// 在 auto 模式下，只要历史里已经出现过 assistant reasoning，也会为所有 assistant
// 消息补齐该字段（缺失时传空字符串），以满足 DeepSeek/OpenAI reasoning provider
// 对历史消息形状的一致性要求。
func toOpenAIMessages(messages []ChatMessage, thinkingMode string) []openai.ChatCompletionMessageParamUnion {
	thinkingEnabled := thinkingMode != "" && thinkingMode != "disabled"
	reasoningHistoryObserved := thinkingMode != "disabled" && hasAssistantReasoningHistory(messages)
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
			// Thinking mode 开启，或有实际 reasoning_content 时，使用 param.Override 路径
			// 确保 reasoning_content 字段始终传给 API（DeepSeek thinking mode 要求）
			if thinkingEnabled || reasoningHistoryObserved || msg.ReasoningContent != "" {
				var content any = msg.Content
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

// buildToolCallsParamForJSON 构建用于 JSON 序列化的 tool calls。
// OpenAI/DeepSeek-compatible chat history requires function.arguments to stay a
// JSON string, not a decoded object. Empty arguments are normalized to "{}".
func buildToolCallsParamForJSON(toolCalls []ToolCall) []map[string]any {
	result := make([]map[string]any, 0, len(toolCalls))
	for _, tc := range toolCalls {
		args := tc.Arguments
		if args == "" {
			args = "{}"
		}
		result = append(result, map[string]any{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": args,
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
// modelMaxOutputTokens returns the maximum output tokens a model can produce.
// Used to clamp max_tokens/max_completion_tokens to prevent API errors when
// the user configures a value larger than the model supports.
// Returns 0 for unknown models (no clamping — let the API decide).
func modelMaxOutputTokens(model string) int {
	// Prefix match: "gpt-4.1-mini-2025-04-14" should match "gpt-4.1-mini".
	// Order matters: more specific prefixes first.
	type limit struct {
		prefix string
		tokens int
	}
	limits := []limit{
		// OpenAI GPT-4.1 family (2025-04)
		{"gpt-4.1-nano", 32768},
		{"gpt-4.1-mini", 32768},
		{"gpt-4.1", 32768},
		// OpenAI o-series reasoning models
		{"o4-mini", 100000},
		{"o3-mini", 65536},
		{"o3", 100000},
		{"o1-mini", 65536},
		{"o1", 100000},
		// OpenAI GPT-4o family
		{"gpt-4o-mini", 16384},
		{"gpt-4o", 16384},
		{"gpt-4-turbo", 4096},
		{"gpt-4-32k", 4096},
		{"gpt-4", 8192},
		{"gpt-3.5-turbo", 4096},
		// Anthropic Claude (via proxy)
		{"claude-opus-4", 32768},
		{"claude-sonnet-4", 16384},
		{"claude-3-5-sonnet", 8192},
		{"claude-3-opus", 4096},
		{"claude-3-haiku", 4096},
		// DeepSeek
		{"deepseek-r1", 16384},
		{"deepseek-reasoner", 16384},
		{"deepseek-v3", 8192},
		{"deepseek-chat", 8192},
		// Zhipu GLM
		{"glm-4-plus", 4096},
		{"glm-4", 4096},
		{"glm-4-flash", 4096},
		// Google Gemini (via proxy)
		{"gemini-2.5-pro", 65536},
		{"gemini-2.5-flash", 65536},
		{"gemini-2.0-flash", 8192},
		{"gemini-1.5-pro", 8192},
		// Qwen
		{"qwen-max", 8192},
		{"qwen-plus", 8192},
		{"qwen-turbo", 8192},
	}
	lower := strings.ToLower(model)
	for _, l := range limits {
		if strings.HasPrefix(lower, l.prefix) {
			return l.tokens
		}
	}
	return 0
}

func (o *OpenAILLM) buildParams(model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string, stream bool) openai.ChatCompletionNewParams {
	openaiMessages := toOpenAIMessages(messages, thinkingMode)

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
	effectiveMaxTokens := o.maxTokens

	// Clamp to model's max output token limit to prevent API errors.
	// Models not in this table are left unclamped — the API will return
	// its own error if the value exceeds the model's limit.
	if maxOut := modelMaxOutputTokens(model); maxOut > 0 && effectiveMaxTokens > maxOut {
		effectiveMaxTokens = maxOut
	}

	if _, useNew := o.maxTokensUpgrade.Load(model); useNew {
		p.MaxCompletionTokens = param.Opt[int64]{Value: int64(effectiveMaxTokens)}
	} else {
		p.MaxTokens = param.Opt[int64]{Value: int64(effectiveMaxTokens)}
	}

	if len(tools) > 0 {
		p.Tools = toOpenAITools(tools)
	}
	// Only set StreamOptions for streaming requests — some providers
	// (e.g. DeepSeek) reject stream_options when stream=false.
	if stream {
		p.StreamOptions = openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.Opt[bool]{Value: true},
		}
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
		// Supports two formats:
		//   1. Flat thinking object: {"type": "enabled"} → set as "thinking" param
		//   2. Nested with extras:   {"thinking": {"type": "enabled"}, "reasoning_effort": "high"}
		//      → "thinking" object goes to "thinking" param, other keys become top-level params
		//   3. Arbitrary key-values: {"reasoning_effort": "high"} → each key is a top-level param
		if len(thinkingMode) > 0 && thinkingMode[0] == '{' {
			var customParams map[string]any
			if err := json.Unmarshal([]byte(thinkingMode), &customParams); err == nil {
				if thinkingObj, hasThinking := customParams["thinking"]; hasThinking {
					// Format 2: explicit "thinking" key + optional top-level params
					opts = append(opts, option.WithJSONSet("thinking", thinkingObj))
					for key, value := range customParams {
						if key == "thinking" {
							continue
						}
						opts = append(opts, option.WithJSONSet(key, value))
					}
				} else if _, hasType := customParams["type"]; hasType {
					// Format 1: flat thinking object
					opts = append(opts, option.WithJSONSet("thinking", customParams))
				} else {
					// Format 3: arbitrary key-values
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
	params := o.buildParams(model, messages, tools, thinkingMode, false)

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
			params = o.buildParams(model, messages, tools, thinkingMode, false)
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

	stream, err := o.newStreamingWithRetry(ctx, model, messages, tools, thinkingMode, opts)
	if err != nil {
		return nil, fmt.Errorf("openai stream completion: %w", err)
	}

	// 创建事件 channel
	eventChan := make(chan StreamEvent, 100)

	// 启动 goroutine 处理流式响应
	go o.processStream(ctx, stream, eventChan, startTime, messages, model, tools, thinkingMode)

	return eventChan, nil
}

func (o *OpenAILLM) newStreamingWithRetry(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string, opts []option.RequestOption) (*ssestream.Stream[openai.ChatCompletionChunk], error) {
	params := o.buildParams(model, messages, tools, thinkingMode, true)
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
			params = o.buildParams(model, messages, tools, thinkingMode, true)
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
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
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
