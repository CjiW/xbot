package agent

import (
	"context"
	"fmt"
	"time"

	"xbot/memory"
)

// --- Priority 0-99: 基础设施 ---

// SystemPromptMiddleware 注入基础系统提示词模板（prompt.md 渲染结果）
type SystemPromptMiddleware struct {
	loader *PromptLoader
}

func NewSystemPromptMiddleware(loader *PromptLoader) *SystemPromptMiddleware {
	return &SystemPromptMiddleware{loader: loader}
}

func (m *SystemPromptMiddleware) Name() string  { return "system_prompt" }
func (m *SystemPromptMiddleware) Priority() int { return 0 }

func (m *SystemPromptMiddleware) Process(mc *MessageContext) error {
	content := m.loader.Render(PromptData{
		Channel: mc.Channel,
		WorkDir: mc.WorkDir,
	})
	mc.SystemParts["00_base"] = content
	return nil
}

// --- Priority 100-199: 上下文注入 ---

// SkillsCatalogMiddleware 注入 Skills 目录。
// 从 MessageContext.Extra[ExtraKeySkillsCatalog] 读取动态内容。
type SkillsCatalogMiddleware struct{}

func NewSkillsCatalogMiddleware() *SkillsCatalogMiddleware {
	return &SkillsCatalogMiddleware{}
}

func (m *SkillsCatalogMiddleware) Name() string  { return "skills_catalog" }
func (m *SkillsCatalogMiddleware) Priority() int { return 100 }

func (m *SkillsCatalogMiddleware) Process(mc *MessageContext) error {
	catalog, _ := mc.GetExtraString(ExtraKeySkillsCatalog)
	if catalog != "" {
		mc.SystemParts["10_skills"] = catalog
	}
	return nil
}

// AgentsCatalogMiddleware 注入 Agents 目录。
// 从 MessageContext.Extra[ExtraKeyAgentsCatalog] 读取动态内容。
type AgentsCatalogMiddleware struct{}

func NewAgentsCatalogMiddleware() *AgentsCatalogMiddleware {
	return &AgentsCatalogMiddleware{}
}

func (m *AgentsCatalogMiddleware) Name() string  { return "agents_catalog" }
func (m *AgentsCatalogMiddleware) Priority() int { return 110 }

func (m *AgentsCatalogMiddleware) Process(mc *MessageContext) error {
	catalog, _ := mc.GetExtraString(ExtraKeyAgentsCatalog)
	if catalog != "" {
		mc.SystemParts["15_agents"] = catalog
	}
	return nil
}

// MemoryMiddleware 注入长期记忆。
// 从 MessageContext.Extra[ExtraKeyMemoryProvider] 读取动态 MemoryProvider。
type MemoryMiddleware struct{}

func NewMemoryMiddleware() *MemoryMiddleware {
	return &MemoryMiddleware{}
}

func (m *MemoryMiddleware) Name() string  { return "memory" }
func (m *MemoryMiddleware) Priority() int { return 120 }

func (m *MemoryMiddleware) Process(mc *MessageContext) error {
	mem, ok := GetExtraTyped[memory.MemoryProvider](mc, ExtraKeyMemoryProvider)
	if !ok || mem == nil {
		return nil
	}
	ctx := mc.Ctx
	if ctx == nil {
		ctx = context.TODO()
	}
	memCtx, err := mem.Recall(ctx, mc.UserContent)
	if err != nil {
		return fmt.Errorf("recall memory: %w", err)
	}
	if memCtx != "" {
		mc.SystemParts["20_memory"] = "# Memory\n\n" + memCtx + "\n"
	}
	return nil
}

// SenderInfoMiddleware 注入发送者信息到系统提示词
type SenderInfoMiddleware struct{}

func NewSenderInfoMiddleware() *SenderInfoMiddleware {
	return &SenderInfoMiddleware{}
}

func (m *SenderInfoMiddleware) Name() string  { return "sender_info" }
func (m *SenderInfoMiddleware) Priority() int { return 130 }

func (m *SenderInfoMiddleware) Process(mc *MessageContext) error {
	if mc.SenderName != "" {
		mc.SystemParts["30_sender"] = fmt.Sprintf("\n## Current Sender\nName: %s\n", mc.SenderName)
	}
	return nil
}

// --- Priority 200-299: 用户消息处理 ---

// systemGuideText 系统引导文本，提示 Agent 使用正确的工具搜索策略。
const systemGuideText = `[系统引导] 在执行任何操作前，**必须**先用` + "`search_tools`" + `搜索工具库尝试寻找工具。
- 搜索实时信息 → web_search（搜索引擎，不是浏览网页）
- 浏览/获取网页内容 → Fetch
- 如果需要查找或使用 skill，请使用 ` + "`Skill`" + ` 工具（不是 search_tools）
- search_tools 仅用于搜索其他工具
`

// UserMessageMiddleware 构建最终的用户消息（注入时间戳、发送者标识、系统引导）
type UserMessageMiddleware struct{}

func NewUserMessageMiddleware() *UserMessageMiddleware {
	return &UserMessageMiddleware{}
}

func (m *UserMessageMiddleware) Name() string  { return "user_message" }
func (m *UserMessageMiddleware) Priority() int { return 200 }

func (m *UserMessageMiddleware) Process(mc *MessageContext) error {
	now := time.Now().Format("2006-01-02 15:04:05 MST")

	var userMsg string
	if mc.SenderName != "" {
		userMsg = fmt.Sprintf("[%s] [%s]\n%s", now, mc.SenderName, mc.UserContent)
	} else {
		userMsg = fmt.Sprintf("[%s]\n%s", now, mc.UserContent)
	}

	userMsg = fmt.Sprintf("%s\n\n%s现在时间：%s\n", userMsg, systemGuideText, now)

	mc.UserMessage = userMsg
	return nil
}

// --- Cron 专用中间件 ---

// CronSystemPromptMiddleware 注入 Cron 专用系统提示词
type CronSystemPromptMiddleware struct {
	workDir string
}

func NewCronSystemPromptMiddleware(workDir string) *CronSystemPromptMiddleware {
	return &CronSystemPromptMiddleware{workDir: workDir}
}

func (m *CronSystemPromptMiddleware) Name() string  { return "cron_system_prompt" }
func (m *CronSystemPromptMiddleware) Priority() int { return 0 }

func (m *CronSystemPromptMiddleware) Process(mc *MessageContext) error {
	now := time.Now().Format("2006-01-02 15:04:05 MST")
	mc.SystemParts["00_base"] = fmt.Sprintf(cronSystemPrompt, m.workDir, now)
	// Cron 消息不需要额外处理 UserMessage，直接使用原始内容
	mc.UserMessage = mc.UserContent
	return nil
}
