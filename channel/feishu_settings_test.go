package channel

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"xbot/bus"
	"xbot/storage/sqlite"
)

// newTestFeishuChannel creates a FeishuChannel with minimal config for testing.
func newTestFeishuChannel() *FeishuChannel {
	return NewFeishuChannel(FeishuConfig{}, bus.NewMessageBus())
}

// helper to extract elements slice from a card body
func getCardElements(card map[string]any) ([]map[string]any, bool) {
	body, ok := card["body"].(map[string]any)
	if !ok {
		return nil, false
	}
	elements, ok := body["elements"].([]map[string]any)
	return elements, ok
}

// helper to find action values from card elements (supports V2 column_set nesting)
func collectActionDataFromCard(card map[string]any) []string {
	var results []string
	elements, ok := getCardElements(card)
	if !ok {
		return results
	}
	collectInteractiveRecursive(elements, &results, nil)
	return results
}

// collectInteractiveRecursive searches for buttons and selects at any nesting depth.
func collectInteractiveRecursive(elements []map[string]any, buttonResults *[]string, selectResults *[]string) {
	for _, elem := range elements {
		switch elem["tag"] {
		case "button":
			if value, ok := elem["value"].(map[string]string); ok {
				if ad := value["action_data"]; ad != "" {
					*buttonResults = append(*buttonResults, ad)
				}
			}
		case "select_static":
			if selectResults != nil {
				if value, ok := elem["value"].(map[string]string); ok {
					if ad := value["action_data"]; ad != "" {
						*selectResults = append(*selectResults, ad)
					}
				}
			}
		case "column_set":
			if columns, ok := elem["columns"].([]map[string]any); ok {
				collectInteractiveRecursive(columns, buttonResults, selectResults)
			}
		case "column", "interactive_container", "form", "collapsible_panel":
			if children, ok := elem["elements"].([]map[string]any); ok {
				collectInteractiveRecursive(children, buttonResults, selectResults)
			}
		}
	}
}

// collectSelectsFromCard collects action_data from select_static elements.
func collectSelectsFromCard(card map[string]any) []string {
	var buttons, selects []string
	elements, ok := getCardElements(card)
	if !ok {
		return selects
	}
	collectInteractiveRecursive(elements, &buttons, &selects)
	return selects
}

// cardContainsTag checks if a card contains any element with the given tag (recursively).
func cardContainsTag(card map[string]any, tag string) bool {
	elements, ok := getCardElements(card)
	if !ok {
		return false
	}
	return containsTagRecursive(elements, tag)
}

func containsTagRecursive(elements []map[string]any, tag string) bool {
	for _, elem := range elements {
		if elem["tag"] == tag {
			return true
		}
		if columns, ok := elem["columns"].([]map[string]any); ok {
			if containsTagRecursive(columns, tag) {
				return true
			}
		}
		if children, ok := elem["elements"].([]map[string]any); ok {
			if containsTagRecursive(children, tag) {
				return true
			}
		}
	}
	return false
}

// --- parseActionData tests ---

func TestParseActionData(t *testing.T) {
	t.Run("valid JSON", func(t *testing.T) {
		result := parseActionData(`{"action":"settings_tab","tab":"model"}`)
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if result["action"] != "settings_tab" {
			t.Errorf("expected action=settings_tab, got %q", result["action"])
		}
		if result["tab"] != "model" {
			t.Errorf("expected tab=model, got %q", result["tab"])
		}
	})

	t.Run("empty string returns nil", func(t *testing.T) {
		result := parseActionData("")
		if result != nil {
			t.Errorf("expected nil for empty string, got %v", result)
		}
	})

	t.Run("whitespace string returns nil", func(t *testing.T) {
		result := parseActionData("   ")
		if result != nil {
			t.Errorf("expected nil for whitespace string, got %v", result)
		}
	})

	t.Run("invalid JSON returns nil", func(t *testing.T) {
		result := parseActionData("{not valid json")
		if result != nil {
			t.Errorf("expected nil for invalid JSON, got %v", result)
		}
	})

	t.Run("JSON array returns nil", func(t *testing.T) {
		result := parseActionData(`[1,2,3]`)
		if result != nil {
			t.Errorf("expected nil for JSON array, got %v", result)
		}
	})
}

// --- parseActionDataFromMap tests ---

func TestParseActionDataFromMap(t *testing.T) {
	t.Run("valid action_data string in map", func(t *testing.T) {
		m := map[string]any{
			"action_data": `{"action":"settings_set_model","model":"gpt-4"}`,
		}
		result := parseActionDataFromMap(m)
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if result["action"] != "settings_set_model" {
			t.Errorf("expected action=settings_set_model, got %q", result["action"])
		}
		if result["model"] != "gpt-4" {
			t.Errorf("expected model=gpt-4, got %q", result["model"])
		}
	})

	t.Run("action_data is not string returns nil", func(t *testing.T) {
		m := map[string]any{
			"action_data": 12345,
		}
		result := parseActionDataFromMap(m)
		if result != nil {
			t.Errorf("expected nil when action_data is not string, got %v", result)
		}
	})

	t.Run("missing action_data field returns nil", func(t *testing.T) {
		m := map[string]any{
			"other_key": "some_value",
		}
		result := parseActionDataFromMap(m)
		if result != nil {
			t.Errorf("expected nil when action_data is missing, got %v", result)
		}
	})

	t.Run("empty action_data returns nil", func(t *testing.T) {
		m := map[string]any{
			"action_data": "",
		}
		result := parseActionDataFromMap(m)
		if result != nil {
			t.Errorf("expected nil for empty action_data, got %v", result)
		}
	})
}

// --- mustMapToJSON tests ---

func TestMustMapToJSON(t *testing.T) {
	result := mustMapToJSON(map[string]string{"key": "value", "action": "test"})
	var parsed map[string]string
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("expected valid JSON, got error: %v", err)
	}
	if parsed["key"] != "value" {
		t.Errorf("expected key=value, got %q", parsed["key"])
	}
	if parsed["action"] != "test" {
		t.Errorf("expected action=test, got %q", parsed["action"])
	}
}

// --- BuildSettingsCard tests ---

func TestBuildSettingsCard_BasicTab(t *testing.T) {
	f := newTestFeishuChannel()

	settingsGetCalled := false
	f.SetSettingsCallbacks(SettingsCallbacks{
		SettingsGet: func(channelName, senderID string) (map[string]string, error) {
			settingsGetCalled = true
			return map[string]string{
				"context_mode":       "phase2",
				"notify_on_complete": "true",
			}, nil
		},
	})

	ctx := context.Background()
	card, err := f.BuildSettingsCard(ctx, "user1", "chat1", "basic")
	if err != nil {
		t.Fatalf("BuildSettingsCard returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}

	if card["schema"] != "2.0" {
		t.Errorf("expected schema=2.0, got %v", card["schema"])
	}

	header, ok := card["header"].(map[string]any)
	if !ok {
		t.Fatal("expected header to be a map")
	}
	titleMap, ok := header["title"].(map[string]any)
	if !ok {
		t.Fatal("expected header.title to be a map")
	}
	if titleMap["content"] != "⚙️ 设置" {
		t.Errorf("expected header title '⚙️ 设置', got %v", titleMap["content"])
	}

	// Verify select_static elements for select-type settings
	selects := collectSelectsFromCard(card)
	hasSettingsSelect := false
	for _, ad := range selects {
		if strings.Contains(ad, "settings_set") {
			hasSettingsSelect = true
		}
	}
	if !hasSettingsSelect {
		t.Error("expected select_static element with 'settings_set' action for select-type settings")
	}

	// Verify tab buttons exist
	actionDataList := collectActionDataFromCard(card)
	hasTabButton := false
	for _, ad := range actionDataList {
		if strings.Contains(ad, "settings_tab") {
			hasTabButton = true
		}
	}
	if !hasTabButton {
		t.Error("expected tab button with 'settings_tab' action")
	}

	if !settingsGetCalled {
		t.Error("expected SettingsGet callback to be called")
	}

	// Ensure no V2-unsupported tags
	if cardContainsTag(card, "note") {
		t.Error("card should not contain 'note' tag (unsupported in V2)")
	}
	if cardContainsTag(card, "action") {
		t.Error("card should not contain 'action' tag (unsupported in V2)")
	}
}

func TestBuildSettingsCard_ModelTab(t *testing.T) {
	f := newTestFeishuChannel()

	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMList: func(senderID string) ([]string, string) {
			return []string{"gpt-4", "gpt-3.5-turbo", "claude-3"}, "gpt-4"
		},
	})

	ctx := context.Background()
	card, err := f.BuildSettingsCard(ctx, "user1", "chat1", "model")
	if err != nil {
		t.Fatalf("BuildSettingsCard returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}

	cardJSON, _ := json.Marshal(card)
	cardStr := string(cardJSON)
	if !strings.Contains(cardStr, "gpt-4") {
		t.Error("expected card to contain current model 'gpt-4'")
	}

	// Model tab should have a select_static for model selection
	selects := collectSelectsFromCard(card)
	hasSetModelSelect := false
	for _, ad := range selects {
		if strings.Contains(ad, "settings_set_model") {
			hasSetModelSelect = true
		}
	}
	if !hasSetModelSelect {
		t.Error("expected select_static with 'settings_set_model' action in model tab")
	}

	// Verify markdown showing current model
	elements, _ := getCardElements(card)
	hasModelMarkdown := false
	for _, elem := range elements {
		if elem["tag"] == "markdown" {
			content, _ := elem["content"].(string)
			if strings.Contains(content, "gpt-4") {
				hasModelMarkdown = true
			}
		}
	}
	if !hasModelMarkdown {
		t.Error("expected markdown element showing current model")
	}

	// Must NOT contain 'note' tag
	if cardContainsTag(card, "note") {
		t.Error("model tab should not contain 'note' tag (unsupported in V2)")
	}
}

// --- HandleSettingsAction tests ---

func TestHandleSettingsAction_TabSwitch(t *testing.T) {
	f := newTestFeishuChannel()

	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMList: func(senderID string) ([]string, string) {
			return []string{"gpt-4", "claude-3"}, "gpt-4"
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_tab","tab":"model"}`,
	}

	ctx := context.Background()
	card, err := f.HandleSettingsAction(ctx, actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("HandleSettingsAction returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}

	cardJSON, _ := json.Marshal(card)
	cardStr := string(cardJSON)
	if !strings.Contains(cardStr, "gpt-4") {
		t.Error("expected switched card to contain model info")
	}
}

func TestHandleSettingsAction_SetValue(t *testing.T) {
	f := newTestFeishuChannel()

	var setKey, setValue string
	settingsSetCalled := false

	f.SetSettingsCallbacks(SettingsCallbacks{
		SettingsSet: func(channelName, senderID, key, value string) error {
			settingsSetCalled = true
			setKey = key
			setValue = value
			return nil
		},
		SettingsGet: func(channelName, senderID string) (map[string]string, error) {
			return map[string]string{
				"context_mode": "phase1",
			}, nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_set","key":"context_mode","value":"phase2"}`,
	}

	ctx := context.Background()
	card, err := f.HandleSettingsAction(ctx, actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("HandleSettingsAction returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card after settings_set action")
	}

	if !settingsSetCalled {
		t.Error("expected SettingsSet callback to be called")
	}
	if setKey != "context_mode" {
		t.Errorf("expected key=context_mode, got %q", setKey)
	}
	if setValue != "phase2" {
		t.Errorf("expected value=phase2, got %q", setValue)
	}

	if card["schema"] != "2.0" {
		t.Errorf("expected returned card schema=2.0, got %v", card["schema"])
	}
}

func TestHandleSettingsAction_SetValueFromSelect(t *testing.T) {
	f := newTestFeishuChannel()

	var setKey, setValue string
	f.SetSettingsCallbacks(SettingsCallbacks{
		SettingsSet: func(channelName, senderID, key, value string) error {
			setKey = key
			setValue = value
			return nil
		},
		SettingsGet: func(channelName, senderID string) (map[string]string, error) {
			return map[string]string{}, nil
		},
	})

	// Simulate select_static callback: action_data has key but no value,
	// selected_option is injected by onCardAction from action.Option
	actionData := map[string]any{
		"action_data":     `{"action":"settings_set","key":"context_mode"}`,
		"selected_option": "phase2",
	}

	ctx := context.Background()
	card, err := f.HandleSettingsAction(ctx, actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("HandleSettingsAction returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}
	if setKey != "context_mode" {
		t.Errorf("expected key=context_mode, got %q", setKey)
	}
	if setValue != "phase2" {
		t.Errorf("expected value=phase2 from selected_option, got %q", setValue)
	}
}

func TestHandleSettingsAction_SetModelFromSelect(t *testing.T) {
	f := newTestFeishuChannel()

	var setModel string
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMSet: func(senderID, model string) error {
			setModel = model
			return nil
		},
		LLMList: func(senderID string) ([]string, string) {
			return []string{"gpt-4", "claude-3"}, "gpt-4"
		},
	})

	// Simulate select_static callback for model selection
	actionData := map[string]any{
		"action_data":     `{"action":"settings_set_model"}`,
		"selected_option": "claude-3",
	}

	ctx := context.Background()
	card, err := f.HandleSettingsAction(ctx, actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("HandleSettingsAction returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}
	if setModel != "claude-3" {
		t.Errorf("expected model=claude-3, got %q", setModel)
	}
}

func TestHandleSettingsAction_SetModel(t *testing.T) {
	f := newTestFeishuChannel()

	var setModel string
	llmSetCalled := false

	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMSet: func(senderID, model string) error {
			llmSetCalled = true
			setModel = model
			return nil
		},
		LLMList: func(senderID string) ([]string, string) {
			return []string{"gpt-4", "claude-3"}, "gpt-4"
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_set_model","model":"claude-3"}`,
	}

	ctx := context.Background()
	card, err := f.HandleSettingsAction(ctx, actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("HandleSettingsAction returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card after settings_set_model action")
	}

	if !llmSetCalled {
		t.Error("expected LLMSet callback to be called")
	}
	if setModel != "claude-3" {
		t.Errorf("expected model=claude-3, got %q", setModel)
	}
}

func TestHandleSettingsAction_Install(t *testing.T) {
	f := newTestFeishuChannel()

	var installedType string
	var installedID int64
	var installedSender string
	installCalled := false

	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return []sqlite.SharedEntry{
				{ID: 42, Type: "skill", Name: "test-skill"},
			}, nil
		},
		RegistryInstall: func(entryType string, id int64, senderID string) error {
			installCalled = true
			installedType = entryType
			installedID = id
			installedSender = senderID
			return nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_install","entry_type":"skill","entry_id":"42"}`,
	}

	ctx := context.Background()
	card, err := f.HandleSettingsAction(ctx, actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("HandleSettingsAction returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card after settings_install action")
	}

	if !installCalled {
		t.Error("expected RegistryInstall callback to be called")
	}
	if installedType != "skill" {
		t.Errorf("expected entry_type=skill, got %q", installedType)
	}
	if installedID != 42 {
		t.Errorf("expected entry_id=42, got %d", installedID)
	}
	if installedSender != "user1" {
		t.Errorf("expected senderID=user1, got %q", installedSender)
	}
}

func TestHandleSettingsAction_UnknownAction(t *testing.T) {
	f := newTestFeishuChannel()

	actionData := map[string]any{
		"action_data": `{"action":"unknown_action"}`,
	}

	ctx := context.Background()
	card, err := f.HandleSettingsAction(ctx, actionData, "user1", "chat1", "msg1")
	if err == nil {
		t.Error("expected error for unknown action")
	}
	if card != nil {
		t.Error("expected nil card for unknown action")
	}
}

func TestHandleSettingsAction_MissingActionData(t *testing.T) {
	f := newTestFeishuChannel()

	actionData := map[string]any{}

	ctx := context.Background()
	card, err := f.HandleSettingsAction(ctx, actionData, "user1", "chat1", "msg1")
	if err == nil {
		t.Error("expected error for missing action_data")
	}
	if card != nil {
		t.Error("expected nil card for missing action_data")
	}
}

func TestBuildSettingsCard_MarketTab(t *testing.T) {
	f := newTestFeishuChannel()

	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return []sqlite.SharedEntry{
				{ID: 1, Type: "skill", Name: "my-skill"},
				{ID: 2, Type: "agent", Name: "my-agent"},
			}, nil
		},
	})

	ctx := context.Background()
	card, err := f.BuildSettingsCard(ctx, "user1", "chat1", "market")
	if err != nil {
		t.Fatalf("BuildSettingsCard returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}

	cardJSON, _ := json.Marshal(card)
	cardStr := string(cardJSON)

	if !strings.Contains(cardStr, "my-skill") {
		t.Error("expected market tab to contain skill entry 'my-skill'")
	}
	if !strings.Contains(cardStr, "my-agent") {
		t.Error("expected market tab to contain agent entry 'my-agent'")
	}

	// Must NOT contain 'note' tag
	if cardContainsTag(card, "note") {
		t.Error("market tab should not contain 'note' tag (unsupported in V2)")
	}
}

func TestBuildSettingsCard_DefaultTab(t *testing.T) {
	f := newTestFeishuChannel()

	f.SetSettingsCallbacks(SettingsCallbacks{
		SettingsGet: func(channelName, senderID string) (map[string]string, error) {
			return map[string]string{}, nil
		},
	})

	ctx := context.Background()
	card, err := f.BuildSettingsCard(ctx, "user1", "chat1", "")
	if err != nil {
		t.Fatalf("BuildSettingsCard returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}

	header, ok := card["header"].(map[string]any)
	if !ok {
		t.Fatal("expected header to be a map")
	}
	titleMap, ok := header["title"].(map[string]any)
	if !ok {
		t.Fatal("expected header.title to be a map")
	}
	if titleMap["content"] != "⚙️ 设置" {
		t.Errorf("expected header title '⚙️ 设置', got %v", titleMap["content"])
	}
}

func TestBuildSettingsCard_NilCallbacks(t *testing.T) {
	f := newTestFeishuChannel()

	ctx := context.Background()
	card, err := f.BuildSettingsCard(ctx, "user1", "chat1", "basic")
	if err != nil {
		t.Fatalf("BuildSettingsCard returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card even without callbacks")
	}
	if card["schema"] != "2.0" {
		t.Errorf("expected schema=2.0, got %v", card["schema"])
	}
}

func TestBuildSettingsCard_NoReplyStyle(t *testing.T) {
	f := newTestFeishuChannel()

	f.SetSettingsCallbacks(SettingsCallbacks{
		SettingsGet: func(channelName, senderID string) (map[string]string, error) {
			return map[string]string{}, nil
		},
	})

	ctx := context.Background()
	card, err := f.BuildSettingsCard(ctx, "user1", "chat1", "basic")
	if err != nil {
		t.Fatalf("BuildSettingsCard returned error: %v", err)
	}

	cardJSON, _ := json.Marshal(card)
	cardStr := string(cardJSON)
	if strings.Contains(cardStr, "reply_style") || strings.Contains(cardStr, "回复风格") {
		t.Error("card should not contain removed reply_style setting")
	}
}
