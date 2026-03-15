package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"text/template"
	"time"

	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/memory/letta"
)

// defaultSystemPrompt 最小 fallback，仅在 prompt.md 文件不存在时使用。
const defaultSystemPrompt = `你是 xbot。渠道：{{.Channel}} | 工作目录：{{.WorkDir}}
`

// PromptData 模板渲染数据
type PromptData struct {
	Channel string
	WorkDir string
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

// BuildMessages 构建完整的 LLM 消息列表。
// 内部使用 MessagePipeline 实现，保留此函数签名以兼容现有调用方。
//
// 拼接顺序经过优化以最大化 KV-cache 命中率：
//
//	固定提示词 → Skills（相对稳定） → Agents → Memory（会变化） → Sender Info → Time（每次都变）
func BuildMessages(history []llm.ChatMessage, userContent string, channel string, mem memory.MemoryProvider, workDir string, skillsCatalog string, agentsCatalog string, promptLoader *PromptLoader, senderName string, senderID string) []llm.ChatMessage {
	pipeline := NewMessagePipeline(
		NewSystemPromptMiddleware(promptLoader),
		NewSkillsCatalogMiddleware(skillsCatalog),
		NewAgentsCatalogMiddleware(agentsCatalog),
		NewMemoryMiddleware(mem),
		NewSenderInfoMiddleware(),
		NewUserMessageMiddleware(),
	)

	mc := &MessageContext{
		Ctx:         letta.WithUserID(context.TODO(), senderID),
		SystemParts: make(map[string]string),
		UserContent: userContent,
		History:     history,
		Channel:     channel,
		WorkDir:     workDir,
		SenderName:  senderName,
		SenderID:    senderID,
	}

	return pipeline.Run(mc)
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

// BuildCronMessages 构建 cron 专用消息（无历史上下文）。
// 内部使用 MessagePipeline 实现。
func BuildCronMessages(task string, workDir string) []llm.ChatMessage {
	pipeline := NewMessagePipeline(
		NewCronSystemPromptMiddleware(workDir),
	)

	mc := &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: task,
	}

	return pipeline.Run(mc)
}
