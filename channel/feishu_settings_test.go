package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"xbot/bus"
	"xbot/storage/sqlite"
)

func newTestFeishuChannel() *FeishuChannel {
	return NewFeishuChannel(FeishuConfig{}, bus.NewMessageBus())
}

func getCardElements(card map[string]any) ([]map[string]any, bool) {
	body, ok := card["body"].(map[string]any)
	if !ok {
		return nil, false
	}
	elements, ok := body["elements"].([]map[string]any)
	return elements, ok
}

func collectInteractiveRecursive(elements []map[string]any, buttons *[]string, selects *[]string) {
	for _, elem := range elements {
		switch elem["tag"] {
		case "button":
			if value, ok := elem["value"].(map[string]string); ok {
				if ad := value["action_data"]; ad != "" {
					*buttons = append(*buttons, ad)
				}
			}
		case "select_static":
			if selects != nil {
				if value, ok := elem["value"].(map[string]string); ok {
					if ad := value["action_data"]; ad != "" {
						*selects = append(*selects, ad)
					}
				}
			}
		case "column_set":
			if columns, ok := elem["columns"].([]map[string]any); ok {
				collectInteractiveRecursive(columns, buttons, selects)
			}
		case "column", "interactive_container":
			if children, ok := elem["elements"].([]map[string]any); ok {
				collectInteractiveRecursive(children, buttons, selects)
			}
		case "form":
			if children, ok := elem["elements"].([]map[string]any); ok {
				collectInteractiveRecursive(children, buttons, selects)
			}
		}
	}
}

func collectSelectsFromCard(card map[string]any) []string {
	var buttons, selects []string
	elements, ok := getCardElements(card)
	if !ok {
		return nil
	}
	collectInteractiveRecursive(elements, &buttons, &selects)
	return selects
}

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

func cardJSON(card map[string]any) string {
	data, _ := json.Marshal(card)
	return string(data)
}

// --- Parsing helpers tests ---

func TestParseActionData(t *testing.T) {
	if r := parseActionData(`{"action":"settings_tab","tab":"model"}`); r == nil || r["action"] != "settings_tab" {
		t.Error("expected valid parse")
	}
	if parseActionData("") != nil {
		t.Error("expected nil for empty")
	}
	if parseActionData("{bad") != nil {
		t.Error("expected nil for invalid JSON")
	}
}

func TestParseActionDataFromMap(t *testing.T) {
	m := map[string]any{"action_data": `{"action":"settings_set_model"}`}
	if r := parseActionDataFromMap(m); r == nil || r["action"] != "settings_set_model" {
		t.Error("expected valid parse")
	}
	if parseActionDataFromMap(map[string]any{}) != nil {
		t.Error("expected nil for missing")
	}
}

func TestMustMapToJSON(t *testing.T) {
	result := mustMapToJSON(map[string]string{"k": "v"})
	var parsed map[string]string
	if err := json.Unmarshal([]byte(result), &parsed); err != nil || parsed["k"] != "v" {
		t.Errorf("unexpected: %s", result)
	}
}

func TestFormStr(t *testing.T) {
	data := map[string]any{"name": "  hello  ", "number": 42}
	if formStr(data, "name") != "hello" {
		t.Error("should trim spaces")
	}
	if formStr(data, "number") != "" {
		t.Error("non-string should return empty")
	}
	if formStr(data, "missing") != "" {
		t.Error("missing key should return empty")
	}
}

// --- General tab ---

func TestBuildSettingsCard_GeneralTab(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RunnerConnectCmdGet: func(senderID string) string {
			return "./xbot-runner --server ws://example.com:8080/" + senderID + " --token secret"
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "general")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card["schema"] != "2.0" {
		t.Errorf("expected schema=2.0")
	}

	if !strings.Contains(cardJSON(card), "远程 Runner") {
		t.Error("general tab should have remote runner section")
	}
	if !strings.Contains(cardJSON(card), "xbot-runner") {
		t.Error("should show runner connect command")
	}
	if !strings.Contains(cardJSON(card), "user1") {
		t.Error("should include senderID in connect command")
	}
}

func TestBuildSettingsCard_DefaultsToGeneral(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RunnerConnectCmdGet: func(senderID string) string {
			return "./xbot-runner --server ws://example.com:8080/" + senderID + " --token secret"
		},
	})

	for _, tab := range []string{"", "unknown", "basic"} {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", tab)
		if err != nil {
			t.Fatalf("tab=%q error: %v", tab, err)
		}
		if !strings.Contains(cardJSON(card), "远程 Runner") {
			t.Errorf("tab=%q should default to general tab", tab)
		}
	}
}

func TestBuildSettingsCard_ModelTab_NoCustomLLM(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return "", "", "", false
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "model")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	s := cardJSON(card)

	if !cardContainsTag(card, "form") {
		t.Error("should show setup form when no custom LLM")
	}
	if !strings.Contains(s, "配置个人模型") {
		t.Error("should show setup title")
	}
	if strings.Contains(s, "/set-llm") {
		t.Error("should NOT show command instructions")
	}

	selects := collectSelectsFromCard(card)
	for _, ad := range selects {
		if strings.Contains(ad, "settings_set_model") {
			t.Error("should NOT have model switcher without custom LLM")
		}
	}
}

func TestBuildSettingsCard_ModelTab_WithCustomLLM(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return "openai", "https://api.example.com/v1", "gpt-4", true
		},
		LLMList: func(senderID string) ([]string, string) {
			return []string{"gpt-4", "claude-3"}, "gpt-4"
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "model")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	s := cardJSON(card)

	if strings.Contains(s, "api_key") || strings.Contains(s, "sk-") {
		t.Error("API key must NEVER appear in card")
	}
	if !strings.Contains(s, "openai") {
		t.Error("should show provider")
	}
	if !strings.Contains(s, "api.example.com") {
		t.Error("should show base URL")
	}

	selects := collectSelectsFromCard(card)
	hasModel := false
	for _, ad := range selects {
		if strings.Contains(ad, "settings_set_model") {
			hasModel = true
		}
	}
	if !hasModel {
		t.Error("should have model select when custom LLM configured")
	}

	var buttons []string
	elements, _ := getCardElements(card)
	collectInteractiveRecursive(elements, &buttons, nil)
	hasDelete := false
	for _, ad := range buttons {
		if strings.Contains(ad, "settings_delete_llm") {
			hasDelete = true
		}
	}
	if !hasDelete {
		t.Error("should have delete button when custom LLM configured")
	}
}

func TestBuildSettingsCard_ModelTab_NoAPIKeyExposed(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return "openai", "https://api.openai.com/v1", "gpt-4o", true
		},
		LLMList: func(senderID string) ([]string, string) {
			return []string{"gpt-4o"}, "gpt-4o"
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "model")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	s := cardJSON(card)
	if strings.Contains(s, "api_key") || strings.Contains(s, "API Key") {
		t.Error("API key field should not appear in existing config display")
	}
}

func TestHandleSettingsAction_SetModel(t *testing.T) {
	f := newTestFeishuChannel()
	var setModel string
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMSet: func(senderID, model string) error { setModel = model; return nil },
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return "openai", "https://api.openai.com/v1", "claude-3", true
		},
		LLMList: func(senderID string) ([]string, string) { return []string{"gpt-4", "claude-3"}, "claude-3" },
	})

	actionData := map[string]any{
		"action_data":     `{"action":"settings_set_model"}`,
		"selected_option": "claude-3",
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	if setModel != "claude-3" {
		t.Errorf("expected model=claude-3, got %q", setModel)
	}
}

func TestHandleSettingsAction_SetLLM(t *testing.T) {
	f := newTestFeishuChannel()
	var gotProvider, gotURL, gotKey, gotModel string
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMSetConfig: func(senderID, provider, baseURL, apiKey, model string, maxOutputTokens int, thinkingMode string) error {
			gotProvider = provider
			gotURL = baseURL
			gotKey = apiKey
			gotModel = model
			return nil
		},
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return gotProvider, gotURL, gotModel, gotProvider != ""
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_set_llm"}`,
		"provider":    "openai",
		"base_url":    "https://api.openai.com/v1",
		"api_key":     "sk-test123",
		"model":       "gpt-4o",
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	if gotProvider != "openai" || gotURL != "https://api.openai.com/v1" || gotKey != "sk-test123" || gotModel != "gpt-4o" {
		t.Errorf("unexpected config: provider=%q url=%q key=%q model=%q", gotProvider, gotURL, gotKey, gotModel)
	}
}

func TestHandleSettingsAction_SetLLM_MissingFields(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{})

	actionData := map[string]any{
		"action_data": `{"action":"settings_set_llm"}`,
		"provider":    "openai",
	}
	_, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err == nil {
		t.Error("should fail with missing required fields")
	}
}

func TestHandleSettingsAction_DeleteLLM(t *testing.T) {
	f := newTestFeishuChannel()
	deleted := false
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMDelete: func(senderID string) error { deleted = true; return nil },
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return "", "", "", false
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_delete_llm"}`,
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	if !deleted {
		t.Error("LLMDelete should have been called")
	}
}

// --- Market tab ---

func TestBuildSettingsCard_MarketTab(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			if entryType == "skill" {
				return []sqlite.SharedEntry{{ID: 1, Type: "skill", Name: "cool-skill"}}, nil
			}
			return []sqlite.SharedEntry{{ID: 2, Type: "agent", Name: "cool-agent"}}, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			if entryType == "skill" {
				return nil, []string{"skill:my-local-skill"}, nil
			}
			if entryType == "agent" {
				return nil, []string{"agent:my-agent"}, nil
			}
			return nil, nil, nil
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	s := cardJSON(card)
	if !strings.Contains(s, "cool-skill") {
		t.Error("should contain marketplace skill")
	}
	if !strings.Contains(s, "cool-agent") {
		t.Error("should contain marketplace agent")
	}
	if !strings.Contains(s, "my-local-skill") {
		t.Error("should contain user's local skill")
	}
	if !strings.Contains(s, "my-agent") {
		t.Error("should contain user's local agent")
	}
	if !strings.Contains(s, "分享") {
		t.Error("should have share button for unpublished local items")
	}
	if !strings.Contains(s, "settings_delete_item") {
		t.Error("should have delete button for local items")
	}
}

func TestBuildSettingsCard_MarketTab_PublishedItem(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return nil, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			if entryType == "skill" {
				return []sqlite.SharedEntry{{Name: "shared-skill", Sharing: "public"}}, []string{"skill:shared-skill"}, nil
			}
			return nil, nil, nil
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	s := cardJSON(card)
	if !strings.Contains(s, "已分享") {
		t.Error("should show '已分享' for published items")
	}
	if !strings.Contains(s, "settings_unpublish") {
		t.Error("published items should have unpublish button")
	}
}

func TestBuildSettingsCard_MarketTab_Pagination(t *testing.T) {
	allSkills := make([]sqlite.SharedEntry, 12)
	for i := range allSkills {
		allSkills[i] = sqlite.SharedEntry{ID: int64(i + 1), Type: "skill", Name: fmt.Sprintf("skill-%d", i+1)}
	}
	allAgents := make([]sqlite.SharedEntry, 3)
	for i := range allAgents {
		allAgents[i] = sqlite.SharedEntry{ID: int64(100 + i), Type: "agent", Name: fmt.Sprintf("agent-%d", i+1)}
	}

	browseFn := func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
		var src []sqlite.SharedEntry
		if entryType == "skill" {
			src = allSkills
		} else {
			src = allAgents
		}
		if offset >= len(src) {
			return nil, nil
		}
		end := offset + limit
		if end > len(src) {
			end = len(src)
		}
		return src[offset:end], nil
	}

	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: browseFn,
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	t.Run("first page shows next only", func(t *testing.T) {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		s := cardJSON(card)
		if !strings.Contains(s, "skill-1") {
			t.Error("first page should contain skill-1")
		}
		if !strings.Contains(s, "skill-5") {
			t.Error("first page should contain skill-5")
		}
		if strings.Contains(s, "skill-6") {
			t.Error("first page should NOT contain skill-6")
		}
		if strings.Contains(s, "上一页") {
			t.Error("first page should NOT have prev button")
		}
		if !strings.Contains(s, "下一页") {
			t.Error("first page should have next button for skills")
		}
		if !strings.Contains(s, "第 1 页") {
			t.Error("first page should show page number")
		}
	})

	t.Run("middle page shows both prev and next", func(t *testing.T) {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market", SettingsCardOpts{
			SkillMarketPage: 1,
		})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		s := cardJSON(card)
		if !strings.Contains(s, "skill-6") {
			t.Error("page 2 should contain skill-6")
		}
		if !strings.Contains(s, "skill-10") {
			t.Error("page 2 should contain skill-10")
		}
		if strings.Contains(s, "skill-5\"") {
			t.Error("page 2 should NOT contain skill-5 install button")
		}
		if !strings.Contains(s, "上一页") {
			t.Error("middle page should have prev button")
		}
		if !strings.Contains(s, "下一页") {
			t.Error("middle page should have next button")
		}
		if !strings.Contains(s, "第 2 页") {
			t.Error("should show page 2")
		}
	})

	t.Run("last page shows prev only", func(t *testing.T) {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market", SettingsCardOpts{
			SkillMarketPage: 2,
		})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		s := cardJSON(card)
		if !strings.Contains(s, "skill-11") {
			t.Error("last page should contain skill-11")
		}
		if !strings.Contains(s, "skill-12") {
			t.Error("last page should contain skill-12")
		}
		if strings.Contains(s, "skill-10\"") {
			t.Error("last page should NOT contain skill-10 install button")
		}
		if !strings.Contains(s, "第 3 页") {
			t.Error("should show page 3")
		}
	})

	t.Run("agents section fits on one page with no pagination", func(t *testing.T) {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		s := cardJSON(card)
		if !strings.Contains(s, "agent-1") {
			t.Error("should show all agents")
		}
		if !strings.Contains(s, "agent-3") {
			t.Error("should show all agents")
		}
	})
}

func TestHandleSettingsAction_MarketPage(t *testing.T) {
	f := newTestFeishuChannel()
	skills := make([]sqlite.SharedEntry, 8)
	for i := range skills {
		skills[i] = sqlite.SharedEntry{ID: int64(i + 1), Type: "skill", Name: fmt.Sprintf("skill-%d", i+1)}
	}
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			if entryType == "skill" {
				if offset >= len(skills) {
					return nil, nil
				}
				end := offset + limit
				if end > len(skills) {
					end = len(skills)
				}
				return skills[offset:end], nil
			}
			return nil, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_market_page","skill_page":"1","agent_page":"0"}`,
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	s := cardJSON(card)
	if !strings.Contains(s, "skill-6") {
		t.Error("page 2 should contain skill-6")
	}
	if strings.Contains(s, "skill-5\"") {
		t.Error("page 2 should NOT contain skill-5 install button")
	}
}

func TestBuildSettingsCard_MyItemsPagination(t *testing.T) {
	localSkills := make([]string, 8)
	for i := range localSkills {
		localSkills[i] = fmt.Sprintf("skill:my-skill-%d", i+1)
	}

	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return nil, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			if entryType == "skill" {
				return nil, localSkills, nil
			}
			return nil, nil, nil
		},
	})

	t.Run("first page shows first 5 items with next", func(t *testing.T) {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		s := cardJSON(card)
		if !strings.Contains(s, "my-skill-1") {
			t.Error("first page should contain my-skill-1")
		}
		if !strings.Contains(s, "my-skill-5") {
			t.Error("first page should contain my-skill-5")
		}
		if strings.Contains(s, "my-skill-6") {
			t.Error("first page should NOT contain my-skill-6")
		}
		if !strings.Contains(s, "下一页") {
			t.Error("should have next button")
		}
	})

	t.Run("second page shows remaining items with prev", func(t *testing.T) {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market", SettingsCardOpts{
			MySkillPage: 1,
		})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		s := cardJSON(card)
		if !strings.Contains(s, "my-skill-6") {
			t.Error("second page should contain my-skill-6")
		}
		if !strings.Contains(s, "my-skill-8") {
			t.Error("second page should contain my-skill-8")
		}
		if strings.Contains(s, "my-skill-5\"") {
			t.Error("second page should NOT contain my-skill-5 buttons")
		}
		if !strings.Contains(s, "上一页") {
			t.Error("should have prev button")
		}
	})

	t.Run("pagination preserves page state across sections", func(t *testing.T) {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "market", SettingsCardOpts{
			MySkillPage: 1,
		})
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		s := cardJSON(card)
		if !strings.Contains(s, "my_skill_page") {
			t.Error("pagination buttons should carry my_skill_page state")
		}
	})

	t.Run("few items no pagination", func(t *testing.T) {
		f2 := newTestFeishuChannel()
		f2.SetSettingsCallbacks(SettingsCallbacks{
			RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
				return nil, nil
			},
			RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
				if entryType == "skill" {
					return nil, []string{"skill:only-one"}, nil
				}
				return nil, nil, nil
			},
		})
		card, err := f2.BuildSettingsCard(context.Background(), "user1", "chat1", "market")
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		s := cardJSON(card)
		if !strings.Contains(s, "only-one") {
			t.Error("should show the single item")
		}
		if strings.Contains(s, "上一页") || strings.Contains(s, "下一页") {
			t.Error("single item should have no pagination")
		}
	})
}

func TestHandleSettingsAction_MyItemsPage(t *testing.T) {
	localSkills := make([]string, 8)
	for i := range localSkills {
		localSkills[i] = fmt.Sprintf("skill:my-skill-%d", i+1)
	}

	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return nil, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			if entryType == "skill" {
				return nil, localSkills, nil
			}
			return nil, nil, nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_market_page","my_skill_page":"1","my_agent_page":"0","skill_page":"0","agent_page":"0"}`,
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	s := cardJSON(card)
	if !strings.Contains(s, "my-skill-6") {
		t.Error("page 2 should contain my-skill-6")
	}
	if strings.Contains(s, "my-skill-5\"") {
		t.Error("page 2 should NOT contain my-skill-5 buttons")
	}
}

func TestHandleSettingsAction_PreservesPageState(t *testing.T) {
	allSkills := make([]sqlite.SharedEntry, 12)
	for i := range allSkills {
		allSkills[i] = sqlite.SharedEntry{ID: int64(i + 1), Type: "skill", Name: fmt.Sprintf("skill-%d", i+1)}
	}

	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			if entryType == "skill" {
				if offset >= len(allSkills) {
					return nil, nil
				}
				end := offset + limit
				if end > len(allSkills) {
					end = len(allSkills)
				}
				return allSkills[offset:end], nil
			}
			return nil, nil
		},
		RegistryInstall:   func(entryType string, id int64, senderID string) error { return nil },
		RegistryPublish:   func(entryType, name, senderID string) error { return nil },
		RegistryUnpublish: func(entryType, name, senderID string) error { return nil },
		RegistryDelete:    func(entryType, name, senderID string) error { return nil },
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	pageFields := `"my_skill_page":"0","my_agent_page":"0","skill_page":"1","agent_page":"0"`

	tests := []struct {
		name   string
		action string
	}{
		{"install preserves page", `{"action":"settings_install","entry_type":"skill","entry_id":"6",` + pageFields + `}`},
		{"publish preserves page", `{"action":"settings_publish","entry_type":"skill","name":"foo",` + pageFields + `}`},
		{"unpublish preserves page", `{"action":"settings_unpublish","entry_type":"skill","name":"foo",` + pageFields + `}`},
		{"delete preserves page", `{"action":"settings_delete_item","entry_type":"skill","name":"foo",` + pageFields + `}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			card, err := f.HandleSettingsAction(context.Background(), map[string]any{
				"action_data": tt.action,
			}, "user1", "chat1", "msg1")
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			s := cardJSON(card)
			if !strings.Contains(s, "skill-6") {
				t.Error("should be on skill market page 2 (showing skill-6)")
			}
			if strings.Contains(s, `"📥 skill-5"`) {
				t.Error("should NOT show skill-5 (that's page 1)")
			}
		})
	}
}

func TestHandleSettingsAction_Install(t *testing.T) {
	f := newTestFeishuChannel()
	var installedType string
	var installedID int64
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return nil, nil
		},
		RegistryInstall: func(entryType string, id int64, senderID string) error {
			installedType = entryType
			installedID = id
			return nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_install","entry_type":"skill","entry_id":"42"}`,
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	if installedType != "skill" || installedID != 42 {
		t.Errorf("expected skill/42, got %s/%d", installedType, installedID)
	}
}

func TestHandleSettingsAction_Publish(t *testing.T) {
	f := newTestFeishuChannel()
	var pubType, pubName string
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryPublish: func(entryType, name, senderID string) error {
			pubType = entryType
			pubName = name
			return nil
		},
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return nil, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_publish","entry_type":"skill","name":"my-skill"}`,
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	if pubType != "skill" || pubName != "my-skill" {
		t.Errorf("expected skill/my-skill, got %s/%s", pubType, pubName)
	}
}

func TestHandleSettingsAction_Unpublish(t *testing.T) {
	f := newTestFeishuChannel()
	var unpubType, unpubName string
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryUnpublish: func(entryType, name, senderID string) error {
			unpubType = entryType
			unpubName = name
			return nil
		},
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return nil, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_unpublish","entry_type":"skill","name":"my-skill"}`,
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	if unpubType != "skill" || unpubName != "my-skill" {
		t.Errorf("expected skill/my-skill, got %s/%s", unpubType, unpubName)
	}
}

func TestHandleSettingsAction_DeleteItem(t *testing.T) {
	f := newTestFeishuChannel()
	var delType, delName string
	f.SetSettingsCallbacks(SettingsCallbacks{
		RegistryDelete: func(entryType, name, senderID string) error {
			delType = entryType
			delName = name
			return nil
		},
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return nil, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	actionData := map[string]any{
		"action_data": `{"action":"settings_delete_item","entry_type":"agent","name":"old-agent"}`,
	}
	card, err := f.HandleSettingsAction(context.Background(), actionData, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected card")
	}
	if delType != "agent" || delName != "old-agent" {
		t.Errorf("expected agent/old-agent, got %s/%s", delType, delName)
	}
}

// --- Error cases ---

func TestHandleSettingsAction_UnknownAction(t *testing.T) {
	f := newTestFeishuChannel()
	_, err := f.HandleSettingsAction(context.Background(), map[string]any{
		"action_data": `{"action":"unknown"}`,
	}, "user1", "chat1", "msg1")
	if err == nil {
		t.Error("expected error")
	}
}

func TestHandleSettingsAction_MissingActionData(t *testing.T) {
	f := newTestFeishuChannel()
	_, err := f.HandleSettingsAction(context.Background(), map[string]any{}, "u", "c", "m")
	if err == nil {
		t.Error("expected error")
	}
}

// --- V2 compatibility ---

func TestSettingsCard_NoUnsupportedV2Tags(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		ContextModeGet: func() string { return "phase1" },
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return "openai", "https://api.openai.com/v1", "gpt-4", true
		},
		LLMList: func(senderID string) ([]string, string) { return []string{"gpt-4"}, "gpt-4" },
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return []sqlite.SharedEntry{{ID: 1, Name: "test"}}, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	for _, tab := range []string{"general", "model", "market"} {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", tab)
		if err != nil {
			t.Fatalf("tab %s: %v", tab, err)
		}
		if cardContainsTag(card, "note") {
			t.Errorf("tab %s: 'note' tag not supported in V2", tab)
		}
		if cardContainsTag(card, "action") {
			t.Errorf("tab %s: 'action' tag not supported in V2", tab)
		}
	}
}

func TestSettingsCard_NoCommandReferences(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		ContextModeGet: func() string { return "phase1" },
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return "", "", "", false
		},
		RegistryBrowse: func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error) {
			return nil, nil
		},
		RegistryListMy: func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error) {
			return nil, nil, nil
		},
	})

	for _, tab := range []string{"general", "model", "market"} {
		card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", tab)
		if err != nil {
			t.Fatalf("tab %s: %v", tab, err)
		}
		s := cardJSON(card)
		for _, cmd := range []string{"/set-llm", "/unset-llm", "/llm", "/browse", "/install", "/my skills", "/publish"} {
			if strings.Contains(s, cmd) {
				t.Errorf("tab %s: should not reference command %q", tab, cmd)
			}
		}
	}
}

func TestBuildSettingsCard_NilCallbacks(t *testing.T) {
	f := newTestFeishuChannel()
	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "general")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil || card["schema"] != "2.0" {
		t.Error("should produce valid card even without callbacks")
	}
}

// --- Concurrency settings ---

func TestHandleSettingsAction_SetConcurrency(t *testing.T) {
	f := newTestFeishuChannel()
	var gotPersonal int
	var gotSenderID string
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMSetPersonalConcurrency: func(senderID string, personal int) error {
			gotSenderID = senderID
			gotPersonal = personal
			return nil
		},
	})

	card, err := f.HandleSettingsAction(context.Background(), map[string]any{
		"action_data":     `{"action":"settings_set_concurrency"}`,
		"selected_option": "5",
	}, "user1", "chat1", "msg1")
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if card == nil {
		t.Fatal("expected non-nil card")
	}
	if gotSenderID != "user1" {
		t.Errorf("expected senderID=user1, got %q", gotSenderID)
	}
	if gotPersonal != 5 {
		t.Errorf("expected personal=5, got %d", gotPersonal)
	}
}

func TestHandleSettingsAction_SetConcurrency_Error(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{})

	// Missing conc and selected_option
	_, err := f.HandleSettingsAction(context.Background(), map[string]any{
		"action_data": `{"action":"settings_set_concurrency"}`,
	}, "user1", "chat1", "msg1")
	if err == nil {
		t.Error("expected error for missing conc value")
	}
}

func TestBuildSettingsCard_ModelTab_WithConcurrency(t *testing.T) {
	f := newTestFeishuChannel()
	f.SetSettingsCallbacks(SettingsCallbacks{
		LLMGetConfig: func(senderID string) (string, string, string, bool) {
			return "openai", "https://api.example.com/v1", "gpt-4", true
		},
		LLMList: func(senderID string) ([]string, string) {
			return []string{"gpt-4", "gpt-4o"}, "gpt-4"
		},
		LLMGetPersonalConcurrency: func(senderID string) int {
			return 5
		},
	})

	card, err := f.BuildSettingsCard(context.Background(), "user1", "chat1", "model")
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	s := cardJSON(card)
	if !strings.Contains(s, "个人 LLM 并发限制") {
		t.Error("model tab should contain personal concurrency section header")
	}
	if !strings.Contains(s, "并发上限") {
		t.Error("model tab should contain concurrency label")
	}

	// Verify concurrency select dropdown is present
	selects := collectSelectsFromCard(card)
	hasConc := false
	for _, ad := range selects {
		if strings.Contains(ad, "settings_set_concurrency") {
			hasConc = true
			break
		}
	}
	if !hasConc {
		t.Error("model tab should have concurrency select dropdown")
	}
}
