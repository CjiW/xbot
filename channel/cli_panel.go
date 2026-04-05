package channel

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"context"
	"fmt"
	"strings"
	"time"
	"xbot/llm"
	"xbot/tools"
)

// --- §12 Interactive Panel ---

// openSettingsPanel activates the settings panel overlay.
func (m *cliModel) openSettingsPanel(schema []SettingDefinition, values map[string]string, onSubmit func(map[string]string)) {
	m.panelMode = "settings"
	m.relayoutViewport() // 缩小 viewport 为 panel 腾出空间
	m.panelCursor = 0
	m.panelEdit = false
	m.panelSchema = make([]SettingDefinition, len(schema))
	copy(m.panelSchema, schema)
	m.panelValues = make(map[string]string, len(values))
	for k, v := range values {
		m.panelValues[k] = v
	}
	// Fill defaults
	for _, def := range m.panelSchema {
		if _, ok := m.panelValues[def.Key]; !ok && def.DefaultValue != "" {
			m.panelValues[def.Key] = def.DefaultValue
		}
	}
	m.panelOnSubmit = onSubmit
	m.panelOnCancel = nil
	// Pre-create textarea for editing
	ta := textarea.New()
	ta.Placeholder = m.locale.PanelEditPlaceholder
	ta.SetWidth(m.panelWidth(60))
	ta.SetHeight(1)
	ta.CharLimit = 200
	m.panelEditTA = ta
}

// openSetupPanel opens the first-run setup wizard as a settings-style panel.
func (m *cliModel) openSetupPanel() {
	schema := m.locale.SetupSchema
	values := make(map[string]string)
	// Pre-fill defaults from schema
	for _, def := range schema {
		if def.DefaultValue != "" {
			values[def.Key] = def.DefaultValue
		}
	}
	// Pre-fill current values if available
	if m.channel != nil && m.channel.config.GetCurrentValues != nil {
		for k, v := range m.channel.config.GetCurrentValues() {
			if v != "" {
				values[k] = v
			}
		}
	}
	m.openSettingsPanel(schema, values, func(vals map[string]string) {
		// Apply all settings including setup-only keys (provider, api_key, sandbox, memory)
		if m.channel.config.ApplySettings != nil {
			m.channel.config.ApplySettings(vals)
		}
		// NOTE: UI updates (theme/locale/viewport) are handled by
		// handleSettingsSavedMsg in Update() since this runs in a goroutine.
	})
}

// askItem represents a single question in the AskUser panel.
type askItem struct {
	Question string   // the question text
	Options  []string // choices (empty = free input only)
	Answer   string   // user's answer (set on submit)
	Other    string   // user's custom input when "Other" option selected
}

// askQItem is the JSON structure for questions metadata from the AskUser tool.
type askQItem struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// dangerItem represents a single clearable memory target in the danger zone panel.
type dangerItem struct {
	Action string // "session", "core_persona", etc.
	Label  string // display label
	Stat   string // current stat (e.g. "128 msgs")
}

// dangerConfirmStrings maps action keys to required confirmation strings.
var dangerConfirmStrings = map[string]string{
	"session":       "DELETE-SESSION",
	"core_persona":  "DELETE-PERSONA",
	"core_human":    "DELETE-HUMAN",
	"core_working":  "DELETE-WORKING",
	"core_all":      "DELETE-CORE-MEMORY",
	"long_term":     "DELETE-LONG-TERM",
	"event_history": "DELETE-HISTORY",
	"archival":      "DELETE-ARCHIVAL",
	"reset_all":     "RESET-ALL-MEMORY",
}

// openAskUserPanel activates the ask-user panel overlay.
func (m *cliModel) openAskUserPanel(items []askItem, onAnswer func(map[string]string), onCancel func()) {
	m.panelMode = "askuser"
	m.relayoutViewport() // 缩小 viewport 为 panel 腾出空间
	m.panelItems = items
	m.panelTab = 0
	m.panelOptSel = make(map[int]map[int]bool)
	m.panelOptCursor = make(map[int]int)
	ta := textarea.New()
	ta.Placeholder = m.locale.PanelEditPlaceholder
	ta.Prompt = "  "
	applyTAStyles(&ta, &m.styles)
	ta.CharLimit = 0
	ta.SetWidth(m.panelWidth(50))
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.Focus()
	m.panelAnswerTA = ta
	// Initialize Other single-line input
	ti := textinput.New()
	ti.Placeholder = m.locale.PanelOtherPlaceholder
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.SetWidth(m.panelWidth(40))
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	ti.Focus()
	m.panelOtherTI = ti
	m.panelOnAnswer = onAnswer
	m.panelOnCancel = onCancel
}

// closePanel deactivates any active panel.
func (m *cliModel) closePanel() {
	m.panelMode = ""
	m.panelEdit = false
	m.panelCombo = false
	m.panelSchema = nil
	m.panelValues = nil
	m.panelOnSubmit = nil
	m.panelItems = nil
	m.panelTab = 0
	m.panelOptSel = nil
	m.panelOptCursor = nil
	// Bg tasks panel cleanup
	m.panelBgTasks = nil
	m.panelBgViewing = false
	m.panelBgScroll = 0
	m.panelBgLogLines = nil
	// Danger zone cleanup
	m.panelDangerItems = nil
	m.panelDangerCursor = 0
	m.panelDangerConfirm = false
	m.panelDangerOnExec = nil
	// Runner panel cleanup
	m.panelRunnerServerTI = textinput.Model{}
	m.panelRunnerTokenTI = textinput.Model{}
	m.panelRunnerWorkspace = textinput.Model{}
	m.panelRunnerEditField = 0
	// 恢复 viewport 到正常模式高度
	m.relayoutViewport()
}

// openBgTasksPanel opens the background task management panel.
func (m *cliModel) openBgTasksPanel() {
	if m.channel == nil || m.channel.bgTaskMgr == nil {
		return
	}
	m.panelMode = "bgtasks"
	m.relayoutViewport() // 缩小 viewport 为 panel 腾出空间
	m.panelBgTasks = m.channel.bgTaskMgr.ListRunning(m.channel.bgSessionKey)
	m.panelBgCursor = 0
	m.panelBgViewing = false
	m.panelBgScroll = 0
	m.panelBgLogLines = nil
	// Clamp cursor
	if len(m.panelBgTasks) == 0 {
		m.panelBgCursor = -1
	} else if m.panelBgCursor >= len(m.panelBgTasks) {
		m.panelBgCursor = len(m.panelBgTasks) - 1
	}
}

// updateBgTasksPanel handles key events in the bg tasks panel.
// Returns (handled, newModel, cmd).
func (m *cliModel) updateBgTasksPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	// Refresh task list
	if m.channel != nil && m.channel.bgTaskMgr != nil {
		m.panelBgTasks = m.channel.bgTaskMgr.ListRunning(m.channel.bgSessionKey)
	}

	// Log viewing sub-mode
	if m.panelBgViewing {
		switch {
		case msg.Code == tea.KeyEsc || msg.String() == "ctrl+c":
			m.panelBgViewing = false
			m.panelBgScroll = 0
			m.panelBgLogLines = nil
			return true, m, nil
		case msg.Code == tea.KeyUp:
			m.panelBgScroll -= 5
			if m.panelBgScroll < 0 {
				m.panelBgScroll = 0
			}
			return true, m, nil
		case msg.Code == tea.KeyDown:
			maxScroll := len(m.panelBgLogLines) - 20
			if maxScroll < 0 {
				maxScroll = 0
			}
			m.panelBgScroll += 5
			if m.panelBgScroll > maxScroll {
				m.panelBgScroll = maxScroll
			}
			return true, m, nil
		case msg.Code == tea.KeyPgUp:
			m.panelBgScroll -= 18
			if m.panelBgScroll < 0 {
				m.panelBgScroll = 0
			}
			return true, m, nil
		default:
			// PgDn: bubbletea doesn't have a constant, match by string
			if msg.String() == "pgdown" {
				maxScroll := len(m.panelBgLogLines) - 20
				if maxScroll < 0 {
					maxScroll = 0
				}
				m.panelBgScroll += 18
				if m.panelBgScroll > maxScroll {
					m.panelBgScroll = maxScroll
				}
				return true, m, nil
			}
		}
		return true, m, nil
	}

	// Task list mode
	switch {
	case msg.Code == tea.KeyEsc || msg.String() == "ctrl+c":
		return m.closePanelAndResume()

	case msg.Code == tea.KeyUp || msg.String() == "ctrl+k":
		if m.panelBgCursor > 0 {
			m.panelBgCursor--
		}
		return true, m, nil

	case msg.Code == tea.KeyDown || msg.String() == "ctrl+j":
		if m.panelBgCursor < len(m.panelBgTasks)-1 {
			m.panelBgCursor++
		}
		return true, m, nil

	case msg.Code == tea.KeyEnter:
		// View log of selected task
		if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
			task := m.panelBgTasks[m.panelBgCursor]
			// Output is written atomically by the task runner, safe to read
			m.panelBgLogLines = splitLines(task.Output)
			if len(m.panelBgLogLines) == 0 {
				m.panelBgLogLines = []string{"(no output)"}
			}
			m.panelBgViewing = true
			m.panelBgScroll = 0
		}
		return true, m, nil

	case msg.Code == tea.KeyDelete || msg.String() == "ctrl+d":
		// Kill selected running task
		if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
			task := m.panelBgTasks[m.panelBgCursor]
			if task.Status == tools.BgTaskRunning {
				if m.channel != nil && m.channel.bgTaskMgr != nil {
					if err := m.channel.bgTaskMgr.Kill(task.ID); err != nil {
						m.showTempStatus(fmt.Sprintf(m.locale.KillFailed, err))
						return true, m, m.clearTempStatusCmd()
					}
					// Refresh list after kill
					m.panelBgTasks = m.channel.bgTaskMgr.ListRunning(m.channel.bgSessionKey)
					if m.panelBgCursor >= len(m.panelBgTasks) {
						m.panelBgCursor = len(m.panelBgTasks) - 1
					}
					return true, m, nil
				}
			}
		}
		return true, m, nil
	}

	return true, m, nil
}

// viewBgTasksPanel renders the bg tasks panel.
func (m *cliModel) viewBgTasksPanel() string {
	if m.panelBgViewing {
		return m.viewBgTaskLog()
	}
	return m.viewBgTaskList()
}

// viewBgTaskList renders the task list view.
func (m *cliModel) viewBgTaskList() string {
	// §20 使用缓存样式
	s := &m.styles
	cursorStyle := s.PanelCursor
	header := s.PanelHeader.Render(m.locale.BgTasksTitle)
	help := s.PanelDesc.Render(m.locale.BgTasksHelp)

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	if len(m.panelBgTasks) == 0 {
		sb.WriteString(s.PanelEmpty.Render(m.locale.BgTasksEmpty))
	} else {
		for i, task := range m.panelBgTasks {
			elapsed := time.Since(task.StartedAt).Round(time.Second)
			if task.FinishedAt != nil {
				elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
			}
			statusIcon := "●"
			statusStyle := s.ProgressRunning
			if task.Status == tools.BgTaskDone {
				if task.Error != "" || task.ExitCode != 0 {
					statusIcon = "✗"
					statusStyle = s.ProgressError
				} else {
					statusIcon = "✓"
					statusStyle = s.ProgressDone
				}
			}

			prefix := "  "
			if i == m.panelBgCursor {
				prefix = cursorStyle.Render("▸")
			}

			cmd := task.Command
			if len(cmd) > 50 {
				cmd = cmd[:47] + "..."
			}

			line := fmt.Sprintf("%s %s  %-8s %s  %s",
				prefix,
				statusStyle.Render(statusIcon),
				task.ID,
				formatElapsed(int64(elapsed.Milliseconds())),
				cmd,
			)
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}

	return m.styles.PanelBox.Render(sb.String())
}

// viewBgTaskLog renders the log viewer for a selected task.
func (m *cliModel) viewBgTaskLog() string {
	// §20 使用缓存样式
	s := &m.styles

	var title string
	if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
		task := m.panelBgTasks[m.panelBgCursor]
		cmd := task.Command
		if len(cmd) > 40 {
			cmd = cmd[:37] + "..."
		}
		title = fmt.Sprintf(m.locale.BgTaskLogTitle, task.ID, cmd)
	}
	help := s.PanelDesc.Render(m.locale.BgTaskLogHelp)

	maxLines := 18
	start := m.panelBgScroll
	if start > len(m.panelBgLogLines) {
		start = len(m.panelBgLogLines)
	}
	end := start + maxLines
	if end > len(m.panelBgLogLines) {
		end = len(m.panelBgLogLines)
	}

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render(title))
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	for _, line := range m.panelBgLogLines[start:end] {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if end < len(m.panelBgLogLines) {
		sb.WriteString(s.PanelDesc.Render(fmt.Sprintf(m.locale.BgTaskLogMore, len(m.panelBgLogLines)-end)))
		sb.WriteString("\n")
	}

	return m.styles.PanelBox.Render(sb.String())
}

// viewDangerPanel renders the danger zone panel.
func (m *cliModel) viewDangerPanel() string {
	s := &m.styles
	var sb strings.Builder

	sb.WriteString(s.PanelHeader.Render("⚠ 危险区"))
	sb.WriteString("\n")

	if m.panelDangerConfirm && m.panelDangerCursor < len(m.panelDangerItems) {
		// Confirmation sub-mode
		item := m.panelDangerItems[m.panelDangerCursor]
		confirmStr := dangerConfirmStrings[item.Action]
		fmt.Fprintf(&sb, "  确认清空：%s\n", s.WarningSt.Render(item.Label))
		sb.WriteString(s.PanelDesc.Render("  此操作不可恢复"))
		sb.WriteString("\n\n")
		fmt.Fprintf(&sb, "  请输入 %s 确认：\n", s.ProgressError.Render(confirmStr))
		sb.WriteString("  ")
		sb.WriteString(m.panelDangerInput.View())
		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  Enter 提交  Esc 返回"))
	} else {
		// Item selection mode
		for i, item := range m.panelDangerItems {
			var prefix string
			statText := ""
			if item.Stat != "" {
				statText = fmt.Sprintf("  %s", s.InfoSt.Render(item.Stat))
			}
			if i == m.panelDangerCursor {
				prefix = s.PanelCursor.Render("▸")
			} else {
				prefix = "  "
			}
			line := fmt.Sprintf("%s %s%s", prefix, item.Label, statText)
			if i == m.panelDangerCursor {
				line = s.SettingsSelBg.Width(m.panelWidth(60) - 4).Render(line)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  ↑↓ 选择  Enter 确认  Esc 返回"))
	}

	return s.PanelBox.Render(sb.String())
}

// splitLines splits a string into lines, preserving trailing empty line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// updatePanel handles key events when a panel is active.
// Returns (handled, newModel, cmd).
func (m *cliModel) updatePanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelMode == "" {
		return false, m, nil
	}

	switch m.panelMode {
	case "settings":
		return m.updateSettingsPanel(msg)
	case "askuser":
		return m.updateAskUserPanel(msg)
	case "bgtasks":
		return m.updateBgTasksPanel(msg)
	case "danger":
		return m.updateDangerPanel(msg)
	case "runner":
		return m.updateRunnerPanel(msg)
	}
	return false, m, nil
}

// updateDangerPanel handles key events in the danger zone panel.
func (m *cliModel) updateDangerPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelDangerConfirm {
		// Confirmation input mode
		switch msg.Code {
		case tea.KeyEsc:
			m.panelDangerConfirm = false
			m.panelDangerInput.SetValue("")
			return true, m, nil
		case tea.KeyEnter:
			if m.panelDangerOnExec == nil || m.panelDangerCursor >= len(m.panelDangerItems) {
				return true, m, nil
			}
			item := m.panelDangerItems[m.panelDangerCursor]
			confirmStr := dangerConfirmStrings[item.Action]
			if m.panelDangerInput.Value() != confirmStr {
				m.showSystemMsg("❌ 确认文字不匹配", feedbackWarning)
				return true, m, nil
			}
			// Execute the clear action
			if err := m.panelDangerOnExec(item.Action); err != nil {
				m.showSystemMsg(fmt.Sprintf("❌ 清空失败：%v", err), feedbackWarning)
			} else {
				m.showSystemMsg(fmt.Sprintf("✅ 已清空：%s", item.Label), feedbackInfo)
			}
			m.closePanel()
			return true, m, nil
		default:
			var cmd tea.Cmd
			m.panelDangerInput, cmd = m.panelDangerInput.Update(msg)
			return true, m, cmd
		}
	}

	// Item selection mode
	switch {
	case msg.Code == tea.KeyEsc || msg.String() == "ctrl+c":
		return m.closePanelAndResume()

	case msg.Code == tea.KeyUp:
		if m.panelDangerCursor > 0 {
			m.panelDangerCursor--
		}

	case msg.Code == tea.KeyDown:
		if m.panelDangerCursor < len(m.panelDangerItems)-1 {
			m.panelDangerCursor++
		}

	case msg.Code == tea.KeyEnter:
		if m.panelDangerCursor < len(m.panelDangerItems) {
			m.panelDangerConfirm = true
			m.panelDangerInput.SetValue("")
			m.panelDangerInput.Focus()
			return true, m, m.panelDangerInput.Focus()
		}
	}

	return true, m, nil
}

// openDangerPanelFromSettings builds danger items with stats and opens the danger zone panel.
func (m *cliModel) openDangerPanelFromSettings() {
	stats := map[string]string{}
	if m.channel != nil && m.channel.config.GetMemoryStats != nil {
		stats = m.channel.config.GetMemoryStats()
	}

	items := []dangerItem{
		{"session", "会话历史", stats["session"]},
		{"core_persona", "Core Memory: persona", stats["persona"]},
		{"core_human", "Core Memory: human", stats["human"]},
		{"core_working", "Core Memory: working_context", stats["working_context"]},
		{"core_all", "Core Memory: 全部", ""},
		{"long_term", "长期记忆", stats["long_term"]},
		{"event_history", "事件历史", stats["event_history"]},
		{"archival", "归档记忆（向量数据库）", stats["archival"]},
	}

	m.panelMode = "danger"
	m.relayoutViewport()
	m.panelDangerItems = items
	m.panelDangerCursor = 0
	m.panelDangerConfirm = false
	m.panelDangerOnExec = func(targetType string) error {
		if m.channel != nil && m.channel.config.ClearMemory != nil {
			return m.channel.config.ClearMemory(targetType)
		}
		return fmt.Errorf("clear memory not configured")
	}
	// Pre-create text input for confirmation
	ti := textinput.New()
	ti.Placeholder = "输入确认文字..."
	ti.CharLimit = 50
	ti.SetWidth(m.panelWidth(40))
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	m.panelDangerInput = ti
}

func (m *cliModel) updateSettingsPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelEdit {
		// Editing mode
		switch msg.Code {
		case tea.KeyEnter:
			// Save value
			newVal := strings.TrimSpace(m.panelEditTA.Value())
			if m.panelCursor < len(m.panelSchema) {
				key := m.panelSchema[m.panelCursor].Key
				m.panelValues[key] = newVal
			}
			m.panelEdit = false
			return true, m, nil
		case tea.KeyEsc:
			m.panelEdit = false
			return true, m, nil
		default:
			// Delegate to textarea
			var cmd tea.Cmd
			m.panelEditTA, cmd = m.panelEditTA.Update(msg)
			return true, m, cmd
		}
	}

	// Combo dropdown mode
	if m.panelCombo {
		if m.panelCursor < len(m.panelSchema) {
			def := m.panelSchema[m.panelCursor]
			opts := def.Options
			switch msg.Code {
			case tea.KeyEsc:
				m.panelCombo = false
				return true, m, nil
			case tea.KeyUp:
				if m.panelComboIdx > 0 {
					m.panelComboIdx--
				}
				return true, m, nil
			case tea.KeyDown:
				if m.panelComboIdx < len(opts)-1 {
					m.panelComboIdx++
				}
				return true, m, nil
			case tea.KeyEnter:
				if m.panelComboIdx < len(opts) {
					m.panelValues[def.Key] = opts[m.panelComboIdx].Value
				}
				m.panelCombo = false
				return true, m, nil
			case tea.KeySpace:
				m.panelCombo = false
				// Start typing to filter / enter custom value → switch to edit mode
				m.panelEdit = true
				ta := m.newPanelTextArea(m.panelValues[def.Key], 50, 1)
				var cmd tea.Cmd
				m.panelEditTA, cmd = ta.Update(msg)
				return true, m, cmd
			}
		}
		return true, m, nil
	}

	// Navigation mode
	switch {
	case msg.Code == tea.KeyEsc:
		m.closePanel()
		return true, m, nil
	case msg.String() == "ctrl+s":
		// Submit all settings — async to avoid blocking the UI.
		// Close panel immediately to restore responsiveness, then run
		// the save callback in a goroutine and send back results.
		onSubmit := m.panelOnSubmit
		panelVals := m.panelValues
		m.closePanel()
		if onSubmit != nil && panelVals != nil {
			return true, m, m.doSaveSettingsAsync(onSubmit, panelVals)
		}
		return true, m, nil
	case msg.Code == tea.KeyUp || msg.String() == "shift+tab":
		if m.panelCursor > 0 {
			m.panelCursor--
		}
		return true, m, nil
	case msg.Code == tea.KeyDown || msg.Code == tea.KeyTab:
		if m.panelCursor < len(m.panelSchema)-1 {
			m.panelCursor++
		}
		return true, m, nil
	case msg.Code == tea.KeyEnter:
		if m.panelCursor < len(m.panelSchema) {
			def := m.panelSchema[m.panelCursor]
			// Runner panel entry
			if def.Key == "runner_panel" {
				m.openRunnerPanel()
				return true, m, nil
			}
			// Danger zone entry
			if def.Key == "danger_zone" {
				m.openDangerPanelFromSettings()
				return true, m, nil
			}
			switch def.Type {
			case SettingTypeToggle:
				// Toggle on Enter
				cur := m.panelValues[def.Key]
				if cur == "true" {
					m.panelValues[def.Key] = "false"
				} else {
					m.panelValues[def.Key] = "true"
				}
				return true, m, nil
			case SettingTypeSelect:
				// Cycle through options
				cur := m.panelValues[def.Key]
				found := false
				for i, opt := range def.Options {
					if opt.Value == cur && i < len(def.Options)-1 {
						m.panelValues[def.Key] = def.Options[i+1].Value
						found = true
						break
					}
				}
				if !found && len(def.Options) > 0 {
					m.panelValues[def.Key] = def.Options[0].Value
				}
				return true, m, nil
			case SettingTypeCombo:
				// Open combo dropdown if options available, otherwise edit
				if len(def.Options) > 0 {
					m.panelCombo = true
					m.panelComboIdx = 0
					// Pre-select current value if it matches an option
					cur := m.panelValues[def.Key]
					for i, opt := range def.Options {
						if opt.Value == cur {
							m.panelComboIdx = i
							break
						}
					}
					return true, m, nil
				}
				// No options: fall through to default edit mode
				fallthrough
			default:
				// Enter edit mode for text/number/textarea/combo(fallback)
				m.panelEdit = true
				m.panelEditTA = m.newPanelTextArea(m.panelValues[def.Key], 50, 1)
				return true, m, nil
			}
		}
		return true, m, nil
	}
	return true, m, nil
}

func (m *cliModel) updateAskUserPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return true, m, nil
	}
	item := &m.panelItems[m.panelTab]
	numOpts := len(item.Options)
	hasOpts := numOpts > 0
	// Cursor: 0..numOpts-1 (checkbox), numOpts (Other input), numOpts+1 (Submit)
	cursor := m.panelOptCursor[m.panelTab]
	onOther := hasOpts && cursor == numOpts
	onSubmit := hasOpts && cursor == numOpts+1

	switch {
	case msg.String() == "ctrl+s":
		return m.submitAskAnswers()
	case msg.Code == tea.KeyEsc:
		if m.panelOnCancel != nil {
			m.panelOnCancel()
		}
		m.closePanel()
		return true, m, nil
	case msg.Code == tea.KeyRight || msg.Code == tea.KeyTab:
		if len(m.panelItems) > 1 && m.panelTab < len(m.panelItems)-1 {
			m.saveCurrentFreeInput()
			m.panelTab++
			m.restoreFreeInput()
		}
		return true, m, nil
	case msg.String() == "shift+tab" || msg.Code == tea.KeyLeft:
		if len(m.panelItems) > 1 && m.panelTab > 0 {
			m.saveCurrentFreeInput()
			m.panelTab--
			m.restoreFreeInput()
		}
		return true, m, nil
	case msg.Code == tea.KeyUp:
		if hasOpts {
			if onOther {
				m.panelOptCursor[m.panelTab] = numOpts - 1
				return true, m, nil
			}
			if cursor > 0 {
				m.panelOptCursor[m.panelTab] = cursor - 1
			}
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case msg.Code == tea.KeyDown:
		if hasOpts {
			if onOther {
				m.panelOptCursor[m.panelTab] = numOpts + 1
				return true, m, nil
			}
			if cursor < numOpts+1 {
				m.panelOptCursor[m.panelTab] = cursor + 1
			}
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case msg.Code == tea.KeyEnter:
		if hasOpts {
			if onSubmit {
				return m.submitAskAnswers()
			}
			// On checkbox: toggle; on Other: do nothing (let user type)
			if !onOther {
				m.toggleOptAtCursor()
			}
			return true, m, nil
		}
		return m.submitAskAnswers()
	case msg.Code == tea.KeySpace:
		if hasOpts && !onOther {
			if cursor < numOpts {
				m.toggleOptAtCursor()
			}
			if cursor < numOpts+1 {
				m.panelOptCursor[m.panelTab] = cursor + 1
			}
			return true, m, nil
		}
		if onOther {
			// Other 输入框：空格传给 textinput
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		// No options: fall through to textarea
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case len(msg.Text) > 0:
		if hasOpts && !onOther {
			m.panelOptCursor[m.panelTab] = numOpts
			m.restoreOtherInput()
		}
		if onOther {
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		if hasOpts {
			// With options, all input goes through Other textinput
			return true, m, nil
		}
		// No options: textarea
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	default:
		if isCtrlJ(msg) {
			if !hasOpts {
				m.panelAnswerTA.InsertString("\n")
				m.autoExpandAskTA()
			}
			return true, m, nil
		}
		if onOther {
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		if hasOpts {
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	}

}

// toggleOptAtCursor toggles the checkbox at the current cursor position.
func (m *cliModel) toggleOptAtCursor() {
	tab := m.panelTab
	if m.panelOptSel[tab] == nil {
		m.panelOptSel[tab] = make(map[int]bool)
	}
	cursor := m.panelOptCursor[tab]
	m.panelOptSel[tab][cursor] = !m.panelOptSel[tab][cursor]
}

// collectAskAnswers gathers answers from all questions.
func (m *cliModel) collectAskAnswers() map[string]string {
	answers := make(map[string]string)
	for i, item := range m.panelItems {
		key := fmt.Sprintf("q%d", i)
		hasOpts := len(item.Options) > 0
		var parts []string
		if hasOpts {
			if sel, ok := m.panelOptSel[i]; ok && len(sel) > 0 {
				for idx := range sel {
					if idx < len(item.Options) {
						parts = append(parts, item.Options[idx])
					}
				}
			}
			var otherText string
			if i == m.panelTab {
				otherText = strings.TrimSpace(m.panelOtherTI.Value())
			} else {
				otherText = strings.TrimSpace(item.Other)
			}
			if otherText != "" {
				parts = append(parts, otherText)
			}
			answers[key] = strings.Join(parts, ", ")
		} else {
			if i == m.panelTab {
				answers[key] = strings.TrimSpace(m.panelAnswerTA.Value())
			} else {
				answers[key] = strings.TrimSpace(item.Other)
			}
		}
	}
	return answers
}

// saveCurrentFreeInput saves textarea/textinput content for the current tab.
func (m *cliModel) saveCurrentFreeInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := &m.panelItems[m.panelTab]
	if len(item.Options) > 0 {
		item.Other = m.panelOtherTI.Value()
	} else {
		item.Other = m.panelAnswerTA.Value()
	}
}

// restoreFreeInput restores textarea/textinput content for the current tab.
func (m *cliModel) restoreFreeInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := m.panelItems[m.panelTab]
	if len(item.Options) > 0 {
		m.panelOtherTI.SetValue(item.Other)
		m.panelOtherTI.CursorEnd()
		m.panelOtherTI.Focus()
	} else {
		m.panelAnswerTA.SetValue(item.Other)
		m.panelAnswerTA.CursorEnd()
		m.panelAnswerTA.Focus()
		m.autoExpandAskTA()
	}
}

// restoreOtherInput restores the Other textinput for the current tab (options mode).
func (m *cliModel) restoreOtherInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	m.panelOtherTI.SetValue(m.panelItems[m.panelTab].Other)
	m.panelOtherTI.CursorEnd()
}

// autoExpandAskTA adjusts textarea height based on content.
func (m *cliModel) autoExpandAskTA() {
	lines := strings.Count(m.panelAnswerTA.Value(), "\n") + 1
	if lines < 2 {
		lines = 2
	}
	if lines > 6 {
		lines = 6
	}
	if m.panelAnswerTA.Height() != lines {
		m.panelAnswerTA.SetHeight(lines)
	}
}

// panelMaxHeight 根据 viewport 高度计算 panel 可用最大行数。
// Panel 模式下 viewport 已缩到最小，panel 获得几乎所有剩余空间。
// 返回的是 panel 内容区的可用行数（不含 PanelBox 的 border 行）。
func (m *cliModel) panelMaxHeight() int {
	// fixedLines: titleBar(1) + status(1) + footer(1) = 3
	// viewportMin: 3 (panel 模式下 viewport 缩到最小)
	// panelOverhead: panelBorder(2) + panelFooter(1) + toast(~1) = 4
	panelAvailable := m.height - 3 - 3 - 4
	// 减去 PanelBox 自身的 border 行数（RoundedBorder 上下各1行 = 2行）
	panelAvailable -= 2
	if panelAvailable < 6 {
		panelAvailable = 6 // 最小高度保证：至少显示标题+几行内容
	}
	return panelAvailable
}

// clampPanelContent 根据 maxHeight 裁剪 panel 内容行。
// 保留头部和底部提示，中间部分可裁剪并显示 "... N more ..."。
func clampPanelContent(content string, maxHeight int) string {
	if maxHeight <= 0 {
		return content
	}
	lines := strings.Split(content, "\n")
	// 去掉末尾可能的空行
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) <= maxHeight {
		return strings.Join(lines, "\n")
	}
	// 保留前 4 行（标题+表头）和最后 2 行（操作提示）
	headerLines := 4
	footerLines := 2
	if headerLines+footerLines >= maxHeight {
		// 极端情况：空间不足以同时保留头部和尾部
		// 至少保留 headerLines 行，剩余空间给尾部
		if maxHeight <= 1 {
			// 仅保留第一行（标题）
			return lines[0]
		}
		headerLines = (maxHeight + 1) / 2
		footerLines = maxHeight - headerLines
	}
	kept := headerLines + footerLines
	omitted := len(lines) - kept
	result := make([]string, 0, maxHeight+1)
	result = append(result, lines[:headerLines]...)
	moreHint := fmt.Sprintf("  ... %d lines omitted (narrow terminal) ...", omitted)
	result = append(result, moreHint)
	result = append(result, lines[len(lines)-footerLines:]...)
	return strings.Join(result, "\n")
}

// viewPanel renders the active panel as a string.
func (m *cliModel) viewPanel() string {
	var raw string
	switch m.panelMode {
	case "settings":
		raw = m.viewSettingsPanel()
	case "askuser":
		raw = m.viewAskUserPanel()
	case "bgtasks":
		raw = m.viewBgTasksPanel()
	case "danger":
		raw = m.viewDangerPanel()
	case "runner":
		raw = m.viewRunnerPanel()
	default:
		return ""
	}
	// §8 Panel 小终端适配：根据可用高度裁剪内容
	maxH := m.panelMaxHeight()
	return clampPanelContent(raw, maxH)
}

func (m *cliModel) viewSettingsPanel() string {

	// §20 使用缓存样式
	s := &m.styles
	valueStyle := s.InfoSt
	cursorStyle := s.PanelCursor
	descStyle := s.PanelDesc
	hintStyle := s.PanelHint

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render("⚙ " + m.locale.PanelSettingsTitle))
	sb.WriteString("\n")
	// 表头下方精致分割线，区分标题与内容
	sb.WriteString(s.SettingsDivider.Render("┈" + strings.Repeat("┈", 30)))

	// Group by category
	lastCat := ""
	for i, def := range m.panelSchema {
		if def.Category != lastCat {
			lastCat = def.Category
			sb.WriteString("\n")
			sb.WriteString(s.SettingsCat.Render("▸ " + lastCat))
			sb.WriteString("\n")
		}

		cur := m.panelValues[def.Key]
		var prefix string
		if i == m.panelCursor && !m.panelEdit {
			prefix = cursorStyle.Render("▸")
		} else {
			prefix = "  "
		}

		// Runner panel entry: render with accent style
		if def.Key == "runner_panel" {
			line := fmt.Sprintf("%s %s", prefix, s.ProgressDone.Render(def.Label))
			if i == m.panelCursor && !m.panelEdit {
				line = s.SettingsSelBg.Width(m.width - 6).Render(line)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			continue
		}

		// Danger zone entry: render with warning style
		if def.Key == "danger_zone" {
			line := fmt.Sprintf("%s %s", prefix, s.WarningSt.Render(def.Label))
			if i == m.panelCursor && !m.panelEdit {
				line = s.SettingsSelBg.Width(m.width - 6).Render(line)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
			continue
		}

		// Format value display
		var displayVal string
		switch def.Type {
		case SettingTypeToggle:
			if cur == "true" {
				displayVal = valueStyle.Render(m.locale.PanelToggleOn)
			} else {
				displayVal = valueStyle.Render(m.locale.PanelToggleOff)
			}
		case SettingTypeSelect:
			// Find label for current value
			displayVal = cur
			for _, opt := range def.Options {
				if opt.Value == cur {
					displayVal = valueStyle.Render(opt.Label)
					break
				}
			}
		case SettingTypeCombo:
			// Show current value with dropdown hint
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueStyle.Render(cur)
			}
			if len(def.Options) > 0 {
				displayVal += descStyle.Render(" ▾")
			}
		case SettingTypePassword:
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueStyle.Render("••••••")
			}
		default:
			if cur == "" {
				displayVal = descStyle.Render(m.locale.PanelNotSet)
			} else {
				displayVal = valueStyle.Render(cur)
			}
		}

		line := fmt.Sprintf("%s %s: %s", prefix, def.Label, displayVal)
		if i == m.panelCursor && !m.panelEdit {
			line = s.SettingsSelBg.Width(m.width - 6).Render(line)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Editing overlay
	if m.panelEdit && m.panelCursor < len(m.panelSchema) {
		def := m.panelSchema[m.panelCursor]
		sb.WriteString("\n")
		editLabel := cursorStyle.Render("  ✎ " + def.Label + ": ")
		sb.WriteString(editLabel)
		sb.WriteString(m.panelEditTA.View())
		sb.WriteString("\n")
		sb.WriteString(descStyle.Render("  " + m.locale.PanelEditHint))
	} else if m.panelCombo && m.panelCursor < len(m.panelSchema) {
		def := m.panelSchema[m.panelCursor]
		sb.WriteString("\n")
		comboTitle := cursorStyle.Render("  ▾ " + def.Label + ":")
		sb.WriteString(comboTitle)
		sb.WriteString("\n")
		maxShow := 8
		start := 0
		if m.panelComboIdx >= maxShow {
			start = m.panelComboIdx - maxShow + 1
		}
		end := start + maxShow
		if end > len(def.Options) {
			end = len(def.Options)
		}
		for j := start; j < end; j++ {
			opt := def.Options[j]
			label := opt.Label
			// Truncate long model names to prevent box overflow
			runes := []rune(label)
			if len(runes) > 40 {
				label = string(runes[:37]) + "..."
			}
			if j == m.panelComboIdx {
				sb.WriteString(cursorStyle.Render("  ▸ " + label))
			} else {
				sb.WriteString("    " + label)
			}
			sb.WriteString("\n")
		}
		sb.WriteString(descStyle.Render("  " + m.locale.PanelComboHint))
	} else {
		sb.WriteString("\n")
		sb.WriteString(hintStyle.Render("  " + m.locale.PanelNavHint))
	}

	return m.styles.PanelBox.Render(sb.String())
}

func (m *cliModel) viewAskUserPanel() string {

	// §20 使用缓存样式
	s := &m.styles
	questionStyle := s.WarningSt.Bold(true)
	hintStyle := s.PanelHint
	activeTabStyle := s.PanelHeader
	inactiveTabStyle := s.PanelDesc
	checkStyle := s.ToolItem
	cursorStyle := s.PanelCursor
	submitStyle := s.TodoDone

	var sb strings.Builder

	// Tab bar (if multiple questions)
	if len(m.panelItems) > 1 {
		for i := range m.panelItems {
			label := fmt.Sprintf(" %d ", i+1)
			if i == m.panelTab {
				sb.WriteString(activeTabStyle.Render(label))
			} else {
				sb.WriteString(inactiveTabStyle.Render(label))
			}
			if i < len(m.panelItems)-1 {
				sb.WriteString(inactiveTabStyle.Render("│"))
			}
		}
		sb.WriteString("\n\n")
	}

	// Current question
	if m.panelTab >= 0 && m.panelTab < len(m.panelItems) {
		item := m.panelItems[m.panelTab]
		sb.WriteString(questionStyle.Render("❓ " + item.Question))
		sb.WriteString("\n")

		hasOpts := len(item.Options) > 0

		if hasOpts {
			sb.WriteString("\n")
			sel := m.panelOptSel[m.panelTab]
			cursor := m.panelOptCursor[m.panelTab]
			numOpts := len(item.Options)

			for i, opt := range item.Options {
				checked := sel != nil && sel[i]
				var box string
				if checked {
					box = "☑"
				} else {
					box = "☐"
				}
				var line string
				if i == cursor {
					prefix := cursorStyle.Render("▸ ")
					if checked {
						line = checkStyle.Render(prefix + box + " " + opt)
					} else {
						line = prefix + box + " " + opt
					}
				} else {
					if checked {
						line = checkStyle.Render("  " + box + " " + opt)
					} else {
						line = "  " + box + " " + opt
					}
				}
				sb.WriteString(line)
				sb.WriteString("\n")
			}

			// Other input (single-line)
			otherLabel := m.locale.PanelOther
			if cursor == numOpts {
				sb.WriteString(cursorStyle.Render("▸ ") + otherLabel)
			} else {
				sb.WriteString("  " + otherLabel)
			}
			sb.WriteString(m.panelOtherTI.View())
			sb.WriteString("\n")

			// Submit button
			submitLabel := m.locale.PanelSubmit
			if cursor == numOpts+1 {
				sb.WriteString(cursorStyle.Render("▸ ") + submitStyle.Render(submitLabel))
			} else {
				sb.WriteString("  " + submitStyle.Render(submitLabel))
			}
			sb.WriteString("\n")
		} else {
			sb.WriteString("\n")
			sb.WriteString(m.panelAnswerTA.View())
			sb.WriteString("\n")
		}
	}

	// Hints
	sb.WriteString("\n")
	hints := []string{}
	if len(m.panelItems) > 1 {
		hints = append(hints, m.locale.PanelAskNav)
	}
	if len(m.panelItems) > 0 && m.panelTab < len(m.panelItems) {
		item := m.panelItems[m.panelTab]
		if len(item.Options) > 0 {
			hints = append(hints, m.locale.PanelAskToggle, m.locale.PanelAskOther, m.locale.PanelAskSubmit)
		} else {
			hints = append(hints, m.locale.PanelAskNewline)
		}
	}
	hints = append(hints, m.locale.PanelAskCancel)
	sb.WriteString(hintStyle.Render("  " + strings.Join(hints, " · ")))

	return m.styles.PanelBox.Render(sb.String())
}

// --- SettingsCapability implementation for CLIChannel ---

// SettingsSchema returns the settings definitions for CLI channel.
func (c *CLIChannel) SettingsSchema() []SettingDefinition {
	loc := GetLocale(currentLocaleLang)
	return loc.SettingsSchema
}

// HandleSettingSubmit processes a setting value submission from the CLI channel.
func (c *CLIChannel) HandleSettingSubmit(ctx context.Context, rawInput string) (map[string]string, error) {
	// CLI uses interactive panel, this is for programmatic access
	return nil, fmt.Errorf("CLI uses interactive settings panel")
}

// SetSettingsService injects the settings service for the interactive panel.
func (c *CLIChannel) SetSettingsService(svc SettingsService) {
	c.settingsSvc = svc
}

// SetModelLister injects the model lister for combo settings.
func (c *CLIChannel) SetModelLister(lister ModelLister) {
	c.modelLister = lister
}

// UpdateConfig updates the live LLM configuration (model, base_url).
// These overrides are picked up by the Agent on next message.
func (c *CLIChannel) UpdateConfig(model, baseURL string) {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	if model != "" {
		c.modelOverride = model
	}
	if baseURL != "" {
		c.baseURLOverride = baseURL
	}
	// NOTE: do NOT call refreshCachedModelName() here — it acquires configMu.RLock()
	// which would deadlock with the write lock held above. Callers must call
	// refreshCachedModelName() after UpdateConfig returns if needed.
}

// GetModelOverride returns the user-overridden model name (empty if not set).
func (c *CLIChannel) GetModelOverride() string {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.modelOverride
}

// GetBaseURLOverride returns the user-overridden base URL (empty if not set).
func (c *CLIChannel) GetBaseURLOverride() string {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.baseURLOverride
}

// ---------------------------------------------------------------------------
// Runner Panel
// ---------------------------------------------------------------------------

// openRunnerPanel 打开 Runner 管理面板
func (m *cliModel) openRunnerPanel() {
	m.panelMode = "runner"
	m.relayoutViewport()

	// 初始化 textinput 字段
	serverURL := ""
	token := ""
	workspace := m.workDir

	// 从设置中读取已保存的值
	if m.channel != nil && m.channel.settingsSvc != nil {
		if vals, err := m.channel.settingsSvc.GetSettings(cliChannelName, cliSenderID); err == nil {
			if v, ok := vals["runner_server"]; ok && v != "" {
				serverURL = v
			}
			if v, ok := vals["runner_token"]; ok && v != "" {
				token = v
			}
			if v, ok := vals["runner_workspace"]; ok && v != "" {
				workspace = v
			}
		}
	}

	m.panelRunnerServerTI = m.newPanelTextInput(serverURL, m.locale.RunnerServerPlaceholder)
	m.panelRunnerTokenTI = m.newPanelTextInput(token, m.locale.RunnerTokenPlaceholder)
	m.panelRunnerTokenTI.EchoMode = 0 // password mode
	m.panelRunnerTokenTI.EchoCharacter = '•'
	m.panelRunnerWorkspace = m.newPanelTextInput(workspace, m.locale.RunnerWorkspacePlaceholder)
	m.panelRunnerEditField = 0
}

// newPanelTextInput 创建一个配置好的 textinput 用于面板输入
func (m *cliModel) newPanelTextInput(value, placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.SetWidth(m.panelWidth(50))
	ti.SetValue(value)
	if value != "" {
		ti.CursorEnd()
	}
	tiStyles := ti.Styles()
	tiStyles.Focused.Prompt = m.styles.TIPrompt
	tiStyles.Focused.Text = m.styles.TIText
	tiStyles.Focused.Placeholder = m.styles.TIPlaceholder
	tiStyles.Cursor.Color = m.styles.TICursor.GetForeground()
	ti.SetStyles(tiStyles)
	ti.Focus()
	return ti
}

// updateRunnerPanel 处理 Runner 面板的键盘事件
func (m *cliModel) updateRunnerPanel(msg tea.KeyPressMsg) (bool, tea.Model, tea.Cmd) {
	// 已连接状态：显示连接信息 + 断开按钮
	rb := m.runnerBridge
	if rb == nil {
		m.closePanel()
		return true, m, nil
	}

	status := rb.Status()

	// 连接中：只允许 Esc
	if status == RunnerConnecting {
		if msg.Code == tea.KeyEsc || msg.String() == "ctrl+c" {
			return m.closePanelAndResume()
		}
		return true, m, nil
	}

	// 已连接：导航 + 断开
	if status == RunnerConnected {
		switch msg.Code {
		case tea.KeyEsc:
			return m.closePanelAndResume()
		case tea.KeyEnter:
			// 断开连接
			rb.Disconnect()
			m.closePanel()
			return true, m, nil
		}
		return true, m, nil
	}

	// 未连接：表单编辑
	switch msg.Code {
	case tea.KeyEsc:
		return m.closePanelAndResume()

	case tea.KeyUp:
		if m.panelRunnerEditField > 0 {
			m.panelRunnerEditField--
		}
		return true, m, nil

	case tea.KeyDown:
		if m.panelRunnerEditField < 2 {
			m.panelRunnerEditField++
		}
		return true, m, nil

	case tea.KeyTab:
		m.panelRunnerEditField = (m.panelRunnerEditField + 1) % 3
		return true, m, nil

	case tea.KeyEnter:
		// 验证并连接
		serverURL := strings.TrimSpace(m.panelRunnerServerTI.Value())
		token := strings.TrimSpace(m.panelRunnerTokenTI.Value())
		workspace := strings.TrimSpace(m.panelRunnerWorkspace.Value())

		if serverURL == "" {
			m.showTempStatus(m.locale.RunnerServerRequired)
			return true, m, m.clearTempStatusCmd()
		}
		if workspace == "" {
			m.showTempStatus(m.locale.RunnerWorkspaceRequired)
			return true, m, m.clearTempStatusCmd()
		}

		// 保存设置
		if m.channel != nil && m.channel.settingsSvc != nil {
			_ = m.channel.settingsSvc.SetSetting(cliChannelName, cliSenderID, "runner_server", serverURL)
			_ = m.channel.settingsSvc.SetSetting(cliChannelName, cliSenderID, "runner_token", token)
			_ = m.channel.settingsSvc.SetSetting(cliChannelName, cliSenderID, "runner_workspace", workspace)
		}

		// 关闭面板，发起连接
		m.closePanel()

		// 获取 LLM 客户端
		var llmClient llm.LLM
		var models []string
		if m.channel != nil {
			llmClient = m.channel.getLLMClient()
			models = m.channel.getModelList()
		}

		if rb != nil {
			rb.Connect(serverURL, token, workspace, llmClient, models)
		}

		m.showTempStatus(m.locale.RunnerConnecting)
		return true, m, m.clearTempStatusCmd()
	}

	// 将按键传递给当前编辑的 textinput
	var cmd tea.Cmd
	switch m.panelRunnerEditField {
	case 0:
		m.panelRunnerServerTI, cmd = m.panelRunnerServerTI.Update(msg)
	case 1:
		m.panelRunnerTokenTI, cmd = m.panelRunnerTokenTI.Update(msg)
	case 2:
		m.panelRunnerWorkspace, cmd = m.panelRunnerWorkspace.Update(msg)
	}
	return true, m, cmd
}

// viewRunnerPanel 渲染 Runner 管理面板
func (m *cliModel) viewRunnerPanel() string {
	s := &m.styles
	var sb strings.Builder

	sb.WriteString(s.PanelHeader.Render("🔧 " + m.locale.RunnerPanelTitle))
	sb.WriteString("\n")

	rb := m.runnerBridge
	if rb == nil {
		sb.WriteString(s.PanelDesc.Render(m.locale.RunnerNotAvailable))
		sb.WriteString("\n")
		return m.styles.PanelBox.Render(sb.String())
	}

	status := rb.Status()

	switch status {
	case RunnerConnecting:
		sb.WriteString("\n")
		sb.WriteString(s.ProgressRunning.Render("⟳ " + m.locale.RunnerConnecting))
		sb.WriteString("\n")
		sb.WriteString(s.PanelDesc.Render("  " + rb.ServerURL()))
		sb.WriteString("\n\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.RunnerPleaseWait))

	case RunnerConnected:
		stats := rb.Stats()
		elapsed := time.Since(stats.ConnectedAt).Round(time.Minute)
		elapsedStr := formatElapsed(int64(elapsed.Milliseconds()))

		sb.WriteString("\n")
		fmt.Fprintf(&sb, "  %s %s (%s)\n",
			s.ProgressDone.Render("●"),
			m.locale.RunnerStatusConnected,
			s.InfoSt.Render(elapsedStr),
		)
		sb.WriteString(s.PanelDesc.Render("  Server: "))
		sb.WriteString(s.InfoSt.Render(rb.ServerURL()))
		sb.WriteString("\n")
		sb.WriteString(s.PanelDesc.Render("  " + m.locale.RunnerWorkspaceLabel + ": "))
		sb.WriteString(s.InfoSt.Render(rb.Workspace()))
		sb.WriteString("\n\n")
		sb.WriteString(s.WarningSt.Render("  [ " + m.locale.RunnerDisconnect + " ]"))
		sb.WriteString("\n\n")
		sb.WriteString(s.PanelHint.Render("  Enter " + m.locale.RunnerDisconnectAction + "  Esc " + m.locale.RunnerBack))

	default: // RunnerDisconnected
		// 显示连接表单
		sb.WriteString("\n")

		fields := []struct {
			label       string
			input       textinput.Model
			active      bool
			placeholder string
		}{
			{m.locale.RunnerServerLabel, m.panelRunnerServerTI, m.panelRunnerEditField == 0, m.locale.RunnerServerPlaceholder},
			{m.locale.RunnerTokenLabel, m.panelRunnerTokenTI, m.panelRunnerEditField == 1, m.locale.RunnerTokenPlaceholder},
			{m.locale.RunnerWorkspaceLabel, m.panelRunnerWorkspace, m.panelRunnerEditField == 2, m.locale.RunnerWorkspacePlaceholder},
		}

		for _, f := range fields {
			prefix := "  "
			if f.active {
				prefix = s.PanelCursor.Render("▸")
			}
			fmt.Fprintf(&sb, "%s %s\n", prefix, f.label)
			sb.WriteString("  ")
			sb.WriteString(f.input.View())
			sb.WriteString("\n")
		}

		sb.WriteString("\n")
		sb.WriteString(s.PanelHint.Render("  " + m.locale.RunnerNavHint))
	}

	return m.styles.PanelBox.Render(sb.String())
}
