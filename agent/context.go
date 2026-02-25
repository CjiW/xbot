package agent

import (
	"fmt"
	"time"
	"xbot/llm"
)

// defaultSystemPrompt 系统提示词模板
// 注意：拼接顺序经过优化以最大化 KV-cache 命中率
// 固定内容在前（Guidelines、Channels、WorkEnv），易变内容在后（Skills、Memory、Time）
const defaultSystemPrompt = `You are xbot, a helpful AI assistant.

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
// 拼接顺序：固定提示词 → Skills → Memory → Time（易变内容在后，最大化 KV-cache 命中）
func BuildMessages(history []llm.ChatMessage, userContent string, channel string, memory *MemoryStore, memoryDir string, workDir string, skillsPrompt string) []llm.ChatMessage {
	now := time.Now().Format("2006-01-02 15:04:05 MST")

	systemContent := fmt.Sprintf(defaultSystemPrompt, channel, workDir, memoryDir, memoryDir)

	// 注入已激活的 skills（相对稳定，放在 Memory 之前）
	if skillsPrompt != "" {
		systemContent += "\n" + skillsPrompt
	}

	// 注入长期记忆（会随合并变化，放在 Skills 之后）
	if memory != nil {
		memCtx := memory.GetMemoryContext()
		if memCtx != "" {
			systemContent += "\n# Memory\n\n" + memCtx + "\n"
		}
	}

	// 时间戳放在系统提示词最末尾（每次请求都变，放最后以最大化前缀缓存命中）
	systemContent += fmt.Sprintf("\n## Current Time\n%s\n", now)

	messages := make([]llm.ChatMessage, 0, len(history)+2)
	messages = append(messages, llm.NewSystemMessage(systemContent))
	messages = append(messages, history...)

	// 用户消息中也注入时间戳，确保模型在近期注意力范围内感知当前时间
	userMsg := fmt.Sprintf("[%s]\n%s", now, userContent)
	messages = append(messages, llm.NewUserMessage(userMsg))
	return messages
}
