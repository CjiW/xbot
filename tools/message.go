package tools

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
)

// MessageTool 主动发送消息工具
type MessageTool struct{}

func (t *MessageTool) Name() string { return "Message" }

func (t *MessageTool) Description() string {
	return `Send a message to the user proactively. Use this when you need to deliver intermediate results, progress updates, or split a long response into multiple messages. The message is sent immediately to the current channel without waiting for the final response.`
}

func (t *MessageTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "content", Type: "string", Description: "Message content to send", Required: true},
	}
}

func (t *MessageTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if params.Content == "" {
		return nil, fmt.Errorf("content is required")
	}
	if ctx == nil || ctx.SendFunc == nil {
		return nil, fmt.Errorf("message sending not available in this context")
	}
	if ctx.Channel == "" || ctx.ChatID == "" {
		return nil, fmt.Errorf("no active channel/chat to send to")
	}

	ctx.SendFunc(ctx.Channel, ctx.ChatID, params.Content)
	return NewResult("Message sent."), nil
}
