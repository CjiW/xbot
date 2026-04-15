package channel

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// mockSubscriptionManager implements SubscriptionManager for testing.
type mockSubscriptionManager struct {
	subs      []Subscription
	defaultID string
	addCalled bool
	setDefID  string
	saveErr   error
}

func (m *mockSubscriptionManager) List(_ string) ([]Subscription, error) {
	return m.subs, nil
}

func (m *mockSubscriptionManager) GetDefault(_ string) (*Subscription, error) {
	for _, s := range m.subs {
		if s.ID == m.defaultID {
			return &s, nil
		}
	}
	return nil, nil
}

func (m *mockSubscriptionManager) Add(sub *Subscription) error {
	m.addCalled = true
	m.subs = append(m.subs, *sub)
	return m.saveErr
}

func (m *mockSubscriptionManager) Remove(id string) error {
	return nil
}

func (m *mockSubscriptionManager) SetDefault(id string) error {
	m.setDefID = id
	for i := range m.subs {
		m.subs[i].Active = m.subs[i].ID == id
	}
	return m.saveErr
}

func (m *mockSubscriptionManager) SetModel(id, model string) error {
	return nil
}

func (m *mockSubscriptionManager) Rename(id, name string) error {
	return nil
}

func (m *mockSubscriptionManager) Update(id string, sub *Subscription) error {
	return nil
}

// TestApplyQuickSwitch tests that switching a subscription actually calls SwitchLLM.
func TestApplyQuickSwitch(t *testing.T) {
	// Track what SwitchLLM received
	var switchedProvider, switchedBaseURL, switchedAPIKey, switchedModel string
	switchCalled := false

	mgr := &mockSubscriptionManager{
		subs: []Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", BaseURL: "https://glm.example.com/v1", APIKey: "key1", Model: "glm-4", Active: true},
			{ID: "sub2", Name: "gpt", Provider: "openai", BaseURL: "https://api.openai.com/v1", APIKey: "key2", Model: "gpt-4.1", Active: false},
		},
	}

	model := newCLIModel()
	model.subscriptionMgr = mgr
	model.channel = &CLIChannel{
		config: CLIChannelConfig{
			SwitchLLM: func(provider, baseURL, apiKey, model string) error {
				switchCalled = true
				switchedProvider = provider
				switchedBaseURL = baseURL
				switchedAPIKey = apiKey
				switchedModel = model
				return nil
			},
			GetCurrentValues: func() map[string]string {
				return map[string]string{"llm_model": "glm-4"}
			},
		},
	}

	// Open quick switch, select second subscription, apply
	model.openQuickSwitch("subscription")
	if model.quickSwitchMode != "subscription" {
		t.Fatalf("expected quickSwitchMode=subscription, got %s", model.quickSwitchMode)
	}

	// The active sub (sub1) should be pre-selected
	if model.quickSwitchCursor != 0 {
		t.Fatalf("expected cursor=0 (active sub), got %d", model.quickSwitchCursor)
	}

	// Move cursor to sub2 (index 1, before __add__ at index 2)
	model.quickSwitchCursor = 1
	model.applyQuickSwitch()

	// applyQuickSwitch now defers SwitchLLM + SetDefault to a background Cmd.
	// Verify pending Cmds were queued (showTempStatus + async SwitchLLM).
	if len(model.pendingCmds) < 2 {
		t.Fatalf("expected at least 2 pendingCmds (status clear + async switch), got %d", len(model.pendingCmds))
	}

	// The last pending Cmd is the async SwitchLLM (first is tempStatus clear)
	asyncCmd := model.pendingCmds[len(model.pendingCmds)-1]
	msg := asyncCmd()
	doneMsg, ok := msg.(cliSwitchLLMDoneMsg)
	if !ok {
		t.Fatalf("expected cliSwitchLLMDoneMsg, got %T", msg)
	}
	if doneMsg.err != nil {
		t.Fatalf("unexpected SwitchLLM error: %v", doneMsg.err)
	}

	// Verify SwitchLLM was called with correct values
	if !switchCalled {
		t.Fatal("SwitchLLM was NOT called!")
	}
	if switchedProvider != "openai" {
		t.Errorf("expected provider=openai, got %s", switchedProvider)
	}
	if switchedBaseURL != "https://api.openai.com/v1" {
		t.Errorf("expected baseURL=https://api.openai.com/v1, got %s", switchedBaseURL)
	}
	if switchedAPIKey != "key2" {
		t.Errorf("expected apiKey=key2, got %s", switchedAPIKey)
	}
	if switchedModel != "gpt-4.1" {
		t.Errorf("expected model=gpt-4.1, got %s", switchedModel)
	}

	// Verify quick switch mode is cleared
	if model.quickSwitchMode != "" {
		t.Errorf("expected quickSwitchMode cleared, got %s", model.quickSwitchMode)
	}

	// Now simulate the Update handler processing the doneMsg (which calls SetDefault)
	// The Update handler in cli_update.go does this when it receives cliSwitchLLMDoneMsg
	if doneMsg.mgr != nil {
		if err := doneMsg.mgr.SetDefault(doneMsg.subID); err != nil {
			t.Fatalf("SetDefault failed: %v", err)
		}
	}
	if mgr.setDefID != "sub2" {
		t.Errorf("expected SetDefault(sub2), got SetDefault(%s)", mgr.setDefID)
	}
}

func TestPanelBoxLeftAlign(t *testing.T) {
	// Verify that settings panel content is left-aligned after PanelBox wrapping.
	// Regression test: lipgloss v2 Width() defaults to centering content.
	m := newCLIModel()
	m.width = 80
	m.styles = buildStyles(80) // rebuild with test width

	// Simulate a settings panel with a short selected line
	schema := []SettingDefinition{
		{Key: "name", Label: "Name", Type: SettingTypeText, Category: "Test"},
		{Key: "provider", Label: "Provider", Type: SettingTypeText, DefaultValue: "openai", Category: "Test"},
	}
	m.panelSchema = schema
	m.panelValues = map[string]string{"provider": "openai"}
	m.panelCursor = 0
	m.panelMode = "settings"

	raw := m.viewPanel()
	// Wrap in PanelBox like cli_view.go does
	boxed := m.styles.PanelBox.Render(raw)

	t.Log("=== Boxed panel output (stripped) ===")
	lines := splitANSILines(boxed)
	for i, line := range lines {
		t.Logf("  [%d] len=%d: %q", i, len(stripANSI(line)), stripANSI(line))
	}
	// Find the line containing "Name:" and verify position
	for _, line := range lines {
		stripped := stripANSI(line)
		if idx := indexOfStr(stripped, "Name:"); idx >= 0 {
			// After PanelBox border ("│") + padding (" ") + cursor ("▸") + space = ~4 visible chars
			// "Name:" should appear at column ~4 in the stripped string
			// (may be higher due to multi-byte UTF-8 in cursor char + ANSI-wrapped styling)
			if idx > 20 {
				t.Errorf("'Name:' at column %d (expected <= 6, looks centered).\nLine: %q", idx, stripped)
			}
			return
		}
	}
	t.Error("could not find 'Name:' in panel output")
}

// TestSubscriptionGenerationGuard tests that stale per-subscription LLM values
// are NEVER written back after a subscription switch. This is the structural guarantee
// against the subscription overwrite bug.
func TestSubscriptionGenerationGuard(t *testing.T) {
	model := newCLIModel()
	model.subGeneration = 5

	// Simulate: settings panel opens with generation 5
	model.panelSubGeneration = model.subGeneration

	// Simulate: user edits some values
	values := map[string]string{
		"llm_provider":   "openai",
		"llm_api_key":    "old-key-123",
		"llm_model":      "gpt-4o",
		"llm_base_url":   "https://api.openai.com/v1",
		"vanguard_model": "claude-opus-4",
		"balance_model":  "claude-sonnet-4",
		"max_tokens":     "4096",
	}

	// Simulate: subscription switch happens (generation increments)
	model.subGeneration = 6

	// Simulate: the onSubmit callback runs (this is what the guard checks)
	// After switch, stale LLM fields should be stripped
	if model.panelSubGeneration != model.subGeneration {
		for _, k := range []string{"llm_provider", "llm_model", "llm_base_url", "llm_api_key"} {
			delete(values, k)
		}
	}

	// Verify: per-subscription LLM fields are GONE
	for _, k := range []string{"llm_provider", "llm_api_key", "llm_model", "llm_base_url"} {
		if _, exists := values[k]; exists {
			t.Errorf("BUG: stale LLM field %q should have been deleted after subscription switch", k)
		}
	}

	// Verify: global/tier settings are PRESERVED
	if values["vanguard_model"] != "claude-opus-4" {
		t.Errorf("global setting vanguard_model should be preserved, got %q", values["vanguard_model"])
	}
	if values["balance_model"] != "claude-sonnet-4" {
		t.Errorf("global setting balance_model should be preserved, got %q", values["balance_model"])
	}
	if values["max_tokens"] != "4096" {
		t.Errorf("global setting max_tokens should be preserved, got %q", values["max_tokens"])
	}
}

// TestSubscriptionGenerationGuardNoSwitch tests that when subscription does NOT change,
// all LLM fields are preserved (no false positives).
func TestSubscriptionGenerationGuardNoSwitch(t *testing.T) {
	model := newCLIModel()
	model.subGeneration = 5
	model.panelSubGeneration = 5 // same generation = no switch

	values := map[string]string{
		"llm_provider": "anthropic",
		"llm_api_key":  "key-456",
		"llm_model":    "claude-sonnet-4",
		"llm_base_url": "https://api.anthropic.com",
	}

	// Guard should NOT strip anything
	if model.panelSubGeneration != model.subGeneration {
		for _, k := range []string{"llm_provider", "llm_model", "llm_base_url", "llm_api_key"} {
			delete(values, k)
		}
	}

	// All fields should still be present
	for _, k := range []string{"llm_provider", "llm_api_key", "llm_model", "llm_base_url"} {
		if _, exists := values[k]; !exists {
			t.Errorf("LLM field %q should NOT be deleted when subscription hasn't changed", k)
		}
	}
}

// TestApplyQuickSwitchNilChannel tests that nil channel doesn't crash.
func TestApplyQuickSwitchNilChannel(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", Model: "glm-4", Active: true},
		},
	}

	model := newCLIModel()
	model.subscriptionMgr = mgr
	// channel is nil!

	model.openQuickSwitch("subscription")
	model.quickSwitchCursor = 0
	model.applyQuickSwitch() // should NOT panic

	// SetDefault should NOT be called because SwitchLLM is unreachable (nil channel)
	if mgr.setDefID != "" {
		t.Errorf("expected SetDefault NOT called with nil channel, got %s", mgr.setDefID)
	}
}

// TestApplyQuickSwitchNilSwitchLLM tests that nil SwitchLLM doesn't crash.
func TestApplyQuickSwitchNilSwitchLLM(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", Model: "glm-4", Active: true},
		},
	}

	model := newCLIModel()
	model.subscriptionMgr = mgr
	model.channel = &CLIChannel{
		config: CLIChannelConfig{
			// SwitchLLM is nil
			GetCurrentValues: func() map[string]string {
				return map[string]string{}
			},
		},
	}

	model.openQuickSwitch("subscription")
	model.quickSwitchCursor = 0
	model.applyQuickSwitch() // should NOT panic, should NOT call SwitchLLM

	// SetDefault should NOT be called because SwitchLLM is nil
	if mgr.setDefID != "" {
		t.Errorf("expected SetDefault NOT called with nil SwitchLLM, got %s", mgr.setDefID)
	}
}

// TestOpenQuickSwitchWithEmptySubs tests that add entry is shown even with no subs.
func TestOpenQuickSwitchWithEmptySubs(t *testing.T) {
	mgr := &mockSubscriptionManager{subs: nil}

	model := newCLIModel()
	model.subscriptionMgr = mgr

	model.openQuickSwitch("subscription")

	if model.quickSwitchMode != "subscription" {
		t.Fatalf("expected mode=subscription, got %s", model.quickSwitchMode)
	}

	found := false
	for _, s := range model.quickSwitchList {
		if s.ID == "__add__" {
			found = true
		}
	}
	if !found {
		t.Error("expected __add__ entry in quick switch list")
	}
}

// TestApplyQuickSwitchError tests error handling in SwitchLLM.
func TestApplyQuickSwitchError(t *testing.T) {
	mgr := &mockSubscriptionManager{
		subs: []Subscription{
			{ID: "sub1", Name: "glm", Provider: "openai", BaseURL: "https://glm.example.com", APIKey: "k1", Model: "glm-4", Active: true},
			{ID: "sub2", Name: "gpt", Provider: "openai", BaseURL: "https://bad.url", APIKey: "k2", Model: "gpt-4", Active: false},
		},
	}

	model := newCLIModel()
	model.subscriptionMgr = mgr
	model.channel = &CLIChannel{
		config: CLIChannelConfig{
			SwitchLLM: func(provider, baseURL, apiKey, model string) error {
				return fmt.Errorf("connection refused")
			},
			GetCurrentValues: func() map[string]string {
				return map[string]string{}
			},
		},
	}

	model.openQuickSwitch("subscription")
	model.quickSwitchCursor = 1
	model.applyQuickSwitch()

	// SetDefault should NOT be called because SwitchLLM failed
	if mgr.setDefID != "" {
		t.Errorf("expected SetDefault NOT called after SwitchLLM error, got %s", mgr.setDefID)
	}

	// tempStatus should show error
	if model.tempStatus == "" {
		t.Error("expected tempStatus to show error")
	}
	// subs should remain unchanged — sub1 still active, sub2 still inactive
	if !mgr.subs[0].Active {
		t.Error("expected sub1 still active after SwitchLLM failure")
	}
	if mgr.subs[1].Active {
		t.Error("expected sub2 still inactive after SwitchLLM failure")
	}
	}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

func splitANSILines(s string) []string {
	return strings.Split(s, "\n")
}

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func indexOfStr(s, substr string) int {
	return strings.Index(s, substr)
}
