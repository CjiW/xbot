package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xbot/agent"
	"xbot/bus"
	"xbot/channel"
	"xbot/clipanic"
	"xbot/config"
	"xbot/event"
	"xbot/llm"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

func TestAppendCLIPanicLogIncludesMainContext(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "cli-panic.log")
	clipanic.EnableFileLogging(logPath)
	defer clipanic.DisableFileLogging()

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected main recover to repanic")
			}
		}()
		func() {
			defer clipanic.Recover("main.main", nil, true)
			panic("boom")
		}()
	}()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read panic log: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "where=main.main") {
		t.Fatalf("expected panic log to include main context, got: %s", content)
	}
	if !strings.Contains(content, "panic=boom") {
		t.Fatalf("expected panic log to include panic value, got: %s", content)
	}
}

func TestSubscriptionPersistence(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	// Write initial config with two subscriptions, "copilot" active
	cfg := &config.Config{
		LLM: config.LLMConfig{
			Provider: "openai",
			BaseURL:  "https://api.openai.com/v1",
			APIKey:   "sk-test",
			Model:    "gpt-4.1",
		},
		Subscriptions: []config.SubscriptionConfig{
			{ID: "default", Name: "glm", Provider: "openai", BaseURL: "https://glm.example.com/v1", APIKey: "sk-glm", Model: "glm-5", Active: false},
			{ID: "copilot", Name: "copilot", Provider: "openai", BaseURL: "https://copilot.example.com/v1", APIKey: "sk-copilot", Model: "gpt-4.1", Active: true},
		},
	}
	saveFn := func() error { return config.SaveToFile(cfgPath, cfg) }

	if err := saveFn(); err != nil {
		t.Fatalf("save initial config: %v", err)
	}

	// Verify copilot is active after save
	loaded := config.LoadFromFile(cfgPath)
	if loaded == nil {
		t.Fatal("failed to load config")
	}
	var activeName string
	for _, s := range loaded.Subscriptions {
		if s.Active {
			activeName = s.Name
			break
		}
	}
	if activeName != "copilot" {
		t.Errorf("expected active subscription 'copilot', got %q", activeName)
	}

	// Simulate SetDefault to switch to "default"
	mgr := newConfigSubscriptionManager(cfg, saveFn, nil)
	if err := mgr.SetDefault("default", ""); err != nil {
		t.Fatalf("SetDefault: %v", err)
	}

	// Reload and verify
	loaded = config.LoadFromFile(cfgPath)
	if loaded == nil {
		t.Fatal("failed to reload config")
	}
	activeName = ""
	for _, s := range loaded.Subscriptions {
		if s.Active {
			activeName = s.Name
			break
		}
	}
	if activeName != "glm" {
		t.Errorf("expected active subscription 'glm' after SetDefault, got %q", activeName)
	}

	// After SetDefault, cfg.LLM is stale (SetDefault only changes Active flag).
	// In production, syncLLMFromActiveSub would be called to derive cfg.LLM.
	// Verify the active subscription's model is correct (single source of truth).
	activeModel := ""
	for _, s := range cfg.Subscriptions {
		if s.Active {
			activeModel = s.Model
			break
		}
	}
	if activeModel != "glm-5" {
		t.Errorf("active subscription model should be 'glm-5' after SetDefault, got %q", activeModel)
	}

	// Test syncLLMFromActiveSub derives cfg.LLM from active subscription
	syncLLMFromActiveSub(cfg)
	if cfg.LLM.Model != "glm-5" {
		t.Errorf("cfg.LLM.Model should be 'glm-5' after syncLLMFromActiveSub, got %q", cfg.LLM.Model)
	}
	if cfg.LLM.Provider != "openai" {
		t.Errorf("cfg.LLM.Provider should be 'openai', got %q", cfg.LLM.Provider)
	}

	// Test model change via subscription (single source of truth)
	for i := range cfg.Subscriptions {
		if cfg.Subscriptions[i].Active {
			cfg.Subscriptions[i].Model = "glm-5-turbo"
			break
		}
	}
	syncLLMFromActiveSub(cfg)
	if err := saveFn(); err != nil {
		t.Fatalf("save after model change: %v", err)
	}

	// Verify cfg.LLM.Model and active subscription Model are both consistent
	if cfg.LLM.Model != "glm-5-turbo" {
		t.Errorf("cfg.LLM.Model should be 'glm-5-turbo', got %q", cfg.LLM.Model)
	}
	activeModel = ""
	for _, s := range cfg.Subscriptions {
		if s.Active {
			activeModel = s.Model
			break
		}
	}
	if activeModel != "glm-5-turbo" {
		t.Errorf("active subscription model should be 'glm-5-turbo', got %q", activeModel)
	}

	// Reload and verify persistence
	loaded = config.LoadFromFile(cfgPath)
	if loaded.LLM.Model != "glm-5-turbo" {
		t.Errorf("loaded cfg.LLM.Model should be 'glm-5-turbo', got %q", loaded.LLM.Model)
	}
	activeModel = ""
	for _, s := range loaded.Subscriptions {
		if s.Active {
			activeModel = s.Model
			break
		}
	}
	if activeModel != "glm-5-turbo" {
		t.Errorf("loaded active subscription model should be 'glm-5-turbo', got %q", activeModel)
	}
}

func TestSubscriptionActiveFieldJSONRoundTrip(t *testing.T) {
	// Verify Active=false is present in JSON output
	s := config.SubscriptionConfig{ID: "a", Name: "a", Active: false}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" {
		t.Fatal("JSON output is empty")
	}
	// Verify "active":false is in the output
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if active, ok := raw["active"].(bool); !ok || active {
		t.Errorf("Active=false should be in JSON, got: %v", raw["active"])
	}

	// Verify unmarshaling
	var s2 config.SubscriptionConfig
	if err := json.Unmarshal(data, &s2); err != nil {
		t.Fatal(err)
	}
	if s2.Active != false {
		t.Error("Active should be false after unmarshal")
	}
}

// TestConfigFilePathStability verifies SaveToFile and LoadFromFile use the same path
func TestConfigFilePathStability(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	cfg := &config.Config{
		LLM: config.LLMConfig{Provider: "openai", Model: "test"},
		Subscriptions: []config.SubscriptionConfig{
			{ID: "s1", Name: "sub1", Active: true, Model: "m1"},
		},
	}
	if err := config.SaveToFile(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	loaded := config.LoadFromFile(cfgPath)
	if loaded == nil {
		t.Fatal("LoadFromFile returned nil")
	}
	if len(loaded.Subscriptions) != 1 || !loaded.Subscriptions[0].Active {
		t.Error("subscription not preserved correctly")
	}
	// Verify file content has "active":true
	data, _ := os.ReadFile(cfgPath)
	if string(data) == "" {
		t.Fatal("config file is empty")
	}
}

func TestSaveCLIConfigPreservesDiskFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	cfgPath := filepath.Join(dir, "config.json")

	// Seed disk config with values across many sections.
	diskCfg := &config.Config{
		CLI:     config.CLIConfig{ServerURL: "ws://localhost:9999", Token: "keep-token"},
		Admin:   config.AdminConfig{Token: "admin-secret", ChatID: "ou_123"},
		Web:     config.WebConfig{Port: 8082, Enable: true},
		Server:  config.ServerConfig{Port: 9999},
		Sandbox: config.SandboxConfig{Mode: "none"},
		Feishu:  config.FeishuConfig{AppID: "cli_test", AppSecret: "secret123"},
		Subscriptions: []config.SubscriptionConfig{{
			ID: "disk-sub", Name: "disk", Provider: "openai",
			BaseURL: "https://disk.example/v1", APIKey: "disk-key",
			Model: "disk-model", Active: true,
		}},
	}
	if err := config.SaveToFile(cfgPath, diskCfg); err != nil {
		t.Fatalf("seed disk config: %v", err)
	}

	// Runtime cfg only modifies LLM and Agent — everything else is zero/default.
	appCfg := &config.Config{
		LLM:   config.LLMConfig{Provider: "openai", Model: "gpt-4.1"},
		Agent: config.AgentConfig{MaxIterations: 123, MaxConcurrency: 7},
		// Deliberately zero: CLI, Admin, Web, Sandbox, Feishu, Subscriptions
	}
	if err := saveCLIConfig(appCfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	loaded := config.LoadFromFile(cfgPath)
	if loaded == nil {
		t.Fatal("LoadFromFile returned nil")
	}

	// LLM and Agent should be updated from appCfg.
	if loaded.LLM.Model != "gpt-4.1" {
		t.Fatalf("LLM.Model should be updated to gpt-4.1, got %q", loaded.LLM.Model)
	}
	if loaded.Agent.MaxIterations != 123 || loaded.Agent.MaxConcurrency != 7 {
		t.Fatalf("Agent fields should be updated, got %+v", loaded.Agent)
	}

	// All other sections must be UNTOUCHED from disk.
	if loaded.CLI.ServerURL != "ws://localhost:9999" || loaded.CLI.Token != "keep-token" {
		t.Fatalf("CLI should be untouched, got %+v", loaded.CLI)
	}
	if loaded.Admin.Token != "admin-secret" || loaded.Admin.ChatID != "ou_123" {
		t.Fatalf("Admin should be untouched, got %+v", loaded.Admin)
	}
	if loaded.Web.Port != 8082 || !loaded.Web.Enable {
		t.Fatalf("Web should be untouched, got %+v", loaded.Web)
	}
	if loaded.Sandbox.Mode != "none" {
		t.Fatalf("Sandbox should be untouched, got %q", loaded.Sandbox.Mode)
	}
	if loaded.Feishu.AppID != "cli_test" || loaded.Feishu.AppSecret != "secret123" {
		t.Fatalf("Feishu should be untouched, got %+v", loaded.Feishu)
	}
	if len(loaded.Subscriptions) != 1 || loaded.Subscriptions[0].ID != "disk-sub" {
		t.Fatalf("Subscriptions should be untouched, got %+v", loaded.Subscriptions)
	}
}

func TestLoadLLMFromDBSubscriptionPrefersDB(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)

	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	svc := sqlite.NewLLMSubscriptionService(db)
	if err := svc.Add(&sqlite.LLMSubscription{
		ID:        "db-sub",
		SenderID:  cliSenderID,
		Name:      "db",
		Provider:  "openai",
		BaseURL:   "https://db.example/v1",
		APIKey:    "db-key",
		Model:     "db-model",
		IsDefault: true,
	}); err != nil {
		t.Fatalf("seed db subscription: %v", err)
	}

	cfg := &config.Config{
		LLM: config.LLMConfig{
			Provider: "openai",
			BaseURL:  "https://config.example/v1",
			APIKey:   "config-key",
			Model:    "config-model",
		},
		Subscriptions: []config.SubscriptionConfig{{
			ID:       "cfg-sub",
			Name:     "cfg",
			Provider: "openai",
			BaseURL:  "https://config.example/v1",
			APIKey:   "config-key",
			Model:    "config-model",
			Active:   true,
		}},
	}

	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), nil, "")
	factory.SetSubscriptionSvc(svc)
	backend := &fakeAgentBackend{factory: factory, defaultModel: "db-model", defaultSub: &channel.Subscription{ID: "db-sub", Name: "db", Provider: "openai", BaseURL: "https://db.example/v1", APIKey: "db-key", Model: "db-model", Active: true}}

	loadLLMFromDBSubscription(backend, cfg)

	if cfg.LLM.BaseURL != "https://db.example/v1" || cfg.LLM.APIKey != "db-key" || cfg.LLM.Model != "db-model" {
		t.Fatalf("expected cfg.LLM to be loaded from DB default subscription, got %+v", cfg.LLM)
	}
}

func TestSeedLocalDBSubscriptionsOnlyWhenDBEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)

	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	svc := sqlite.NewLLMSubscriptionService(db)
	factory := agent.NewLLMFactory(sqlite.NewUserLLMConfigService(db), nil, "")
	factory.SetSubscriptionSvc(svc)
	backend := &fakeAgentBackend{factory: factory, defaultModel: ""}

	cfg := &config.Config{Subscriptions: []config.SubscriptionConfig{{
		ID:       "cfg-sub",
		Name:     "cfg",
		Provider: "openai",
		BaseURL:  "https://config.example/v1",
		APIKey:   "config-key",
		Model:    "config-model",
		Active:   true,
	}}}

	seedLocalDBSubscriptions(backend, cfg)
	subs, err := backend.ListSubscriptions(cliSenderID)
	if err != nil {
		t.Fatalf("list subscriptions after seed: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != "cfg-sub" {
		t.Fatalf("expected config subscription to seed empty DB, got %+v", subs)
	}

	cfg.Subscriptions = []config.SubscriptionConfig{{
		ID:       "cfg-sub-2",
		Name:     "cfg2",
		Provider: "openai",
		BaseURL:  "https://config2.example/v1",
		APIKey:   "config-key-2",
		Model:    "config-model-2",
		Active:   true,
	}}
	seedLocalDBSubscriptions(backend, cfg)
	subs, err = backend.ListSubscriptions(cliSenderID)
	if err != nil {
		t.Fatalf("list subscriptions after second seed: %v", err)
	}
	if len(subs) != 1 || subs[0].ID != "cfg-sub" {
		t.Fatalf("expected existing DB subscriptions to remain authoritative, got %+v", subs)
	}
}

type fakeAgentBackend struct {
	factory      *agent.LLMFactory
	defaultModel string
	defaultSub   *channel.Subscription
}

func (b *fakeAgentBackend) Start(context.Context) error                                  { return nil }
func (b *fakeAgentBackend) Stop()                                                        {}
func (b *fakeAgentBackend) SendInbound(bus.InboundMessage) error                         { return nil }
func (b *fakeAgentBackend) OnOutbound(func(bus.OutboundMessage))                         {}
func (b *fakeAgentBackend) OnProgress(func(*channel.CLIProgressPayload))                 {}
func (b *fakeAgentBackend) Bus() *bus.MessageBus                                         { return nil }
func (b *fakeAgentBackend) IsRemote() bool                                               { return false }
func (b *fakeAgentBackend) IsProcessing(string, string) bool                             { return false }
func (b *fakeAgentBackend) GetActiveProgress(string, string) *channel.CLIProgressPayload { return nil }
func (b *fakeAgentBackend) LLMFactory() *agent.LLMFactory                                { return b.factory }
func (b *fakeAgentBackend) SettingsService() *agent.SettingsService                      { return nil }
func (b *fakeAgentBackend) MultiSession() *session.MultiTenantSession                    { return nil }
func (b *fakeAgentBackend) BgTaskManager() *tools.BackgroundTaskManager                  { return nil }
func (b *fakeAgentBackend) ToolHookChain() *tools.HookChain                              { return nil }
func (b *fakeAgentBackend) SetDirectSend(func(bus.OutboundMessage) (string, error))      {}
func (b *fakeAgentBackend) SetChannelFinder(func(string) (channel.Channel, bool))        {}
func (b *fakeAgentBackend) SetChannelPromptProviders(...agent.ChannelPromptProvider)     {}
func (b *fakeAgentBackend) RegisterCoreTool(tools.Tool)                                  {}
func (b *fakeAgentBackend) IndexGlobalTools()                                            {}
func (b *fakeAgentBackend) CountInteractiveSessions(string, string) int                  { return 0 }
func (b *fakeAgentBackend) ListInteractiveSessions(string, string) []agent.InteractiveSessionInfo {
	return nil
}
func (b *fakeAgentBackend) InspectInteractiveSession(context.Context, string, string, string, string, int) (string, error) {
	return "", nil
}
func (b *fakeAgentBackend) GetSessionMessages(string, string, string, string) ([]agent.SessionMessage, bool) {
	return nil, false
}
func (b *fakeAgentBackend) GetAgentSessionDump(string, string, string, string) (*agent.AgentSessionDump, bool) {
	return nil, false
}
func (b *fakeAgentBackend) GetAgentSessionDumpByFullKey(string) (*agent.AgentSessionDump, bool) {
	return nil, false
}
func (b *fakeAgentBackend) SetContextMode(string) error                    { return nil }
func (b *fakeAgentBackend) SetCWD(string, string, string) error            { return nil }
func (b *fakeAgentBackend) SetMaxIterations(int)                           {}
func (b *fakeAgentBackend) SetMaxConcurrency(int)                          {}
func (b *fakeAgentBackend) SetMaxContextTokens(int)                        {}
func (b *fakeAgentBackend) SetSandbox(tools.Sandbox, string)               {}
func (b *fakeAgentBackend) GetCardBuilder() *tools.CardBuilder             { return nil }
func (b *fakeAgentBackend) SetEventRouter(*event.Router)                   {}
func (b *fakeAgentBackend) GetBgTaskCount(string) int                      { return 0 }
func (b *fakeAgentBackend) ListBgTasks(string) ([]agent.BgTaskJSON, error) { return nil, nil }
func (b *fakeAgentBackend) KillBgTask(string) error                        { return nil }
func (b *fakeAgentBackend) CleanupCompletedBgTasks(string)                 {}
func (b *fakeAgentBackend) ListTenants() ([]agent.TenantInfo, error)       { return nil, nil }
func (b *fakeAgentBackend) ListSubscriptions(senderID string) ([]channel.Subscription, error) {
	svc := b.factory.GetSubscriptionSvc()
	subs, err := svc.List(senderID)
	if err != nil {
		return nil, err
	}
	out := make([]channel.Subscription, len(subs))
	for i, s := range subs {
		out[i] = channel.Subscription{ID: s.ID, Name: s.Name, Provider: s.Provider, BaseURL: s.BaseURL, APIKey: s.APIKey, Model: s.Model, Active: s.IsDefault}
	}
	return out, nil
}
func (b *fakeAgentBackend) GetDefaultSubscription(senderID string) (*channel.Subscription, error) {
	if b.defaultSub != nil {
		return b.defaultSub, nil
	}
	svc := b.factory.GetSubscriptionSvc()
	sub, err := svc.GetDefault(senderID)
	if err != nil || sub == nil {
		return nil, err
	}
	return &channel.Subscription{ID: sub.ID, Name: sub.Name, Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey, Model: sub.Model, Active: sub.IsDefault}, nil
}
func (b *fakeAgentBackend) AddSubscription(senderID string, sub channel.Subscription) error {
	return b.factory.GetSubscriptionSvc().Add(&sqlite.LLMSubscription{ID: sub.ID, SenderID: senderID, Name: sub.Name, Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey, Model: sub.Model, IsDefault: sub.Active})
}
func (b *fakeAgentBackend) RemoveSubscription(id string) error {
	return b.factory.GetSubscriptionSvc().Remove(id)
}
func (b *fakeAgentBackend) SetDefaultSubscription(id string, _ string) error {
	return b.factory.GetSubscriptionSvc().SetDefault(id)
}
func (b *fakeAgentBackend) RenameSubscription(id, name string) error {
	return b.factory.GetSubscriptionSvc().Rename(id, name)
}
func (b *fakeAgentBackend) UpdateSubscription(id string, sub channel.Subscription) error {
	return b.factory.GetSubscriptionSvc().Update(&sqlite.LLMSubscription{ID: id, SenderID: cliSenderID, Name: sub.Name, Provider: sub.Provider, BaseURL: sub.BaseURL, APIKey: sub.APIKey, Model: sub.Model, IsDefault: sub.Active})
}
func (b *fakeAgentBackend) RegisterTool(tools.Tool)                   {}
func (b *fakeAgentBackend) RegistryManager() *agent.RegistryManager   { return nil }
func (b *fakeAgentBackend) SetProxyLLM(string, *llm.ProxyLLM, string) {}
func (b *fakeAgentBackend) ClearProxyLLM(string)                      {}
func (b *fakeAgentBackend) SetDefaultModel(string)                    {}
func (b *fakeAgentBackend) SetUserModel(string, string) error         { return nil }
func (b *fakeAgentBackend) SetSubscriptionModel(id, model string) error {
	return b.factory.GetSubscriptionSvc().SetModel(id, model)
}
func (b *fakeAgentBackend) SwitchModel(string, string) error                      { return nil }
func (b *fakeAgentBackend) GetDefaultModel() string                               { return b.defaultModel }
func (b *fakeAgentBackend) GetUserMaxContext(string) int                          { return 0 }
func (b *fakeAgentBackend) SetUserMaxContext(string, int) error                   { return nil }
func (b *fakeAgentBackend) GetUserMaxOutputTokens(string) int                     { return 0 }
func (b *fakeAgentBackend) SetUserMaxOutputTokens(string, int) error              { return nil }
func (b *fakeAgentBackend) GetUserThinkingMode(string) string                     { return "" }
func (b *fakeAgentBackend) SetUserThinkingMode(string, string) error              { return nil }
func (b *fakeAgentBackend) GetLLMConcurrency(string) int                          { return 0 }
func (b *fakeAgentBackend) SetLLMConcurrency(string, int) error                   { return nil }
func (b *fakeAgentBackend) GetContextMode() string                                { return "" }
func (b *fakeAgentBackend) GetSettings(string, string) (map[string]string, error) { return nil, nil }
func (b *fakeAgentBackend) SetSetting(string, string, string, string) error       { return nil }
func (b *fakeAgentBackend) ListModels() []string                                  { return nil }
func (b *fakeAgentBackend) ListAllModels() []string                               { return nil }
func (b *fakeAgentBackend) SetModelTiers(config.LLMConfig) error                  { return nil }
func (b *fakeAgentBackend) SetDefaultThinkingMode(string) error                   { return nil }
func (b *fakeAgentBackend) GetUserTokenUsage(string) (map[string]any, error)      { return nil, nil }
func (b *fakeAgentBackend) GetDailyTokenUsage(string, int) ([]map[string]any, error) {
	return nil, nil
}
func (b *fakeAgentBackend) ClearMemory(context.Context, string, string, string, string) error {
	return nil
}
func (b *fakeAgentBackend) GetMemoryStats(context.Context, string, string, string) map[string]string {
	return nil
}
func (b *fakeAgentBackend) GetHistory(string, string) ([]channel.HistoryMessage, error) {
	return nil, nil
}
func (b *fakeAgentBackend) TrimHistory(string, string, time.Time) error { return nil }
func (b *fakeAgentBackend) ResetTokenState()                            {}
func (b *fakeAgentBackend) Close() error                                { return nil }
func (b *fakeAgentBackend) Run(context.Context) error                   { return nil }

func TestCLISettingHandlersCoversAllRuntimeKeys(t *testing.T) {
	missing := missingCLIHandlerKeys()
	if len(missing) > 0 {
		t.Errorf("cliSettingHandlers is missing handlers for keys in channel.CLIRuntimeSettingKeys: %v\n"+
			"Add entries to cliSettingHandlers in setting_handlers.go for each missing key.", missing)
	}
}

func TestApplyCLISettingsToConfig(t *testing.T) {
	cfg := &config.Config{}
	handled := applyCLISettingsToConfig(cfg, map[string]string{
		"max_iterations": "50",
		"context_mode":   "auto",
	})
	if cfg.Agent.MaxIterations != 50 {
		t.Errorf("max_iterations = %d, want %d", cfg.Agent.MaxIterations, 50)
	}
	if cfg.Agent.ContextMode != "auto" {
		t.Errorf("context_mode = %q, want %q", cfg.Agent.ContextMode, "auto")
	}
	// All keys should be handled
	for _, k := range []string{"max_iterations", "context_mode"} {
		if !handled[k] {
			t.Errorf("expected %q to be handled", k)
		}
	}
}
