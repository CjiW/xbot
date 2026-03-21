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

const (
	offloadDefaultLimit = 8000  // 每次默认返回的 rune 数（约 8000 字符/16000 字节中文）
	offloadMaxLimit     = 16000 // 最大返回 rune 数上限：平衡 LLM 上下文窗口与信息完整性，
	// 单次约 16000 字符（约 32KB 中文），超大内容通过 offset 分页读取
	offloadDefaultOffset = 0
)

// OffloadRecallTool 召回已 offload 的工具结果完整内容，支持分页读取。
type OffloadRecallTool struct {
	Store OffloadRecallStore
}

// offloadRecallParams 是 offload_recall 工具的参数。
type offloadRecallParams struct {
	ID     string `json:"id"`
	Offset int    `json:"offset,omitempty"` // 起始位置（rune 偏移），默认 0
	Limit  int    `json:"limit,omitempty"`  // 最大返回字符数（rune），默认 8000，最大 16000
}

func (t *OffloadRecallTool) Name() string { return "offload_recall" }

func (t *OffloadRecallTool) Description() string {
	return `Retrieve the full content of a previously offloaded tool result.
Use the offload ID (from 📂 markers in tool results) to retrieve the complete data.
Supports pagination via offset and limit parameters for large content.
Default: offset=0, limit=8000. Max limit: 16000.`
}

func (t *OffloadRecallTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "id", Type: "string", Description: "Offload ID (obtained from 📂 markers, e.g. ol_1234abcd)", Required: true},
		{Name: "offset", Type: "integer", Description: "Rune offset to start reading from (default: 0)", Required: false},
		{Name: "limit", Type: "integer", Description: "Max runes to return (default: 8000, max: 16000)", Required: false},
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

	// 默认值填充
	if params.Offset < 0 {
		params.Offset = offloadDefaultOffset
	}
	if params.Limit <= 0 {
		params.Limit = offloadDefaultLimit
	}
	if params.Limit > offloadMaxLimit {
		params.Limit = offloadMaxLimit
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

	runes := []rune(content)
	totalRunes := len(runes)
	totalBytes := len(content)

	// offset 超出范围
	if params.Offset >= totalRunes {
		return NewResult(fmt.Sprintf("⚠️ offset %d exceeds total length %d runes (the content is fully read)", params.Offset, totalRunes)), nil
	}

	end := params.Offset + params.Limit
	hasMore := end < totalRunes
	if end > totalRunes {
		end = totalRunes
	}

	sliced := string(runes[params.Offset:end])

	// 分页信息头
	header := fmt.Sprintf("📂 [%s] bytes:%d runes:%d-%d/%d", params.ID, totalBytes, params.Offset, end, totalRunes)
	if hasMore {
		header += fmt.Sprintf(" | ▶️ Use offset=%d to read next page", end)
	}
	if params.Offset > 0 {
		header += fmt.Sprintf(" | ◀️ offset=%d for previous", params.Offset)
	}

	result := header + "\n" + sliced
	if hasMore {
		result += "\n\n... (more content below, use offset=" + fmt.Sprintf("%d", end) + " to continue)"
	}

	return NewResult(result), nil
}
