package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubscriptionConfigRoundtrip(t *testing.T) {
	cfg := Config{
		LLM: LLMConfig{
			Provider: "openai",
			BaseURL:  "https://api.openai.com/v1",
			APIKey:   "sk-test",
			Model:    "gpt-4",
		},
		Subscriptions: []SubscriptionConfig{
			{
				ID:       "default",
				Name:     "openai",
				Provider: "openai",
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "sk-test",
				Model:    "gpt-4",
				Active:   true,
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var cfg2 Config
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(cfg2.Subscriptions) != 1 {
		t.Fatalf("expected 1 subscription, got %d", len(cfg2.Subscriptions))
	}

	sub := cfg2.Subscriptions[0]
	if sub.ID != "default" {
		t.Errorf("expected ID=default, got %s", sub.ID)
	}
	if sub.Provider != "openai" {
		t.Errorf("expected Provider=openai, got %s", sub.Provider)
	}
	if sub.Model != "gpt-4" {
		t.Errorf("expected Model=gpt-4, got %s", sub.Model)
	}
	if !sub.Active {
		t.Error("expected Active=true")
	}
}

func TestSubscriptionConfigOmitEmpty(t *testing.T) {
	// Config without subscriptions should serialize to empty or omit the field
	cfg := Config{
		LLM: LLMConfig{Provider: "openai", Model: "gpt-4"},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var cfg2 Config
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if len(cfg2.Subscriptions) != 0 {
		t.Errorf("expected 0 subscriptions, got %d", len(cfg2.Subscriptions))
	}
}

func TestSubscriptionMigrationFromEmpty(t *testing.T) {
	// Simulate: user has no subscriptions, LLM config has provider/model
	cfg := &Config{
		LLM: LLMConfig{
			Provider: "openai",
			BaseURL:  "https://api.example.com/v1",
			APIKey:   "sk-key",
			Model:    "gpt-4",
		},
		Subscriptions: nil,
	}

	// Migration logic (mirrors main.go)
	if len(cfg.Subscriptions) == 0 {
		cfg.Subscriptions = []SubscriptionConfig{{
			ID:       "default",
			Name:     cfg.LLM.Provider,
			Provider: cfg.LLM.Provider,
			BaseURL:  cfg.LLM.BaseURL,
			APIKey:   cfg.LLM.APIKey,
			Model:    cfg.LLM.Model,
			Active:   true,
		}}
	}

	if len(cfg.Subscriptions) != 1 {
		t.Fatalf("expected 1 subscription after migration, got %d", len(cfg.Subscriptions))
	}

	sub := cfg.Subscriptions[0]
	if sub.ID != "default" {
		t.Errorf("expected ID=default, got %s", sub.ID)
	}
	if sub.Provider != "openai" {
		t.Errorf("expected Provider=openai, got %s", sub.Provider)
	}
	if sub.BaseURL != "https://api.example.com/v1" {
		t.Errorf("expected BaseURL from LLM config, got %s", sub.BaseURL)
	}
	if sub.APIKey != "sk-key" {
		t.Errorf("expected APIKey from LLM config, got %s", sub.APIKey)
	}
	if !sub.Active {
		t.Error("expected Active=true for migrated subscription")
	}
}

func TestSaveToFilePreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// 1. Write initial config with a custom unknown field
	initial := `{
  "llm": {"provider": "openai", "model": "gpt-4"},
  "agent": {"work_dir": "/tmp/test", "prompt_file": "CLAUDE.md", "custom_future_field": "keep_me"},
  "my_custom_section": {"key": "value"}
}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatalf("write initial: %v", err)
	}

	// 2. Load, modify a known field, save
	cfg := LoadFromFile(path)
	if cfg == nil {
		t.Fatal("LoadFromFile returned nil")
	}
	cfg.Agent.MaxIterations = 500

	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	// 3. Verify unknown fields are preserved
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, `"custom_future_field": "keep_me"`) {
		t.Errorf("custom_future_field not preserved in output:\n%s", content)
	}
	if !strings.Contains(content, `"my_custom_section"`) {
		t.Errorf("my_custom_section not preserved in output:\n%s", content)
	}

	// 4. Verify known fields are correctly updated
	if !strings.Contains(content, `"max_iterations": 500`) {
		t.Errorf("max_iterations not updated in output:\n%s", content)
	}
	if !strings.Contains(content, `"prompt_file": "CLAUDE.md"`) {
		t.Errorf("prompt_file not preserved in output:\n%s", content)
	}
	if !strings.Contains(content, `"work_dir": "/tmp/test"`) {
		t.Errorf("work_dir not preserved in output:\n%s", content)
	}
}

func TestSaveToFileCreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := &Config{
		LLM:   LLMConfig{Provider: "openai", Model: "gpt-4"},
		Agent: AgentConfig{WorkDir: "/tmp", PromptFile: "prompt.md"},
	}

	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if loaded.LLM.Model != "gpt-4" {
		t.Errorf("expected model gpt-4, got %s", loaded.LLM.Model)
	}
	if loaded.Agent.PromptFile != "prompt.md" {
		t.Errorf("expected prompt.md, got %s", loaded.Agent.PromptFile)
	}
}

func TestMergeJSONPreserveUnknown(t *testing.T) {
	existing := `{"a": 1, "b": 2, "unknown_key": "keep"}`
	structData := `{"a": 10, "c": 3}`

	merged, err := mergeJSONPreserveUnknown([]byte(existing), []byte(structData))
	if err != nil {
		t.Fatalf("mergeJSONPreserveUnknown: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(merged, &m); err != nil {
		t.Fatalf("unmarshal merged: %v", err)
	}

	// struct key overrides existing
	if m["a"] != float64(10) {
		t.Errorf("expected a=10, got %v", m["a"])
	}
	// existing-only key preserved
	if m["b"] != float64(2) {
		t.Errorf("expected b=2, got %v", m["b"])
	}
	// unknown key preserved
	if m["unknown_key"] != "keep" {
		t.Errorf("expected unknown_key=keep, got %v", m["unknown_key"])
	}
	// struct-only key added
	if m["c"] != float64(3) {
		t.Errorf("expected c=3, got %v", m["c"])
	}
}

func TestSaveToFileLoadSaveRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Write a full config with all known fields
	cfg := &Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			BaseURL:  "https://open.bigmodel.cn/api/anthropic",
			APIKey:   "test-key",
			Model:    "glm-5.1",
		},
		Agent: AgentConfig{
			MaxIterations:    2000,
			MaxConcurrency:   3,
			MemoryProvider:   "flat",
			WorkDir:          "/ipfs_flash/test",
			PromptFile:       "CLAUDE.md",
			MaxContextTokens: 200000,
		},
		Feishu: FeishuConfig{
			Enabled: true,
			AppID:   "test-app",
		},
	}

	if err := SaveToFile(path, cfg); err != nil {
		t.Fatalf("first save: %v", err)
	}

	// Load and save again (simulates the load → modify → save cycle)
	cfg2 := LoadFromFile(path)
	if cfg2 == nil {
		t.Fatal("LoadFromFile returned nil")
	}
	cfg2.Agent.MaxIterations = 3000
	if err := SaveToFile(path, cfg2); err != nil {
		t.Fatalf("second save: %v", err)
	}

	// Verify all fields preserved
	cfg3 := LoadFromFile(path)
	if cfg3 == nil {
		t.Fatal("final LoadFromFile returned nil")
	}
	if cfg3.Agent.PromptFile != "CLAUDE.md" {
		t.Errorf("prompt_file lost: got %q", cfg3.Agent.PromptFile)
	}
	if cfg3.Agent.WorkDir != "/ipfs_flash/test" {
		t.Errorf("work_dir lost: got %q", cfg3.Agent.WorkDir)
	}
	if cfg3.Agent.MaxIterations != 3000 {
		t.Errorf("max_iterations not updated: got %d", cfg3.Agent.MaxIterations)
	}
	if cfg3.LLM.Provider != "anthropic" {
		t.Errorf("llm provider lost: got %q", cfg3.LLM.Provider)
	}
	if cfg3.Feishu.AppID != "test-app" {
		t.Errorf("feishu app_id lost: got %q", cfg3.Feishu.AppID)
	}
}
