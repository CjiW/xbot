package tools

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
)

// ContextEditHandler 是 engine 层实现的消息编辑回调接口。
type ContextEditHandler interface {
	HandleRequest(action string, params map[string]any) (string, error)
}

// ContextEditTool 允许 Agent 精确编辑上下文中的历史消息。
// 这是上下文管理的关键工具——不同于压缩（LLM 摘要），这是精确的手术式编辑。
//
// 特殊设计：这个工具通过 Handler 回调在 engine 层执行，因为它需要直接修改
// messages slice。标准 ToolResult 机制无法修改已有消息。
type ContextEditTool struct {
	Handler ContextEditHandler
}

func (t *ContextEditTool) Name() string { return "context_edit" }

func (t *ContextEditTool) Description() string {
	return `Precisely edit, truncate, or delete content in conversation history messages.
This is a surgical context management tool — unlike compression (which summarizes and loses info),
context_edit lets you precisely remove or modify specific content to reclaim context space.

Actions:
- "list": List conversation grouped by turns (user message + associated iterations/tools). No other params needed.
- "delete_turn": Delete an entire conversation turn (user msg + all iterations + all tool results). Most efficient for reclaiming context.
- "delete": Replace a single message's content with a placeholder (frees tokens from that message)
- "truncate": Keep only the first N characters of a message's content
- "replace": Find and replace specific text within a message (supports regex: prefix with "regex:")

Safety rules:
- Cannot edit system messages
- Cannot delete the last (current) turn
- Cannot edit the last 3 messages (protected to prevent losing current context)
- Always provide a reason for the edit

Use "list" first to see conversation turns and their sizes. Prefer "delete_turn" for bulk cleanup.`
}

func (t *ContextEditTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "action", Type: "string", Description: `Action: "list" (show turns), "delete_turn" (delete entire turn), "delete", "truncate", or "replace"`, Required: true},
		{Name: "turn_idx", Type: "integer", Description: "Turn index for \"delete_turn\" action (from \"list\" output, 0-based)", Required: false},
		{Name: "message_idx", Type: "integer", Description: "Message index for delete/truncate/replace actions (from \"list\" output, 0-based). Not needed for \"list\" or \"delete_turn\".", Required: false},
		{Name: "max_chars", Type: "integer", Description: "For \"truncate\" action: number of characters to keep (default: 200)", Required: false},
		{Name: "old_text", Type: "string", Description: "For \"replace\" action: text to find. Prefix with \"regex:\" for regex matching.", Required: false},
		{Name: "new_text", Type: "string", Description: "For \"replace\" action: replacement text (empty = delete matched text)", Required: false},
		{Name: "reason", Type: "string", Description: "Brief explanation of why this edit is needed", Required: false},
	}
}

func (t *ContextEditTool) Execute(ctx *ToolContext, args string) (*ToolResult, error) {
	if t.Handler == nil {
		return nil, fmt.Errorf("context edit handler not available")
	}

	var raw map[string]any
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return nil, err
	}

	action, ok := raw["action"].(string)
	if !ok || action == "" {
		return nil, fmt.Errorf("action is required")
	}

	result, err := t.Handler.HandleRequest(action, raw)
	if err != nil {
		return nil, err
	}

	return NewResult(result), nil
}
