package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	log "xbot/logger"
)

// BaseLLM 提供 LLM 实现的公共字段和方法。
// 各 provider（Anthropic、CodeBuddy、OpenAI）可嵌入此结构体以复用通用逻辑。
type BaseLLM struct {
	models       []string
	defaultModel string
	httpClient   *http.Client
}

// ListModels 返回可用模型列表（返回副本，避免外部修改）。
func (b *BaseLLM) ListModels() []string {
	result := make([]string, len(b.models))
	copy(result, b.models)
	return result
}

// GetDefaultModel 返回默认模型。
func (b *BaseLLM) GetDefaultModel() string {
	if b.defaultModel != "" {
		return b.defaultModel
	}
	if len(b.models) > 0 {
		return b.models[0]
	}
	return ""
}

// resolveModel 如果 model 为空则返回默认模型。
func (b *BaseLLM) resolveModel(model string) string {
	if model == "" {
		return b.GetDefaultModel()
	}
	return model
}

// handleHTTPError 处理非 200 HTTP 响应，读取 body 并记录日志后返回格式化错误。
func (b *BaseLLM) handleHTTPError(ctx context.Context, provider string, resp *http.Response) error {
	bodyBytes, _ := io.ReadAll(resp.Body)
	log.Ctx(ctx).WithFields(log.Fields{
		"provider":    provider,
		"status_code": resp.StatusCode,
		"body":        string(bodyBytes),
	}).Error("[LLM] API error")
	return fmt.Errorf("%s API error: status=%d, body=%s", provider, resp.StatusCode, string(bodyBytes))
}

// withOptionalTimeout 如果 timeout > 0，则基于 parent 创建带超时的 context；
// 否则返回 parent 本身和一个空操作的 cancel 函数。
func withOptionalTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(parent, timeout)
	}
	return parent, func() {}
}

// buildToolJSONSchema 从 ToolDefinition 的参数列表构建标准 JSON Schema（object 类型）。
// 返回的 map 包含 type、properties、required 字段。
func buildToolJSONSchema(tool ToolDefinition) map[string]any {
	properties := make(map[string]any)
	required := make([]string, 0)
	for _, p := range tool.Parameters() {
		prop := map[string]any{
			"type":        p.Type,
			"description": p.Description,
		}
		if p.Items != nil {
			prop["items"] = map[string]string{"type": p.Items.Type}
		}
		properties[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

// MapFinishReason 将各 provider 的原始 finish reason 字符串映射为统一的 FinishReason。
func MapFinishReason(raw string) FinishReason {
	switch raw {
	case "stop", "end_turn":
		return FinishReasonStop
	case "length", "max_tokens":
		return FinishReasonLength
	case "tool_calls", "tool_use":
		return FinishReasonToolCalls
	case "content_filter":
		return FinishReasonContentFilter
	default:
		return FinishReason(raw)
	}
}

// AggregateStreamEvents 将流式事件 channel 聚合为完整的 LLMResponse。
// 适用于通过流式接口实现非流式调用的场景。
func AggregateStreamEvents(eventChan <-chan StreamEvent) (*LLMResponse, error) {
	resp := &LLMResponse{
		Usage: TokenUsage{},
	}

	var contentBuilder, reasoningBuilder stringBuilder
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

	return resp, nil
}

// stringBuilder 是 strings.Builder 的轻量别名，避免在 base.go 中额外 import strings。
type stringBuilder struct {
	buf []byte
}

func (sb *stringBuilder) WriteString(s string) {
	sb.buf = append(sb.buf, s...)
}

func (sb *stringBuilder) String() string {
	return string(sb.buf)
}
