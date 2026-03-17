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
			"/compress — 手动触发上下文压缩\n" +
			"/context — 查看当前 token 数和组成\n" +
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

// --- /context ---

type contextCmd struct{}

func (c *contextCmd) Name() string        { return "/context" }
func (c *contextCmd) Aliases() []string   { return nil }
func (c *contextCmd) Match(s string) bool { return strings.ToLower(s) == "/context" }
func (c *contextCmd) Concurrent() bool    { return true }

func (c *contextCmd) Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	tenantSession, err := a.multiSession.GetOrCreateSession(msg.Channel, msg.ChatID)
	if err != nil {
		return nil, err
	}
	return a.handleContext(ctx, msg, tenantSession)
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
	r.Register(&contextCmd{})
	r.Register(&bangCmd{})
}
