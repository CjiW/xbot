package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xbot/memory"
	"xbot/prompt"
)

// --- Priority 0-99: 基础设施 ---

// SystemPromptMiddleware 注入基础系统提示词模板（prompt.md 渲染结果）
type SystemPromptMiddleware struct {
	loader         *PromptLoader
	memoryProvider string
}

func NewSystemPromptMiddleware(loader *PromptLoader, memoryProvider string) *SystemPromptMiddleware {
	return &SystemPromptMiddleware{loader: loader, memoryProvider: memoryProvider}
}

func (m *SystemPromptMiddleware) Name() string  { return "system_prompt" }
func (m *SystemPromptMiddleware) Priority() int { return 0 }

func (m *SystemPromptMiddleware) Process(mc *MessageContext) error {
	content := m.loader.Render(PromptData{
		Channel:        mc.Channel,
		WorkDir:        mc.WorkDir,
		CWD:            mc.CWD,
		MemoryProvider: m.memoryProvider,
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

// AgentsCatalogMiddleware injects available agents catalog.
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

// PermUsersConfig holds the permission control user configuration.
type PermUsersConfig struct {
	DefaultUser    string `json:"default_user"`
	PrivilegedUser string `json:"privileged_user"`
}

// PermissionControlMiddleware injects the permission control system prompt
// when the feature is enabled (at least one user is configured).
type PermissionControlMiddleware struct{}

func NewPermissionControlMiddleware() *PermissionControlMiddleware {
	return &PermissionControlMiddleware{}
}

func (m *PermissionControlMiddleware) Name() string  { return "permission_control" }
func (m *PermissionControlMiddleware) Priority() int { return 115 }

func (m *PermissionControlMiddleware) Process(mc *MessageContext) error {
	config, ok := GetExtraTyped[*PermUsersConfig](mc, ExtraKeyPermUsers)
	if !ok || config == nil {
		return nil
	}
	if config.DefaultUser == "" && config.PrivilegedUser == "" {
		return nil // feature disabled
	}

	var sb strings.Builder
	sb.WriteString("## Execution User Control\n\n")
	sb.WriteString("You can execute tools as a different OS user by passing the `run_as` parameter.\n")
	sb.WriteString("Available users are configured by the system administrator.\n\n")
	sb.WriteString("### Available Users\n")
	sb.WriteString("| User | Approval Required | Description |\n")
	sb.WriteString("|------|-------------------|-------------|\n")
	sb.WriteString("| (omit run_as) | None | Current process user |\n")
	if config.DefaultUser != "" {
		sb.WriteString(fmt.Sprintf("| %s | None | Default execution user |\n", config.DefaultUser))
	}
	if config.PrivilegedUser != "" {
		sb.WriteString(fmt.Sprintf("| %s | **Yes** | Privileged user — user must approve each use |\n", config.PrivilegedUser))
	}
	sb.WriteString("\n### Rules\n")
	sb.WriteString("- Omit `run_as` to execute as the current process user\n")
	if config.DefaultUser != "" {
		sb.WriteString(fmt.Sprintf("- Use `run_as: %q` for routine operations\n", config.DefaultUser))
	}
	if config.PrivilegedUser != "" {
		sb.WriteString(fmt.Sprintf("- Use `run_as: %q` ONLY when the task genuinely requires elevated privileges\n", config.PrivilegedUser))
		sb.WriteString("- Always explain WHY you need the privileged user when requesting it\n")
	}

	mc.SystemParts["14_perm_control"] = sb.String()
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

// LanguageMiddleware injects a language instruction into the system prompt
// based on the user's language setting. Priority 135 (after sender info).
type LanguageMiddleware struct {
	settingsSvc SettingsReader
}

// SettingsReader abstracts settings access for the middleware.
type SettingsReader interface {
	GetSettings(channelName, senderID string) (map[string]string, error)
}

// LanguageInstruction returns an LLM language instruction for the given language code.
func LanguageInstruction(lang string) string {
	switch lang {
	case "en":
		return "## Language\n\nAlways respond in English."
	case "zh":
		return "## Language\n\n始终使用中文回复。"
	case "ja":
		return "## Language\n\n常に日本語で返答してください。"
	default:
		return fmt.Sprintf("## Language\n\nAlways respond in %s.", lang)
	}
}

func NewLanguageMiddleware(svc SettingsReader) *LanguageMiddleware {
	return &LanguageMiddleware{settingsSvc: svc}
}

func (m *LanguageMiddleware) Name() string  { return "language" }
func (m *LanguageMiddleware) Priority() int { return 135 }

func (m *LanguageMiddleware) Process(mc *MessageContext) error {
	if m.settingsSvc == nil {
		return nil
	}
	vals, err := m.settingsSvc.GetSettings(mc.Channel, mc.SenderID)
	if err != nil {
		return nil
	}
	lang, ok := vals["language"]
	if !ok || lang == "" {
		return nil
	}
	// Map language code to a natural instruction for the LLM
	mc.SystemParts["32_language"] = LanguageInstruction(lang)
	return nil
}

// --- Priority 200-299: 用户消息处理 ---

// buildSystemGuideText 根据记忆模式生成系统引导文本。
// letta 模式下包含 search_tools 引导，flat 模式下不包含。
func buildSystemGuideText(memoryProvider string) string {
	if memoryProvider == "letta" {
		return prompt.UserMessageGuideLetta
	}
	return prompt.UserMessageGuideFlat
}

// UserMessageMiddleware 构建最终的用户消息（注入时间戳、发送者标识、系统引导）
type UserMessageMiddleware struct {
	memoryProvider string
}

func NewUserMessageMiddleware(memoryProvider string) *UserMessageMiddleware {
	return &UserMessageMiddleware{memoryProvider: memoryProvider}
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

	guide := buildSystemGuideText(m.memoryProvider)
	userMsg = fmt.Sprintf("%s\n\n%s现在时间：%s\n", userMsg, guide, now)

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
	cronPrompt := EmbeddedCronPrompt()
	if cronPrompt == "" {
		cronPrompt = "You are xbot executing a scheduled cron task.\n\n## Guidelines\n- You are processing a scheduled reminder/task\n- Execute the task directly and concisely\n- Use tools when needed\n- Report results clearly\n- WorkDir: %s\n- Time: %s\n"
	}
	mc.SystemParts["00_base"] = fmt.Sprintf(cronPrompt, m.workDir, now)
	// Cron 消息不需要额外处理 UserMessage，直接使用原始内容
	mc.UserMessage = mc.UserContent
	return nil
}
