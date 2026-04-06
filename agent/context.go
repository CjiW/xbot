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
)

// defaultSystemPrompt 最小 fallback，仅在 embed 和文件都不存在时使用。
const defaultSystemPrompt = `你是 xbot。渠道：{{.Channel}} | 工作目录：{{.WorkDir}} | 当前目录：{{.CWD}}
`

// PromptData 模板渲染数据
type PromptData struct {
	Channel        string
	WorkDir        string
	CWD            string // 当前工作目录（始终有值，默认等于 WorkDir）
	MemoryProvider string // "flat" or "letta"
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

// load 加载模板（从文件 → 内嵌默认 → 最小 fallback）
func (pl *PromptLoader) load() {
	if pl.filePath != "" {
		if err := pl.loadFromFile(); err == nil {
			return
		} else {
			log.WithError(err).WithField("path", pl.filePath).Warn("Failed to load prompt file, trying embedded default")
		}
	}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	// 优先使用嵌入的完整默认 prompt
	if ep := EmbeddedPrompt(); ep != "" {
		if t, err := template.New("system").Parse(ep); err != nil {
			log.WithError(err).Error("Failed to parse embedded prompt, using minimal fallback")
		} else {
			pl.tmpl = t
			pl.lastMod = time.Time{}
			return
		}
	}
	// 最终 fallback
	if t, err := template.New("system").Parse(defaultSystemPrompt); err != nil {
		log.Fatalf("Failed to parse default system prompt template: %v", err)
	} else {
		pl.tmpl = t
	}
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

// initPipelines 初始化 Agent 的消息构建管道。
// 在 Agent 创建时调用一次，后续通过 pipeline.Use/Remove 动态调整。
func (a *Agent) initPipelines(memoryProvider string) {
	promptWorkDir := a.workDir
	if a.sandboxMode == "docker" {
		promptWorkDir = "/workspace"
	}

	// 主 pipeline：用于普通消息和卡片响应
	a.pipeline = NewMessagePipeline(
		NewProjectHintMiddleware(a.multiSession.ArchivalService),
		NewSystemPromptMiddleware(a.promptLoader, memoryProvider),
		NewSkillsCatalogMiddleware(),
		NewAgentsCatalogMiddleware(),
		NewMemoryMiddleware(),
		NewSenderInfoMiddleware(),
		NewLanguageMiddleware(a.settingsSvc),
		NewUserMessageMiddleware(memoryProvider),
	)

	// Cron pipeline：用于定时任务（简洁，无记忆和技能）
	a.cronPipeline = NewMessagePipeline(
		NewCronSystemPromptMiddleware(promptWorkDir),
	)
}

// Pipeline 返回 Agent 的主消息构建管道，支持运行时动态增删中间件。
func (a *Agent) Pipeline() *MessagePipeline {
	return a.pipeline
}

// CronPipeline 返回 Agent 的 Cron 消息构建管道。
func (a *Agent) CronPipeline() *MessagePipeline {
	return a.cronPipeline
}

// NewMessageContext 创建一个预填充的 MessageContext，用于主 pipeline。
// 调用方设置动态字段（Extra 中的 ExtraKeySkillsCatalog、ExtraKeyAgentsCatalog、ExtraKeyMemoryProvider）后，
// 传入 pipeline.Run(mc) 执行。
func NewMessageContext(ctx context.Context, userContent string, history []llm.ChatMessage, channel, workDir, senderName, senderID, chatID string) *MessageContext {
	return &MessageContext{
		Ctx:         ctx,
		SystemParts: make(map[string]string),
		UserContent: userContent,
		History:     history,
		Channel:     channel,
		WorkDir:     workDir,
		SenderName:  senderName,
		SenderID:    senderID,
		ChatID:      chatID,
		Extra:       make(map[string]any),
	}
}

// NewCronMessageContext 创建一个 Cron 专用的 MessageContext。
func NewCronMessageContext(task string) *MessageContext {
	return &MessageContext{
		SystemParts: make(map[string]string),
		UserContent: task,
		Extra:       make(map[string]any),
	}
}
