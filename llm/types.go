package llm

import "time"

// ChatMessage 业务层定义的消息类型，与具体 LLM 实现解耦
type ChatMessage struct {
	Role          string     `json:"role"` // "system", "user", "assistant", "tool"
	Content       string     `json:"content"`
	ToolCallID    string     `json:"tool_call_id,omitempty"`   // 如果是 tool 消息，记录工具调用 ID
	ToolName      string     `json:"tool_name,omitempty"`      // 如果是 tool 消息，记录工具名称
	ToolArguments string     `json:"tool_arguments,omitempty"` // 如果是 tool 消息，记录工具调用参数
	ToolCalls     []ToolCall `json:"tool_calls,omitempty"`     // 如果是 assistant 消息且有工具调用
	Detail        string     `json:"-"`                        // 工具结果详情（如 diff），不参与 LLM 上下文，仅持久化和前端展示
	Timestamp     time.Time  `json:"-"`                        // 消息时间戳，不参与 LLM 上下文
}

// NewSystemMessage 创建系统消息
func NewSystemMessage(content string) ChatMessage {
	return ChatMessage{Role: "system", Content: content, Timestamp: time.Now()}
}

// NewUserMessage 创建用户消息
func NewUserMessage(content string) ChatMessage {
	return ChatMessage{Role: "user", Content: content, Timestamp: time.Now()}
}

// NewAssistantMessage 创建助手消息
func NewAssistantMessage(content string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: content, Timestamp: time.Now()}
}

// NewToolMessage 创建工具消息
func NewToolMessage(toolName, toolCallID, arguments, content string) ChatMessage {
	return ChatMessage{
		Role:          "tool",
		Content:       content,
		ToolName:      toolName,
		ToolCallID:    toolCallID,
		ToolArguments: arguments,
		Timestamp:     time.Now(),
	}
}

// ToolCall 业务层定义的工具调用类型
type ToolCall struct {
	ID        string `json:"id"`        // 工具调用 ID，用于后续返回结果时关联
	Name      string `json:"name"`      // 工具名称
	Arguments string `json:"arguments"` // 工具参数（JSON 字符串）
}

// FinishReason LLM 结束原因
type FinishReason string

const (
	FinishReasonStop          FinishReason = "stop"           // 正常结束
	FinishReasonLength        FinishReason = "length"         // 达到最大长度
	FinishReasonToolCalls     FinishReason = "tool_calls"     // 工具调用
	FinishReasonContentFilter FinishReason = "content_filter" // 内容过滤
)

// TokenUsage token 使用统计
type TokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`     // 输入 token 数
	CompletionTokens int64 `json:"completion_tokens"` // 输出 token 数
	TotalTokens      int64 `json:"total_tokens"`      // 总 token 数
}

func (u TokenUsage) Add(u1 TokenUsage) TokenUsage {
	u.CompletionTokens += u1.CompletionTokens
	u.PromptTokens += u1.PromptTokens
	u.TotalTokens += u1.TotalTokens
	return u
}

// LLMResponse 业务层定义的 LLM 响应类型
type LLMResponse struct {
	Content      string       `json:"content"`       // 文本内容
	ToolCalls    []ToolCall   `json:"tool_calls"`    // 工具调用列表（可能为空）
	FinishReason FinishReason `json:"finish_reason"` // 结束原因
	Usage        TokenUsage   `json:"usage"`         // token 使用统计
}

// HasToolCalls 检查是否有工具调用
func (r *LLMResponse) HasToolCalls() bool {
	return r.FinishReason == FinishReasonToolCalls && len(r.ToolCalls) > 0
}

// StreamEventType 流式事件类型
type StreamEventType string

const (
	EventContent  StreamEventType = "content"   // 文本内容增量
	EventToolCall StreamEventType = "tool_call" // 工具调用增量
	EventUsage    StreamEventType = "usage"     // Token 统计
	EventDone     StreamEventType = "done"      // 完成
	EventError    StreamEventType = "error"     // 错误
)

// ToolCallDelta 工具调用增量
type ToolCallDelta struct {
	Index     int    `json:"index"`               // 工具调用索引
	ID        string `json:"id,omitempty"`        // 工具调用 ID（首次出现）
	Name      string `json:"name,omitempty"`      // 工具名称（首次出现）
	Arguments string `json:"arguments,omitempty"` // 参数增量
}

// StreamEvent 流式事件
type StreamEvent struct {
	Type         StreamEventType `json:"type"`
	Content      string          `json:"content,omitempty"`       // 文本增量
	ToolCall     *ToolCallDelta  `json:"tool_call,omitempty"`     // 工具调用增量
	Usage        *TokenUsage     `json:"usage,omitempty"`         // Token 统计
	FinishReason FinishReason    `json:"finish_reason,omitempty"` // 结束原因
	Error        string          `json:"error,omitempty"`         // 错误信息
}

// ToolParam 工具参数定义
type ToolParam struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// ToolDefinition 工具定义接口（用于 LLM 调用）
type ToolDefinition interface {
	Name() string
	Description() string
	Parameters() []ToolParam
}
