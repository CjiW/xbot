package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	log "xbot/logger"

	"xbot/tools"
)

const settingsCardActionPrefix = "settings_"

const marketPageSize = 5

// SettingsCardOpts carries optional state for building the settings card (e.g. pagination).
type SettingsCardOpts struct {
	MySkillPage     int
	MyAgentPage     int
	SkillMarketPage int
	AgentMarketPage int
	// RunnerConnectBanner, if set, shows a one-shot markdown block with the xbot-runner shell command (after create / token generate).
	RunnerConnectBanner string
}

// BuildSettingsCard constructs an interactive Feishu card JSON for settings.
func (f *FeishuChannel) BuildSettingsCard(ctx context.Context, senderID, chatID, tab string, opts ...SettingsCardOpts) (map[string]any, error) {
	var o SettingsCardOpts
	if len(opts) > 0 {
		o = opts[0]
	}

	switch tab {
	case "general", "model", "market", "metrics", "danger":
	default:
		tab = "general"
	}

	log.WithField("tab", tab).Info("BuildSettingsCard start")

	elements := buildTabButtons(tab)
	elements = append(elements, map[string]any{"tag": "hr"})

	switch tab {
	case "general":
		elements = append(elements, f.buildGeneralTabContent(senderID, o)...)
	case "model":
		elements = append(elements, f.buildModelTabContent(ctx, senderID)...)
	case "market":
		elements = append(elements, f.buildMarketTabContent(ctx, senderID, o)...)
	case "metrics":
		elements = append(elements, f.buildMetricsTabContent()...)
	case "danger":
		elements = append(elements, f.buildDangerTabContent(ctx, senderID, chatID)...)
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
		if maxCtx < 1000 || maxCtx > 2000000 {
			return nil, fmt.Errorf("max_context must be between 1000 and 2000000, got %d", maxCtx)
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
		return f.BuildSettingsCard(ctx, senderID, chatID, "market", parsePageOpts(parsed))

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
		return f.BuildSettingsCard(ctx, senderID, chatID, "market", parsePageOpts(parsed))

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
		return f.BuildSettingsCard(ctx, senderID, chatID, "market", parsePageOpts(parsed))

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
		return f.BuildSettingsCard(ctx, senderID, chatID, "market", parsePageOpts(parsed))

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

	case "settings_market_page":
		return f.BuildSettingsCard(ctx, senderID, chatID, "market", parsePageOpts(parsed))

	case "settings_generate_token":
		if f.settingsCallbacks.RunnerTokenGenerate == nil {
			return nil, fmt.Errorf("per-user runner token 功能未启用")
		}
		mode := formStr(actionData, "runner_mode")
		if mode == "" {
			mode = parsed["runner_mode"]
		}
		if mode == "" {
			mode = "native"
		}
		var dockerImage, workspace string
		if mode == "docker" {
			dockerImage = formStr(actionData, "tok_docker_image")
			workspace = formStr(actionData, "tok_docker_ws")
		} else {
			workspace = formStr(actionData, "tok_native_ws")
		}
		if workspace == "" {
			workspace = "/workspace"
		}
		cmd, err := f.settingsCallbacks.RunnerTokenGenerate(senderID, mode, dockerImage, workspace)
		if err != nil {
			return nil, fmt.Errorf("生成 token 失败: %v", err)
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general", SettingsCardOpts{RunnerConnectBanner: cmd})

	case "settings_revoke_token":
		if f.settingsCallbacks.RunnerTokenRevoke == nil {
			return nil, fmt.Errorf("per-user runner token 功能未启用")
		}
		if err := f.settingsCallbacks.RunnerTokenRevoke(senderID); err != nil {
			return nil, fmt.Errorf("撤销 token 失败: %v", err)
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general")

	// ── Multi-Runner management actions ──
	case "settings_runner_set_active":
		if f.settingsCallbacks.RunnerSetActive == nil {
			return nil, fmt.Errorf("runner 管理功能未启用")
		}
		runnerName := parsed["runner_name"]
		if runnerName == "" {
			return nil, fmt.Errorf("缺少 runner 名称")
		}
		if err := f.settingsCallbacks.RunnerSetActive(senderID, runnerName); err != nil {
			return nil, fmt.Errorf("切换 runner 失败: %v", err)
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general")

	case "settings_runner_delete":
		if f.settingsCallbacks.RunnerDelete == nil {
			return nil, fmt.Errorf("runner 管理功能未启用")
		}
		runnerName := parsed["runner_name"]
		if runnerName == "" {
			return nil, fmt.Errorf("缺少 runner 名称")
		}
		if runnerName == tools.BuiltinDockerRunnerName {
			return nil, fmt.Errorf("内置 Docker Sandbox 不可删除")
		}
		if err := f.settingsCallbacks.RunnerDelete(senderID, runnerName); err != nil {
			return nil, fmt.Errorf("删除 runner 失败: %v", err)
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general")

	case "settings_runner_create":
		if f.settingsCallbacks.RunnerCreate == nil {
			return nil, fmt.Errorf("runner 管理功能未启用")
		}
		// Feishu requires input "name" unique across the entire card; native/docker use separate field names.
		var runnerName, workspace, mode, dockerImage string
		if n := formStr(actionData, "mr_native_name"); n != "" {
			runnerName = n
			workspace = formStr(actionData, "mr_native_workspace")
			mode = "native"
		} else if n := formStr(actionData, "mr_docker_name"); n != "" {
			runnerName = n
			dockerImage = formStr(actionData, "mr_docker_image")
			workspace = formStr(actionData, "mr_docker_workspace")
			mode = "docker"
		} else {
			return nil, fmt.Errorf("请填写 runner 名称")
		}
		if mode != "docker" {
			dockerImage = ""
		}
		cmd, err := f.settingsCallbacks.RunnerCreate(senderID, runnerName, mode, dockerImage, workspace)
		if err != nil {
			return nil, fmt.Errorf("创建 runner 失败: %v", err)
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general", SettingsCardOpts{RunnerConnectBanner: cmd})

	case "settings_feishu_web_link":
		username := formStr(actionData, "web_username")
		password := formStr(actionData, "web_password")
		if username == "" || password == "" {
			return nil, fmt.Errorf("请填写用户名和密码")
		}
		if f.settingsCallbacks.FeishuWebLink == nil {
			return nil, fmt.Errorf("web linking not enabled")
		}
		if _, err := f.settingsCallbacks.FeishuWebLink(senderID, username, password); err != nil {
			return nil, fmt.Errorf("关联失败: %v", err)
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general")

	case "settings_feishu_web_unlink":
		if f.settingsCallbacks.FeishuWebUnlink == nil {
			return nil, fmt.Errorf("web linking not enabled")
		}
		if err := f.settingsCallbacks.FeishuWebUnlink(senderID); err != nil {
			return nil, fmt.Errorf("取消关联失败: %v", err)
		}
		return f.BuildSettingsCard(ctx, senderID, chatID, "general")

	// ── Danger zone: step 1 → show confirmation card ──
	case "danger_clear_session", "danger_clear_core_persona", "danger_clear_core_human",
		"danger_clear_core_working", "danger_clear_core_all", "danger_clear_long_term",
		"danger_clear_event_history", "danger_clear_archival", "danger_reset_all":
		confirmStr := dangerConfirmString(action)
		label := dangerTargetLabel(action)
		return buildDangerConfirmCard(label, confirmStr, action), nil

	// ── Danger zone: step 2 → execute after user confirmation ──
	case "danger_confirm":
		userInput := formStr(actionData, "confirm_input")
		expected := formStr(actionData, "expect_input")
		target := formStr(actionData, "target_action")
		if userInput != expected {
			return buildDangerResultCard("❌ 确认文字不匹配，操作已取消。"), nil
		}
		if f.settingsCallbacks.MemoryClear != nil {
			if err := f.settingsCallbacks.MemoryClear(senderID, chatID, target); err != nil {
				return buildDangerResultCard(fmt.Sprintf("❌ 清空失败：%v", err)), nil
			}
		}
		label := dangerTargetLabel(target)
		return buildDangerResultCard(fmt.Sprintf("✅ 已清空：%s", label)), nil

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
		{"danger", "⚠️ 危险区"},
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

func (f *FeishuChannel) buildGeneralTabContent(senderID string, o SettingsCardOpts) []map[string]any {
	var elements []map[string]any

	// ── Multi-Runner Management Section ──
	if f.settingsCallbacks.RunnerList != nil {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "**🖥️ 工作环境**",
		})

		if strings.TrimSpace(o.RunnerConnectBanner) != "" {
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": fmt.Sprintf("✅ **连接命令**（在本地终端执行）：\n```\n%s\n```", strings.TrimSpace(o.RunnerConnectBanner)),
			})
		}

		runners, err := f.settingsCallbacks.RunnerList(senderID)
		if err != nil || len(runners) == 0 {
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": "尚未添加工作环境。点击下方按钮添加 Runner。",
			})
		} else {
			// Get active runner name
			activeName := ""
			if f.settingsCallbacks.RunnerGetActive != nil {
				if name, err := f.settingsCallbacks.RunnerGetActive(senderID); err == nil {
					activeName = name
				}
			}

			for _, r := range runners {
				isBuiltin := r.Name == tools.BuiltinDockerRunnerName
				statusIcon := "🟢"
				if !r.Online {
					statusIcon = "⚫"
				}
				activeTag := ""
				if r.Name == activeName {
					activeTag = " ← 活跃"
				}
				displayName := r.Name
				if isBuiltin {
					displayName = "Docker Sandbox (内置)"
				}
				modeTag := "原生"
				if r.Mode == "docker" {
					modeTag = "🐳 Docker"
				}
				wsTag := ""
				if r.Workspace != "" {
					wsTag = fmt.Sprintf(" · %s", r.Workspace)
				}
				if isBuiltin && r.DockerImage != "" {
					wsTag = fmt.Sprintf(" · %s", r.DockerImage)
				}
				elements = append(elements, map[string]any{
					"tag":     "markdown",
					"content": fmt.Sprintf("%s **%s**%s (%s%s)", statusIcon, displayName, activeTag, modeTag, wsTag),
				})

				// Buttons: set active + delete (builtin docker cannot be deleted)
				var btns []map[string]any
				if r.Name != activeName {
					btns = append(btns, map[string]any{
						"tag":  "button",
						"text": map[string]any{"tag": "plain_text", "content": "切换"},
						"type": "primary",
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action":      "settings_runner_set_active",
								"runner_name": r.Name,
							}),
						},
					})
				}
				if !isBuiltin {
					btns = append(btns, map[string]any{
						"tag":  "button",
						"text": map[string]any{"tag": "plain_text", "content": "🗑️ 删除"},
						"type": "danger",
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action":      "settings_runner_delete",
								"runner_name": r.Name,
							}),
						},
					})
				}
				if len(btns) > 0 {
					elements = append(elements, wrapButtonsInColumns(btns))
				}
			}
		}

		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "添加 Runner：任选其一提交。**原生**无镜像字段；**Docker** 需填写镜像。",
		})

		formNative := []map[string]any{
			{
				"tag":  "input",
				"name": "mr_native_name",
				"label": map[string]any{
					"tag":     "plain_text",
					"content": "Runner 名称",
				},
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "例如：MacBook Pro",
				},
			},
			{
				"tag":  "input",
				"name": "mr_native_workspace",
				"label": map[string]any{
					"tag":     "plain_text",
					"content": "工作目录",
				},
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "/workspace",
				},
			},
			{
				"tag":         "button",
				"name":        "runner_create_native_submit",
				"text":        map[string]any{"tag": "plain_text", "content": "✨ 添加原生 Runner"},
				"type":        "primary",
				"action_type": "form_submit",
				"value": map[string]string{
					"action_data": mustMapToJSON(map[string]string{
						"action":      "settings_runner_create",
						"runner_mode": "native",
					}),
				},
			},
		}
		elements = append(elements, map[string]any{
			"tag":      "form",
			"name":     "runner_create_form_native",
			"elements": formNative,
		})

		formDocker := []map[string]any{
			{
				"tag":  "input",
				"name": "mr_docker_name",
				"label": map[string]any{
					"tag":     "plain_text",
					"content": "Runner 名称",
				},
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "例如：docker-dev",
				},
			},
			{
				"tag":  "input",
				"name": "mr_docker_image",
				"label": map[string]any{
					"tag":     "plain_text",
					"content": "Docker 镜像",
				},
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "ubuntu:22.04",
				},
			},
			{
				"tag":  "input",
				"name": "mr_docker_workspace",
				"label": map[string]any{
					"tag":     "plain_text",
					"content": "工作目录",
				},
				"placeholder": map[string]any{
					"tag":     "plain_text",
					"content": "/workspace",
				},
			},
			{
				"tag":         "button",
				"name":        "runner_create_docker_submit",
				"text":        map[string]any{"tag": "plain_text", "content": "✨ 添加 Docker Runner"},
				"type":        "primary",
				"action_type": "form_submit",
				"value": map[string]string{
					"action_data": mustMapToJSON(map[string]string{
						"action":      "settings_runner_create",
						"runner_mode": "docker",
					}),
				},
			},
		}
		elements = append(elements, map[string]any{
			"tag":      "form",
			"name":     "runner_create_form_docker",
			"elements": formDocker,
		})

		elements = append(elements, map[string]any{"tag": "hr"})
	} else {
		// ── Legacy: fallback to old single-token runner ──
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "**远程 Runner**",
		})

		if f.settingsCallbacks.RunnerTokenGet != nil {
			connectCmd := f.settingsCallbacks.RunnerTokenGet(senderID)
			if connectCmd != "" {
				elements = append(elements, map[string]any{
					"tag":     "markdown",
					"content": fmt.Sprintf("在本地机器上运行以下命令连接远程沙箱：\n```\n%s\n```", connectCmd),
				})
				elements = append(elements, wrapButtonsInColumns([]map[string]any{
					{
						"tag":  "button",
						"text": map[string]any{"tag": "plain_text", "content": "🔄 重新生成"},
						"type": "danger",
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action": "settings_generate_token",
							}),
						},
					},
					{
						"tag":  "button",
						"text": map[string]any{"tag": "plain_text", "content": "🗑️ 撤销"},
						"type": "danger",
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action": "settings_revoke_token",
							}),
						},
					},
				}))
			} else {
				// No token — show generation forms (legacy): native vs docker
				elements = append(elements, map[string]any{
					"tag":     "markdown",
					"content": "生成连接命令：**原生**仅工作目录；**Docker** 需填写镜像。",
				})
				formLegacyNative := []map[string]any{
					{
						"tag":  "input",
						"name": "tok_native_ws",
						"label": map[string]any{
							"tag":     "plain_text",
							"content": "工作目录",
						},
						"placeholder": map[string]any{
							"tag":     "plain_text",
							"content": "/workspace",
						},
					},
					{
						"tag":         "button",
						"name":        "token_submit_native",
						"text":        map[string]any{"tag": "plain_text", "content": "生成原生连接命令"},
						"type":        "primary",
						"action_type": "form_submit",
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action":      "settings_generate_token",
								"runner_mode": "native",
							}),
						},
					},
				}
				elements = append(elements, map[string]any{
					"tag":      "form",
					"name":     "runner_token_form_native",
					"elements": formLegacyNative,
				})
				formLegacyDocker := []map[string]any{
					{
						"tag":  "input",
						"name": "tok_docker_image",
						"label": map[string]any{
							"tag":     "plain_text",
							"content": "Docker 镜像",
						},
						"placeholder": map[string]any{
							"tag":     "plain_text",
							"content": "xbot-sandbox:latest",
						},
					},
					{
						"tag":  "input",
						"name": "tok_docker_ws",
						"label": map[string]any{
							"tag":     "plain_text",
							"content": "工作目录",
						},
						"placeholder": map[string]any{
							"tag":     "plain_text",
							"content": "/workspace",
						},
					},
					{
						"tag":         "button",
						"name":        "token_submit_docker",
						"text":        map[string]any{"tag": "plain_text", "content": "生成 Docker 连接命令"},
						"type":        "primary",
						"action_type": "form_submit",
						"value": map[string]string{
							"action_data": mustMapToJSON(map[string]string{
								"action":      "settings_generate_token",
								"runner_mode": "docker",
							}),
						},
					},
				}
				elements = append(elements, map[string]any{
					"tag":      "form",
					"name":     "runner_token_form_docker",
					"elements": formLegacyDocker,
				})
			}
		} else if f.settingsCallbacks.RunnerConnectCmdGet != nil {
			connectCmd := f.settingsCallbacks.RunnerConnectCmdGet(senderID)
			if connectCmd != "" {
				elements = append(elements, map[string]any{
					"tag":     "markdown",
					"content": fmt.Sprintf("在本地机器上运行以下命令连接远程沙箱：\n```\n%s\n```", connectCmd),
				})
			} else {
				elements = append(elements, map[string]any{
					"tag":     "markdown",
					"content": "远程 Runner 功能未启用，请设置 `SANDBOX_AUTH_TOKEN`。",
				})
			}
		}

		elements = append(elements, map[string]any{"tag": "hr"})
	}

	// ── Feishu ↔ Web Account Linking Section ──
	if f.settingsCallbacks.FeishuWebGetLinked != nil {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "**🔗 Web 账户关联**",
		})

		linkedUser, linked := f.settingsCallbacks.FeishuWebGetLinked(senderID)
		if linked {
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": fmt.Sprintf("已关联 Web 账户：**%s**", linkedUser),
			})
			elements = append(elements, wrapButtonsInColumns([]map[string]any{
				{
					"tag":  "button",
					"text": map[string]any{"tag": "plain_text", "content": "取消关联"},
					"type": "danger",
					"value": map[string]string{
						"action_data": mustMapToJSON(map[string]string{
							"action": "settings_feishu_web_unlink",
						}),
					},
				},
			}))
		} else {
			elements = append(elements, map[string]any{
				"tag":     "markdown",
				"content": "关联 Web 账户后，可使用飞书身份登录 Web 端。",
			})
			formWebLink := []map[string]any{
				{
					"tag":  "input",
					"name": "web_username",
					"label": map[string]any{
						"tag":     "plain_text",
						"content": "用户名",
					},
					"placeholder": map[string]any{
						"tag":     "plain_text",
						"content": "输入 Web 用户名",
					},
				},
				{
					"tag":  "input",
					"name": "web_password",
					"label": map[string]any{
						"tag":     "plain_text",
						"content": "密码",
					},
					"placeholder": map[string]any{
						"tag":     "plain_text",
						"content": "输入密码",
					},
				},
				{
					"tag":         "button",
					"name":        "web_link_submit",
					"text":        map[string]any{"tag": "plain_text", "content": "关联账户"},
					"type":        "primary",
					"action_type": "form_submit",
					"value": map[string]string{
						"action_data": mustMapToJSON(map[string]string{
							"action": "settings_feishu_web_link",
						}),
					},
				},
			}
			elements = append(elements, map[string]any{
				"tag":      "form",
				"name":     "feishu_web_link_form",
				"elements": formWebLink,
			})
		}

		elements = append(elements, map[string]any{"tag": "hr"})
	}

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

// --- Danger zone helpers ---

// dangerTargetLabel returns the human-readable label for a danger action.
func dangerTargetLabel(action string) string {
	labels := map[string]string{
		"danger_clear_session":       "会话历史",
		"danger_clear_core_persona":  "Core Memory (persona)",
		"danger_clear_core_human":    "Core Memory (human)",
		"danger_clear_core_working":  "Core Memory (working_context)",
		"danger_clear_core_all":      "Core Memory (全部)",
		"danger_clear_long_term":     "长期记忆",
		"danger_clear_event_history": "事件历史",
		"danger_clear_archival":      "归档记忆（向量数据库）",
		"danger_reset_all":           "全部记忆",
	}
	if label, ok := labels[action]; ok {
		return label
	}
	return action
}

// dangerConfirmString returns the string the user must type to confirm.
func dangerConfirmString(action string) string {
	strs := map[string]string{
		"danger_clear_session":       "DELETE-SESSION",
		"danger_clear_core_persona":  "DELETE-PERSONA",
		"danger_clear_core_human":    "DELETE-HUMAN",
		"danger_clear_core_working":  "DELETE-WORKING",
		"danger_clear_core_all":      "DELETE-CORE-MEMORY",
		"danger_clear_long_term":     "DELETE-LONG-TERM",
		"danger_clear_event_history": "DELETE-HISTORY",
		"danger_clear_archival":      "DELETE-ARCHIVAL",
		"danger_reset_all":           "RESET-ALL-MEMORY",
	}
	if s, ok := strs[action]; ok {
		return s
	}
	return "CONFIRM-DELETE"
}

// buildDangerTabContent builds the danger zone tab with memory stats and clear buttons.
func (f *FeishuChannel) buildDangerTabContent(ctx context.Context, senderID, chatID string) []map[string]any {
	var elements []map[string]any

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**⚠️ 危险区**\n以下操作不可恢复，请谨慎操作。",
	})
	elements = append(elements, map[string]any{"tag": "hr"})

	stats := map[string]string{}
	if f.settingsCallbacks.MemoryGetStats != nil {
		stats = f.settingsCallbacks.MemoryGetStats(senderID, chatID)
	}

	type dangerItem struct {
		action string
		label  string
		stat   string
	}
	items := []dangerItem{
		{"danger_clear_session", "会话历史", stats["session"]},
		{"danger_clear_core_persona", "Core Memory: persona", stats["persona"]},
		{"danger_clear_core_human", "Core Memory: human", stats["human"]},
		{"danger_clear_core_working", "Core Memory: working_context", stats["working_context"]},
		{"danger_clear_core_all", "Core Memory: 全部", ""},
		{"danger_clear_long_term", "长期记忆", stats["long_term"]},
		{"danger_clear_event_history", "事件历史", stats["event_history"]},
		{"danger_clear_archival", "归档记忆（向量数据库）", stats["archival"]},
	}

	for _, item := range items {
		text := fmt.Sprintf("🗑️ 清空 %s", item.label)
		if item.stat != "" {
			text += fmt.Sprintf("（%s）", item.stat)
		}
		elements = append(elements, map[string]any{
			"tag":  "button",
			"text": map[string]any{"tag": "plain_text", "content": text},
			"type": "danger",
			"value": map[string]string{
				"action_data": mustMapToJSON(map[string]string{"action": item.action}),
			},
		})
	}

	elements = append(elements, map[string]any{"tag": "hr"})
	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": "**🔴 一键重置全部**\n清空以上所有记忆数据。",
	})
	elements = append(elements, map[string]any{
		"tag":  "button",
		"text": map[string]any{"tag": "plain_text", "content": "🔴 重置全部记忆"},
		"type": "danger",
		"value": map[string]string{
			"action_data": mustMapToJSON(map[string]string{"action": "danger_reset_all"}),
		},
	})

	return elements
}

// buildDangerConfirmCard builds a confirmation card requiring user to type a confirm string.
func buildDangerConfirmCard(targetLabel, confirmString, targetAction string) map[string]any {
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": "⚠️ 确认清空"},
			"template": "red",
		},
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": fmt.Sprintf("**确认清空：%s**\n\n此操作不可恢复。请在下方输入框中输入以下文字确认：\n\n`%s`", targetLabel, confirmString),
				},
				{"tag": "hr"},
				{
					"tag":  "form",
					"name": "danger_confirm_form",
					"elements": []map[string]any{
						{
							"tag":   "input",
							"name":  "confirm_input",
							"label": map[string]any{"tag": "plain_text", "content": "确认文字"},
							"placeholder": map[string]any{
								"tag":     "plain_text",
								"content": confirmString,
							},
						},
						{
							"tag":         "button",
							"name":        "danger_confirm_cancel",
							"text":        map[string]any{"tag": "plain_text", "content": "取消"},
							"type":        "default",
							"action_type": "form_reset",
						},
						{
							"tag":         "button",
							"name":        "danger_confirm_submit",
							"text":        map[string]any{"tag": "plain_text", "content": "🔴 确认清空"},
							"type":        "danger",
							"action_type": "form_submit",
							"value": map[string]string{
								"action_data": mustMapToJSON(map[string]string{
									"action":        "danger_confirm",
									"target_action": targetAction,
									"expect_input":  confirmString,
								}),
							},
						},
					},
				},
			},
		},
	}
}

// buildDangerResultCard builds a result card after danger zone action.
func buildDangerResultCard(message string) map[string]any {
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{"wide_screen_mode": true},
		"header": map[string]any{
			"title":    map[string]any{"tag": "plain_text", "content": "记忆管理"},
			"template": "red",
		},
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":     "markdown",
					"content": message,
				},
			},
		},
	}
}

// buildMarketTabContent builds the market browsing tab with my items + marketplace.
func (f *FeishuChannel) buildMarketTabContent(ctx context.Context, senderID string, o SettingsCardOpts) []map[string]any {
	var elements []map[string]any

	if o.MySkillPage < 0 {
		o.MySkillPage = 0
	}
	if o.MyAgentPage < 0 {
		o.MyAgentPage = 0
	}
	if o.SkillMarketPage < 0 {
		o.SkillMarketPage = 0
	}
	if o.AgentMarketPage < 0 {
		o.AgentMarketPage = 0
	}

	pageState := map[string]int{
		"my_skill_page": o.MySkillPage,
		"my_agent_page": o.MyAgentPage,
		"skill_page":    o.SkillMarketPage,
		"agent_page":    o.AgentMarketPage,
	}

	// "我的" section
	if f.settingsCallbacks.RegistryListMy != nil {
		elements = append(elements, f.buildMyItemsSection(senderID, "skill", "技能", o.MySkillPage, pageState)...)
		elements = append(elements, map[string]any{"tag": "hr"})
		elements = append(elements, f.buildMyItemsSection(senderID, "agent", "代理", o.MyAgentPage, pageState)...)
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

	elements = append(elements, f.buildMarketSection("skill", "技能市场", o.SkillMarketPage, pageState)...)
	elements = append(elements, map[string]any{"tag": "hr"})
	elements = append(elements, f.buildMarketSection("agent", "代理市场", o.AgentMarketPage, pageState)...)

	log.WithField("element_count", len(elements)).Info("buildMarketTabContent completed")
	return elements
}

func (f *FeishuChannel) buildMyItemsSection(senderID, entryType, label string, page int, pageState map[string]int) []map[string]any {
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

	// Build the full list of item rows.
	var rows []map[string]any
	for _, item := range local {
		name := strings.TrimPrefix(item, prefix)
		if publishedNames[name] {
			rows = append(rows, buildItemRow(name, "✅ 已分享",
				actionBtn("📤 下架", "settings_unpublish", entryType, name, pageState),
				actionBtn("🗑️", "settings_delete_item", entryType, name, pageState),
			))
		} else {
			rows = append(rows, buildItemRow(name, "",
				actionBtn("📤 分享", "settings_publish", entryType, name, pageState),
				actionBtn("🗑️", "settings_delete_item", entryType, name, pageState),
			))
		}
	}

	for _, e := range published {
		found := false
		for _, item := range local {
			if strings.TrimPrefix(item, prefix) == e.Name {
				found = true
				break
			}
		}
		if !found && e.Sharing == "public" {
			rows = append(rows, buildItemRow(e.Name, "✅ 已分享（本地已删除）",
				actionBtn("📤 下架", "settings_unpublish", entryType, e.Name, pageState),
			))
		}
	}

	if len(rows) == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": fmt.Sprintf("_暂无%s_", label),
		})
		return elements
	}

	// Paginate.
	start := page * marketPageSize
	if start >= len(rows) {
		start = 0
		page = 0
	}
	end := start + marketPageSize
	hasNext := end < len(rows)
	if end > len(rows) {
		end = len(rows)
	}
	elements = append(elements, rows[start:end]...)

	pageKey := "my_" + entryType + "_page"
	hasPrev := page > 0
	if hasPrev || hasNext {
		elements = append(elements, buildMarketPagination(page, hasPrev, hasNext, pageKey, pageState))
	}

	return elements
}

func actionBtn(text, action, entryType, name string, pageState map[string]int) map[string]any {
	data := map[string]string{
		"action":     action,
		"entry_type": entryType,
		"name":       name,
	}
	for k, v := range pageState {
		data[k] = fmt.Sprintf("%d", v)
	}
	return map[string]any{
		"tag":  "button",
		"text": map[string]any{"tag": "plain_text", "content": text},
		"type": "default",
		"size": "small",
		"value": map[string]string{
			"action_data": mustMapToJSON(data),
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

func (f *FeishuChannel) buildMarketSection(entryType, title string, page int, pageState map[string]int) []map[string]any {
	var elements []map[string]any

	elements = append(elements, map[string]any{
		"tag":     "markdown",
		"content": fmt.Sprintf("**🏪 %s**", title),
	})

	offset := page * marketPageSize
	entries, err := f.settingsCallbacks.RegistryBrowse(entryType, marketPageSize+1, offset)
	if err != nil {
		log.WithError(err).Warnf("buildMarketSection: Browse failed for %s", entryType)
	}

	if len(entries) == 0 && page == 0 {
		elements = append(elements, map[string]any{
			"tag":     "markdown",
			"content": "_暂无公开内容_",
		})
		return elements
	}

	hasNext := len(entries) > marketPageSize
	if hasNext {
		entries = entries[:marketPageSize]
	}

	var buttons []map[string]any
	for _, entry := range entries {
		desc := entry.Name
		if entry.Description != "" {
			desc = fmt.Sprintf("%s - %s", entry.Name, entry.Description)
		}
		data := map[string]string{
			"action":     "settings_install",
			"entry_type": entryType,
			"entry_id":   fmt.Sprintf("%d", entry.ID),
		}
		for k, v := range pageState {
			data[k] = fmt.Sprintf("%d", v)
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
				"action_data": mustMapToJSON(data),
			},
		})
	}
	if len(buttons) > 0 {
		elements = append(elements, wrapButtonsInColumns(buttons))
	}

	pageKey := entryType + "_page"
	hasPrev := page > 0
	if hasPrev || hasNext {
		elements = append(elements, buildMarketPagination(page, hasPrev, hasNext, pageKey, pageState))
	}

	return elements
}

// buildMarketPagination builds the prev/next pagination row for a market section.
func buildMarketPagination(page int, hasPrev, hasNext bool, pageKey string, pageState map[string]int) map[string]any {
	var cols []map[string]any

	if hasPrev {
		prevState := copyPageState(pageState)
		prevState[pageKey] = page - 1
		cols = append(cols, map[string]any{
			"tag":            "column",
			"width":          "weighted",
			"weight":         1,
			"vertical_align": "center",
			"elements": []map[string]any{
				{
					"tag":      "interactive_container",
					"elements": []map[string]any{marketPageBtn("⬅️ 上一页", prevState)},
				},
			},
		})
	} else {
		cols = append(cols, map[string]any{
			"tag":    "column",
			"width":  "weighted",
			"weight": 1,
			"elements": []map[string]any{
				{"tag": "markdown", "content": " "},
			},
		})
	}

	cols = append(cols, map[string]any{
		"tag":            "column",
		"width":          "weighted",
		"weight":         1,
		"vertical_align": "center",
		"elements": []map[string]any{
			{"tag": "markdown", "content": fmt.Sprintf("第 %d 页", page+1), "text_align": "center"},
		},
	})

	if hasNext {
		nextState := copyPageState(pageState)
		nextState[pageKey] = page + 1
		cols = append(cols, map[string]any{
			"tag":            "column",
			"width":          "weighted",
			"weight":         1,
			"vertical_align": "center",
			"elements": []map[string]any{
				{
					"tag":      "interactive_container",
					"elements": []map[string]any{marketPageBtn("➡️ 下一页", nextState)},
				},
			},
		})
	} else {
		cols = append(cols, map[string]any{
			"tag":    "column",
			"width":  "weighted",
			"weight": 1,
			"elements": []map[string]any{
				{"tag": "markdown", "content": " "},
			},
		})
	}

	return map[string]any{
		"tag":                "column_set",
		"flex_mode":          "none",
		"horizontal_spacing": "default",
		"columns":            cols,
	}
}

func marketPageBtn(text string, pageState map[string]int) map[string]any {
	data := map[string]string{"action": "settings_market_page"}
	for k, v := range pageState {
		data[k] = fmt.Sprintf("%d", v)
	}
	return map[string]any{
		"tag":  "button",
		"text": map[string]any{"tag": "plain_text", "content": text},
		"type": "default",
		"size": "small",
		"value": map[string]string{
			"action_data": mustMapToJSON(data),
		},
	}
}

func copyPageState(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func parsePageOpts(parsed map[string]string) SettingsCardOpts {
	mySkillPage, _ := strconv.Atoi(parsed["my_skill_page"])
	myAgentPage, _ := strconv.Atoi(parsed["my_agent_page"])
	skillPage, _ := strconv.Atoi(parsed["skill_page"])
	agentPage, _ := strconv.Atoi(parsed["agent_page"])
	return SettingsCardOpts{
		MySkillPage:     mySkillPage,
		MyAgentPage:     myAgentPage,
		SkillMarketPage: skillPage,
		AgentMarketPage: agentPage,
	}
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
