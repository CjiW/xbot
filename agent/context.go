package agent

import (
	"bytes"
	"fmt"
	"os"
	"sync"
	"text/template"
	"time"
	"xbot/llm"

	log "xbot/logger"
)

// defaultSystemPrompt 系统提示词模板
// 注意：模板中不包含时间戳，时间戳在 BuildMessages 中动态拼接到末尾
// 拼接顺序经过优化以最大化 KV-cache 命中率：
//
//	固定内容（模板渲染） → Skills（相对稳定） → Memory（会变化） → Time（每次都变）
const defaultSystemPrompt = `You are xbot, a helpful AI assistant.

## Guidelines
- Be concise, accurate, and helpful
- Use tools when needed to accomplish tasks
- Explain what you're doing before taking actions
- Ask for clarification when the request is ambiguous

## Available Channels
You are communicating through the "{{.Channel}}" channel. You shoud always respond in markdown format(e.g. *bold*, _italic_, [a](local/a.txt)).

## Working Environment
- Working directory: {{.WorkDir}} (Shell commands run here; use relative paths when possible)
- Internal data: .xbot/ (session, skills — managed automatically)

## Memory Files
- Long-term memory: {{.MemoryDir}}/MEMORY.md (always loaded below)
- History log: {{.MemoryDir}}/HISTORY.md (grep-searchable event log)

When remembering something important, write to MEMORY.md in the working directory.
To recall past events, grep HISTORY.md in the working directory.
`

// PromptData 模板渲染数据
type PromptData struct {
	Channel   string
	WorkDir   string
	MemoryDir string
}

// PromptLoader 负责加载和渲染系统提示词模板
type PromptLoader struct {
	filePath string
	mu       sync.RWMutex
	tmpl     *template.Template
	lastMod  time.Time
}

// NewPromptLoader 创建 PromptLoader
// filePath 为空或文件不存在时，使用内置默认模板
func NewPromptLoader(filePath string) *PromptLoader {
	pl := &PromptLoader{filePath: filePath}
	pl.load()
	return pl
}

// load 加载模板（从文件或内置默认值）
func (pl *PromptLoader) load() {
	if pl.filePath != "" {
		if err := pl.loadFromFile(); err == nil {
			return
		} else {
			log.WithError(err).WithField("path", pl.filePath).Warn("Failed to load prompt file, using default")
		}
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.tmpl = template.Must(template.New("system").Parse(defaultSystemPrompt))
	pl.lastMod = time.Time{}
}

// loadFromFile 从文件加载模板
func (pl *PromptLoader) loadFromFile() error {
	info, err := os.Stat(pl.filePath)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(pl.filePath)
	if err != nil {
		return err
	}

	tmpl, err := template.New("system").Parse(string(content))
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	pl.mu.Lock()
	defer pl.mu.Unlock()
	pl.tmpl = tmpl
	pl.lastMod = info.ModTime()
	log.WithField("path", pl.filePath).Info("System prompt loaded from file")
	return nil
}

// reload 检查文件是否更新，如果更新则重新加载
func (pl *PromptLoader) reload() {
	if pl.filePath == "" {
		return
	}
	info, err := os.Stat(pl.filePath)
	if err != nil {
		return
	}
	pl.mu.RLock()
	needReload := info.ModTime().After(pl.lastMod)
	pl.mu.RUnlock()
	if needReload {
		if err := pl.loadFromFile(); err != nil {
			log.WithError(err).Warn("Failed to reload prompt file, keeping current template")
		} else {
			log.Info("System prompt reloaded (file changed)")
		}
	}
}

// Render 渲染系统提示词
// 每次调用时检查文件是否更新，支持热加载
func (pl *PromptLoader) Render(data PromptData) string {
	pl.reload()

	pl.mu.RLock()
	tmpl := pl.tmpl
	pl.mu.RUnlock()

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.WithError(err).Error("Failed to render system prompt template")
		// fallback: 使用简单格式化
		return fmt.Sprintf("You are xbot, a helpful AI assistant.\nChannel: %s\nWorkDir: %s",
			data.Channel, data.WorkDir)
	}
	return buf.String()
}

// BuildMessages 构建完整的 LLM 消息列表
// 拼接顺序经过优化以最大化 KV-cache 命中率：
//
//	固定提示词 → Skills（相对稳定） → Memory（会变化） → Time（每次都变）
func BuildMessages(history []llm.ChatMessage, userContent string, channel string, memory *MemoryStore, memoryDir string, workDir string, skillsPrompt string, promptLoader *PromptLoader) []llm.ChatMessage {
	now := time.Now().Format("2006-01-02 15:04:05 MST")

	// 渲染固定部分的模板（不含时间戳）
	systemContent := promptLoader.Render(PromptData{
		Channel:   channel,
		WorkDir:   workDir,
		MemoryDir: memoryDir,
	})

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

// cronSystemPrompt Cron 专用系统提示词（简洁，无记忆和技能）
const cronSystemPrompt = `You are xbot executing a scheduled cron task.

## Guidelines
- You are processing a scheduled reminder/task
- Execute the task directly and concisely
- Use tools when needed
- Report results clearly

## Working Environment
- Working directory: %s

Current Time: %s
`

// BuildCronMessages 构建 cron 专用消息（无历史上下文）
func BuildCronMessages(task string, workDir string) []llm.ChatMessage {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	systemContent := fmt.Sprintf(cronSystemPrompt, workDir, now)

	return []llm.ChatMessage{
		llm.NewSystemMessage(systemContent),
		llm.NewUserMessage(task),
	}
}
