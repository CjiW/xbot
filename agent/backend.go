package agent

import (
	"context"
	"encoding/json"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/event"
	llm "xbot/llm"
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

	// IsRemote returns true if the backend is remote (server-side agent loop).
	// Callers should use this to guard calls that return nil for RemoteBackend
	// (SettingsService, LLMFactory, BgTaskManager, HookManager, MultiSession).
	IsRemote() bool

	// IsProcessing returns true if there is an active agent turn for the given channel/chatID.
	// Used by CLI to restore typing indicator on mid-session reconnect.
	IsProcessing(ch, chatID string) bool

	// GetActiveProgress returns the latest progress snapshot for an active turn,
	// or nil if no turn is active. Used by CLI to restore tool call progress
	// and streaming content on mid-session reconnect.
	GetActiveProgress(ch, chatID string) *channel.CLIProgressPayload

	// OnProgress registers a callback for streaming progress events from the server.
	// LocalBackend: no-op (progress flows through dispatcher/channel directly).
	// RemoteBackend: converts WS progress_structured/stream_content messages to
	// CLIProgressPayload and calls the callback.
	OnProgress(callback func(*channel.CLIProgressPayload))

	// --- Runtime management (used by CLI settings panel, dispatchers, etc.) ---

	// LLMFactory returns the LLM factory for model management.
	LLMFactory() *LLMFactory

	// SettingsService returns the settings service.
	SettingsService() *SettingsService

	// MultiSession returns the multi-tenant session manager.
	MultiSession() *session.MultiTenantSession

	// BgTaskManager returns the background task manager.
	BgTaskManager() *tools.BackgroundTaskManager

	// HookManager returns the tool hook manager.
	HookManager() *hooks.Manager

	// ApprovalState returns the approval state for runtime handler injection.
	// LocalBackend delegates to Agent; RemoteBackend returns nil.
	ApprovalState() *hooks.ApprovalState

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

	// GetSessionMessages returns the conversation messages for a specific interactive SubAgent session.
	GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool)

	// GetAgentSessionDump returns the full session state (messages + iteration snapshots).
	GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool)

	// GetAgentSessionDumpByFullKey returns the session state using the full interactiveKey directly.
	GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool)

	// SetCWD sets the current working directory for a session on the server.
	// Used by CLI remote mode to sync the client's cwd to the server session.
	SetCWD(ch, chatID, dir string) error

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

	// --- Extended methods (used by server main.go) ---

	// RegisterTool registers a user tool.
	RegisterTool(tool tools.Tool)

	// RegistryManager returns the registry manager for shared entries.
	RegistryManager() *RegistryManager

	// SetProxyLLM injects a proxy LLM for a specific user.
	SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string)

	// ClearProxyLLM removes the proxy LLM for a specific user.
	ClearProxyLLM(senderID string)

	// GetDefaultModel returns the default model name.
	GetDefaultModel() string

	// SetUserModel sets the model for a specific user.
	SetUserModel(senderID, model string) error

	// SwitchModel switches the active model in memory (no LLMConfig required).
	SwitchModel(senderID, model string) error

	// GetUserMaxContext returns the max context tokens for a specific user.
	GetUserMaxContext(senderID string) int

	// SetUserMaxContext sets the max context tokens for a specific user.
	SetUserMaxContext(senderID string, maxContext int) error

	// GetUserMaxOutputTokens returns the max output tokens for a specific user.
	GetUserMaxOutputTokens(senderID string) int

	// SetUserMaxOutputTokens sets the max output tokens for a specific user.
	SetUserMaxOutputTokens(senderID string, maxTokens int) error

	// GetUserThinkingMode returns the thinking mode for a specific user.
	GetUserThinkingMode(senderID string) string

	// SetUserThinkingMode sets the thinking mode for a specific user.
	SetUserThinkingMode(senderID string, mode string) error

	// GetLLMConcurrency returns the LLM concurrency limit for a specific user.
	GetLLMConcurrency(senderID string) int

	// SetLLMConcurrency sets the LLM concurrency limit for a specific user.
	SetLLMConcurrency(senderID string, personal int) error

	// GetContextMode returns the current context management mode.
	GetContextMode() string

	// --- Extended RPC methods (remote-friendly, used by CLI adapters) ---
	// LocalBackend delegates to local services; RemoteBackend forwards via WS RPC.

	// GetSettings retrieves settings for a namespace/sender.
	GetSettings(namespace, senderID string) (map[string]string, error)

	// SetSetting sets a single setting value.
	SetSetting(namespace, senderID, key, value string) error

	// ListModels returns available model names.
	ListModels() []string

	// ListAllModels returns all available model names (including subscriptions).
	ListAllModels() []string

	// SetModelTiers syncs model tier configuration.
	SetModelTiers(cfg config.LLMConfig) error

	// SetDefaultThinkingMode sets the default thinking mode.
	SetDefaultThinkingMode(mode string) error

	// ClearMemory clears memory for a channel/chat/sender.
	ClearMemory(ctx context.Context, channel, chatID, targetType, senderID string) error

	// GetMemoryStats retrieves memory statistics.
	GetMemoryStats(ctx context.Context, channel, chatID, senderID string) map[string]string

	// GetUserTokenUsage retrieves token usage for a sender.
	GetUserTokenUsage(senderID string) (map[string]any, error)

	// GetDailyTokenUsage retrieves daily token usage.
	GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error)

	// GetBgTaskCount returns the count of active background tasks.
	GetBgTaskCount(sessionKey string) int

	// ListBgTasks returns detailed info about running background tasks (remote: RPC-backed).
	ListBgTasks(sessionKey string) ([]BgTaskJSON, error)

	// KillBgTask terminates a background task by ID (remote: RPC-backed).
	KillBgTask(taskID string) error

	// CleanupCompletedBgTasks removes completed/errored tasks from the task manager.
	CleanupCompletedBgTasks(sessionKey string)

	// ListTenants returns all tenant sessions from the DB.
	ListTenants() ([]TenantInfo, error)

	// ListSubscriptions lists LLM subscriptions.
	ListSubscriptions(senderID string) ([]channel.Subscription, error)

	// GetDefaultSubscription gets the default subscription.
	GetDefaultSubscription(senderID string) (*channel.Subscription, error)

	// AddSubscription adds a new subscription.
	AddSubscription(senderID string, sub channel.Subscription) error

	// RemoveSubscription removes a subscription by ID.
	RemoveSubscription(id string) error

	// SetDefaultSubscription sets the default subscription for a chat.
	SetDefaultSubscription(id string, chatID string) error

	// RenameSubscription renames a subscription.
	RenameSubscription(id, name string) error

	// UpdateSubscription updates all fields of a subscription.
	UpdateSubscription(id string, sub channel.Subscription) error

	// SetSubscriptionModel updates the model of a subscription.
	SetSubscriptionModel(id, model string) error

	// GetHistory retrieves session messages for a channel/chatID pair.
	// RemoteBackend forwards via RPC; LocalBackend reads from local DB.
	GetHistory(channel, chatID string) ([]channel.HistoryMessage, error)

	// GetTokenState retrieves the last API token counts for a session.
	// Returns (0, 0, nil) when no data is available.
	GetTokenState(channel, chatID string) (promptTokens, completionTokens int64, err error)

	// TrimHistory deletes messages newer than or equal to cutoff for a channel/chatID.
	// Used by CLI Ctrl+K session truncation. RemoteBackend forwards via RPC.
	TrimHistory(channel, chatID string, cutoff time.Time) error

	// ResetTokenState clears the cached prompt/completion token counts.
	// Must be called after /rewind to prevent maybeCompress from using stale
	// large token counts and triggering an immediate incorrect compression.
	ResetTokenState()

	// --- Channel configuration (remote-mode friendly) ---

	// GetChannelConfigs returns channel configurations as flat maps.
	// Keys: "web", "feishu", "qq", "napcat".
	GetChannelConfigs() (map[string]map[string]string, error)

	// SetChannelConfig updates a channel's configuration in config.json.
	// channel: "web", "feishu", "qq", or "napcat".
	SetChannelConfig(channel string, values map[string]string) error

	// CallRPC sends a raw RPC call to the server (RemoteBackend forwards via WS).
	// LocalBackend returns an error (not available in standalone mode, but
	// CLI callbacks use DB directly instead of going through this).
	CallRPC(method string, params any) (json.RawMessage, error)

	// Close shuts down the agent, releasing all resources.
	Close() error

	// Run starts the agent loop and blocks until the context is cancelled.
	// Use this for the main goroutine blocking wait.
	Run(ctx context.Context) error
}
