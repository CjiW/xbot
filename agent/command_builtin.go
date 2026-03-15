package agent

import (
	"context"
	"strings"

	"xbot/bus"
	"xbot/version"
)

// --- /new ---

type newCmd struct{}

func (c *newCmd) Name() string        { return "/new" }
func (c *newCmd) Aliases() []string   { return nil }
func (c *newCmd) Match(s string) bool { return strings.ToLower(s) == "/new" }

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
			"/llm — 查看当前 LLM 配置\n" +
			"/compress — 手动触发上下文压缩\n" +
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

func (c *setLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return a.handleSetLLM(ctx, msg)
}

// --- /llm ---

type getLLMCmd struct{}

func (c *getLLMCmd) Name() string        { return "/llm" }
func (c *getLLMCmd) Aliases() []string   { return nil }
func (c *getLLMCmd) Match(s string) bool { return strings.ToLower(s) == "/llm" }

func (c *getLLMCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	return a.handleGetLLM(ctx, msg)
}

// --- /compress ---

type compressCmd struct{}

func (c *compressCmd) Name() string        { return "/compress" }
func (c *compressCmd) Aliases() []string   { return nil }
func (c *compressCmd) Match(s string) bool { return strings.ToLower(s) == "/compress" }

func (c *compressCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handleCompress(ctx, msg, tenantSession)
}

// --- ! (bang command) ---

type bangCmd struct{}

func (c *bangCmd) Name() string      { return "!" }
func (c *bangCmd) Aliases() []string { return nil }
func (c *bangCmd) Match(s string) bool {
	_, ok := isBangCommand(s)
	return ok
}

func (c *bangCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	cmd, _ := isBangCommand(msg.Content)
	return a.handleBangCommand(ctx, msg, cmd)
}

// registerBuiltinCommands registers all built-in commands to the registry.
func registerBuiltinCommands(r *CommandRegistry) {
	r.Register(&newCmd{})
	r.Register(&versionCmd{})
	r.Register(&helpCmd{})
	r.Register(&promptCmd{})
	r.Register(&setLLMCmd{})
	r.Register(&getLLMCmd{})
	r.Register(&compressCmd{})
	r.Register(&bangCmd{})
}
