package agent

import (
	"context"

	"xbot/bus"
	"xbot/channel"
	"xbot/event"
	"xbot/session"
	"xbot/tools"
)

// AgentBackend abstracts where the agent loop runs.
//   - LocalBackend: in-process agent.Agent (default CLI mode)
//   - RemoteBackend: connects to a remote xbot server via WebSocket
//
// CLI uses this interface to interact with the agent regardless of location.
// Management methods may return nil for RemoteBackend (where the operation
// runs server-side); callers should nil-check as appropriate.
type AgentBackend interface {
	// Start launches the backend (local: agent.Run, remote: WS connect).
	Start(ctx context.Context) error

	// Stop shuts down the backend gracefully.
	Stop()

	// SendInbound sends a user message to the agent.
	SendInbound(msg bus.InboundMessage) error

	// OnOutbound registers a callback for agent replies.
	OnOutbound(callback func(bus.OutboundMessage))

	// Bus returns the message bus (LocalBackend only; RemoteBackend returns nil).
	Bus() *bus.MessageBus

	// --- Runtime management (used by CLI settings panel, dispatchers, etc.) ---

	// LLMFactory returns the LLM factory for model management.
	LLMFactory() *LLMFactory

	// SettingsService returns the settings service.
	SettingsService() *SettingsService

	// MultiSession returns the multi-tenant session manager.
	MultiSession() *session.MultiTenantSession

	// BgTaskManager returns the background task manager.
	BgTaskManager() *tools.BackgroundTaskManager

	// ToolHookChain returns the tool hook chain.
	ToolHookChain() *tools.HookChain

	// SetDirectSend injects the direct send function (bypasses bus for message tracking).
	SetDirectSend(fn func(bus.OutboundMessage) (string, error))

	// SetChannelFinder sets the channel lookup function.
	SetChannelFinder(fn func(name string) (channel.Channel, bool))

	// SetChannelPromptProviders sets channel-specific prompt providers.
	SetChannelPromptProviders(providers ...ChannelPromptProvider)

	// RegisterCoreTool registers a core tool.
	RegisterCoreTool(tool tools.Tool)

	// IndexGlobalTools indexes all global tools for semantic search.
	IndexGlobalTools()

	// CountInteractiveSessions counts active interactive subagent sessions.
	CountInteractiveSessions(channelName, chatID string) int

	// ListInteractiveSessions lists interactive subagent sessions.
	ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo

	// InspectInteractiveSession inspects a running interactive subagent.
	InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error)

	// SetContextMode changes the runtime context management mode.
	SetContextMode(mode string) error

	// SetMaxIterations sets the max tool iterations per request.
	SetMaxIterations(n int)

	// SetMaxConcurrency sets the max concurrent chat workers.
	SetMaxConcurrency(n int)

	// SetMaxContextTokens sets the max context token limit.
	SetMaxContextTokens(n int)

	// SetSandbox replaces the sandbox instance and mode at runtime.
	SetSandbox(sb tools.Sandbox, mode string)

	// GetCardBuilder returns the card builder (for feishu card callbacks).
	GetCardBuilder() *tools.CardBuilder

	// SetEventRouter sets the event trigger router.
	SetEventRouter(router *event.Router)
}
