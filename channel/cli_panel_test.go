package channel

import (
	"fmt"
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

	// Verify SetDefault was called
	if mgr.setDefID != "sub2" {
		t.Errorf("expected SetDefault(sub2), got SetDefault(%s)", mgr.setDefID)
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
