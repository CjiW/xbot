package serverapp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/config"
	"xbot/event"
	llm "xbot/llm"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

func newTestConfig() *config.Config {
	enableAutoCompress := false
	return &config.Config{
		LLM: config.LLMConfig{
			Provider:      "openai",
			APIKey:        "sk-test",
			Model:         "gpt-4.1",
			BaseURL:       "https://api.example.com/v1",
			VanguardModel: "gpt-4.1-pro",
			BalanceModel:  "gpt-4.1",
			SwiftModel:    "gpt-4.1-mini",
		},
		Sandbox: config.SandboxConfig{Mode: "docker"},
		Agent: config.AgentConfig{
			MemoryProvider:     "flat",
			ContextMode:        "manual",
			MaxIterations:      321,
			MaxConcurrency:     7,
			MaxContextTokens:   456789,
			EnableAutoCompress: &enableAutoCompress,
		},
		TavilyAPIKey: "tv-test",
	}
}

func newTestBackendWithSettings(t *testing.T) (agent.AgentBackend, *sqlite.UserSettingsService) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatalf("sqlite.Open() error = %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := sqlite.NewUserSettingsService(db)
	agentSvc := agent.NewSettingsService(store)
	return fakeBackend{settingsSvc: agentSvc}, store
}

type fakeBackend struct {
	settingsSvc *agent.SettingsService
}

func (b fakeBackend) Start(_ context.Context) error                                      { return nil }
func (b fakeBackend) Stop()                                                              {}
func (b fakeBackend) SendInbound(_ bus.InboundMessage) error                             { return nil }
func (b fakeBackend) OnOutbound(_ func(bus.OutboundMessage))                             {}
func (b fakeBackend) Bus() *bus.MessageBus                                               { return nil }
func (b fakeBackend) IsRemote() bool                                                     { return false }
func (b fakeBackend) IsProcessing(_, _ string) bool                                      { return false }
func (b fakeBackend) GetActiveProgress(_, _ string) *channel.CLIProgressPayload          { return nil }
func (b fakeBackend) OnProgress(_ func(*channel.CLIProgressPayload))                     {}
func (b fakeBackend) LLMFactory() *agent.LLMFactory                                      { return nil }
func (b fakeBackend) SettingsService() *agent.SettingsService                            { return b.settingsSvc }
func (b fakeBackend) MultiSession() *session.MultiTenantSession                          { return nil }
func (b fakeBackend) BgTaskManager() *tools.BackgroundTaskManager                        { return nil }
func (b fakeBackend) ToolHookChain() *tools.HookChain                                    { return nil }
func (b fakeBackend) SetDirectSend(_ func(bus.OutboundMessage) (string, error))          {}
func (b fakeBackend) SetChannelFinder(_ func(string) (channel.Channel, bool))            {}
func (b fakeBackend) SetChannelPromptProviders(_ ...agent.ChannelPromptProvider)         {}
func (b fakeBackend) RegisterCoreTool(_ tools.Tool)                                      {}
func (b fakeBackend) IndexGlobalTools()                                                  {}
func (b fakeBackend) CountInteractiveSessions(_, _ string) int                           { return 0 }
func (b fakeBackend) ListInteractiveSessions(_, _ string) []agent.InteractiveSessionInfo { return nil }
func (b fakeBackend) InspectInteractiveSession(_ context.Context, _, _, _, _ string, _ int) (string, error) {
	return "", nil
}
func (b fakeBackend) SetContextMode(_ string) error                                  { return nil }
func (b fakeBackend) SetCWD(_, _, _ string) error                                    { return nil }
func (b fakeBackend) SetMaxIterations(_ int)                                         {}
func (b fakeBackend) SetMaxConcurrency(_ int)                                        {}
func (b fakeBackend) SetMaxContextTokens(_ int)                                      {}
func (b fakeBackend) SetSandbox(_ tools.Sandbox, _ string)                           {}
func (b fakeBackend) GetCardBuilder() *tools.CardBuilder                             { return nil }
func (b fakeBackend) SetEventRouter(_ *event.Router)                                 {}
func (b fakeBackend) RegisterTool(_ tools.Tool)                                      {}
func (b fakeBackend) RegistryManager() *agent.RegistryManager                        { return nil }
func (b fakeBackend) SetProxyLLM(_ string, _ *llm.ProxyLLM, _ string)                {}
func (b fakeBackend) ClearProxyLLM(_ string)                                         {}
func (b fakeBackend) GetDefaultModel() string                                        { return "" }
func (b fakeBackend) SetUserModel(_, _ string) error                                 { return nil }
func (b fakeBackend) GetUserMaxContext(_ string) int                                 { return 0 }
func (b fakeBackend) SetUserMaxContext(_ string, _ int) error                        { return nil }
func (b fakeBackend) GetUserMaxOutputTokens(_ string) int                            { return 0 }
func (b fakeBackend) SetUserMaxOutputTokens(_ string, _ int) error                   { return nil }
func (b fakeBackend) GetUserThinkingMode(_ string) string                            { return "" }
func (b fakeBackend) SetUserThinkingMode(_, _ string) error                          { return nil }
func (b fakeBackend) ListModels() []string                                           { return nil }
func (b fakeBackend) ListAllModels() []string                                        { return nil }
func (b fakeBackend) GetSettings(_, _ string) (map[string]string, error)             { return nil, nil }
func (b fakeBackend) SetSetting(_, _, _, _ string) error                             { return nil }
func (b fakeBackend) ListSubscriptions(_ string) ([]channel.Subscription, error)     { return nil, nil }
func (b fakeBackend) GetDefaultSubscription(_ string) (*channel.Subscription, error) { return nil, nil }
func (b fakeBackend) AddSubscription(_ string, _ channel.Subscription) error         { return nil }
func (b fakeBackend) RemoveSubscription(_ string) error                              { return nil }
func (b fakeBackend) SetDefaultSubscription(_ string) error                          { return nil }
func (b fakeBackend) RenameSubscription(_, _ string) error                           { return nil }
func (b fakeBackend) UpdateSubscription(_ string, _ channel.Subscription) error      { return nil }
func (b fakeBackend) SetSubscriptionModel(_, _ string) error                         { return nil }
func (b fakeBackend) LLMGenerate(_ context.Context, _, _ string, _ []llm.ChatMessage, _ []llm.ToolDefinition, _ string) (*llm.LLMResponse, error) {
	return nil, nil
}
func (b fakeBackend) LLMModels(_ context.Context, _ string) ([]string, error)            { return nil, nil }
func (b fakeBackend) SetModelTiers(_ config.LLMConfig) error                             { return nil }
func (b fakeBackend) SetDefaultThinkingMode(_ string) error                              { return nil }
func (b fakeBackend) ClearMemory(_ context.Context, _, _, _, _ string) error             { return nil }
func (b fakeBackend) GetMemoryStats(_ context.Context, _, _, _ string) map[string]string { return nil }
func (b fakeBackend) GetUserTokenUsage(_ string) (map[string]any, error)                 { return nil, nil }
func (b fakeBackend) GetDailyTokenUsage(_ string, _ int) ([]map[string]any, error)       { return nil, nil }
func (b fakeBackend) GetBgTaskCount(_ string) int                                        { return 0 }
func (b fakeBackend) GetHistory(_, _ string) ([]channel.HistoryMessage, error)           { return nil, nil }
func (b fakeBackend) TrimHistory(_, _ string, _ time.Time) error                         { return nil }
func (b fakeBackend) ResetTokenState()                                                   {}
func (b fakeBackend) Close() error                                                       { return nil }
func (b fakeBackend) Run(_ context.Context) error                                        { return nil }
func (b fakeBackend) GetLLMConcurrency(_ string) int                                     { return 0 }
func (b fakeBackend) SetLLMConcurrency(_ string, _ int) error                            { return nil }
func (b fakeBackend) GetContextMode() string                                             { return "" }

func TestMigrateCLIUserSettingsFromGlobalIfNeeded_SeedsOnlyWhenEmpty(t *testing.T) {
	cfg := newTestConfig()
	backend, store := newTestBackendWithSettings(t)
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, backend, "cli", "cli_user"); err != nil {
		t.Fatalf("migrateCLIUserSettingsFromGlobalIfNeeded() error = %v", err)
	}
	seeded, err := store.Get("cli", "cli_user")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if len(seeded) == 0 {
		t.Fatal("expected seeded settings, got none")
	}
	if seeded["context_mode"] != "manual" {
		t.Fatalf("context_mode = %q, want manual", seeded["context_mode"])
	}
	if seeded["theme"] != "midnight" {
		t.Fatalf("theme = %q, want midnight", seeded["theme"])
	}
	if seeded["enable_auto_compress"] != "false" {
		t.Fatalf("enable_auto_compress = %q, want false", seeded["enable_auto_compress"])
	}
	if _, ok := seeded["llm_model"]; ok {
		t.Fatalf("llm_model should not be seeded into user settings: %#v", seeded)
	}
}

func TestMigrateCLIUserSettingsFromGlobalIfNeeded_SkipsWhenUserAlreadyHasSettings(t *testing.T) {
	cfg := newTestConfig()
	backend, store := newTestBackendWithSettings(t)
	if err := store.Set("cli", "cli_user", "theme", "mono"); err != nil {
		t.Fatalf("store.Set() error = %v", err)
	}
	if err := migrateCLIUserSettingsFromGlobalIfNeeded(cfg, backend, "cli", "cli_user"); err != nil {
		t.Fatalf("migrateCLIUserSettingsFromGlobalIfNeeded() error = %v", err)
	}
	vals, err := store.Get("cli", "cli_user")
	if err != nil {
		t.Fatalf("store.Get() error = %v", err)
	}
	if len(vals) != 1 || vals["theme"] != "mono" {
		t.Fatalf("expected existing settings to remain untouched, got %#v", vals)
	}
}

func TestGetGlobalCLISettings_IncludesExpectedScopes(t *testing.T) {
	cfg := newTestConfig()
	vals, err := getGlobalCLISettings(cfg)
	if err != nil {
		t.Fatalf("getGlobalCLISettings() error = %v", err)
	}
	checks := map[string]string{
		"llm_provider":         "openai",
		"llm_model":            "gpt-4.1",
		"sandbox_mode":         "docker",
		"memory_provider":      "flat",
		"context_mode":         "manual",
		"max_iterations":       "321",
		"max_concurrency":      "7",
		"max_context_tokens":   "456789",
		"enable_auto_compress": "false",
		"theme":                "midnight",
	}
	for k, want := range checks {
		if got := vals[k]; got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
}
