package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// --- Core Memory Append ---

// CoreMemoryAppendTool appends text to a named core memory block.
type CoreMemoryAppendTool struct{}

func (t *CoreMemoryAppendTool) Name() string { return "core_memory_append" }
func (t *CoreMemoryAppendTool) Description() string {
	return "Append content to a core memory block. Core memory is always visible in your system prompt. Blocks: persona (your identity), human (observations about current user), working_context (active facts/tasks)."
}
func (t *CoreMemoryAppendTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "block",
			Type:        "string",
			Description: "Block name: persona, human, or working_context",
			Required:    true,
		},
		{
			Name:        "content",
			Type:        "string",
			Description: "Text to append to the block (will be added on a new line)",
			Required:    true,
		},
	}
}

type coreMemoryAppendArgs struct {
	Block   string `json:"block"`
	Content string `json:"content"`
}

func (t *CoreMemoryAppendTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args coreMemoryAppendArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	if !isValidBlock(args.Block) {
		return NewResult("Invalid block name. Must be: persona, human, or working_context"), nil
	}
	if args.Content == "" {
		return NewResult("Content is empty, nothing to append."), nil
	}

	coreSvc := ctx.CoreMemory
	if coreSvc == nil {
		return NewResult("Core memory is not available (memory provider is not letta)."), nil
	}
	tenantID := ctx.TenantID

	current, _, err := coreSvc.GetBlock(tenantID, args.Block)
	if err != nil {
		return nil, fmt.Errorf("read block: %w", err)
	}

	var newContent string
	if current == "" {
		newContent = args.Content
	} else {
		newContent = current + "\n" + args.Content
	}

	if err := coreSvc.SetBlock(tenantID, args.Block, newContent); err != nil {
		return nil, fmt.Errorf("update block: %w", err)
	}

	log.WithFields(log.Fields{
		"block":    args.Block,
		"appended": len(args.Content),
	}).Debug("Core memory appended")

	return NewResult(fmt.Sprintf("Appended to %s block. New length: %d chars.", args.Block, len(newContent))), nil
}

// --- Core Memory Replace ---

// CoreMemoryReplaceTool does find-and-replace within a core memory block.
type CoreMemoryReplaceTool struct{}

func (t *CoreMemoryReplaceTool) Name() string { return "core_memory_replace" }
func (t *CoreMemoryReplaceTool) Description() string {
	return "Find and replace text within a core memory block. Use for surgical edits — updating specific facts without rewriting the whole block."
}
func (t *CoreMemoryReplaceTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "block",
			Type:        "string",
			Description: "Block name: persona, human, or working_context",
			Required:    true,
		},
		{
			Name:        "old_text",
			Type:        "string",
			Description: "Exact text to find in the block",
			Required:    true,
		},
		{
			Name:        "new_text",
			Type:        "string",
			Description: "Replacement text (empty string to delete the old text)",
			Required:    true,
		},
	}
}

type coreMemoryReplaceArgs struct {
	Block   string `json:"block"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func (t *CoreMemoryReplaceTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args coreMemoryReplaceArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	if !isValidBlock(args.Block) {
		return NewResult("Invalid block name. Must be: persona, human, or working_context"), nil
	}
	if args.OldText == "" {
		return NewResult("old_text is empty, nothing to find."), nil
	}

	coreSvc := ctx.CoreMemory
	if coreSvc == nil {
		return NewResult("Core memory is not available (memory provider is not letta)."), nil
	}
	tenantID := ctx.TenantID

	current, _, err := coreSvc.GetBlock(tenantID, args.Block)
	if err != nil {
		return nil, fmt.Errorf("read block: %w", err)
	}

	if !strings.Contains(current, args.OldText) {
		return NewResult(fmt.Sprintf("old_text not found in %s block. Current content:\n%s", args.Block, current)), nil
	}

	newContent := strings.Replace(current, args.OldText, args.NewText, 1)
	if err := coreSvc.SetBlock(tenantID, args.Block, newContent); err != nil {
		return nil, fmt.Errorf("update block: %w", err)
	}

	return NewResult(fmt.Sprintf("Replaced in %s block. New length: %d chars.", args.Block, len(newContent))), nil
}

// --- Rethink ---

// RethinkTool allows the agent to reflect and fully rewrite a core memory block.
type RethinkTool struct{}

func (t *RethinkTool) Name() string { return "rethink" }
func (t *RethinkTool) Description() string {
	return "Reflect on and rewrite a core memory block entirely. Use when the block content is stale, contradictory, or needs reorganization. Requires reasoning for the change."
}
func (t *RethinkTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "block",
			Type:        "string",
			Description: "Block name: persona, human, or working_context",
			Required:    true,
		},
		{
			Name:        "new_content",
			Type:        "string",
			Description: "The complete new content for the block",
			Required:    true,
		},
		{
			Name:        "reasoning",
			Type:        "string",
			Description: "Why this rewrite is needed (logged to history for traceability)",
			Required:    true,
		},
	}
}

type rethinkArgs struct {
	Block      string `json:"block"`
	NewContent string `json:"new_content"`
	Reasoning  string `json:"reasoning"`
}

func (t *RethinkTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args rethinkArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}

	if !isValidBlock(args.Block) {
		return NewResult("Invalid block name. Must be: persona, human, or working_context"), nil
	}

	coreSvc := ctx.CoreMemory
	if coreSvc == nil {
		return NewResult("Core memory is not available (memory provider is not letta)."), nil
	}
	tenantID := ctx.TenantID

	if err := coreSvc.SetBlock(tenantID, args.Block, args.NewContent); err != nil {
		return nil, fmt.Errorf("rewrite block: %w", err)
	}

	// Log to event history for traceability
	if ctx.MemorySvc != nil {
		entry := fmt.Sprintf("[rethink:%s] %s", args.Block, args.Reasoning)
		_ = ctx.MemorySvc.AppendHistory(tenantID, entry)
	}

	log.WithFields(log.Fields{
		"block":     args.Block,
		"reasoning": Truncate(args.Reasoning, 100),
	}).Info("Core memory block rewritten via rethink")

	return NewResult(fmt.Sprintf("Block %s rewritten (%d chars). Reasoning logged.", args.Block, len(args.NewContent))), nil
}

// --- Archival Memory Insert ---

// ArchivalMemoryInsertTool inserts a passage into long-term archival memory.
type ArchivalMemoryInsertTool struct{}

func (t *ArchivalMemoryInsertTool) Name() string { return "archival_memory_insert" }
func (t *ArchivalMemoryInsertTool) Description() string {
	return "Insert a passage into archival memory (long-term storage). Use for detailed facts, events, or context that don't fit in core memory. Archival memory is searchable via archival_memory_search."
}
func (t *ArchivalMemoryInsertTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "content",
			Type:        "string",
			Description: "The text to archive",
			Required:    true,
		},
	}
}

type archivalInsertArgs struct {
	Content string `json:"content"`
}

func (t *ArchivalMemoryInsertTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args archivalInsertArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}
	if args.Content == "" {
		return NewResult("Content is empty, nothing to archive."), nil
	}

	archivalSvc := ctx.ArchivalMemory
	if archivalSvc == nil {
		return NewResult("Archival memory is not available (memory provider is not letta)."), nil
	}
	tenantID := ctx.TenantID

	id, err := archivalSvc.Insert(ctx.Ctx, tenantID, args.Content)
	if err != nil {
		return nil, fmt.Errorf("insert archival: %w", err)
	}

	count, _ := archivalSvc.Count(tenantID)
	return NewResult(fmt.Sprintf("Archived (id=%s). Total archival entries: %d.", id, count)), nil
}

// --- Archival Memory Search ---

// ArchivalMemorySearchTool searches archival memory using semantic similarity.
type ArchivalMemorySearchTool struct{}

func (t *ArchivalMemorySearchTool) Name() string { return "archival_memory_search" }
func (t *ArchivalMemorySearchTool) Description() string {
	return "Search archival memory using semantic similarity. Returns the most relevant archived passages. Also searches conversation history."
}
func (t *ArchivalMemorySearchTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "query",
			Type:        "string",
			Description: "The search query",
			Required:    true,
		},
		{
			Name:        "limit",
			Type:        "integer",
			Description: "Maximum number of results to return (default: 5)",
			Required:    false,
		},
	}
}

type archivalSearchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (t *ArchivalMemorySearchTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args archivalSearchArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}
	if args.Query == "" {
		return NewResult("Query is empty."), nil
	}
	if args.Limit <= 0 {
		args.Limit = 5
	}

	archivalSvc := ctx.ArchivalMemory
	if archivalSvc == nil {
		return NewResult("Archival memory is not available (memory provider is not letta)."), nil
	}
	tenantID := ctx.TenantID

	var sb strings.Builder

	// 1. Search archival memory (vector similarity search via chromem-go)
	entries, err := archivalSvc.Search(ctx.Ctx, tenantID, args.Query, args.Limit)
	if err != nil {
		log.WithError(err).Warn("Archival vector search failed")
	}

	if len(entries) > 0 {
		sb.WriteString("## Archival Memory Results\n")
		for i, entry := range entries {
			fmt.Fprintf(&sb, "%d. [id=%s, %s, sim=%.2f] %s\n", i+1, entry.ID[:8], entry.CreatedAt.Format("2006-01-02"), entry.Similarity, entry.Content)
		}
	} else {
		sb.WriteString("No archival memory entries found.\n")
	}

	// 2. Search conversation history (FTS5 recall)
	historyResults, err := archivalSvc.SearchText(tenantID, args.Query, args.Limit)
	if err != nil {
		log.WithError(err).Warn("Failed to search conversation history")
	}
	if len(historyResults) > 0 {
		sb.WriteString("\n## Conversation History Results\n")
		for i, entry := range historyResults {
			fmt.Fprintf(&sb, "%d. %s\n", i+1, entry)
		}
	}

	if sb.Len() == 0 {
		return NewResult("No results found."), nil
	}
	return NewResult(sb.String()), nil
}

// --- Recall Memory Search ---

// RecallMemorySearchTool searches conversation history by keyword and/or time range.
type RecallMemorySearchTool struct{}

func (t *RecallMemorySearchTool) Name() string { return "recall_memory_search" }
func (t *RecallMemorySearchTool) Description() string {
	return "Search conversation history (recall memory) by keyword and/or time range. Use when the user references past conversations, e.g. \"a few days ago we discussed...\". Supports date-range filtering to narrow results."
}
func (t *RecallMemorySearchTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "query",
			Type:        "string",
			Description: "Search keyword or phrase. Can be empty if only filtering by time range.",
			Required:    false,
		},
		{
			Name:        "start_date",
			Type:        "string",
			Description: "Start date (inclusive) in YYYY-MM-DD format. Example: 2025-01-15",
			Required:    false,
		},
		{
			Name:        "end_date",
			Type:        "string",
			Description: "End date (inclusive) in YYYY-MM-DD format. Example: 2025-01-20",
			Required:    false,
		},
		{
			Name:        "limit",
			Type:        "integer",
			Description: "Maximum number of results to return (default: 20)",
			Required:    false,
		},
	}
}

type recallSearchArgs struct {
	Query     string `json:"query"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Limit     int    `json:"limit"`
}

func (t *RecallMemorySearchTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
	var args recallSearchArgs
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return nil, fmt.Errorf("parse arguments: %w", err)
	}
	if args.Query == "" && args.StartDate == "" && args.EndDate == "" {
		return NewResult("At least one of query, start_date, or end_date must be provided."), nil
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}

	recallFn := ctx.RecallTimeRange
	if recallFn == nil {
		return NewResult("Recall memory search is not available (memory provider is not letta)."), nil
	}

	var startTime, endTime time.Time
	if args.StartDate != "" {
		t, err := time.Parse("2006-01-02", args.StartDate)
		if err != nil {
			return NewResult(fmt.Sprintf("Invalid start_date format: %s. Use YYYY-MM-DD.", args.StartDate)), nil
		}
		startTime = t
	}
	if args.EndDate != "" {
		t, err := time.Parse("2006-01-02", args.EndDate)
		if err != nil {
			return NewResult(fmt.Sprintf("Invalid end_date format: %s. Use YYYY-MM-DD.", args.EndDate)), nil
		}
		// End of day
		endTime = t.Add(24*time.Hour - time.Second)
	}

	entries, err := recallFn(ctx.TenantID, args.Query, startTime, endTime, args.Limit)
	if err != nil {
		log.WithError(err).Warn("Recall memory search failed")
		return NewResult(fmt.Sprintf("Search failed: %v", err)), nil
	}

	if len(entries) == 0 {
		return NewResult("No conversation history found matching the criteria."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Recall Memory Results (%d entries)\n\n", len(entries))
	for i, entry := range entries {
		dateStr := entry.CreatedAt.Format("2006-01-02 15:04")
		fmt.Fprintf(&sb, "%d. [%s] %s\n", i+1, dateStr, entry.Entry)
	}

	return NewResult(sb.String()), nil
}

// --- helpers ---

func isValidBlock(name string) bool {
	switch name {
	case "persona", "human", "working_context":
		return true
	}
	return false
}

// LettaMemoryTools returns all Letta memory tools for registration.
func LettaMemoryTools() []Tool {
	return []Tool{
		&CoreMemoryAppendTool{},
		&CoreMemoryReplaceTool{},
		&RethinkTool{},
		&ArchivalMemoryInsertTool{},
		&ArchivalMemorySearchTool{},
		&RecallMemorySearchTool{},
	}
}
