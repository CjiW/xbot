package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	log "xbot/logger"
)

const (
	anthropicDefaultBaseURL = "https://api.anthropic.com"
	anthropicAPIVersion     = "2023-06-01"
	anthropicMaxTokens      = 8192
)

// AnthropicLLM Anthropic Messages API 实现
type AnthropicLLM struct {
	baseURL      string
	apiKey       string
	userAgent    string
	httpClient   *http.Client
	models       []string
	defaultModel string
	maxTokens    int
}

// AnthropicConfig Anthropic 配置
type AnthropicConfig struct {
	BaseURL      string // 默认 https://api.anthropic.com
	APIKey       string
	DefaultModel string
	MaxTokens    int    // 0 = use default (8192)
	UserAgent    string // 自定义 User-Agent（留空使用默认值）
}

// 常用 Claude 模型列表（供 ListModels）
var anthropicKnownModels = []string{
	"claude-sonnet-4-20250514",
	"claude-opus-4-20250115",
	"claude-3-7-sonnet-20250219",
	"claude-3-5-haiku-20241022",
	"claude-3-5-sonnet-20241022",
	"claude-3-5-sonnet-20240620",
	"claude-3-opus-20240229",
	"claude-3-sonnet-20240229",
	"claude-3-haiku-20240307",
}

// NewAnthropicLLM 创建 Anthropic LLM 实例
func NewAnthropicLLM(cfg AnthropicConfig) *AnthropicLLM {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = anthropicDefaultBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}

	a := &AnthropicLLM{
		baseURL:   baseURL,
		apiKey:    cfg.APIKey,
		userAgent: cfg.UserAgent,
		httpClient: &http.Client{
			Timeout: 300 * time.Second,
		},
		models:       anthropicKnownModels,
		defaultModel: cfg.DefaultModel,
		maxTokens:    cfg.MaxTokens,
	}
	if a.defaultModel == "" && len(a.models) > 0 {
		a.defaultModel = a.models[0]
	}
	// Default User-Agent: masquerade as Claude Code to avoid coding-agent rate limits.
	if a.userAgent == "" {
		a.userAgent = "claude-code/1.0.26"
	}
	return a
}

// ListModels 返回可用模型列表
// Anthropic has no /v1/models API, so we only return the configured model.
// This prevents Ctrl+N from cycling through fake hardcoded model names.
func (a *AnthropicLLM) ListModels() []string {
	if a.defaultModel != "" {
		return []string{a.defaultModel}
	}
	return nil
}

// GetDefaultModel 返回默认模型
func (a *AnthropicLLM) GetDefaultModel() string {
	if a.defaultModel != "" {
		return a.defaultModel
	}
	if len(a.models) > 0 {
		return a.models[0]
	}
	return ""
}

// getMaxTokens returns the configured max output tokens, falling back to the default.
func (a *AnthropicLLM) getMaxTokens() int {
	if a.maxTokens > 0 {
		return a.maxTokens
	}
	return anthropicMaxTokens
}

// --- 请求/响应类型（Anthropic Messages API）---

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []contentBlock
}

type anthropicTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type anthropicToolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
	Effort       string `json:"effort,omitempty"` // "low" | "medium" | "high" (for adaptive mode)
}

type anthropicSystemBlock struct {
	Type         string `json:"type"` // "text"
	Text         string `json:"text"`
	CacheControl *struct {
		Type string `json:"type"` // "ephemeral"
	} `json:"cache_control,omitempty"`
}

type anthropicReq struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	System    any                `json:"system,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
	Thinking  *anthropicThinking `json:"thinking,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

type anthropicContentBlock struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// Thinking block fields
	Thinking string `json:"thinking,omitempty"`
}

type anthropicResp struct {
	ID           string                  `json:"id"`
	Type         string                  `json:"type"`
	Role         string                  `json:"role"`
	Content      []anthropicContentBlock `json:"content"`
	StopReason   string                  `json:"stop_reason"`
	StopSequence *string                 `json:"stop_sequence"`
	Usage        anthropicUsage          `json:"usage"`
	Model        string                  `json:"model"`
}

// toAnthropicMessages 将业务消息转为 Anthropic 格式（跳过 system 消息，由 buildAnthropicSystem 处理）。
// thinkingEnabled controls whether assistant messages get a thinking content block.
// Anthropic requires all assistant messages to include thinking blocks when thinking is enabled,
// even if the original reasoning content was lost (e.g. after compression).
func toAnthropicMessages(messages []ChatMessage, thinkingEnabled bool) []anthropicMessage {
	var msgs []anthropicMessage

	// anthropicThinkingBlock represents a thinking content block in assistant messages.
	type anthropicThinkingBlock struct {
		Type     string `json:"type"`
		Thinking string `json:"thinking"`
	}

	i := 0
	for i < len(messages) {
		msg := messages[i]
		switch msg.Role {
		case "system":
			// system 消息由 buildAnthropicSystem 单独处理，此处跳过
			i++
		case "user":
			msgs = append(msgs, anthropicMessage{Role: "user", Content: msg.Content})
			i++
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				blocks := make([]any, 0, 2+len(msg.ToolCalls))
				// Anthropic requires thinking block before tool_use blocks when thinking is enabled
				if thinkingEnabled {
					blocks = append(blocks, anthropicThinkingBlock{
						Type:     "thinking",
						Thinking: msg.ReasoningContent,
					})
				} else if msg.ReasoningContent != "" {
					blocks = append(blocks, anthropicThinkingBlock{
						Type:     "thinking",
						Thinking: msg.ReasoningContent,
					})
				}
				if msg.Content != "" {
					blocks = append(blocks, anthropicTextBlock{Type: "text", Text: msg.Content})
				}
				for _, tc := range msg.ToolCalls {
					input := json.RawMessage(tc.Arguments)
					if len(input) == 0 || !json.Valid(input) {
						input = json.RawMessage("{}")
					}
					blocks = append(blocks, anthropicToolUseBlock{
						Type:  "tool_use",
						ID:    tc.ID,
						Name:  tc.Name,
						Input: input,
					})
				}
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: blocks})
			} else if thinkingEnabled || msg.ReasoningContent != "" {
				// Text-only assistant message: need blocks for thinking + text
				blocks := make([]any, 0, 2)
				blocks = append(blocks, anthropicThinkingBlock{
					Type:     "thinking",
					Thinking: msg.ReasoningContent,
				})
				if msg.Content != "" {
					blocks = append(blocks, anthropicTextBlock{Type: "text", Text: msg.Content})
				}
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: blocks})
			} else {
				msgs = append(msgs, anthropicMessage{Role: "assistant", Content: msg.Content})
			}
			i++
		case "tool":
			// 连续多条 tool 消息合并为一条 user 消息，content 为多个 tool_result
			var results []anthropicToolResultBlock
			for i < len(messages) && messages[i].Role == "tool" {
				t := messages[i]
				results = append(results, anthropicToolResultBlock{
					Type:      "tool_result",
					ToolUseID: t.ToolCallID,
					Content:   t.Content,
				})
				i++
			}
			blocks := make([]any, 0, len(results))
			for _, r := range results {
				blocks = append(blocks, r)
			}
			msgs = append(msgs, anthropicMessage{Role: "user", Content: blocks})
		default:
			i++
		}
	}

	return msgs
}

// toAnthropicTools 将工具定义转为 Anthropic tools（input_schema 为 JSON Schema）
func toAnthropicTools(tools []ToolDefinition) []anthropicTool {
	out := make([]anthropicTool, 0, len(tools))
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
		out = append(out, anthropicTool{
			Name:        tool.Name(),
			Description: tool.Description(),
			InputSchema: map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   required,
			},
		})
	}
	return out
}

// buildAnthropicSystem 根据 ChatMessage 中的 system 消息构建 Anthropic system 字段。
// - 无 system 消息时返回空字符串（向后兼容）
// - 单条无缓存 system 时返回 string（向后兼容，避免不必要的数组序列化）
// - 有 CacheHint="static" 时返回带 cache_control 的 blocks 数组
// - 混合 static 和非 static 时返回 blocks 数组
func buildAnthropicSystem(messages []ChatMessage) any {
	var blocks []anthropicSystemBlock
	for _, msg := range messages {
		if msg.Role != "system" {
			continue
		}
		if msg.CacheHint == "static" {
			blocks = append(blocks, anthropicSystemBlock{
				Type: "text",
				Text: msg.Content,
				CacheControl: &struct {
					Type string `json:"type"`
				}{Type: "ephemeral"},
			})
		} else {
			blocks = append(blocks, anthropicSystemBlock{
				Type: "text",
				Text: msg.Content,
			})
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	if len(blocks) == 1 && blocks[0].CacheControl == nil {
		return blocks[0].Text
	}
	return blocks
}

func (a *AnthropicLLM) setHeaders(req *http.Request) {
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)
	req.Header.Set("Content-Type", "application/json")
	if a.userAgent != "" {
		req.Header.Set("User-Agent", a.userAgent)
	}
}

// parseAnthropicThinking 解析 thinkingMode 参数为 Anthropic thinking 结构
// 支持格式:
//   - "enabled" -> {type: "enabled"}
//   - "adaptive" -> {type: "adaptive"}
//   - "disabled" -> nil (不发送 thinking 参数)
//   - JSON 格式: {"type": "enabled", "budget_tokens": 10000} 或 {"type": "adaptive", "effort": "high"}
func parseAnthropicThinking(thinkingMode string) *anthropicThinking {
	if thinkingMode == "" || thinkingMode == "disabled" {
		return nil
	}

	// 简单关键字
	switch thinkingMode {
	case "enabled":
		return &anthropicThinking{Type: "enabled"}
	case "adaptive":
		return &anthropicThinking{Type: "adaptive", Effort: "high"}
	}

	// JSON 格式解析
	var thinking anthropicThinking
	if err := json.Unmarshal([]byte(thinkingMode), &thinking); err == nil {
		if thinking.Type != "" {
			return &thinking
		}
	}

	// 无法解析，默认启用
	return &anthropicThinking{Type: "enabled"}
}

// Generate 非流式生成
func (a *AnthropicLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	if model == "" {
		model = a.GetDefaultModel()
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"provider":    "anthropic",
		"model":       model,
		"stream":      false,
		"msg_count":   len(messages),
		"tools_count": len(tools),
	}).Debug("[LLM] Starting non-stream request")

	anthropicMsgs := toAnthropicMessages(messages, thinkingMode != "" && thinkingMode != "disabled")
	body := anthropicReq{
		Model:     model,
		MaxTokens: a.getMaxTokens(),
		Messages:  anthropicMsgs,
		System:    buildAnthropicSystem(messages),
		Stream:    false,
	}
	if len(tools) > 0 {
		body.Tools = toAnthropicTools(tools)
	}
	body.Thinking = parseAnthropicThinking(thinkingMode)

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	url := a.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	a.setHeaders(httpReq)

	startTime := time.Now()
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		log.Ctx(ctx).WithError(err).WithField("provider", "anthropic").Error("[LLM] Request failed")
		return nil, fmt.Errorf("anthropic API request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		log.Ctx(ctx).WithFields(log.Fields{
			"provider":    "anthropic",
			"status_code": resp.StatusCode,
			"body":        string(bodyBytes),
		}).Error("[LLM] API error")
		return nil, fmt.Errorf("anthropic API error: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	var apiResp anthropicResp
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	// Drain remaining body to allow connection reuse
	io.Copy(io.Discard, resp.Body)

	out := &LLMResponse{
		Usage: TokenUsage{
			PromptTokens:        int64(apiResp.Usage.InputTokens),
			CompletionTokens:    int64(apiResp.Usage.OutputTokens),
			TotalTokens:         int64(apiResp.Usage.InputTokens + apiResp.Usage.OutputTokens),
			CacheHitTokens:      int64(apiResp.Usage.CacheReadInputTokens),
			CacheCreationTokens: int64(apiResp.Usage.CacheCreationInputTokens),
		},
		FinishReason: mapStopReason(apiResp.StopReason),
	}

	var textParts []string
	var reasoningParts []string
	for _, block := range apiResp.Content {
		switch block.Type {
		case "text":
			textParts = append(textParts, block.Text)
		case "thinking":
			// Anthropic extended thinking block
			reasoningParts = append(reasoningParts, block.Thinking)
		case "tool_use":
			out.ToolCalls = append(out.ToolCalls, ToolCall{
				ID:        block.ID,
				Name:      block.Name,
				Arguments: string(block.Input),
			})
		}
	}
	out.Content = strings.Join(textParts, "")
	if len(reasoningParts) > 0 {
		out.ReasoningContent = strings.Join(reasoningParts, "\n")
	}

	logFields := log.Fields{
		"provider":      "anthropic",
		"content_len":   len(out.Content),
		"tool_calls":    len(out.ToolCalls),
		"finish_reason": out.FinishReason,
		"duration":      time.Since(startTime).String(),
	}
	if apiResp.Usage.CacheReadInputTokens > 0 {
		logFields["cache_read_tokens"] = apiResp.Usage.CacheReadInputTokens
	}
	if apiResp.Usage.CacheCreationInputTokens > 0 {
		logFields["cache_creation_tokens"] = apiResp.Usage.CacheCreationInputTokens
	}
	log.Ctx(ctx).WithFields(logFields).Debug("[LLM] Non-stream response")

	return out, nil
}

func mapStopReason(s string) FinishReason {
	switch s {
	case "end_turn":
		return FinishReasonStop
	case "max_tokens":
		return FinishReasonLength
	case "tool_use":
		return FinishReasonToolCalls
	default:
		return FinishReason(s)
	}
}

// GenerateStream 流式生成
func (a *AnthropicLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	if model == "" {
		model = a.GetDefaultModel()
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"provider":    "anthropic",
		"model":       model,
		"stream":      true,
		"msg_count":   len(messages),
		"tools_count": len(tools),
	}).Debug("[LLM] Starting stream request")

	anthropicMsgs := toAnthropicMessages(messages, thinkingMode != "" && thinkingMode != "disabled")
	body := anthropicReq{
		Model:     model,
		MaxTokens: a.getMaxTokens(),
		Messages:  anthropicMsgs,
		System:    buildAnthropicSystem(messages),
		Stream:    true,
	}
	if len(tools) > 0 {
		body.Tools = toAnthropicTools(tools)
	}
	body.Thinking = parseAnthropicThinking(thinkingMode)

	reqBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: marshal request: %w", err)
	}

	url := a.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("anthropic: create request: %w", err)
	}
	a.setHeaders(httpReq)

	startTime := time.Now()
	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		log.Ctx(ctx).WithError(err).WithField("provider", "anthropic").Error("[LLM] Request failed")
		return nil, fmt.Errorf("anthropic streaming API request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		log.Ctx(ctx).WithFields(log.Fields{
			"provider":    "anthropic",
			"status_code": resp.StatusCode,
			"body":        string(bodyBytes),
		}).Error("[LLM] API error")
		return nil, fmt.Errorf("anthropic API error: status=%d, body=%s", resp.StatusCode, string(bodyBytes))
	}

	eventChan := make(chan StreamEvent, 100)
	go a.processStream(ctx, resp, eventChan, startTime)
	return eventChan, nil
}

// Anthropic SSE 事件类型
type anthropicStreamEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index,omitempty"`
	Message      *anthropicResp  `json:"message,omitempty"`
	Delta        json.RawMessage `json:"delta,omitempty"`
	ContentBlock *struct {
		Type  string          `json:"type"`
		Text  string          `json:"text,omitempty"`
		ID    string          `json:"id,omitempty"`
		Name  string          `json:"name,omitempty"`
		Input json.RawMessage `json:"input,omitempty"`
	} `json:"content_block,omitempty"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

func (a *AnthropicLLM) processStream(ctx context.Context, resp *http.Response, eventChan chan<- StreamEvent, startTime time.Time) {
	defer close(eventChan)
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var currentIndex int
	toolCallsByIndex := make(map[int]*ToolCall)
	var lastUsage *TokenUsage
	lastFinishReason := FinishReasonStop
	doneSent := false

	for {
		select {
		case <-ctx.Done():
			eventChan <- StreamEvent{Type: EventError, Error: ctx.Err().Error()}
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if !doneSent {
					eventChan <- StreamEvent{Type: EventDone}
				}
				return
			}
			eventChan <- StreamEvent{Type: EventError, Error: fmt.Sprintf("read stream: %v", err)}
			return
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimPrefix(line, "data:")
		data = strings.TrimPrefix(data, " ") // tolerate both "data:" and "data: "
		if data == "[DONE]" || data == "" {
			if lastUsage != nil {
				eventChan <- StreamEvent{Type: EventUsage, Usage: lastUsage}
			}
			if !doneSent {
				eventChan <- StreamEvent{Type: EventDone}
			}
			return
		}

		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}

		switch ev.Type {
		case "message_start":
			// 可选：从 message.content 解析已有块（流式时通常为空）
			if ev.Message != nil {
				for _, block := range ev.Message.Content {
					if block.Type == "text" && block.Text != "" {
						eventChan <- StreamEvent{Type: EventContent, Content: block.Text}
					}
					if block.Type == "tool_use" {
						tc := &ToolCall{ID: block.ID, Name: block.Name, Arguments: string(block.Input)}
						eventChan <- StreamEvent{
							Type:     EventToolCall,
							ToolCall: &ToolCallDelta{Index: currentIndex, ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments},
						}
						toolCallsByIndex[currentIndex] = tc
						currentIndex++
					}
				}
				if ev.Message.Usage.InputTokens+ev.Message.Usage.OutputTokens > 0 {
					if lastUsage == nil {
						lastUsage = &TokenUsage{}
					}
					lastUsage.PromptTokens = int64(ev.Message.Usage.InputTokens)
					lastUsage.CompletionTokens = int64(ev.Message.Usage.OutputTokens)
					lastUsage.TotalTokens = lastUsage.PromptTokens + lastUsage.CompletionTokens
					lastUsage.CacheHitTokens = int64(ev.Message.Usage.CacheReadInputTokens)
					lastUsage.CacheCreationTokens = int64(ev.Message.Usage.CacheCreationInputTokens)
				}
			}

		case "content_block_start":
			if ev.ContentBlock != nil {
				switch ev.ContentBlock.Type {
				case "tool_use":
					tc := &ToolCall{
						ID:        ev.ContentBlock.ID,
						Name:      ev.ContentBlock.Name,
						Arguments: "", // input_json_delta events will build up the arguments
					}
					toolCallsByIndex[ev.Index] = tc
					eventChan <- StreamEvent{
						Type: EventToolCall,
						ToolCall: &ToolCallDelta{
							Index:     ev.Index,
							ID:        tc.ID,
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					}
				case "thinking":
					// Anthropic extended thinking block - no action needed here
					// The thinking content will be delivered via thinking_delta events
				}
			}

		case "content_block_delta":
			if len(ev.Delta) == 0 {
				continue
			}
			var delta struct {
				Type        string `json:"type"`
				Text        string `json:"text,omitempty"`
				Thinking    string `json:"thinking,omitempty"`
				PartialJSON string `json:"partial_json,omitempty"`
			}
			if err := json.Unmarshal(ev.Delta, &delta); err != nil {
				continue
			}
			if delta.Type == "text_delta" && delta.Text != "" {
				eventChan <- StreamEvent{Type: EventContent, Content: delta.Text}
			}
			if delta.Type == "thinking_delta" && delta.Thinking != "" {
				// Anthropic extended thinking delta
				eventChan <- StreamEvent{Type: EventReasoningContent, ReasoningContent: delta.Thinking}
			}
			if delta.Type == "input_json_delta" && delta.PartialJSON != "" {
				if tc, ok := toolCallsByIndex[ev.Index]; ok {
					tc.Arguments += delta.PartialJSON
					eventChan <- StreamEvent{
						Type:     EventToolCall,
						ToolCall: &ToolCallDelta{Index: ev.Index, Arguments: delta.PartialJSON},
					}
				}
			}

		case "message_delta":
			if ev.Usage != nil {
				if lastUsage == nil {
					lastUsage = &TokenUsage{}
				}
				if ev.Usage.InputTokens > 0 {
					lastUsage.PromptTokens = int64(ev.Usage.InputTokens)
				}
				if ev.Usage.OutputTokens > 0 {
					lastUsage.CompletionTokens = int64(ev.Usage.OutputTokens)
				}
				lastUsage.TotalTokens = lastUsage.PromptTokens + lastUsage.CompletionTokens
				eventChan <- StreamEvent{Type: EventUsage, Usage: lastUsage}
			}
			if len(ev.Delta) > 0 {
				var delta struct {
					StopReason string `json:"stop_reason"`
				}
				if err := json.Unmarshal(ev.Delta, &delta); err == nil && delta.StopReason != "" {
					lastFinishReason = mapStopReason(delta.StopReason)
				}
			}

		case "message_stop":
			doneSent = true
			eventChan <- StreamEvent{Type: EventDone, FinishReason: lastFinishReason}
		}
	}
}
