package letta

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
)

// LettaMemory implements memory.MemoryProvider with a Letta (MemGPT) architecture:
// - Core Memory: structured blocks injected into system prompt (persona/human/working_context)
// - Archival Memory: long-term embedding-backed storage (on-demand via tools)
// - Recall Memory: conversation history retrieval by time range
type LettaMemory struct {
	tenantID     int64
	userID       *string // for per-user human block (senderID string like "ou_xxx")
	coreSvc      *sqlite.CoreMemoryService
	archivalSvc  *vectordb.ArchivalService
	memorySvc    *sqlite.MemoryService
	toolIndexSvc *vectordb.ToolIndexService
}

var _ memory.MemoryProvider = (*LettaMemory)(nil)
var _ memory.ToolIndexer = (*LettaMemory)(nil)

// New creates a LettaMemory instance.
// If userID is provided (senderID string like "ou_xxx"), the human block will be per-user instead of per-tenant.
func New(tenantID int64, userID *string, coreSvc *sqlite.CoreMemoryService, archivalSvc *vectordb.ArchivalService, memorySvc *sqlite.MemoryService, toolIndexSvc *vectordb.ToolIndexService) *LettaMemory {
	// Ensure default blocks exist (with userID for human block)
	if err := coreSvc.InitBlocks(tenantID, userID); err != nil {
		log.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to init core memory blocks")
	}
	return &LettaMemory{
		tenantID:     tenantID,
		userID:       userID,
		coreSvc:      coreSvc,
		archivalSvc:  archivalSvc,
		memorySvc:    memorySvc,
		toolIndexSvc: toolIndexSvc,
	}
}

// Recall returns formatted core memory blocks for system prompt injection.
// Unlike FlatMemory which dumps everything, Letta injects only structured blocks.
// Archival memory is accessed on-demand via tools.
func (m *LettaMemory) Recall(_ context.Context, _ string) (string, error) {
	blocks, err := m.coreSvc.GetAllBlocks(m.tenantID, m.userID)
	if err != nil {
		return "", fmt.Errorf("recall core blocks: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("## Core Memory\n")

	// Render in stable order
	for _, name := range []string{"persona", "human", "working_context"} {
		content := blocks[name]
		title := blockTitle(name)
		fmt.Fprintf(&sb, "### %s\n", title)
		if content != "" {
			sb.WriteString(content)
		} else {
			sb.WriteString("(empty)")
		}
		sb.WriteString("\n\n")
	}

	// Archival memory summary
	if m.archivalSvc != nil {
		count, err := m.archivalSvc.Count(m.tenantID)
		if err != nil {
			log.WithError(err).Warn("Failed to count archival memory")
		}
		fmt.Fprintf(&sb, "[Archival Memory: %d entries | Use archival_memory_search to retrieve]\n", count)
	}

	return sb.String(), nil
}

// Memorize consolidates conversation messages into core memory updates + archival storage.
// Uses LLM with a multi-tool rethink prompt.
func (m *LettaMemory) Memorize(ctx context.Context, input memory.MemorizeInput) (memory.MemorizeResult, error) {
	messages := input.Messages
	lastConsolidated := input.LastConsolidated
	archiveAll := input.ArchiveAll
	memoryWindow := input.MemoryWindow

	var oldMessages []llm.ChatMessage
	keepCount := 0

	if archiveAll {
		oldMessages = messages
		log.WithField("tenant_id", m.tenantID).Infof("Letta memory consolidation (archive_all): %d messages", len(messages))
	} else {
		keepCount = memoryWindow / 2
		if len(messages) <= keepCount {
			return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: true}, nil
		}
		if len(messages)-lastConsolidated <= 0 {
			return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: true}, nil
		}
		end := len(messages) - keepCount
		if lastConsolidated >= end {
			return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: true}, nil
		}
		oldMessages = messages[lastConsolidated:end]
		if len(oldMessages) == 0 {
			return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: true}, nil
		}
		log.WithField("tenant_id", m.tenantID).Infof("Letta memory consolidation: %d to consolidate, %d keep", len(oldMessages), keepCount)
	}

	// Format old messages as text
	var lines []string
	for _, msg := range oldMessages {
		if msg.Content == "" {
			continue
		}
		role := strings.ToUpper(msg.Role)
		toolHint := ""
		if msg.Role == "tool" && msg.ToolName != "" {
			toolHint = fmt.Sprintf(" [tool: %s]", msg.ToolName)
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			names := make([]string, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				names[i] = tc.Name
			}
			toolHint = fmt.Sprintf(" [tools: %s]", strings.Join(names, ", "))
		}
		ts := msg.Timestamp
		if ts.IsZero() {
			ts = time.Now()
		}
		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		lines = append(lines, fmt.Sprintf("[%s] %s%s: %s", ts.Format("2006-01-02 15:04"), role, toolHint, content))
	}

	if len(lines) == 0 {
		newLC := 0
		if !archiveAll {
			newLC = len(messages) - keepCount
		}
		return memory.MemorizeResult{NewLastConsolidated: newLC, OK: true}, nil
	}

	// Read current core memory blocks
	blocks, err := m.coreSvc.GetAllBlocks(m.tenantID, m.userID)
	if err != nil {
		log.WithError(err).Error("Failed to read core memory for consolidation")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	var coreDisplay strings.Builder
	for _, name := range []string{"persona", "human", "working_context"} {
		content := blocks[name]
		if content == "" {
			content = "(empty)"
		}
		fmt.Fprintf(&coreDisplay, "### %s\n%s\n\n", blockTitle(name), content)
	}

	prompt := fmt.Sprintf(`You are a memory consolidation agent for a Letta-style memory system.
Review the conversation below and call the consolidate_memory tool to update the memory system.

## Instructions

- Update core memory blocks (persona/human/working_context) if the conversation reveals new important information
- Archive detailed facts/events to archival memory that don't fit in core memory
- Write a history entry summarizing key events
- Only update blocks that need changes. Set unchanged block values to empty string "".
- Keep core memory blocks concise (bullet points, not prose)

## Current Core Memory
%s

## Conversation to Process
%s
`, coreDisplay.String(), strings.Join(lines, "\n"))

	resp, err := input.LLMClient.Generate(ctx, input.Model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a memory consolidation agent. Call the consolidate_memory tool."),
		llm.NewUserMessage(prompt),
	}, consolidateMemoryTool)
	if err != nil {
		log.WithError(err).Error("Letta memory consolidation LLM call failed")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	if !resp.HasToolCalls() {
		log.Warn("Letta memory consolidation: LLM did not call consolidate_memory, skipping")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	var args consolidateMemoryArgs
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Arguments), &args); err != nil {
		log.WithError(err).Error("Letta memory consolidation: failed to parse arguments")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	// Apply core memory updates
	for _, blockName := range []string{"persona", "human", "working_context"} {
		var newContent string
		switch blockName {
		case "persona":
			newContent = args.Persona
		case "human":
			newContent = args.Human
		case "working_context":
			newContent = args.WorkingContext
		}
		oldContent := blocks[blockName]
		if newContent != "" {
			log.WithFields(log.Fields{
				"tenant_id": m.tenantID,
				"block":     blockName,
				"old_len":   len(oldContent),
				"new_len":   len(newContent),
			}).Info("Updating core memory block")
			if err := m.coreSvc.SetBlock(m.tenantID, blockName, newContent, m.userID); err != nil {
				log.WithError(err).WithField("block", blockName).Error("Failed to update core memory block")
			}
		}
	}

	// Archive to archival memory (embedding computed by chromem-go)
	// Use the midpoint of the conversation time range as the information timestamp
	archivalTS := conversationMidpoint(oldMessages)
	for _, entry := range args.ArchivalEntries {
		if entry == "" {
			continue
		}
		if m.archivalSvc != nil {
			if _, err := m.archivalSvc.Insert(ctx, m.tenantID, entry, archivalTS); err != nil {
				log.WithError(err).Error("Failed to insert archival entry during consolidation")
			}
		}
	}

	// Append history entry
	if args.HistoryEntry != "" {
		if err := m.memorySvc.AppendHistory(m.tenantID, args.HistoryEntry); err != nil {
			log.WithError(err).Error("Failed to append history entry")
		}
	}

	newLC := 0
	if !archiveAll {
		newLC = len(messages) - keepCount
	}
	log.WithField("tenant_id", m.tenantID).Infof("Letta memory consolidation done: lastConsolidated=%d", newLC)
	return memory.MemorizeResult{NewLastConsolidated: newLC, OK: true}, nil
}

// Close releases resources (no-op for LettaMemory).
func (m *LettaMemory) Close() error {
	return nil
}

// TenantID returns the tenant ID (exposed for tools to access storage).
func (m *LettaMemory) TenantID() int64 {
	return m.tenantID
}

// CoreService returns the core memory service (exposed for tools).
func (m *LettaMemory) CoreService() *sqlite.CoreMemoryService {
	return m.coreSvc
}

// ArchivalService returns the archival memory service (exposed for tools).
func (m *LettaMemory) ArchivalService() *vectordb.ArchivalService {
	return m.archivalSvc
}

// MemoryService returns the underlying memory service (exposed for recall search).
func (m *LettaMemory) MemoryService() *sqlite.MemoryService {
	return m.memorySvc
}

// ToolIndexerService returns the tool index service.
func (m *LettaMemory) ToolIndexerService() *vectordb.ToolIndexService {
	return m.toolIndexSvc
}

// IndexTools implements memory.ToolIndexer.
func (m *LettaMemory) IndexTools(ctx context.Context, tools []memory.ToolIndexEntry) error {
	if m.toolIndexSvc == nil {
		return fmt.Errorf("tool index service not available")
	}
	// Clear existing tools and re-index
	if err := m.toolIndexSvc.ClearTools(ctx, m.tenantID); err != nil {
		log.WithError(err).Warn("Failed to clear tool index")
	}
	for _, tool := range tools {
		content := fmt.Sprintf("Tool: %s\nServer: %s\nSource: %s\nDescription: %s",
			tool.Name, tool.ServerName, tool.Source, tool.Description)
		toolID := fmt.Sprintf("%s_%s", tool.ServerName, tool.Name)
		if err := m.toolIndexSvc.InsertTool(ctx, m.tenantID, toolID, content); err != nil {
			log.WithError(err).WithField("tool", tool.Name).Warn("Failed to index tool")
		}
	}
	log.WithField("tenant_id", m.tenantID).Infof("Indexed %d tools", len(tools))
	return nil
}

// SearchTools implements memory.ToolIndexer (searches current tenant).
func (m *LettaMemory) SearchTools(ctx context.Context, query string, topK int) ([]memory.ToolIndexEntry, error) {
	return m.SearchToolsForTenant(ctx, m.tenantID, query, topK)
}

// SearchToolsForTenant searches tools for a specific tenant.
func (m *LettaMemory) SearchToolsForTenant(ctx context.Context, tenantID int64, query string, topK int) ([]memory.ToolIndexEntry, error) {
	if m.toolIndexSvc == nil {
		return nil, fmt.Errorf("tool index service not available")
	}
	results, err := m.toolIndexSvc.SearchTools(ctx, tenantID, query, topK)
	if err != nil {
		return nil, fmt.Errorf("search tools: %w", err)
	}
	entries := make([]memory.ToolIndexEntry, len(results))
	for i, r := range results {
		// Parse tool ID to extract server and name
		// Format: serverName_toolName
		parts := strings.SplitN(r.ID, "_", 2)
		serverName := ""
		toolName := r.ID
		if len(parts) >= 2 {
			serverName = parts[0]
			toolName = parts[1]
		}
		entries[i] = memory.ToolIndexEntry{
			Name:        toolName,
			ServerName:  serverName,
			Source:      "personal",
			Description: r.Content,
		}
	}
	return entries, nil
}

// --- helpers ---

func blockTitle(name string) string {
	switch name {
	case "persona":
		return "Persona"
	case "human":
		return "Human"
	case "working_context":
		return "Working Context"
	default:
		return name
	}
}

// conversationMidpoint returns the midpoint timestamp of a slice of messages.
// If no message has a non-zero Timestamp, returns the current time.
func conversationMidpoint(msgs []llm.ChatMessage) time.Time {
	var earliest, latest time.Time
	for _, m := range msgs {
		ts := m.Timestamp
		if ts.IsZero() {
			continue
		}
		if earliest.IsZero() || ts.Before(earliest) {
			earliest = ts
		}
		if latest.IsZero() || ts.After(latest) {
			latest = ts
		}
	}
	if earliest.IsZero() {
		return time.Now()
	}
	mid := earliest.Add(latest.Sub(earliest) / 2)
	return mid
}

// --- consolidate_memory tool definition ---

var consolidateMemoryTool = []llm.ToolDefinition{&consolidateMemoryToolDef{}}

type consolidateMemoryToolDef struct{}

func (t *consolidateMemoryToolDef) Name() string { return "consolidate_memory" }
func (t *consolidateMemoryToolDef) Description() string {
	return "Save memory consolidation results to the Letta memory system."
}
func (t *consolidateMemoryToolDef) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "persona",
			Type:        "string",
			Description: "Updated persona block. WARNING: This will COMPLETELY REPLACE existing content. Return empty string if no changes needed.",
			Required:    true,
		},
		{
			Name:        "human",
			Type:        "string",
			Description: "Updated human block (observations about the user). WARNING: This will COMPLETELY REPLACE existing content. Return empty string if no changes needed.",
			Required:    true,
		},
		{
			Name:        "working_context",
			Type:        "string",
			Description: "Updated working context block (active facts/session context). WARNING: This will COMPLETELY REPLACE existing content. Return empty string if no changes needed.",
			Required:    true,
		},
		{
			Name:        "archival_entries",
			Type:        "array",
			Description: "List of detailed facts/events to archive to long-term storage. Each entry is a string.",
			Required:    false,
		},
		{
			Name:        "history_entry",
			Type:        "string",
			Description: "A paragraph summarizing key events/decisions. Start with [YYYY-MM-DD HH:MM].",
			Required:    true,
		},
	}
}

type consolidateMemoryArgs struct {
	Persona         string   `json:"persona"`
	Human           string   `json:"human"`
	WorkingContext  string   `json:"working_context"`
	ArchivalEntries []string `json:"archival_entries"`
	HistoryEntry    string   `json:"history_entry"`
}
