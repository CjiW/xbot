package channel

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"xbot/bus"
	"xbot/version"
)

// ---------------------------------------------------------------------------
// Helper Methods
// ---------------------------------------------------------------------------

// handleTabComplete 处理 Tab 补全（§8：/ 命令补全，§8b：@ 文件路径补全）
func (m *cliModel) handleTabComplete() {
	input := m.textarea.Value()

	// 检测 @ 文件引用补全（从输入末尾检测）
	atOk, atPrefix := detectAtPrefix(input)
	if atOk {
		m.handleFileTabComplete(input, atPrefix)
		return
	}

	// / 命令补全
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return
	}

	if len(m.completions) == 0 {
		for _, cmd := range cliCommands {
			if strings.HasPrefix(cmd, trimmed) {
				m.completions = append(m.completions, cmd)
			}
		}
		if len(m.completions) == 0 {
			return
		}
		m.compIdx = 0
	} else {
		m.compIdx = (m.compIdx + 1) % len(m.completions)
	}

	m.textarea.SetValue(m.completions[m.compIdx] + " ")
}

// detectAtPrefix 检测输入文本末尾是否有 @ 触发文件补全。
// ok=true 表示检测到 @（即使后面无字符也应触发 glob）。
// prefix 是 @ 之后到文本末尾的部分。
func detectAtPrefix(input string) (ok bool, prefix string) {
	if len(input) == 0 || input[len(input)-1] == ' ' {
		return false, ""
	}
	i := len(input) - 1
	for i >= 0 && input[i] != ' ' && input[i] != '@' {
		i--
	}
	if i < 0 || input[i] != '@' {
		return false, ""
	}
	if i > 0 && input[i-1] != ' ' {
		return false, ""
	}
	return true, input[i+1:]
}

// populateFileCompletions 根据 prefix 执行 glob 搜索并填充 fileCompletions
func (m *cliModel) populateFileCompletions(prefix string) {
	pattern := prefix
	if !strings.Contains(pattern, "*") {
		if strings.HasSuffix(pattern, "/") {
			pattern += "*"
		} else {
			pattern += "*"
		}
	}
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		m.fileCompletions = nil
		m.fileCompIdx = 0
		return
	}
	// 过滤隐藏文件（以 . 开头）
	filtered := matches[:0]
	for _, f := range matches {
		base := filepath.Base(f)
		if len(base) > 0 && base[0] != '.' {
			filtered = append(filtered, f)
		}
	}
	matches = filtered
	sort.Slice(matches, func(i, j int) bool {
		di, dj := isDir(matches[i]), isDir(matches[j])
		if di != dj {
			return di
		}
		return matches[i] < matches[j]
	})
	if len(matches) > 20 {
		matches = matches[:20]
	}
	m.fileCompletions = matches
	m.fileCompIdx = 0
}

// handleFileTabComplete 处理 @ 文件路径 Tab 补全
func (m *cliModel) handleFileTabComplete(input string, prefix string) {
	if !m.fileCompActive || len(m.fileCompletions) == 0 {
		// 首次 Tab 或候选被清空：glob 并进入循环模式
		m.populateFileCompletions(prefix)
		if len(m.fileCompletions) == 0 {
			return
		}
		m.fileCompActive = true
	} else {
		// 循环模式：切换到下一个候选
		m.fileCompIdx = (m.fileCompIdx + 1) % len(m.fileCompletions)
	}

	selected := m.fileCompletions[m.fileCompIdx]
	if isDir(selected) {
		selected += "/"
	}
	atStart := len(input) - len(prefix) - 1
	newInput := input[:atStart] + "@" + selected
	m.textarea.SetValue(newInput)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// newInbound creates a bus.InboundMessage with common fields pre-filled.
// metadata can be nil.
func (m *cliModel) newInbound(content string, metadata map[string]string) bus.InboundMessage {
	return bus.InboundMessage{
		Channel:    m.channelName,
		SenderID:   m.senderID,
		ChatID:     m.chatID,
		ChatType:   "p2p",
		Content:    content,
		SenderName: "CLI User",
		Time:       time.Now(),
		RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		Metadata:   metadata,
	}
}

// appendSystem adds a system message to the message history and marks it as dirty.
func (m *cliModel) appendSystem(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      "system",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})
}

// appendSystemMarkdown adds a system message that will be rendered through
// the glamour markdown renderer (for tables, headers, etc.).
func (m *cliModel) appendSystemMarkdown(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      "system",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
		markdown:  true,
	})
}

// sendCancel sends a cancel request to the agent and adds a system notification.
func (m *cliModel) sendCancel() {
	if m.msgBus != nil {
		m.msgBus.Inbound <- m.newInbound("/cancel", nil)
	}
	m.showSystemMsg(m.locale.CancelSent, feedbackInfo)
}

// sendToAgent 发送命令到 agent，并添加用户消息到历史（§3 命令透传机制）
func (m *cliModel) sendToAgent(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})
	if m.msgBus != nil {
		m.msgBus.Inbound <- m.newInbound(content, map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional})
		m.startAgentTurn()
	}
}

// sendMessage 发送用户消息，返回可能需要执行的 tea.Cmd（如彩蛋动画 tick）。
func (m *cliModel) sendMessage(content string) tea.Cmd {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "/") {
		return m.handleSlashCommand(content)
	}

	// 🥚 彩蛋 #3: The Answer is 42 检测
	if isAnswer42(content) {
		_ = m.activateEasterEgg(easterEggAnswer42)
	}

	// 解析 @ 文件引用，提取文件路径
	media := parseFileReferences(content)

	// 添加用户消息到历史
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})

	// 更新显示并强制滚动到底部（用户发送新消息时始终可见）
	m.updateViewportContent()
	m.viewport.GotoBottom()
	m.newContentHint = false

	// 发送到消息总线
	if m.msgBus != nil {
		msg := m.newInbound(content, nil) // ReplyPolicyAuto (default)
		msg.Media = media
		m.msgBus.Inbound <- msg
		m.startAgentTurn()
	}
	return nil
}

// parseFileReferences 从用户消息中提取 @path 文件引用。
// 匹配 @ 后跟非空格字符的路径，验证文件存在后返回。
func parseFileReferences(content string) []string {
	var files []string
	seen := make(map[string]bool)
	for i := 0; i < len(content); i++ {
		if content[i] == '@' {
			// @ 必须在词首
			if i > 0 && content[i-1] != ' ' {
				continue
			}
			// 提取 @ 后的路径
			j := i + 1
			for j < len(content) && content[j] != ' ' {
				j++
			}
			path := content[i+1 : j]
			// 去掉末尾的 /
			path = strings.TrimRight(path, "/")
			if path != "" && !seen[path] {
				if _, err := os.Stat(path); err == nil {
					files = append(files, path)
					seen[path] = true
				}
			}
			i = j
		}
	}
	return files
}

// resetProgressState resets iteration tracking for a new agent turn.
func (m *cliModel) resetProgressState() {
	m.iterationHistory = nil
	m.lastSeenIteration = 0
	m.lastReasoning = ""
	m.typingStartTime = time.Now()
}

// collectAllTools gathers all tools from iteration history into a flat slice.
func (m *cliModel) collectAllTools() []CLIToolProgress {
	var all []CLIToolProgress
	for _, snap := range m.iterationHistory {
		all = append(all, snap.Tools...)
	}
	return all
}

// handleSlashCommand 处理斜杠命令
func (m *cliModel) handleSlashCommand(cmd string) tea.Cmd {
	cmd = strings.TrimSpace(cmd)
	// 提取命令部分（去掉参数）
	parts := strings.Fields(cmd)
	command := ""
	if len(parts) > 0 {
		command = strings.ToLower(parts[0])
	}

	// 🥚 彩蛋命令优先检测（隐藏命令不注册到 cliCommands）
	if handled, cmd := m.handleEasterEggCommand(cmd); handled {
		return cmd
	}

	switch command {
	// --- 本地命令 ---
	case "/cancel":
		m.sendCancel()

	case "/clear":
		m.messages = make([]cliMessage, 0, cliMsgBufSize)
		m.cachedHistory = ""
		m.exitSearch()

	case "/settings":
		// Open interactive settings panel locally
		if m.channel != nil {
			schema := m.channel.SettingsSchema()
			if len(schema) == 0 {
				m.showSystemMsg(m.locale.NoSettings, feedbackWarning)
			} else {
				// Get current values: config is the single source of truth for LLM settings.
				// Only overlay non-LLM settings from SettingsService (e.g. theme, language).
				currentValues := make(map[string]string)
				if m.channel.config.GetCurrentValues != nil {
					for k, v := range m.channel.config.GetCurrentValues() {
						currentValues[k] = v
					}
				}
				if m.channel.settingsSvc != nil {
					vals, err := m.channel.settingsSvc.GetSettings(m.channelName, m.senderID)
					if err == nil {
						for k, v := range vals {
							// Skip LLM fields — they come from config (single source of truth)
							switch k {
							case "llm_provider", "llm_model", "llm_base_url", "llm_api_key":
								continue
							}
							currentValues[k] = v
						}
					}
				}
				// Inject model list into combo options
				if m.channel.modelLister != nil {
					models := m.channel.modelLister.ListModels()
					for i, s := range schema {
						if s.Key == "llm_model" && len(models) > 0 {
							opts := make([]SettingOption, len(models))
							for j, m := range models {
								opts[j] = SettingOption{Label: m, Value: m}
							}
							schema[i].Options = opts
							break
						}
					}
				}
				m.openSettingsPanel(schema, currentValues, func(values map[string]string) {
					// Persist non-LLM settings to SettingsService (SQLite).
					// LLM settings go only to config.json (single source of truth).
					if m.channel.settingsSvc != nil {
						for k, v := range values {
							switch k {
							case "llm_provider", "llm_model", "llm_base_url", "llm_api_key":
								continue
							}
							_ = m.channel.settingsSvc.SetSetting(m.channelName, m.senderID, k, v)
						}
					}
					// Apply settings: write config.json + update runtime state
					// (LLM client rebuild, agent state updates — all non-UI work)
					if m.channel.config.ApplySettings != nil {
						m.channel.config.ApplySettings(values)
					}
					// NOTE: UI updates (theme/locale/model/viewport) are handled
					// by handleSettingsSavedMsg in Update() — do NOT call them here
					// since this callback runs in a background goroutine.
				})
			}
		}

	case "/setup":
		m.openSetupPanel()

	case "/update":
		if m.checkingUpdate {
			m.showSystemMsg(m.locale.CheckingUpdate, feedbackInfo)
		} else {
			m.checkingUpdate = true
			m.updateNotice = nil
			if m.channel != nil {
				m.channel.CheckUpdateAsync()
			}
			m.showSystemMsg(m.locale.CheckingUpdate, feedbackInfo)
		}

	case "/quit", "/exit":
		m.shouldQuit = true

	case "/help":
		helpContent := m.renderHelpPanel()
		m.showSystemMsg(helpContent, feedbackInfo)

	case "/search":
		m.enterSearchMode()

	case "/compact":
		// 保留本地处理（system 消息样式），发送到 msgBus 但不作为用户气泡
		if m.msgBus != nil {
			m.msgBus.Inbound <- m.newInbound("/compact", nil)
		}

	// --- 透传命令（发送到 agent） ---
	case "/context":
		m.sendToAgent(cmd) // 直接透传，agent 层会解析

	case "/new":
		m.sendToAgent("/new")

	case "/tasks":
		// /tasks — open unified tasks & agents panel
		taskCount := 0
		if m.bgTaskCountFn != nil {
			taskCount = m.bgTaskCountFn()
		}
		agentCnt := 0
		if m.agentCountFn != nil {
			agentCnt = m.agentCountFn()
		}
		if taskCount+agentCnt == 0 {
			m.showSystemMsg(m.locale.BgTasksEmpty, feedbackInfo)
		} else {
			m.openBgTasksPanel()
		}

	case "/su":
		// /su <userID> — 切换到指定用户身份，查看其对话历史
		// channelName 始终保持 "cli"，确保 TUI 功能（进度条、ticker、ack 等）正常
		if len(parts) < 2 {
			// 无参数：切回默认身份
			if m.senderID == "cli_user" {
				m.showSystemMsg(m.locale.SuAlreadyDefault, feedbackInfo)
				return nil
			}
			m.senderID = "cli_user"
			m.chatID = m.defaultChatID
		} else {
			newID := strings.TrimSpace(parts[1])
			if newID == "cli_user" || newID == "" {
				// 切回默认
				m.senderID = "cli_user"
				m.chatID = m.defaultChatID
			} else {
				m.senderID = newID
				m.chatID = newID
			}
		}
		// 清空当前消息列表，异步加载目标用户历史
		m.messages = nil
		m.invalidateAllCache(false)
		if m.channel != nil && m.channel.config.DynamicHistoryLoader != nil {
			m.suLoading = true
			m.splashFrame = 0
			return tea.Batch(m.splashTick(0), m.suLoadHistoryCmd())
		} else {
			m.showSystemMsg(fmt.Sprintf(m.locale.SuSwitched, m.senderID), feedbackInfo)
		}

	case "/usage":
		m.handleUsageCommand()

	default:
		// 🥚 彩蛋 #7: /version 三连检测
		if command == "/version" {
			if m.recordVersionHit() {
				art := fmt.Sprintf(versionAchievementArt, version.Version)
				_ = m.activateEasterEgg(easterEggVersion)
				m.easterEggCustom = art
				m.updateViewportContent()
				return nil
			}
		}
		// 未知命令尝试透传到 agent（agent 层可能认识）
		m.sendToAgent(cmd)
	}

	m.updateViewportContent()
	return nil
}

// handleAgentMessage 处理 agent 回复
func (m *cliModel) handleAgentMessage(msg bus.OutboundMessage) {
	turnID := m.agentTurnID // capture at entry for stale-signal guard
	content := msg.Content

	// 处理 __FEISHU_CARD__ 协议（简化显示）
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		content = ConvertFeishuCard(content)
	}

	// Empty content with no waiting user: end turn and flush queue,
	// but don't append a blank message.
	if content == "" && !msg.WaitingUser && len(msg.ToolsUsed) == 0 {
		m.streamingMsgIdx = -1
		m.progress = nil
		m.endAgentTurn(turnID)
		if turnID == m.agentTurnID {
			m.inputReady = true
			if len(m.messageQueue) > 0 {
				m.needFlushQueue = true
			}
		}
		return
	}

	if msg.IsPartial {
		// 流式输出：追加到当前消息
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// 追加到现有流式消息
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].dirty = true
		} else {
			// 创建新的流式消息
			m.streamingMsgIdx = len(m.messages)
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: true,
				dirty:     true,
			})
		}
	} else {
		// 完整消息
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// 更新流式消息为完整消息
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].isPartial = false
			m.messages[m.streamingMsgIdx].dirty = true
		} else {
			// 新增完整的 assistant 消息
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: false,
				dirty:     true,
			})
		}
		// 重置流式状态
		m.streamingMsgIdx = -1
		// 清除进度信息（保留 TODO，可跨 turn 存活）
		// Capture reasoning before clearing — needed for final iteration snapshot.
		if turnID == m.agentTurnID {
			if m.progress != nil {
				if m.progress.Reasoning != "" {
					m.lastReasoning = m.progress.Reasoning
				}
				if m.progress.Thinking != "" {
					m.lastThinking = m.progress.Thinking
				}
			}
			m.progress = nil
		}
		m.renderCacheValid = false
		m.updateViewportContent()

		// §12 AskUser panel: detect WaitingUser and open interactive panel
		if msg.WaitingUser {
			var items []askItem
			if msg.Metadata != nil {
				if qJSON := msg.Metadata["ask_questions"]; qJSON != "" {
					// Multi-question mode: parse questions array
					var qs []askQItem
					if json.Unmarshal([]byte(qJSON), &qs) == nil {
						for _, q := range qs {
							items = append(items, askItem{Question: q.Question, Options: q.Options})
						}
					}
				}
			}
			// Fallback: search message history for ❓ (legacy single-question format)
			if len(items) == 0 {
				for i := len(m.messages) - 1; i >= 0; i-- {
					if strings.HasPrefix(m.messages[i].content, "❓") {
						question := strings.TrimSpace(strings.TrimPrefix(m.messages[i].content, "❓"))
						m.messages = append(m.messages[:i], m.messages[i+1:]...)
						if question != "" {
							items = append(items, askItem{Question: question})
						}
						break
					}
				}
			}
			if len(items) > 0 {
				m.updateViewportContent()
				m.openAskUserPanel(items, func(answers map[string]string) {
					// Format answers as tool-call style message
					var parts []string
					for i, item := range items {
						key := fmt.Sprintf("q%d", i)
						ans := answers[key]
						parts = append(parts, fmt.Sprintf("Q: %s\nA: %s", item.Question, ans))
					}
					content := strings.Join(parts, "\n\n")
					// Send to agent as tool result replacement (not a new user message)
					if m.msgBus != nil {
						m.msgBus.Inbound <- m.newInbound(content, map[string]string{"ask_user_answered": "true"})
					}
					// Render as tool call style (not user message)
					m.messages = append(m.messages, cliMessage{
						role:       "tool_summary",
						content:    "AskUser",
						timestamp:  time.Now(),
						dirty:      true,
						iterations: nil,
						tools: []CLIToolProgress{
							{
								Name:    "AskUser",
								Label:   fmt.Sprintf("asked %d question(s)", len(items)),
								Status:  "completed",
								Elapsed: 0,
							},
						},
					})
					// Show answers as system message
					var answerParts []string
					for i, item := range items {
						key := fmt.Sprintf("q%d", i)
						ans := answers[key]
						answerParts = append(answerParts, fmt.Sprintf("  %s → %s", item.Question, ans))
					}
					m.showSystemMsg(strings.Join(answerParts, "\n"), feedbackInfo)
					m.startAgentTurn()
					m.updateViewportContent()
				}, func() {
					m.showSystemMsg(m.locale.AskCancelled, feedbackInfo)
					m.typing = false
					m.updatePlaceholder()
					m.inputReady = true
					m.resetProgressState()
					m.updateViewportContent()
				})
				return
			}
		}

		// Snapshot the final iteration before clearing
		if m.lastSeenIteration >= 0 && (len(m.lastCompletedTools) > 0 || m.lastReasoning != "" || m.lastThinking != "") {
			alreadySnapped := false
			for _, s := range m.iterationHistory {
				if s.Iteration == m.lastSeenIteration {
					alreadySnapped = true
					break
				}
			}
			if !alreadySnapped {
				// Filter tools by Iteration field to ensure correct attribution
				var finalTools []CLIToolProgress
				for _, t := range m.lastCompletedTools {
					if t.Iteration == m.lastSeenIteration {
						finalTools = append(finalTools, t)
					}
				}
				snap := cliIterationSnapshot{
					Iteration: m.lastSeenIteration,
					Reasoning: m.lastReasoning,
					Thinking:  m.lastThinking,
					Tools:     finalTools,
				}
				if len(finalTools) > 0 || m.lastReasoning != "" || m.lastThinking != "" {
					m.iterationHistory = append(m.iterationHistory, snap)
				}
			}
		}

		// §2 工具可视化：在 assistant 消息之前插入 tool_summary
		// Build iterations from pendingToolSummary (PhaseDone) + local iterationHistory.
		// If PhaseDone already appended a placeholder, remove it first.
		var toolSummaryIterations []cliIterationSnapshot
		if m.pendingToolSummary != nil {
			toolSummaryIterations = append(toolSummaryIterations, m.pendingToolSummary.iterations...)
			// Remove the last tool_summary placeholder that PhaseDone appended.
			// We track by index from end because append copies the value,
			// making pointer comparison unreliable.
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].role == "tool_summary" {
					m.messages = append(m.messages[:i], m.messages[i+1:]...)
					break
				}
			}
			m.pendingToolSummary = nil
		}
		if len(m.iterationHistory) > 0 {
			toolSummaryIterations = append(toolSummaryIterations, m.iterationHistory...)
		}
		if len(toolSummaryIterations) > 0 {
			toolMsg := cliMessage{
				role:       "tool_summary",
				content:    "",
				timestamp:  time.Now(),
				iterations: toolSummaryIterations,
				dirty:      true,
			}
			// Find the assistant message we just added and insert before it
			assistantIdx := len(m.messages) - 1
			if assistantIdx >= 0 && m.messages[assistantIdx].role == "assistant" {
				m.messages = append(m.messages[:assistantIdx], append([]cliMessage{toolMsg}, m.messages[assistantIdx:]...)...)
			} else {
				// Fallback: append at end
				m.messages = append(m.messages, toolMsg)
			}
			m.renderCacheValid = false
		}

		// 重置迭代追踪状态
		m.endAgentTurn(turnID)
		if turnID == m.agentTurnID {
			m.inputReady = true
			// §Q 标记需要刷新消息队列（由 Update 循环检查）
			if len(m.messageQueue) > 0 {
				m.needFlushQueue = true
			}
		}

	}

	m.updateViewportContent()
}

// renderProgressBlock renders the iteration progress panel for the viewport.
func (m *cliModel) renderProgressBlock() string {
	if !m.typing && m.progress == nil {
		return ""
	}

	bubbleWidth := m.width - 4
	innerWidth := bubbleWidth - 4 // border(2) + padding(2)

	// §20 使用缓存样式
	s := &m.styles
	iterStyle := s.ProgressIter
	thinkingStyle := s.ProgressThinking
	reasoningStyle := s.TextMutedSt // dimmed style for reasoning chain
	toolDoneStyle := s.ProgressDone
	toolRunningStyle := s.ProgressRunning
	toolErrorStyle := s.ProgressError
	elapsedStyle := s.ProgressElapsed
	indentGuide := s.ProgressIndent
	reasoningGuide := s.ProgressDim // dimmer │ for reasoning
	thinkingGuide := indentGuide    // normal │ for thinking
	reasoningW := lipgloss.Width(reasoningGuide.Render("  │ "))
	thinkingW := lipgloss.Width(thinkingGuide.Render("  │ "))
	dimStyle := s.ProgressDim

	var sb strings.Builder

	// Render completed iterations (dimmed)
	for _, snap := range m.iterationHistory {
		sb.WriteString(dimStyle.Render(iterStyle.Render(fmt.Sprintf("#%d", snap.Iteration))))
		sb.WriteString("\n")
		if snap.Reasoning != "" {
			for _, line := range strings.Split(snap.Reasoning, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-reasoningW), "\n") {
					sb.WriteString(dimStyle.Render(reasoningGuide.Render("  │ ") + reasoningStyle.Render(wl)))
					sb.WriteString("\n")
				}
			}
		}
		if snap.Thinking != "" {
			for _, line := range strings.Split(snap.Thinking, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n") {
					sb.WriteString(dimStyle.Render(thinkingGuide.Render("  │ ") + thinkingStyle.Render(wl)))
					sb.WriteString("\n")
				}
			}
		}
		for _, tool := range snap.Tools {
			label, icon, sty := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
			line := fmt.Sprintf("  │ %s %s", icon, label)
			if tool.Elapsed > 0 {
				pad := innerWidth - lipgloss.Width(line) - len(formatElapsed(tool.Elapsed))
				if pad < 1 {
					pad = 1
				}
				line += strings.Repeat(" ", pad) + elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			sb.WriteString(dimStyle.Render(sty.Render(line)))
			sb.WriteString("\n")
		}
	}

	// Render current iteration
	if m.progress != nil {
		sb.WriteString(iterStyle.Render(fmt.Sprintf("#%d", m.progress.Iteration)))
		sb.WriteString("\n")

		if m.progress.Reasoning != "" {
			for _, line := range strings.Split(m.progress.Reasoning, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-reasoningW), "\n") {
					sb.WriteString(reasoningGuide.Render("  │ ") + reasoningStyle.Render(wl))
					sb.WriteString("\n")
				}
			}
		}

		if m.progress.Thinking != "" {
			for _, line := range strings.Split(m.progress.Thinking, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				for _, wl := range strings.Split(hardWrapRunes(line, innerWidth-thinkingW), "\n") {
					sb.WriteString(thinkingGuide.Render("  │ ") + thinkingStyle.Render(wl))
					sb.WriteString("\n")
				}
			}
		}

		// Completed tools in current iteration — filter by Iteration field
		for _, tool := range m.progress.CompletedTools {
			if tool.Iteration != m.progress.Iteration {
				continue
			}
			label, icon, sty := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
			line := fmt.Sprintf("  │ %s %s", icon, label)
			if tool.Elapsed > 0 {
				pad := innerWidth - lipgloss.Width(line) - len(formatElapsed(tool.Elapsed))
				if pad < 1 {
					pad = 1
				}
				line += strings.Repeat(" ", pad) + elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			sb.WriteString(sty.Render(line))
			sb.WriteString("\n")
		}

		// Active tools — with mini pulse progress bar animation
		for _, tool := range m.progress.ActiveTools {
			if tool.Status == "done" || tool.Status == "error" {
				continue
			}
			label, _, _ := toolDisplayInfo(tool, toolDoneStyle, toolErrorStyle)
			// Mini pulse progress bar with dynamic width
			miniW := 8 + int(m.ticker.ticks%7) // dynamic width 8-14
			tick2 := int(m.ticker.ticks) % (miniW * 2)
			pos2 := tick2
			if pos2 >= miniW {
				pos2 = miniW*2 - pos2 - 1
			}
			miniBar := strings.Repeat("░", pos2) + "▓" + strings.Repeat("░", miniW-pos2-1)
			pulseIcon := m.ticker.viewFrames(pulseFrames)
			line := fmt.Sprintf("  │ %s %s  %s", pulseIcon, label, s.ProgressGradient.Render(miniBar))
			if tool.Elapsed > 0 {
				pad := innerWidth - lipgloss.Width(line) - len(formatElapsed(tool.Elapsed))
				if pad < 1 {
					pad = 1
				}
				line += strings.Repeat(" ", pad) + elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			sb.WriteString(toolRunningStyle.Render(line))
			sb.WriteString("\n")
		}

		// Phase-specific fallback when no tools are shown
		hasTools := len(m.progress.ActiveTools) > 0 || len(m.progress.CompletedTools) > 0
		if !hasTools {
			switch m.progress.Phase {
			case "thinking":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.view())
				sb.WriteString(thinkingStyle.Render(" " + m.pickVerb(m.ticker.ticks) + "..."))
				sb.WriteString("\n")
			case "compressing":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" compressing..."))
				sb.WriteString("\n")
			case "retrying":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" retrying..."))
				sb.WriteString("\n")
			}
		}

		// SubAgent tree
		if len(m.progress.SubAgents) > 0 {
			var treeSB strings.Builder
			m.renderSubAgentTree(&treeSB, m.progress.SubAgents, 1)
			if treeSB.Len() > 0 {
				sb.WriteString("\n")
				sb.WriteString(treeSB.String())
			}
		}
	} else if m.typing {
		sb.WriteString("  ")
		sb.WriteString(m.ticker.viewFrames(orbitFrames))
		sb.WriteString(thinkingStyle.Render(" " + m.pickVerb(m.ticker.ticks) + "..."))
		sb.WriteString("\n")
	}

	content := strings.TrimRight(sb.String(), "\n")
	if content == "" {
		return ""
	}

	// Total elapsed
	elapsed := ""
	if !m.typingStartTime.IsZero() {
		elapsed = " " + elapsedStyle.Render(formatElapsed(time.Since(m.typingStartTime).Milliseconds()))
	}

	// Header
	headerStyle := s.ProgressHeader
	header := headerStyle.Render("Progress") + elapsed

	// Wrap in border
	blockStyle := s.ProgressBlock.Width(bubbleWidth)

	return blockStyle.Render(header+"\n"+content) + "\n\n"
}

// renderSubAgentTree renders nested sub-agents with indentation.
// Only renders running/pending agents — completed or errored ones are already
// captured in the tool summary and shouldn't linger in the progress panel.
func (m *cliModel) renderSubAgentTree(sb *strings.Builder, agents []CLISubAgent, depth int) {
	for i, sa := range agents {
		if sa.Status == "done" || sa.Status == "error" {
			continue
		}
		// §22 树状连接线
		prefix := ""
		if depth == 1 {
			if i == len(agents)-1 {
				prefix = "└── "
			} else {
				prefix = "├── "
			}
		} else {
			indent := strings.Repeat("│   ", depth-1)
			if i == len(agents)-1 {
				prefix = indent + "└── "
			} else {
				prefix = indent + "├── "
			}
		}
		icon := m.ticker.viewFrames(waveFrames)
		style := m.styles.ProgressRunning
		switch sa.Status {
		case "error":
			icon = "✗"
			style = m.styles.ProgressError
		}
		line := fmt.Sprintf("%s%s %s", prefix, icon, sa.Role)
		if sa.Desc != "" {
			line += ": " + sa.Desc
		}
		sb.WriteString(style.Render(line))
		sb.WriteString("\n")
		if len(sa.Children) > 0 {
			m.renderSubAgentTree(sb, sa.Children, depth+1)
		}
	}
}

// renderHelpPanel 渲染格式化的帮助面板（第 4 轮）。
// 使用 lipgloss 边框 + 分组布局 + 状态图标，替代纯文本。
func (m *cliModel) renderHelpPanel() string {
	contentWidth := m.width - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	// §20 使用缓存样式
	s := &m.styles
	titleStyle := s.HelpTitle
	cmdStyle := s.HelpCmd
	descStyle := s.HelpDesc
	groupStyle := s.HelpGroup
	keyStyle := s.HelpKey
	panelStyle := s.HelpPanel.Width(contentWidth)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render(m.locale.HelpTitle))
	sb.WriteString("\n")

	sb.WriteString(groupStyle.Render(m.locale.HelpCommandsTitle))
	sb.WriteString("\n")
	for _, c := range m.locale.HelpCmds {
		sb.WriteString("  " + cmdStyle.Render(c.Cmd) + " " + descStyle.Render(c.Desc))
		sb.WriteString("\n")
	}

	sb.WriteString(groupStyle.Render(m.locale.HelpShortcutsTitle))
	sb.WriteString("\n")
	for _, k := range m.locale.HelpKeys {
		sb.WriteString("  " + keyStyle.Render(k.Key) + " " + descStyle.Render(k.Desc))
		sb.WriteString("\n")
	}

	return panelStyle.Render(sb.String())
}

// renderMessage 渲染单条消息为 ANSI 字符串（§1 增量渲染：自包含方法）
// toolDisplayInfo 从工具进度条目中提取显示用的 label、状态图标和样式。
func toolDisplayInfo(tool CLIToolProgress, okStyle, errStyle lipgloss.Style) (label, icon string, sty lipgloss.Style) {
	if tool.Label == "" {
		label = tool.Name
	} else {
		label = tool.Label
	}
	icon = "✓"
	sty = okStyle
	if tool.Status == "error" {
		icon = "✗"
		sty = errStyle
	}
	return
}

func (m *cliModel) renderMessage(msg *cliMessage) string {
	// §20 使用缓存样式
	s := &m.styles
	var sb strings.Builder
	contentWidth := m.width - 4 // 留边距
	timeStyle := s.Time
	userLabelStyle := s.UserLabel
	assistantLabelStyle := s.AssistLabel
	streamingLabelStyle := s.StreamingLabel
	systemMsgStyle := s.SystemMsg
	errorMsgStyle := s.ErrorMsg

	// 渲染 Markdown（assistant 消息 + 带 markdown 标记的 system 消息）
	var rendered string
	if msg.role == "assistant" || (msg.role == "system" && msg.markdown) {
		// Pre-process: render mermaid code blocks to ASCII art
		// Truncate to glamour wrap width to prevent wrapping.
		preprocessed := msg.content
		if msg.role == "assistant" {
			preprocessed = renderMermaidBlocks(msg.content, m.width-4)
		}
		var err error
		rendered, err = m.renderer.Render(preprocessed)
		if err != nil {
			rendered = msg.content
		}
		rendered = strings.TrimSpace(rendered)
	} else {
		rendered = msg.content
	}

	timeStr := timeStyle.Render(msg.timestamp.Format("15:04:05"))

	switch msg.role {
	case "tool_summary":
		// §20 使用缓存样式
		toolSummaryStyle := s.ToolSummary
		toolHeaderStyle := s.ToolHeader
		toolItemStyle := s.ToolItem
		toolErrorItemStyle := s.ToolErrorItem
		thinkingStyle := s.ProgressThinking
		reasoningStyle := s.TextMutedSt
		reasoningGuide := s.ProgressDim
		thinkingGuide := s.ProgressIndent
		hintStyle := s.ToolHint

		// 统计总工具数和总耗时
		allTools, iterCount := msg.iterToolsFlat()
		totalTools := len(allTools)
		totalMs := int64(0)
		for _, tool := range allTools {
			totalMs += tool.Elapsed
		}

		var toolSb strings.Builder

		if m.toolSummaryExpanded {
			// 展开模式：完整渲染
			if iterCount > 0 {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d iterations, %d calls)", iterCount, totalTools)))
				toolSb.WriteString("\n")
				// Box internal width: ToolSummary has Border(2) + Padding(0,1 → 2) = 4 cols overhead
				boxInnerW := contentWidth - 4
				guideW := lipgloss.Width(s.ProgressIndent.Render("  │ "))
				textW := boxInnerW - guideW
				for _, it := range msg.iterations {
					// Render #iter header
					iterLabel := s.ProgressIter.Render(fmt.Sprintf("#%d", it.Iteration))
					toolSb.WriteString(iterLabel)
					toolSb.WriteString("\n")
					if it.Reasoning != "" {
						for _, line := range strings.Split(it.Reasoning, "\n") {
							line = strings.TrimSpace(line)
							if line == "" {
								continue
							}
							for _, wl := range strings.Split(hardWrapRunes(line, textW), "\n") {
								toolSb.WriteString(reasoningGuide.Render("  │ ") + reasoningStyle.Render(wl))
								toolSb.WriteString("\n")
							}
						}
					}
					if it.Thinking != "" {
						for _, line := range strings.Split(it.Thinking, "\n") {
							line = strings.TrimSpace(line)
							if line == "" {
								continue
							}
							for _, wl := range strings.Split(hardWrapRunes(line, textW), "\n") {
								toolSb.WriteString(thinkingGuide.Render("  │ ") + thinkingStyle.Render(wl))
								toolSb.WriteString("\n")
							}
						}
					}
					for _, tool := range it.Tools {
						label, icon, sty := toolDisplayInfo(tool, toolItemStyle, toolErrorItemStyle)
						elapsed := ""
						if tool.Elapsed > 0 {
							elapsed = fmt.Sprintf(" (%dms)", tool.Elapsed)
						}
						toolSb.WriteString(sty.Render(fmt.Sprintf("    %s %s%s", icon, label, elapsed)))
						toolSb.WriteString("\n")
					}
				}
			} else {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d)", totalTools)))
				toolSb.WriteString("\n")
				for _, tool := range msg.tools {
					label, icon, sty := toolDisplayInfo(tool, toolItemStyle, toolErrorItemStyle)
					elapsed := ""
					if tool.Elapsed > 0 {
						elapsed = fmt.Sprintf(" (%dms)", tool.Elapsed)
					}
					toolSb.WriteString(sty.Render(fmt.Sprintf("  %s %s%s", icon, label, elapsed)))
					toolSb.WriteString("\n")
				}
			}
		} else {
			// 折叠模式升级（第 4 轮）：统计摘要 + 成功/失败状态图标
			elapsedStr := formatElapsed(totalMs)
			// 统计成功/失败工具数
			successCount, errorCount := 0, 0
			for _, tool := range allTools {
				if tool.Status == "error" {
					errorCount++
				} else {
					successCount++
				}
			}
			var statusIcons string
			if errorCount > 0 {
				statusIcons = s.ProgressError.Render("✗") +
					s.TextMutedSt.Render(fmt.Sprintf("%d", errorCount))
			}
			if successCount > 0 && errorCount > 0 {
				statusIcons += " "
			}
			if successCount > 0 {
				statusIcons += s.ProgressDone.Render("✓") +
					s.TextMutedSt.Render(fmt.Sprintf("%d", successCount))
			}
			toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools %d calls · %s", totalTools, elapsedStr)))
			if statusIcons != "" {
				toolSb.WriteString("  ")
				toolSb.WriteString(statusIcons)
			}
			toolSb.WriteString("  ")
			toolSb.WriteString(hintStyle.Render("[Ctrl+O]"))
		}
		sb.WriteString(toolSummaryStyle.Render(toolSb.String()))
	case "system":
		if msg.markdown {
			// Markdown system messages (e.g. /usage tables): use glamour-rendered output directly
			sb.WriteString(rendered)
		} else if isErrorContent(msg.content) {
			sb.WriteString(errorMsgStyle.Render("⚠ " + msg.content))
		} else {
			sb.WriteString(systemMsgStyle.Render(msg.content))
		}
	case "user":
		// 用户消息上方：右侧柔和光点分隔，与 assistant 的左侧竖线形成对称
		dotSep := s.UserDotSep.Width(contentWidth).Align(lipgloss.Right).Render("···")
		sb.WriteString(dotSep)
		sb.WriteString("\n")
		label := userLabelStyle.Render("You")
		header := s.UserHeader.Width(contentWidth).Align(lipgloss.Right).Render(fmt.Sprintf("%s %s", timeStr, label))
		sb.WriteString(header)
		sb.WriteString("\n")
		// 用户消息：右对齐气泡效果
		// 计算内容最大行宽，整块右对齐而非每行拉伸
		lines := strings.Split(rendered, "\n")
		maxWidth := 0
		for _, line := range lines {
			w := lipgloss.Width(line)
			if w > maxWidth {
				maxWidth = w
			}
		}
		maxBubble := contentWidth * 3 / 4
		userStyle := s.UserContent
		if maxWidth <= maxBubble {
			// 内容够窄，左填充实现气泡靠右
			userStyle = s.UserContent.PaddingLeft(contentWidth - maxWidth)
		}
		// 内容超宽时退回左对齐，避免终端折行后跑到最左边
		sb.WriteString(userStyle.Render(rendered))
	default:
		// assistant 消息：左侧竖线引导 + 标签
		guide := s.AssistantGuide.Render("│")
		if msg.isPartial {
			guide = s.WarningSt.Render("│")
			label := streamingLabelStyle.Render("Assistant")
			fmt.Fprintf(&sb, "%s %s %s ...", guide, timeStr, label)
		} else {
			label := assistantLabelStyle.Render("Assistant")
			fmt.Fprintf(&sb, "%s %s %s", guide, timeStr, label)
		}
		sb.WriteString("\n")
		// §19 长消息折叠：对已完成的 assistant 消息截取预览
		if msg.folded && !msg.isPartial {
			origLines := msg.originalRenderedLines
			if origLines == 0 {
				origLines = msg.renderedLines
			}
			if origLines > msgFoldThresholdLines {
				renderedLines := strings.Split(rendered, "\n")
				if len(renderedLines) > msgFoldPreviewLines {
					rendered = strings.Join(renderedLines[:msgFoldPreviewLines], "\n")
					foldHint := m.styles.TextMutedSt.Render(
						fmt.Sprintf("  ... %s (%d lines) ...",
							m.locale.MsgCollapsed, origLines))
					rendered += "\n" + foldHint
				}
			}
		}
		// Agent 消息直接渲染（glamour 已处理 markdown）
		sb.WriteString(rendered)
		// 流式输出时追加闪烁光标，让用户感知"正在生成"
		if msg.isPartial && rendered != "" {
			sb.WriteString(s.StreamCursor.Render("▋"))
		}
	}

	sb.WriteString("\n\n")

	// §19 计算渲染后行数（每次 dirty 重算）
	msg.renderedLines = strings.Count(sb.String(), "\n") + 1

	return sb.String()
}

// setViewportContentForScroll is like setViewportContent but skips GotoBottom(),
// allowing the caller to set a precise YOffset afterwards (e.g. for Ctrl+K red line).
func (m *cliModel) setViewportContentForScroll(content string) {
	if m.width > 0 {
		lines := strings.Split(content, "\n")
		var wrapped []string
		for _, line := range lines {
			line = strings.TrimRight(line, " \t")
			wrapped = append(wrapped, strings.Split(hardWrapRunes(line, m.width), "\n")...)
		}
		content = strings.Join(wrapped, "\n")
	}
	m.viewport.SetContent(content)
	m.newContentHint = false
}

// setViewportContent sets viewport content while preserving scroll position.
// If the user was at the bottom before the update, keep them at the bottom.
// Lines wider than the viewport are truncated to prevent layout breakage.
func (m *cliModel) setViewportContent(content string) {
	if m.width > 0 {
		lines := strings.Split(content, "\n")
		var wrapped []string
		for _, line := range lines {
			// Strip trailing whitespace first — mermaid-ascii and wide tables
			// pad lines with spaces that inflate lipgloss.Width() far beyond
			// the actual visible content, causing premature wrapping.
			line = strings.TrimRight(line, " \t")
			wrapped = append(wrapped, strings.Split(hardWrapRunes(line, m.width), "\n")...)
		}
		content = strings.Join(wrapped, "\n")
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(content)
	if atBottom {
		m.viewport.GotoBottom()
		m.newContentHint = false
	} else {
		m.newContentHint = true
	}
}

// wrappedLineCount returns the number of viewport display lines after hard-wrapping.
// The logic mirrors setViewportContent exactly so that msgLineOffsets (computed via
// this function) are always in sync with the viewport's internal line numbering.
func wrappedLineCount(content string, width int) int {
	if content == "" {
		return 0
	}
	if width <= 0 {
		return strings.Count(content, "\n")
	}
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, " \t")
		count += strings.Count(hardWrapRunes(line, width), "\n") + 1
	}
	return count
}

// renderDeleteBoundaryLine 渲染 Ctrl+K 删除边界红线。
func (m *cliModel) renderDeleteBoundaryLine() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	redStyle := m.styles.ProgressError // §20
	label := " ✂ delete below "
	// label 的可见宽度（不含 ANSI 转义）
	labelWidth := lipgloss.Width(redStyle.Bold(true).Render(label))
	totalPad := w - labelWidth
	if totalPad < 0 {
		totalPad = 0
	}
	leftPad := totalPad / 2
	rightPad := totalPad - leftPad
	line := redStyle.Bold(true).Render(
		strings.Repeat("━", leftPad) + label + strings.Repeat("━", rightPad),
	)
	return "\n" + line + "\n"
}

// visibleTurnIndices 返回每个"对话轮次"的起始 slice 索引。
// 每个 turn 以 user 消息开头，包含之后所有的 assistant/tool_summary 消息
// 直到下一个 user 消息为止。tool_summary 自动归属其前面最近的 user 所在的 turn。
//
// 例如: [user(0), assistant(1), tool_summary(2), user(3), assistant(4)]
// turns: [0, 3] — 按"1"删最后 1 轮即 cutIdx=3，保留 [user(0), assistant(1), tool_summary(2)]
func visibleTurnIndices(messages []cliMessage) []int {
	var turns []int
	for i, msg := range messages {
		if msg.role == "user" {
			turns = append(turns, i)
		}
	}
	// 如果没有 user 消息但有其他消息，回退到旧逻辑（保留兼容）
	if len(turns) == 0 && len(messages) > 0 {
		turns = append(turns, 0)
	}
	return turns
}

// visibleMsgGroupIndices 是 visibleTurnIndices 的别名，保留向后兼容。
func visibleMsgGroupIndices(messages []cliMessage) []int {
	return visibleTurnIndices(messages)
}

// updateViewportContent 更新 viewport 显示内容（§1 增量渲染）
func (m *cliModel) updateViewportContent() {
	// 快速路径：流式消息 + 缓存有效
	if m.streamingMsgIdx >= 0 && m.renderCacheValid {
		m.updateStreamingOnly()
		return
	}

	// 快速路径：缓存有效 + 无流式消息 + 消息数未变，只刷新 progress block（tick 场景）
	if m.renderCacheValid && m.streamingMsgIdx < 0 && m.cachedMsgCount == len(m.messages) {
		var sb strings.Builder
		sb.WriteString(m.cachedHistory)
		sb.WriteString(m.renderProgressBlock())
		m.setViewportContent(sb.String())
		return
	}

	// 快速路径：缓存有效 + 仅追加了新消息（无流式、无搜索、无删除确认）
	// 只渲染新增的 dirty 消息并追加到 cachedHistory，跳过全量重建。
	if m.renderCacheValid && m.streamingMsgIdx < 0 && !m.searchMode && m.confirmDelete == 0 &&
		len(m.messages) > m.cachedMsgCount {
		m.appendNewMessagesToCache()
		return
	}

	// 慢速路径：全量重建
	m.fullRebuild()
}

// updateStreamingOnly 只重新渲染当前流式消息（快速路径）
func (m *cliModel) updateStreamingOnly() {
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)

	// 只渲染当前流式消息
	msg := &m.messages[m.streamingMsgIdx]
	msg.dirty = true
	sb.WriteString(m.renderMessage(msg))

	// Append progress block
	sb.WriteString(m.renderProgressBlock())

	m.setViewportContent(sb.String())
}

// appendNewMessagesToCache incrementally renders and appends only the new messages
// since cachedMsgCount, updating cachedHistory and msgLineOffsets without rebuilding
// old messages. This is O(new_messages) instead of O(all_messages).
func (m *cliModel) appendNewMessagesToCache() {
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)

	// Calculate starting line offset for new messages
	runningLines := 0
	if len(m.msgLineOffsets) > 0 {
		// Approximate: use the line count of cachedHistory at current width.
		// This is an estimate but sufficient for msgLineOffsets (used for Ctrl+E folding).
		runningLines = wrappedLineCount(m.cachedHistory, m.width)
	}

	startIdx := m.cachedMsgCount
	for i := startIdx; i < len(m.messages); i++ {
		msg := &m.messages[i]
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		rendered := m.renderMessage(msg)
		msg.rendered = rendered
		msg.dirty = false
		msg.renderWidth = m.width
		sb.WriteString(rendered)
		runningLines += wrappedLineCount(rendered, m.width)
	}

	m.cachedHistory = sb.String()
	m.renderCacheValid = true
	m.cachedMsgCount = len(m.messages)

	// Set viewport with new content + progress block
	var vp strings.Builder
	vp.WriteString(m.cachedHistory)
	vp.WriteString(m.renderProgressBlock())
	m.setViewportContent(vp.String())
}

// fullRebuild 全量重建渲染缓存（慢速路径）
func (m *cliModel) fullRebuild() {
	var historyBuf strings.Builder

	// splitIdx 确保当前流式消息不进入 cachedHistory
	splitIdx := len(m.messages)
	if m.streamingMsgIdx >= 0 {
		splitIdx = m.streamingMsgIdx
	}

	// §9 Ctrl+K 红线：根据可见消息组计算删除边界 slice 索引
	var redLineInsertIdx = -1
	if m.confirmDelete > 0 {
		groups := visibleMsgGroupIndices(m.messages)
		if m.confirmDelete <= len(groups) {
			redLineInsertIdx = groups[len(groups)-m.confirmDelete] - 1
		}
	}

	// §19 重置消息行号偏移（基于折行后的 viewport 行号）
	m.msgLineOffsets = m.msgLineOffsets[:0]
	runningLines := 0
	// §9 Ctrl+K 红线：记录红线在折行后的 viewport 行号
	var redLineWrappedPos = -1
	for i := range m.messages[:splitIdx] {
		// §19 记录消息在 viewport 折行后内容中的起始行号
		m.msgLineOffsets = append(m.msgLineOffsets, runningLines)
		needsRender := m.messages[i].dirty || m.messages[i].renderWidth != m.width
		if needsRender {
			rendered := m.renderMessage(&m.messages[i])
			m.messages[i].rendered = rendered
			m.messages[i].dirty = false
			m.messages[i].renderWidth = m.width
		}
		// Build per-message chunk for line counting (avoids calling
		// historyBuf.String() on every iteration — the O(N²) full
		// buffer copy caused 100% CPU during resize with many messages).
		chunk := m.messages[i].rendered
		// §21 搜索高亮：匹配消息前插入指示条
		if m.searchMode && m.isSearchMatch(i) {
			indicator := m.styles.SearchIndicator.Render("▸ ")
			historyBuf.WriteString(indicator)
			chunk = indicator + chunk
		}
		historyBuf.WriteString(m.messages[i].rendered)
		// §9 Ctrl+K 红线：在删除边界处插入红线指示器
		if redLineInsertIdx >= 0 && i == redLineInsertIdx {
			boundary := m.renderDeleteBoundaryLine()
			redLineWrappedPos = runningLines + wrappedLineCount(chunk+"\n"+boundary, m.width)
			historyBuf.WriteString(boundary)
		}
		// 累加本消息（含搜索指示条/红线）在折行后占用的行数
		runningLines += wrappedLineCount(chunk, m.width)
	}

	m.cachedHistory = historyBuf.String()
	m.renderCacheValid = true
	m.cachedMsgCount = len(m.messages)

	// 拼接最终内容：历史 + 当前流式消息（如有） + progress block
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)
	if m.streamingMsgIdx >= 0 {
		sb.WriteString(m.renderMessage(&m.messages[m.streamingMsgIdx]))
	}
	sb.WriteString(m.renderProgressBlock())

	// §9 Ctrl+K 红线：设置内容时禁止 GotoBottom，以便随后精确定位红线
	if m.confirmDelete > 0 {
		m.setViewportContentForScroll(sb.String())
	} else {
		m.setViewportContent(sb.String())
	}

	// §9 Ctrl+K 红线：自动滚动到红线位置（使用折行后的精确行号）
	if m.confirmDelete > 0 && redLineWrappedPos >= 0 {
		vpHeight := m.viewport.Height()
		totalLines := m.viewport.TotalLineCount()
		// 将红线定位到视口中央偏上（留 3 行上方边距）
		targetY := redLineWrappedPos - 3
		if targetY < 0 {
			targetY = 0
		}
		maxOff := totalLines - vpHeight
		if maxOff < 0 {
			maxOff = 0
		}
		if targetY > maxOff {
			targetY = maxOff
		}
		m.viewport.SetYOffset(targetY)
	}
}

// isSearchMatch 检查消息是否匹配当前搜索（§21）
func (m *cliModel) isSearchMatch(idx int) bool {
	for _, si := range m.searchResults {
		if si == idx {
			return true
		}
	}
	return false
}

// toggleMessageFold 批量切换所有 assistant 消息的折叠状态（§19）
// 如果当前有任一长消息未折叠 → 全部折叠；否则 → 全部展开。
func (m *cliModel) toggleMessageFold() {
	if len(m.messages) == 0 {
		return
	}
	// 决定目标状态：如果存在任何未折叠的长消息，则全部折叠
	anyUnfolded := false
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.role == "assistant" && !msg.isPartial && !msg.folded {
			lines := msg.originalRenderedLines
			if lines == 0 {
				lines = msg.renderedLines
			}
			if lines > msgFoldThresholdLines {
				anyUnfolded = true
				break
			}
		}
	}
	targetFold := anyUnfolded

	changed := false
	for i := range m.messages {
		msg := &m.messages[i]
		if msg.role != "assistant" || msg.isPartial {
			continue
		}
		if !targetFold {
			// Unfolding: skip threshold — renderedLines reflects folded preview,
			// not original length. Only unfold messages that are currently folded.
			if !msg.folded {
				continue
			}
			msg.folded = false
			msg.dirty = true
			changed = true
			continue
		}
		// Folding: check threshold using original line count
		lines := msg.originalRenderedLines
		if lines == 0 {
			lines = msg.renderedLines
		}
		if lines <= msgFoldThresholdLines {
			continue
		}
		if !msg.folded {
			msg.folded = true
			msg.originalRenderedLines = msg.renderedLines
			msg.dirty = true
			changed = true
		}
	}
	if changed {
		m.renderCacheValid = false
		m.updateViewportContent()
	}
}

// enterSearchMode 进入搜索模式（§21）
func (m *cliModel) enterSearchMode() {
	ti := textinput.New()
	ti.Placeholder = m.locale.SearchPlaceholder
	ti.Prompt = "/"
	ti.CharLimit = 100
	ti.Focus()
	w := m.width - 20
	if w < 20 {
		w = 20
	}
	ti.SetWidth(w)
	m.searchTI = ti
	m.searchMode = true
	m.searchEditing = true
	m.searchQuery = ""
	m.searchResults = nil
	m.searchIdx = -1
	m.renderCacheValid = false
	m.updateViewportContent()
}

// executeSearch 执行搜索（§21）
func (m *cliModel) executeSearch() {
	query := strings.TrimSpace(m.searchTI.Value())
	if query == "" {
		m.exitSearch()
		return
	}
	m.searchQuery = query
	lower := strings.ToLower(query)
	m.searchResults = nil
	for i, msg := range m.messages {
		if msg.role == "system" {
			continue
		}
		if strings.Contains(strings.ToLower(msg.content), lower) {
			m.searchResults = append(m.searchResults, i)
		}
	}
	m.searchIdx = -1
	m.searchEditing = false
	if len(m.searchResults) == 0 {
		m.showSystemMsg(m.locale.SearchNoResults, feedbackInfo)
	} else {
		m.showSystemMsg(fmt.Sprintf(m.locale.SearchResults, len(m.searchResults)), feedbackInfo)
		m.jumpToSearchResult(0)
	}
	m.renderCacheValid = false
	m.updateViewportContent()
}

// exitSearch 退出搜索模式（§21）
func (m *cliModel) exitSearch() {
	m.searchMode = false
	m.searchQuery = ""
	m.searchResults = nil
	m.searchIdx = -1
	m.searchEditing = false
	m.renderCacheValid = false
	m.updateViewportContent()
}

// jumpToSearchResult 跳转到指定搜索结果（§21）
func (m *cliModel) jumpToSearchResult(idx int) {
	if idx < 0 || idx >= len(m.searchResults) {
		return
	}
	m.searchIdx = idx
	msgIdx := m.searchResults[idx]
	if msgIdx < len(m.msgLineOffsets) {
		m.viewport.SetYOffset(m.msgLineOffsets[msgIdx])
	}
}

// // tickCmd returns a command that periodically refreshes viewport during agent processing.
func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return cliTickMsg{}
	})
}

// idleTickCmd returns a low-frequency tick (3s) for placeholder rotation in idle state.
func idleTickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg {
		return idleTickMsg{}
	})
}

// tickerCmd is deprecated — ticker is now driven by cliTickMsg.
// Kept for reference only.
// func tickerCmd() tea.Cmd {
// 	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
// 		return tickerTickMsg{}
// 	})
// }
