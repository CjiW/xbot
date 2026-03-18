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

// userIDKey is the context key for per-user human block senderID
type contextKey string

const userIDKey contextKey = "letta_user_id"

// WithUserID returns a context with the senderID for per-user human block
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

// GetUserID extracts senderID from context for per-user human block
func GetUserID(ctx context.Context) string {
	if uid, ok := ctx.Value(userIDKey).(string); ok {
		return uid
	}
	return ""
}

// LettaMemory implements memory.MemoryProvider with a Letta (MemGPT) architecture:
// - Core Memory: structured blocks injected into system prompt (persona/human/working_context)
// - Archival Memory: long-term embedding-backed storage (on-demand via tools)
// - Recall Memory: conversation history retrieval by time range
//
// NOTE: userID is NOT stored in struct. Per-user human block is handled dynamically
// via context (WithUserID/GetUserID) passed to Recall/Memorize at call time.
// This avoids session cache key issues where different users would share a cached LettaMemory
// with a stale userID.
type LettaMemory struct {
	tenantID     int64
	coreSvc      *sqlite.CoreMemoryService
	archivalSvc  *vectordb.ArchivalService
	memorySvc    *sqlite.MemoryService
	toolIndexSvc *vectordb.ToolIndexService
}

var _ memory.MemoryProvider = (*LettaMemory)(nil)
var _ memory.ToolIndexer = (*LettaMemory)(nil)

// New creates a LettaMemory instance.
// NOTE: senderID is NOT passed here. Per-user human block is handled dynamically
// via context (WithUserID/GetUserID) passed to Recall/Memorize at call time.
func New(tenantID int64, coreSvc *sqlite.CoreMemoryService, archivalSvc *vectordb.ArchivalService, memorySvc *sqlite.MemoryService, toolIndexSvc *vectordb.ToolIndexService) *LettaMemory {
	// Ensure default blocks exist (global, userID="")
	// Per-user blocks are created on-demand when Recall/Memorize is called with userID in context
	if err := coreSvc.InitBlocks(tenantID, ""); err != nil {
		log.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to init core memory blocks")
	}
	return &LettaMemory{
		tenantID:     tenantID,
		coreSvc:      coreSvc,
		archivalSvc:  archivalSvc,
		memorySvc:    memorySvc,
		toolIndexSvc: toolIndexSvc,
	}
}

// Recall returns formatted core memory blocks for system prompt injection.
// Unlike FlatMemory which dumps everything, Letta injects only structured blocks.
// Archival memory is accessed on-demand via tools.
// userID for per-user human block is extracted from ctx via GetUserID(ctx).
func (m *LettaMemory) Recall(ctx context.Context, _ string) (string, error) {
	// Get userID from context for per-user human block (empty = global)
	userID := GetUserID(ctx)

	blocks, err := m.coreSvc.GetAllBlocks(m.tenantID, userID)
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
// userID for per-user human block is extracted from ctx via GetUserID(ctx).
func (m *LettaMemory) Memorize(ctx context.Context, input memory.MemorizeInput) (memory.MemorizeResult, error) {
	// Get userID from context for per-user human block (empty = global)
	userID := GetUserID(ctx)

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

	// Deduplication: search for similar existing memories before archiving
	// Similarity thresholds:
	// - > 0.9: duplicate, merge or skip
	// - 0.5-0.9: potential conflict, let LLM decide
	// - < 0.5: new information, insert directly
	const dedupSearchLimit = 10
	const similarityConflictThreshold = float32(0.5)

	var existingMemories []ExistingMemory
	if m.archivalSvc != nil && len(oldMessages) > 0 {
		// Build a query from the messages to consolidate
		var queryBuilder strings.Builder
		for _, msg := range oldMessages {
			if msg.Content != "" {
				queryBuilder.WriteString(msg.Content)
				queryBuilder.WriteString(" ")
			}
			if queryBuilder.Len() > 500 {
				break
			}
		}
		query := queryBuilder.String()
		if query != "" {
			results, err := m.archivalSvc.Search(ctx, m.tenantID, query, dedupSearchLimit)
			if err != nil {
				log.WithError(err).Warn("Failed to search for similar memories during deduplication")
			} else {
				for _, r := range results {
					// Only include memories with similarity > conflict threshold
					if r.Similarity > similarityConflictThreshold {
						existingMemories = append(existingMemories, ExistingMemory{
							ID:         r.ID,
							Content:    r.Content,
							Similarity: r.Similarity,
						})
					}
				}
				if len(existingMemories) > 0 {
					log.WithField("tenant_id", m.tenantID).Infof("Found %d similar memories for deduplication", len(existingMemories))
				}
			}
		}
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
		// 按 rune 截断，避免中文乱码
		content := msg.Content
		if len([]rune(content)) > 500 {
			content = string([]rune(content)[:500]) + "..."
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
	blocks, err := m.coreSvc.GetAllBlocks(m.tenantID, userID)
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

	// Build deduplication context for the prompt
	var dedupContext string
	if len(existingMemories) > 0 {
		dedupContext = "\n## Existing Similar Memories (for deduplication)\n"
		dedupContext += "The following memories are similar to what you're about to archive. Use this to:\n"
		dedupContext += "- Skip duplicate information (similarity > 0.9)\n"
		dedupContext += "- Resolve conflicts by choosing the more accurate one (similarity 0.5-0.9)\n"
		dedupContext += "- Set entries_to_delete for any memories that should be replaced/removed\n\n"
		for _, mem := range existingMemories {
			dedupContext += fmt.Sprintf("- [ID: %s, Similarity: %.2f]: %s\n", mem.ID, mem.Similarity, mem.Content)
			if len(dedupContext) > 2000 {
				dedupContext += "... (truncated)\n"
				break
			}
		}
	}

	prompt := fmt.Sprintf(`You are a memory consolidation agent for a Letta-style memory system.
Review the conversation below and call the consolidate_memory tool to update the memory system.

## Instructions

- Update core memory blocks (persona/human/working_context) if the conversation reveals new important information
- Archive detailed facts/events to archival memory that don't fit in core memory
- Write a history entry summarizing key events
- Only update blocks that need changes. Set unchanged block values to empty string "".
- Keep core memory blocks concise (bullet points, not prose)
- Deduplicate: compare new archival_entries against existing memories. If information is redundant (similarity > 0.9), merge or skip it. If there's conflict (similarity 0.5-0.9), decide which to keep and add its ID to entries_to_delete.
- Always prefer new information when in doubt

## Current Core Memory
%s
%s

## Conversation to Process
%s
`, coreDisplay.String(), dedupContext, strings.Join(lines, "\n"))

	resp, err := input.LLMClient.Generate(ctx, input.Model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a memory consolidation agent. Call the consolidate_memory tool."),
		llm.NewUserMessage(prompt),
	}, consolidateMemoryTool, "")
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
			if err := m.coreSvc.SetBlock(m.tenantID, blockName, newContent, userID); err != nil {
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

	// Delete duplicate/conflicting memories as determined by LLM
	if len(args.EntriesToDelete) > 0 && m.archivalSvc != nil {
		for _, entryID := range args.EntriesToDelete {
			if err := m.archivalSvc.Delete(ctx, m.tenantID, entryID); err != nil {
				log.WithError(err).WithField("entry_id", entryID).Warn("Failed to delete duplicate memory during consolidation")
			} else {
				log.WithField("tenant_id", m.tenantID).Infof("Deleted duplicate memory: %s", entryID)
			}
		}
	}

	// Append history entry
	if args.HistoryEntry != "" {
		if err := m.memorySvc.AppendHistory(ctx, m.tenantID, args.HistoryEntry); err != nil {
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
// Delegates to ToolIndexService.IndexTools which handles metadata storage properly.
func (m *LettaMemory) IndexTools(ctx context.Context, tools []memory.ToolIndexEntry) error {
	if m.toolIndexSvc == nil {
		return fmt.Errorf("tool index service not available")
	}
	// Directly delegate to ToolIndexService.IndexTools
	// It handles storing channels in metadata (not content) to avoid affecting embeddings
	return m.toolIndexSvc.IndexTools(ctx, m.tenantID, tools)
}

// SearchTools implements memory.ToolIndexer (searches current tenant without channel filter).
func (m *LettaMemory) SearchTools(ctx context.Context, query string, topK int) ([]memory.ToolIndexEntry, error) {
	return m.SearchToolsForTenant(ctx, m.tenantID, query, topK, "")
}

// SearchToolsForTenant searches tools for a specific tenant.
// If channel is not empty, filters results to only include tools that support that channel.
// Channels are read from metadata (stored during indexing).
func (m *LettaMemory) SearchToolsForTenant(ctx context.Context, tenantID int64, query string, topK int, channel string) ([]memory.ToolIndexEntry, error) {
	if m.toolIndexSvc == nil {
		return nil, fmt.Errorf("tool index service not available")
	}
	results, err := m.toolIndexSvc.SearchTools(ctx, tenantID, query, topK)
	if err != nil {
		return nil, fmt.Errorf("search tools: %w", err)
	}
	entries := make([]memory.ToolIndexEntry, 0, len(results))
	for _, r := range results {
		// Parse tool ID to extract server and name
		// Format: serverName_toolName
		parts := strings.SplitN(r.ID, "_", 2)
		serverName := ""
		toolName := r.ID
		if len(parts) >= 2 {
			serverName = parts[0]
			toolName = parts[1]
		}
		// Extract channels from metadata (stored during indexing)
		var channels []string
		if r.Metadata != nil {
			if chStr, ok := r.Metadata["channels"]; ok && chStr != "" {
				channels = strings.Split(chStr, ",")
			}
			// Also prefer metadata server_name if available
			if sn, ok := r.Metadata["server_name"]; ok && sn != "" {
				serverName = sn
			}
		}
		entry := memory.ToolIndexEntry{
			Name:        toolName,
			ServerName:  serverName,
			Source:      "personal",
			Description: r.Content,
			Channels:    channels,
		}
		// 渠道过滤：如果指定了渠道，检查工具是否支持
		if channel != "" && len(entry.Channels) > 0 {
			supported := false
			for _, c := range entry.Channels {
				if c == channel {
					supported = true
					break
				}
			}
			if !supported {
				continue
			}
		}
		entries = append(entries, entry)
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
			Description: "Updated persona block. LIMIT: 2000 chars. Recommended: 500-1500 chars. WARNING: This will COMPLETELY REPLACE existing content. Return empty string if no changes needed.",
			Required:    true,
		},
		{
			Name:        "human",
			Type:        "string",
			Description: "Updated human block. LIMIT: 2000 chars. Recommended: 300-1000 chars. WARNING: This will COMPLETELY REPLACE existing content. Return empty string if no changes needed.",
			Required:    true,
		},
		{
			Name:        "working_context",
			Type:        "string",
			Description: "Updated working context block. LIMIT: 4000 chars. Recommended: 500-2000 chars. WARNING: This will COMPLETELY REPLACE existing content. Return empty string if no changes needed.",
			Required:    true,
		},
		{
			Name:        "archival_entries",
			Type:        "array",
			Description: "List of detailed facts/events to archive. Recommended: 100-500 chars per entry. Each entry is a string.",
			Required:    false,
		},
		{
			Name:        "history_entry",
			Type:        "string",
			Description: "A paragraph summarizing key events/decisions. Recommended: 50-200 chars. Start with [YYYY-MM-DD HH:MM].",
			Required:    true,
		},
		{
			Name:        "entries_to_delete",
			Type:        "array",
			Description: "List of existing memory IDs to delete (for deduplication/conflict resolution). Only include IDs that should be replaced or removed.",
			Required:    false,
			Items:       &llm.ToolParamItems{Type: "string"},
		},
	}
}

// consolidateMemoryArgs consolidates memory with deduplication and conflict detection.
type consolidateMemoryArgs struct {
	Persona         string   `json:"persona"`
	Human           string   `json:"human"`
	WorkingContext  string   `json:"working_context"`
	ArchivalEntries []string `json:"archival_entries"`
	HistoryEntry    string   `json:"history_entry"`
	EntriesToDelete []string `json:"entries_to_delete"`
}

// ExistingMemory represents a similar memory found during deduplication search.
type ExistingMemory struct {
	ID         string  `json:"id"`
	Content    string  `json:"content"`
	Similarity float32 `json:"similarity"`
}
