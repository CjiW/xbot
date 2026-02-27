package tools

import (
	"encoding/json"
	"fmt"
	"xbot/llm"
)

// NotifyTool 向用户发送中间通知，用于长时间任务的进度汇报
type NotifyTool struct{}

func (t *NotifyTool) Name() string {
	return "Notify"
}

func (t *NotifyTool) Description() string {
	return `Send a progress notification to the user during long-running tasks.
Use this tool to keep the user informed about intermediate progress without waiting for the final response.
Guidelines:
  - Use when a task is expected to take more than 30 seconds
  - Use between major steps of a multi-step task
  - Do NOT overuse — avoid sending more than one notification per step
Parameters (JSON):
  - message: string, the notification content to send to the user
Example: {"message": "已完成第1篇论文的下载，正在处理第2篇..."}`
}

func (t *NotifyTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "message", Type: "string", Description: "The notification content to send to the user", Required: true},
	}
}

func (t *NotifyTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Message == "" {
		return nil, fmt.Errorf("message is required")
	}

	if toolCtx.SendFunc == nil {
		return nil, fmt.Errorf("send function not available")
	}

	// 直接通过 SendFunc 发送消息给用户，不中断 Agent 循环
	toolCtx.SendFunc(toolCtx.Channel, toolCtx.ChatID, params.Message)

	return NewResult("Notification sent to user."), nil
}
