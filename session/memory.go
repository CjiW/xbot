package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	log "xbot/logger"
	"xbot/storage/sqlite"
)

// TenantMemory handles memory operations for a single tenant
type TenantMemory struct {
	tenantID  int64
	memorySvc *sqlite.MemoryService
}

// saveMemoryTool is the LLM tool definition for memory consolidation
var saveMemoryTool = []llm.ToolDefinition{&saveMemoryToolDef{}}

type saveMemoryToolDef struct{}

func (t *saveMemoryToolDef) Name() string { return "save_memory" }
func (t *saveMemoryToolDef) Description() string {
	return "Save the memory consolidation result to persistent storage."
}
func (t *saveMemoryToolDef) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{
			Name:        "history_entry",
			Type:        "string",
			Description: "A paragraph (2-5 sentences) summarizing key events/decisions/topics. Start with [YYYY-MM-DD HH:MM]. Include detail useful for grep search.",
			Required:    true,
		},
		{
			Name:        "memory_update",
			Type:        "string",
			Description: "Full updated long-term memory as markdown. Include all existing facts plus new ones. Return unchanged if nothing new.",
			Required:    true,
		},
	}
}

type saveMemoryArgs struct {
	HistoryEntry string `json:"history_entry"`
	MemoryUpdate string `json:"memory_update"`
}

// ReadLongTerm retrieves the long-term memory content
func (m *TenantMemory) ReadLongTerm() (string, error) {
	return m.memorySvc.ReadLongTerm(m.tenantID)
}

// WriteLongTerm saves the long-term memory content
func (m *TenantMemory) WriteLongTerm(content string) error {
	return m.memorySvc.WriteLongTerm(m.tenantID, content)
}

// AppendHistory adds an entry to the event history
func (m *TenantMemory) AppendHistory(entry string) error {
	return m.memorySvc.AppendHistory(m.tenantID, entry)
}

// GetMemoryContext returns the formatted long-term memory for system prompt injection
func (m *TenantMemory) GetMemoryContext() (string, error) {
	content, err := m.ReadLongTerm()
	if err != nil {
		return "", err
	}
	if content == "" {
		return "", nil
	}
	return "## Long-term Memory\n" + content, nil
}

// Consolidate uses LLM to consolidate old messages into long-term memory and event history
//
// Parameters:
// - ctx: context for LLM call
// - messages: all messages in the session
// - lastConsolidated: current consolidation offset
// - llmClient: LLM client for consolidation
// - model: model name
// - archiveAll: if true, consolidate all messages; otherwise only consolidate messages outside the window
// - memoryWindow: the context window size
//
// Returns:
// - newLastConsolidated: the new consolidation offset
// - ok: true if consolidation succeeded
func (m *TenantMemory) Consolidate(
	ctx context.Context,
	messages []llm.ChatMessage,
	lastConsolidated int,
	llmClient llm.LLM,
	model string,
	archiveAll bool,
	memoryWindow int,
) (newLastConsolidated int, ok bool) {
	var oldMessages []llm.ChatMessage
	keepCount := 0

	if archiveAll {
		oldMessages = messages
		log.WithField("tenant_id", m.tenantID).Infof("Memory consolidation (archive_all): %d messages", len(messages))
	} else {
		keepCount = memoryWindow / 2
		if len(messages) <= keepCount {
			return lastConsolidated, true
		}
		if len(messages)-lastConsolidated <= 0 {
			return lastConsolidated, true
		}
		end := len(messages) - keepCount
		if lastConsolidated >= end {
			return lastConsolidated, true
		}
		oldMessages = messages[lastConsolidated:end]
		if len(oldMessages) == 0 {
			return lastConsolidated, true
		}
		log.WithField("tenant_id", m.tenantID).Infof("Memory consolidation: %d to consolidate, %d keep", len(oldMessages), keepCount)
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
		ts := time.Now().Format("2006-01-02 15:04")
		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "..."
		}
		lines = append(lines, fmt.Sprintf("[%s] %s%s: %s", ts, role, toolHint, content))
	}

	if len(lines) == 0 {
		if archiveAll {
			return 0, true
		}
		return len(messages) - keepCount, true
	}

	currentMemory, err := m.ReadLongTerm()
	if err != nil {
		log.WithError(err).Error("Failed to read long-term memory for consolidation")
		return lastConsolidated, false
	}

	memoryDisplay := currentMemory
	if memoryDisplay == "" {
		memoryDisplay = "(empty)"
	}

	prompt := fmt.Sprintf(`Process this conversation and call the save_memory tool with your consolidation.

## Current Long-term Memory
%s

## Conversation to Process
%s`, memoryDisplay, strings.Join(lines, "\n"))

	// Call LLM for consolidation
	resp, err := llmClient.Generate(ctx, model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a memory consolidation agent. Call the save_memory tool with your consolidation of the conversation."),
		llm.NewUserMessage(prompt),
	}, saveMemoryTool)
	if err != nil {
		log.WithError(err).Error("Memory consolidation LLM call failed")
		return lastConsolidated, false
	}

	if !resp.HasToolCalls() {
		log.Warn("Memory consolidation: LLM did not call save_memory, skipping")
		return lastConsolidated, false
	}

	// Parse save_memory tool arguments
	var args saveMemoryArgs
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Arguments), &args); err != nil {
		log.WithError(err).Error("Memory consolidation: failed to parse save_memory arguments")
		return lastConsolidated, false
	}

	// Write to event history
	if args.HistoryEntry != "" {
		if err := m.AppendHistory(args.HistoryEntry); err != nil {
			log.WithError(err).Error("Failed to append history entry")
		}
	}

	// Write long-term memory (only if changed)
	if args.MemoryUpdate != "" && args.MemoryUpdate != currentMemory {
		if err := m.WriteLongTerm(args.MemoryUpdate); err != nil {
			log.WithError(err).Error("Failed to write long-term memory")
		}
	}

	if archiveAll {
		newLastConsolidated = 0
	} else {
		newLastConsolidated = len(messages) - keepCount
	}
	log.WithField("tenant_id", m.tenantID).Infof("Memory consolidation done: lastConsolidated=%d", newLastConsolidated)
	return newLastConsolidated, true
}
