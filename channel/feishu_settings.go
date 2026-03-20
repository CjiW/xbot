package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	log "xbot/logger"
)

// settingsCardActionPrefix is the prefix for all settings card callback actions.
const settingsCardActionPrefix = "settings_"

// BuildSettingsCard constructs an interactive Feishu card JSON for settings.
// tab: "basic" | "model" | "market"
// senderID: current user ID (needed for model listing)
// chatID: current chat ID
func (f *FeishuChannel) BuildSettingsCard(ctx context.Context, senderID, chatID, tab string) (map[string]any, error) {
	if tab == "" {
		tab = "basic"
	}

	// Fetch current settings
	var settings map[string]string
	if f.settingsCallbacks.SettingsGet != nil {
		var err error
		settings, err = f.settingsCallbacks.SettingsGet(f.Name(), senderID)
		if err != nil {
			log.WithError(err).Warn("BuildSettingsCard: failed to get settings")
			settings = make(map[string]string)
		}
	}

	elements := buildTabButtons(tab)
	elements = append(elements, map[string]any{"tag": "hr"})

	switch tab {
	case "basic":
		elements = append(elements, f.buildBasicTabContent(settings)...)
	case "model":
		elements = append(elements, f.buildModelTabContent(ctx, senderID)...)
	case "market":
		elements = append(elements, f.buildMarketTabContent(ctx, senderID)...)
	}

	card := map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"wide_screen_mode": true,
			"update_multi":     true,
		},
		"header": map[string]any{
			"title": map[string]any{
				"tag":     "plain_text",
				"content": "⚙️ 设置",
			},
			"template": "indigo",
		},
		"body": map[string]any{
			"elements": elements,
		},
	}

	return card, nil
}

// HandleSettingsAction processes settings card callback actions.
// It returns the updated card JSON that should be returned in the callback response.
func (f *FeishuChannel) HandleSettingsAction(ctx context.Context, actionData map[string]any, senderID, chatID, messageID string) (map[string]any, error) {
	actionDataJSON, _ := actionData["action_data"].(string)
	if actionDataJSON == "" {
		return nil, fmt.Errorf("missing action_data")
	}

	parsed := parseActionData(actionDataJSON)
	if parsed == nil {
		return nil, fmt.Errorf("invalid action_data format")
	}

	action := parsed["action"]
	switch action {
	case "settings_tab":
		tab := parsed["tab"]
		return f.BuildSettingsCard(ctx, senderID, chatID, tab)

	case "settings_set":
		key := parsed["key"]
		value := parsed["value"]
		if value == "" {
			if opt, ok := actionData["selected_option"].(string); ok {
				value = opt
			}
		}
		if key == "" {
			return nil, fmt.Errorf("missing key in settings_set action")
		}
		if f.settingsCallbacks.SettingsSet != nil {
			if err := f.settingsCallbacks.SettingsSet(f.Name(), senderID, key, value); err != nil {
				log.WithError(err).Warnf("HandleSettingsAction: failed to set %s=%s", key, value)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "basic")

	case "settings_set_model":
		model := parsed["model"]
		if model == "" {
			if opt, ok := actionData["selected_option"].(string); ok {
				model = opt
			}
		}
		if model == "" {
			return nil, fmt.Errorf("missing model in settings_set_model action")
		}
		if f.settingsCallbacks.LLMSet != nil {
			if err := f.settingsCallbacks.LLMSet(senderID, model); err != nil {
				log.WithError(err).Warnf("HandleSettingsAction: failed to set model %s", model)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "model")

	case "settings_install":
		entryType := parsed["entry_type"]
		entryIDStr := parsed["entry_id"]
		if entryType == "" || entryIDStr == "" {
			return nil, fmt.Errorf("missing entry_type or entry_id in settings_install action")
		}
		entryID, err := strconv.ParseInt(entryIDStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid entry_id: %s", entryIDStr)
		}
		if f.settingsCallbacks.RegistryInstall != nil {
			if err := f.settingsCallbacks.RegistryInstall(entryType, entryID, senderID); err != nil {
				log.WithError(err).Warnf("HandleSettingsAction: failed to install %s/%d", entryType, entryID)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "market")

	default:
		return nil, fmt.Errorf("unknown settings action: %s", action)
	}
}

// --- Tab content builders ---

// buildTabButtons creates the tab switching buttons in a horizontal layout.
func buildTabButtons(currentTab string) []map[string]any {
	tabs := []struct {
		key   string
		label string
	}{
		{"basic", "🎯 基础"},
		{"model", "🤖 模型"},
		{"market", "📦 市场"},
	}

	var buttons []map[string]any
	for _, t := range tabs {
		isActive := t.key == currentTab
		btnType := "default"
		if isActive {
			btnType = "primary"
		}
		buttons = append(buttons, map[string]any{
			"tag": "button",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": t.label,
			},
			"type": btnType,
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action": "settings_tab",
					"tab":    t.key,
				}),
			},
		})
	}

	return []map[string]any{wrapButtonsInColumns(buttons)}
}

// buildBasicTabContent builds the basic settings tab with select dropdowns and toggles.
func (f *FeishuChannel) buildBasicTabContent(settings map[string]string) []map[string]any {
	schema := feishuSettingsSchema()
	var elements []map[string]any

	categories := make(map[string][]SettingDefinition)
	var catOrder []string
	for _, def := range schema {
		cat := def.Category
		if cat == "" {
			cat = "通用"
		}
		if _, exists := categories[cat]; !exists {
			catOrder = append(catOrder, cat)
		}
		categories[cat] = append(categories[cat], def)
	}

	for _, cat := range catOrder {
		defs := categories[cat]
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": fmt.Sprintf("**%s**", cat),
		})

		for _, def := range defs {
			currentValue := settings[def.Key]
			if currentValue == "" {
				currentValue = def.DefaultValue
			}

			switch def.Type {
			case SettingTypeSelect:
				elements = append(elements, buildSelectSetting(def, currentValue))

			case SettingTypeToggle:
				isOn := currentValue == "true"
				toggleValue := "true"
				if isOn {
					toggleValue = "false"
				}

				statusIcon := "🔴"
				statusText := "关闭"
				if isOn {
					statusIcon = "🟢"
					statusText = "开启"
				}

				elements = append(elements, buildSettingRow(
					fmt.Sprintf("%s %s", def.Label, def.Description),
					fmt.Sprintf("%s %s", statusIcon, statusText),
					map[string]any{
						"tag": "button",
						"text": map[string]any{
							"tag":     "plain_text",
							"content": "切换",
						},
						"type": "default",
						"size": "small",
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action": "settings_set",
								"key":    def.Key,
								"value":  toggleValue,
							}),
						},
					},
				))
			}
		}
	}

	return elements
}

// buildModelTabContent builds the model selection tab with a dropdown.
func (f *FeishuChannel) buildModelTabContent(ctx context.Context, senderID string) []map[string]any {
	var elements []map[string]any

	var models []string
	currentModel := ""
	if f.settingsCallbacks.LLMList != nil {
		models, currentModel = f.settingsCallbacks.LLMList(senderID)
	}

	if len(models) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "**当前模型：** `" + currentModel + "`\n\n_暂无可用模型。请先使用 `/set-llm` 配置自定义 LLM。_",
		})
		return elements
	}

	maxModels := 20
	if len(models) > maxModels {
		models = models[:maxModels]
	}

	var options []map[string]any
	for _, model := range models {
		options = append(options, map[string]any{
			"text": map[string]any{
				"tag":     "plain_text",
				"content": model,
			},
			"value": model,
		})
	}

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": fmt.Sprintf("当前模型：**%s**", currentModel),
	})

	elements = append(elements, map[string]any{
		"tag":            "select_static",
		"name":           "settings_model_select",
		"placeholder":    map[string]any{"tag": "plain_text", "content": "选择模型..."},
		"initial_option": currentModel,
		"options":        options,
		"value": map[string]string{
			"action_data": mustMapToJSON(map[string]string{
				"action": "settings_set_model",
			}),
		},
	})

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "💡 选择即可切换。使用 `/llm` 查看完整 LLM 配置。",
	})

	return elements
}

// buildMarketTabContent builds the registry/market browsing tab.
func (f *FeishuChannel) buildMarketTabContent(ctx context.Context, senderID string) []map[string]any {
	var elements []map[string]any

	if f.settingsCallbacks.RegistryBrowse == nil {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_Registry 功能未启用_",
		})
		return elements
	}

	// Skills section
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**📦 Skills**",
	})

	skillEntries, err := f.settingsCallbacks.RegistryBrowse("skill", 10, 0)
	if err != nil {
		log.WithError(err).Warn("BuildSettingsCard: failed to browse skills")
	}
	if len(skillEntries) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_暂无公开的 Skill_",
		})
	} else {
		var buttons []map[string]any
		for _, entry := range skillEntries {
			buttons = append(buttons, map[string]any{
				"tag": "button",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": fmt.Sprintf("📥 %s", entry.Name),
				},
				"type": "default",
				"size": "small",
				"value": map[string]string{
					"action_data": mustMapToJSON(map[string]string{
						"action":     "settings_install",
						"entry_type": "skill",
						"entry_id":   fmt.Sprintf("%d", entry.ID),
					}),
				},
			})
		}
		elements = append(elements, wrapButtonsInColumns(buttons))
	}

	// Agents section
	elements = append(elements, map[string]any{"tag": "hr"})
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**🤖 Agents**",
	})

	agentEntries, err := f.settingsCallbacks.RegistryBrowse("agent", 10, 0)
	if err != nil {
		log.WithError(err).Warn("BuildSettingsCard: failed to browse agents")
	}
	if len(agentEntries) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_暂无公开的 Agent_",
		})
	} else {
		var buttons []map[string]any
		for _, entry := range agentEntries {
			buttons = append(buttons, map[string]any{
				"tag": "button",
				"text": map[string]any{
					"tag":     "plain_text",
					"content": fmt.Sprintf("📥 %s", entry.Name),
				},
				"type": "default",
				"size": "small",
				"value": map[string]string{
					"action_data": mustMapToJSON(map[string]string{
						"action":     "settings_install",
						"entry_type": "agent",
						"entry_id":   fmt.Sprintf("%d", entry.ID),
					}),
				},
			})
		}
		elements = append(elements, wrapButtonsInColumns(buttons))
	}

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "💡 也可以使用 `/browse` 和 `/install` 命令管理市场资源。",
	})

	return elements
}

// --- Helpers ---

// buildSelectSetting builds a select_static dropdown for a setting definition.
func buildSelectSetting(def SettingDefinition, currentValue string) map[string]any {
	var options []map[string]any
	for _, opt := range def.Options {
		options = append(options, map[string]any{
			"text": map[string]any{
				"tag":     "plain_text",
				"content": opt.Label,
			},
			"value": opt.Value,
		})
	}

	return buildSettingRow(
		fmt.Sprintf("%s %s", def.Label, def.Description),
		formatCurrentValue(currentValue, def),
		map[string]any{
			"tag":            "select_static",
			"name":           "settings_" + def.Key,
			"placeholder":    map[string]any{"tag": "plain_text", "content": "选择..."},
			"initial_option": currentValue,
			"options":        options,
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action": "settings_set",
					"key":    def.Key,
				}),
			},
		},
	)
}

// buildSettingRow creates a two-column layout with label on the left and control on the right.
func buildSettingRow(label, currentDisplay string, control map[string]any) map[string]any {
	return map[string]any{
		"tag":                "column_set",
		"flex_mode":          "none",
		"horizontal_spacing": "default",
		"columns": []map[string]any{
			{
				"tag":            "column",
				"width":          "weighted",
				"weight":         1,
				"vertical_align": "center",
				"elements": []map[string]any{
					{
						"tag":     "markdown",
						"content": fmt.Sprintf("%s　**%s**", label, currentDisplay),
					},
				},
			},
			{
				"tag":            "column",
				"width":          "weighted",
				"weight":         1,
				"vertical_align": "center",
				"elements": []map[string]any{
					control,
				},
			},
		},
	}
}

// wrapButtonsInColumns wraps button elements in a V2-compatible column_set layout.
func wrapButtonsInColumns(buttons []map[string]any) map[string]any {
	return map[string]any{
		"tag":                "column_set",
		"flex_mode":          "none",
		"horizontal_spacing": "default",
		"columns": []map[string]any{
			{
				"tag":    "column",
				"width":  "weighted",
				"weight": 1,
				"elements": []map[string]any{
					{
						"tag":      "interactive_container",
						"elements": buttons,
					},
				},
			},
		},
	}
}

// formatCurrentValue returns the display label for the current setting value.
func formatCurrentValue(value string, def SettingDefinition) string {
	for _, opt := range def.Options {
		if opt.Value == value {
			return opt.Label
		}
	}
	return value
}

// mustMapToJSON serializes a flat map[string]string to a compact JSON string.
func mustMapToJSON(m map[string]string) string {
	data, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(data)
}

// parseActionData parses a JSON action_data string to map[string]string.
func parseActionData(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var result map[string]string
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil
	}
	return result
}

// parseActionDataFromMap extracts action_data from a raw action data map
// and parses it to map[string]string.
func parseActionDataFromMap(actionData map[string]any) map[string]string {
	raw, ok := actionData["action_data"].(string)
	if !ok {
		return nil
	}
	return parseActionData(raw)
}
