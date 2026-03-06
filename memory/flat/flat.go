package flat

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
)

// FlatMemory 全量注入式记忆（现有逻辑的接口化包装）。
// 所有长期记忆全量注入 system prompt，不做按需检索。
type FlatMemory struct {
	tenantID  int64
	memorySvc *sqlite.MemoryService
}

var _ memory.MemoryProvider = (*FlatMemory)(nil)

// New 创建 FlatMemory 实例。
func New(tenantID int64, memorySvc *sqlite.MemoryService) *FlatMemory {
	return &FlatMemory{
		tenantID:  tenantID,
		memorySvc: memorySvc,
	}
}

// Recall 返回全量长期记忆（忽略 query 参数）。
func (m *FlatMemory) Recall(_ context.Context, _ string) (string, error) {
	content, err := m.memorySvc.ReadLongTerm(m.tenantID)
	if err != nil {
		return "", err
	}
	if content == "" {
		return "", nil
	}
	return "## Long-term Memory\n" + content, nil
}

// Memorize 使用 LLM 合并旧消息到长期记忆和事件历史。
func (m *FlatMemory) Memorize(ctx context.Context, input memory.MemorizeInput) (memory.MemorizeResult, error) {
	messages := input.Messages
	lastConsolidated := input.LastConsolidated
	archiveAll := input.ArchiveAll
	memoryWindow := input.MemoryWindow

	var oldMessages []llm.ChatMessage
	keepCount := 0

	if archiveAll {
		oldMessages = messages
		log.WithField("tenant_id", m.tenantID).Infof("Memory consolidation (archive_all): %d messages", len(messages))
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
		newLC := 0
		if !archiveAll {
			newLC = len(messages) - keepCount
		}
		return memory.MemorizeResult{NewLastConsolidated: newLC, OK: true}, nil
	}

	currentMemory, err := m.memorySvc.ReadLongTerm(m.tenantID)
	if err != nil {
		log.WithError(err).Error("Failed to read long-term memory for consolidation")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
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

	resp, err := input.LLMClient.Generate(ctx, input.Model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a memory consolidation agent. Call the save_memory tool with your consolidation of the conversation."),
		llm.NewUserMessage(prompt),
	}, saveMemoryTool)
	if err != nil {
		log.WithError(err).Error("Memory consolidation LLM call failed")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	if !resp.HasToolCalls() {
		log.Warn("Memory consolidation: LLM did not call save_memory, skipping")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	var args saveMemoryArgs
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Arguments), &args); err != nil {
		log.WithError(err).Error("Memory consolidation: failed to parse save_memory arguments")
		return memory.MemorizeResult{NewLastConsolidated: lastConsolidated, OK: false}, nil
	}

	if args.HistoryEntry != "" {
		if err := m.memorySvc.AppendHistory(m.tenantID, args.HistoryEntry); err != nil {
			log.WithError(err).Error("Failed to append history entry")
		}
	}

	if args.MemoryUpdate != "" && args.MemoryUpdate != currentMemory {
		if err := m.memorySvc.WriteLongTerm(m.tenantID, args.MemoryUpdate); err != nil {
			log.WithError(err).Error("Failed to write long-term memory")
		}
	}

	newLC := 0
	if !archiveAll {
		newLC = len(messages) - keepCount
	}
	log.WithField("tenant_id", m.tenantID).Infof("Memory consolidation done: lastConsolidated=%d", newLC)
	return memory.MemorizeResult{NewLastConsolidated: newLC, OK: true}, nil
}

// Close 释放资源（FlatMemory 无需清理）。
func (m *FlatMemory) Close() error {
	return nil
}

// --- save_memory tool definition ---

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
