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
	"xbot/logger"

	"github.com/google/uuid"
)

// CodeBuddyLLM CodeBuddy LLM 实现
type CodeBuddyLLM struct {
	baseURL      string
	token        string
	httpClient   *http.Client
	headers      map[string]string
	userID       string   // X-User-Id
	enterpriseID string   // X-Enterprise-Id / X-Tenant-Id
	domain       string   // X-Domain
	models       []string // 可用模型列表
	defaultModel string   // 默认模型
}

// CodeBuddyConfig CodeBuddy 配置
type CodeBuddyConfig struct {
	BaseURL      string
	Token        string
	Headers      map[string]string // 额外的自定义请求头
	UserID       string            // X-User-Id
	EnterpriseID string            // X-Enterprise-Id / X-Tenant-Id
	Domain       string            // X-Domain
	DefaultModel string            // 默认模型（API 获取失败时的回退模型）
}

// NewCodeBuddyLLM 创建 CodeBuddy LLM 实例
func NewCodeBuddyLLM(cfg CodeBuddyConfig) *CodeBuddyLLM {
	c := &CodeBuddyLLM{
		baseURL: cfg.BaseURL,
		token:   cfg.Token,
		httpClient: &http.Client{
			Timeout: 300 * time.Second, // 流式响应需要更长超时
		},
		headers:      cfg.Headers,
		userID:       cfg.UserID,
		enterpriseID: cfg.EnterpriseID,
		domain:       cfg.Domain,
		models:       nil,
		defaultModel: cfg.DefaultModel,
	}

	// 尝试从 API 加载模型列表
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.LoadModelsFromAPI(ctx); err != nil {
		logger.WithError(err).Warn("[LLM] Failed to load models from CodeBuddy API")
		// API 获取失败，使用默认模型作为回退
		if cfg.DefaultModel != "" {
			c.models = []string{cfg.DefaultModel}
			logger.WithField("fallback_model", cfg.DefaultModel).Info("[LLM] Using fallback model from config")
		}
	}

	return c
}

// ListModels 获取可用模型列表
func (c *CodeBuddyLLM) ListModels() []string {
	result := make([]string, len(c.models))
	copy(result, c.models)
	return result
}

// GetDefaultModel 获取默认模型
func (c *CodeBuddyLLM) GetDefaultModel() string {
	if c.defaultModel != "" {
		return c.defaultModel
	}
	if len(c.models) > 0 {
		return c.models[0]
	}
	return ""
}

// CodeBuddy 请求/响应类型定义

// cbRequest CodeBuddy 请求格式
type cbRequest struct {
	Model       string      `json:"model"`
	Messages    []cbMessage `json:"messages"`
	Tools       []cbTool    `json:"tools,omitempty"`
	ToolChoice  string      `json:"tool_choice,omitempty"`
	Stream      bool        `json:"stream"`
	Temperature float64     `json:"temperature,omitempty"`
	MaxTokens   int32       `json:"max_tokens,omitempty"`
}

// cbMessage CodeBuddy 消息格式
type cbMessage struct {
	Role       string       `json:"role"`
	Content    any          `json:"content,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
	ToolCalls  []cbToolCall `json:"tool_calls,omitempty"`
}

// cbTool CodeBuddy 工具定义
type cbTool struct {
	Type     string     `json:"type"`
	Function cbFunction `json:"function"`
}

// cbFunction CodeBuddy 函数定义
type cbFunction struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Parameters  *cbParameters `json:"parameters,omitempty"`
}

// cbParameters CodeBuddy 参数定义
type cbParameters struct {
	Type       string                 `json:"type"`
	Properties map[string]*cbProperty `json:"properties"`
	Required   []string               `json:"required"`
}

// cbProperty CodeBuddy 属性定义
type cbProperty struct {
	Description string `json:"description"`
	Type        string `json:"type"`
}

// cbToolCall CodeBuddy 工具调用
type cbToolCall struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Function cbFunctionCall `json:"function"`
}

// cbFunctionCall CodeBuddy 函数调用
type cbFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// cbUsage CodeBuddy token 使用统计
type cbUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// cbStreamResponse CodeBuddy 流式响应格式
type cbStreamResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []cbStreamChoice `json:"choices"`
	Usage   *cbUsage         `json:"usage,omitempty"` // 最后一个 chunk 可能包含 usage
}

// cbStreamChoice CodeBuddy 流式选择项
type cbStreamChoice struct {
	Index        int           `json:"index"`
	Delta        cbStreamDelta `json:"delta"`
	FinishReason string        `json:"finish_reason,omitempty"`
}

// cbStreamDelta CodeBuddy 流式增量
type cbStreamDelta struct {
	Role      string             `json:"role,omitempty"`
	Content   string             `json:"content,omitempty"`
	ToolCalls []cbStreamToolCall `json:"tool_calls,omitempty"`
}

// cbStreamToolCall CodeBuddy 流式工具调用
type cbStreamToolCall struct {
	Index    int                  `json:"index"`
	ID       string               `json:"id,omitempty"`
	Type     string               `json:"type,omitempty"`
	Function cbStreamFunctionCall `json:"function,omitempty"`
}

// cbStreamFunctionCall CodeBuddy 流式函数调用
type cbStreamFunctionCall struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// toCodeBuddyMessages 将业务消息转换为 CodeBuddy 消息格式
func toCodeBuddyMessages(messages []ChatMessage) []cbMessage {
	result := make([]cbMessage, 0, len(messages))
	for _, msg := range messages {
		cbMsg := cbMessage{
			Role: msg.Role,
		}

		switch msg.Role {
		case "system", "user":
			cbMsg.Content = msg.Content
		case "assistant":
			if len(msg.ToolCalls) > 0 {
				cbMsg.Content = msg.Content
				cbMsg.ToolCalls = make([]cbToolCall, 0, len(msg.ToolCalls))
				for _, tc := range msg.ToolCalls {
					cbMsg.ToolCalls = append(cbMsg.ToolCalls, cbToolCall{
						ID:   tc.ID,
						Type: "function",
						Function: cbFunctionCall{
							Name:      tc.Name,
							Arguments: tc.Arguments,
						},
					})
				}
			} else {
				cbMsg.Content = msg.Content
			}
		case "tool":
			cbMsg.Content = msg.Content
			cbMsg.ToolCallID = msg.ToolCallID
		}

		result = append(result, cbMsg)
	}
	return result
}

// toCodeBuddyTools 将工具定义转换为 CodeBuddy 格式
func toCodeBuddyTools(tools []ToolDefinition) []cbTool {
	result := make([]cbTool, 0, len(tools))
	for _, tool := range tools {
		properties := make(map[string]*cbProperty)
		required := make([]string, 0)

		for _, p := range tool.Parameters() {
			properties[p.Name] = &cbProperty{
				Type:        p.Type,
				Description: p.Description,
			}
			if p.Required {
				required = append(required, p.Name)
			}
		}

		result = append(result, cbTool{
			Type: "function",
			Function: cbFunction{
				Name:        tool.Name(),
				Description: tool.Description(),
				Parameters: &cbParameters{
					Type:       "object",
					Properties: properties,
					Required:   required,
				},
			},
		})
	}
	return result
}

// setCommonHeaders 设置通用请求头
func (c *CodeBuddyLLM) setCommonHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-Agent-Intent", "craft")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("X-IDE-Type", "CodeBuddyIDE")
	req.Header.Set("X-IDE-Name", "CodeBuddyIDE")
	req.Header.Set("X-IDE-Version", "4.4.1")
	req.Header.Set("X-Product-Version", "4.4.1")
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("X-Env-ID", "production")
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Host", "copilot.tencent.com")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", "CodeBuddyIDE/1.100.0 CodeBuddy/3.10.0")
	convId := strings.ReplaceAll(uuid.New().String(), "-", "")
	req.Header.Set("X-Conversation-Id", convId)
	req.Header.Set("X-Conversation-Request-Id", convId)
	req.Header.Set("X-Conversation-Message-Id", convId)
	req.Header.Set("X-Request-Id", convId)
	req.Header.Set("X-Request-Trace-Id", convId)

	if c.userID != "" {
		req.Header.Set("X-User-Id", c.userID)
	}
	if c.enterpriseID != "" {
		req.Header.Set("X-Enterprise-Id", c.enterpriseID)
		req.Header.Set("X-Tenant-Id", c.enterpriseID)
	}
	if c.domain != "" {
		req.Header.Set("X-Domain", c.domain)
	}
}

// buildRequest 构建 HTTP 请求
func (c *CodeBuddyLLM) buildRequest(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, stream bool) (*http.Request, error) {
	req := cbRequest{
		Model:    model,
		Messages: toCodeBuddyMessages(messages),
		Stream:   stream,
	}

	if len(tools) > 0 {
		req.Tools = toCodeBuddyTools(tools)
		req.ToolChoice = "auto"
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	logger.Debugf("CodeBuddy request: %s", string(reqBody))

	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	c.setCommonHeaders(httpReq)

	logger.Debugf("[LLM] Request URL: %s", httpReq.URL.String())
	for k, v := range httpReq.Header {
		logger.Debugf("[LLM] Header: %s = %s", k, v)
	}

	return httpReq, nil
}

// GenerateStream 流式生成 LLM 响应
func (c *CodeBuddyLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	// 如果未指定模型，使用默认模型
	if model == "" {
		model = c.GetDefaultModel()
	}

	logger.Ctx(ctx).WithFields(logger.Fields{
		"provider":      "codebuddy",
		"model":         model,
		"stream":        true,
		"msg_count":     len(messages),
		"tools_count":   len(tools),
		"thinking_mode": thinkingMode,
	}).Info("[LLM] Starting stream request")

	// 注意：CodeBuddy 不支持 thinking_mode 参数，忽略该参数
	// 如果未来支持，可以在 buildRequest 中添加相应参数

	startTime := time.Now()

	httpReq, err := c.buildRequest(ctx, model, messages, tools, true)
	if err != nil {
		logger.Ctx(ctx).WithError(err).Error("[LLM] Failed to build request")
		return nil, err
	}

	// 发送请求
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		logger.Ctx(ctx).WithFields(logger.Fields{
			"provider": "codebuddy",
			"duration": time.Since(startTime).String(),
			"error":    err.Error(),
		}).Error("[LLM] Request failed")
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	logger.Ctx(ctx).WithFields(logger.Fields{
		"provider":    "codebuddy",
		"status_code": resp.StatusCode,
		"duration":    time.Since(startTime).String(),
	}).Debug("[LLM] Got HTTP response")

	// 检查 HTTP 状态码
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		logger.Ctx(ctx).WithFields(logger.Fields{
			"provider":    "codebuddy",
			"status_code": resp.StatusCode,
			"body":        string(body),
		}).Error("[LLM] API error")
		return nil, fmt.Errorf("CodeBuddy API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	// 创建事件 channel
	eventChan := make(chan StreamEvent, 100)

	// 启动 goroutine 处理流式响应
	go c.processStreamResponse(ctx, resp, eventChan, startTime)

	return eventChan, nil
}

// processStreamResponse 处理流式响应
func (c *CodeBuddyLLM) processStreamResponse(ctx context.Context, resp *http.Response, eventChan chan<- StreamEvent, startTime time.Time) {
	defer close(eventChan)
	defer resp.Body.Close()

	l := logger.Ctx(ctx)
	reader := bufio.NewReader(resp.Body)
	chunkCount := 0
	var firstChunkTime time.Time
	var lastUsage *TokenUsage

	for {
		select {
		case <-ctx.Done():
			// Drain remaining body before returning to allow connection reuse
			io.Copy(io.Discard, resp.Body)
			eventChan <- StreamEvent{
				Type:  EventError,
				Error: ctx.Err().Error(),
			}
			return
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return
			}
			// Drain remaining body before returning
			io.Copy(io.Discard, resp.Body)
			eventChan <- StreamEvent{
				Type:  EventError,
				Error: fmt.Sprintf("read stream error: %v", err),
			}
			return
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// SSE 格式: data: {...}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		// 检查结束标记
		if data == "[DONE]" {
			fields := logger.Fields{
				"provider":       "codebuddy",
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
			eventChan <- StreamEvent{
				Type: EventDone,
			}
			return
		}

		// 解析 JSON
		var streamResp cbStreamResponse
		if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
			l.Warnf("failed to parse stream response: %v, data: %s", err, data)
			continue
		}

		// 记录第一个 chunk 时间
		chunkCount++
		if chunkCount == 1 {
			firstChunkTime = time.Now()
			l.WithFields(logger.Fields{
				"provider": "codebuddy",
				"ttft":     firstChunkTime.Sub(startTime).String(),
			}).Debug("[LLM] First chunk received")
		}

		// 处理 choices
		for _, choice := range streamResp.Choices {
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
					l.WithFields(logger.Fields{
						"provider":  "codebuddy",
						"tool_id":   tc.ID,
						"tool_name": tc.Function.Name,
						"index":     tc.Index,
					}).Debug("[LLM] Tool call started")
				}
				eventChan <- StreamEvent{
					Type: EventToolCall,
					ToolCall: &ToolCallDelta{
						Index:     tc.Index,
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					},
				}
			}

			// 处理完成原因
			if choice.FinishReason != "" {
				var finishReason FinishReason
				switch choice.FinishReason {
				case "stop":
					finishReason = FinishReasonStop
				case "length":
					finishReason = FinishReasonLength
				case "tool_calls":
					finishReason = FinishReasonToolCalls
				case "content_filter":
					finishReason = FinishReasonContentFilter
				default:
					finishReason = FinishReason(choice.FinishReason)
				}
				eventChan <- StreamEvent{
					Type:         EventDone,
					FinishReason: finishReason,
				}
			}
		}

		// 收集 usage（通常在最后一个 chunk），不单独打日志，合并到 Stream completed
		if streamResp.Usage != nil {
			lastUsage = &TokenUsage{
				PromptTokens:     streamResp.Usage.PromptTokens,
				CompletionTokens: streamResp.Usage.CompletionTokens,
				TotalTokens:      streamResp.Usage.TotalTokens,
			}
			eventChan <- StreamEvent{
				Type:  EventUsage,
				Usage: lastUsage,
			}
		}
	}
}

// Generate 生成 LLM 响应（通过聚合流式响应实现）
func (c *CodeBuddyLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	// 如果未指定模型，使用默认模型
	if model == "" {
		model = c.GetDefaultModel()
	}

	logger.Ctx(ctx).WithFields(logger.Fields{
		"provider":      "codebuddy",
		"model":         model,
		"stream":        false,
		"msg_count":     len(messages),
		"tools_count":   len(tools),
		"thinking_mode": thinkingMode,
	}).Info("[LLM] Starting non-stream request (via stream aggregation)")

	// 调用流式接口
	eventChan, err := c.GenerateStream(ctx, model, messages, tools, thinkingMode)
	if err != nil {
		return nil, err
	}

	// 聚合响应
	resp := &LLMResponse{
		Usage: TokenUsage{},
	}

	var contentBuilder strings.Builder
	var reasoningBuilder strings.Builder
	toolCallsMap := make(map[int]*ToolCall) // 按 index 聚合工具调用

	for event := range eventChan {
		switch event.Type {
		case EventContent:
			contentBuilder.WriteString(event.Content)

		case EventReasoningContent:
			reasoningBuilder.WriteString(event.ReasoningContent)

		case EventToolCall:
			if event.ToolCall != nil {
				tc, ok := toolCallsMap[event.ToolCall.Index]
				if !ok {
					tc = &ToolCall{}
					toolCallsMap[event.ToolCall.Index] = tc
				}
				if event.ToolCall.ID != "" {
					tc.ID = event.ToolCall.ID
				}
				if event.ToolCall.Name != "" {
					tc.Name = event.ToolCall.Name
				}
				tc.Arguments += event.ToolCall.Arguments
			}

		case EventUsage:
			if event.Usage != nil {
				resp.Usage = *event.Usage
			}

		case EventDone:
			if event.FinishReason != "" {
				resp.FinishReason = event.FinishReason
			}

		case EventError:
			return nil, fmt.Errorf("stream error: %s", event.Error)
		}
	}

	resp.Content = contentBuilder.String()
	resp.ReasoningContent = reasoningBuilder.String()

	// 转换工具调用 map 为 slice
	if len(toolCallsMap) > 0 {
		resp.ToolCalls = make([]ToolCall, 0, len(toolCallsMap))
		// 按 index 顺序添加
		for i := 0; i < len(toolCallsMap); i++ {
			if tc, ok := toolCallsMap[i]; ok {
				resp.ToolCalls = append(resp.ToolCalls, *tc)
			}
		}
		// 确保有工具调用时 FinishReason 正确
		if resp.FinishReason == "" || resp.FinishReason == FinishReasonStop {
			resp.FinishReason = FinishReasonToolCalls
		}
	}

	logger.Ctx(ctx).WithFields(logger.Fields{
		"provider":          "codebuddy",
		"content_len":       len(resp.Content),
		"reasoning_len":     len(resp.ReasoningContent),
		"tool_calls":        len(resp.ToolCalls),
		"finish_reason":     resp.FinishReason,
		"prompt_tokens":     resp.Usage.PromptTokens,
		"completion_tokens": resp.Usage.CompletionTokens,
		"total_tokens":      resp.Usage.TotalTokens,
	}).Info("[LLM] Non-stream response aggregated")

	return resp, nil
}

// cbConfigResponse CodeBuddy /v3/config API 响应格式
type cbConfigResponse struct {
	Code    int          `json:"code"`
	Message string       `json:"message"`
	Data    cbConfigData `json:"data"`
}

// cbConfigData config 响应中的 data 字段
type cbConfigData struct {
	Models []cbModelInfo `json:"models"`
}

// cbModelInfo 模型信息
type cbModelInfo struct {
	ID string `json:"id"`
}

// LoadModelsFromAPI 从 CodeBuddy API 加载可用模型列表
func (c *CodeBuddyLLM) LoadModelsFromAPI(ctx context.Context) error {
	configURL := "https://copilot.tencent.com/v3/config"
	traceID := uuid.New().String()

	httpReq, err := http.NewRequestWithContext(ctx, "GET", configURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create config request: %w", err)
	}

	c.setCommonHeaders(httpReq)

	logger.WithFields(logger.Fields{
		"url":      configURL,
		"trace_id": traceID,
	}).Debug("[LLM] Loading models from CodeBuddy API")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send config request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("CodeBuddy config API error: status=%d, body=%s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read config response: %w", err)
	}

	var configResp cbConfigResponse
	if err := json.Unmarshal(body, &configResp); err != nil {
		return fmt.Errorf("failed to parse config response: %w", err)
	}

	models := make([]string, 0, len(configResp.Data.Models))
	for _, model := range configResp.Data.Models {
		if model.ID != "" {
			models = append(models, model.ID)
		}
	}

	if len(models) == 0 {
		logger.Warn("[LLM] No models found from CodeBuddy API")
		return nil
	}

	c.models = models

	if c.defaultModel == "" && len(c.models) > 0 {
		c.defaultModel = c.models[0]
	}

	logger.WithFields(logger.Fields{
		"model_count":   len(c.models),
		"models":        c.models,
		"default_model": c.defaultModel,
	}).Info("[LLM] Models loaded from CodeBuddy API")

	return nil
}
