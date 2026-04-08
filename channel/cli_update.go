package channel

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
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

	// Async settings save completed — apply theme/locale/viewport changes
	if saved, ok := msg.(cliSettingsSavedMsg); ok {
		cmd := m.handleSettingsSavedMsg(saved)
		return m, cmd
	}

	// Async subscription switch completed
	if done, ok := msg.(cliSwitchLLMDoneMsg); ok {
		if done.err != nil {
			m.showTempStatus(fmt.Sprintf("Failed to switch LLM: %v", done.err))
		} else if done.mgr != nil {
			if err := done.mgr.SetDefault(done.subID); err != nil {
				m.showTempStatus(fmt.Sprintf("LLM switched but failed to save: %v", err))
			} else {
				m.showTempStatus(fmt.Sprintf("Switched to: %s (%s)", done.subName, done.subModel))
			}
			m.refreshCachedModelName()
		}
		return m, nil
	}

	// Runner status change notification
	if rsm, ok := msg.(runnerStatusMsg); ok {
		cmd := m.handleRunnerStatusMsg(rsm)
		return m, cmd
	}

	// 主题变更通知：重建样式缓存 + glamour 渲染器
	select {
	case <-themeChangeCh:
		m.applyThemeAndRebuild(currentThemeName)
		m.updateViewportContent()
	default:
	}

	// Drain pending cmds queued by helpers (e.g. showTempStatus).
	// Prepend to cmds so they get batched with any cmds produced by the
	// switch cases below — do NOT return early here, or the tick chain
	// breaks (e.g. a pending tempStatus clear would prevent cliTickMsg
	// from emitting the next tickCmd).
	if len(m.pendingCmds) > 0 {
		cmds = append(cmds, m.pendingCmds...)
		m.pendingCmds = nil
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

	// §15 Quick switch overlay: highest priority (above panelMode).
	// This ensures ESC in quick switch closes the overlay, not the panel behind it.
	if key, ok := msg.(tea.KeyPressMsg); ok {
		if handled, cmd := m.handleQuickSwitchKey(key); handled {
			return m, cmd
		}
	}

	// §12 Panel mode: intercept all key events when panel is active
	if key, ok := msg.(tea.KeyPressMsg); ok && m.panelMode != "" {
		// Ctrl+C must always cancel the agent — never swallow it
		if key.String() == "ctrl+c" && m.typing {
			m.closePanel()
			m.sendCancel()
			return m, tickCmd()
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
		model, keyCmds, handled := m.handleKeyPress(msg, wasTyping)
		if handled {
			return model, tea.Batch(keyCmds...)
		}
		// Unhandled key: fall through to post-switch processing

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
		m.handleProgressMsg(msg)
		// §Q 刷新消息队列（PhaseDone 可能先于 cliOutboundMsg 到达）
		if m.needFlushQueue {
			m.needFlushQueue = false
			cmds = append(cmds, m.flushMessageQueue())
		}

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
			// Advance spinner frame on every tick so the animation stays in sync
			// with elapsed time display. Previously driven by a separate tickerTickMsg
			// chain that could break when m.progress briefly went nil.
			m.ticker.tick()
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
		cmds = append(cmds, m.handleInjectedUserMsg(msg)...)
	case cliUpdateCheckMsg:
		m.handleUpdateCheck(msg)

	case tickerTickMsg:
		// Legacy: ticker is now driven by cliTickMsg. Drop stale messages.

	case splashTickMsg:
		return m.handleSplashTick(msg)

	case splashDoneMsg:
		// §14 启动画面结束确认
		m.splashDone = true
		cmds = append(cmds, idleTickCmd())

	case suHistoryLoadMsg:
		m.handleSuHistoryLoad(msg)

	case cliToastMsg:
		cmds = append(cmds, m.handleToastMsg(msg)...)

	case cliToastClearMsg:
		cmds = append(cmds, m.handleToastClear(msg)...)

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

	// Kick off tick chain when processing just started
	if m.typing && !wasTyping {
		cmds = append(cmds, tickCmd())
	}

	// 更新 viewport
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	// 更新 textarea
	// Skip WindowSizeMsg: handleResize already calls SetWidth() which
	// triggers recalculateHeight(). Forwarding the resize message to
	// textarea.Update() would redundantly recalculate + render view().
	if _, ok := msg.(tea.WindowSizeMsg); !ok {
		m.textarea, cmd = m.textarea.Update(msg)
		cmds = append(cmds, cmd)
	}

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
}

// handleResize 处理窗口大小变化
func (m *cliModel) handleResize(width, height int) {
	// Deduplicate: skip if size hasn't actually changed.
	// During resize drags, terminals (especially foot) may fire many
	// SIGWINCH signals with the same dimensions — each one triggers a
	// full O(N) rebuild of the message history.
	if width == m.width && height == m.height && m.ready {
		return
	}

	m.width = width
	m.height = height

	// §20 重建样式缓存
	m.styles = buildStyles(width)

	m.viewport.SetWidth(width)
	m.viewport.SetHeight(m.layoutViewportHeight())

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
