package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
)

// MaskedRecallStore 是 ObservationMaskStore 暴露给 tools 包的接口。
// 返回字符串而非 struct，避免 tools/agent 循环依赖。
type MaskedRecallStore interface {
	// RecallMasked 按 ID 召回已遮蔽的内容，返回 (toolName, fullContent, error)
	RecallMasked(id string) (toolName string, content string, err error)
	// ListMasked 列出所有已遮蔽的 observation，返回 JSON 格式的列表
	ListMasked() []map[string]any
}

// RecallMaskedTool 召回已被 observation masking 遮蔽的工具结果。
type RecallMaskedTool struct {
	Store MaskedRecallStore
}

// recallMaskedParams 参数
type recallMaskedParams struct {
	ID    string `json:"id"`              // 要召回的 mask ID
	List  bool   `json:"list,omitempty"`  // 列出所有已遮蔽的 observation
	Limit int    `json:"limit,omitempty"` // 返回内容最大 rune 数（默认 8000，最大 16000）
}

const (
	recallMaskedDefaultLimit = 8000
	recallMaskedMaxLimit     = 16000
)

func (t *RecallMaskedTool) Name() string { return "recall_masked" }

func (t *RecallMaskedTool) Description() string {
	return `Retrieve the full content of a previously masked tool result.
Observation masking hides old tool results to save context, but preserves them for recall.
Use this tool when you need to see the full content of a masked observation.

Parameters:
- id: The mask ID from 📂 [masked:mk_xxxx] markers (required unless listing)
- list: Set to true to list all masked observations
- limit: Max chars to return (default: 8000, max: 16000)`
}

func (t *RecallMaskedTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "id", Type: "string", Description: "Mask ID (from 📂 [masked:mk_xxxx] markers, e.g. mk_1234abcd). Required unless list=true.", Required: false},
		{Name: "list", Type: "boolean", Description: "If true, list all masked observations without retrieving content", Required: false},
		{Name: "limit", Type: "integer", Description: "Max chars to return (default: 8000, max: 16000)", Required: false},
	}
}

func (t *RecallMaskedTool) Execute(ctx *ToolContext, args string) (*ToolResult, error) {
	if t.Store == nil {
		return nil, fmt.Errorf("observation mask store not available")
	}

	var params recallMaskedParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	// 列出所有已遮蔽的 observation
	if params.List {
		return t.listMasked()
	}

	// 召回特定 ID
	if params.ID == "" {
		return nil, fmt.Errorf("missing required parameter: id (or set list=true to list all)")
	}

	return t.recallByID(params)
}

func (t *RecallMaskedTool) listMasked() (*ToolResult, error) {
	entries := t.Store.ListMasked()

	if len(entries) == 0 {
		return NewResult("No masked observations found."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "📋 Masked Observations (%d total):\n\n", len(entries))

	for i, e := range entries {
		id, _ := e["id"].(string)
		toolName, _ := e["tool_name"].(string)
		argsPreview, _ := e["args_preview"].(string)
		charCount, _ := e["char_count"].(int)
		fmt.Fprintf(&sb, "%d. 📂 [%s] %s(%s) — %d chars\n", i+1, id, toolName, argsPreview, charCount)
	}

	sb.WriteString("\nUse recall_masked(id=\"mk_xxxx\") to retrieve full content.")
	return NewResult(sb.String()), nil
}

func (t *RecallMaskedTool) recallByID(params recallMaskedParams) (*ToolResult, error) {
	toolName, content, err := t.Store.RecallMasked(params.ID)
	if err != nil {
		return nil, err
	}

	limit := params.Limit
	if limit <= 0 {
		limit = recallMaskedDefaultLimit
	}
	if limit > recallMaskedMaxLimit {
		limit = recallMaskedMaxLimit
	}

	runes := []rune(content)
	totalRunes := len(runes)
	totalBytes := len(content)

	header := fmt.Sprintf("📂 [%s] %s\n", params.ID, toolName)
	header += fmt.Sprintf("bytes:%d runes:%d", totalBytes, totalRunes)

	if totalRunes <= limit {
		// 完整返回
		return NewResult(header + "\n" + content), nil
	}

	// 截断返回
	end := limit
	sliced := string(runes[:end])
	result := header + fmt.Sprintf(" (showing first %d of %d runes)\n\n%s\n\n... (truncated, %d more runes. Use a smaller scope or different query.)", limit, totalRunes, sliced, totalRunes-limit)
	return NewResult(result), nil
}
