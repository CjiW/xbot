package channel

import (
	"testing"
)

func TestBuildTextSettingsUI(t *testing.T) {
	schema := []SettingDefinition{
		{
			Key:          "reply_style",
			Label:        "回复风格",
			Description:  "控制机器人的回复风格",
			Type:         SettingTypeSelect,
			Category:     "对话",
			DefaultValue: "detailed",
			Options: []SettingOption{
				{Label: "简洁", Value: "concise"},
				{Label: "详细", Value: "detailed"},
			},
		},
		{
			Key:      "language",
			Label:    "语言",
			Type:     SettingTypeSelect,
			Category: "对话",
			Options:  []SettingOption{{Label: "中文", Value: "zh"}},
		},
		{
			Key:          "notify",
			Label:        "通知",
			Type:         SettingTypeToggle,
			Category:     "通知",
			DefaultValue: "true",
		},
	}

	// With no current values
	ui := BuildTextSettingsUI(schema, nil)
	if ui == "" {
		t.Fatal("expected non-empty UI output")
	}
	if !contains(ui, "回复风格") {
		t.Error("expected label in output")
	}
	if !contains(ui, "选项") {
		t.Error("expected options prefix in output")
	}

	// With current values overriding defaults
	currentValues := map[string]string{
		"reply_style": "concise",
	}
	ui = BuildTextSettingsUI(schema, currentValues)
	if !contains(ui, "`concise`") {
		t.Error("expected current value 'concise' in output")
	}
}

func TestBuildTextSettingsUIEmpty(t *testing.T) {
	ui := BuildTextSettingsUI(nil, nil)
	if ui != "没有可配置的设置项。" {
		t.Errorf("expected empty message, got %q", ui)
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) && search(s, substr)
}

func search(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
