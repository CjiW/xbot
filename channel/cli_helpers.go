package channel

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var cliUserScopedSettingKeys = map[string]struct{}{
	"theme":                {},
	"language":             {},
	"context_mode":         {},
	"max_iterations":       {},
	"max_concurrency":      {},
	"max_context_tokens":   {},
	"enable_auto_compress": {},
	"runner_server":        {},
	"runner_token":         {},
	"runner_workspace":     {},
}

var cliGlobalScopedSettingKeys = map[string]struct{}{
	"vanguard_model":  {},
	"balance_model":   {},
	"swift_model":     {},
	"sandbox_mode":    {},
	"memory_provider": {},
	"tavily_api_key":  {},
	"enable_stream":   {},
	"enable_masking":  {},
	"default_user":    {},
	"privileged_user": {},
}

// CLIRuntimeSettingKeys lists all setting keys that require runtime application
// beyond DB persistence. Both serverapp and cmd/xbot-cli use this list to verify
// every runtime-affecting key has a handler registered.
//
// To add a new runtime setting:
//  1. Add the key here
//  2. Add a handler to settingHandlerRegistry (serverapp) AND cliRuntimeHandlers (cmd/xbot-cli)
//  3. That's it. The test TestAllRuntimeKeysHaveHandlers will catch omissions.
var CLIRuntimeSettingKeys = []string{
	"vanguard_model",
	"balance_model",
	"swift_model",
	"sandbox_mode",
	"memory_provider",
	"tavily_api_key",
	"context_mode",
	"max_iterations",
	"max_concurrency",
	"max_context_tokens",
	"enable_auto_compress",
}

// ParseSettingBool parses a boolean setting value.
// Accepts "true", "1", "yes" (case-insensitive) as true; everything else as false.
// Shared between serverapp and cmd/xbot-cli for consistent behavior.
func ParseSettingBool(value string) bool {
	return strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "yes")
}

// ParseSettingInt parses an integer setting value, returning fallback on failure.
// Shared between serverapp and cmd/xbot-cli for consistent behavior.
func ParseSettingInt(value string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return n
}

var cliActionSettingKeys = map[string]struct{}{
	"subscription_manage": {},
	"runner_panel":        {},
	"danger_zone":         {},
}

var cliSubscriptionScopedSettingKeys = map[string]struct{}{
	"llm_provider":      {},
	"llm_api_key":       {},
	"llm_base_url":      {},
	"llm_model":         {},
	"max_output_tokens": {},
	"thinking_mode":     {},
}

// isMaskedAPIKey detects API keys that were masked by the server for safe transport.
// Server masks keys as "<prefix>****" (e.g. "sk-a****"). Writing masked keys
// back to storage would destroy the real key — this function prevents that.
func isMaskedAPIKey(key string) bool {
	return strings.HasSuffix(key, "****") && len(key) <= 20
}

func isUserScopedSettingKey(key string) bool {
	_, ok := cliUserScopedSettingKeys[key]
	return ok
}

func isGlobalScopedSettingKey(key string) bool {
	_, ok := cliGlobalScopedSettingKeys[key]
	return ok
}

func isActionSettingKey(key string) bool {
	_, ok := cliActionSettingKeys[key]
	return ok
}

func isSubscriptionScopedSettingKey(key string) bool {
	_, ok := cliSubscriptionScopedSettingKeys[key]
	return ok
}

func cliSettingScope(key string) string {
	if isUserScopedSettingKey(key) {
		return "user"
	}
	if isGlobalScopedSettingKey(key) {
		return "global"
	}
	if isSubscriptionScopedSettingKey(key) {
		return "subscription"
	}
	if isActionSettingKey(key) {
		return "action"
	}
	return "unknown"
}

func (m *cliModel) mergeCLISettingsValues() map[string]string {
	values := make(map[string]string)
	if m.channel == nil {
		return values
	}
	// Non-LLM settings from GetCurrentValues (theme, language, tiers, etc.)
	if m.channel.config.GetCurrentValues != nil {
		for k, v := range m.channel.config.GetCurrentValues() {
			values[k] = v
		}
	}
	// Subscription-scoped settings from active subscription.
	if m.channel.subscriptionMgr != nil {
		if sub, err := m.channel.subscriptionMgr.GetDefault(m.senderID); err == nil && sub != nil {
			values["llm_provider"] = sub.Provider
			values["llm_api_key"] = sub.APIKey
			values["llm_base_url"] = sub.BaseURL
			values["llm_model"] = sub.Model
			values["max_output_tokens"] = strconv.Itoa(sub.MaxOutputTokens)
			values["thinking_mode"] = sub.ThinkingMode
		}
	}
	// User-scoped settings (theme, language, context_mode, etc.) override GetCurrentValues
	if m.channel.settingsSvc != nil {
		vals, err := m.channel.settingsSvc.GetSettings(m.channelName, m.senderID)
		if err == nil {
			for k, v := range vals {
				if isUserScopedSettingKey(k) {
					values[k] = v
				}
			}
		}
	}
	return values
}

func (m *cliModel) persistCLISettingsValues(values map[string]string) {
	if m.channel != nil && m.channel.settingsSvc != nil {
		for k, v := range values {
			if isUserScopedSettingKey(k) {
				_ = m.channel.settingsSvc.SetSetting(m.channelName, m.senderID, k, v)
			}
		}
	}
	if m.channel != nil && m.channel.config.ApplySettings != nil {
		m.channel.config.ApplySettings(values)
	}
}

// ---------------------------------------------------------------------------
// Refactored common patterns (方案 B: 提取重复代码)
// ---------------------------------------------------------------------------

// invalidateAllCache marks the render cache invalid, dirties all messages,
// and optionally updates the viewport content.
// This pattern appears in theme change, locale change, resize, and tool-summary toggle.
func (m *cliModel) invalidateAllCache(updateViewport bool) {
	m.renderCacheValid = false
	m.lastViewportContent = "" // Force viewport refresh on next updateViewportContent
	m.lastViewportWidth = 0
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
			if (s.Key == "vanguard_model" || s.Key == "balance_model" || s.Key == "swift_model") && len(allModels) > 0 {
				opts := make([]SettingOption, len(allModels))
				for j, ml := range allModels {
					opts[j] = SettingOption{Label: ml, Value: ml}
				}
				schema[i].Options = opts
			}
		}
	}
	// Re-read ALL values fresh (including LLM fields from new active subscription)
	values := m.mergeCLISettingsValues()
	// Overlay non-subscription values from backup (preserves user's in-memory edits).
	// Subscription quick switch should only refresh the active subscription-backed keys.
	for k, v := range m.panelValuesBackup {
		if isSubscriptionScopedSettingKey(k) {
			continue
		}
		values[k] = v
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
	m.turnCancelled = false // clear any previous cancel flag
	// Remote mode: optimistically show initial progress so the user sees
	// immediate feedback (progress bubble) without waiting for the server's
	// first progress_structured event (which has network round-trip latency).
	if m.remoteMode && m.progress == nil {
		m.progress = &CLIProgressPayload{
			Phase:     "thinking",
			Iteration: 0,
		}
		m.renderCacheValid = false
	}
	// NOTE: Callers are responsible for ensuring the tick chain starts:
	//   - Inside Bubble Tea Update: return tickCmd() in the cmd chain
	//   - Outside Update (callbacks): append to m.pendingCmds before calling
	// Sync checkpoint hook turn index
	if m.checkpointHook != nil {
		m.checkpointHook.SetTurnIdx(int(m.agentTurnID))
	}
	// Clear rewind result when new turn starts
	m.rewindResult = nil
	m.updatePlaceholder()
	m.inputReady = false
	m.resetProgressState()
}

// restoreProgressSnapshot applies a progress snapshot to the model for seamless
// reconnect/session-switch. Used when CLI reconnects to a running agent turn.
// Sets the model into typing state with the full iteration history restored.
// Safe to call before BubbleTea program starts (no channel sends).
func (m *cliModel) restoreProgressSnapshot(payload *CLIProgressPayload) {
	if payload == nil || payload.Phase == "done" {
		return
	}

	// Start agent turn (sets typing=true, increments turnID).
	// Note: startAgentTurn calls resetProgressState which clears m.progress,
	// but we overwrite it below.
	m.startAgentTurn()

	// Apply the progress payload
	m.progress = payload

	// Restore StartedAt for active tools so live elapsed timers work.
	for i := range m.progress.ActiveTools {
		t := &m.progress.ActiveTools[i]
		if t.StartedAt.IsZero() && t.Elapsed > 0 {
			t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
		}
	}

	// Restore iteration history from the progress snapshot.
	if len(payload.IterationHistory) > 0 {
		for _, ih := range payload.IterationHistory {
			snap := cliIterationSnapshot{
				Iteration: ih.Iteration,
				Thinking:  ih.Thinking,
				Reasoning: ih.Reasoning,
				Tools:     ih.CompletedTools,
			}
			for i := range snap.Tools {
				t := &snap.Tools[i]
				if t.StartedAt.IsZero() && t.Elapsed > 0 {
					t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
				}
			}
			m.iterationHistory = append(m.iterationHistory, snap)
		}
		if len(m.iterationHistory) > 0 {
			lastIter := m.iterationHistory[len(m.iterationHistory)-1].Iteration
			if lastIter > m.lastSeenIteration {
				m.lastSeenIteration = lastIter
			}
		}

		// Deduplicate: remove ALL tool_summary messages. When progress is
		// active, the progress block owns iteration display — any static
		// tool_summary would duplicate content with mismatched iteration numbers.
		m.removeAllToolSummaries()
	}

	m.invalidateAllCache(false)
	// Do NOT call updateViewportContent() here — terminal size may not be
	// initialized yet (pre-program path), causing panic in truncateToWidth.
	// View() will rebuild on the next render cycle.
	m.viewport.GotoBottom()
}

// dedupToolSummary removes the last tool_summary message from m.messages when
// restoring active progress. The last tool_summary in messages comes from
// intermediate assistant messages (postToolProcessing) of the in-progress turn.
// The progress snapshot's IterationHistory contains the same data plus live state,
// removeAllToolSummaries removes ALL tool_summary messages from m.messages.
// Used when restoring active progress on session switch: the progress block
// owns iteration display entirely, and any static tool_summary from
// ConvertMessagesToHistory would duplicate content with mismatched iteration numbers.
func (m *cliModel) removeAllToolSummaries() {
	filtered := m.messages[:0] // reuse backing array
	for _, msg := range m.messages {
		if msg.role != "tool_summary" {
			filtered = append(filtered, msg)
		}
	}
	m.messages = filtered
	m.renderCacheValid = false
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
	m.twVisible = 0
	m.rwVisible = 0
	m.typing = false
	m.typewriterTickActive = false
	m.turnCancelled = true // prevent stale progress from auto-starting after cancel
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

// ensureSessionCursorVisible adjusts panelScrollY so the session cursor is within the visible area.
// Each session entry takes exactly 1 rendered line.
func (m *cliModel) ensureSessionCursorVisible() {
	visibleH := m.panelVisibleHeight()
	// +1 for header line
	cursorLine := m.panelSessionCursor + 1
	totalLines := len(m.panelSessionItems) + 1
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
// Default scroll is 0 (show question at top), not bottom (hints).
// Caches total line count in askPanelTotalLines for use by ensureAskUserVisible.
func (m *cliModel) clampAskUserPanelScroll(rawContent string) {
	total := strings.Count(rawContent, "\n") + 1
	m.askPanelTotalLines = total
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

// handleUserCommand manages web users from TUI.
// Usage:
//
//	/user add <username>  — create a web user, returns auto-generated password
//	/user list            — list all web users
//	/user del <username>  — delete a web user
func (m *cliModel) handleUserCommand(arg string) {
	// All subcommands require admin.
	if m.isAdminFn != nil && !m.isAdminFn() {
		m.showSystemMsg("⛔ Admin only", feedbackError)
		return
	}
	if m.createWebUserFn == nil && m.listWebUsersFn == nil {
		m.showSystemMsg("Web user management not available (server-client mode or web not configured)", feedbackWarning)
		return
	}

	arg = strings.TrimSpace(arg)
	if arg == "" || arg == "list" || arg == "ls" {
		m.handleUserList()
		return
	}

	parts := strings.Fields(arg)
	if len(parts) < 2 {
		m.showSystemMsg("Usage: /user add <username> | /user list | /user del <username>", feedbackInfo)
		return
	}

	subcmd := parts[0]
	username := parts[1]

	switch subcmd {
	case "add", "create":
		if m.createWebUserFn == nil {
			m.showSystemMsg("Create web user not available", feedbackWarning)
			return
		}
		password, err := m.createWebUserFn(username)
		if err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Failed to create user: %v", err), feedbackError)
			return
		}
		m.showSystemMsg(fmt.Sprintf("✅ Web user created!\n\n| | |\n|---|---|\n| **Username** | `%s` |\n| **Password** | `%s` |\n\n⚠️ Save the password — it won't be shown again.", username, password), feedbackInfo)

	case "del", "delete", "rm", "remove":
		if m.deleteWebUserFn == nil {
			m.showSystemMsg("Delete web user not available", feedbackWarning)
			return
		}
		if err := m.deleteWebUserFn(username); err != nil {
			m.showSystemMsg(fmt.Sprintf("❌ Failed to delete user: %v", err), feedbackError)
			return
		}
		m.showSystemMsg(fmt.Sprintf("✅ Web user `%s` deleted", username), feedbackInfo)

	default:
		m.showSystemMsg("Usage: /user add <username> | /user list | /user del <username>", feedbackInfo)
	}
}

// handleUserList displays all web users in a table.
func (m *cliModel) handleUserList() {
	if m.listWebUsersFn == nil {
		m.showSystemMsg("List web users not available", feedbackWarning)
		return
	}
	users, err := m.listWebUsersFn()
	if err != nil {
		m.showSystemMsg(fmt.Sprintf("❌ Failed to list users: %v", err), feedbackError)
		return
	}
	if len(users) == 0 {
		m.showSystemMsg("No web users found. Use `/user add <username>` to create one.", feedbackInfo)
		return
	}

	var sb strings.Builder
	sb.WriteString("# Web Users\n\n")
	sb.WriteString("| # | Username | Created |\n|---|---|---|\n")
	for i, u := range users {
		username, _ := u["username"].(string)
		createdAt, _ := u["created_at"].(string)
		if createdAt != "" {
			// Trim to date+hour
			if t, err := time.Parse("2006-01-02T15:04:05Z07:00", createdAt); err == nil {
				createdAt = t.Format("2006-01-02 15:04")
			}
		}
		fmt.Fprintf(&sb, "| %d | `%s` | %s |\n", i+1, username, createdAt)
	}
	sb.WriteString("\n`/user add <name>` to create · `/user del <name>` to delete")
	m.showSystemMsg(sb.String(), feedbackInfo)
}
