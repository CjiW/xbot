package llm

import "context"

// LLM 接口，使用业务定义的消息和响应类型
type LLM interface {
	// Generate 生成 LLM 响应
	// model: 模型名称
	// messages: 消息列表
	// tools: 工具定义列表
	Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition) (*LLMResponse, error)

	// ListModels 获取可用模型列表
	ListModels() []string
}

// StreamingLLM 流式 LLM 接口
type StreamingLLM interface {
	LLM
	// GenerateStream 流式生成，返回事件 channel
	// model: 模型名称
	// messages: 消息列表
	// tools: 工具定义列表
	// channel 会在完成或出错时关闭
	GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition) (<-chan StreamEvent, error)
}
