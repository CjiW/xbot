package serverapp

import (
	"encoding/json"
	"fmt"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
)

// rpcContext holds shared dependencies for RPC handlers.
// Constructed once per server startup, used by all handler methods.
type rpcContext struct {
	cfg     *config.Config
	backend agent.AgentBackend
	disp    *channel.Dispatcher
	msgBus  *bus.MessageBus
	// Per-request fields — set before dispatch.
	authSenderID string // WS auth identity (for isAdmin checks)
	bizID        string // Resolved business identity (for DB operations)
}

// buildRPCTable constructs the complete RPC dispatch table.
// The returned table is a map of method → handler.
// authSenderID and bizID are injected per-request via buildRPCTable closure.
func buildRPCTable(cfg *config.Config, backend agent.AgentBackend, disp *channel.Dispatcher, msgBus *bus.MessageBus, authSenderID, bizID string) rpcTable {
	h := &rpcContext{cfg: cfg, backend: backend, disp: disp, msgBus: msgBus, authSenderID: authSenderID, bizID: bizID}
	t := make(rpcTable, 70)

	// --- Context / settings ---
	t["get_context_mode"] = rpc0(h.getContextMode)
	t["set_context_mode"] = h.requireAdmin(rpc1void(h.setContextMode))
	t["set_cwd"] = rpc1(h.setCWD)
	t["get_settings"] = rpc1(h.getSettings)
	t["set_setting"] = rpc1void(h.setSetting)

	// --- Max iterations / concurrency / context tokens ---
	t["set_max_iterations"] = h.requireAdmin(rpc1void(h.setMaxIterations))
	t["set_max_concurrency"] = h.requireAdmin(rpc1void(h.setMaxConcurrency))
	t["set_max_context_tokens"] = h.requireAdmin(rpc1void(h.setMaxContextTokens))

	// --- LLM ---
	t["get_default_model"] = rpc0(h.getDefaultModel)
	t["set_user_model"] = rpc1void(h.setUserModel)
	t["switch_model"] = rpc1void(h.switchModel)
	t["get_user_max_context"] = rpc0(h.getUserMaxContext)
	t["set_user_max_context"] = rpc1void(h.setUserMaxContext)
	t["get_user_max_output_tokens"] = rpc0(h.getUserMaxOutputTokens)
	t["set_user_max_output_tokens"] = rpc1void(h.setUserMaxOutputTokens)
	t["get_user_thinking_mode"] = rpc0(h.getUserThinkingMode)
	t["set_user_thinking_mode"] = rpc1void(h.setUserThinkingMode)
	t["get_llm_concurrency"] = rpc0(h.getLLMConcurrency)
	t["set_llm_concurrency"] = rpc1void(h.setLLMConcurrency)
	t["set_default_thinking_mode"] = h.requireAdmin(rpc1void(h.setDefaultThinkingMode))
	t["list_models"] = rpc0err(h.listModels)
	t["list_all_models"] = rpc0err(h.listAllModels)
	t["set_model_tiers"] = h.requireAdmin(rpc1void(h.setModelTiers))
	t["set_proxy_llm"] = rpc1void(h.setProxyLLM)
	t["clear_proxy_llm"] = rpc0void(h.clearProxyLLM)

	// --- Subscriptions ---
	t["list_subscriptions"] = rpc0err(h.listSubscriptions)
	t["get_default_subscription"] = rpc0err(h.getDefaultSubscription)
	t["add_subscription"] = rpc1void(h.addSubscription)
	t["update_subscription"] = rpc1void(h.updateSubscription)
	t["remove_subscription"] = rpc1void(h.removeSubscription)
	t["set_default_subscription"] = rpc1void(h.setDefaultSubscription)
	t["rename_subscription"] = rpc1void(h.renameSubscription)
	t["set_subscription_model"] = rpc1void(h.setSubscriptionModel)

	// --- Memory ---
	t["clear_memory"] = rpc1void(h.clearMemory)
	t["get_memory_stats"] = rpc1(h.getMemoryStats)
	t["get_user_token_usage"] = rpc0err(h.getUserTokenUsage)
	t["get_daily_token_usage"] = rpc1(h.getDailyTokenUsage)

	// --- Sub-agents / sessions ---
	t["count_interactive_sessions"] = rpc1(h.countInteractiveSessions)
	t["list_interactive_sessions"] = rpc1(h.listInteractiveSessions)
	t["inspect_interactive_session"] = rpc1(h.inspectInteractiveSession)
	t["get_session_messages"] = rpc1(h.getSessionMessages)
	t["get_agent_session_dump"] = rpc1(h.getAgentSessionDump)
	t["get_agent_session_dump_by_full_key"] = rpc1(h.getAgentSessionDumpByFullKey)

	// --- Background tasks ---
	t["get_bg_task_count"] = rpc1(h.getBgTaskCount)
	t["list_bg_tasks"] = rpc1(h.listBgTasks)
	t["kill_bg_task"] = rpc1void(h.killBgTask)
	t["cleanup_completed_bg_tasks"] = rpc1(h.cleanupCompletedBgTasks)

	// --- Tenants ---
	t["list_tenants"] = rpc0err(h.listTenants)

	// --- History ---
	t["get_history"] = rpc1(h.getHistory)
	t["trim_history"] = rpc1void(h.trimHistory)

	// --- Status ---
	t["is_processing"] = rpc1(h.isProcessing)
	t["get_active_progress"] = rpc1(h.getActiveProgress)

	// --- Admin ---
	t["reset_token_state"] = h.requireAdmin(rpc0void(h.resetTokenState))
	t["get_channel_config"] = h.requireAdmin(rpc0err(h.getChannelConfigs))
	t["set_channel_config"] = h.requireAdmin(rpc1(h.setChannelConfig))

	return t
}

// handleCLIRPC dispatches RPC requests from CLI RemoteBackend clients
// to the server's LocalBackend. This is the server-side counterpart of
// RemoteBackend.callRPC().
func handleCLIRPC(cfg *config.Config, backend agent.AgentBackend, disp *channel.Dispatcher, msgBus *bus.MessageBus, method string, params json.RawMessage, senderID string) (json.RawMessage, error) {
	bizID := senderIDFromParams(params, senderID)
	t := buildRPCTable(cfg, backend, disp, msgBus, senderID, bizID)
	return t.dispatch(method, params)
}

// requireAdmin wraps a handler to require admin privileges.
func (h *rpcContext) requireAdmin(next rpcHandler) rpcHandler {
	return func(params json.RawMessage) (json.RawMessage, error) {
		if !isAdmin(h.authSenderID) {
			return nil, fmt.Errorf("admin only")
		}
		return next(params)
	}
}

// ownOrAdmin checks that the caller owns the resource or is admin.
func (h *rpcContext) ownOrAdmin(chatID string) error {
	if isAdmin(h.authSenderID) {
		return nil
	}
	if chatID != "" && chatID != h.bizID {
		return fmt.Errorf("access denied")
	}
	return nil
}
