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

// helper to find action values from card elements (supports V1 "action" and V2 "column_set" nesting)
func collectActionDataFromCard(card map[string]any) []string {
	var results []string
	elements, ok := getCardElements(card)
	if !ok {
		return results
	}
	collectButtonsRecursive(elements, &results)
	return results
}

// collectButtonsRecursive searches for buttons at any nesting depth within card elements.
// Handles both V1 (action > button) and V2 (column_set > column > interactive_container > button).
func collectButtonsRecursive(elements []map[string]any, results *[]string) {
	for _, elem := range elements {
		switch elem["tag"] {
		case "action":
			// V1: action > button[]
			if actions, ok := elem["actions"].([]map[string]any); ok {
				collectButtonsRecursive(actions, results)
			}
		case "button":
			if value, ok := elem["value"].(map[string]string); ok {
				if ad := value["action_data"]; ad != "" {
					*results = append(*results, ad)
				}
			}
		case "column_set":
			if columns, ok := elem["columns"].([]map[string]any); ok {
				collectButtonsRecursive(columns, results)
			}
		case "column", "interactive_container", "form", "collapsible_panel":
			if children, ok := elem["elements"].([]map[string]any); ok {
				collectButtonsRecursive(children, results)
			}
		}
	}
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
				"reply_style":        "concise",
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

	// Verify card structure
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

	elements, ok := getCardElements(card)
	if !ok {
		t.Fatal("expected body.elements to be a []map[string]any")
	}

	// Verify that there are markdown elements and button elements
	hasMarkdown := false
	actionDataList := collectActionDataFromCard(card)
	hasButton := len(actionDataList) > 0
	hasSettingsAction := false
	for _, elem := range elements {
		if elem["tag"] == "markdown" {
			hasMarkdown = true
		}
	}
	for _, ad := range actionDataList {
		if strings.Contains(ad, "settings_") {
			hasSettingsAction = true
		}
	}

	if !hasMarkdown {
		t.Error("expected at least one markdown element in card")
	}
	if !hasButton {
		t.Error("expected at least one button element in card")
	}
	if !hasSettingsAction {
		t.Error("expected button value to contain 'settings_' action prefix")
	}
	if !settingsGetCalled {
		t.Error("expected SettingsGet callback to be called")
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

	elements, ok := getCardElements(card)
	if !ok {
		t.Fatal("expected body.elements to be a []map[string]any")
	}

	// Verify current model info is in card
	cardJSON, _ := json.Marshal(card)
	cardStr := string(cardJSON)
	if !strings.Contains(cardStr, "gpt-4") {
		t.Error("expected card to contain current model 'gpt-4'")
	}

	// Verify model selection buttons contain settings_set_model action
	actionDataList := collectActionDataFromCard(card)
	hasSetModelAction := false
	for _, ad := range actionDataList {
		if strings.Contains(ad, "settings_set_model") {
			hasSetModelAction = true
		}
	}
	if !hasSetModelAction {
		t.Error("expected model tab buttons to contain 'settings_set_model' action")
	}

	// Verify there are markdown elements for model display
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

	// Verify the returned card is for the model tab (contains model info)
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
				"reply_style": "concise",
			}, nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_set","key":"reply_style","value":"detailed"}`,
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
	if setKey != "reply_style" {
		t.Errorf("expected key=reply_style, got %q", setKey)
	}
	if setValue != "detailed" {
		t.Errorf("expected value=detailed, got %q", setValue)
	}

	// Verify returned card is valid (basic tab after set)
	if card["schema"] != "2.0" {
		t.Errorf("expected returned card schema=2.0, got %v", card["schema"])
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
}

func TestBuildSettingsCard_DefaultTab(t *testing.T) {
	f := newTestFeishuChannel()

	f.SetSettingsCallbacks(SettingsCallbacks{
		SettingsGet: func(channelName, senderID string) (map[string]string, error) {
			return map[string]string{}, nil
		},
	})

	// Pass empty tab — should default to "basic"
	ctx := context.Background()
	card, err := f.BuildSettingsCard(ctx, "user1", "chat1", "")
	if err != nil {
		t.Fatalf("BuildSettingsCard returned error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}

	// Basic tab should have "设置面板" title
	cardJSON, _ := json.Marshal(card)
	cardStr := string(cardJSON)
	if !strings.Contains(cardStr, "设置面板") {
		t.Error("expected default tab card to contain '设置面板'")
	}
}

func TestBuildSettingsCard_NilCallbacks(t *testing.T) {
	f := newTestFeishuChannel()
	// No callbacks set — should not panic

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
