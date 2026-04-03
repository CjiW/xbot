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
		m.updateViewportContent()
	default:
	}

	// Ctrl+Z: 紧急退出（无论什么状态，包括 panel/typing/idle）
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+z" {
		m.appendSystem("🚪 紧急退出 (Ctrl+Z)")
		m.updateViewportContent()
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
		m.toolSummaryExpanded = !m.toolSummaryExpanded
		m.renderCacheValid = false
		m.cachedHistory = ""
		for i := range m.messages {
			m.messages[i].dirty = true
		}
		m.updateViewportContent()
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		// §9 Ctrl+K 确认模式：必须在 switch msg.Code 之前拦截所有按键
		if m.confirmDelete > 0 {
			groups := visibleMsgGroupIndices(m.messages)
			switch msg.String() {
			case "y", "Y":
				// 确认删除：根据 group 索引截断
				if m.confirmDelete > len(groups) {
					m.confirmDelete = len(groups)
				}
				cutIdx := groups[len(groups)-m.confirmDelete]
				m.messages = m.messages[:cutIdx]
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

		switch {
		case msg.String() == "ctrl+c", msg.Code == tea.KeyEsc:
			// Ctrl+C / Esc：有迭代时中止，无迭代时清空输入
			if m.typing {
				m.sendCancel()
				return m, tea.Batch(cmds...)
			}
			// 非处理状态：清空输入
			if m.textarea.Value() != "" {
				m.textarea.Reset()
				m.autoExpandInput()
			}
			return m, nil

		case msg.Code == tea.KeyUp:
			// ↑ with bg tasks running + empty input → open bg tasks panel
			if m.bgTaskCount > 0 && m.textarea.Value() == "" && m.panelMode == "" {
				m.openBgTasksPanel()
				return m, nil
			}

		case msg.Code == tea.KeyEnter:
			// Enter 发送消息
			if !m.inputReady {
				if m.textarea.Value() != "" {
					m.tempStatus = m.locale.WaitingOperation
					return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return cliTempStatusClearMsg{} })
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
				if m.allTodosDone() {
					m.todos = nil
					m.todosDoneCleared = true
				}
				m.sendMessage(content)
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
				defaultDel := 2
				if defaultDel > len(groups) {
					defaultDel = len(groups)
				}
				m.confirmDelete = defaultDel
				m.renderCacheValid = false
				m.updateViewportContent()
			} else if !m.typing {
				m.tempStatus = m.locale.NoMessagesToDelete
				return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return cliTempStatusClearMsg{} })
			}
			return m, nil

		case msg.String() == "ctrl+o":
			// §11 Ctrl+O 切换 tool summary 展开/折叠（兼容非 CSI-u 终端）
			m.toolSummaryExpanded = !m.toolSummaryExpanded
			m.renderCacheValid = false
			m.cachedHistory = ""
			for i := range m.messages {
				m.messages[i].dirty = true
			}
			m.updateViewportContent()
			return m, nil

		} // end switch

	case tea.WindowSizeMsg:
		// 窗口大小变化 - 动态调整布局
		m.handleResize(msg.Width, msg.Height)

	case cliOutboundMsg:
		// 收到 agent 回复
		m.handleAgentMessage(msg.msg)

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
				}
			} else {
				m.todos = nil
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
				filtered := m.lastCompletedTools[:0]
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
		if (m.bgTaskCountFn != nil && m.bgTaskCount > 0) || m.typing || m.progress != nil {
			cmds = append(cmds, tickCmd())
		}
		if m.typing || m.progress != nil {
			m.updateViewportContent()
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
		cmds = append(cmds, func() tea.Msg {
			return cliToastMsg{text: firstLine, icon: icon}
		})

	case cliUpdateCheckMsg:
		m.checkingUpdate = false
		if msg.info != nil {
			m.updateNotice = msg.info
			if msg.info.HasUpdate {
				content := fmt.Sprintf(m.locale.UpdateFound, msg.info.Current, msg.info.Latest, msg.info.URL)
				m.appendSystem(content)
				m.updateViewportContent()
			} else {
				content := fmt.Sprintf(m.locale.UpdateCurrent, msg.info.Current)
				m.appendSystem(content)
				m.updateViewportContent()
			}
		} else {
			m.appendSystem(m.locale.UpdateFailed)
			m.updateViewportContent()
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
		if m.ready {
			// 初始化完成（收到 WindowSizeMsg），结束 splash
			m.splashDone = true
			return m, nil
		}
		// 持续动画（最多 ~3 秒 = 37 帧）
		if msg.frame >= 37 {
			m.splashDone = true
			return m, nil
		}
		cmds = append(cmds, m.splashTick(msg.frame))
		return m, tea.Batch(cmds...)

	case splashDoneMsg:
		// §14 启动画面结束确认
		m.splashDone = true

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

// autoExpandInput adjusts the main textarea height based on content lines.
// Keeps it between minTaHeight and maxTaHeight, and shrinks viewport accordingly.
const (
	minTaHeight = 3
	maxTaHeight = 10
)

func (m *cliModel) autoExpandInput() {
	lines := strings.Count(m.textarea.Value(), "\n") + 1
	if lines < minTaHeight {
		lines = minTaHeight
	}
	if lines > maxTaHeight {
		lines = maxTaHeight
	}
	if m.textarea.Height() == lines {
		return
	}
	oldHeight := m.textarea.Height()
	grew := lines > oldHeight
	m.textarea.SetHeight(lines)
	// Adjust viewport to compensate
	delta := lines - oldHeight
	if m.viewport.Height()-delta >= 3 {
		m.viewport.SetHeight(m.viewport.Height() - delta)
	}
	if grew {
		// When height increases, bubbles textarea repositionView only scrolls
		// down (if cursor below view) or up (if cursor above). It won't
		// shrink YOffset when more lines become visible, so the first content
		// line stays scrolled off-screen.
		// Fix: move cursor to top (resets YOffset to 0 via repositionView),
		// then move back to the original row.
		targetRow := m.textarea.Line()
		if targetRow > 0 {
			// InputBegin is bound to ctrl+home; this triggers moveToBegin + repositionView
			m.textarea, _ = m.textarea.Update(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
			for i := 0; i < targetRow; i++ {
				m.textarea.CursorDown()
			}
			// CursorDown doesn't call repositionView, but since YOffset=0
			// and height >= total lines, the cursor is always visible.
			// We still need one Update to sync internal viewport state.
			m.textarea, _ = m.textarea.Update(nil)
		}
	}
}

// handleResize 处理窗口大小变化
func (m *cliModel) handleResize(width, height int) {
	m.width = width
	m.height = height

	// §20 重建样式缓存
	m.styles = buildStyles(width)

	// Layout: titleBar(1) + status(1) + footer(1) + inputBox(taHeight+2 border)
	fixedLines := 3 // titleBar + status + footer
	taBorder := 2   // top + bottom border
	reservedLines := fixedLines + taBorder + m.textarea.Height()
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
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(viewportHeight)

	// inputBoxStyle uses Width(width-4) for content, Padding(0,1) adds 2, Border adds 2.
	// textarea must match the content width exactly.
	iw := width - 4
	if iw < 10 {
		iw = 10
	}
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
