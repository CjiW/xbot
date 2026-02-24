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

const defaultSystemPrompt = `You are xbot, a helpful AI assistant.

## Current Time
{{.Time}}

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
	Time      string
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
		return fmt.Sprintf("You are xbot, a helpful AI assistant.\nCurrent Time: %s\nChannel: %s\nWorkDir: %s",
			data.Time, data.Channel, data.WorkDir)
	}
	return buf.String()
}

// BuildMessages 构建完整的 LLM 消息列表
func BuildMessages(history []llm.ChatMessage, userContent string, channel string, memory *MemoryStore, memoryDir string, workDir string, skillsPrompt string, promptLoader *PromptLoader) []llm.ChatMessage {
	now := time.Now().Format("2006-01-02 15:04:05 MST")

	systemContent := promptLoader.Render(PromptData{
		Time:      now,
		Channel:   channel,
		WorkDir:   workDir,
		MemoryDir: memoryDir,
	})

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
