package serverapp

import (
	"strconv"
	"strings"

	"xbot/agent"
	"xbot/channel"
	"xbot/config"
	log "xbot/logger"
	"xbot/tools"
)

// settingHandler defines how a setting key updates runtime state.
// Each field is optional — a key that only updates config leaves ApplyBackend nil.
type settingHandler struct {
	// ApplyConfig updates the in-memory config struct. cfg is always non-nil.
	ApplyConfig func(cfg *config.Config, value string)
	// ApplyBackend applies runtime side effects via the backend.
	// Called after ApplyConfig. Both backend and senderID are non-nil/non-empty.
	ApplyBackend func(backend agent.AgentBackend, senderID, value string)
	// ApplyFull is called with both cfg and backend. Used when the side effect
	// needs config context (e.g. sandbox reinit needs cfg.Agent.WorkDir).
	// If set, called instead of the ApplyConfig+ApplyBackend pair.
	ApplyFull func(cfg *config.Config, backend agent.AgentBackend, senderID, value string)
}

// settingHandlerRegistry is the single source of truth for server-side runtime
// setting effects. Every key in channel.CLIRuntimeSettingKeys that needs server-side
// handling MUST have an entry here.
//
// To add a new runtime setting:
//  1. Add the key to channel.CLIRuntimeSettingKeys
//  2. Add a handler here (and in cmd/xbot-cli/setting_handlers.go for CLI side)
//  3. Done — no switch-case to update, no if-chain to extend.
//
// If a key appears in channel.CLIRuntimeSettingKeys but is missing here,
// TestAllRuntimeKeysHaveHandlers will fail at CI time.
var settingHandlerRegistry = map[string]settingHandler{
	// --- LLM tier settings (config-only, backend effects applied by caller via SetModelTiers) ---
	"vanguard_model": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.LLM.VanguardModel = strings.TrimSpace(value)
		},
	},
	"balance_model": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.LLM.BalanceModel = strings.TrimSpace(value)
		},
	},
	"swift_model": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.LLM.SwiftModel = strings.TrimSpace(value)
		},
	},

	// --- Agent settings ---
	"sandbox_mode": {
		ApplyConfig: func(cfg *config.Config, value string) { cfg.Sandbox.Mode = value },
		ApplyFull: func(cfg *config.Config, backend agent.AgentBackend, senderID, value string) {
			workDir := cfg.Agent.WorkDir
			if workDir == "" {
				workDir = "."
			}
			tools.ReinitSandbox(cfg.Sandbox, workDir)
			backend.SetSandbox(tools.GetSandbox(), value)
		},
	},
	"memory_provider": {
		ApplyConfig: func(cfg *config.Config, value string) { cfg.Agent.MemoryProvider = value },
	},
	"tavily_api_key": {
		ApplyConfig: func(cfg *config.Config, value string) { cfg.TavilyAPIKey = value },
	},

	// --- Runtime state settings (config + backend side-effects) ---
	"context_mode": {
		ApplyConfig: func(cfg *config.Config, value string) { cfg.Agent.ContextMode = value },
		ApplyBackend: func(backend agent.AgentBackend, senderID, value string) {
			_ = backend.SetContextMode(value)
		},
	},
	"max_iterations": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.Agent.MaxIterations = channel.ParseSettingInt(value, cfg.Agent.MaxIterations)
		},
		ApplyBackend: func(backend agent.AgentBackend, senderID, value string) {
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				backend.SetMaxIterations(n)
			}
		},
	},
	"max_concurrency": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.Agent.MaxConcurrency = channel.ParseSettingInt(value, cfg.Agent.MaxConcurrency)
		},
		ApplyBackend: func(backend agent.AgentBackend, senderID, value string) {
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				backend.SetMaxConcurrency(n)
			}
		},
	},
	"max_context_tokens": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.Agent.MaxContextTokens = channel.ParseSettingInt(value, cfg.Agent.MaxContextTokens)
		},
		ApplyBackend: func(backend agent.AgentBackend, senderID, value string) {
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				backend.SetMaxContextTokens(n)
			}
		},
	},
	// enable_auto_compress is a legacy alias for context_mode.
	// Its ApplyBackend calls SetContextMode, so context_mode must be processed LAST
	// in batch mode to correctly override when both are present.
	"enable_auto_compress": {
		ApplyConfig: func(cfg *config.Config, value string) {
			b := channel.ParseSettingBool(value)
			cfg.Agent.EnableAutoCompress = &b
		},
		ApplyBackend: func(backend agent.AgentBackend, senderID, value string) {
			if channel.ParseSettingBool(value) {
				_ = backend.SetContextMode("auto")
			} else {
				_ = backend.SetContextMode("none")
			}
		},
	},
}

// serverKnownNonRuntimeKeys are keys that may appear in DB settings but don't need
// runtime handling. They should not trigger warning logs.
var serverKnownNonRuntimeKeys = map[string]bool{
	"theme": true, "language": true,
	"runner_server": true, "runner_token": true, "runner_workspace": true,
	"enable_stream": true, "enable_masking": true,
	"default_user": true, "privileged_user": true,
}

// applyRuntimeSetting applies a single setting change to the in-memory config and backend.
// Used by admin RPC handler after the setting is persisted to DB.
// For batch operations (startup sync), use applyRuntimeSettings instead.
func applyRuntimeSetting(cfg *config.Config, backend agent.AgentBackend, senderID, key, value string) {
	handler, ok := settingHandlerRegistry[key]
	if !ok {
		if !serverKnownNonRuntimeKeys[key] {
			log.WithField("key", key).WithField("value", value).
				Warn("applyRuntimeSetting: unhandled setting key, ignoring")
		}
		return
	}
	if handler.ApplyFull != nil && backend != nil {
		handler.ApplyFull(cfg, backend, senderID, value)
	} else {
		if handler.ApplyConfig != nil {
			handler.ApplyConfig(cfg, value)
		}
		if handler.ApplyBackend != nil && backend != nil {
			handler.ApplyBackend(backend, senderID, value)
		}
	}
	if backend != nil && backend.LLMFactory() != nil {
		backend.LLMFactory().SetModelTiers(cfg.LLM)
	}
	_ = saveServerConfig(cfg)
}

// applyRuntimeSettings applies a batch of setting changes to the in-memory config and backend.
// context_mode is processed LAST so it correctly overrides enable_auto_compress when both are present.
// SetModelTiers and saveServerConfig are called once after all keys are processed.
func applyRuntimeSettings(cfg *config.Config, backend agent.AgentBackend, senderID string, values map[string]string) {
	// Process all keys except context_mode first
	for k, v := range values {
		if k == "context_mode" {
			continue // process last
		}
		handler, ok := settingHandlerRegistry[k]
		if !ok {
			if !serverKnownNonRuntimeKeys[k] {
				log.WithField("key", k).WithField("value", v).
					Warn("applyRuntimeSettings: unhandled setting key, ignoring")
			}
			continue
		}
		if handler.ApplyFull != nil && backend != nil {
			handler.ApplyFull(cfg, backend, senderID, v)
		} else {
			if handler.ApplyConfig != nil {
				handler.ApplyConfig(cfg, v)
			}
			if handler.ApplyBackend != nil && backend != nil {
				handler.ApplyBackend(backend, senderID, v)
			}
		}
	}
	// Process context_mode last so it overrides enable_auto_compress
	if v, ok := values["context_mode"]; ok && v != "" {
		handler := settingHandlerRegistry["context_mode"]
		if handler.ApplyFull != nil && backend != nil {
			handler.ApplyFull(cfg, backend, senderID, v)
		} else {
			if handler.ApplyConfig != nil {
				handler.ApplyConfig(cfg, v)
			}
			if handler.ApplyBackend != nil && backend != nil {
				handler.ApplyBackend(backend, senderID, v)
			}
		}
	}
	// SetModelTiers and config save: once after all keys
	if backend != nil && backend.LLMFactory() != nil {
		backend.LLMFactory().SetModelTiers(cfg.LLM)
	}
	_ = saveServerConfig(cfg)
}

// missingHandlerKeys returns keys from channel.CLIRuntimeSettingKeys
// that are missing from settingHandlerRegistry.
func missingHandlerKeys() []string {
	expected := make(map[string]bool, len(channel.CLIRuntimeSettingKeys))
	for _, k := range channel.CLIRuntimeSettingKeys {
		expected[k] = true
	}
	var missing []string
	for k := range expected {
		if _, ok := settingHandlerRegistry[k]; !ok {
			missing = append(missing, k)
		}
	}
	return missing
}
