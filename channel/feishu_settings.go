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

	// Build header elements
	elements := []map[string]any{
		{
			"tag":     "markdown",
			"content": "**⚙️ 设置面板**",
		},
		{"tag": "hr"},
	}

	// Tab buttons
	elements = append(elements, buildTabButtons(tab)...)
	elements = append(elements, map[string]any{"tag": "hr"})

	// Tab content
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
			"template": "blue",
		},
		"body": map[string]any{
			"elements": elements,
		},
	}

	return card, nil
}

// HandleSettingsAction processes settings card callback actions.
// It returns the updated card JSON that should be patched back to the message.
func (f *FeishuChannel) HandleSettingsAction(ctx context.Context, actionData map[string]any, senderID, chatID, messageID string) (map[string]any, error) {
	// Extract action_data JSON string
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

// buildTabButtons creates the three tab switching buttons.
func buildTabButtons(currentTab string) []map[string]any {
	tabs := []struct {
		key   string
		label string
	}{
		{"basic", "🎯 基础"},
		{"model", "🤖 模型"},
		{"market", "📦 市场"},
	}

	var elements []map[string]any
	var actions []map[string]any
	for _, t := range tabs {
		isActive := t.key == currentTab
		btnType := "default"
		if isActive {
			btnType = "primary"
		}
		label := t.label
		if isActive {
			label = "▶ " + t.label
		}
		actions = append(actions, map[string]any{
			"tag": "button",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": label,
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

	elements = append(elements, wrapButtonsAsColumnSet(actions))

	return elements
}

// buildBasicTabContent builds the basic settings tab content.
func (f *FeishuChannel) buildBasicTabContent(settings map[string]string) []map[string]any {
	schema := feishuSettingsSchema()
	var elements []map[string]any

	// Group by category
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
				// Show current value as markdown
				displayVal := formatCurrentValue(currentValue, def)
				elements = append(elements, map[string]any{
					"tag":     "markdown",
					"content": fmt.Sprintf("- %s：**%s**", def.Label, displayVal),
				})
				// Option buttons
				var actions []map[string]any
				for _, opt := range def.Options {
					isActive := opt.Value == currentValue
					btnType := "default"
					if isActive {
						btnType = "primary"
					}
					actions = append(actions, map[string]any{
						"tag": "button",
						"text": map[string]any{
							"tag":     "plain_text",
							"content": opt.Label,
						},
						"type": btnType,
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action": "settings_set",
								"key":    def.Key,
								"value":  opt.Value,
							}),
						},
					})
				}
				elements = append(elements, wrapButtonsAsColumnSet(actions))

			case SettingTypeToggle:
				isOn := currentValue == "true"
				displayLabel := "❌ 关"
				if isOn {
					displayLabel = "✅ 开"
				}
				elements = append(elements, map[string]any{
					"tag":     "markdown",
					"content": fmt.Sprintf("- %s：**%s**", def.Label, displayLabel),
				})
				toggleValue := "true"
				if isOn {
					toggleValue = "false"
				}
				elements = append(elements, wrapButtonsAsColumnSet([]map[string]any{
					{
						"tag": "button",
						"text": map[string]any{
							"tag":     "plain_text",
							"content": "切换",
						},
						"type": "default",
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action": "settings_set",
								"key":    def.Key,
								"value":  toggleValue,
							}),
						},
					},
				}))
			}
		}
	}

	return elements
}

// buildModelTabContent builds the model selection tab content.
func (f *FeishuChannel) buildModelTabContent(ctx context.Context, senderID string) []map[string]any {
	var elements []map[string]any

	var models []string
	currentModel := ""
	if f.settingsCallbacks.LLMList != nil {
		models, currentModel = f.settingsCallbacks.LLMList(senderID)
	}

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": fmt.Sprintf("**当前模型：** `%s`", currentModel),
	})

	if len(models) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_暂无可用模型。请先使用 `/set-llm` 配置自定义 LLM。_",
		})
		return elements
	}

	// Model selection buttons (limit to avoid oversized card)
	maxModels := 10
	if len(models) > maxModels {
		models = models[:maxModels]
	}

	var actions []map[string]any
	for _, model := range models {
		isActive := model == currentModel
		btnType := "default"
		if isActive {
			btnType = "primary"
		}
		label := model
		if isActive {
			label = "✅ " + model
		}
		actions = append(actions, map[string]any{
			"tag": "button",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": label,
			},
			"type": btnType,
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action": "settings_set_model",
					"model":  model,
				}),
			},
		})
	}

	elements = append(elements, wrapButtonsAsColumnSet(actions))

	if len(models) > 0 {
		elements = append(elements, map[string]any{
			"tag": "note",
			"elements": []map[string]any{
				{
					"tag":     "plain_text",
					"content": "💡 点击模型名称即可切换。使用 `/llm` 查看完整 LLM 配置。",
				},
			},
		})
	}

	return elements
}

// buildMarketTabContent builds the registry/market browsing tab content.
func (f *FeishuChannel) buildMarketTabContent(ctx context.Context, senderID string) []map[string]any {
	var elements []map[string]any

	if f.settingsCallbacks.RegistryBrowse == nil {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_Registry 功能未启用_",
		})
		return elements
	}

	// Browse skills
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**📦 Skills 市场**",
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
		for _, entry := range skillEntries {
			elements = append(elements, wrapButtonsAsColumnSet([]map[string]any{
				{
					"tag": "button",
					"text": map[string]any{
						"tag":     "plain_text",
						"content": fmt.Sprintf("📥 %s", entry.Name),
					},
					"type": "default",
					"value": map[string]string{
						"action_data": mustMapToJSON(map[string]string{
							"action":     "settings_install",
							"entry_type": "skill",
							"entry_id":   fmt.Sprintf("%d", entry.ID),
						}),
					},
				},
			}))
		}
	}

	// Browse agents
	elements = append(elements, map[string]any{"tag": "hr"})
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**🤖 Agents 市场**",
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
		for _, entry := range agentEntries {
			elements = append(elements, wrapButtonsAsColumnSet([]map[string]any{
				{
					"tag": "button",
					"text": map[string]any{
						"tag":     "plain_text",
						"content": fmt.Sprintf("📥 %s", entry.Name),
					},
					"type": "default",
					"value": map[string]string{
						"action_data": mustMapToJSON(map[string]string{
							"action":     "settings_install",
							"entry_type": "agent",
							"entry_id":   fmt.Sprintf("%d", entry.ID),
						}),
					},
				},
			}))
		}
	}

	elements = append(elements, map[string]any{
		"tag": "note",
		"elements": []map[string]any{
			{
				"tag":     "plain_text",
				"content": "💡 也可以使用 `/browse` 和 `/install` 命令管理市场资源。",
			},
		},
	})

	return elements
}

// --- Helpers ---

// wrapButtonsAsColumnSet wraps a slice of button elements in a Schema V2-compatible
// column_set > column > interactive_container structure, replacing the deprecated
// V1 "action" tag.
func wrapButtonsAsColumnSet(buttons []map[string]any) map[string]any {
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
