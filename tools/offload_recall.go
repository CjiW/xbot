package tools

import (
	"encoding/json"
	"fmt"

	"xbot/llm"
)

// OffloadRecallStore 是 OffloadStore 暴露给 tools 包的接口。
type OffloadRecallStore interface {
	Recall(sessionKey, id string) (string, error)
}

// OffloadRecallTool 召回已 offload 的工具结果完整内容。
type OffloadRecallTool struct {
	Store OffloadRecallStore
}

// offloadRecallParams 是 offload_recall 工具的参数。
type offloadRecallParams struct {
	ID string `json:"id"`
}

func (t *OffloadRecallTool) Name() string { return "offload_recall" }

func (t *OffloadRecallTool) Description() string {
	return `Recall the full content of a previously offloaded tool result.
Use the offload ID (from 📂 markers in tool results) to retrieve the complete data.
This is useful when you need to see the full output of a large tool result that was summarized.`
}

func (t *OffloadRecallTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "id", Type: "string", Description: "Offload ID (obtained from 📂 markers, e.g. ol_1234abcd)", Required: true},
	}
}

func (t *OffloadRecallTool) Execute(ctx *ToolContext, args string) (*ToolResult, error) {
	if t.Store == nil {
		return nil, fmt.Errorf("offload store not available")
	}

	var params offloadRecallParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}
	if params.ID == "" {
		return nil, fmt.Errorf("missing required parameter: id")
	}

	// 构建 sessionKey
	sessionKey := ctx.Channel + ":" + ctx.ChatID
	if sessionKey == ":" {
		sessionKey = ""
	}

	content, err := t.Store.Recall(sessionKey, params.ID)
	if err != nil {
		return nil, fmt.Errorf("recall failed: %w", err)
	}

	// 截断到 8000 字符防止上下文爆炸（使用 []rune 避免 UTF-8 多字节截断）
	const maxLen = 8000
	if len([]rune(content)) > maxLen {
		content = string([]rune(content)[:maxLen]) + "\n\n... (truncated, original " + fmt.Sprintf("%d", len(content)) + " bytes)"
	}

	return NewResult(content), nil
}
