package agent

import (
	"path/filepath"

	"xbot/bus"
	"xbot/config"
	llm "xbot/llm"
	"xbot/tools"
)

// BackendConfig contains the parameters shared by both CLI and server
// to create a LocalBackend. It bundles everything needed to derive
// an agent.Config from the application config.
type BackendConfig struct {
	Cfg      *config.Config // application config (config.json)
	LLM      llm.LLM        // initialized LLM client
	Bus      *bus.MessageBus
	DBPath   string // path to SQLite database
	WorkDir  string
	XbotHome string

	// Optional overrides (zero value = use default from config)
	DirectWorkspace  string // CLI: workspace = workDir directly
	PersonaIsolation bool   // Server: per-web-user persona isolation
}

// AgentConfig derives an agent.Config from the application-level config.
// Both main.go and cmd/xbot-cli/main.go call this instead of constructing
// agent.Config by hand — eliminating ~35 lines of duplicated config mapping
// in each entry point.
func (bc BackendConfig) AgentConfig() Config {
	cfg := bc.Cfg

	// Embedding fallback: use LLM endpoint if embedding not configured
	embBaseURL := cfg.Embedding.BaseURL
	if embBaseURL == "" {
		embBaseURL = cfg.LLM.BaseURL
	}
	embAPIKey := cfg.Embedding.APIKey
	if embAPIKey == "" {
		embAPIKey = cfg.LLM.APIKey
	}

	offloadDir := filepath.Join(bc.XbotHome, "offload_store")

	return Config{
		Bus:                  bc.Bus,
		LLM:                  bc.LLM,
		Model:                cfg.LLM.Model,
		MaxIterations:        cfg.Agent.MaxIterations,
		MaxConcurrency:       cfg.Agent.MaxConcurrency,
		DBPath:               bc.DBPath,
		SkillsDir:            filepath.Join(bc.XbotHome, "skills"),
		AgentsDir:            filepath.Join(bc.XbotHome, "agents"),
		WorkDir:              bc.WorkDir,
		XbotHome:             bc.XbotHome,
		PromptFile:           cfg.Agent.PromptFile,
		DirectWorkspace:      bc.DirectWorkspace,
		SandboxMode:          cfg.Sandbox.Mode,
		Sandbox:              tools.GetSandbox(),
		MemoryProvider:       cfg.Agent.MemoryProvider,
		EmbeddingProvider:    cfg.Embedding.Provider,
		EmbeddingBaseURL:     embBaseURL,
		EmbeddingAPIKey:      embAPIKey,
		EmbeddingModel:       cfg.Embedding.Model,
		EmbeddingMaxTokens:   cfg.Embedding.MaxTokens,
		MCPInactivityTimeout: cfg.Agent.MCPInactivityTimeout,
		MCPCleanupInterval:   cfg.Agent.MCPCleanupInterval,
		SessionCacheTimeout:  cfg.Agent.SessionCacheTimeout,
		EnableAutoCompress:   cfg.Agent.EffectiveEnableAutoCompress(),
		MaxContextTokens:     cfg.Agent.MaxContextTokens,
		CompressionThreshold: cfg.Agent.CompressionThreshold,
		DynamicMaxTokens:     cfg.Agent.EffectiveDynamicMaxTokens(),
		ContextMode:          ContextMode(cfg.Agent.ContextMode),
		MaxSubAgentDepth:     cfg.Agent.MaxSubAgentDepth,
		PurgeOldMessages:     cfg.Agent.PurgeOldMessages,
		SandboxIdleTimeout:   cfg.Sandbox.IdleTimeout,
		PersonaIsolation:     bc.PersonaIsolation,
		OffloadDir:           offloadDir,
	}
}
