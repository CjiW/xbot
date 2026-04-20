package agent

import (
	"testing"

	"xbot/config"
	"xbot/llm"
	"xbot/storage/sqlite"
)

func TestGuessProvider(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-sonnet-4-20250514", "anthropic"},
		{"claude-opus-4-20250115", "anthropic"},
		{"gpt-4o", "openai"},
		{"gpt-4.1", "openai"},
		{"o1-preview", "openai"},
		{"o3-mini", "openai"},
		{"deepseek-chat", "deepseek"},
		{"deepseek-reasoner", "deepseek"},
		{"gemini-2.0-flash", "google"},
		{"qwen-max", "qwen"},
		{"unknown-model", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := guessProvider(tt.model)
			if got != tt.want {
				t.Errorf("guessProvider(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestGetLLMForModel_EmptyTarget(t *testing.T) {
	// Empty target model → should return default model name without hitting subscription logic
	f := NewLLMFactory(nil, nil, "default-model")
	f.defaultThinkingMode = "auto"

	// Verify the early return path: targetModel="" should not try to list subscriptions
	// (subscriptionSvc is nil, so if it tried, we'd get a different error)
	_, model, _, tm, usedCustom := f.GetLLMForModel("user1", "")
	if model != "default-model" {
		t.Errorf("model = %q, want %q", model, "default-model")
	}
	if usedCustom {
		t.Error("usedCustom should be false for empty target model")
	}
	if tm != "auto" {
		t.Errorf("thinkingMode = %q, want %q", tm, "auto")
	}
}

func TestGetLLMForModel_NilSubscriptionSvc(t *testing.T) {
	f := NewLLMFactory(nil, nil, "default-model")
	f.defaultThinkingMode = "auto"

	// No subscriptionSvc + explicit model → model not found in any subscription,
	// fallback to default client with its OWN model (not the target model).
	_, model, _, _, usedCustom := f.GetLLMForModel("user1", "claude-opus-4-20250115")
	if model != "default-model" {
		t.Errorf("model = %q, want default-model (fallback uses default client's model)", model)
	}
	if usedCustom {
		t.Error("usedCustom should be false when model not found in any subscription")
	}
}

func TestNormalizeModelTier(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"vanguard", "vanguard"},
		{"VANGUARD", "vanguard"},
		{"Vanguard", "vanguard"},
		{"strong", "vanguard"},
		{"Strong", "vanguard"},
		{"balance", "balance"},
		{"medium", "balance"},
		{"swift", "swift"},
		{"weak", "swift"},
		{"gpt-4o", ""},
		{"", ""},
		{"unknown", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := normalizeModelTier(tt.input)
			if got != tt.want {
				t.Errorf("normalizeModelTier(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestResolveTierModel(t *testing.T) {
	f := NewLLMFactory(nil, nil, "default-model")

	// No tiers configured → tier keywords are recognized but model is empty
	model, usedTier := f.resolveTierModel("vanguard")
	if !usedTier {
		t.Error("usedTier should be true (keyword recognized)")
	}
	if model != "" {
		t.Errorf("model = %q, want empty", model)
	}

	// Non-tier value passes through unchanged
	model, usedTier = f.resolveTierModel("gpt-4o")
	if usedTier {
		t.Error("usedTier should be false for non-tier value")
	}
	if model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", model)
	}

	// Configure tiers
	f.SetModelTiers(config.LLMConfig{
		VanguardModel: "claude-opus-4-20250115",
		BalanceModel:  "claude-sonnet-4-20250514",
		SwiftModel:    "gpt-4o-mini",
	})

	model, usedTier = f.resolveTierModel("vanguard")
	if !usedTier {
		t.Error("usedTier should be true")
	}
	if model != "claude-opus-4-20250115" {
		t.Errorf("model = %q, want claude-opus-4-20250115", model)
	}

	model, usedTier = f.resolveTierModel("balance")
	if !usedTier {
		t.Error("usedTier should be true")
	}
	if model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want claude-sonnet-4-20250514", model)
	}

	model, usedTier = f.resolveTierModel("swift")
	if !usedTier {
		t.Error("usedTier should be true")
	}
	if model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", model)
	}

	// Aliases: strong/medium/weak
	model, _ = f.resolveTierModel("strong")
	if model != "claude-opus-4-20250115" {
		t.Errorf("model = %q, want claude-opus-4-20250115", model)
	}

	model, _ = f.resolveTierModel("medium")
	if model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want claude-sonnet-4-20250514", model)
	}

	model, _ = f.resolveTierModel("weak")
	if model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", model)
	}

	// Partial config: only vanguard set
	f.SetModelTiers(config.LLMConfig{
		VanguardModel: "opus",
	})
	model, usedTier = f.resolveTierModel("balance")
	if !usedTier {
		t.Error("usedTier should be true even for unconfigured tier")
	}
	// balance unconfigured → fallback to vanguard
	if model != "opus" {
		t.Errorf("model = %q, want opus (fallback from unconfigured balance to vanguard)", model)
	}
}

func TestGetLLMForModel_TierResolution(t *testing.T) {
	f := NewLLMFactory(nil, nil, "default-model")
	f.defaultThinkingMode = "auto"

	// Tier with no subscriptionSvc → model not found, fallback to default client
	f.SetModelTiers(config.LLMConfig{
		VanguardModel: "claude-opus-4-20250115",
	})

	_, model, _, _, usedCustom := f.GetLLMForModel("user1", "vanguard")
	if usedCustom {
		t.Error("usedCustom should be false when model not found in any subscription")
	}
	if model != "default-model" {
		t.Errorf("model = %q, want default-model (fallback)", model)
	}

	// Non-tier model with no subscriptionSvc → same fallback
	_, model, _, _, usedCustom = f.GetLLMForModel("user1", "gpt-4o")
	if usedCustom {
		t.Error("usedCustom should be false when model not found in any subscription")
	}
	if model != "default-model" {
		t.Errorf("model = %q, want default-model (fallback)", model)
	}
}

func TestResolveTierModel_UnconfiguredFallback(t *testing.T) {
	// When swift/vanguard are not configured, should fallback to balance
	f := NewLLMFactory(nil, nil, "default-model")
	f.SetModelTiers(config.LLMConfig{
		BalanceModel: "gpt-4o",
		// VanguardModel and SwiftModel intentionally empty
	})

	// swift not configured → fallback to balance
	model, usedTier := f.resolveTierModel("swift")
	if !usedTier {
		t.Error("usedTier should be true")
	}
	if model != "gpt-4o" {
		t.Errorf("swift fallback = %q, want gpt-4o (balance)", model)
	}

	// vanguard not configured → fallback to balance
	model, usedTier = f.resolveTierModel("vanguard")
	if !usedTier {
		t.Error("usedTier should be true")
	}
	if model != "gpt-4o" {
		t.Errorf("vanguard fallback = %q, want gpt-4o (balance)", model)
	}

	// balance configured → returns balance
	model, usedTier = f.resolveTierModel("balance")
	if !usedTier {
		t.Error("usedTier should be true")
	}
	if model != "gpt-4o" {
		t.Errorf("balance = %q, want gpt-4o", model)
	}
}

func TestResolveTierModel_AllUnconfigured(t *testing.T) {
	// All tiers unconfigured → returns empty string (will fall to default client)
	f := NewLLMFactory(nil, nil, "default-model")
	f.SetModelTiers(config.LLMConfig{})

	model, usedTier := f.resolveTierModel("swift")
	if !usedTier {
		t.Error("usedTier should be true (tier keyword recognized)")
	}
	if model != "" {
		t.Errorf("model = %q, want empty (no tiers configured)", model)
	}
}

func TestHasCustomLLMChecksSubscriptionSvc(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	db, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	factory := NewLLMFactory(sqlite.NewUserLLMConfigService(db), &llm.MockLLM{}, "default-model")
	subSvc := sqlite.NewLLMSubscriptionService(db)
	factory.SetSubscriptionSvc(subSvc)
	if err := subSvc.Add(&sqlite.LLMSubscription{ID: "sub-1", SenderID: "cli_user", Name: "s1", Provider: "openai", BaseURL: "https://example.com/v1", APIKey: "sk-test", Model: "m1", IsDefault: true}); err != nil {
		t.Fatalf("add sub: %v", err)
	}
	if !factory.HasCustomLLM("cli_user") {
		t.Fatal("expected HasCustomLLM to return true when default subscription exists")
	}
}
