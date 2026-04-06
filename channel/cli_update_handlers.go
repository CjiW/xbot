package channel

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	log "xbot/logger"
)

// handleKeyPress processes key press events in the main update loop.
// Returns (model, cmds, handled). If handled is true, the caller should return
// immediately; otherwise, post-switch processing (viewport/textarea update) should continue.
func (m *cliModel) handleKeyPress(msg tea.KeyPressMsg, wasTyping bool) (tea.Model, []tea.Cmd, bool) {
	var cmds []tea.Cmd

	// §9 Ctrl+K 确认模式：必须在 switch msg.Code 之前拦截所有按键
	if m.confirmDelete > 0 {
		groups := visibleMsgGroupIndices(m.messages)
		switch msg.String() {
		case "y", "Y":
			// 确认删除：根据 turn 索引截断
			if m.confirmDelete > len(groups) {
				m.confirmDelete = len(groups)
			}
			cutIdx := groups[len(groups)-m.confirmDelete]
			m.messages = m.messages[:cutIdx]
			// 同步截断数据库中的 session messages（异步避免阻塞 UI）
			// safe: 此时 typing=false，输入被 confirmDelete 拦截，不会有并发写入
			if m.trimHistoryFn != nil {
				keepCount := cutIdx
				go func() {
					if err := m.trimHistoryFn(keepCount); err != nil {
						log.WithError(err).Warn("Failed to trim session history after Ctrl+K")
					}
				}()
			}
			m.confirmDelete = 0
			m.renderCacheValid = false
			m.cachedHistory = ""
			m.updateViewportContent()
			return m, nil, true
		case "n", "N":
			// 取消删除
			m.confirmDelete = 0
			m.renderCacheValid = false
			m.updateViewportContent()
			return m, nil, true
		default:
			// 检查数字键（调整删除数量）
			if len(msg.Text) > 0 {
				if len(msg.Text) == 1 && msg.Text[0] >= '1' && msg.Text[0] <= '9' {
					newDel := int(msg.Text[0] - '0')
					if newDel > len(groups) {
						newDel = len(groups)
					}
					m.confirmDelete = newDel
					m.renderCacheValid = false
					m.updateViewportContent()
					return m, nil, true
				}
			}
			// 其他键也取消（包括 Esc）
			m.confirmDelete = 0
			m.renderCacheValid = false
			m.updateViewportContent()
			return m, nil, true
		}
	}

	// 🥚 彩蛋覆盖层激活时，按任意键退出（Ctrl+C 除外，已在上面处理）
	if m.easterEgg != easterEggNone {
		return m, []tea.Cmd{func() tea.Msg { return easterEggDoneMsg{} }}, true
	}

	// 🥚 Konami Code 彩蛋：监听方向键和字母键
	if m.easterEgg == easterEggNone {
		konamiKey := ""
		switch msg.Code {
		case tea.KeyUp:
			konamiKey = "up"
		case tea.KeyDown:
			konamiKey = "down"
		case tea.KeyLeft:
			konamiKey = "left"
		case tea.KeyRight:
			konamiKey = "right"
		}
		// 检测字母键 B 和 A
		if len(msg.Text) == 1 {
			switch msg.Text[0] {
			case 'b', 'B':
				konamiKey = "b"
			case 'a', 'A':
				konamiKey = "a"
			}
		}
		if konamiKey != "" && m.checkKonami(konamiKey) {
			// Konami Code 完整序列匹配！
			cmd := m.activateEasterEgg(easterEggKonami)
			return m, []tea.Cmd{cmd}, true
		}
	}

	switch {
	case msg.String() == "ctrl+c":
		// Ctrl+C：有迭代时中止；无迭代时清空输入
		if m.typing {
			// 如果正在编辑排队消息，先取消编辑
			if m.queueEditing {
				m.queueEditing = false
				m.queueEditBuf = ""
				m.textarea.SetValue("")
				return m, nil, true
			}
			m.sendCancel()
			return m, cmds, true
		}
		// 非处理状态：清空输入
		if m.textarea.Value() != "" {
			m.textarea.Reset()
			m.inputHistoryIdx = -1
			m.inputDraft = ""
			m.autoExpandInput()
		}
		return m, nil, true

	case msg.Code == tea.KeyEsc:
		// Esc：非处理状态清空输入；处理中时取消排队编辑或清空输入
		if m.queueEditing {
			m.queueEditing = false
			m.queueEditBuf = ""
			m.textarea.SetValue("")
			return m, nil, true
		}
		if !m.typing {
			if m.textarea.Value() != "" {
				m.textarea.Reset()
				m.inputHistoryIdx = -1
				m.inputDraft = ""
				m.autoExpandInput()
			}
		}
		return m, nil, true

	case msg.Text == "^":
		if m.panelMode == "" && m.bgTaskCount > 0 && m.inputHistoryIdx == -1 {
			m.openBgTasksPanel()
			return m, nil, true
		}

	case msg.Code == tea.KeyUp:
		// §Q 消息队列：typing 时 ↑ 追回最后一条排队消息编辑
		if m.panelMode == "" && m.typing && !m.inputReady && len(m.messageQueue) > 0 {
			if !m.queueEditing && m.textarea.Value() == "" {
				// 追回最后一条排队消息
				m.queueEditing = true
				m.queueEditBuf = m.messageQueue[len(m.messageQueue)-1]
				m.textarea.SetValue(m.queueEditBuf)
				m.autoExpandInput()
				return m, nil, true
			}
		}
		if m.panelMode == "" && !m.typing {
			// 空输入时浏览历史（仅空输入触发，避免破坏 textarea 多行编辑）
			if m.textarea.Value() == "" && len(m.inputHistory) > 0 {
				if m.inputHistoryIdx == -1 {
					m.inputDraft = "" // 保存空草稿
					m.inputHistoryIdx = 0
				} else if m.inputHistoryIdx < len(m.inputHistory)-1 {
					m.inputHistoryIdx++
				}
				m.textarea.SetValue(m.inputHistory[m.inputHistoryIdx])
				m.autoExpandInput()
				return m, nil, true
			}
		}

	case msg.Code == tea.KeyDown:
		if m.panelMode == "" && !m.typing && m.inputHistoryIdx >= 0 {
			if m.inputHistoryIdx > 0 {
				m.inputHistoryIdx--
				m.textarea.SetValue(m.inputHistory[m.inputHistoryIdx])
			} else {
				m.inputHistoryIdx = -1
				m.textarea.SetValue(m.inputDraft)
			}
			m.autoExpandInput()
			return m, nil, true
		}

	case msg.Code == tea.KeyEnter:
		// Enter 发送消息
		if !m.inputReady {
			// §Q 消息队列：typing 期间允许排队消息
			if m.queueEditing {
				// 正在编辑排队消息 → 保存编辑结果
				m.messageQueue[len(m.messageQueue)-1] = m.textarea.Value()
				m.queueEditing = false
				m.queueEditBuf = ""
				m.textarea.SetValue("")
				return m, nil, true
			}
			if m.textarea.Value() != "" {
				m.messageQueue = append(m.messageQueue, m.textarea.Value())
				m.textarea.SetValue("")
				// 显示队列提示
				if len(m.messageQueue) == 1 {
					m.showTempStatus(fmt.Sprintf(m.locale.MessageQueuedUp, len(m.messageQueue)))
				} else {
					m.showTempStatus(fmt.Sprintf(m.locale.MessageQueued, len(m.messageQueue)))
				}
				return m, []tea.Cmd{m.clearTempStatusCmd()}, true
			}
			return m, nil, true
		}
		// §8b @ 模式：Enter 进入目录或确认文件
		if m.fileCompActive && len(m.fileCompletions) > 0 {
			selected := m.fileCompletions[m.fileCompIdx]
			input := m.textarea.Value()
			_, prefix := detectAtPrefix(input)
			atStart := len(input) - len(prefix) - 1
			if isDir(selected) {
				newInput := input[:atStart] + "@" + selected + "/"
				m.textarea.SetValue(newInput)
				m.fileCompActive = false
				m.populateFileCompletions(selected + "/")
			} else {
				newInput := input[:atStart] + "@" + selected + " "
				m.textarea.SetValue(newInput)
				m.fileCompActive = false
				m.fileCompletions = nil
				m.fileCompIdx = 0
			}
			return m, nil, true
		}
		content := strings.TrimSpace(m.textarea.Value())
		if content != "" {
			// §22 输入历史：保存发送的内容（去重，不保存 / 命令和空输入）
			if !strings.HasPrefix(content, "/") {
				if len(m.inputHistory) == 0 || m.inputHistory[0] != content {
					m.inputHistory = append([]string{content}, m.inputHistory...)
					if len(m.inputHistory) > 100 {
						m.inputHistory = m.inputHistory[:100]
					}
				}
			}
			m.inputHistoryIdx = -1
			m.inputDraft = ""
			if m.allTodosDone() {
				m.todos = nil
				m.todosDoneCleared = true
				m.relayoutViewport() // TODO 清除，恢复 viewport 高度
			}
			// 发送消息（彩蛋可能返回动画 cmd）
			if cmd := m.sendMessage(content); cmd != nil {
				cmds = append(cmds, cmd)
			}
			m.textarea.Reset()
			m.autoExpandInput()
			m.viewport.GotoBottom()
			m.newContentHint = false
		}
		if m.typing {
			cmds = append(cmds, tickCmd())
		}
		// Kick off ticker chain when processing just started
		if m.typing && !wasTyping {
			cmds = append(cmds, tickerCmd())
		}
		return m, cmds, true

	case msg.Code == tea.KeyTab:
		// §8 Tab 命令补全
		m.handleTabComplete()
		return m, nil, true

	case msg.String() == "ctrl+k":
		// §9 Ctrl+K 上下文编辑（按可见消息组计数，tool_summary 合并到 assistant）
		if !m.typing && len(m.messages) > 0 {
			groups := visibleMsgGroupIndices(m.messages)
			defaultDel := 1
			if defaultDel > len(groups) {
				defaultDel = len(groups)
			}
			m.confirmDelete = defaultDel
			m.renderCacheValid = false
			m.updateViewportContent()
		} else if !m.typing {
			m.showTempStatus(m.locale.NoMessagesToDelete)
			return m, []tea.Cmd{m.clearTempStatusCmd()}, true
		}
		return m, nil, true

	case msg.String() == "ctrl+o":
		// §11 Ctrl+O 切换 tool summary 展开/折叠（兼容非 CSI-u 终端）
		m.toggleToolSummary()
		return m, nil, true

	case msg.String() == "ctrl+e":
		// §19 Ctrl+E 切换长消息折叠（搜索导航模式下拦截）
		if m.searchMode && !m.searchEditing {
			return m, nil, true
		}
		if !m.typing && !m.searchMode && len(m.messages) > 0 {
			m.toggleMessageFold()
		}
		return m, nil, true

	} // end switch

	// Unhandled key — let post-switch processing handle it (viewport/textarea update)
	return m, nil, false
}

// handleProgressMsg processes progress update events from the agent.
func (m *cliModel) handleProgressMsg(msg cliProgressMsg) {
	prev := m.progress
	m.progress = msg.payload
	// Update bg task count from callback
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	if msg.payload != nil {
		// Sync todo items from progress event
		if len(msg.payload.Todos) > 0 {
			allDone := true
			for _, t := range msg.payload.Todos {
				if !t.Done {
					allDone = false
					break
				}
			}
			if m.todosDoneCleared && allDone {
				// Already cleared by user input; don't re-accept stale all-done list
			} else {
				m.todos = make([]CLITodoItem, len(msg.payload.Todos))
				copy(m.todos, msg.payload.Todos)
				m.todosDoneCleared = false
				m.relayoutViewport() // TODO 行数可能变化，重新计算 viewport 高度
			}
		} else {
			prevTodoCount := len(m.todos)
			m.todos = nil
			if prevTodoCount > 0 {
				m.relayoutViewport() // TODO 清除，恢复 viewport 高度
			}
		}
		// Detect iteration change: snapshot previous iteration into history
		if msg.payload.Iteration > m.lastSeenIteration && m.lastSeenIteration >= 0 && prev != nil {
			// Filter CompletedTools by Iteration field for the previous iteration
			var prevIterTools []CLIToolProgress
			for _, t := range prev.CompletedTools {
				if t.Iteration == m.lastSeenIteration {
					prevIterTools = append(prevIterTools, t)
				}
			}
			if len(prevIterTools) > 0 || prev.Thinking != "" {
				snap := cliIterationSnapshot{
					Iteration: m.lastSeenIteration,
					Thinking:  prev.Thinking,
					Tools:     prevIterTools,
				}
				m.iterationHistory = append(m.iterationHistory, snap)
			}
			// Clear lastCompletedTools to prevent stale tools from being
			// re-snapshotted when the final iteration is snapshotted in handleAgentMessage.
			m.lastCompletedTools = m.lastCompletedTools[:0]
		}
		m.lastSeenIteration = msg.payload.Iteration

		// §2 工具可视化：快照 CompletedTools 到独立字段
		// Only keep tools matching the current iteration to avoid cross-iteration leakage.
		if len(msg.payload.CompletedTools) > 0 {
			var filtered []CLIToolProgress
			for _, t := range msg.payload.CompletedTools {
				if t.Iteration == msg.payload.Iteration {
					filtered = append(filtered, t)
				}
			}
			m.lastCompletedTools = filtered
		}
		if msg.payload.Phase == "done" {
			// Snapshot the final iteration before clearing progress.
			// This handles the case where PhaseDone arrives before
			// handleAgentMessage (e.g. agent error/cancel).
			// Skip if handleAgentMessage already processed (m.typing == false
			// means the reply arrived and cleaned up iteration state).
			if m.typing && m.lastSeenIteration >= 0 {
				alreadySnapped := false
				for _, s := range m.iterationHistory {
					if s.Iteration == m.lastSeenIteration {
						alreadySnapped = true
						break
					}
				}
				if !alreadySnapped {
					var finalTools []CLIToolProgress
					// Check progress.CompletedTools first (set by progressFinalizer)
					for _, t := range msg.payload.CompletedTools {
						if t.Iteration == m.lastSeenIteration {
							finalTools = append(finalTools, t)
						}
					}
					// Also include any from lastCompletedTools (race safety)
					for _, t := range m.lastCompletedTools {
						if t.Iteration == m.lastSeenIteration {
							dup := false
							for _, existing := range finalTools {
								if existing.Name == t.Name && existing.Label == t.Label {
									dup = true
									break
								}
							}
							if !dup {
								finalTools = append(finalTools, t)
							}
						}
					}
					if len(finalTools) > 0 {
						m.iterationHistory = append(m.iterationHistory, cliIterationSnapshot{
							Iteration: m.lastSeenIteration,
							Tools:     finalTools,
						})
					}
				}
				// Generate tool_summary if we have iteration history.
				// Append to end immediately so cancel/error cases (no handleAgentMessage)
				// still display the summary. handleAgentMessage will relocate it before
				// the assistant reply if one follows.
				if len(m.iterationHistory) > 0 {
					m.pendingToolSummary = &cliMessage{
						role:       "tool_summary",
						content:    "",
						timestamp:  time.Now(),
						iterations: append([]cliIterationSnapshot{}, m.iterationHistory...),
						dirty:      true,
					}
					m.messages = append(m.messages, *m.pendingToolSummary)
					m.renderCacheValid = false
				}
			}
			// Reset all iteration tracking state (always, even if handleAgentMessage ran first)
			m.todos = nil
			m.todosDoneCleared = false
			m.endAgentTurn()
			m.inputReady = true
			m.relayoutViewport()
		}
	}
	m.updateViewportContent()
}

// handleInjectedUserMsg processes user messages injected by the agent (e.g. bg task completion).
func (m *cliModel) handleInjectedUserMsg(msg cliInjectedUserMsg) []tea.Cmd {
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   msg.content,
		timestamp: time.Now(),
		dirty:     true,
	})
	m.typing = true
	m.inputReady = false
	m.resetProgressState()
	// Refresh bg task count on injection
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	m.renderCacheValid = false
	// §16 触发 toast 通知（后台任务完成提示）
	// 提取首行作为 toast 文本，避免内容过长
	firstLine := msg.content
	if idx := strings.Index(msg.content, "\n"); idx >= 0 {
		firstLine = msg.content[:idx]
	}
	if len([]rune(firstLine)) > 50 {
		firstLine = string([]rune(firstLine)[:47]) + "..."
	}
	// 检测是否为完成或失败消息
	icon := "ℹ"
	lower := strings.ToLower(firstLine)
	if strings.Contains(lower, "done") || strings.Contains(lower, "completed") || strings.Contains(lower, "完成") {
		icon = "✓"
	} else if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
		icon = "✗"
	}
	return []tea.Cmd{m.enqueueToast(firstLine, icon)}
}

// handleUpdateCheck processes update check results.
func (m *cliModel) handleUpdateCheck(msg cliUpdateCheckMsg) {
	m.checkingUpdate = false
	if msg.info != nil {
		m.updateNotice = msg.info
		if msg.info.HasUpdate {
			content := fmt.Sprintf(m.locale.UpdateFound, msg.info.Current, msg.info.Latest, msg.info.URL)
			m.showSystemMsg(content, feedbackInfo)
		} else {
			content := fmt.Sprintf(m.locale.UpdateCurrent, msg.info.Current)
			m.showSystemMsg(content, feedbackInfo)
		}
	} else {
		m.showSystemMsg(m.locale.UpdateFailed, feedbackError)
	}
}

// handleSuHistoryLoad processes /su user switch history load results.
func (m *cliModel) handleSuHistoryLoad(msg suHistoryLoadMsg) {
	m.suLoading = false
	if msg.err != nil {
		m.showSystemMsg(fmt.Sprintf(m.locale.SuLoadFailed, msg.err), feedbackWarning)
	} else {
		for _, hm := range msg.history {
			cm := cliMessage{
				role:      hm.Role,
				content:   hm.Content,
				timestamp: hm.Timestamp,
				isPartial: false,
				dirty:     true,
			}
			if len(hm.Iterations) > 0 {
				cm.iterations = make([]cliIterationSnapshot, len(hm.Iterations))
				for i, hi := range hm.Iterations {
					cm.iterations[i] = cliIterationSnapshot(hi)
				}
			}
			m.messages = append(m.messages, cm)
		}
		m.showSystemMsg(fmt.Sprintf(m.locale.SuSwitchedHistory, m.senderID, len(msg.history)), feedbackInfo)
	}
	m.invalidateAllCache(false)
	m.viewport.GotoBottom()
}

// handleSplashTick processes splash animation frames.
func (m *cliModel) handleSplashTick(msg splashTickMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	m.splashFrame = msg.frame
	if m.suLoading {
		// /su 历史加载中，持续动画
		cmds = append(cmds, m.splashTick(msg.frame))
		return m, tea.Batch(cmds...)
	}
	if m.ready && msg.frame >= 20 {
		// 初始化完成且已展示至少 1 秒（20 帧 × 50ms）
		m.splashDone = true
		return m, idleTickCmd()
	}
	// 兜底上限：~2 秒（40 帧）
	if msg.frame >= 40 {
		m.splashDone = true
		return m, idleTickCmd()
	}
	cmds = append(cmds, m.splashTick(msg.frame))
	return m, tea.Batch(cmds...)
}

// handleToastMsg enqueues a toast notification.
func (m *cliModel) handleToastMsg(msg cliToastMsg) []tea.Cmd {
	// §16 Toast 通知入队（最多保留 5 条，显示前 3 条）
	if len(m.toasts) >= 5 {
		m.toasts = m.toasts[len(m.toasts)-4:]
	}
	m.toasts = append(m.toasts, cliToastItem(msg))
	if !m.toastTimer {
		m.toastTimer = true
		return []tea.Cmd{tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return cliToastClearMsg{}
		})}
	}
	return nil
}

// handleToastClear removes the oldest toast notification.
func (m *cliModel) handleToastClear(msg cliToastClearMsg) []tea.Cmd {
	if len(m.toasts) > 0 {
		m.toasts = m.toasts[1:]
	}
	if len(m.toasts) > 0 {
		return []tea.Cmd{tea.Tick(3*time.Second, func(time.Time) tea.Msg {
			return cliToastClearMsg{}
		})}
	}
	m.toastTimer = false
	return nil
}
