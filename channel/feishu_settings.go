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

// context mode display labels
var contextModeLabels = map[string]string{
	"phase1": "双视图压缩",
	"phase2": "渐进压缩",
	"none":   "禁用压缩",
}

// BuildSettingsCard constructs an interactive Feishu card JSON for settings.
// tab: "general" | "model" | "market"
func (f *FeishuChannel) BuildSettingsCard(ctx context.Context, senderID, chatID, tab string) (map[string]any, error) {
	switch tab {
	case "general", "model", "market":
	default:
		tab = "general"
	}

	elements := buildTabButtons(tab)
	elements = append(elements, map[string]any{"tag": "hr"})

	switch tab {
	case "general":
		elements = append(elements, f.buildGeneralTabContent()...)
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

	case "settings_context_mode":
		mode := parsed["mode"]
		if mode == "" {
			if opt, ok := actionData["selected_option"].(string); ok {
				mode = opt
			}
		}
		if mode == "" {
			return nil, fmt.Errorf("missing mode")
		}
		if f.settingsCallbacks.ContextModeSet != nil {
			if err := f.settingsCallbacks.ContextModeSet(mode); err != nil {
				return nil, fmt.Errorf("切换失败: %v", err)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general")

	case "settings_set_model":
		model := parsed["model"]
		if model == "" {
			if opt, ok := actionData["selected_option"].(string); ok {
				model = opt
			}
		}
		if model == "" {
			return nil, fmt.Errorf("missing model")
		}
		if f.settingsCallbacks.LLMSet != nil {
			if err := f.settingsCallbacks.LLMSet(senderID, model); err != nil {
				return nil, fmt.Errorf("设置模型失败: %v", err)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "model")

	case "settings_install":
		entryType := parsed["entry_type"]
		entryIDStr := parsed["entry_id"]
		if entryType == "" || entryIDStr == "" {
			return nil, fmt.Errorf("missing entry_type or entry_id")
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

func buildTabButtons(currentTab string) []map[string]any {
	tabs := []struct {
		key   string
		label string
	}{
		{"general", "🎯 通用"},
		{"model", "🤖 模型"},
		{"market", "📦 市场"},
	}

	var buttons []map[string]any
	for _, t := range tabs {
		btnType := "default"
		if t.key == currentTab {
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

// buildGeneralTabContent builds the general settings tab with real controls.
func (f *FeishuChannel) buildGeneralTabContent() []map[string]any {
	var elements []map[string]any

	// --- Context Mode ---
	currentMode := "phase1"
	if f.settingsCallbacks.ContextModeGet != nil {
		currentMode = f.settingsCallbacks.ContextModeGet()
	}

	modeLabel := contextModeLabels[currentMode]
	if modeLabel == "" {
		modeLabel = currentMode
	}

	var modeOptions []map[string]any
	for _, m := range []struct{ value, label string }{
		{"phase1", "双视图压缩"},
		{"phase2", "渐进压缩"},
		{"none", "禁用压缩"},
	} {
		modeOptions = append(modeOptions, map[string]any{
			"text":  map[string]any{"tag": "plain_text", "content": m.label},
			"value": m.value,
		})
	}

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**上下文管理**",
	})

	elements = append(elements, buildSettingRow(
		"压缩模式",
		modeLabel,
		map[string]any{
			"tag":            "select_static",
			"name":           "settings_context_mode",
			"placeholder":    map[string]any{"tag": "plain_text", "content": "选择模式..."},
			"initial_option": currentMode,
			"options":        modeOptions,
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action": "settings_context_mode",
				}),
			},
		},
	))

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**双视图**：摘要+尾部原文 · **渐进**：渐进式智能压缩 · **禁用**：不自动压缩",
	})

	return elements
}

// buildModelTabContent builds the model configuration tab.
func (f *FeishuChannel) buildModelTabContent(ctx context.Context, senderID string) []map[string]any {
	var elements []map[string]any

	hasCustom := false
	if f.settingsCallbacks.LLMHasCustom != nil {
		hasCustom = f.settingsCallbacks.LLMHasCustom(senderID)
	}

	var models []string
	currentModel := ""
	if f.settingsCallbacks.LLMList != nil {
		models, currentModel = f.settingsCallbacks.LLMList(senderID)
	}

	if !hasCustom {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": fmt.Sprintf("当前使用全局模型：**%s**", currentModel),
		})
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "配置自定义 LLM 后可在此切换模型：\n```\n/set-llm provider=openai base_url=https://... api_key=sk-xxx model=gpt-4o\n```",
		})
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "支持 `openai`（含兼容 API）和 `anthropic` 两种 provider。\n使用 `/llm` 查看当前配置。",
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
			"text":  map[string]any{"tag": "plain_text", "content": model},
			"value": model,
		})
	}

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": fmt.Sprintf("当前模型：**%s**", currentModel),
	})

	if len(options) > 0 {
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
	}

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "💡 `/llm` 查看完整配置 · `/set-llm` 修改配置 · `/unset-llm` 恢复全局默认",
	})

	return elements
}

// buildMarketTabContent builds the market browsing tab.
func (f *FeishuChannel) buildMarketTabContent(ctx context.Context, senderID string) []map[string]any {
	var elements []map[string]any

	if f.settingsCallbacks.RegistryBrowse == nil {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_Registry 功能未启用_",
		})
		return elements
	}

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
		"content": "💡 `/browse` 浏览市场 · `/install` 安装 · `/my skills` 查看已安装",
	})

	return elements
}

// --- Layout helpers ---

// buildSettingRow creates a two-column row: label+value on the left, control on the right.
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

// --- Parsing helpers ---

func mustMapToJSON(m map[string]string) string {
	data, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(data)
}

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

func parseActionDataFromMap(actionData map[string]any) map[string]string {
	raw, ok := actionData["action_data"].(string)
	if !ok {
		return nil
	}
	return parseActionData(raw)
}
