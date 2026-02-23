package agent

import (
	"fmt"
	"time"
	"xbot/llm"
)

const defaultSystemPrompt = `You are xbot, a helpful AI assistant.

## Current Time
%s

## Guidelines
- Be concise, accurate, and helpful
- Use tools when needed to accomplish tasks
- Explain what you're doing before taking actions
- Ask for clarification when the request is ambiguous

## Available Channels
You are communicating through the "%s" channel.

## Working Environment
- Working directory: %s (Shell commands run here; use relative paths when possible)
- Data directory: %s (persistent storage for session, memory, cron jobs, etc.)

## Memory Files
- Long-term memory: %s/MEMORY.md (always loaded below)
- History log: %s/HISTORY.md (grep-searchable event log)

When you learn important facts about the user or project, note that they will be automatically persisted.
`

// BuildMessages 构建完整的 LLM 消息列表
func BuildMessages(history []llm.ChatMessage, userContent string, channel string, memory *MemoryStore, memoryDir string, workDir string, dataDir string) []llm.ChatMessage {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	systemContent := fmt.Sprintf(defaultSystemPrompt, now, channel, workDir, dataDir, memoryDir, memoryDir)

	// 注入长期记忆
	if memory != nil {
		memCtx := memory.GetMemoryContext()
		if memCtx != "" {
			systemContent += "\n# Memory\n\n" + memCtx + "\n"
		}
	}

	messages := make([]llm.ChatMessage, 0, len(history)+2)
	messages = append(messages, llm.NewSystemMessage(systemContent))
	messages = append(messages, history...)
	messages = append(messages, llm.NewUserMessage(userContent))
	return messages
}
