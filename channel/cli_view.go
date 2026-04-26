package channel

import (
	"fmt"
	"image/color"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
	"xbot/version"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// appendStatusHint appends a styled hint to the status line, with proper spacing.
func appendStatusHint(status, hint string) string {
	if hint == "" {
		return status
	}
	if status == "" {
		return hint
	}
	return status + "  " + hint
}

// renderTitleBar builds the top title bar with mode label, hints, runner status,
// and user identity indicator.
func (m *cliModel) renderTitleBar() string {
	titleLeft := m.titleText()
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
	return m.styles.TitleBar.Render(titleLeft + strings.Repeat(" ", titlePad) + titleRight)
}

// renderInputArea renders the textarea input box with dynamic border color
// and manual placeholder overlay (avoids textarea's built-in placeholder
// which triggers CJK rendering bugs on Windows Terminal).
func (m *cliModel) renderInputArea(borderColor color.Color) string {
	inputBoxStyle := m.styles.InputBox.BorderForeground(borderColor)
	inputArea := m.textarea.View()

	// Render placeholder manually when textarea is empty.
	if m.textarea.Value() == "" && m.placeholderText != "" {
		taHeight := minTaHeight
		if h := m.textarea.Height(); h > 0 {
			taHeight = h
		}
		ph := m.placeholderText
		if tw := m.textarea.Width(); tw > 0 {
			ph = truncateToWidth(ph, tw)
		}
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

	return inputBoxStyle.Render(inputArea)
}

// renderReadyStatus builds the "Ready" status bar line with message count,
// model name, agent session indicator, and right-aligned context usage bar.
func (m *cliModel) renderReadyStatus() string {
	readyParts := []string{m.locale.StatusReady}
	// Session indicator (for agent sessions)
	if m.channelName == "agent" {
		parts := strings.SplitN(m.chatID, "/", 2)
		if len(parts) == 2 {
			readyParts = append(readyParts, fmt.Sprintf("🤖 %s", parts[1]))
		} else {
			readyParts = append(readyParts, fmt.Sprintf("🤖 %s", m.chatID))
		}
	}
	// Message count
	msgCount := len(m.messages)
	if msgCount > 0 {
		s := ""
		if msgCount > 1 {
			s = "s"
		}
		readyParts = append(readyParts, fmt.Sprintf("%d msg%s", msgCount, s))
	}
	// Model name (cached, avoids per-frame lookup)
	if m.cachedModelName != "" {
		modelHint := m.cachedModelName
		if m.modelCount > 1 {
			modelHint += " [Ctrl+N]"
		}
		readyParts = append(readyParts, modelHint)
	}
	leftParts := strings.Join(readyParts, " · ")

	// Context usage bar (right-aligned)
	if ctxBar := m.renderContextUsage(); ctxBar != "" {
		return m.styles.ReadyStatus.Render(padBetween(leftParts, ctxBar, m.width))
	}
	return m.styles.ReadyStatus.Render(leftParts)
}

// layoutSearch renders the search-mode layout: title bar, viewport, search bar,
// input box, and toast.
func (m *cliModel) layoutSearch(titleBar, input, toast string) string {
	var searchBar string
	if m.searchEditing {
		searchBar = m.styles.SearchBar.Render(m.searchTI.View())
	} else {
		searchBar = m.styles.SearchBar.Render(
			fmt.Sprintf(m.locale.SearchNavFormat, m.searchQuery, m.searchIdx+1, len(m.searchResults)))
	}
	return fmt.Sprintf("%s\n%s\n%s\n%s%s",
		titleBar, m.viewport.View(), searchBar, input, toast)
}

// layoutAskUser renders the askuser panel layout: title bar, viewport,
// scrollable ask panel with progress indicator, and toast.
func (m *cliModel) layoutAskUser(titleBar, toast string) string {
	askRaw := m.viewAskUserPanel()
	m.clampAskUserPanelScroll(askRaw)
	askLines := strings.Split(askRaw, "\n")
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
	scrollHint := ""
	totalAskLines := len(askLines)
	if totalAskLines > askVisibleH {
		pct := (m.askPanelScrollY + askVisibleH) * 100 / totalAskLines
		scrollHint = m.styles.PanelDesc.Render(fmt.Sprintf(" [%d%%] Ctrl+↑↓/PgUp/PgDn", pct))
	}
	return fmt.Sprintf("%s\n%s\n%s%s%s",
		titleBar, m.viewport.View(), boxedAsk, scrollHint, toast)
}

// layoutPanel renders the generic panel-mode layout: title bar, scrollable
// panel content in a bordered box, panel footer, and toast.
func (m *cliModel) layoutPanel(titleBar, toast string) string {
	panelFooter := m.renderFooter()
	rawContent := m.viewPanel()
	m.clampPanelScroll(rawContent)
	rawLines := strings.Split(rawContent, "\n")
	visibleH := m.panelVisibleHeight()
	if m.panelScrollY+visibleH > len(rawLines) {
		m.panelScrollY = max(0, len(rawLines)-visibleH)
	}
	end := m.panelScrollY + visibleH
	if end > len(rawLines) {
		end = len(rawLines)
	}
	visible := rawLines[m.panelScrollY:end]
	panelContent := strings.Join(visible, "\n")
	boxedContent := m.styles.PanelBox.Render(panelContent)
	return fmt.Sprintf("%s\n%s%s%s",
		titleBar, boxedContent, panelFooter, toast)
}

// layoutMain renders the primary chat layout: title bar, viewport, status bar
// (with hints for temp status, new content, background tasks, agents, and queue),
// optional footer/todo bar, input box, and toast.
func (m *cliModel) layoutMain(titleBar, input, toast, completionsHint string) string {
	// Render status bar
	var status string
	if m.typing || m.progress != nil {
		thinkingStatusStyle := m.styles.ThinkingSt
		progressStyle := m.styles.Progress
		toolStyle := m.styles.Tool
		status = thinkingStatusStyle.Render(m.renderProgressStatus(progressStyle, toolStyle))
	} else if m.checkingUpdate {
		status = m.styles.ThinkingSt.Render(m.locale.CheckingUpdates)
	} else if completionsHint != "" {
		status = completionsHint
	} else {
		status = m.renderReadyStatus()
	}

	// Accumulate status hints
	var hints []string
	if m.tempStatus != "" {
		hints = append(hints, m.styles.WarningSt.Render(m.tempStatus))
	}
	if m.newContentHint {
		hints = append(hints, m.styles.InfoSt.Render(m.locale.NewContentHint))
	}
	if m.bgTaskCount > 0 {
		hints = append(hints, m.styles.WarningSt.Render(fmt.Sprintf(m.locale.BgTaskRunning, m.bgTaskCount)))
	}
	if m.agentCount > 0 {
		hints = append(hints, m.styles.WarningSt.Render(fmt.Sprintf(m.locale.AgentRunning, m.agentCount)))
	}
	if len(m.messageQueue) > 0 {
		hints = append(hints, m.styles.InfoSt.Render(fmt.Sprintf(m.locale.QueuePending, len(m.messageQueue))))
	}
	if len(hints) > 0 {
		status = appendStatusHint(status, strings.Join(hints, "  "))
	}

	// Layout assembly
	todoBar := m.renderTodoBar()
	footer := m.renderFooter()

	switch {
	case todoBar != "":
		return fmt.Sprintf("%s\n%s\n%s\n%s\n%s%s",
			titleBar, m.viewport.View(), status, todoBar, input, toast)
	case footer != "":
		return fmt.Sprintf("%s\n%s\n%s\n%s\n%s%s",
			titleBar, m.viewport.View(), status, footer, input, toast)
	default:
		return fmt.Sprintf("%s\n%s\n%s\n%s%s",
			titleBar, m.viewport.View(), status, input, toast)
	}
}

// View renders the CLI interface.
func (m *cliModel) View() tea.View {
	// Splash screen
	if !m.splashDone {
		v := tea.NewView(m.renderSplash())
		v.AltScreen = true
		return v
	}
	if !m.ready {
		v := tea.NewView("\n  " + m.locale.SplashLoading)
		v.AltScreen = true
		return v
	}

	// Easter egg overlay
	if m.easterEgg != easterEggNone {
		v := tea.NewView(m.renderEasterEggOverlay())
		v.AltScreen = true
		return v
	}

	// /su loading
	if m.suLoading {
		v := tea.NewView(m.renderSuLoading())
		v.AltScreen = true
		return v
	}

	// Build shared components
	titleBar := m.renderTitleBar()
	borderColor, completionsHint := m.renderCompletionsHint(m.textarea.Value())
	input := m.renderInputArea(borderColor)
	toast := m.renderToast()

	// Layout selection
	var content string
	switch {
	case m.searchMode:
		content = m.layoutSearch(titleBar, input, toast)
	case m.panelMode == "askuser":
		content = m.layoutAskUser(titleBar, toast)
	case m.panelMode != "":
		content = m.layoutPanel(titleBar, toast)
	default:
		content = m.layoutMain(titleBar, input, toast, completionsHint)
	}

	v := tea.NewView(content)
	v.AltScreen = true

	// Quick switch overlay
	if m.quickSwitchMode != "" {
		if overlay := m.viewQuickSwitch(m.width, m.height); overlay != "" {
			v.Content = overlay
		}
	}

	// Rewind overlay
	if m.rewindMode {
		if overlay := m.viewRewindPanel(m.width, m.height); overlay != "" {
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

		// Phase hint
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

	// §18 Context usage bar — right-aligned so position stays fixed
	var ctxBar string
	if ctxHint := m.renderContextUsage(); ctxHint != "" {
		ctxBar = ctxHint
	} else if m.progress != nil && m.progress.TokenUsage != nil && m.progress.TokenUsage.TotalTokens > 0 {
		// Fallback: raw token count when context bar data is not yet available
		tu := m.progress.TokenUsage
		ctxBar = s.TokenUsage.Render(formatTokenCount(tu))
	}

	// Queue indicator (persistent during typing, not just temp status)
	if len(m.messageQueue) > 0 {
		sb.WriteString(" · ")
		fmt.Fprintf(&sb, m.locale.QueuePending, len(m.messageQueue))
	}

	leftText := sb.String()
	if ctxBar != "" {
		return padBetween(leftText, ctxBar, m.width)
	}
	return leftText
}

// formatTokenCount formats token usage for the fallback status line (when context bar is unavailable).
// Shows prompt tokens (context fill) as the primary metric.
func formatTokenCount(tu *CLITokenUsage) string {
	if tu.PromptTokens < 1000 {
		return fmt.Sprintf("ctx: %d tokens", tu.PromptTokens)
	}
	return fmt.Sprintf("ctx: %s", formatTokenCompact(tu.PromptTokens))
}

// Pre-created styles for context bar (avoid per-frame allocation).
var ctxBarStyles = struct {
	fillGreen  lipgloss.Style
	fillYellow lipgloss.Style
	fillRed    lipgloss.Style
	dim        lipgloss.Style
	empty      lipgloss.Style
	threshold  lipgloss.Style
	label      lipgloss.Style
	pctGreen   lipgloss.Style
	pctYellow  lipgloss.Style
	pctRed     lipgloss.Style
}{
	fillGreen:  lipgloss.NewStyle().Foreground(lipgloss.Color("#6bcb77")),
	fillYellow: lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd93d")),
	fillRed:    lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b6b")),
	dim:        lipgloss.NewStyle().Foreground(lipgloss.Color("#555555")).Faint(true),
	empty:      lipgloss.NewStyle().Foreground(lipgloss.Color("#333333")),
	threshold:  lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b6b")).Bold(true),
	label:      lipgloss.NewStyle().Foreground(lipgloss.Color("#888888")),
	pctGreen:   lipgloss.NewStyle().Foreground(lipgloss.Color("#6bcb77")),
	pctYellow:  lipgloss.NewStyle().Foreground(lipgloss.Color("#ffd93d")),
	pctRed:     lipgloss.NewStyle().Foreground(lipgloss.Color("#ff6b6b")),
}

// renderContextUsage returns a context usage bar for the status bar.
// Uses cached rendering — only recomputes when token data or terminal width changes.
//
// Shows a segmented progress bar with:
//   - Filled portion (prompt tokens used) — color-coded by fill ratio
//   - Output reservation marker (right-side dim segment)
//   - Compression threshold marker
//   - Numeric label: "prompt/budget"
func (m *cliModel) renderContextUsage() string {
	if m.lastTokenUsage == nil || m.cachedMaxContextTokens <= 0 {
		return ""
	}
	promptTokens := m.lastTokenUsage.PromptTokens
	maxTokens := int64(m.cachedMaxContextTokens)
	if promptTokens <= 0 || maxTokens <= 0 {
		return ""
	}

	// Cache key: re-render only when data changes
	cacheKey := fmt.Sprintf("%d:%d:%d:%d:%g", promptTokens, maxTokens, m.cachedMaxOutputTokens, m.width, m.cachedCompressRatio)
	if cacheKey == m.ctxBarCacheKey && m.ctxBarCache != "" {
		return m.ctxBarCache
	}

	maxOutputTokens := m.cachedMaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = 8192
	}
	promptBudget := maxTokens - maxOutputTokens
	if promptBudget <= 0 {
		promptBudget = maxTokens / 2
	}

	// Use cached compression ratio (set during progress events), not GetCurrentValues()
	compressRatio := m.cachedCompressRatio
	if compressRatio <= 0 {
		compressRatio = 0.9
	}
	compressThreshold := int64(float64(promptBudget) * compressRatio)

	// Bar width: adapt to terminal width
	barWidth := 20
	if m.width > 120 {
		barWidth = 30
	}
	if m.width < 80 {
		barWidth = 12
	}

	promptFill := promptTokens
	if promptFill > maxTokens {
		promptFill = maxTokens
	}
	filledCells := int(float64(barWidth) * float64(promptFill) / float64(maxTokens))
	if filledCells > barWidth {
		filledCells = barWidth
	}

	outputCells := int(float64(barWidth) * float64(maxOutputTokens) / float64(maxTokens))
	if outputCells < 1 {
		outputCells = 1
	}
	if outputCells > barWidth-1 {
		outputCells = barWidth - 1
	}

	compressPos := int(float64(barWidth) * float64(compressThreshold) / float64(maxTokens))
	if compressPos < 1 {
		compressPos = 1
	}
	if compressPos >= barWidth {
		compressPos = barWidth - 1
	}

	pct := float64(promptTokens) / float64(maxTokens) * 100

	// Select pre-created style (no allocation)
	var fillColor, pctStyle lipgloss.Style
	switch {
	case pct > 80:
		fillColor = ctxBarStyles.fillRed
		pctStyle = ctxBarStyles.pctRed
	case pct > 50:
		fillColor = ctxBarStyles.fillYellow
		pctStyle = ctxBarStyles.pctYellow
	default:
		fillColor = ctxBarStyles.fillGreen
		pctStyle = ctxBarStyles.pctGreen
	}

	// Build bar runes first (single allocation)
	outputStart := barWidth - outputCells
	if outputStart < filledCells {
		outputStart = filledCells
	}

	// Render bar segments as batch strings (3 segments max, not barWidth calls)
	var barStr strings.Builder
	// Segment 1: filled area
	if filledCells > 0 {
		barStr.WriteString(fillColor.Render(strings.Repeat("█", filledCells)))
	}
	// Segment 2: empty area (may contain threshold marker)
	emptyStart := filledCells
	emptyEnd := outputStart
	if emptyEnd > emptyStart {
		if compressPos >= emptyStart && compressPos < emptyEnd {
			// Split empty into two parts around threshold marker
			before := compressPos - emptyStart
			after := emptyEnd - compressPos - 1
			if before > 0 {
				barStr.WriteString(ctxBarStyles.empty.Render(strings.Repeat("░", before)))
			}
			barStr.WriteString(ctxBarStyles.threshold.Render("┊"))
			if after > 0 {
				barStr.WriteString(ctxBarStyles.empty.Render(strings.Repeat("░", after)))
			}
		} else {
			barStr.WriteString(ctxBarStyles.empty.Render(strings.Repeat("░", emptyEnd-emptyStart)))
		}
	}
	// Segment 3: output reservation
	if barWidth-outputStart > 0 {
		barStr.WriteString(ctxBarStyles.dim.Render(strings.Repeat("▒", barWidth-outputStart)))
	}

	pctStr := pctStyle.Render(fmt.Sprintf("%.0f%%", pct))

	var result string
	if m.width < 100 {
		result = fmt.Sprintf("%s %s", pctStr, barStr.String())
	} else {
		usageStr := formatTokenCompact(promptTokens)
		budgetStr := formatTokenCompact(promptBudget)
		label := ctxBarStyles.label.Render(fmt.Sprintf("%s/%s", usageStr, budgetStr))
		result = fmt.Sprintf("%s %s %s", pctStr, barStr.String(), label)
	}

	m.ctxBarCache = result
	m.ctxBarCacheKey = cacheKey
	return result
}

// formatTokenCompact formats token counts as compact human-readable strings.
// e.g. 12500 → "12.5K", 128000 → "128K", 500 → "500"
func formatTokenCompact(tokens int64) string {
	if tokens >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	}
	if tokens >= 1000 {
		val := float64(tokens) / 1000
		if val == float64(int(val)) {
			return fmt.Sprintf("%dK", int(val))
		}
		return fmt.Sprintf("%.1fK", val)
	}
	return fmt.Sprintf("%d", tokens)
}

// ---------------------------------------------------------------------------
