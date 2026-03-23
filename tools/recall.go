package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"xbot/llm"
)

const (
	recallDefaultLimit  = 16000
	recallMaxLimit      = 32000
	recallDefaultOffset = 0
)

// RecallTool is a unified recall tool that retrieves both offloaded and masked content.
// It auto-routes based on ID prefix: "ol_" → OffloadStore, "mk_" → MaskedStore.
type RecallTool struct {
	OffloadStore OffloadRecallStore
	MaskedStore  MaskedRecallStore
}

type recallParams struct {
	ID     string `json:"id"`
	List   bool   `json:"list,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

func (t *RecallTool) Name() string { return "recall" }

func (t *RecallTool) Description() string {
	return `Retrieve the full content of a previously offloaded or masked tool result.
Works with both offload IDs (ol_xxxx from 📂 [offload:...] markers) and
mask IDs (mk_xxxx from 📂 [masked:...] markers). The tool auto-detects based on ID prefix.
Supports pagination for large offloaded content via offset/limit.
Set list=true to list all available masked observations.`
}

func (t *RecallTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "id", Type: "string", Description: "The ID to recall (ol_xxxx for offloaded, mk_xxxx for masked). Required unless list=true.", Required: false},
		{Name: "list", Type: "boolean", Description: "If true, list all masked observations", Required: false},
		{Name: "offset", Type: "integer", Description: "Rune offset for offloaded content pagination (default: 0)", Required: false},
		{Name: "limit", Type: "integer", Description: "Max runes to return (default: 16000, max: 32000)", Required: false},
	}
}

func (t *RecallTool) Execute(ctx *ToolContext, args string) (*ToolResult, error) {
	var params recallParams
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.List {
		return t.listMasked()
	}

	if params.ID == "" {
		return nil, fmt.Errorf("missing required parameter: id (or set list=true)")
	}

	limit := params.Limit
	if limit <= 0 {
		limit = recallDefaultLimit
	}
	if limit > recallMaxLimit {
		limit = recallMaxLimit
	}

	if strings.HasPrefix(params.ID, "ol_") {
		return t.recallOffloaded(ctx, params.ID, params.Offset, limit)
	}
	if strings.HasPrefix(params.ID, "mk_") {
		return t.recallMasked(params.ID, limit)
	}

	// Unknown prefix: try both stores
	if t.OffloadStore != nil {
		sessionKey := resolveRecallSessionKey(ctx)
		if content, err := t.OffloadStore.Recall(sessionKey, params.ID); err == nil {
			return t.formatOffloaded(params.ID, content, params.Offset, limit)
		}
	}
	if t.MaskedStore != nil {
		if _, content, err := t.MaskedStore.RecallMasked(params.ID); err == nil {
			return t.formatMasked(params.ID, "", content, limit)
		}
	}

	return nil, fmt.Errorf("ID %s not found in either offload or masked store", params.ID)
}

func (t *RecallTool) recallOffloaded(ctx *ToolContext, id string, offset, limit int) (*ToolResult, error) {
	if t.OffloadStore == nil {
		return nil, fmt.Errorf("offload store not available")
	}
	if offset < 0 {
		offset = 0
	}

	sessionKey := resolveRecallSessionKey(ctx)
	content, err := t.OffloadStore.Recall(sessionKey, id)
	if err != nil {
		return nil, fmt.Errorf("recall failed: %w", err)
	}
	return t.formatOffloaded(id, content, offset, limit)
}

func (t *RecallTool) formatOffloaded(id, content string, offset, limit int) (*ToolResult, error) {
	runes := []rune(content)
	totalRunes := len(runes)
	totalBytes := len(content)

	if offset >= totalRunes {
		return NewResult(fmt.Sprintf("⚠️ offset %d exceeds total length %d runes", offset, totalRunes)), nil
	}

	end := offset + limit
	hasMore := end < totalRunes
	if end > totalRunes {
		end = totalRunes
	}

	sliced := string(runes[offset:end])

	header := fmt.Sprintf("📂 [%s] bytes:%d runes:%d-%d/%d", id, totalBytes, offset, end, totalRunes)
	if hasMore {
		header += fmt.Sprintf(" | ▶️ Use offset=%d to read next page", end)
	}

	result := header + "\n" + sliced
	if hasMore {
		result += fmt.Sprintf("\n\n... (more content, use recall(id=\"%s\", offset=%d) to continue)", id, end)
	}
	return NewResult(result), nil
}

func (t *RecallTool) recallMasked(id string, limit int) (*ToolResult, error) {
	if t.MaskedStore == nil {
		return nil, fmt.Errorf("masked store not available")
	}
	toolName, content, err := t.MaskedStore.RecallMasked(id)
	if err != nil {
		return nil, err
	}
	return t.formatMasked(id, toolName, content, limit)
}

func (t *RecallTool) formatMasked(id, toolName, content string, limit int) (*ToolResult, error) {
	runes := []rune(content)
	totalRunes := len(runes)
	totalBytes := len(content)

	header := fmt.Sprintf("📂 [%s]", id)
	if toolName != "" {
		header += " " + toolName
	}
	header += fmt.Sprintf("\nbytes:%d runes:%d", totalBytes, totalRunes)

	if totalRunes <= limit {
		return NewResult(header + "\n" + content), nil
	}

	sliced := string(runes[:limit])
	result := header + fmt.Sprintf(" (showing first %d of %d runes)\n\n%s\n\n... (truncated, %d more runes)", limit, totalRunes, sliced, totalRunes-limit)
	return NewResult(result), nil
}

func (t *RecallTool) listMasked() (*ToolResult, error) {
	if t.MaskedStore == nil {
		return NewResult("No masked store available."), nil
	}
	entries := t.MaskedStore.ListMasked()
	if len(entries) == 0 {
		return NewResult("No masked observations found."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "📋 Masked Observations (%d total):\n\n", len(entries))
	for i, e := range entries {
		id, _ := e["id"].(string)
		tn, _ := e["tool_name"].(string)
		ap, _ := e["args_preview"].(string)
		cc, _ := e["char_count"].(int)
		fmt.Fprintf(&sb, "%d. 📂 [%s] %s(%s) — %d chars\n", i+1, id, tn, ap, cc)
	}
	sb.WriteString("\nUse recall(id=\"mk_xxxx\") to retrieve full content.")
	return NewResult(sb.String()), nil
}

func resolveRecallSessionKey(ctx *ToolContext) string {
	if ctx == nil {
		return ""
	}
	sessionKey := ctx.RootSessionKey
	if sessionKey == "" {
		sessionKey = ctx.Channel + ":" + ctx.ChatID
	}
	if sessionKey == ":" {
		sessionKey = ""
	}
	return sessionKey
}
