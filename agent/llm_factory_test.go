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

// TestInvalidate_ClearsPerChatCache verifies that Invalidate(senderID) clears
// both user-level and per-chat (senderID:chatID) cache entries.
// This is the fix for: switching sub then changing model in settings was stuck
// on the old model because Invalidate only cleared the user-level key.
func TestInvalidate_ClearsPerChatCache(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")

	senderID := "cli_user"
	chatID := "/home/user/project"
	subA := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api-a.com/v1", APIKey: "sk-a",
		Model: "gpt-4o", MaxOutputTokens: 8192,
	}
	subB := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api-b.com/v1", APIKey: "sk-b",
		Model: "deepseek-v3", MaxOutputTokens: 4096,
	}

	// Simulate: SwitchSubscription creates both user-level and per-chat caches
	if err := f.SwitchSubscription(senderID, subA, chatID); err != nil {
		t.Fatalf("SwitchSubscription subA: %v", err)
	}

	// Verify both caches exist
	_, modelA, _, _ := f.GetLLMForChat(senderID, chatID)
	if modelA != "gpt-4o" {
		t.Fatalf("initial model = %q, want gpt-4o", modelA)
	}

	// Simulate: set_default_subscription calls Invalidate then SwitchSubscription
	// (the actual server handler path for subscription switching)
	f.Invalidate(senderID)
	if err := f.SwitchSubscription(senderID, subB, chatID); err != nil {
		t.Fatalf("SwitchSubscription subB: %v", err)
	}

	_, modelB, _, _ := f.GetLLMForChat(senderID, chatID)
	if modelB != "deepseek-v3" {
		t.Errorf("after sub switch, model = %q, want deepseek-v3", modelB)
	}

	// Simulate: update_subscription (settings panel) calls Invalidate + SwitchSubscription
	// with chatID="" — per-chat cache was NOT cleared before the fix
	f.Invalidate(senderID)
	updatedSubB := *subB
	updatedSubB.Model = "deepseek-r1"
	updatedSubB.MaxOutputTokens = 16384
	if err := f.SwitchSubscription(senderID, &updatedSubB, ""); err != nil {
		t.Fatalf("SwitchSubscription updatedSubB: %v", err)
	}

	// GetLLMForChat should NOT return stale per-chat cache
	_, modelUpdated, _, thinkingUpdated := f.GetLLMForChat(senderID, chatID)
	if modelUpdated != "deepseek-r1" {
		t.Errorf("after settings update, model = %q, want deepseek-r1 (stale per-chat cache bug)", modelUpdated)
	}
	// Verify thinking mode is also not stale
	if thinkingUpdated != "" {
		t.Errorf("after settings update, thinkingMode = %q, want empty", thinkingUpdated)
	}
}

// TestSwitchModel_ClearsPerChatCache verifies that SwitchModel clears per-chat
// model caches so GetLLMForChat returns the new model instead of a stale
// per-chat entry.
func TestSwitchModel_ClearsPerChatCache(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")

	senderID := "cli_user"
	chatID := "/home/user/project"
	sub := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api.example.com/v1", APIKey: "sk-test",
		Model: "gpt-4o", MaxOutputTokens: 8192,
	}

	// Create per-chat cache via SwitchSubscription
	if err := f.SwitchSubscription(senderID, sub, chatID); err != nil {
		t.Fatalf("SwitchSubscription: %v", err)
	}

	// Now SwitchModel (e.g., from quick panel model switch)
	f.SwitchModel(senderID, "gpt-4o-mini")

	// GetLLMForChat should return the new model, not the stale per-chat one
	_, model, _, _ := f.GetLLMForChat(senderID, chatID)
	if model != "gpt-4o-mini" {
		t.Errorf("after SwitchModel, per-chat model = %q, want gpt-4o-mini (stale per-chat cache bug)", model)
	}
}

// TestInvalidate_DoesNotAffectOtherUsers verifies that Invalidate(senderID) only
// clears entries for that specific sender, not other users.
func TestInvalidate_DoesNotAffectOtherUsers(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")

	subA := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api-a.com/v1", APIKey: "sk-a",
		Model: "gpt-4o",
	}
	subB := &sqlite.LLMSubscription{
		Provider: "openai", BaseURL: "https://api-b.com/v1", APIKey: "sk-b",
		Model: "claude-3-opus",
	}

	// User A gets per-chat cache
	if err := f.SwitchSubscription("userA", subA, "/home/a"); err != nil {
		t.Fatalf("SwitchSubscription userA: %v", err)
	}
	// User B gets per-chat cache
	if err := f.SwitchSubscription("userB", subB, "/home/b"); err != nil {
		t.Fatalf("SwitchSubscription userB: %v", err)
	}

	// Invalidate user A — should NOT affect user B
	f.Invalidate("userA")

	// User B's per-chat cache should still work
	_, modelB, _, _ := f.GetLLMForChat("userB", "/home/b")
	if modelB != "claude-3-opus" {
		t.Errorf("userB model after Invalidate(userA) = %q, want claude-3-opus", modelB)
	}
}

// TestGetLLMForModel_ConfigSubExactMatch verifies the config.json subscription path:
// when configSubsFn returns a subscription whose Model matches the resolved tier model,
// GetLLMForModel should use that subscription (usedCustom=true).
func TestGetLLMForModel_ConfigSubExactMatch(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")
	f.defaultThinkingMode = "auto"

	// Configure tier so "vanguard" resolves to "gpt-4o"
	f.SetModelTiers(config.LLMConfig{
		VanguardModel: "gpt-4o",
	})

	// Set up configSubsFn with a matching subscription
	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "sub-1",
				Name:     "test-sub",
				Provider: "openai",
				BaseURL:  "https://api.test/v1",
				APIKey:   "sk-test",
				Model:    "gpt-4o",
			},
		}
	})

	client, model, _, _, usedCustom := f.GetLLMForModel("user1", "vanguard")
	if !usedCustom {
		t.Error("usedCustom should be true when config sub matches resolved model")
	}
	if model != "gpt-4o" {
		t.Errorf("model = %q, want %q", model, "gpt-4o")
	}
	if client == nil {
		t.Error("client should not be nil when config sub matches")
	}
}

// TestGetLLMForModel_ConfigSubNoMatch verifies that when configSubsFn returns
// subscriptions but none match the resolved tier model, it falls back to the
// default LLM (usedCustom=false, model=default-model).
func TestGetLLMForModel_ConfigSubNoMatch(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")
	f.defaultThinkingMode = "auto"

	// Configure tier so "vanguard" resolves to "gpt-4o"
	f.SetModelTiers(config.LLMConfig{
		VanguardModel: "gpt-4o",
	})

	// Config sub has a different model — no match
	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "sub-1",
				Name:     "other-sub",
				Provider: "openai",
				BaseURL:  "https://api.test/v1",
				APIKey:   "sk-test",
				Model:    "other-model",
			},
		}
	})

	client, model, _, _, usedCustom := f.GetLLMForModel("user1", "vanguard")
	if usedCustom {
		t.Error("usedCustom should be false when no config sub matches resolved model")
	}
	if model != "default-model" {
		t.Errorf("model = %q, want %q (fallback to default)", model, "default-model")
	}
	if client == nil {
		t.Error("client should not be nil (fallback default client)")
	}
}

// TestGetLLMForModel_ConfigSubSkipsEmptyCredentials verifies that config
// subscriptions with matching Model but empty BaseURL or APIKey are skipped,
// and the function falls through to the default LLM.
func TestGetLLMForModel_ConfigSubSkipsEmptyCredentials(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")
	f.defaultThinkingMode = "auto"

	// Configure tier so "vanguard" resolves to "gpt-4o"
	f.SetModelTiers(config.LLMConfig{
		VanguardModel: "gpt-4o",
	})

	// Sub-tests for empty BaseURL and empty APIKey
	tests := []struct {
		name string
		sub  config.SubscriptionConfig
	}{
		{
			name: "empty BaseURL",
			sub: config.SubscriptionConfig{
				ID:       "sub-empty-url",
				Name:     "no-url",
				Provider: "openai",
				BaseURL:  "",
				APIKey:   "sk-test",
				Model:    "gpt-4o",
			},
		},
		{
			name: "empty APIKey",
			sub: config.SubscriptionConfig{
				ID:       "sub-empty-key",
				Name:     "no-key",
				Provider: "openai",
				BaseURL:  "https://api.test/v1",
				APIKey:   "",
				Model:    "gpt-4o",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f.SetConfigSubs(func() []config.SubscriptionConfig {
				return []config.SubscriptionConfig{tt.sub}
			})

			_, model, _, _, usedCustom := f.GetLLMForModel("user1", "vanguard")
			if usedCustom {
				t.Error("usedCustom should be false when config sub has empty credentials")
			}
			if model != "default-model" {
				t.Errorf("model = %q, want %q (fallback to default)", model, "default-model")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for buildModelSubscriptionMap & configSubToLLMSubscription
// ---------------------------------------------------------------------------

// TestBuildModelSubscriptionMap_ConfigSubs verifies that config subscriptions
// with different models each produce an entry in the model→subscription map.
func TestBuildModelSubscriptionMap_ConfigSubs(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")

	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "sub-a",
				Name:     "OpenAI",
				Provider: "openai",
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "sk-openai",
				Model:    "gpt-4o",
			},
			{
				ID:       "sub-b",
				Name:     "Anthropic",
				Provider: "anthropic",
				BaseURL:  "https://api.anthropic.com",
				APIKey:   "sk-ant-key",
				Model:    "claude-sonnet-4-20250514",
			},
		}
	})

	m := f.buildModelSubscriptionMap("user1")

	if len(m) != 2 {
		t.Fatalf("map size = %d, want 2", len(m))
	}
	if sub, ok := m["gpt-4o"]; !ok {
		t.Error("missing gpt-4o entry")
	} else if sub.ID != "sub-a" {
		t.Errorf("gpt-4o mapped to sub %q, want sub-a", sub.ID)
	}
	if sub, ok := m["claude-sonnet-4-20250514"]; !ok {
		t.Error("missing claude-sonnet-4-20250514 entry")
	} else if sub.ID != "sub-b" {
		t.Errorf("claude-sonnet-4-20250514 mapped to sub %q, want sub-b", sub.ID)
	}
}

// TestBuildModelSubscriptionMap_ConfigSubsSkipsEmptyCredentials verifies that
// config subscriptions with empty BaseURL or APIKey are not added to the map.
func TestBuildModelSubscriptionMap_ConfigSubsSkipsEmptyCredentials(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")

	// Sub with matching Model but empty BaseURL — must be skipped.
	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "sub-empty-url",
				Name:     "No URL",
				Provider: "openai",
				BaseURL:  "",
				APIKey:   "sk-test",
				Model:    "gpt-4o",
			},
			{
				ID:       "sub-empty-key",
				Name:     "No Key",
				Provider: "openai",
				BaseURL:  "https://api.openai.com/v1",
				APIKey:   "",
				Model:    "gpt-4o-mini",
			},
		}
	})

	m := f.buildModelSubscriptionMap("user1")

	if len(m) != 0 {
		t.Fatalf("map size = %d, want 0 (both subs have empty credentials)", len(m))
	}
}

// TestBuildModelSubscriptionMap_EmptySenderID verifies that with an empty
// senderID and nil subscriptionSvc, only config subs are included.
func TestBuildModelSubscriptionMap_EmptySenderID(t *testing.T) {
	f := NewLLMFactory(nil, &llm.MockLLM{}, "default-model")
	// subscriptionSvc is nil by default — no DB path at all.

	f.SetConfigSubs(func() []config.SubscriptionConfig {
		return []config.SubscriptionConfig{
			{
				ID:       "cfg-1",
				Name:     "ConfigSub",
				Provider: "openai",
				BaseURL:  "https://api.test/v1",
				APIKey:   "sk-cfg",
				Model:    "gpt-4o",
			},
		}
	})

	// Empty senderID — DB path is also gated by senderID != ""
	m := f.buildModelSubscriptionMap("")

	if len(m) != 1 {
		t.Fatalf("map size = %d, want 1 (only config sub)", len(m))
	}
	if _, ok := m["gpt-4o"]; !ok {
		t.Error("missing gpt-4o entry from config sub")
	}
}

// TestConfigSubToLLMSubscription verifies that configSubToLLMSubscription
// correctly maps every field from SubscriptionConfig to LLMSubscription.
func TestConfigSubToLLMSubscription(t *testing.T) {
	cs := config.SubscriptionConfig{
		ID:              "sub-42",
		Name:            "My Sub",
		Provider:        "deepseek",
		BaseURL:         "https://api.deepseek.com/v1",
		APIKey:          "sk-deep",
		Model:           "deepseek-chat",
		MaxOutputTokens: 4096,
		ThinkingMode:    "enabled",
	}

	sub := configSubToLLMSubscription(cs)

	if sub.ID != "sub-42" {
		t.Errorf("ID = %q, want %q", sub.ID, "sub-42")
	}
	if sub.Name != "My Sub" {
		t.Errorf("Name = %q, want %q", sub.Name, "My Sub")
	}
	if sub.Provider != "deepseek" {
		t.Errorf("Provider = %q, want %q", sub.Provider, "deepseek")
	}
	if sub.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("BaseURL = %q, want %q", sub.BaseURL, "https://api.deepseek.com/v1")
	}
	if sub.APIKey != "sk-deep" {
		t.Errorf("APIKey = %q, want %q", sub.APIKey, "sk-deep")
	}
	if sub.Model != "deepseek-chat" {
		t.Errorf("Model = %q, want %q", sub.Model, "deepseek-chat")
	}
	if sub.MaxOutputTokens != 4096 {
		t.Errorf("MaxOutputTokens = %d, want 4096", sub.MaxOutputTokens)
	}
	if sub.ThinkingMode != "enabled" {
		t.Errorf("ThinkingMode = %q, want %q", sub.ThinkingMode, "enabled")
	}
}
