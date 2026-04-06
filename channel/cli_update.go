package channel

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	log "github.com/sirupsen/logrus"
)

// Update 处理消息
func (m *cliModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	// §8 Tab 补全：记录输入内容变化以重置补全状态
	prevText := m.textarea.Value()

	wasTyping := m.typing

	// Async settings save completed — apply theme/locale/viewport changes
	if saved, ok := msg.(cliSettingsSavedMsg); ok {
		cmd := m.handleSettingsSavedMsg(saved)
		return m, cmd
	}

	// Runner status change notification
	if rsm, ok := msg.(runnerStatusMsg); ok {
		cmd := m.handleRunnerStatusMsg(rsm)
		return m, cmd
	}

	// 主题变更通知：重建样式缓存 + glamour 渲染器
	select {
	case <-themeChangeCh:
		if m.width > 4 {
			m.renderer = newGlamourRenderer(m.width - 4)
		}
		// §20 重建样式缓存
		m.styles = buildStyles(m.width)
		// 刷新 textarea 样式（初始化时一次性绑定，theme 切换后需重建）
		applyTAStyles(&m.textarea, &m.styles)
		// 刷新 ticker 颜色
		m.ticker.style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Warning))
		m.renderCacheValid = false
		for i := range m.messages {
			m.messages[i].dirty = true
		}
		m.updateViewportContent()
	default:
	}

	// i18n: locale 变更通知
	select {
	case <-localeChangeCh:
		m.locale = GetLocale(currentLocaleLang)
		m.renderCacheValid = false
		for i := range m.messages {
			m.messages[i].dirty = true
		}
		m.updatePlaceholder()
		m.updateViewportContent()
	default:
	}

	// Ctrl+Z: 紧急退出（无论什么状态，包括 panel/typing/idle）
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+z" {
		m.showSystemMsg(m.locale.EmergencyQuitHint, feedbackWarning)
		return m, tea.Quit
	}

	// §12 Panel mode: intercept all key events when panel is active
	if key, ok := msg.(tea.KeyPressMsg); ok && m.panelMode != "" {
		// Ctrl+C must always cancel the agent — never swallow it
		if key.String() == "ctrl+c" && m.typing {
			m.closePanel()
			m.sendCancel()
			return m, tea.Batch(tickerCmd(), tickCmd())
		}
		handled, newModel, cmd := m.updatePanel(key)
		if handled {
			return newModel, cmd
		}
	}
	// §12b Panel mode: intercept paste events — PasteMsg is not KeyPressMsg,
	// so it bypasses the above panel interceptor and would be captured by the
	// main textarea below. Forward it to the panel's internal textarea instead.
	if paste, ok := msg.(tea.PasteMsg); ok && m.panelMode != "" {
		var cmd tea.Cmd
		switch m.panelMode {
		case "askuser":
			// Check if current tab has options (use textinput) or free input (use textarea)
			if m.panelTab >= 0 && m.panelTab < len(m.panelItems) && len(m.panelItems[m.panelTab].Options) > 0 {
				m.panelOtherTI, cmd = m.panelOtherTI.Update(paste)
			} else {
				m.autoExpandAskTA()
				m.panelAnswerTA, cmd = m.panelAnswerTA.Update(paste)
			}
		case "settings":
			if m.panelEdit {
				m.panelEditTA, cmd = m.panelEditTA.Update(paste)
			}
		}
		return m, cmd
	}

	// §21 搜索模式拦截
	if key, ok := msg.(tea.KeyPressMsg); ok && m.searchMode {
		switch {
		case m.searchEditing:
			switch key.String() {
			case "enter":
				m.executeSearch()
				return m, nil
			case "esc":
				m.exitSearch()
				return m, nil
			}
			var cmd tea.Cmd
			m.searchTI, cmd = m.searchTI.Update(msg)
			return m, cmd
		default:
			switch key.String() {
			case "n":
				if len(m.searchResults) > 0 {
					next := m.searchIdx + 1
					if next >= len(m.searchResults) {
						next = 0
					}
					m.jumpToSearchResult(next)
					m.renderCacheValid = false
					m.updateViewportContent()
				}
				return m, nil
			case "N":
				if len(m.searchResults) > 0 {
					prev := m.searchIdx - 1
					if prev < 0 {
						prev = len(m.searchResults) - 1
					}
					m.jumpToSearchResult(prev)
					m.renderCacheValid = false
					m.updateViewportContent()
				}
				return m, nil
			case "esc":
				m.exitSearch()
				return m, nil
			}
			return m, nil
		}
	}

	// Home/End 跳顶部/底部
	if key, ok := msg.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "home":
			m.viewport.GotoTop()
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			m.newContentHint = false
			return m, nil
		}
	}

	// Ctrl+Enter 换行（终端发送的 raw sequence 不统一，需手动检测）
	if isCtrlEnter(msg) {
		m.textarea.InsertString("\n")
		m.autoExpandInput()
		return m, nil
	}
	// Ctrl+O 切换 tool summary 展开/折叠（CSI u 协议兼容层，kitty/Ghostty 等）
	if isCtrlO(msg) {
		m.toggleToolSummary()
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
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
				return m, nil
			case "n", "N":
				// 取消删除
				m.confirmDelete = 0
				m.renderCacheValid = false
				m.updateViewportContent()
				return m, nil
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
						return m, nil
					}
				}
				// 其他键也取消（包括 Esc）
				m.confirmDelete = 0
				m.renderCacheValid = false
				m.updateViewportContent()
				return m, nil
			}
		}

		// 🥚 彩蛋覆盖层激活时，按任意键退出（Ctrl+C 除外，已在上面处理）
		if m.easterEgg != easterEggNone {
			return m, func() tea.Msg { return easterEggDoneMsg{} }
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
				return m, cmd
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
					return m, nil
				}
				m.sendCancel()
				return m, tea.Batch(cmds...)
			}
			// 非处理状态：清空输入
			if m.textarea.Value() != "" {
				m.textarea.Reset()
				m.inputHistoryIdx = -1
				m.inputDraft = ""
				m.autoExpandInput()
			}
			return m, nil

		case msg.Code == tea.KeyEsc:
			// Esc：非处理状态清空输入；处理中时取消排队编辑或清空输入
			if m.queueEditing {
				m.queueEditing = false
				m.queueEditBuf = ""
				m.textarea.SetValue("")
				return m, nil
			}
			if !m.typing {
				if m.textarea.Value() != "" {
					m.textarea.Reset()
					m.inputHistoryIdx = -1
					m.inputDraft = ""
					m.autoExpandInput()
				}
			}
			return m, nil

		case msg.Text == "^":
			if m.panelMode == "" && m.bgTaskCount > 0 && m.inputHistoryIdx == -1 {
				m.openBgTasksPanel()
				return m, nil
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
					return m, nil
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
					return m, nil
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
				return m, nil
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
					return m, nil
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
					return m, m.clearTempStatusCmd()
				}
				return m, nil
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
				return m, nil
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
			return m, tea.Batch(cmds...)

		case msg.Code == tea.KeyTab:
			// §8 Tab 命令补全
			m.handleTabComplete()
			return m, nil

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
				return m, m.clearTempStatusCmd()
			}
			return m, nil

		case msg.String() == "ctrl+o":
			// §11 Ctrl+O 切换 tool summary 展开/折叠（兼容非 CSI-u 终端）
			m.toggleToolSummary()
			return m, nil

		case msg.String() == "ctrl+e":
			// §19 Ctrl+E 切换长消息折叠（搜索导航模式下拦截）
			if m.searchMode && !m.searchEditing {
				return m, nil
			}
			if !m.typing && !m.searchMode && len(m.messages) > 0 {
				m.toggleMessageFold()
			}
			return m, nil

		} // end switch

	case tea.WindowSizeMsg:
		// 窗口大小变化 - 动态调整布局
		m.handleResize(msg.Width, msg.Height)

	case cliOutboundMsg:
		// 收到 agent 回复
		m.handleAgentMessage(msg.msg)
		// §Q 刷新消息队列
		if m.needFlushQueue {
			m.needFlushQueue = false
			cmds = append(cmds, m.flushMessageQueue())
		}

	case cliProgressMsg:
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
				m.lastCompletedTools = nil
				m.iterationHistory = nil
				m.lastSeenIteration = 0
				m.typingStartTime = time.Time{}
				m.todos = nil
				m.todosDoneCleared = false
				m.relayoutViewport() // TODO 清除，恢复 viewport 高度
				m.progress = nil
				m.typing = false
			}
		}
		m.updateViewportContent()

	case cliTickMsg:
		// Always refresh bg task count on tick so status bar updates immediately
		// when a bg task completes (even when no progress event is coming)
		if m.bgTaskCountFn != nil {
			prev := m.bgTaskCount
			m.bgTaskCount = m.bgTaskCountFn()
			// Force re-render when count changes (e.g. task killed in panel)
			if m.bgTaskCount != prev {
				m.renderCacheValid = false
			}
		}
		// Schedule next tick when agent is active or bg tasks are running.
		// IMPORTANT: only emit ONE tickCmd to prevent exponential message growth
		// (two tickCmd() would double the message count every 100ms → CPU explosion).
		busy := m.typing || m.progress != nil
		if (m.bgTaskCountFn != nil && m.bgTaskCount > 0) || busy {
			cmds = append(cmds, tickCmd())
		} else {
			// Transition to idle: start low-frequency tick for placeholder rotation
			cmds = append(cmds, idleTickCmd())
		}
		if busy {
			m.updateViewportContent()
		}

	case idleTickMsg:
		// Low-frequency idle tick: rotate placeholder and keep alive
		if !m.typing && m.progress == nil {
			m.updatePlaceholder()
			cmds = append(cmds, idleTickCmd())
		}

	case cliTempStatusClearMsg:
		m.tempStatus = ""

	case cliInjectedUserMsg:
		// Agent injected a user message (e.g. bg task completion notification).
		// Display it identically to a manually typed user message and start spinner.
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
		cmds = append(cmds, m.enqueueToast(firstLine, icon))
	case cliUpdateCheckMsg:
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

	case tickerTickMsg:
		// Ticker tick: advance frame and trigger viewport refresh
		if m.typing || m.progress != nil {
			m.ticker.tick()
			cmds = append(cmds, tickerCmd())
			m.updateViewportContent()
		}

	case splashTickMsg:
		// §14 启动画面动画帧推进
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

	case splashDoneMsg:
		// §14 启动画面结束确认
		m.splashDone = true
		cmds = append(cmds, idleTickCmd())

	case suHistoryLoadMsg:
		// /su 切换用户后历史加载完成
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

	case cliToastMsg:
		// §16 Toast 通知入队（最多保留 5 条，显示前 3 条）
		if len(m.toasts) >= 5 {
			m.toasts = m.toasts[len(m.toasts)-4:]
		}
		m.toasts = append(m.toasts, cliToastItem(msg))
		if !m.toastTimer {
			m.toastTimer = true
			cmds = append(cmds, tea.Tick(3*time.Second, func(time.Time) tea.Msg {
				return cliToastClearMsg{}
			}))
		}

	case cliToastClearMsg:
		// §16 Toast 通知：弹出队列头部
		if len(m.toasts) > 0 {
			m.toasts = m.toasts[1:]
		}
		if len(m.toasts) > 0 {
			cmds = append(cmds, tea.Tick(3*time.Second, func(time.Time) tea.Msg {
				return cliToastClearMsg{}
			}))
		} else {
			m.toastTimer = false
		}

	case easterEggDoneMsg:
		// 🥚 彩蛋关闭（按任意键触发）
		m.dismissEasterEgg()
		m.renderCacheValid = false
		m.updateViewportContent()
		return m, nil

	case easterEggMatrixTickMsg:
		// 🥚 Matrix 代码雨动画帧推进
		if m.easterEgg == easterEggMatrix {
			m.tickMatrix()
			cmds = append(cmds, matrixTickCmd())
		}
		return m, tea.Batch(cmds...)
	}

	// Kick off ticker + tick chains when processing just started
	if m.typing && !wasTyping {
		cmds = append(cmds, tickerCmd(), tickCmd())
	}

	// 更新 viewport
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	// 更新 textarea
	m.textarea, cmd = m.textarea.Update(msg)
	cmds = append(cmds, cmd)

	// §8 Tab 补全：输入内容变化时重置补全状态
	newVal := m.textarea.Value()
	if newVal != prevText {
		m.completions = nil
		m.compIdx = 0
		m.fileCompActive = false
		// 用户手动输入：根据当前 @ prefix 重新 glob
		// 但如果 fileCompActive（Tab 循环中），不重新 glob
		if !m.fileCompActive {
			if ok, prefix := detectAtPrefix(newVal); ok {
				m.populateFileCompletions(prefix)
			} else {
				m.fileCompletions = nil
				m.fileCompIdx = 0
			}
		}
	}

	// 检查是否需要退出
	if m.shouldQuit {
		return m, tea.Quit
	}

	m.autoExpandInput()

	return m, tea.Batch(cmds...)
}

// autoExpandInput adjusts the viewport height to compensate for textarea height changes.
// With DynamicHeight enabled on the textarea, it manages its own height based on
// visual lines (including soft wraps from CJK characters). We just need to keep the
// viewport in sync.
const (
	minTaHeight = 3
	maxTaHeight = 10
)

func (m *cliModel) autoExpandInput() {
	// DynamicHeight manages textarea height based on visual lines.
	// We just need to sync the viewport and clamp textarea if terminal is too small.
	taHeight := m.textarea.Height()
	if taHeight < minTaHeight {
		taHeight = minTaHeight
	}
	// Clamp textarea height to available space (don't let it push viewport below minimum)
	availableForTA := m.height - 3 - 2 // 3 = title+status+footer, 2 = ta border
	if m.todos != nil {
		availableForTA -= 1 + len(m.todos)
	}
	maxAllowed := availableForTA - 3 // 3 = minimum viewport
	if maxAllowed < minTaHeight {
		maxAllowed = minTaHeight
	}
	if taHeight > maxAllowed {
		taHeight = maxAllowed
		m.textarea.SetHeight(taHeight)
	}
	expectedVP := m.layoutViewportHeight()
	currentVP := m.viewport.Height()
	if currentVP != expectedVP {
		wasAtBottom := m.viewport.AtBottom()
		m.viewport.SetHeight(expectedVP)
		if wasAtBottom {
			m.viewport.GotoBottom()
		}
	}
}

// layoutViewportHeight 计算 viewport 应有的高度，考虑 panel 模式。
// 正常模式：titleBar(1) + status(1) + footer(1) + inputBox(taHeight+border)
// Panel 模式：titleBar(1) + panel(border) + panelFooter(1) + toast(~1)
func (m *cliModel) layoutViewportHeight() int {
	height := m.height
	fixedLines := 3 // titleBar + status + footer

	if m.panelMode != "" {
		// Panel 模式：viewport 缩到最小，给 panel 尽可能多的空间
		// 用户在操作 panel 时 viewport 只是背景参考
		return 3
	}

	// 正常模式
	taBorder := 2 // top + bottom border
	// 计算 todoBar 占用的行数：标题行(1) + 每个 todo item 一行
	todoLines := 0
	if len(m.todos) > 0 {
		todoLines = 1 + len(m.todos)
	}
	reservedLines := fixedLines + taBorder + m.textarea.Height() + todoLines
	// §20b 小终端适配：极小窗口下动态缩减布局
	if height < 12 {
		reservedLines = fixedLines + taBorder + 2 // min textarea
	}
	if height < 8 {
		reservedLines = 4
	}
	if height < 5 {
		reservedLines = 4
	}
	viewportHeight := height - reservedLines
	if viewportHeight < 3 {
		viewportHeight = 3
	}
	return viewportHeight
}

// layoutPanelHeight 计算 panel viewport 的高度（与 layoutViewportHeight 同模式）。
// panel 布局：titleBar(1) + panelViewport + footer(1) + toast(0-1) = m.height
func (m *cliModel) layoutPanelHeight() int {
	return m.height - 3 // titleBar(1) + footer(1) + toast预留(1)
}

// relayoutViewport 重新计算并设置 viewport 高度（不重建样式缓存）。
// 用于 panel 打开/关闭、todo 增减时动态调整布局。
// 如果用户之前在底部，调整后继续保持跟随底部。
func (m *cliModel) relayoutViewport() {
	if m.width == 0 || m.height == 0 {
		return
	}
	wasAtBottom := m.viewport.AtBottom()
	m.viewport.SetHeight(m.layoutViewportHeight())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	// Panel viewport：和主 viewport 同模式
	if m.panelMode != "" {
		m.panelViewport.SetHeight(m.layoutPanelHeight())
		m.panelViewport.SetWidth(m.width)
	}
}

// handleResize 处理窗口大小变化
func (m *cliModel) handleResize(width, height int) {
	m.width = width
	m.height = height

	// §20 重建样式缓存
	m.styles = buildStyles(width)

	m.viewport.SetWidth(width)
	m.viewport.SetHeight(m.layoutViewportHeight())
	m.panelViewport.SetWidth(width)
	m.panelViewport.SetHeight(m.layoutPanelHeight())

	// InputBox lipgloss style: Width(width-4) includes border(2) + padding(2).
	// Content area = width-4-2-2 = width-8. Textarea must match this.
	iw := width - 8
	if iw < 10 {
		iw = 10
	}
	iw = iw &^ 1 // round down to even for CJK
	m.textarea.SetWidth(iw)

	// Glamour word-wrap must match viewport width so that lines
	// don't get re-wrapped by lipgloss (which would lose the margin).
	if width > 4 {
		m.renderer = newGlamourRenderer(width - 4)
	}

	if !m.ready {
		m.ready = true
	}

	// §1 增量渲染：resize 后缓存全部失效
	m.renderCacheValid = false
	for i := range m.messages {
		m.messages[i].dirty = true
	}

	// 更新内容（保持用户滚动位置）
	wasAtBottom := m.viewport.AtBottom()
	m.updateViewportContent()
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// panelWidth returns a width suitable for panel textareas,
// adapting to the current terminal width (with sensible bounds).
func (m *cliModel) panelWidth(want int) int {
	maxW := m.width - 8 // room for panel border + padding
	if want > maxW {
		return maxW
	}
	if want < 30 {
		return 30
	}
	return want
}

// renderCompletionsHint returns the dynamic border color and completions hint string
// based on the current input content (slash commands, @ file references, etc.).
func (m *cliModel) renderCompletionsHint(inputValue string) (borderColor color.Color, hint string) {
	borderColor = lipgloss.Color(currentTheme.Accent)

	if strings.HasPrefix(inputValue, "!") {
		borderColor = lipgloss.Color(currentTheme.Error)
		return
	}

	if strings.HasPrefix(inputValue, "/") {
		borderColor = lipgloss.Color(currentTheme.Success)
		if len(m.completions) > 0 {
			parts := make([]string, len(m.completions))
			for i, c := range m.completions {
				if i == m.compIdx {
					parts[i] = m.styles.CompSelected.Render(c)
				} else {
					parts[i] = m.styles.CompItem.Render(c)
				}
			}
			hint = m.styles.CompHint.Render(strings.Join(parts, " · "))
		} else {
			var matches []string
			for _, cmd := range cliCommands {
				if strings.HasPrefix(cmd, inputValue) {
					matches = append(matches, cmd)
				}
			}
			if len(matches) > 0 {
				hint = m.styles.CompHintBorder.Render("[Tab] " + strings.Join(matches, " · "))
			}
		}
		return
	}

	// §20c @ 文件引用补全（带目录/文件图标区分 + 截断）
	rawInput := m.textarea.Value()
	if ok, _ := detectAtPrefix(rawInput); ok {
		borderColor = lipgloss.Color(currentTheme.Info)
		if len(m.fileCompletions) > 0 {
			parts := make([]string, len(m.fileCompletions))
			for i, c := range m.fileCompletions {
				base := filepath.Base(c)
				dir := isDir(c)
				if dir {
					base += "/"
				}
				// 截断过长文件名
				if utf8.RuneCountInString(base) > 20 {
					runes := []rune(base)
					base = string(runes[:18]) + "…"
				}
				icon := "📄 "
				if dir {
					icon = "📁 "
				}
				display := icon + base
				if i == m.fileCompIdx {
					parts[i] = m.styles.FileCompSel.Render(display)
				} else {
					parts[i] = m.styles.FileCompFile.Render(display)
				}
			}
			hint = m.styles.TextMutedSt.Padding(0, 1).
				Render("[Tab] " + strings.Join(parts, " · "))
		} else {
			hint = m.styles.TextMutedSt.Padding(0, 1).
				Render(m.locale.TabNoMatch)
		}
		return
	}
	return
}

// handleRunnerStatusMsg 处理 runner 连接状态变化
func (m *cliModel) handleRunnerStatusMsg(msg runnerStatusMsg) tea.Cmd {
	if msg.err != nil {
		m.showTempStatus(fmt.Sprintf("%s: %v", m.locale.RunnerConnectFailed, msg.err))
		return m.clearTempStatusCmd()
	}
	if msg.status == RunnerConnected {
		m.showTempStatus(m.locale.RunnerConnectSuccess)
		return m.clearTempStatusCmd()
	}
	return nil
}
