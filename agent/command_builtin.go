package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"xbot/bus"
	"xbot/channel"
	"xbot/version"
)

// --- /new ---

type newCmd struct{}

func (c *newCmd) Name() string        { return "/new" }
func (c *newCmd) Aliases() []string   { return nil }
func (c *newCmd) Match(s string) bool { return strings.ToLower(s) == "/new" }
func (c *newCmd) Concurrent() bool    { return false } // mutates session

func (c *newCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handleNewSession(ctx, msg, tenantSession)
}

// --- /version ---

type versionCmd struct{}

func (c *versionCmd) Name() string        { return "/version" }
func (c *versionCmd) Aliases() []string   { return nil }
func (c *versionCmd) Match(s string) bool { return strings.ToLower(s) == "/version" }
func (c *versionCmd) Concurrent() bool    { return true } // stateless

func (c *versionCmd) Execute(_ context.Context, _ *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: version.Info(),
	}, nil
}

// --- /help ---

type helpCmd struct{}

func (c *helpCmd) Name() string        { return "/help" }
func (c *helpCmd) Aliases() []string   { return nil }
func (c *helpCmd) Match(s string) bool { return strings.ToLower(s) == "/help" }
func (c *helpCmd) Concurrent() bool    { return true } // stateless

func (c *helpCmd) Execute(_ context.Context, _ *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "xbot 命令:\n" +
			"/new — 开始新对话（归档记忆后重置）\n" +
			"/version — 显示版本信息\n" +
			"/prompt <query> — 预览完整提示词（不调用 LLM）\n" +
			"/help — 显示帮助\n" +
			"/set-llm — 设置自定义 LLM API\n" +
			"/unset-llm — 清除自定义 LLM 配置\n" +
			"/llm — 查看当前 LLM 配置\n" +
			"/models — 列出当前 API 可用模型\n" +
			"/set-model <model> — 设置当前使用的模型\n" +
			"/compress — 手动触发上下文压缩\n" +
			"/context info — 查看 token 统计\n" +
			"/context mode — 查看/切换压缩模式\n" +
			"/cancel — 取消当前正在处理的请求\n" +
			"!<command> — 快捷执行命令（跳过 LLM，直接在 sandbox 中运行）",
	}, nil
}

// --- /prompt ---

type promptCmd struct{}

func (c *promptCmd) Name() string      { return "/prompt" }
func (c *promptCmd) Aliases() []string { return nil }
func (c *promptCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/prompt" || strings.HasPrefix(lower, "/prompt ")
}
func (c *promptCmd) Concurrent() bool { return true } // read-only snapshot, no real-time requirement

func (c *promptCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handlePromptQuery(ctx, msg, tenantSession)
}

// --- /set-llm ---

type setLLMCmd struct{}

func (c *setLLMCmd) Name() string      { return "/set-llm" }
func (c *setLLMCmd) Aliases() []string { return nil }
func (c *setLLMCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/set-llm" || strings.HasPrefix(lower, "/set-llm ")
}
func (c *setLLMCmd) Concurrent() bool { return false } // mutates LLM config

func (c *setLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return a.handleSetLLM(ctx, msg)
}

// --- /llm ---

type getLLMCmd struct{}

func (c *getLLMCmd) Name() string        { return "/llm" }
func (c *getLLMCmd) Aliases() []string   { return nil }
func (c *getLLMCmd) Match(s string) bool { return strings.ToLower(s) == "/llm" }
func (c *getLLMCmd) Concurrent() bool    { return true } // read-only

func (c *getLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return a.handleGetLLM(ctx, msg)
}

// --- /unset-llm ---

type unsetLLMCmd struct{}

func (c *unsetLLMCmd) Name() string        { return "/unset-llm" }
func (c *unsetLLMCmd) Aliases() []string   { return nil }
func (c *unsetLLMCmd) Match(s string) bool { return strings.ToLower(s) == "/unset-llm" }
func (c *unsetLLMCmd) Concurrent() bool    { return false } // mutates LLM config

func (c *unsetLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return a.handleUnsetLLM(ctx, msg)
}

// --- /compress ---

type compressCmd struct{}

func (c *compressCmd) Name() string        { return "/compress" }
func (c *compressCmd) Aliases() []string   { return nil }
func (c *compressCmd) Match(s string) bool { return strings.ToLower(s) == "/compress" }
func (c *compressCmd) Concurrent() bool    { return false } // mutates session

func (c *compressCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handleCompress(ctx, msg, tenantSession)
}

// --- /context info --- (read-only, concurrent)

type contextInfoCmd struct{}

func (c *contextInfoCmd) Name() string      { return "/context" }
func (c *contextInfoCmd) Aliases() []string { return nil }
func (c *contextInfoCmd) Match(s string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	return trimmed == "/context" || trimmed == "/context info"
}
func (c *contextInfoCmd) Concurrent() bool { return true } // read-only

func (c *contextInfoCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handleContextInfo(ctx, msg, tenantSession)
}

// --- /context mode --- (stateful, NOT concurrent)

type contextModeCmd struct{}

func (c *contextModeCmd) Name() string      { return "/context mode" }
func (c *contextModeCmd) Aliases() []string { return nil }
func (c *contextModeCmd) Match(s string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	return strings.HasPrefix(trimmed, "/context mode")
}
func (c *contextModeCmd) Concurrent() bool { return false } // mutates runtime mode

func (c *contextModeCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	content := strings.TrimSpace(msg.Content)
	modeStr := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(content), "/context mode"))
	return a.handleContextMode(ctx, msg, modeStr)
}

// --- /models ---

type modelsCmd struct{}

func (c *modelsCmd) Name() string        { return "/models" }
func (c *modelsCmd) Aliases() []string   { return nil }
func (c *modelsCmd) Match(s string) bool { return strings.ToLower(s) == "/models" }
func (c *modelsCmd) Concurrent() bool    { return true } // read-only

func (c *modelsCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return a.handleModels(ctx, msg)
}

// --- /set-model ---

type setModelCmd struct{}

func (c *setModelCmd) Name() string      { return "/set-model" }
func (c *setModelCmd) Aliases() []string { return nil }
func (c *setModelCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/set-model" || strings.HasPrefix(lower, "/set-model ")
}
func (c *setModelCmd) Concurrent() bool { return false } // mutates LLM config

func (c *setModelCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return a.handleSetModel(ctx, msg)
}

// --- ! (bang command) ---

type bangCmd struct{}

func (c *bangCmd) Name() string      { return "!" }
func (c *bangCmd) Aliases() []string { return nil }
func (c *bangCmd) Match(s string) bool {
	_, ok := isBangCommand(s)
	return ok
}
func (c *bangCmd) Concurrent() bool { return true } // runs in sandbox, no session mutation

func (c *bangCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	cmd, _ := isBangCommand(msg.Content)
	return a.handleBangCommand(ctx, msg, cmd)
}

// --- /publish ---

type publishCmd struct{}

func (c *publishCmd) Name() string      { return "/publish" }
func (c *publishCmd) Aliases() []string { return nil }
func (c *publishCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "/publish ")
}
func (c *publishCmd) Concurrent() bool { return false }

func (c *publishCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	content := strings.TrimSpace(msg.Content)
	args := strings.TrimPrefix(strings.ToLower(content), "/publish ")
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/publish skill|agent <name>`"}, nil
	}
	entryType := parts[0]
	name := parts[1]
	if entryType != "skill" && entryType != "agent" {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "类型必须是 skill 或 agent"}, nil
	}
	if a.registryManager == nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}
	err := a.registryManager.Publish(entryType, name, msg.SenderID)
	if err != nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("发布失败：%v", err)}, nil
	}
	return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("✅ %s %q 已发布", entryType, name)}, nil
}

// --- /unpublish ---

type unpublishCmd struct{}

func (c *unpublishCmd) Name() string      { return "/unpublish" }
func (c *unpublishCmd) Aliases() []string { return nil }
func (c *unpublishCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "/unpublish ")
}
func (c *unpublishCmd) Concurrent() bool { return false }

func (c *unpublishCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	content := strings.TrimSpace(msg.Content)
	args := strings.TrimPrefix(strings.ToLower(content), "/unpublish ")
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/unpublish skill|agent <name>`"}, nil
	}
	entryType := parts[0]
	name := parts[1]
	if a.registryManager == nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}
	err := a.registryManager.Unpublish(entryType, name, msg.SenderID)
	if err != nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("取消发布失败：%v", err)}, nil
	}
	return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("✅ %s %q 已取消发布", entryType, name)}, nil
}

// --- /browse ---

type browseCmd struct{}

func (c *browseCmd) Name() string      { return "/browse" }
func (c *browseCmd) Aliases() []string { return nil }
func (c *browseCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/browse" || strings.HasPrefix(lower, "/browse ")
}
func (c *browseCmd) Concurrent() bool { return true }

func (c *browseCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	content := strings.TrimSpace(msg.Content)
	entryType := strings.TrimPrefix(strings.ToLower(content), "/browse ")
	entryType = strings.TrimSpace(entryType)

	if a.registryManager == nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}

	entries, err := a.registryManager.Browse(entryType, 20, 0)
	if err != nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("浏览失败：%v", err)}, nil
	}
	if len(entries) == 0 {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "🏪 市场暂无公开的 Skill/Agent"}, nil
	}

	var sb strings.Builder
	sb.WriteString("## 🏪 市场浏览\n\n")
	for i, e := range entries {
		typeLabel := "📦"
		if e.Type == "agent" {
			typeLabel = "🤖"
		}
		fmt.Fprintf(&sb, "%d. %s **%s** — %s\n", i+1, typeLabel, e.Name, e.Description)
		if e.Author != "" {
			fmt.Fprintf(&sb, "   作者：%s\n", e.Author)
		}
		fmt.Fprintf(&sb, "   安装：`/install %s %d`\n\n", e.Type, e.ID)
	}
	return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: sb.String()}, nil
}

// --- /install ---

type installCmd struct{}

func (c *installCmd) Name() string      { return "/install" }
func (c *installCmd) Aliases() []string { return nil }
func (c *installCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "/install ")
}
func (c *installCmd) Concurrent() bool { return false }

func (c *installCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	content := strings.TrimSpace(msg.Content)
	args := strings.TrimPrefix(strings.ToLower(content), "/install ")
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/install skill|agent <id>`"}, nil
	}
	entryType := parts[0]
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "ID 必须是数字"}, nil
	}
	if a.registryManager == nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}
	err = a.registryManager.Install(entryType, id, msg.SenderID)
	if err != nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("安装失败：%v", err)}, nil
	}
	return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("✅ %s #%d 已安装", entryType, id)}, nil
}

// --- /uninstall ---

type uninstallCmd struct{}

func (c *uninstallCmd) Name() string      { return "/uninstall" }
func (c *uninstallCmd) Aliases() []string { return nil }
func (c *uninstallCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "/uninstall ")
}
func (c *uninstallCmd) Concurrent() bool { return false }

func (c *uninstallCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	content := strings.TrimSpace(msg.Content)
	args := strings.TrimPrefix(strings.ToLower(content), "/uninstall ")
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/uninstall skill|agent <name>`"}, nil
	}
	entryType := parts[0]
	name := parts[1]
	if a.registryManager == nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}
	err := a.registryManager.Uninstall(entryType, name, msg.SenderID)
	if err != nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("卸载失败：%v", err)}, nil
	}
	return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("✅ %s %q 已卸载", entryType, name)}, nil
}

// --- /my ---

type myCmd struct{}

func (c *myCmd) Name() string      { return "/my" }
func (c *myCmd) Aliases() []string { return nil }
func (c *myCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "/my ")
}
func (c *myCmd) Concurrent() bool { return true }

func (c *myCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	content := strings.TrimSpace(msg.Content)
	subject := strings.TrimPrefix(strings.ToLower(content), "/my ")
	subject = strings.TrimSpace(subject)

	if a.registryManager == nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "RegistryManager 未初始化"}, nil
	}

	entryType := ""
	switch subject {
	case "skills":
		entryType = "skill"
	case "agents":
		entryType = "agent"
	default:
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/my skills` 或 `/my agents`"}, nil
	}

	published, installed, err := a.registryManager.ListMy(msg.SenderID, entryType)
	if err != nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("查询失败：%v", err)}, nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "## 我的 %s\n\n", subject)

	if len(published) > 0 {
		sb.WriteString("### 📤 已发布\n\n")
		for _, e := range published {
			fmt.Fprintf(&sb, "- **%s** — %s (ID:%d)\n", e.Name, e.Description, e.ID)
		}
		sb.WriteString("\n")
	}

	if len(installed) > 0 {
		sb.WriteString("### 📥 已安装\n\n")
		for _, item := range installed {
			fmt.Fprintf(&sb, "- %s\n", item)
		}
		sb.WriteString("\n")
	}

	if len(published) == 0 && len(installed) == 0 {
		fmt.Fprintf(&sb, "暂无数据。使用 `/browse %s` 浏览市场。\n", subject)
	}

	return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: sb.String()}, nil
}

// --- /settings ---

type settingsCmd struct{}

func (c *settingsCmd) Name() string      { return "/settings" }
func (c *settingsCmd) Aliases() []string { return nil }
func (c *settingsCmd) Match(s string) bool {
	lower := strings.ToLower(s)
	return lower == "/settings" || strings.HasPrefix(lower, "/settings ")
}
func (c *settingsCmd) Concurrent() bool { return true }

func (c *settingsCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	if msg.ChatType == "group" {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "⚠️ 设置仅限私聊使用，请私信我发送 /settings"}, nil
	}

	if a.settingsSvc == nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "SettingsService 未初始化"}, nil
	}

	content := strings.TrimSpace(msg.Content)
	args := strings.TrimPrefix(strings.ToLower(content), "/settings ")
	args = strings.TrimSpace(args)

	// /settings set <key> <value>
	if strings.HasPrefix(args, "set ") {
		setParts := strings.Fields(strings.TrimPrefix(args, "set "))
		if len(setParts) < 2 {
			return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: "用法：`/settings set <key> <value>`"}, nil
		}
		key := setParts[0]
		value := strings.Join(setParts[1:], " ")

		// Fix 4: Validate key against schema if channelFinder is available
		schema := a.settingsSvc.GetSettingsSchema(msg.Channel)
		if len(schema) > 0 {
			valid := false
			for _, def := range schema {
				if def.Key == key {
					valid = true
					break
				}
			}
			if !valid {
				var validKeys []string
				for _, def := range schema {
					validKeys = append(validKeys, def.Key)
				}
				return &bus.OutboundMessage{
					Channel: msg.Channel, ChatID: msg.ChatID,
					Content: fmt.Sprintf("未知设置项: %q\n可用设置项: %s", key, strings.Join(validKeys, ", ")),
				}, nil
			}
		}

		err := a.settingsSvc.SetSetting(msg.Channel, msg.SenderID, key, value)
		if err != nil {
			return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("设置失败：%v", err)}, nil
		}
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("✅ %s = %s", key, value)}, nil
	}

	// /settings (list) — 检测飞书渠道使用交互式卡片，其他渠道使用 markdown
	if a.channelFinder != nil {
		if ch, ok := a.channelFinder(msg.Channel); ok {
			if fc, ok := ch.(*channel.FeishuChannel); ok {
				card, err := fc.BuildSettingsCard(ctx, msg.SenderID, msg.ChatID, "basic")
				if err != nil {
					return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("构建设置卡片失败：%v", err)}, nil
				}
				cardJSON, err := json.Marshal(card)
				if err != nil {
					return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("序列化设置卡片失败：%v", err)}, nil
				}
				return &bus.OutboundMessage{
					Channel: msg.Channel,
					ChatID:  msg.ChatID,
					Content: "__FEISHU_CARD__::" + string(cardJSON),
				}, nil
			}
		}
	}

	// Fallback: 非 Feishu 渠道使用 markdown UI
	ui, err := a.settingsSvc.GetSettingsUI(msg.Channel, msg.SenderID)
	if err != nil {
		return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: fmt.Sprintf("获取设置失败：%v", err)}, nil
	}
	return &bus.OutboundMessage{Channel: msg.Channel, ChatID: msg.ChatID, Content: ui}, nil
}

// --- /menu ---

type menuCmd struct{}

func (c *menuCmd) Name() string        { return "/menu" }
func (c *menuCmd) Aliases() []string   { return nil }
func (c *menuCmd) Match(s string) bool { return strings.ToLower(s) == "/menu" }
func (c *menuCmd) Concurrent() bool    { return true }

func (c *menuCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "## 🏠 主菜单\n\n" +
			"- ⚙️ `/settings` — 个人设置\n" +
			"- 📦 `/my skills` — 我的 Skills\n" +
			"- 🤖 `/my agents` — 我的 Agents\n" +
			"- 🏪 `/browse` — 浏览市场\n" +
			"- 📤 `/publish skill|agent <name>` — 发布\n" +
			"- 📥 `/install skill|agent <id>` — 安装\n" +
			"- 🗑️ `/uninstall skill|agent <name>` — 卸载\n",
	}, nil
}

// registerBuiltinCommands registers all built-in commands to the registry.
func registerBuiltinCommands(r *CommandRegistry) {
	r.Register(&newCmd{})
	r.Register(&versionCmd{})
	r.Register(&helpCmd{})
	r.Register(&promptCmd{})
	r.Register(&setLLMCmd{})
	r.Register(&unsetLLMCmd{})
	r.Register(&getLLMCmd{})
	r.Register(&compressCmd{})
	r.Register(&contextModeCmd{}) // 先注册（更精确的匹配优先）
	r.Register(&contextInfoCmd{}) // 后注册（更宽泛的匹配）
	r.Register(&modelsCmd{})
	r.Register(&setModelCmd{})
	r.Register(&bangCmd{})

	// Registry & settings commands
	r.Register(&publishCmd{})
	r.Register(&unpublishCmd{})
	r.Register(&browseCmd{})
	r.Register(&installCmd{})
	r.Register(&uninstallCmd{})
	r.Register(&myCmd{})
	r.Register(&settingsCmd{})
	r.Register(&menuCmd{})
}
