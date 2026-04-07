package config

import (
	"encoding/json"
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
