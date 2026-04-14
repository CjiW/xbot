package channel

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
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
// It preserves the viewport scroll position anchored to the first visible message,
// so Ctrl+O doesn't cause a jarring jump when tool summary lines change.
func (m *cliModel) toggleToolSummary() {
	// Find the first visible message index before toggling.
	prevYOffset := m.viewport.YOffset()
	prevAtBottom := m.viewport.AtBottom()
	anchorMsgIdx := -1
	if !prevAtBottom && len(m.msgLineOffsets) > 0 {
		for i := len(m.msgLineOffsets) - 1; i >= 0; i-- {
			if m.msgLineOffsets[i] <= prevYOffset {
				anchorMsgIdx = i
				break
			}
		}
	}

	m.toolSummaryExpanded = !m.toolSummaryExpanded
	m.cachedHistory = ""
	m.invalidateAllCache(true)

	// Restore scroll position anchored to the same message.
	if !prevAtBottom && anchorMsgIdx >= 0 && anchorMsgIdx < len(m.msgLineOffsets) {
		m.viewport.SetYOffset(m.msgLineOffsets[anchorMsgIdx])
	}
}

// openSettingsFromQuickSwitch restores the settings panel after a subscription quick switch.
// The subscription generation guard (in onSubmit) prevents stale LLM fields from being
// written back. Here we only need to refresh LLM display values from the new active
// subscription and preserve global settings from the backup.
func (m *cliModel) openSettingsFromQuickSwitch() {
	if m.channel == nil || len(m.panelValuesBackup) == 0 {
		return
	}
	schema := m.channel.SettingsSchema()
	if len(schema) == 0 {
		return
	}
	// Refresh model list options in the schema (subscription change may affect available models)
	if m.channel.modelLister != nil {
		allModels := m.channel.modelLister.ListAllModels()
		for i, s := range schema {
			if (s.Key == "llm_model" || s.Key == "vanguard_model" || s.Key == "balance_model" || s.Key == "swift_model") && len(allModels) > 0 {
				opts := make([]SettingOption, len(allModels))
				for j, ml := range allModels {
					opts[j] = SettingOption{Label: ml, Value: ml}
				}
				schema[i].Options = opts
			}
		}
	}
	// Re-read ALL values fresh (including LLM fields from new active subscription)
	values := make(map[string]string)
	if m.channel.config.GetCurrentValues != nil {
		for k, v := range m.channel.config.GetCurrentValues() {
			values[k] = v
		}
	}
	if m.channel.settingsSvc != nil {
		vals, err := m.channel.settingsSvc.GetSettings(m.channelName, m.senderID)
		if err == nil {
			for k, v := range vals {
				switch k {
				case "llm_provider", "llm_model", "llm_base_url", "llm_api_key", "vanguard_model", "balance_model", "swift_model":
					continue
				}
				values[k] = v
			}
		}
	}
	// Overlay non-LLM settings from backup (preserves user's in-memory edits for global settings)
	perSubKeys := map[string]bool{
		"llm_provider": true, "llm_api_key": true, "llm_model": true, "llm_base_url": true,
	}
	for k, v := range m.panelValuesBackup {
		if !perSubKeys[k] {
			values[k] = v
		}
	}
	cursor := m.panelCursorBackup
	onSubmit := m.panelOnSubmitBackup
	// Clear backup
	m.panelValuesBackup = nil
	m.panelOnSubmitBackup = nil
	// Open panel with restored state
	m.openSettingsPanel(schema, values, onSubmit)
	m.panelCursor = cursor
}

// startAgentTurn transitions the model into the "agent processing" state:
// sets typing=true, updates placeholder, disables input, resets progress,
// and queues a tick command to ensure the spinner/progress chain starts.
// This is the SINGLE source of truth for tick chain initiation — no other
// code path should emit tickCmd() on idle→typing transition.
func (m *cliModel) startAgentTurn() {
	m.agentTurnID++
	m.typing = true
	// Sync checkpoint hook turn index
	if m.checkpointHook != nil {
		m.checkpointHook.SetTurnIdx(int(m.agentTurnID))
	}
	// Clear rewind result when new turn starts
	m.rewindResult = nil
	m.updatePlaceholder()
	m.inputReady = false
	m.resetProgressState()
	// Queue tickCmd so the next Update() drain picks it up.
	// This guarantees the tick chain starts regardless of any early-return
	// paths in Update() — the cmd will be drained at the top of the next call.
	m.pendingCmds = append(m.pendingCmds, tickCmd())
}

// endAgentTurn resets all agent-turn tracking state and returns to idle.
// Takes the turnID that triggered this end. If a new turn has already
// started (turnID != m.agentTurnID), the call is a no-op — this prevents
// stale completion signals (cliOutboundMsg / PhaseDone) from killing a
// new turn's animation.
func (m *cliModel) endAgentTurn(turnID uint64) {
	if turnID != m.agentTurnID {
		return // new turn already started — stale signal, ignore
	}
	m.lastCompletedTools = nil
	m.iterationHistory = nil
	m.lastSeenIteration = 0
	m.lastReasoning = ""
	m.lastThinking = ""
	m.typingStartTime = time.Time{}
	m.progress = nil
	m.typing = false
	// Refresh agent count so the tick chain continues if agents exist
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}
	m.updatePlaceholder()
}

// flushMessageQueue sends the first queued message (if any) when input becomes ready.
// Returns a tea.Cmd to send the message, or nil if queue is empty.
func (m *cliModel) flushMessageQueue() {
	if len(m.messageQueue) == 0 {
		return
	}
	msg := m.messageQueue[0]
	m.messageQueue = m.messageQueue[1:]
	m.queueEditing = false
	m.queueEditBuf = ""
	// Put message into textarea and trigger send
	m.textarea.SetValue(msg)
	m.sendMessageFromQueue()
}

// sendMessageFromQueue sends the current textarea content as a queued message.
// Does NOT return tickCmd() — startAgentTurn() inside sendMessage() handles that.
func (m *cliModel) sendMessageFromQueue() {
	content := strings.TrimSpace(m.textarea.Value())
	if content == "" {
		return
	}
	m.textarea.Reset()
	m.autoExpandInput()
	m.sendMessage(content)
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

// ensurePanelCursorVisible 确保 panel cursor 行在可见区域内。
// 编辑/combo 模式下额外滚到底部，确保 inline editor 可见。
func (m *cliModel) ensurePanelCursorVisible() {
	if len(m.panelSchema) == 0 || m.panelCursor >= len(m.panelSchema) {
		return
	}
	// 编辑或下拉模式：直接滚到内容底部，因为 overlay 在末尾
	if m.panelEdit || m.panelCombo {
		m.panelScrollY = 0 // 先重置
		raw := m.viewPanel()
		total := strings.Count(raw, "\n") + 1
		visible := m.panelVisibleHeight()
		if total > visible {
			m.panelScrollY = total - visible
		}
		return
	}
	// 复刻 viewSettingsPanel 的行号计算逻辑。
	// header = 2 lines (title + divider), each category = 2 lines (\n + title),
	// each item = 1 line (label + value rendered inline).
	cursorLn := 2 // header offset
	lastCat := ""
	for i, def := range m.panelSchema {
		if def.Category != lastCat {
			lastCat = def.Category
			cursorLn += 2 // 空行 + 分类标题
		}
		cursorLn++ // 所有 item 类型都是单行渲染
		if i == m.panelCursor {
			break
		}
	}
	visibleH := m.panelVisibleHeight()
	totalLines := cursorLn + 5 // +5 保证底部有足够空间
	if totalLines <= visibleH {
		m.panelScrollY = 0
		return
	}
	if cursorLn >= m.panelScrollY+visibleH {
		m.panelScrollY = cursorLn - visibleH + 1
	}
	if cursorLn < m.panelScrollY {
		m.panelScrollY = cursorLn
	}
}

// ensureBgCursorVisible adjusts panelScrollY so the bg task/agent cursor is within the visible area.
// Accounts for preview lines (an agent with a preview takes 2 rendered lines).
func (m *cliModel) ensureBgCursorVisible() {
	visibleH := m.panelVisibleHeight()
	// Calculate the cursor item's approximate line number.
	// Tasks take 1 line each; agents take 1 line + 1 extra if they have a preview.
	cursorLine := 0
	// Header line
	cursorLine = 1
	idx := 0
	for _, task := range m.panelBgTasks {
		_ = task // tasks are always 1 line
		if idx == m.panelBgCursor {
			break
		}
		cursorLine++
		idx++
	}
	for _, ag := range m.panelBgAgents {
		if idx == m.panelBgCursor {
			break
		}
		cursorLine++ // agent label line
		if ag.Preview != "" {
			cursorLine++ // preview line
		}
		idx++
	}

	totalLines := cursorLine + 2 // +2 for header and bottom padding
	if totalLines <= visibleH {
		m.panelScrollY = 0
		return
	}
	if cursorLine >= m.panelScrollY+visibleH {
		m.panelScrollY = cursorLine - visibleH + 1
	}
	if cursorLine < m.panelScrollY {
		m.panelScrollY = cursorLine
	}
}

// panelVisibleHeight 返回 panel 可见区域高度。
func (m *cliModel) panelVisibleHeight() int {
	h := m.height - 5 // titleBar(1) + footer(1) + toast(1) + PanelBox borders(2)
	if h < 3 {
		h = 3
	}
	return h
}

// clampPanelScroll 确保 panelScrollY 不超出范围。
// rawContent 是已渲染的 panel 内容，避免重复调用 viewPanel()。
func (m *cliModel) clampPanelScroll(rawContent string) {
	total := strings.Count(rawContent, "\n") + 1
	visible := m.panelVisibleHeight()
	if total <= visible {
		m.panelScrollY = 0
		return
	}
	if m.panelScrollY < 0 {
		m.panelScrollY = 0
	}
	if m.panelScrollY > total-visible {
		m.panelScrollY = total - visible
	}
}

// clampAskUserPanelScroll adjusts askPanelScrollY for the askuser split layout.
// The visible height depends on viewport height + fixed chrome, not panelVisibleHeight().
func (m *cliModel) clampAskUserPanelScroll(rawContent string) {
	total := strings.Count(rawContent, "\n") + 1
	fixedLines := 2 // titleBar + toast (no separate footer — hints are in-panel)
	panelBorder := 2
	viewportH := m.layoutViewportHeight()
	visible := m.height - fixedLines - viewportH - panelBorder
	if visible < 3 {
		visible = 3
	}
	if total <= visible {
		m.askPanelScrollY = 0
		return
	}
	// When content overflows, default to showing the bottom (hints) rather than the top.
	// Only respect user's explicit scroll position if they've scrolled away from default.
	if m.askPanelScrollY == 0 {
		m.askPanelScrollY = total - visible
	}
	if m.askPanelScrollY < 0 {
		m.askPanelScrollY = 0
	}
	if m.askPanelScrollY > total-visible {
		m.askPanelScrollY = total - visible
	}
}

// askUserPanelVisibleHeight returns how many lines the askuser panel can display.
func (m *cliModel) askUserPanelVisibleHeight() int {
	fixedLines := 2 // titleBar + toast (no separate footer — hints are in-panel)
	panelBorder := 2
	viewportH := m.layoutViewportHeight()
	visible := m.height - fixedLines - viewportH - panelBorder
	if visible < 3 {
		return 3
	}
	return visible
}

// ensureAskPanelCursorVisible scrolls the askuser panel so the current cursor line is visible.
func (m *cliModel) ensureAskPanelCursorVisible() {
	// The cursor is at approximately: tab bar (1-2 lines) + question (1) + gap (1) + cursor position
	lineOfCursor := 2 // question + gap
	if m.panelTab > 0 {
		lineOfCursor = 2 // tab bar + gap
	}
	cursor := m.panelOptCursor[m.panelTab]
	lineOfCursor += cursor + 1 // +1 for the option line itself

	visible := m.askUserPanelVisibleHeight()
	if m.askPanelScrollY+visible <= lineOfCursor {
		m.askPanelScrollY = lineOfCursor - visible + 1
	}
	if m.askPanelScrollY > lineOfCursor {
		m.askPanelScrollY = lineOfCursor
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

// doSaveSettings runs the settings save callback synchronously and returns
// a tea.Cmd that sends the result back as cliSettingsSavedMsg.
// The callback is pure local I/O (config.json write, SQLite write, LLM rebuild)
// with no network calls, so it completes in milliseconds.
func (m *cliModel) doSaveSettings(onSubmit func(map[string]string), vals map[string]string) tea.Cmd {
	// Capture the values we need for the UI update
	theme, hasTheme := vals["theme"]
	lang, hasLang := vals["language"]
	// Capture feedback string now (m.locale is only safe to read in Update)
	feedbackMsg := m.locale.SettingsSaved

	// Run synchronously — all operations are local I/O, no network calls
	onSubmit(vals)

	return func() tea.Msg {
		return cliSettingsSavedMsg{
			themeChanged: hasTheme && theme != "",
			theme:        theme,
			langChanged:  hasLang,
			lang:         lang,
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
	m.saveCurrentFreeInput()
	answers := m.collectAskAnswers()
	if m.panelOnAnswer != nil {
		m.panelOnAnswer(answers)
	}
	m.closePanel()
	// NOTE: tickCmd() is NOT returned here. If agent is typing, the tick chain
	// is already running from startAgentTurn(). Returning tickCmd() while busy
	// creates a duplicate chain → 2x spinner speed.
	return true, m, nil
}

// closePanelAndResume closes the active panel and returns the appropriate
// tea.Cmd based on whether the agent is still typing.
// This pattern appears in bgtasks panel (Esc/Ctrl+C), settings panel (Esc),
// and askuser panel (Esc/cancel) handlers.
func (m *cliModel) closePanelAndResume() (bool, tea.Model, tea.Cmd) {
	m.closePanel()
	// NOTE: do NOT return tickCmd() here — same reason as submitAskAnswers.
	// The tick chain is already running if agent is typing.
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
// showTempStatus sets a temporary status message that auto-clears after 5s.
// Queues the clear command into pendingCmds (auto-drained by Update).
func (m *cliModel) showTempStatus(text string) {
	m.tempStatus = text
	m.pendingCmds = append(m.pendingCmds, m.clearTempStatusCmd(5*time.Second))
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

// handleUsageCommand renders token usage statistics for the current user.
func (m *cliModel) handleUsageCommand() {
	if m.usageQueryFn == nil {
		m.showSystemMsg("Usage tracking not available", feedbackWarning)
		return
	}

	cumulative, daily, err := m.usageQueryFn(m.senderID, 30)
	if err != nil {
		m.showSystemMsg(fmt.Sprintf("Failed to query usage: %v", err), feedbackError)
		return
	}

	var sb strings.Builder
	sb.WriteString("# Token Usage\n\n")

	// --- Cumulative totals ---
	if cumulative != nil && cumulative.TotalTokens > 0 {
		// Calculate usage duration from daily data
		usageDays := 0
		if len(daily) > 0 {
			// daily is sorted by date DESC; last entry = earliest date
			earliest := daily[len(daily)-1].Date
			if first, err := time.Parse("2006-01-02", earliest); err == nil {
				usageDays = int(time.Since(first).Hours()/24) + 1
			}
		}

		sb.WriteString("## Summary\n\n")
		sb.WriteString("| | |\n|---|---|\n")
		fmt.Fprintf(&sb, "| **Total tokens** | **%s** |\n", fmtTokens(cumulative.TotalTokens))
		fmt.Fprintf(&sb, "| Input | %s |\n", fmtTokens(cumulative.InputTokens))
		fmt.Fprintf(&sb, "| Output | %s |\n", fmtTokens(cumulative.OutputTokens))
		fmt.Fprintf(&sb, "| Cached | %s |\n", fmtTokens(cumulative.CachedTokens))
		fmt.Fprintf(&sb, "| Conversations | %d |\n", cumulative.ConversationCount)
		fmt.Fprintf(&sb, "| LLM calls | %d |\n", cumulative.LLMCallCount)
		if usageDays > 0 {
			fmt.Fprintf(&sb, "| **Usage duration** | **%d days** |\n", usageDays)
			avgDaily := cumulative.TotalTokens / int64(usageDays)
			fmt.Fprintf(&sb, "| Avg daily tokens | %s |\n", fmtTokens(avgDaily))
		}

		// Analysis section
		sb.WriteString("\n### Analysis\n\n")
		sb.WriteString("| | |\n|---|---|\n")
		if cumulative.InputTokens > 0 {
			cacheRate := float64(cumulative.CachedTokens) / float64(cumulative.InputTokens) * 100
			fmt.Fprintf(&sb, "| **Cache hit rate** | **%.1f%%** |\n", cacheRate)
			nonCachedInput := cumulative.InputTokens - cumulative.CachedTokens
			fmt.Fprintf(&sb, "| Actual input (non-cached) | %s |\n", fmtTokens(nonCachedInput))
		}
		if cumulative.LLMCallCount > 0 {
			avgIn := cumulative.InputTokens / cumulative.LLMCallCount
			avgOut := cumulative.OutputTokens / cumulative.LLMCallCount
			fmt.Fprintf(&sb, "| Avg input/call | %s |\n", fmtTokens(avgIn))
			fmt.Fprintf(&sb, "| Avg output/call | %s |\n", fmtTokens(avgOut))
		}
		if cumulative.ConversationCount > 0 {
			avgCalls := float64(cumulative.LLMCallCount) / float64(cumulative.ConversationCount)
			fmt.Fprintf(&sb, "| Avg calls/conversation | %.1f |\n", avgCalls)
		}
	} else {
		sb.WriteString("No usage data recorded yet.\n")
	}

	// --- Daily breakdown (last 30 days) ---
	if len(daily) > 0 {
		sb.WriteString("\n## Daily Breakdown (last 30 days)\n\n")
		sb.WriteString("| Date | Model | Input | Output | Cached | Cache%% | Calls |\n")
		sb.WriteString("|------|-------|-------|--------|--------|--------|-------|\n")
		for _, d := range daily {
			model := d.Model
			if model == "" {
				model = "(unknown)"
			}
			cacheRate := ""
			if d.InputTokens > 0 {
				cacheRate = fmt.Sprintf("%.0f%%", float64(d.CachedTokens)/float64(d.InputTokens)*100)
			}
			fmt.Fprintf(&sb, "| %s | %s | %s | %s | %s | %s | %d |\n",
				d.Date, model,
				fmtTokens(d.InputTokens),
				fmtTokens(d.OutputTokens),
				fmtTokens(d.CachedTokens),
				cacheRate,
				d.LLMCallCount,
			)
		}
	}

	m.appendSystemMarkdown(sb.String())
	m.updateViewportContent()
}

// formatTokenCount is defined in cli_view.go — do not duplicate here.

// fmtTokens formats large token counts with K/M suffixes for usage tables.
func fmtTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 10_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
