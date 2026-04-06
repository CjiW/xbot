package channel

import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// Refactored common patterns (方案 B: 提取重复代码)
// ---------------------------------------------------------------------------

// invalidateAllCache marks the render cache invalid, dirties all messages,
// and optionally updates the viewport content.
// This pattern appears in theme change, locale change, resize, and tool-summary toggle.
func (m *cliModel) invalidateAllCache(updateViewport bool) {
	m.renderCacheValid = false
	for i := range m.messages {
		m.messages[i].dirty = true
	}
	if updateViewport {
		m.updateViewportContent()
	}
}

// toggleToolSummary toggles the tool-summary expanded state,
// invalidates all cached rendering, clears cachedHistory, and refreshes the viewport.
func (m *cliModel) toggleToolSummary() {
	m.toolSummaryExpanded = !m.toolSummaryExpanded
	m.cachedHistory = ""
	m.invalidateAllCache(true)
}

// startAgentTurn transitions the model into the "agent processing" state:
// sets typing=true, updates placeholder, disables input, and resets progress.
func (m *cliModel) startAgentTurn() {
	m.typing = true
	m.updatePlaceholder()
	m.inputReady = false
	m.resetProgressState()
}

// flushMessageQueue sends the first queued message (if any) when input becomes ready.
// Returns a tea.Cmd to send the message, or nil if queue is empty.
func (m *cliModel) flushMessageQueue() tea.Cmd {
	if len(m.messageQueue) == 0 {
		return nil
	}
	msg := m.messageQueue[0]
	m.messageQueue = m.messageQueue[1:]
	m.queueEditing = false
	m.queueEditBuf = ""
	// Put message into textarea and trigger send
	m.textarea.SetValue(msg)
	return m.sendMessageFromQueue()
}

// sendMessageFromQueue sends the current textarea content as a queued message.
func (m *cliModel) sendMessageFromQueue() tea.Cmd {
	content := strings.TrimSpace(m.textarea.Value())
	if content == "" {
		return nil
	}
	m.textarea.Reset()
	m.autoExpandInput()
	m.sendToAgent(content)
	return nil
}

// applyThemeAndRebuild applies a theme change synchronously: sets the theme,
// rebuilds styles cache, glamour renderer, and marks all messages dirty.
// Uses setTheme() instead of ApplyTheme() to avoid sending on themeChangeCh,
// which would cause a redundant fullRebuild in the next Update cycle.
func (m *cliModel) applyThemeAndRebuild(theme string) {
	setTheme(theme)
	// Rebuild styles cache (same as themeChangeCh handler in Update)
	m.styles = buildStyles(m.width)
	applyTAStyles(&m.textarea, &m.styles)
	m.ticker.style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Warning))
	// Rebuild glamour renderer
	if m.width > 4 {
		m.renderer = newGlamourRenderer(m.width - 4)
	}
	// Mark all messages for re-render (new theme = new styles)
	m.renderCacheValid = false
	for i := range m.messages {
		m.messages[i].dirty = true
	}
}

// syncPanelViewport 将当前 panel 内容写入 panelViewport，统一滚动逻辑。
// 每次 panel 内容变化（打开、cursor 移动、编辑等）后调用。
// firstOpen=true 时滚动到顶部（panel 刚打开）。
func (m *cliModel) syncPanelViewport(firstOpen ...bool) {
	if m.panelMode == "" {
		return
	}
	// 每次都重算高度，确保和终端尺寸同步
	panelH := m.height - 3 // titleBar(1) + panelFooter(1) + toast(~1)
	if panelH < 5 {
		panelH = 5
	}
	m.panelViewport.SetHeight(panelH)
	m.panelViewport.SetWidth(m.width)
	raw := m.viewPanel()
	m.panelViewport.SetContent(raw)
	if len(firstOpen) > 0 && firstOpen[0] {
		m.panelViewport.GotoTop()
	}
}

// applyLanguageChange applies a language/locale change and invalidates cache.
// Uses setLocale() instead of SetLocale() to avoid sending on localeChangeCh,
// which would cause a redundant fullRebuild in the next Update cycle.
func (m *cliModel) applyLanguageChange(lang string) {
	setLocale(lang)
	m.locale = GetLocale(lang)
	m.renderCacheValid = false
}

// doSaveSettingsAsync runs the settings save callback in a goroutine and returns
// a tea.Cmd that sends the result back as cliSettingsSavedMsg.
// This prevents the BubbleTea Update loop from blocking on SQLite writes,
// file I/O, or fullRebuild.
func (m *cliModel) doSaveSettingsAsync(onSubmit func(map[string]string), vals map[string]string) tea.Cmd {
	// Capture the values we need for the deferred UI update
	theme, hasTheme := vals["theme"]
	lang, hasLang := vals["language"]
	model, hasModel := vals["llm_model"]
	baseURL := vals["llm_base_url"]
	// Capture feedback string now (m.locale is only safe to read in Update)
	feedbackMsg := m.locale.SettingsSaved

	return func() tea.Msg {
		// Run the heavy callback (SQLite writes, config.json save, LLM rebuild)
		// in a background goroutine so the UI stays responsive.
		onSubmit(vals)

		return cliSettingsSavedMsg{
			themeChanged: hasTheme && theme != "",
			theme:        theme,
			langChanged:  hasLang,
			lang:         lang,
			modelChanged: hasModel && model != "",
			model:        model,
			baseURL:      baseURL,
			feedbackMsg:  feedbackMsg,
		}
	}
}

// handleSettingsSavedMsg processes the async settings save result.
// Called from Update() to apply theme/locale changes and refresh the viewport.
func (m *cliModel) handleSettingsSavedMsg(msg cliSettingsSavedMsg) tea.Cmd {
	visualChanged := false
	if msg.themeChanged {
		m.applyThemeAndRebuild(msg.theme)
		visualChanged = true
	}
	if msg.langChanged {
		m.applyLanguageChange(msg.lang)
		visualChanged = true
	}
	if msg.modelChanged {
		if m.channel != nil {
			m.channel.UpdateConfig(msg.model, msg.baseURL)
		}
	}
	m.refreshCachedModelName()
	if msg.feedbackMsg != "" {
		m.appendSystem(msg.feedbackMsg)
	}
	if visualChanged {
		m.invalidateAllCache(true)
	} else {
		m.updateViewportContent()
	}
	return nil
}

// submitAskAnswers collects answers from the AskUser panel, invokes the answer
// callback, closes the panel, and returns the appropriate tea.Cmd.
// This pattern appears 3 times in updateAskUserPanel (ctrl+s, Enter with options, Enter without options).
func (m *cliModel) submitAskAnswers() (bool, tea.Model, tea.Cmd) {
	answers := m.collectAskAnswers()
	if m.panelOnAnswer != nil {
		m.panelOnAnswer(answers)
	}
	m.closePanel()
	if m.typing {
		return true, m, tea.Batch(tickerCmd(), tickCmd())
	}
	return true, m, nil
}

// closePanelAndResume closes the active panel and returns the appropriate
// tea.Cmd based on whether the agent is still typing.
// This pattern appears in bgtasks panel (Esc/Ctrl+C), settings panel (Esc),
// and askuser panel (Esc/cancel) handlers.
func (m *cliModel) closePanelAndResume() (bool, tea.Model, tea.Cmd) {
	m.closePanel()
	if m.typing {
		return true, m, tea.Batch(tickerCmd(), tickCmd())
	}
	return true, m, nil
}

// iterToolsFlat returns a flat slice of all tools from either msg.iterations
// or msg.tools, handling the dual-source pattern used in tool_summary rendering.
// If iterations are present, it also counts them. Returns (tools, iterationCount).
func (msg *cliMessage) iterToolsFlat() (tools []CLIToolProgress, iterCount int) {
	if len(msg.iterations) > 0 {
		iterCount = len(msg.iterations)
		for _, it := range msg.iterations {
			tools = append(tools, it.Tools...)
		}
	} else {
		tools = msg.tools
		iterCount = 0
	}
	return
}

// ---------------------------------------------------------------------------
// Unified error & feedback helpers (方案 A: 统一错误处理)
// ---------------------------------------------------------------------------

// feedbackLevel represents the severity level of user feedback.
type feedbackLevel int

const (
	// feedbackInfo indicates an informational message (e.g. "settings saved").
	feedbackInfo feedbackLevel = iota
	// feedbackWarning indicates a non-critical issue (e.g. "waiting for operation").
	feedbackWarning
	// feedbackError indicates an error that needs user attention (e.g. "kill failed").
	feedbackError
)

// showTempStatus displays a temporary status message in the status bar that
// automatically clears after the given duration. This replaces the repetitive
// pattern of setting m.tempStatus + returning a tea.Tick with cliTempStatusClearMsg.
//
// Usage (in Update method):
//
//	m.showTempStatus(m.locale.WaitingOperation)
//	return m, m.clearTempStatusCmd(2 * time.Second)
func (m *cliModel) showTempStatus(text string) {
	m.tempStatus = text
}

// clearTempStatusCmd returns a tea.Cmd that clears the temp status after the
// given duration. The default is 2 seconds.
func (m *cliModel) clearTempStatusCmd(d ...time.Duration) tea.Cmd {
	dur := 2 * time.Second
	if len(d) > 0 {
		dur = d[0]
	}
	return tea.Tick(dur, func(time.Time) tea.Msg { return cliTempStatusClearMsg{} })
}

// showSystemMsg appends a system message to the chat history and refreshes
// the viewport. This is the unified replacement for the scattered pattern of
// appendSystem() + updateViewportContent() calls throughout the codebase.
//
// The level parameter controls the visual presentation:
//   - feedbackInfo: rendered as a normal system message
//   - feedbackWarning / feedbackError: rendered with warning/error styling
//     if the content matches errorKeywords
func (m *cliModel) showSystemMsg(content string, level feedbackLevel) {
	m.appendSystem(content)
	m.updateViewportContent()
}

// isErrorContent checks whether the given content string contains any of the
// predefined error keywords. This extracts the inline error-detection loop
// that was previously duplicated in message rendering.
func isErrorContent(content string) bool {
	lower := toLowerASCII(content)
	for _, kw := range errorKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// toLowerASCII returns a lowercase version of s using only ASCII rules.
// This avoids the overhead of strings.ToLower for keyword matching.
func toLowerASCII(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte(byte(r + 32))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// enqueueToast adds a toast notification to the display queue.
// Toasts appear at the bottom of the screen and auto-dismiss after 3 seconds.
// The icon parameter controls visual style: "✓" (success), "✗"/"⚠" (error), "ℹ" (info).
func (m *cliModel) enqueueToast(text, icon string) tea.Cmd {
	return func() tea.Msg {
		return cliToastMsg{text: text, icon: icon}
	}
}
