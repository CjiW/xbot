package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xbot/agent"
	"xbot/agent/hooks"
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

	// Agent settings should be updated from appCfg.
	if loaded.Agent.MaxIterations != 123 || loaded.Agent.MaxConcurrency != 7 {
		t.Fatalf("Agent fields should be updated, got %+v", loaded.Agent)
	}
	// LLM credentials should NOT be written back when config.json has subscriptions
	// (single source of truth is the subscription system, not cfg.LLM).
	// Only tier models (vanguard/balance/swift) are written back.
	// The disk subscription's model should remain unchanged.
	if loaded.LLM.Model != "" {
		t.Fatalf("LLM.Model should NOT be written back when subscriptions exist, got %q", loaded.LLM.Model)
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

func TestSaveCLIConfig_WritesLLMCredentialsWhenNoSubscriptions(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	cfgPath := filepath.Join(dir, "config.json")

	// Seed disk config with NO subscriptions — the legacy/first-run path.
	diskCfg := &config.Config{
		Admin: config.AdminConfig{Token: "admin-secret"},
		Web:   config.WebConfig{Port: 9090, Enable: true},
	}
	if err := config.SaveToFile(cfgPath, diskCfg); err != nil {
		t.Fatalf("seed disk config: %v", err)
	}

	// Runtime cfg carries LLM credentials and agent settings.
	appCfg := &config.Config{
		LLM: config.LLMConfig{
			Provider:        "openai",
			BaseURL:         "https://api.openai.com/v1",
			APIKey:          "sk-test-key",
			Model:           "gpt-4.1",
			MaxOutputTokens: 4096,
			ThinkingMode:    "enabled",
		},
		Agent: config.AgentConfig{MaxIterations: 42},
	}

	if err := saveCLIConfig(appCfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	loaded := config.LoadFromFile(cfgPath)
	if loaded == nil {
		t.Fatal("LoadFromFile returned nil")
	}

	// LLM credentials MUST be written because there are no subscriptions.
	if loaded.LLM.Provider != "openai" {
		t.Errorf("LLM.Provider = %q, want %q", loaded.LLM.Provider, "openai")
	}
	if loaded.LLM.BaseURL != "https://api.openai.com/v1" {
		t.Errorf("LLM.BaseURL = %q, want %q", loaded.LLM.BaseURL, "https://api.openai.com/v1")
	}
	if loaded.LLM.APIKey != "sk-test-key" {
		t.Errorf("LLM.APIKey = %q, want %q", loaded.LLM.APIKey, "sk-test-key")
	}
	if loaded.LLM.Model != "gpt-4.1" {
		t.Errorf("LLM.Model = %q, want %q", loaded.LLM.Model, "gpt-4.1")
	}
	if loaded.LLM.MaxOutputTokens != 4096 {
		t.Errorf("LLM.MaxOutputTokens = %d, want %d", loaded.LLM.MaxOutputTokens, 4096)
	}
	if loaded.LLM.ThinkingMode != "enabled" {
		t.Errorf("LLM.ThinkingMode = %q, want %q", loaded.LLM.ThinkingMode, "enabled")
	}

	// Agent should also be persisted.
	if loaded.Agent.MaxIterations != 42 {
		t.Errorf("Agent.MaxIterations = %d, want %d", loaded.Agent.MaxIterations, 42)
	}

	// Other sections from disk should be preserved.
	if loaded.Admin.Token != "admin-secret" {
		t.Errorf("Admin.Token = %q, want %q", loaded.Admin.Token, "admin-secret")
	}
	if loaded.Web.Port != 9090 || !loaded.Web.Enable {
		t.Errorf("Web should be preserved, got %+v", loaded.Web)
	}
}

func TestSaveCLIConfig_TierModelsAlwaysPersisted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	cfgPath := filepath.Join(dir, "config.json")

	// Disk config has subscriptions — LLM credentials should NOT be written,
	// but tier models should always be persisted.
	diskCfg := &config.Config{
		LLM: config.LLMConfig{
			VanguardModel: "old-vanguard",
			BalanceModel:  "old-balance",
			SwiftModel:    "old-swift",
		},
		Subscriptions: []config.SubscriptionConfig{{
			ID: "sub1", Name: "sub1", Provider: "openai",
			BaseURL: "https://sub.example/v1", APIKey: "sub-key",
			Model: "sub-model", Active: true,
		}},
		Admin: config.AdminConfig{ChatID: "ou_999"},
	}
	if err := config.SaveToFile(cfgPath, diskCfg); err != nil {
		t.Fatalf("seed disk config: %v", err)
	}

	// Runtime cfg updates tier models.
	appCfg := &config.Config{
		LLM: config.LLMConfig{
			Provider:      "openai",
			BaseURL:       "https://runtime.example/v1",
			APIKey:        "runtime-key",
			Model:         "runtime-model",
			VanguardModel: "new-vanguard",
			BalanceModel:  "new-balance",
			SwiftModel:    "new-swift",
		},
		Agent: config.AgentConfig{MaxConcurrency: 5},
	}

	if err := saveCLIConfig(appCfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	loaded := config.LoadFromFile(cfgPath)
	if loaded == nil {
		t.Fatal("LoadFromFile returned nil")
	}

	// Tier models must always be persisted regardless of subscriptions.
	if loaded.LLM.VanguardModel != "new-vanguard" {
		t.Errorf("VanguardModel = %q, want %q", loaded.LLM.VanguardModel, "new-vanguard")
	}
	if loaded.LLM.BalanceModel != "new-balance" {
		t.Errorf("BalanceModel = %q, want %q", loaded.LLM.BalanceModel, "new-balance")
	}
	if loaded.LLM.SwiftModel != "new-swift" {
		t.Errorf("SwiftModel = %q, want %q", loaded.LLM.SwiftModel, "new-swift")
	}

	// LLM credentials must NOT be written because subscriptions exist.
	if loaded.LLM.Provider != "" {
		t.Errorf("LLM.Provider should NOT be overwritten when subscriptions exist, got %q", loaded.LLM.Provider)
	}
	if loaded.LLM.BaseURL != "" {
		t.Errorf("LLM.BaseURL should NOT be overwritten when subscriptions exist, got %q", loaded.LLM.BaseURL)
	}
	if loaded.LLM.APIKey != "" {
		t.Errorf("LLM.APIKey should NOT be overwritten when subscriptions exist, got %q", loaded.LLM.APIKey)
	}
	if loaded.LLM.Model != "" {
		t.Errorf("LLM.Model should NOT be overwritten when subscriptions exist, got %q", loaded.LLM.Model)
	}

	// Agent should be persisted.
	if loaded.Agent.MaxConcurrency != 5 {
		t.Errorf("Agent.MaxConcurrency = %d, want %d", loaded.Agent.MaxConcurrency, 5)
	}

	// Disk subscriptions and other sections must remain untouched.
	if len(loaded.Subscriptions) != 1 || loaded.Subscriptions[0].ID != "sub1" {
		t.Errorf("Subscriptions should be preserved, got %+v", loaded.Subscriptions)
	}
	if loaded.Admin.ChatID != "ou_999" {
		t.Errorf("Admin.ChatID = %q, want %q", loaded.Admin.ChatID, "ou_999")
	}
}

func TestSaveCLIConfig_ParsesExistingFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	cfgPath := filepath.Join(dir, "config.json")

	// Write a valid config.json with existing settings across multiple sections.
	diskCfg := &config.Config{
		CLI: config.CLIConfig{ServerURL: "ws://localhost:7777", Token: "existing-token"},
		LLM: config.LLMConfig{
			Provider:      "anthropic",
			BaseURL:       "https://api.anthropic.com",
			APIKey:        "sk-ant-existing",
			Model:         "claude-3",
			VanguardModel: "claude-opus",
		},
		Agent: config.AgentConfig{
			MaxIterations:  10,
			MaxConcurrency: 3,
			ContextMode:    "full",
		},
		Admin:   config.AdminConfig{Token: "admin-tok", ChatID: "ou_admin"},
		Web:     config.WebConfig{Port: 8080, Enable: true},
		Server:  config.ServerConfig{Port: 7777},
		Sandbox: config.SandboxConfig{Mode: "docker"},
		Subscriptions: []config.SubscriptionConfig{{
			ID: "existing-sub", Name: "prod", Provider: "anthropic",
			BaseURL: "https://prod.example/v1", APIKey: "prod-key",
			Model: "claude-prod", Active: true,
		}},
	}
	if err := config.SaveToFile(cfgPath, diskCfg); err != nil {
		t.Fatalf("seed disk config: %v", err)
	}

	// Call saveCLIConfig with new agent settings only.
	appCfg := &config.Config{
		LLM: config.LLMConfig{
			VanguardModel: "claude-opus-4",
			BalanceModel:  "claude-sonnet-4",
			SwiftModel:    "claude-haiku-4",
		},
		Agent: config.AgentConfig{
			MaxIterations:  99,
			MaxConcurrency: 12,
			MemoryProvider: "redis",
		},
	}

	if err := saveCLIConfig(appCfg); err != nil {
		t.Fatalf("saveCLIConfig: %v", err)
	}

	loaded := config.LoadFromFile(cfgPath)
	if loaded == nil {
		t.Fatal("LoadFromFile returned nil")
	}

	// Agent settings must be updated from appCfg.
	if loaded.Agent.MaxIterations != 99 {
		t.Errorf("Agent.MaxIterations = %d, want 99", loaded.Agent.MaxIterations)
	}
	if loaded.Agent.MaxConcurrency != 12 {
		t.Errorf("Agent.MaxConcurrency = %d, want 12", loaded.Agent.MaxConcurrency)
	}
	if loaded.Agent.MemoryProvider != "redis" {
		t.Errorf("Agent.MemoryProvider = %q, want %q", loaded.Agent.MemoryProvider, "redis")
	}

	// Tier models must be updated.
	if loaded.LLM.VanguardModel != "claude-opus-4" {
		t.Errorf("VanguardModel = %q, want %q", loaded.LLM.VanguardModel, "claude-opus-4")
	}
	if loaded.LLM.BalanceModel != "claude-sonnet-4" {
		t.Errorf("BalanceModel = %q, want %q", loaded.LLM.BalanceModel, "claude-sonnet-4")
	}
	if loaded.LLM.SwiftModel != "claude-haiku-4" {
		t.Errorf("SwiftModel = %q, want %q", loaded.LLM.SwiftModel, "claude-haiku-4")
	}

	// Existing CLI settings must be preserved (appCfg.CLI is zero, so no overwrite).
	if loaded.CLI.ServerURL != "ws://localhost:7777" {
		t.Errorf("CLI.ServerURL = %q, want %q", loaded.CLI.ServerURL, "ws://localhost:7777")
	}
	if loaded.CLI.Token != "existing-token" {
		t.Errorf("CLI.Token = %q, want %q", loaded.CLI.Token, "existing-token")
	}

	// Admin, Web, Server, Sandbox, Feishu must all be untouched.
	if loaded.Admin.Token != "admin-tok" || loaded.Admin.ChatID != "ou_admin" {
		t.Errorf("Admin should be preserved, got %+v", loaded.Admin)
	}
	if loaded.Web.Port != 8080 || !loaded.Web.Enable {
		t.Errorf("Web should be preserved, got %+v", loaded.Web)
	}
	if loaded.Server.Port != 7777 {
		t.Errorf("Server.Port = %d, want 7777", loaded.Server.Port)
	}
	if loaded.Sandbox.Mode != "docker" {
		t.Errorf("Sandbox.Mode = %q, want %q", loaded.Sandbox.Mode, "docker")
	}

	// Subscription must remain untouched.
	if len(loaded.Subscriptions) != 1 || loaded.Subscriptions[0].ID != "existing-sub" {
		t.Errorf("Subscriptions should be preserved, got %+v", loaded.Subscriptions)
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
func (b *fakeAgentBackend) HookManager() *hooks.Manager                                  { return nil }
func (b *fakeAgentBackend) ApprovalState() *hooks.ApprovalState                          { return nil }
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
func (b *fakeAgentBackend) SetCompressionThreshold(float64)                {}
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
func (b *fakeAgentBackend) GetTokenState(string, string) (int64, int64, error) {
	return 0, 0, nil
}
func (b *fakeAgentBackend) TrimHistory(string, string, time.Time) error { return nil }
func (b *fakeAgentBackend) ResetTokenState()                            {}
func (b *fakeAgentBackend) GetChannelConfigs() (map[string]map[string]string, error) {
	return nil, nil
}
func (b *fakeAgentBackend) SetChannelConfig(channel string, values map[string]string) error {
	return nil
}
func (b *fakeAgentBackend) Close() error { return nil }
func (b *fakeAgentBackend) CallRPC(string, any) (json.RawMessage, error) {
	return nil, fmt.Errorf("not implemented")
}
func (b *fakeAgentBackend) Run(context.Context) error { return nil }

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

func TestIsCLISubscriptionSettingKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		// Positive cases: all 6 subscription-scoped keys
		{"llm_provider", true},
		{"llm_api_key", true},
		{"llm_base_url", true},
		{"llm_model", true},
		{"max_output_tokens", true},
		{"thinking_mode", true},
		// Negative cases: non-subscription keys
		{"theme", false},
		{"sandbox_mode", false},
		{"vanguard_model", false},
		{"max_iterations", false},
		{"", false},
		{"random_key", false},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			got := isCLISubscriptionSettingKey(tc.key)
			if got != tc.want {
				t.Errorf("isCLISubscriptionSettingKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}
