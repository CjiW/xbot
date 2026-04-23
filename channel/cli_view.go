package channel

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
	"xbot/version"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// View 渲染界面
func (m *cliModel) View() tea.View {
	// §14 启动画面：品牌展示动画（~2.4 秒后自动消失）
	if !m.splashDone {
		v := tea.NewView(m.renderSplash())
		v.AltScreen = true // 使用 AltScreen 避免残留到主终端缓冲区
		return v
	}

	if !m.ready {
		v := tea.NewView("\n  " + m.locale.SplashLoading)
		v.AltScreen = true
		return v
	}

	// 🥚 彩蛋覆盖层：有彩蛋激活时优先渲染全屏覆盖
	if m.easterEgg != easterEggNone {
		v := tea.NewView(m.renderEasterEggOverlay())
		v.AltScreen = true
		return v
	}

	// /su 切换用户后加载历史中的 loading 画面
	if m.suLoading {
		v := tea.NewView(m.renderSuLoading())
		v.AltScreen = true
		return v
	}

	// ========== 样式定义 ==========

	// 标题栏：纯 ASCII，避免 emoji 导致宽度误算
	titleLeft := m.titleText()
	// 标题栏右侧快捷键提示：紧凑的点分隔，比 | 更柔和
	titleRight := m.locale.TitleHint
	// Askuser panel: override titleRight with panel-specific hints (always visible)
	if m.panelMode == "askuser" {
		titleRight = m.askUserTitleHints()
	} else if m.updateNotice != nil && m.updateNotice.HasUpdate {
		titleRight = fmt.Sprintf("%s→%s · /update · /help", m.updateNotice.Current, m.updateNotice.Latest)
	}
	// Runner status + identity indicator in title bar
	if m.runnerBridge != nil {
		switch m.runnerBridge.Status() {
		case RunnerConnected:
			titleRight = "🟢 Runner · " + titleRight
		case RunnerConnecting:
			titleRight = "🟡 Runner · " + titleRight
		}
	}
	if m.senderID != "cli_user" {
		titleRight = "👤 " + m.senderID + " · " + titleRight
	}
	titlePad := m.width - lipgloss.Width(titleLeft) - lipgloss.Width(titleRight)
	if titlePad < 1 {
		titlePad = 1
	}
	titleBar := m.styles.TitleBar.
		Render(titleLeft + strings.Repeat(" ", titlePad) + titleRight)

		// 输入框样式：根据输入内容动态设置边框颜色
		// ! 开头 → 错误色，/ 开头 → 成功色，默认 → 主题强调色
	inputValue := m.textarea.Value()
	borderColor, completionsHint := m.renderCompletionsHint(inputValue)

	inputBoxStyle := m.styles.InputBox.BorderForeground(borderColor)

	inputArea := m.textarea.View()

	// §23 Render placeholder manually when textarea is empty.
	// This avoids textarea's built-in placeholder which causes a view-mode
	// switch that triggers CJK rendering bugs on Windows Terminal.
	if m.textarea.Value() == "" && m.placeholderText != "" {
		// Build a 3-line placeholder view matching the textarea's height (minTaHeight=3).
		taHeight := minTaHeight
		if h := m.textarea.Height(); h > 0 {
			taHeight = h
		}
		// Truncate placeholder to fit the textarea content width on narrow terminals.
		ph := m.placeholderText
		if tw := m.textarea.Width(); tw > 0 {
			ph = truncateToWidth(ph, tw)
		}
		// Render the first character of placeholder as a virtual cursor (reverse style),
		// using the same cursor color as textarea's normal mode (TACursor).
		phRunes := []rune(ph)
		if len(phRunes) > 0 {
			first := string(phRunes[0])
			rest := string(phRunes[1:])
			cursorColor := m.styles.TACursor.GetForeground()
			cursor := lipgloss.NewStyle().Foreground(cursorColor).Reverse(true).Render(first)
			phRendered := cursor + m.styles.PlaceholderSt.Render(rest)
			lines := make([]string, taHeight)
			lines[0] = phRendered
			for i := 1; i < taHeight; i++ {
				lines[i] = ""
			}
			inputArea = strings.Join(lines, "\n")
		}
	}

	// 状态栏样式
	readyStatusStyle := m.styles.ReadyStatus

	// §20 使用缓存样式
	thinkingStatusStyle := m.styles.ThinkingSt

	// §20 进度样式 → 缓存
	progressStyle := m.styles.Progress
	toolStyle := m.styles.Tool

	// ========== 渲染各部分 ==========

	// 输入区
	input := inputBoxStyle.Render(inputArea)

	// Build content string
	var content string

	// §16 Toast 通知渲染
	toastStr := m.renderToast()

	// §21 搜索模式
	if m.searchMode {
		var searchBar string
		if m.searchEditing {
			searchBar = m.styles.SearchBar.Render(m.searchTI.View())
		} else {
			searchBar = m.styles.SearchBar.Render(
				fmt.Sprintf(m.locale.SearchNavFormat, m.searchQuery, m.searchIdx+1, len(m.searchResults)))
		}
		content = fmt.Sprintf(
			"%s\n%s\n%s\n%s%s",
			titleBar,
			m.viewport.View(),
			searchBar,
			input,
			toastStr,
		)
	} else if m.panelMode == "askuser" {
		// §12b AskUser split layout: viewport visible above, panel at bottom
		// Note: no panelFooter here — hints are inside the panel (viewAskUserPanel)
		askRaw := m.viewAskUserPanel()
		m.clampAskUserPanelScroll(askRaw)
		askLines := strings.Split(askRaw, "\n")
		// Calculate available height for the ask panel
		fixedLines := 2 // titleBar + toast (no separate footer — hints are in-panel)
		panelBorder := 2
		viewportH := m.layoutViewportHeight()
		askVisibleH := m.height - fixedLines - viewportH - panelBorder
		if askVisibleH < 3 {
			askVisibleH = 3
		}
		if m.askPanelScrollY+askVisibleH > len(askLines) {
			m.askPanelScrollY = max(0, len(askLines)-askVisibleH)
		}
		end := m.askPanelScrollY + askVisibleH
		if end > len(askLines) {
			end = len(askLines)
		}
		visibleAsk := askLines[m.askPanelScrollY:end]
		askContent := strings.Join(visibleAsk, "\n")
		boxedAsk := m.styles.PanelBox.Render(askContent)
		// Scroll indicator
		totalAskLines := len(askLines)
		scrollHint := ""
		if totalAskLines > askVisibleH {
			pct := (m.askPanelScrollY + askVisibleH) * 100 / totalAskLines
			scrollHint = m.styles.PanelDesc.Render(fmt.Sprintf(" [%d%%] Ctrl+↑↓/PgUp/PgDn", pct))
		}
		content = fmt.Sprintf(
			"%s\n%s\n%s%s%s",
			titleBar,
			m.viewport.View(),
			boxedAsk,
			scrollHint,
			toastStr,
		)
	} else if m.panelMode != "" {
		// §12 Panel mode: 手动切片 + PanelBox 包裹（边框永远在屏幕内）
		panelFooter := m.renderFooter()
		rawContent := m.viewPanel() // 原始内容，无 PanelBox
		m.clampPanelScroll(rawContent)
		rawLines := strings.Split(rawContent, "\n")
		visibleH := m.panelVisibleHeight()
		// 切片可见行
		if m.panelScrollY+visibleH > len(rawLines) {
			m.panelScrollY = max(0, len(rawLines)-visibleH)
		}
		end := m.panelScrollY + visibleH
		if end > len(rawLines) {
			end = len(rawLines)
		}
		visible := rawLines[m.panelScrollY:end]
		panelContent := strings.Join(visible, "\n")
		// PanelBox 包裹（边框在切片之后，保证完整显示）
		boxedContent := m.styles.PanelBox.Render(panelContent)
		content = fmt.Sprintf(
			"%s\n%s%s%s",
			titleBar,
			boxedContent,
			panelFooter,
			toastStr,
		)
	} else {
		// 输入区
		var status string
		if m.typing || m.progress != nil {
			// 显示 spinner + 进度信息
			status = thinkingStatusStyle.Render(m.renderProgressStatus(progressStyle, toolStyle))
		} else if m.checkingUpdate {
			status = thinkingStatusStyle.Render(m.locale.CheckingUpdates)
		} else if completionsHint != "" {
			// 显示补全候选提示
			status = completionsHint
		} else {
			// 就绪态：显示消息计数 + 当前模型（如果有覆盖）
			readyParts := []string{m.locale.StatusReady}
			// Session indicator (for agent sessions)
			if m.channelName == "agent" {
				// Extract role/instance from chatID format: "channel:chatID/role:instance"
				parts := strings.SplitN(m.chatID, "/", 2)
				if len(parts) == 2 {
					readyParts = append(readyParts, fmt.Sprintf("🤖 %s", parts[1]))
				} else {
					readyParts = append(readyParts, fmt.Sprintf("🤖 %s", m.chatID))
				}
			}
			// 消息计数
			msgCount := len(m.messages)
			if msgCount > 0 {
				readyParts = append(readyParts, fmt.Sprintf("%d msg%s", msgCount, func() string {
					if msgCount > 1 {
						return "s"
					}
					return ""
				}()))
			}
			// 模型名称（使用缓存，避免每次 View() 重复查找）
			if m.cachedModelName != "" {
				modelHint := m.cachedModelName
				if m.modelCount > 1 {
					modelHint += " [Ctrl+N]"
				}
				readyParts = append(readyParts, modelHint)
			}
			status = readyStatusStyle.Render(strings.Join(readyParts, " · "))
		}
		// 临时状态提示（自动过期）
		if m.tempStatus != "" {
			ts := m.styles.WarningSt.Render(m.tempStatus)
			if status != "" {
				status += "  " + ts
			} else {
				status = ts
			}
		}
		// 新消息提示：用户上滚且有新内容时显示
		if m.newContentHint {
			hint := m.styles.InfoSt.Render(m.locale.NewContentHint)
			if status != "" {
				status += "  " + hint
			} else {
				status = hint
			}
		}
		// Background task indicator
		if m.bgTaskCount > 0 {
			bgHint := m.styles.WarningSt.Render(
				fmt.Sprintf(m.locale.BgTaskRunning, m.bgTaskCount))
			if status != "" {
				status += "  " + bgHint
			} else {
				status = bgHint
			}
		}
		// Agent indicator
		if m.agentCount > 0 {
			agentHint := m.styles.WarningSt.Render(
				fmt.Sprintf(m.locale.AgentRunning, m.agentCount))
			if status != "" {
				status += "  " + agentHint
			} else {
				status = agentHint
			}
		}
		// Message queue indicator (persistent, not temp status)
		if len(m.messageQueue) > 0 {
			queueHint := m.styles.InfoSt.Render(
				fmt.Sprintf(m.locale.QueuePending, len(m.messageQueue)))
			if status != "" {
				status += "  " + queueHint
			} else {
				status = queueHint
			}
		}

		todoBar := m.renderTodoBar()
		// 底部快捷键提示条（第 4 轮：激活已定义但未使用的 renderFooter）
		footer := m.renderFooter()

		switch {
		case todoBar != "":
			content = fmt.Sprintf(
				"%s\n%s\n%s\n%s\n%s%s",
				titleBar,
				m.viewport.View(),
				status,
				todoBar,
				input,
				toastStr,
			)
		case footer != "":
			content = fmt.Sprintf(
				"%s\n%s\n%s\n%s\n%s%s",
				titleBar,
				m.viewport.View(),
				status,
				footer,
				input,
				toastStr,
			)
		default:
			content = fmt.Sprintf(
				"%s\n%s\n%s\n%s%s",
				titleBar,
				m.viewport.View(),
				status,
				input,
				toastStr,
			)
		}
	}

	v := tea.NewView(content)
	v.AltScreen = true

	// §15 Quick switch overlay (subscription/model picker)
	// Rendered as a centered panel replacing the entire view.
	if m.quickSwitchMode != "" {
		overlay := m.viewQuickSwitch(m.width, m.height)
		if overlay != "" {
			v.Content = overlay
		}
	}

	// §9 Rewind overlay (/rewind command)
	if m.rewindMode {
		overlay := m.viewRewindPanel(m.width, m.height)
		if overlay != "" {
			v.Content = overlay
		}
	}

	return v
}

// allTodosDone returns true when todos exist and every item is marked done.
func (m *cliModel) allTodosDone() bool {
	if len(m.todos) == 0 {
		return false
	}
	for _, t := range m.todos {
		if !t.Done {
			return false
		}
	}
	return true
}

// renderTodoBar renders a compact TODO progress bar between status and input.
// Returns empty string when no todos are active.
func (m *cliModel) renderTodoBar() string {
	if len(m.todos) == 0 {
		return ""
	}

	done := 0
	total := len(m.todos)
	for _, item := range m.todos {
		if item.Done {
			done++
		}
	}

	// All done — still show bar (cleared on next user message)
	// if done == total { return "" }

	// Progress bar: filled portion
	barWidth := 20
	filled := 0
	if total > 0 {
		filled = done * barWidth / total
	}

	barFilled := strings.Repeat("█", filled)
	barEmpty := strings.Repeat("░", barWidth-filled)

	// §20
	s := &m.styles
	todoLabelSt := s.TodoLabel
	todoBarFilledSt := s.TodoFilled
	todoBarEmptySt := s.TodoEmpty
	todoDoneSt := s.TodoDone
	todoPendingSt := s.TodoPending

	var sb strings.Builder
	// Header: TODO label + count + progress bar
	sb.WriteString(todoLabelSt.Render(" TODO "))
	fmt.Fprintf(&sb, "%d/%d ", done, total)
	sb.WriteString(todoBarFilledSt.Render(barFilled))
	sb.WriteString(todoBarEmptySt.Render(barEmpty))
	sb.WriteString("\n")
	// Items
	for i, item := range m.todos {
		text := item.Text
		if utf8.RuneCountInString(text) > 60 {
			text = string([]rune(text)[:59]) + "…"
		}
		if item.Done {
			sb.WriteString("  ")
			sb.WriteString(todoDoneSt.Render("✓"))
			sb.WriteString(" ")
			sb.WriteString(todoPendingSt.Render(text))
		} else {
			sb.WriteString("  ")
			sb.WriteString(todoLabelSt.Render("○"))
			sb.WriteString(" ")
			sb.WriteString(todoPendingSt.Render(text))
		}
		if i < len(m.todos)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// titleText 生成标题栏文字。
func (m *cliModel) titleText() string {
	modeLabel := "⌂ xbot"
	if m.remoteMode {
		host := m.remoteServerURL
		// Strip scheme for display: "ws://host:port" → "host:port"
		if u, err := url.Parse(host); err == nil && u.Host != "" {
			host = u.Host
		}
		// Connection state via plain Unicode symbol (no ANSI — colors break titleBar background)
		var cloud string
		switch m.connState {
		case "connected":
			cloud = "☁"
		case "disconnected":
			cloud = "⊘"
		case "reconnecting":
			cloud = "◌"
		default:
			cloud = "☁"
		}
		if host != "" {
			modeLabel = fmt.Sprintf("%s xbot %s", cloud, host)
		} else {
			modeLabel = fmt.Sprintf("%s xbot remote", cloud)
		}
	}
	if m.workDir != "" {
		// Resolve to absolute path so "." → actual directory name
		abs, err := filepath.Abs(m.workDir)
		if err == nil {
			return fmt.Sprintf(" %s [%s]", modeLabel, filepath.Base(abs))
		}
		return fmt.Sprintf(" %s [%s]", modeLabel, filepath.Base(m.workDir))
	}
	return " " + modeLabel
}

// ---------------------------------------------------------------------------
// §14 Dynamic title bar hints
// ---------------------------------------------------------------------------

// askUserTitleHints returns the minimal control hints for the askuser panel,
// displayed in the header bar so they're always visible regardless of scroll.
// Keep it short — header width is limited and line wrap looks terrible.
func (m *cliModel) askUserTitleHints() string {
	hints := []string{"Shift+↑↓ history", "Ctrl+↑↓ question", "Enter submit", "Esc cancel"}
	if len(m.panelItems) > 1 {
		hints = append([]string{"←→/Tab switch"}, hints...)
	}
	return strings.Join(hints, " · ")
}

// ---------------------------------------------------------------------------
// §14 启动画面 (Splash Screen)
// ---------------------------------------------------------------------------

// xbotLogo — "XBOT" ASCII art（slant 字体，figlet 生成）
var xbotLogo = []string{
	"   _  __    ____    ____    ______",
	"  | |/ /   / __ )  / __ \\  /_  __/",
	"  |   /   / __  | / / / /   / /",
	" /   |   / /_/ / / /_/ /   / /",
	"/_/|_|  /_____/  \\____/   /_/",
}

// renderSplash 渲染启动画面 — 品牌 logo + 版本号 + 加载动画
func (m *cliModel) renderSplash() string {
	// 中心化计算
	screenW := m.width
	if screenW < 40 {
		screenW = 40
	}
	screenH := m.height
	if screenH < 10 {
		screenH = 10
	}

	// §20 使用缓存样式
	logoStyle := m.styles.Accent.Bold(true)
	versionStyle := m.styles.VersionSt
	descStyle := m.styles.TextMutedSt
	loadingStyle := m.styles.WarningSt

	// 组装 splash 内容（logo 按最宽行整体居中，保持字母内部对齐）
	var lines []string
	maxLogoW := 0
	renderedLogo := make([]string, len(xbotLogo))
	for i, line := range xbotLogo {
		renderedLogo[i] = logoStyle.Render(line)
		if w := lipgloss.Width(renderedLogo[i]); w > maxLogoW {
			maxLogoW = w
		}
	}
	logoPad := (screenW - maxLogoW) / 2
	if logoPad < 0 {
		logoPad = 0
	}
	for _, line := range renderedLogo {
		lines = append(lines, strings.Repeat(" ", logoPad)+line)
	}

	// 空行
	lines = append(lines, "")

	// 版本号居中
	versionText := versionStyle.Render(fmt.Sprintf("xbot %s · %s", version.Version, version.Commit))
	vW := lipgloss.Width(versionText)
	vPad := (screenW - vW) / 2
	if vPad < 0 {
		vPad = 0
	}
	lines = append(lines, strings.Repeat(" ", vPad)+versionText)

	// 描述居中（节日版彩蛋）
	splashDesc := m.locale.SplashDesc
	if holidayDesc := getHolidaySplashDesc(); holidayDesc != "" {
		splashDesc = holidayDesc
	}
	descText := descStyle.Render(splashDesc)
	dW := lipgloss.Width(descText)
	dPad := (screenW - dW) / 2
	if dPad < 0 {
		dPad = 0
	}
	lines = append(lines, strings.Repeat(" ", dPad)+descText)

	// 空行
	lines = append(lines, "")

	// 加载动画
	frame := splashFrames[m.splashFrame%len(splashFrames)]
	loadingText := loadingStyle.Render(fmt.Sprintf(m.locale.SplashLoading, frame))
	lW := lipgloss.Width(loadingText)
	lPad := (screenW - lW) / 2
	if lPad < 0 {
		lPad = 0
	}
	lines = append(lines, strings.Repeat(" ", lPad)+loadingText)

	// 垂直居中
	emptyLinesBefore := (screenH - len(lines)) / 2
	if emptyLinesBefore < 2 {
		emptyLinesBefore = 2
	}

	var sb strings.Builder
	for i := 0; i < emptyLinesBefore; i++ {
		sb.WriteString("\n")
	}
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderSuLoading 渲染 /su 切换用户后的历史加载画面（复用 splash 动画帧）
func (m *cliModel) renderSuLoading() string {
	screenW := m.width
	if screenW < 40 {
		screenW = 40
	}
	screenH := m.height
	if screenH < 10 {
		screenH = 10
	}

	loadingStyle := m.styles.WarningSt
	descStyle := m.styles.TextMutedSt

	// 居中内容
	var lines []string
	frame := splashFrames[m.splashFrame%len(splashFrames)]

	// 切换目标提示
	suText := descStyle.Render(fmt.Sprintf(m.locale.SuSwitching, m.senderID))
	suW := lipgloss.Width(suText)
	suPad := (screenW - suW) / 2
	if suPad < 0 {
		suPad = 0
	}
	lines = append(lines, strings.Repeat(" ", suPad)+suText)

	// 空行
	lines = append(lines, "")

	// 加载动画
	loadingText := loadingStyle.Render(fmt.Sprintf(m.locale.SuLoadingHistory, frame))
	lW := lipgloss.Width(loadingText)
	lPad := (screenW - lW) / 2
	if lPad < 0 {
		lPad = 0
	}
	lines = append(lines, strings.Repeat(" ", lPad)+loadingText)

	// 垂直居中
	emptyLinesBefore := (screenH - len(lines)) / 2
	if emptyLinesBefore < 3 {
		emptyLinesBefore = 3
	}

	var sb strings.Builder
	for i := 0; i < emptyLinesBefore; i++ {
		sb.WriteString("\n")
	}
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// ---------------------------------------------------------------------------
// §15 底部快捷键提示条 (Footer Bar)
// ---------------------------------------------------------------------------

// renderFooter 渲染底部快捷键提示条。
// 根据当前状态动态显示最相关的快捷键，避免信息过载。
func (m *cliModel) renderFooter() string {
	// 收集当前上下文最相关的快捷键提示
	var hints []string

	if m.panelMode != "" {
		// 面板打开时：显示面板相关快捷键
		switch m.panelMode {
		case "bgtasks":
			if m.panelBgViewing {
				hints = append(hints, m.keyHint("PgUp/PgDn", m.locale.FooterScroll), m.keyHint("Esc", m.locale.FooterBack))
			} else {
				hints = append(hints, m.keyHint("↑↓", m.locale.FooterNavigate), m.keyHint("Enter", m.locale.FooterLog), m.keyHint("Del", m.locale.FooterKill), m.keyHint("Esc", m.locale.FooterClose))
			}
		case "approval":
			hints = append(hints, m.keyHint("←→", m.locale.FooterNavigate), m.keyHint("y/n", "Quick"), m.keyHint("Enter", m.locale.FooterSelect), m.keyHint("Esc", "Deny"))
		default:
			hints = append(hints, m.keyHint("↑↓", m.locale.FooterNavigate), m.keyHint("Enter", m.locale.FooterSelect), m.keyHint("Esc", m.locale.FooterClose))
		}
	} else if m.typing {
		// 处理中：显示取消快捷键
		hints = append(hints, m.ctrlKey("c", m.locale.FooterCancel))
	} else {
		// 就绪态：显示核心快捷键
		if m.textarea.Value() == "" {
			hints = append(hints, m.ctrlKey("k", m.locale.FooterDelete), m.keyHint("/", m.locale.FooterCommands), m.keyHint("tab", m.locale.FooterComplete), m.ctrlKey("e", m.locale.FooterFold))
			if m.subscriptionMgr != nil {
				hints = append(hints, m.ctrlKey("p", "Subs"))
			}
			hints = append(hints, m.ctrlKey("t", "Sessions"))
			if m.bgTaskCount > 0 {
				hints = append(hints, m.keyHint("^", m.locale.FooterBgTasks))
			}
		} else {
			hints = append(hints, m.ctrlKey("j", m.locale.FooterNewline), m.keyHint("tab", m.locale.FooterComplete), m.ctrlKey("k", m.locale.FooterDelete))
		}
	}

	if len(hints) == 0 {
		return ""
	}

	// §20 使用缓存样式
	helpHint := m.styles.TextMutedSt.Render("/help")
	ellipsis := m.styles.TextMutedSt.Render("…")
	ellipsisW := lipgloss.Width(ellipsis)
	// Progressively drop hints from the end until the footer fits.
	// The rightmost "/help" is always preserved; extra hints are trimmed
	// and replaced with "…" when the terminal is too narrow.
	for len(hints) > 0 {
		footerText := strings.Join(hints, "  ")
		footerText = padBetween(footerText, helpHint, m.width)
		if lipgloss.Width(footerText) <= m.width {
			return m.styles.Footer.Width(m.width).Render(footerText)
		}
		hints = hints[:len(hints)-1]
	}
	// Even a single hint overflows — show just "… /help"
	return m.styles.Footer.Width(m.width).Render(
		padBetween(ellipsis, helpHint, max(ellipsisW+lipgloss.Width(helpHint)+1, m.width)))
}

// ctrlKey 渲染 Ctrl+X 快捷键标签（灰色键帽 + 彩色描述）
func (m *cliModel) ctrlKey(key string, desc string) string {
	k := m.styles.KeyLabelSt.Render("Ctrl+" + key)
	d := m.styles.KeyDescSt.Render(desc)
	return k + " " + d
}

// keyHint 渲染普通按键标签
func (m *cliModel) keyHint(key, desc string) string {
	k := m.styles.KeyLabelSt.Render(key)
	d := m.styles.KeyDescSt.Render(desc)
	return k + " " + d
}

// padBetween 在左右文本之间填充空格，使总宽度达到 width
func padBetween(left, right string, width int) string {
	w := lipgloss.Width(left) + lipgloss.Width(right)
	if w >= width {
		return left + " " + right
	}
	return left + strings.Repeat(" ", width-w) + right
}

// renderToast 渲染底部 Toast 通知堆叠（§16）。
// 支持多条 toast 排队显示，最多同时渲染 3 条，3 秒轮换。
// 浮在界面最底部，使用 Surface 背景与主题保持一致。
func (m *cliModel) renderToast() string {
	if len(m.toasts) == 0 {
		return ""
	}

	// 最多显示 3 条
	showCount := len(m.toasts)
	if showCount > 3 {
		showCount = 3
	}

	var lines []string
	for i := 0; i < showCount; i++ {
		item := m.toasts[i]

		iconSty := m.styles.ToastIcon
		switch item.icon {
		case "✗", "⚠":
			iconSty = iconSty.Foreground(lipgloss.Color(currentTheme.Error))
		case "ℹ":
			iconSty = iconSty.Foreground(lipgloss.Color(currentTheme.Info))
		}

		// 越靠后越透明（营造层级感）
		faintFactor := i // 0=最新最亮, 1=稍暗, 2=最暗
		if faintFactor > 0 {
			iconSty = iconSty.Faint(true)
		}
		textSty := m.styles.ToastText
		if faintFactor > 0 {
			textSty = textSty.Faint(true)
		}

		toastContent := iconSty.Render(" "+item.icon+" ") + " " + textSty.Render(item.text)
		lines = append(lines, m.styles.ToastBg.Render(toastContent))
	}

	return "\n" + strings.Join(lines, "\n")
}

// renderProgressStatus renders a compact one-line status for the status bar.
func (m *cliModel) renderProgressStatus(progressStyle, toolStyle lipgloss.Style) string {
	s := &m.styles // §20
	var sb strings.Builder
	sb.WriteString(s.Progress.Render(m.ticker.view()))
	sb.WriteString(" ")

	if m.progress != nil {
		fmt.Fprintf(&sb, "#%d", m.progress.Iteration)

		// Phase hint (active tool is shown in progress block, skip here to avoid duplication)
		switch m.progress.Phase {
		case "thinking":
			sb.WriteString(" · " + m.pickVerb(m.ticker.ticks))
		case "compressing":
			sb.WriteString(" · " + m.locale.StatusCompressing)
		case "retrying":
			sb.WriteString(" · " + m.locale.StatusRetrying)
		default:
			if len(m.progress.CompletedTools) > 0 {
				sb.WriteString(" · " + m.locale.StatusDone)
			}
		}
	} else {
		sb.WriteString(m.pickVerb(m.ticker.ticks) + "...")
	}

	// Total elapsed
	if !m.typingStartTime.IsZero() {
		elapsed := time.Since(m.typingStartTime).Milliseconds()
		sb.WriteString(" · ")
		sb.WriteString(formatElapsed(elapsed))
	}

	// §18 Token 使用量显示
	if m.progress != nil && m.progress.TokenUsage != nil && m.progress.TokenUsage.TotalTokens > 0 {
		tu := m.progress.TokenUsage
		// §20 tokenStyle → s.TokenUsage
		sb.WriteString(" · ")
		sb.WriteString(s.TokenUsage.Render(formatTokenCount(tu)))
	}

	return sb.String()
}

// formatTokenCount 格式化 Token 使用量为紧凑字符串
func formatTokenCount(tu *CLITokenUsage) string {
	if tu.TotalTokens < 1000 {
		return fmt.Sprintf("tokens: %d", tu.TotalTokens)
	}
	parts := []string{}
	if tu.PromptTokens > 0 {
		parts = append(parts, fmt.Sprintf("in:%d", tu.PromptTokens))
	}
	if tu.CompletionTokens > 0 {
		parts = append(parts, fmt.Sprintf("out:%d", tu.CompletionTokens))
	}
	if len(parts) > 0 {
		return "tokens: " + strings.Join(parts, " ") + fmt.Sprintf(" = %d", tu.TotalTokens)
	}
	return fmt.Sprintf("tokens: %d", tu.TotalTokens)
}

// ---------------------------------------------------------------------------
