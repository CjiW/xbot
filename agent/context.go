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

// BuildMessages 构建完整的 LLM 消息列表
// 拼接顺序经过优化以最大化 KV-cache 命中率：
//
//	固定提示词 → Self Profile（很少变） → Skills（相对稳定） → Memory（会变化） → User Profile（会变化） → Time（每次都变）
func BuildMessages(history []llm.ChatMessage, userContent string, channel string, mem memory.MemoryProvider, workDir string, skillsCatalog string, agentsCatalog string, promptLoader *PromptLoader, senderName string) []llm.ChatMessage {
	now := time.Now().Format("2006-01-02 15:04:05 MST")

	// 渲染固定部分的模板（不含时间戳）
	systemContent := promptLoader.Render(PromptData{
		Channel: channel,
		WorkDir: workDir,
	})

	// 注入 skills 目录（让 LLM 按需用 Read 工具加载 SKILL.md）
	if skillsCatalog != "" {
		systemContent += "\n" + skillsCatalog
	}

	// 注入 agents 目录（让 LLM 知道可用的 SubAgent 角色）
	if agentsCatalog != "" {
		systemContent += "\n" + agentsCatalog
	}

	// SubAgent 使用指南（鼓励 LLM 主动调用 SubAgent）
	systemContent += "\n\n## SubAgent 使用指南\n\n当任务涉及以下场景时，**强烈建议**使用 SubAgent 工具来委托任务：\n- 探索项目结构、查找文件、搜索代码 → 使用 explorer\n- 代码审查、发现潜在 bug → 使用 code-reviewer\n- 编写测试、验证功能 → 使用 tester\n\n使用方式：在 task 中清晰描述任务目标和上下文，SubAgent 会独立完成并返回结果。\n\n例如：\n- \"探索 agent 目录的结构，找出所有处理消息的逻辑\"\n- \"审查刚才提交的代码变更，检查是否有潜在 bug\"\n- \"为 tools/shell.go 添加单元测试\"\n\n**注意**：SubAgent 是独立的工作单元，不会继承当前对话的历史记录。请在 task 中提供足够的上下文信息。"

	// 注入长期记忆（Letta 模式下包含 Core Memory blocks + archival summary）
	if mem != nil {
		memCtx, err := mem.Recall(context.TODO(), userContent)
		if err != nil {
			log.WithError(err).Warn("Failed to get memory context")
		} else if memCtx != "" {
			systemContent += "\n# Memory\n\n" + memCtx + "\n"
		}
	}

	// 注入当前发送者名称
	if senderName != "" {
		systemContent += fmt.Sprintf("\n## Current Sender\nName: %s\n", senderName)
	}

	messages := make([]llm.ChatMessage, 0, len(history)+2)
	messages = append(messages, llm.NewSystemMessage(systemContent))
	messages = append(messages, history...)

	// 用户消息中注入时间戳和发送者标识
	var userMsg string
	if senderName != "" {
		userMsg = fmt.Sprintf("[%s] [%s]\n%s", now, senderName, userContent)
	} else {
		userMsg = fmt.Sprintf("[%s]\n%s", now, userContent)
	}
	userMsg = fmt.Sprintf("%s\n\n[系统引导] 在执行任何操作前，**必须**先用`search_tools`搜索工具库尝试寻找工具。\n- 搜索实时信息 → web_search（搜索引擎，不是浏览网页）\n- 浏览/获取网页内容 → Fetch\n- 如果需要查找或使用 skill，请使用 `Skill` 工具（不是 search_tools）\n- search_tools 仅用于搜索其他工具\n现在时间：%s\n", userMsg, now)
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
