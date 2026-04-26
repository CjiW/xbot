package main

import (
	"strconv"
	"strings"

	"xbot/agent"
	"xbot/channel"
	"xbot/config"
	log "xbot/logger"
)

// cliSettingHandler defines how a setting key affects CLI-side runtime state.
// Each field is optional — a key that only updates config leaves ApplyBackend nil.
type cliSettingHandler struct {
	// ApplyConfig updates the in-memory config struct. cfg is always non-nil.
	ApplyConfig func(cfg *config.Config, value string)
	// ApplyBackend applies runtime side effects via the backend.
	// backend and senderID are always non-nil/non-empty when called.
	ApplyBackend func(backend agent.AgentBackend, senderID, value string)
}

// cliSettingHandlers is the CLI-side counterpart of serverapp.settingHandlerRegistry.
// Every key in channel.CLIRuntimeSettingKeys that needs CLI-side handling MUST have
// an entry here. TestCLISettingHandlersCoversAllRuntimeKeys enforces this.
//
// To add a new runtime setting:
//  1. Add the key to channel.CLIRuntimeSettingKeys
//  2. Add a handler here (and in serverapp/setting_handlers.go)
//  3. Done.
var cliSettingHandlers = map[string]cliSettingHandler{
	// --- LLM tier settings (config-only, LLM rebuild handled by caller) ---
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
		// sandbox ReinitSandbox is handled separately in local mode (needs app.workDir)
	},
	"compression_threshold": {
		ApplyConfig: func(cfg *config.Config, value string) {
			if f, err := strconv.ParseFloat(value, 64); err == nil && f > 0 {
				cfg.Agent.CompressionThreshold = f
			}
		},
		ApplyBackend: func(backend agent.AgentBackend, senderID, value string) {
			if f, err := strconv.ParseFloat(value, 64); err == nil && f > 0 {
				backend.SetCompressionThreshold(f)
			}
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

// applyCLISettingsToConfig applies config field updates for all recognized keys in values.
// Returns the set of keys that were handled (for logging/warning about unhandled keys).
func applyCLISettingsToConfig(cfg *config.Config, values map[string]string) map[string]bool {
	handled := make(map[string]bool, len(values))
	for k, v := range values {
		h, ok := cliSettingHandlers[k]
		if !ok {
			continue
		}
		handled[k] = true
		if h.ApplyConfig != nil {
			h.ApplyConfig(cfg, v)
		}
	}
	return handled
}

// applyCLISettingsToBackend applies runtime backend side-effects for all recognized keys.
// context_mode is processed LAST so it correctly overrides enable_auto_compress when both are present.
func applyCLISettingsToBackend(backend agent.AgentBackend, senderID string, values map[string]string) {
	// Process all keys except context_mode first
	for k, v := range values {
		if k == "context_mode" {
			continue // process last
		}
		h, ok := cliSettingHandlers[k]
		if !ok {
			if !isKnownNonRuntimeKey(k) {
				log.WithField("key", k).Warn("applyCLISettings: unhandled setting key")
			}
			continue
		}
		if h.ApplyBackend != nil {
			h.ApplyBackend(backend, senderID, v)
		}
	}
	// Process context_mode last so it overrides enable_auto_compress
	if v, ok := values["context_mode"]; ok && v != "" {
		if h, ok := cliSettingHandlers["context_mode"]; ok && h.ApplyBackend != nil {
			h.ApplyBackend(backend, senderID, v)
		}
	}
}

// isKnownNonRuntimeKey returns true for keys that don't need runtime handling
// (UI-only, persistence-only, or action keys).
func isKnownNonRuntimeKey(key string) bool {
	switch key {
	case "theme", "language", "runner_server", "runner_token", "runner_workspace",
		"enable_stream", "enable_masking", "default_user", "privileged_user",
		"subscription_manage", "runner_panel", "danger_zone",
		"llm_provider", "llm_api_key", "llm_model", "llm_base_url":
		return true
	}
	return false
}

// missingCLIHandlerKeys returns keys from channel.CLIRuntimeSettingKeys
// that are missing from cliSettingHandlers.
func missingCLIHandlerKeys() []string {
	return channel.MissingRegistryKeys(cliSettingHandlers)
}
