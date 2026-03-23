package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	log "xbot/logger"
)

const settingsCardActionPrefix = "settings_"

var contextModeLabels = map[string]string{
	"phase1": "双视图压缩",
	"none":   "禁用压缩",
}

// BuildSettingsCard constructs an interactive Feishu card JSON for settings.
func (f *FeishuChannel) BuildSettingsCard(ctx context.Context, senderID, chatID, tab string) (map[string]any, error) {
	switch tab {
	case "general", "model", "market", "metrics":
	default:
		tab = "general"
	}

	log.WithField("tab", tab).Info("BuildSettingsCard start")

	elements := buildTabButtons(tab)
	elements = append(elements, map[string]any{"tag": "hr"})

	switch tab {
	case "general":
		elements = append(elements, f.buildGeneralTabContent()...)
	case "model":
		elements = append(elements, f.buildModelTabContent(ctx, senderID)...)
	case "market":
		elements = append(elements, f.buildMarketTabContent(ctx, senderID)...)
	case "metrics":
		elements = append(elements, f.buildMetricsTabContent()...)
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
	log.WithFields(log.Fields{
		"action":    action,
		"sender_id": senderID,
	}).Info("HandleSettingsAction routing")

	switch action {
	case "settings_tab":
		return f.BuildSettingsCard(ctx, senderID, chatID, parsed["tab"])

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

	case "settings_set_max_context":
		maxCtxStr := parsed["max_context"]
		if maxCtxStr == "" {
			if opt, ok := actionData["selected_option"].(string); ok {
				maxCtxStr = opt
			}
		}
		if maxCtxStr == "" {
			return nil, fmt.Errorf("missing max_context")
		}
		maxCtx, err := strconv.Atoi(maxCtxStr)
		if err != nil {
			return nil, fmt.Errorf("invalid max_context: %v", err)
		}
		if f.settingsCallbacks.LLMSetMaxContext != nil {
			if err := f.settingsCallbacks.LLMSetMaxContext(senderID, maxCtx); err != nil {
				return nil, fmt.Errorf("设置 max_context 失败: %v", err)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "model")

	case "settings_set_concurrency":
		concStr := parsed["conc"]
		if concStr == "" {
			if opt, ok := actionData["selected_option"].(string); ok {
				concStr = opt
			}
		}
		if concStr == "" {
			return nil, fmt.Errorf("missing conc")
		}
		conc, err := strconv.Atoi(concStr)
		if err != nil {
			return nil, fmt.Errorf("invalid conc: %v", err)
		}
		if conc < 1 || conc > 20 {
			return nil, fmt.Errorf("concurrency must be between 1 and 20, got %d", conc)
		}
		if f.settingsCallbacks.LLMSetPersonalConcurrency != nil {
			if err := f.settingsCallbacks.LLMSetPersonalConcurrency(senderID, conc); err != nil {
				return nil, fmt.Errorf("设置并发数失败: %v", err)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "model")

	case "settings_set_llm":
		provider := formStr(actionData, "provider")
		baseURL := formStr(actionData, "base_url")
		apiKey := formStr(actionData, "api_key")
		model := formStr(actionData, "model")
		thinkingMode := formStr(actionData, "thinking_mode")
		if provider == "" || baseURL == "" || apiKey == "" {
			return nil, fmt.Errorf("请填写完整配置")
		}
		if f.settingsCallbacks.LLMSetConfig != nil {
			if err := f.settingsCallbacks.LLMSetConfig(senderID, provider, baseURL, apiKey, model); err != nil {
				return nil, fmt.Errorf("保存失败: %v", err)
			}
		}
		if thinkingMode != "" && f.settingsCallbacks.LLMSetThinkingMode != nil {
			if err := f.settingsCallbacks.LLMSetThinkingMode(senderID, thinkingMode); err != nil {
				log.WithError(err).Warn("HandleSettingsAction: failed to set thinking_mode")
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "model")

	case "settings_set_thinking_mode":
		mode := parsed["mode"]
		if mode == "" {
			if opt, ok := actionData["selected_option"].(string); ok {
				mode = opt
			}
		}
		if mode == "" {
			return nil, fmt.Errorf("missing mode")
		}
		if f.settingsCallbacks.LLMSetThinkingMode != nil {
			if err := f.settingsCallbacks.LLMSetThinkingMode(senderID, mode); err != nil {
				return nil, fmt.Errorf("设置思考模式失败: %v", err)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "model")

	case "settings_delete_llm":
		if f.settingsCallbacks.LLMDelete != nil {
			if err := f.settingsCallbacks.LLMDelete(senderID); err != nil {
				return nil, fmt.Errorf("删除失败: %v", err)
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

	case "settings_publish":
		entryType := parsed["entry_type"]
		name := parsed["name"]
		if entryType == "" || name == "" {
			return nil, fmt.Errorf("missing entry_type or name")
		}
		if f.settingsCallbacks.RegistryPublish != nil {
			if err := f.settingsCallbacks.RegistryPublish(entryType, name, senderID); err != nil {
				log.WithError(err).Warnf("HandleSettingsAction: failed to publish %s/%s", entryType, name)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "market")

	case "settings_unpublish":
		entryType := parsed["entry_type"]
		name := parsed["name"]
		if entryType == "" || name == "" {
			return nil, fmt.Errorf("missing entry_type or name")
		}
		if f.settingsCallbacks.RegistryUnpublish != nil {
			if err := f.settingsCallbacks.RegistryUnpublish(entryType, name, senderID); err != nil {
				log.WithError(err).Warnf("HandleSettingsAction: failed to unpublish %s/%s", entryType, name)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "market")

	case "settings_delete_item":
		entryType := parsed["entry_type"]
		name := parsed["name"]
		if entryType == "" || name == "" {
			return nil, fmt.Errorf("missing entry_type or name")
		}
		if f.settingsCallbacks.RegistryDelete != nil {
			if err := f.settingsCallbacks.RegistryDelete(entryType, name, senderID); err != nil {
				log.WithError(err).Warnf("HandleSettingsAction: failed to delete %s/%s", entryType, name)
			}
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "market")

	case "settings_sandbox_cleanup":
		if f.settingsCallbacks.SandboxCleanupTrigger == nil {
			return nil, fmt.Errorf("沙箱持久化功能未启用")
		}
		if f.settingsCallbacks.SandboxIsExporting != nil && f.settingsCallbacks.SandboxIsExporting(senderID) {
			return nil, fmt.Errorf("沙箱正在持久化中，请稍候")
		}
		if err := f.settingsCallbacks.SandboxCleanupTrigger(senderID); err != nil {
			return nil, fmt.Errorf("沙箱持久化失败: %v", err)
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general")

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
		{"metrics", "📊 指标"},
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

func (f *FeishuChannel) buildGeneralTabContent() []map[string]any {
	var elements []map[string]any

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

	// Sandbox cleanup section
	elements = append(elements, map[string]any{"tag": "hr"})
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**沙箱管理**",
	})
	cleanupLabel := "持久化沙箱环境（export + import）"
	elements = append(elements, buildSettingRow(
		cleanupLabel,
		"",
		map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "plain_text", "content": "💾 执行持久化"},
			"type": "default",
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action": "settings_sandbox_cleanup",
				}),
			},
		},
	))
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "将当前沙箱文件系统导出为镜像，用于持久保存。执行期间该用户所有请求将被拒绝。",
	})

	return elements
}

// buildModelTabContent builds the model configuration tab.
func (f *FeishuChannel) buildModelTabContent(ctx context.Context, senderID string) []map[string]any {
	var elements []map[string]any

	hasCustom := false
	var cfgProvider, cfgBaseURL, cfgModel string
	if f.settingsCallbacks.LLMGetConfig != nil {
		var ok bool
		cfgProvider, cfgBaseURL, cfgModel, ok = f.settingsCallbacks.LLMGetConfig(senderID)
		hasCustom = ok
	}

	if !hasCustom {
		// No custom LLM — show setup form
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "**配置个人模型**",
		})
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "当前使用系统默认模型，配置个人 LLM 后可自由选择模型。",
		})

		formElements := []map[string]any{
			{
				"tag":  "select_static",
				"name": "provider",
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "选择 Provider",
				},
				"options": []map[string]any{
					{"text": map[string]any{"tag": "plain_text", "content": "OpenAI（含兼容 API）"}, "value": "openai"},
					{"text": map[string]any{"tag": "plain_text", "content": "Anthropic"}, "value": "anthropic"},
				},
			},
			{
				"tag":  "input",
				"name": "base_url",
				"label": map[string]any{
					"tag":     "plain_text",
					"content": "API 地址",
				},
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "https://api.openai.com/v1",
				},
			},
			{
				"tag":  "input",
				"name": "api_key",
				"label": map[string]any{
					"tag":     "plain_text",
					"content": "API Key",
				},
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "sk-...",
				},
			},
			{
				"tag":  "input",
				"name": "model",
				"label": map[string]any{
					"tag":     "plain_text",
					"content": "模型名称（可选，保存后可从列表选择）",
				},
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "gpt-4o",
				},
			},
			{
				"tag":  "select_static",
				"name": "thinking_mode",
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "思考模式（可选）",
				},
				"options": thinkingModeOptions(),
			},
			{
				"tag":         "button",
				"name":        "llm_submit",
				"text":        map[string]any{"tag": "plain_text", "content": "保存配置"},
				"type":        "primary",
				"action_type": "form_submit",
				"value": map[string]string{
					"action_data": mustMapToJSON(map[string]string{
						"action": "settings_set_llm",
					}),
				},
			},
		}

		elements = append(elements, map[string]any{
			"tag":      "form",
			"name":     "llm_setup_form",
			"elements": formElements,
		})

		return elements
	}

	// Has custom LLM — show config info + model switch + delete
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**个人模型配置**",
	})

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": fmt.Sprintf("Provider：**%s**\nAPI 地址：**%s**", cfgProvider, cfgBaseURL),
	})

	var models []string
	currentModel := cfgModel
	if f.settingsCallbacks.LLMList != nil {
		models, currentModel = f.settingsCallbacks.LLMList(senderID)
	}
	if currentModel == "" {
		currentModel = cfgModel
	}

	maxModels := 30
	if len(models) > maxModels {
		models = models[:maxModels]
	}

	if len(models) > 0 {
		var options []map[string]any
		for _, m := range models {
			options = append(options, map[string]any{
				"text":  map[string]any{"tag": "plain_text", "content": m},
				"value": m,
			})
		}

		elements = append(elements, buildSettingRow(
			"当前模型",
			currentModel,
			map[string]any{
				"tag":            "select_static",
				"name":           "settings_model_select",
				"placeholder":    map[string]any{"tag": "plain_text", "content": "切换模型..."},
				"initial_option": currentModel,
				"options":        options,
				"value": map[string]string{
					"action_data": mustMapToJSON(map[string]string{
						"action": "settings_set_model",
					}),
				},
			},
		))
	} else {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": fmt.Sprintf("当前模型：**%s**", currentModel),
		})
	}

	// Max context setting
	currentMaxContext := 0
	maxContextDisplay := "默认"
	if f.settingsCallbacks.LLMGetMaxContext != nil {
		currentMaxContext = f.settingsCallbacks.LLMGetMaxContext(senderID)
	}
	if currentMaxContext > 0 {
		maxContextDisplay = fmt.Sprintf("%d", currentMaxContext)
	}

	maxContextOptions := []map[string]any{
		{"text": map[string]any{"tag": "plain_text", "content": "默认"}, "value": "0"},
		{"text": map[string]any{"tag": "plain_text", "content": "8,000"}, "value": "8000"},
		{"text": map[string]any{"tag": "plain_text", "content": "32,000"}, "value": "32000"},
		{"text": map[string]any{"tag": "plain_text", "content": "65,000"}, "value": "65000"},
		{"text": map[string]any{"tag": "plain_text", "content": "100,000"}, "value": "100000"},
		{"text": map[string]any{"tag": "plain_text", "content": "200,000"}, "value": "200000"},
	}
	elements = append(elements, buildSettingRow(
		"最大上下文",
		maxContextDisplay,
		map[string]any{
			"tag":            "select_static",
			"name":           "settings_max_context_select",
			"placeholder":    map[string]any{"tag": "plain_text", "content": "选择上下文长度..."},
			"initial_option": fmt.Sprintf("%d", currentMaxContext),
			"options":        maxContextOptions,
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action": "settings_set_max_context",
				}),
			},
		},
	))

	// LLM concurrency settings (personal only)
	personalConc := 3 // default
	if f.settingsCallbacks.LLMGetPersonalConcurrency != nil {
		personalConc = f.settingsCallbacks.LLMGetPersonalConcurrency(senderID)
	}

	concOptions := []map[string]any{
		{"text": map[string]any{"tag": "plain_text", "content": "1"}, "value": "1"},
		{"text": map[string]any{"tag": "plain_text", "content": "2"}, "value": "2"},
		{"text": map[string]any{"tag": "plain_text", "content": "3"}, "value": "3"},
		{"text": map[string]any{"tag": "plain_text", "content": "5"}, "value": "5"},
		{"text": map[string]any{"tag": "plain_text", "content": "8"}, "value": "8"},
		{"text": map[string]any{"tag": "plain_text", "content": "10"}, "value": "10"},
		{"text": map[string]any{"tag": "plain_text", "content": "不限"}, "value": "0"},
	}

	elements = append(elements, map[string]any{"tag": "hr"})
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**个人 LLM 并发限制**",
	})
	elements = append(elements, buildSettingRow(
		"并发上限",
		fmt.Sprintf("%d", personalConc),
		map[string]any{
			"tag":            "select_static",
			"name":           "settings_llm_conc_personal",
			"placeholder":    map[string]any{"tag": "plain_text", "content": "选择并发数..."},
			"initial_option": fmt.Sprintf("%d", personalConc),
			"options":        concOptions,
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action": "settings_set_concurrency",
				}),
			},
		},
	))

	// Thinking mode setting
	currentThinkingMode := ""
	thinkingModeDisplay := "auto"
	if f.settingsCallbacks.LLMGetThinkingMode != nil {
		currentThinkingMode = f.settingsCallbacks.LLMGetThinkingMode(senderID)
	}
	if currentThinkingMode != "" {
		thinkingModeDisplay = thinkingModeLabel(currentThinkingMode)
	}

	elements = append(elements, buildSettingRow(
		"思考模式",
		thinkingModeDisplay,
		map[string]any{
			"tag":            "select_static",
			"name":           "settings_thinking_mode_select",
			"placeholder":    map[string]any{"tag": "plain_text", "content": "选择思考模式..."},
			"initial_option": currentThinkingMode,
			"options":        thinkingModeOptions(),
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action": "settings_set_thinking_mode",
				}),
			},
		},
	))

	elements = append(elements, map[string]any{"tag": "hr"})
	elements = append(elements, map[string]any{
		"tag": "button",
		"text": map[string]any{
			"tag":     "plain_text",
			"content": "🗑️ 删除个人配置，恢复系统默认",
		},
		"type": "danger",
		"value": map[string]string{
			"action_data": mustMapToJSON(map[string]string{
				"action": "settings_delete_llm",
			}),
		},
	})

	return elements
}

// buildMarketTabContent builds the market browsing tab with my items + marketplace.
func (f *FeishuChannel) buildMarketTabContent(ctx context.Context, senderID string) []map[string]any {
	var elements []map[string]any

	// "我的" section
	if f.settingsCallbacks.RegistryListMy != nil {
		elements = append(elements, f.buildMyItemsSection(senderID, "skill", "技能")...)
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, f.buildMyItemsSection(senderID, "agent", "代理")...)
		elements = append(elements, map[string]any{"tag": "hr"})
	}

	// Marketplace section
	if f.settingsCallbacks.RegistryBrowse == nil {
		log.Info("buildMarketTabContent: RegistryBrowse callback not set")
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_市场功能未启用_",
		})
		return elements
	}

	elements = append(elements, f.buildMarketSection("skill", "技能市场")...)
	elements = append(elements, map[string]any{"tag": "hr"})
	elements = append(elements, f.buildMarketSection("agent", "代理市场")...)

	log.WithField("element_count", len(elements)).Info("buildMarketTabContent completed")
	return elements
}

func (f *FeishuChannel) buildMyItemsSection(senderID, entryType, label string) []map[string]any {
	var elements []map[string]any

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": fmt.Sprintf("**📁 我的%s**", label),
	})

	published, local, err := f.settingsCallbacks.RegistryListMy(senderID, entryType)
	if err != nil {
		log.WithError(err).Warnf("buildMyItemsSection: ListMy failed for %s", entryType)
	}

	publishedNames := make(map[string]bool)
	for _, e := range published {
		if e.Sharing == "public" {
			publishedNames[e.Name] = true
		}
	}

	prefix := entryType + ":"
	if len(local) == 0 && len(published) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": fmt.Sprintf("_暂无%s_", label),
		})
		return elements
	}

	for _, item := range local {
		name := strings.TrimPrefix(item, prefix)
		if publishedNames[name] {
			// Already shared: show unpublish + delete
			elements = append(elements, buildItemRow(name, "✅ 已分享",
				actionBtn("📤 下架", "settings_unpublish", entryType, name),
				actionBtn("🗑️", "settings_delete_item", entryType, name),
			))
		} else {
			// Not shared: show share + delete
			elements = append(elements, buildItemRow(name, "",
				actionBtn("📤 分享", "settings_publish", entryType, name),
				actionBtn("🗑️", "settings_delete_item", entryType, name),
			))
		}
	}

	// Published items that are no longer local (edge case: deleted locally but still in registry)
	for _, e := range published {
		found := false
		for _, item := range local {
			if strings.TrimPrefix(item, prefix) == e.Name {
				found = true
				break
			}
		}
		if !found && e.Sharing == "public" {
			elements = append(elements, buildItemRow(e.Name, "✅ 已分享（本地已删除）",
				actionBtn("📤 下架", "settings_unpublish", entryType, e.Name),
			))
		}
	}

	return elements
}

func actionBtn(text, action, entryType, name string) map[string]any {
	return map[string]any{
		"tag":  "button",
		"text": map[string]any{"tag": "plain_text", "content": text},
		"type": "default",
		"size": "small",
		"value": map[string]string{
			"action_data": mustMapToJSON(map[string]string{
				"action":     action,
				"entry_type": entryType,
				"name":       name,
			}),
		},
	}
}

func buildItemRow(name, status string, buttons ...map[string]any) map[string]any {
	leftText := "• " + name
	if status != "" {
		leftText += "　" + status
	}
	return map[string]any{
		"tag":                "column_set",
		"flex_mode":          "none",
		"horizontal_spacing": "default",
		"columns": []map[string]any{
			{
				"tag":            "column",
				"width":          "weighted",
				"weight":         2,
				"vertical_align": "center",
				"elements": []map[string]any{
					{"tag": "markdown", "content": leftText},
				},
			},
			{
				"tag":            "column",
				"width":          "weighted",
				"weight":         1,
				"vertical_align": "center",
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

func (f *FeishuChannel) buildMarketSection(entryType, title string) []map[string]any {
	var elements []map[string]any

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": fmt.Sprintf("**🏪 %s**", title),
	})

	entries, err := f.settingsCallbacks.RegistryBrowse(entryType, 10, 0)
	if err != nil {
		log.WithError(err).Warnf("buildMarketSection: Browse failed for %s", entryType)
	}

	if len(entries) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_暂无公开内容_",
		})
		return elements
	}

	var buttons []map[string]any
	for _, entry := range entries {
		desc := entry.Name
		if entry.Description != "" {
			desc = fmt.Sprintf("%s - %s", entry.Name, entry.Description)
		}
		buttons = append(buttons, map[string]any{
			"tag": "button",
			"text": map[string]any{
				"tag":     "plain_text",
				"content": fmt.Sprintf("📥 %s", desc),
			},
			"type": "default",
			"size": "small",
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{
					"action":     "settings_install",
					"entry_type": entryType,
					"entry_id":   fmt.Sprintf("%d", entry.ID),
				}),
			},
		})
	}
	elements = append(elements, wrapButtonsInColumns(buttons))

	return elements
}

// --- Layout helpers ---

func buildSettingRow(label, currentDisplay string, control map[string]any) map[string]any {
	leftContent := label
	if currentDisplay != "" {
		leftContent = fmt.Sprintf("%s　**%s**", label, currentDisplay)
	}
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
						"content": leftContent,
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

// --- Thinking mode helpers ---

var thinkingModeLabelMap = map[string]string{
	"":         "auto（自动）",
	"enabled":  "enabled（开启）",
	"disabled": "disabled（关闭）",
	"adaptive": "adaptive（自适应）",
}

func thinkingModeLabel(mode string) string {
	if l, ok := thinkingModeLabelMap[mode]; ok {
		return l
	}
	return mode
}

func thinkingModeOptions() []map[string]any {
	return []map[string]any{
		{"text": map[string]any{"tag": "plain_text", "content": "auto（自动）"}, "value": "auto"},
		{"text": map[string]any{"tag": "plain_text", "content": "enabled（开启）"}, "value": "enabled"},
		{"text": map[string]any{"tag": "plain_text", "content": "disabled（关闭）"}, "value": "disabled"},
		{"text": map[string]any{"tag": "plain_text", "content": "adaptive（自适应）"}, "value": "adaptive"},
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

func formStr(actionData map[string]any, key string) string {
	if v, ok := actionData[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

// buildMetricsTabContent builds the metrics dashboard tab.
func (f *FeishuChannel) buildMetricsTabContent() []map[string]any {
	var elements []map[string]any

	if f.settingsCallbacks.MetricsGet == nil {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_指标功能未启用_",
		})
		return elements
	}

	metricsText := f.settingsCallbacks.MetricsGet()
	if metricsText == "" {
		metricsText = "暂无指标数据"
	}

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": metricsText,
	})

	return elements
}
