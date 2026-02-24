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
You are communicating through the "%s" channel. You shoud always respond in markdown format(e.g. *bold*, _italic_, [a](local/a.txt)).

## Working Environment
- Working directory: %s (Shell commands run here; use relative paths when possible)
- Internal data: .xbot/ (session, skills — managed automatically)

## Memory Files
- Long-term memory: %s/MEMORY.md (always loaded below)
- History log: %s/HISTORY.md (grep-searchable event log)

When remembering something important, write to MEMORY.md in the working directory.
To recall past events, grep HISTORY.md in the working directory.
`

// BuildMessages 构建完整的 LLM 消息列表
func BuildMessages(history []llm.ChatMessage, userContent string, channel string, memory *MemoryStore, memoryDir string, workDir string, skillsPrompt string) []llm.ChatMessage {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	systemContent := fmt.Sprintf(defaultSystemPrompt, now, channel, workDir, memoryDir, memoryDir)

	// 注入长期记忆
	if memory != nil {
		memCtx := memory.GetMemoryContext()
		if memCtx != "" {
			systemContent += "\n# Memory\n\n" + memCtx + "\n"
		}
	}

	// 注入已激活的 skills
	if skillsPrompt != "" {
		systemContent += "\n" + skillsPrompt
	}

	messages := make([]llm.ChatMessage, 0, len(history)+2)
	messages = append(messages, llm.NewSystemMessage(systemContent))
	messages = append(messages, history...)
	messages = append(messages, llm.NewUserMessage(userContent))
	return messages
}
