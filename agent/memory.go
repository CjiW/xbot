package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// saveMemoryTool 内部 LLM 工具定义，用于记忆合并时让 LLM 以结构化方式输出结果
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

// saveMemoryArgs save_memory 工具的参数
type saveMemoryArgs struct {
	HistoryEntry string `json:"history_entry"`
	MemoryUpdate string `json:"memory_update"`
}

// MemoryStore 双层记忆：MEMORY.md（长期事实）+ HISTORY.md（可 grep 搜索的事件日志）
type MemoryStore struct {
	mu          sync.Mutex
	memoryDir   string
	memoryFile  string // MEMORY.md 路径
	historyFile string // HISTORY.md 路径
}

// NewMemoryStore 创建 MemoryStore，目录不存在会自动创建
func NewMemoryStore(dir string) *MemoryStore {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.WithError(err).Error("Failed to create memory directory")
	}
	return &MemoryStore{
		memoryDir:   dir,
		memoryFile:  filepath.Join(dir, "MEMORY.md"),
		historyFile: filepath.Join(dir, "HISTORY.md"),
	}
}

// Dir 返回记忆文件目录路径
func (m *MemoryStore) Dir() string {
	return m.memoryDir
}

// ReadLongTerm 读取 MEMORY.md 内容
func (m *MemoryStore) ReadLongTerm() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.memoryFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("Failed to read MEMORY.md")
		}
		return ""
	}
	return string(data)
}

// WriteLongTerm 覆写 MEMORY.md
func (m *MemoryStore) WriteLongTerm(content string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.WriteFile(m.memoryFile, []byte(content), 0o644); err != nil {
		log.WithError(err).Error("Failed to write MEMORY.md")
	}
}

// AppendHistory 向 HISTORY.md 追加一条条目
func (m *MemoryStore) AppendHistory(entry string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, err := os.OpenFile(m.historyFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.WithError(err).Error("Failed to open HISTORY.md for append")
		return
	}
	defer f.Close()
	_, _ = f.WriteString(strings.TrimRight(entry, "\n") + "\n\n")
}

// GetMemoryContext 返回格式化的长期记忆文本，用于注入系统提示词
func (m *MemoryStore) GetMemoryContext() string {
	content := m.ReadLongTerm()
	if content == "" {
		return ""
	}
	return "## Long-term Memory\n" + content
}

// Consolidate 使用 LLM 将旧消息合并到 MEMORY.md 和 HISTORY.md
//
// archiveAll=true 时合并所有消息（/new 命令时使用）
// archiveAll=false 时只合并超出窗口的旧消息
//
// 返回合并后的 lastConsolidated 指针值和是否成功
func (m *MemoryStore) Consolidate(
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
		log.Infof("Memory consolidation (archive_all): %d messages", len(messages))
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
		log.Infof("Memory consolidation: %d to consolidate, %d keep", len(oldMessages), keepCount)
	}

	// 格式化旧消息为文本
	var lines []string
	for _, msg := range oldMessages {
		if msg.Content == "" {
			continue
		}
		role := strings.ToUpper(msg.Role)
		// 工具消息标注工具名
		toolHint := ""
		if msg.Role == "tool" && msg.ToolName != "" {
			toolHint = fmt.Sprintf(" [tool: %s]", msg.ToolName)
		}
		// assistant 消息标注工具调用
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

	currentMemory := m.ReadLongTerm()
	memoryDisplay := currentMemory
	if memoryDisplay == "" {
		memoryDisplay = "(empty)"
	}

	prompt := fmt.Sprintf(`Process this conversation and call the save_memory tool with your consolidation.

## Current Long-term Memory
%s

## Conversation to Process
%s`, memoryDisplay, strings.Join(lines, "\n"))

	// 调用 LLM 进行合并
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

	// 解析 save_memory 工具参数
	var args saveMemoryArgs
	if err := json.Unmarshal([]byte(resp.ToolCalls[0].Arguments), &args); err != nil {
		log.WithError(err).Error("Memory consolidation: failed to parse save_memory arguments")
		return lastConsolidated, false
	}

	// 写入 HISTORY.md
	if args.HistoryEntry != "" {
		m.AppendHistory(args.HistoryEntry)
	}

	// 写入 MEMORY.md（仅当有变化时）
	if args.MemoryUpdate != "" && args.MemoryUpdate != currentMemory {
		m.WriteLongTerm(args.MemoryUpdate)
	}

	if archiveAll {
		newLastConsolidated = 0
	} else {
		newLastConsolidated = len(messages) - keepCount
	}
	log.Infof("Memory consolidation done: %d messages, lastConsolidated=%d", len(messages), newLastConsolidated)
	return newLastConsolidated, true
}
