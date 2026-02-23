package llm

import (
	"context"
	"strings"
	"time"
)

// MockLLM Mock LLM 实现，用于测试
type MockLLM struct {
	ChunkSize     int           // 流式输出每个片段的字符数，默认 5
	ChunkInterval time.Duration // 流式输出每个片段的间隔，默认 50ms
}

// NewMockLLM 创建 MockLLM
func NewMockLLM() *MockLLM {
	return &MockLLM{
		ChunkSize:     5,
		ChunkInterval: 50 * time.Millisecond,
	}
}

// Generate 非流式：拼接所有消息内容作为响应，token 消耗为内容长度
func (m *MockLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition) (*LLMResponse, error) {
	var sb strings.Builder
	for _, msg := range messages {
		if msg.Content != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("[" + msg.Role + "] " + msg.Content)
		}
	}

	content := sb.String()
	contentLen := int64(len([]rune(content)))

	return &LLMResponse{
		Content:      content,
		FinishReason: FinishReasonStop,
		Usage: TokenUsage{
			PromptTokens:     contentLen,
			CompletionTokens: contentLen,
			TotalTokens:      contentLen * 2,
		},
	}, nil
}

// ListModels 返回 mock 模型列表
func (m *MockLLM) ListModels() []string {
	return []string{"mock"}
}

// GenerateStream 流式输出：按 ChunkSize 和 ChunkInterval 分片发送所有消息内容
func (m *MockLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition) (<-chan StreamEvent, error) {
	var sb strings.Builder
	for _, msg := range messages {
		if msg.Content != "" && msg.Role != "tool" && msg.Role != "system" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("[" + msg.Role + "] " + msg.Content)
		}
	}

	content := sb.String()
	runes := []rune(content)
	contentLen := int64(len(runes))

	chunkSize := m.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 5
	}
	interval := m.ChunkInterval
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}

	ch := make(chan StreamEvent, 10)

	go func() {
		defer close(ch)

		for i := 0; i < len(runes); i += chunkSize {
			select {
			case <-ctx.Done():
				ch <- StreamEvent{Type: EventError, Error: ctx.Err().Error()}
				return
			default:
			}

			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			chunk := string(runes[i:end])

			ch <- StreamEvent{Type: EventContent, Content: chunk}

			if end < len(runes) {
				time.Sleep(interval)
			}
		}

		ch <- StreamEvent{
			Type: EventUsage,
			Usage: &TokenUsage{
				PromptTokens:     contentLen,
				CompletionTokens: contentLen,
				TotalTokens:      contentLen * 2,
			},
		}

		ch <- StreamEvent{
			Type:         EventDone,
			FinishReason: FinishReasonStop,
		}
	}()

	return ch, nil
}
