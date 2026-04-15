package agent

import (
	"context"
	"fmt"

	"xbot/bus"
	"xbot/channel"
	"xbot/event"
	llm "xbot/llm"
	"xbot/session"
	"xbot/tools"
)

// LocalBackend runs the agent in-process. It wraps an agent.Agent directly,
// delegating all methods to the underlying Agent.
//
// Outbound messages flow through the MessageBus as usual — the caller
// should set up a Dispatcher to route them to channels.
type LocalBackend struct {
	agent *Agent
	bus   *bus.MessageBus
}

// NewLocalBackend creates a LocalBackend with the given agent config.
// It calls agent.New() internally, so all initialization (tools, sessions, etc.)
// is complete by the time this returns.
func NewLocalBackend(cfg Config) *LocalBackend {
	a := New(cfg)
	return &LocalBackend{
		agent: a,
		bus:   cfg.Bus,
	}
}

// Agent returns the underlying *Agent for direct access when needed
// (e.g., for main.go to inject dependencies before Start).
func (b *LocalBackend) Agent() *Agent {
	return b.agent
}

func (b *LocalBackend) Start(ctx context.Context) error {
	go b.agent.Run(ctx)
	return nil
}

func (b *LocalBackend) Stop() {
	if err := b.agent.Close(); err != nil {
		_ = err // best effort
	}
}

func (b *LocalBackend) SendInbound(msg bus.InboundMessage) error {
	select {
	case b.bus.Inbound <- msg:
		return nil
	default:
		return fmt.Errorf("inbound channel full, message dropped")
	}
}

// OnOutbound is a no-op for LocalBackend: outbound messages are handled
// by the Dispatcher + Channel wiring set up by the caller.
// For RemoteBackend, this registers the callback for WS-delivered replies.
func (b *LocalBackend) OnOutbound(_ func(bus.OutboundMessage)) {
	// no-op: LocalBackend uses Dispatcher for outbound routing
}

func (b *LocalBackend) Bus() *bus.MessageBus { return b.bus }

func (b *LocalBackend) LLMFactory() *LLMFactory {
	return b.agent.LLMFactory()
}

func (b *LocalBackend) SettingsService() *SettingsService {
	return b.agent.SettingsService()
}

func (b *LocalBackend) MultiSession() *session.MultiTenantSession {
	return b.agent.MultiSession()
}

func (b *LocalBackend) BgTaskManager() *tools.BackgroundTaskManager {
	return b.agent.BgTaskManager()
}

func (b *LocalBackend) ToolHookChain() *tools.HookChain {
	return b.agent.ToolHookChain()
}

func (b *LocalBackend) SetDirectSend(fn func(bus.OutboundMessage) (string, error)) {
	b.agent.SetDirectSend(fn)
}

func (b *LocalBackend) SetChannelFinder(fn func(name string) (channel.Channel, bool)) {
	b.agent.SetChannelFinder(fn)
}

func (b *LocalBackend) SetChannelPromptProviders(providers ...ChannelPromptProvider) {
	b.agent.SetChannelPromptProviders(providers...)
}

func (b *LocalBackend) RegisterCoreTool(tool tools.Tool) {
	b.agent.RegisterCoreTool(tool)
}

func (b *LocalBackend) IndexGlobalTools() {
	b.agent.IndexGlobalTools()
}

func (b *LocalBackend) CountInteractiveSessions(channelName, chatID string) int {
	return b.agent.CountInteractiveSessions(channelName, chatID)
}

func (b *LocalBackend) ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo {
	return b.agent.ListInteractiveSessions(channelName, chatID)
}

func (b *LocalBackend) InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error) {
	return b.agent.InspectInteractiveSession(ctx, roleName, channelName, chatID, instance, tailCount)
}

func (b *LocalBackend) SetContextMode(mode string) error {
	return b.agent.SetContextMode(mode)
}

func (b *LocalBackend) SetMaxIterations(n int) {
	b.agent.SetMaxIterations(n)
}

func (b *LocalBackend) SetMaxConcurrency(n int) {
	b.agent.SetMaxConcurrency(n)
}

func (b *LocalBackend) SetMaxContextTokens(n int) {
	b.agent.SetMaxContextTokens(n)
}

func (b *LocalBackend) SetSandbox(sb tools.Sandbox, mode string) {
	b.agent.SetSandbox(sb, mode)
}

func (b *LocalBackend) GetCardBuilder() *tools.CardBuilder {
	return b.agent.GetCardBuilder()
}

func (b *LocalBackend) SetEventRouter(router *event.Router) {
	b.agent.SetEventRouter(router)
}

// --- Extended methods (delegated to b.agent) ---

func (b *LocalBackend) RegisterTool(tool tools.Tool) {
	b.agent.RegisterTool(tool)
}

func (b *LocalBackend) RegistryManager() *RegistryManager {
	return b.agent.RegistryManager()
}

func (b *LocalBackend) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	b.agent.SetProxyLLM(senderID, proxy, model)
}

func (b *LocalBackend) ClearProxyLLM(senderID string) {
	b.agent.ClearProxyLLM(senderID)
}

func (b *LocalBackend) GetDefaultModel() string {
	return b.agent.GetDefaultModel()
}

func (b *LocalBackend) SetUserModel(senderID, model string) error {
	return b.agent.SetUserModel(senderID, model)
}

func (b *LocalBackend) GetUserMaxContext(senderID string) int {
	return b.agent.GetUserMaxContext(senderID)
}

func (b *LocalBackend) SetUserMaxContext(senderID string, maxContext int) error {
	return b.agent.SetUserMaxContext(senderID, maxContext)
}

func (b *LocalBackend) GetUserMaxOutputTokens(senderID string) int {
	return b.agent.GetUserMaxOutputTokens(senderID)
}

func (b *LocalBackend) SetUserMaxOutputTokens(senderID string, maxTokens int) error {
	return b.agent.SetUserMaxOutputTokens(senderID, maxTokens)
}

func (b *LocalBackend) GetUserThinkingMode(senderID string) string {
	return b.agent.GetUserThinkingMode(senderID)
}

func (b *LocalBackend) SetUserThinkingMode(senderID string, mode string) error {
	return b.agent.SetUserThinkingMode(senderID, mode)
}

func (b *LocalBackend) GetLLMConcurrency(senderID string) int {
	return b.agent.GetLLMConcurrency(senderID)
}

func (b *LocalBackend) SetLLMConcurrency(senderID string, personal int) error {
	return b.agent.SetLLMConcurrency(senderID, personal)
}

func (b *LocalBackend) GetContextMode() string {
	return b.agent.GetContextMode()
}

func (b *LocalBackend) Close() error {
	return b.agent.Close()
}

func (b *LocalBackend) Run(ctx context.Context) error {
	return b.agent.Run(ctx)
}
