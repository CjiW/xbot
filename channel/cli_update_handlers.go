package channel

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	log "xbot/logger"
)

// handleKeyPress processes key press events in the main update loop.
// Returns (model, cmds, handled). If handled is true, the caller should return
// immediately; otherwise, post-switch processing (viewport/textarea update) should continue.
func (m *cliModel) handleKeyPress(msg tea.KeyPressMsg, wasTyping bool) (tea.Model, []tea.Cmd, bool) {

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

	// NOTE: Ctrl+C is handled at the top of Update() — never handle it here.
	// This case only remains to prevent Ctrl+C from falling through to the
	// textarea (which would insert a ^C character).
	switch {
	case msg.String() == "ctrl+c":
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

	case msg.String() == "ctrl+p":
		// Ctrl+P: Quick switch subscription
		if m.panelMode == "" && m.subscriptionMgr != nil && !m.typing {
			m.openQuickSwitch("subscription")
			return m, nil, true
		}

	case msg.String() == "ctrl+t":
		// Ctrl+T: Open Sessions panel (T = Tabs/Sessions)
		if m.panelMode == "" {
			m.openSessionsPanel()
			return m, nil, true
		}

	case msg.String() == "ctrl+n":
		// Cycle model (next in list)
		// Uses Ctrl+N instead of Ctrl+M because Ctrl+M is indistinguishable
		// from Enter on Windows VT Input Mode (Char=\r in both cases).
		if m.panelMode == "" && !m.typing && m.channel != nil {
			m.cycleModel()
			// Drain pending cmds (e.g. showTempStatus timer) immediately
			// to avoid an extra Update→View cycle on the next frame.
			if len(m.pendingCmds) > 0 {
				pending := m.pendingCmds
				m.pendingCmds = nil
				return m, []tea.Cmd{tea.Batch(pending...)}, true
			}
			return m, nil, true
		}

	case msg.Text == "^":
		// ^ opens bg tasks panel only when input is empty AND there are running tasks.
		// Gate prevents intercepting the ^ character during normal typing.
		if m.panelMode == "" && m.inputHistoryIdx == -1 && m.bgTaskCount > 0 {
			m.openBgTasksPanel()
			return m, nil, true
		}

	case msg.Code == tea.KeyUp && msg.Mod == tea.ModShift:
		// Shift+Up: recall queued message for editing / browse input history.
		if m.panelMode == "" && m.textarea.Value() != "" {
			return m, nil, true
		}
		if !m.viewport.AtBottom() {
			return m, nil, true
		}
		// §Q 消息队列：typing 时 Shift+↑ 追回最后一条排队消息编辑
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
			// 空输入时浏览历史
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

	case msg.Code == tea.KeyUp:
		// Plain ArrowUp: only viewport scroll (no queue recall / history).
		// If textarea has content, let textarea own multiline vertical cursor movement.
		if m.panelMode == "" && m.textarea.Value() != "" {
			break
		}
		// Viewport 不在底部时，方向键滚动 viewport
		if !m.viewport.AtBottom() {
			m.viewport.ScrollUp(1)
			return m, nil, true
		}

	case msg.Code == tea.KeyDown && msg.Mod == tea.ModShift:
		// Shift+Down: browse input history backwards.
		if m.panelMode == "" && m.textarea.Value() != "" {
			return m, nil, true
		}
		if !m.viewport.AtBottom() {
			return m, nil, true
		}
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

	case msg.Code == tea.KeyDown:
		// Plain ArrowDown: only viewport scroll.
		if m.panelMode == "" && m.textarea.Value() != "" {
			break
		}
		if !m.viewport.AtBottom() {
			m.viewport.ScrollDown(1)
			return m, nil, true
		}

	case msg.Code == tea.KeyEnter:
		model, enterCmds, handled := m.handleEnterKey()
		if handled {
			return model, enterCmds, true
		}

	case msg.Code == tea.KeyTab:
		// §8 Tab 命令补全
		m.handleTabComplete()
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

// restoreIterationHistory converts IterationHistory from a reconnect snapshot
// into local iteration history, bootstrapping tool StartedAt timestamps.
func (m *cliModel) restoreIterationHistory(payload *CLIProgressPayload) {
	if payload == nil || len(payload.IterationHistory) == 0 || len(m.iterationHistory) > 0 {
		return
	}
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
	m.removeAllToolSummaries()
}

// carryForwardProgressState preserves transient state across progress updates
// (StartedAt timers, CompletedTools, Reasoning/Thinking content, SubAgent trees).
func (m *cliModel) carryForwardProgressState(prev *CLIProgressPayload) {
	if m.progress == nil {
		return
	}

	// Preserve StartedAt across progress updates so live timers don't reset.
	startedAtMap := make(map[string]time.Time)
	if prev != nil {
		for _, t := range prev.ActiveTools {
			if !t.StartedAt.IsZero() {
				startedAtMap[t.Name] = t.StartedAt
			}
		}
	}
	for i := range m.progress.ActiveTools {
		t := &m.progress.ActiveTools[i]
		if sa, ok := startedAtMap[t.Name]; ok {
			t.StartedAt = sa
		} else if t.StartedAt.IsZero() {
			if t.Elapsed > 0 {
				t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
			} else {
				t.StartedAt = time.Now()
			}
		}
	}

	if prev == nil {
		return
	}
	sameIter := m.progress.Iteration == prev.Iteration || m.progress.Iteration == 0

	// Carry forward CompletedTools from previous progress within the same iteration.
	if len(m.progress.CompletedTools) == 0 && len(prev.CompletedTools) > 0 && sameIter {
		m.progress.CompletedTools = prev.CompletedTools
	}

	// Carry forward Reasoning/Thinking content.
	if m.progress.Reasoning == "" && prev.Reasoning != "" && sameIter {
		m.progress.Reasoning = prev.Reasoning
	}
	if m.progress.Thinking == "" && prev.Thinking != "" && sameIter {
		m.progress.Thinking = prev.Thinking
	}
	if m.progress.ReasoningStreamContent == "" && prev.ReasoningStreamContent != "" && sameIter {
		if m.progress.StreamContent == "" {
			m.progress.ReasoningStreamContent = prev.ReasoningStreamContent
		}
	}

	// Preserve SubAgent tree within the same iteration (not across iterations).
	if m.progress.Phase == "done" {
		return
	}
	iterationChanged := m.progress.Iteration != prev.Iteration && m.progress.Iteration > 0
	if iterationChanged {
		m.progress.SubAgents = nil
	} else {
		newDepth := maxTreeDepth(m.progress.SubAgents)
		prevDepth := maxTreeDepth(prev.SubAgents)
		if len(m.progress.SubAgents) == 0 && len(prev.SubAgents) > 0 {
			m.progress.SubAgents = prev.SubAgents
		} else if newDepth < prevDepth && newDepth > 0 {
			m.progress.SubAgents = prev.SubAgents
		}
	}
}

// handleProgressMsg processes progress update events from the agent.
func (m *cliModel) handleProgressMsg(msg cliProgressMsg) {
	// Filter by session: only process progress for the currently viewed session.
	// payload.ChatID is set by ProgressEventHandler as "channel:chatID".
	// Fatal guard: ChatID must never be empty — it identifies which session
	// this progress belongs to. Empty ChatID means the progress bypassed
	// session filtering and would leak into the wrong view.
	if msg.payload != nil && msg.payload.ChatID == "" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
		}).Error("handleProgressMsg: received progress with empty ChatID — this is a programming error")
		return
	}
	if msg.payload != nil && msg.payload.ChatID != "" {
		currentKey := m.channelName + ":" + m.chatID
		if msg.payload.ChatID != currentKey {
			return
		}
	}

	turnID := m.agentTurnID // capture before any mutation
	prev := m.progress

	// Guard: ignore progress after explicit Ctrl+C cancel.
	// PhaseDone is allowed through: it's idempotent (endAgentTurn checks turnID).
	// When switching to a running session with no saved state (first switch),
	// turnCancelled is false and m.typing is false — auto-start below handles it.
	if m.turnCancelled && msg.payload != nil && msg.payload.Phase != "done" {
		return
	}

	// Auto-start turn: when receiving progress for current session but not typing,
	// start the turn. This handles first-switch to a running SubAgent session.
	if !m.typing && msg.payload != nil && msg.payload.Phase != "done" {
		log.WithFields(log.Fields{
			"phase":     msg.payload.Phase,
			"iteration": msg.payload.Iteration,
			"active":    len(msg.payload.ActiveTools),
			"chatID":    msg.payload.ChatID,
		}).Info("handleProgressMsg: auto-start turn")
		m.startAgentTurn()
	}

	// Stream-only payloads (from StreamContentFunc/StreamReasoningFunc) only carry
	// stream content fields. Merge into existing progress instead of replacing to
	// preserve tool/iteration state.
	isStreamOnly := msg.payload != nil &&
		msg.payload.Phase == "" && msg.payload.Iteration == 0 &&
		(msg.payload.StreamContent != "" || msg.payload.ReasoningStreamContent != "")
	if isStreamOnly {
		if m.progress != nil {
			if msg.payload.StreamContent != "" {
				m.progress.StreamContent = msg.payload.StreamContent
			}
			if msg.payload.ReasoningStreamContent != "" {
				m.progress.ReasoningStreamContent = msg.payload.ReasoningStreamContent
			}
		} else if m.typing {
			// Turn started but no structured progress yet — create minimal payload
			m.progress = msg.payload
		}
		return
	}
	m.progress = msg.payload

	// Cache token usage for context bar display — every progress event
	// carries fresh token counts from the agent's updateTokenUsage().
	if m.progress != nil {
		m.cacheTokenUsage(m.progress.TokenUsage)
	}
	if m.cachedMaxContextTokens == 0 {
		m.cachedMaxContextTokens = m.resolveMaxContextTokens()
	}
	if m.cachedCompressRatio == 0 {
		m.cachedCompressRatio = m.resolveCompressRatio()
	}

	// Restore iteration history from reconnect/GetActiveProgress snapshot.
	m.restoreIterationHistory(m.progress)

	m.carryForwardProgressState(prev)

	// Update bg task count from callback
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	// Update agent count from callback
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}

	// HistoryCompacted: context compression replaced the engine's message list.
	// Rebuild m.messages from session storage to stay in sync.
	if msg.payload != nil && msg.payload.HistoryCompacted {
		m.reloadMessagesFromSession()
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
			// Snapshot all completed tools from prev — they belong to iterations
			// that finished before this new iteration started. Don't filter by
			// Iteration field because tools from earlier iterations may have been
			// carried forward via the CompletedTools carry-forward logic.
			prevIterTools := prev.CompletedTools
			prevReasoning := prev.Reasoning
			if prevReasoning == "" {
				prevReasoning = prev.ReasoningStreamContent
			}
			if len(prevIterTools) > 0 || prev.Thinking != "" || prevReasoning != "" {
				snap := cliIterationSnapshot{
					Iteration:   m.lastSeenIteration,
					Thinking:    prev.Thinking,
					Reasoning:   prevReasoning,
					Tools:       prevIterTools,
					ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
				}
				m.iterationHistory = append(m.iterationHistory, snap)
			}
			// Clear lastCompletedTools to prevent stale tools from being
			// re-snapshotted when the final iteration is snapshotted in handleAgentMessage.
			m.lastCompletedTools = m.lastCompletedTools[:0]
			m.lastSeenIteration = msg.payload.Iteration
			m.iterationStartTime = time.Now()
		}

		// §2 工具可视化：快照 CompletedTools 到独立字段
		// Accept all completed tools regardless of their Iteration field — they
		// represent work that finished and should be displayed.
		if len(msg.payload.CompletedTools) > 0 {
			m.lastCompletedTools = make([]CLIToolProgress, len(msg.payload.CompletedTools))
			copy(m.lastCompletedTools, msg.payload.CompletedTools)
		}
		if msg.payload.Phase == "done" {
			// Snapshot the final iteration before clearing progress.
			// This handles the case where PhaseDone arrives before
			// handleAgentMessage (e.g. agent error/cancel).
			// Skip if handleAgentMessage already processed (m.typing == false
			// means the reply arrived and cleaned up iteration state).
			if m.typing && m.lastSeenIteration >= 0 {
				alreadySnapped := slices.ContainsFunc(m.iterationHistory, func(s cliIterationSnapshot) bool {
					return s.Iteration == m.lastSeenIteration
				})
				if !alreadySnapped {
					var finalTools []CLIToolProgress
					// Check progress.CompletedTools first (set by progressFinalizer)
					finalTools = append(finalTools, msg.payload.CompletedTools...)
					// Also include any from lastCompletedTools (race safety)
					for _, t := range m.lastCompletedTools {
						if !slices.ContainsFunc(finalTools, func(existing CLIToolProgress) bool {
							return existing.Name == t.Name && existing.Label == t.Label
						}) {
							finalTools = append(finalTools, t)
						}
					}
					snap := cliIterationSnapshot{
						Iteration:   m.lastSeenIteration,
						Thinking:    msg.payload.Thinking,
						Tools:       finalTools,
						ElapsedWall: time.Since(m.iterationStartTime).Milliseconds(),
					}
					// Carry over reasoning: priority is lastReasoning (captured before progress clear)
					// > prev progress Reasoning > prev ReasoningStreamContent
					// > PhaseDone payload Reasoning
					if m.lastReasoning != "" {
						snap.Reasoning = m.lastReasoning
					} else if prev != nil && prev.Reasoning != "" {
						snap.Reasoning = prev.Reasoning
					} else if prev != nil && prev.ReasoningStreamContent != "" {
						snap.Reasoning = prev.ReasoningStreamContent
					} else if msg.payload.Reasoning != "" {
						snap.Reasoning = msg.payload.Reasoning
					}
					if len(finalTools) > 0 || snap.Thinking != "" || snap.Reasoning != "" {
						m.iterationHistory = append(m.iterationHistory, snap)
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
			m.endAgentTurn(turnID)
			if turnID == m.agentTurnID {
				m.inputReady = true
				if len(m.messageQueue) > 0 {
					m.needFlushQueue = true
				}
			}

			// For agent sessions (interactive SubAgent viewer), the outbound
			// message goes back to the parent agent's channel/chatID — it never
			// arrives as a cliOutboundMsg for this session view. So we must
			// synthesize the assistant message from the progress payload's final
			// content (Thinking field carries the last clean assistant text).
			// For main sessions, handleAgentMessage handles this and will
			// relocate the tool_summary before the assistant reply.
			if m.channelName == "agent" && !m.typing {
				assistantContent := msg.payload.Thinking
				if assistantContent == "" {
					assistantContent = msg.payload.StreamContent
				}
				if assistantContent != "" {
					m.messages = append(m.messages, cliMessage{
						role:      "assistant",
						content:   assistantContent,
						timestamp: time.Now(),
						dirty:     true,
					})
					m.renderCacheValid = false
				}
			}

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
	// Only start a new turn if the agent is idle.
	// If already typing, the agent is processing this message (injectInbound was
	// already called). Starting a new turn here would increment agentTurnID,
	// causing the current turn's endAgentTurn to become a no-op (stale turnID).
	// This produces two user messages without an assistant reply between them.
	if !m.typing {
		m.startAgentTurn()
	}
	// Refresh bg task count on injection
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	// Refresh agent count on injection
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
	}
	m.renderCacheValid = false
	// NOTE: do NOT return tickCmd() here. The wasTyping guard at the bottom of
	// Update() detects idle->typing and starts the tick chain.
	// Returning tickCmd() here creates a duplicate chain (2x spinner speed).
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
// Returns tea.Cmds to start the tick chain when active progress is restored.
func (m *cliModel) handleSuHistoryLoad(msg suHistoryLoadMsg) []tea.Cmd {
	m.suLoading = false

	// Stale result guard: if user switched away from the target session
	// while the async load was in-flight, discard the result.
	if msg.channelName != m.channelName || msg.chatID != m.chatID {
		return nil
	}

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

	// Restore active progress for seamless session switch.
	// msg.activeProgress (from GetActiveProgress RPC) is the authoritative source:
	// if the server says the turn is done or gone, any saved state from
	// restoreSession() is stale and must be discarded.
	var cmds []tea.Cmd
	switch {
	case msg.activeProgress != nil && msg.activeProgress.Phase != "done":
		// Turn is still active on the server. Use the server snapshot regardless
		// of whether restoreSession() also restored state — the server snapshot
		// has the freshest progress data.
		if !m.typing {
			m.startAgentTurn()
		}
		m.progress = msg.activeProgress

		// Restore StartedAt for active tools so live elapsed timers work.
		for i := range m.progress.ActiveTools {
			t := &m.progress.ActiveTools[i]
			if t.StartedAt.IsZero() && t.Elapsed > 0 {
				t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
			}
		}

		// Rebuild iteration history from server snapshot (authoritative).
		m.iterationHistory = nil
		if len(msg.activeProgress.IterationHistory) > 0 {
			for _, ih := range msg.activeProgress.IterationHistory {
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
		}
		// When turn is still active, remove ALL tool_summary messages from
		// loaded history. ConvertMessagesToHistory produces tool_summary from
		// intermediate DB messages with globally-cumulative iteration numbers
		// that don't match the progress block's per-turn iteration numbers.
		// The active progress block owns iteration display entirely — any
		// static tool_summary would duplicate content with mismatched numbers.
		m.removeAllToolSummaries()

		// Emit a tickCmd to guarantee the fast tick chain is running,
		// but only if it's not already active (avoid duplicate chains).
		// See handleSplashTick for the other half of this guard.
		if !m.fastTickActive {
			m.fastTickActive = true
			cmds = append(cmds, tickCmd())
		}

	default:
		// Turn is not active (nil or PhaseDone). If restoreSession() restored
		// a stale typing=true state, clear it — the server snapshot is authoritative.
		if m.typing {
			m.endAgentTurn(m.agentTurnID)
		}
		// Reload history to pick up messages that arrived while we were viewing
		// another session (e.g. the assistant's final reply was filtered out by
		// ChatID check during the agent session view).
		if loader := m.channel.config.DynamicHistoryLoader; loader != nil {
			ch, cid := m.channelName, m.chatID
			cmds = append(cmds, func() tea.Msg {
				history, err := loader(ch, cid)
				if err != nil {
					return cliHistoryReloadMsg{err: err}
				}
				return cliHistoryReloadMsg{history: history}
			})
		}
	}
	return cmds
}

// handleHistoryReload rebuilds m.messages from session storage after context compression.
// Unlike /su which appends, this REPLACES the entire message list because compression
// may have replaced many old messages with a single [Compacted context] summary.
func (m *cliModel) handleHistoryReload(msg cliHistoryReloadMsg) {
	if msg.err != nil {
		log.WithError(msg.err).Warn("Failed to reload history after compression")
		return
	}
	var newMessages []cliMessage
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
		newMessages = append(newMessages, cm)
	}
	m.messages = newMessages
	m.streamingMsgIdx = -1
	m.invalidateAllCache(false)
	m.updateViewportContent()
	m.viewport.GotoBottom()
	log.WithField("count", len(newMessages)).Info("History reloaded after compression")
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
		if m.typing && m.progress != nil && !m.fastTickActive {
			m.fastTickActive = true
			cmds = append(cmds, tickCmd())
		} else if !m.typing || m.progress == nil {
			cmds = append(cmds, idleTickCmd())
		}
		return m, tea.Batch(cmds...)
	}
	// 兜底上限：~2 秒（40 帧）
	if msg.frame >= 40 {
		m.splashDone = true
		if m.typing && m.progress != nil && !m.fastTickActive {
			m.fastTickActive = true
			cmds = append(cmds, tickCmd())
		} else if !m.typing || m.progress == nil {
			cmds = append(cmds, idleTickCmd())
		}
		return m, tea.Batch(cmds...)
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

// maxTreeDepth returns the maximum depth of the SubAgent tree (1 for top-level nodes).
func maxTreeDepth(agents []CLISubAgent) int {
	if len(agents) == 0 {
		return 0
	}
	max := 1
	for _, a := range agents {
		if d := maxTreeDepth(a.Children); d+1 > max {
			max = d + 1
		}
	}
	return max
}

// handleCtrlC handles the unified Ctrl+C keypress.
// Returns (model, cmd, handled). If handled is true, Update() returns immediately.
func (m *cliModel) handleCtrlC() (tea.Model, tea.Cmd, bool) {
	// 1. 关闭所有 overlay/panel
	if m.quickSwitchMode != "" {
		m.quickSwitchMode = ""
	}
	if m.rewindMode {
		m.closeRewindPanel()
	}
	if m.panelMode != "" {
		m.closePanel()
	}
	if m.searchMode {
		m.exitSearch()
	}
	// 2. 取消正在编辑的排队消息
	if m.queueEditing {
		m.queueEditing = false
		m.queueEditBuf = ""
		m.textarea.SetValue("")
	}
	// 3. 如果 agent 正在处理：
	//    - 有排队消息：只清空队列，不发 cancel（需要再按一次 Ctrl+C 才 cancel）
	//    - 无排队消息：发送 cancel
	if m.typing {
		queueLen := len(m.messageQueue)
		if queueLen > 0 {
			m.messageQueue = nil
			m.showSystemMsg(fmt.Sprintf(m.locale.QueueCleared, queueLen), feedbackInfo)
		} else {
			m.sendCancel()
		}
		return m, nil, true
	}
	// 4. 空闲状态：清空输入
	if m.textarea.Value() != "" {
		m.textarea.Reset()
		m.inputHistoryIdx = -1
		m.inputDraft = ""
		m.autoExpandInput()
	}
	return m, nil, true
}

// handleSwitchLLMDoneMsg processes async subscription switch completion.
// Returns (model, cmd, handled).
func (m *cliModel) handleSwitchLLMDoneMsg(done cliSwitchLLMDoneMsg) (tea.Model, tea.Cmd, bool) {
	returnToSettings := m.quickSwitchReturnToPanel
	m.quickSwitchReturnToPanel = false
	if done.err != nil {
		m.showTempStatus(fmt.Sprintf("Failed to switch LLM: %v", done.err))
	} else if done.mgr != nil {
		if err := done.mgr.SetDefault(done.subID, m.chatID); err != nil {
			m.showTempStatus(fmt.Sprintf("LLM switched but failed to save: %v", err))
		} else {
			m.subGeneration++ // subscription actually changed
			m.showTempStatus(fmt.Sprintf("Switched to: %s (%s)", done.subName, done.subModel))
			// Refresh values cache so GetCurrentValues() reflects the new subscription.
			if m.channel != nil && m.channel.config.RefreshValuesCache != nil {
				m.channel.config.RefreshValuesCache()
			}
		}
		// Update cached model name directly from the switch result
		// (same pattern as model-switch case — avoids stale config/RPC reads)
		if done.subModel != "" {
			m.cachedModelName = done.subModel
			// Always refresh modelCount after subscription switch
			// so status bar shows correct count and [Ctrl+N] hint.
			if m.channel.modelLister != nil {
				m.modelCount = len(m.channel.modelLister.ListModels())
			}
		} else {
			// Subscription has no model configured — clear stale model name.
			m.cachedModelName = ""
		}
	}
	// If we came from the settings panel, re-open it so the user can continue editing
	if returnToSettings {
		m.openSettingsFromQuickSwitch()
	}
	// Drain pendingCmds (e.g. showTempStatus timer) — must not return nil cmds
	var cmd tea.Cmd
	if len(m.pendingCmds) > 0 {
		cmd = tea.Batch(m.pendingCmds...)
		m.pendingCmds = nil
	}
	return m, cmd, true
}

// handleTickMsg processes the fast tick (100ms) message.
// Returns tea.Cmds to batch with other commands.
func (m *cliModel) handleTickMsg() []tea.Cmd {
	var cmds []tea.Cmd

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
	// Refresh agent count on tick
	if m.agentCountFn != nil {
		prev := m.agentCount
		m.agentCount = m.agentCountFn()
		if m.agentCount != prev {
			m.renderCacheValid = false
		}
	}
	// Schedule next tick when agent is active or bg tasks are running.
	// IMPORTANT: only emit ONE tickCmd to prevent exponential message growth
	// (two tickCmd() would double the message count every 100ms → CPU explosion).
	busy := m.typing || m.progress != nil
	if (m.bgTaskCountFn != nil && m.bgTaskCount > 0) || (m.agentCountFn != nil && m.agentCount > 0) || busy {
		m.fastTickActive = true
		cmds = append(cmds, tickCmd())
	} else if m.needFlushQueue && len(m.messageQueue) > 0 {
		m.fastTickActive = true
		// Pending queue flush — use fast tick so the queued message
		// is sent promptly (not waiting 3s for idleTickCmd).
		cmds = append(cmds, tickCmd())
	} else {
		// Transition to idle: start low-frequency tick for placeholder rotation
		m.fastTickActive = false
		cmds = append(cmds, idleTickCmd())
	}
	if busy {
		// Advance spinner frame on every tick so the animation stays in sync
		// with elapsed time display. Previously driven by a separate tickerTickMsg
		// chain that could break when m.progress briefly went nil.
		m.ticker.tick()
		// Typewriter is now driven by its own typewriterTickMsg chain (50ms).
		// Start the typewriter chain if there's stream or reasoning content to reveal.
		hasStreamContent := m.progress != nil && m.progress.StreamContent != "" && m.twVisible < len([]rune(m.progress.StreamContent))
		hasReasoningContent := m.progress != nil && m.progress.ReasoningStreamContent != "" && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent))
		if hasStreamContent || hasReasoningContent {
			if !m.typewriterTickActive {
				m.typewriterTickActive = true
				cmds = append(cmds, typewriterTickCmd())
			}
		}
		m.updateViewportContent()
	} else {
		// Not busy: stop typewriter chain
		m.typewriterTickActive = false
		// Still refresh viewport if messages were added/changed (e.g. assistant
		// reply arrived via handleAgentMessage after PhaseDone cleared progress).
		if !m.renderCacheValid {
			m.updateViewportContent()
		}
	}

	// §Q Flush message queue on tick (not in cliProgressMsg/cliOutboundMsg).
	// This ensures the previous reply is already appended to m.messages before
	// the queued message gets sent, producing correct order: msg1, reply1, msg2.
	// Guard: only flush when NOT typing (previous turn fully complete).
	if m.needFlushQueue && !m.typing && len(m.messageQueue) > 0 {
		m.needFlushQueue = false
		m.flushMessageQueue()
		// Always return after flush so the tickCmd queued by startAgentTurn()
		// (inside sendMessageFromQueue → sendMessage) gets picked up in cmds.
		return cmds
	}

	return cmds
}

// handleIdleTick processes the low-frequency idle tick for placeholder rotation.
func (m *cliModel) handleIdleTick() []tea.Cmd {
	var cmds []tea.Cmd
	// Low-frequency idle tick: rotate placeholder and keep alive
	// Remote mode: keep retrying model name fetch until we get one.
	if m.cachedModelName == "" && m.remoteMode {
		m.refreshCachedModelName()
	}
	if !m.typing && m.progress == nil {
		m.updatePlaceholder()
		cmds = append(cmds, idleTickCmd())
	} else if !m.fastTickActive {
		// Self-healing: if fast tick chain broke but we're still busy
		// (typing or progress active), re-arm fast tick.
		m.fastTickActive = true
		cmds = append(cmds, tickCmd())
	}
	return cmds
}

// handleTypewriterTick advances the typewriter effect and continues the chain.
func (m *cliModel) handleTypewriterTick() []tea.Cmd {
	var cmds []tea.Cmd
	// Advance typewriter by 1 rune on its own 50ms cadence.
	m.advanceTypewriter()
	m.updateViewportContent()
	// Continue chain if still behind on either stream or reasoning content
	streamBehind := m.progress != nil && m.progress.StreamContent != "" && m.twVisible < len([]rune(m.progress.StreamContent))
	reasoningBehind := m.progress != nil && m.progress.ReasoningStreamContent != "" && m.rwVisible < len([]rune(m.progress.ReasoningStreamContent))
	if m.typewriterTickActive && (streamBehind || reasoningBehind) {
		cmds = append(cmds, typewriterTickCmd())
	} else {
		m.typewriterTickActive = false
	}
	return cmds
}

// handleSplashDone processes the splash screen completion.
func (m *cliModel) handleSplashDone() []tea.Cmd {
	var cmds []tea.Cmd
	// §14 启动画面结束确认
	m.splashDone = true
	// Remote mode: retry model name fetch — the initial call in cli.go:76
	// may have failed if the WS RPC wasn't fully ready yet.
	if m.cachedModelName == "" && m.remoteMode {
		m.refreshCachedModelName()
	}
	if m.typing && m.progress != nil && !m.fastTickActive {
		m.fastTickActive = true
		cmds = append(cmds, tickCmd())
	} else if !m.typing || m.progress == nil {
		cmds = append(cmds, idleTickCmd())
	}
	return cmds
}

// handleHistoryLoad loads pre-converted history messages into the model.
func (m *cliModel) handleHistoryLoad(msg cliHistoryLoadMsg) {
	if len(msg.history) > 0 {
		m.messages = append(m.messages, msg.history...)
		m.invalidateAllCache(false)
		m.updateViewportContent()
		if m.streamingMsgIdx < 0 {
			m.viewport.GotoBottom()
		}
		log.WithFields(log.Fields{"count": len(msg.history)}).Info("Applied history load in Update loop")
	}
}

// handleApprovalRequest shows the approval dialog for a permission request.
func (m *cliModel) handleApprovalRequest(msg approvalRequestMsg) (tea.Model, tea.Cmd) {
	// Permission control: show approval dialog
	m.approvalRequest = &msg.request
	m.approvalResultCh = msg.resultCh
	m.approvalCursor = 0 // default to Approve
	m.approvalEnteringDeny = false
	m.approvalDenyInput = textinput.New()
	m.approvalDenyInput.Placeholder = "Optional deny reason for LLM"
	m.approvalDenyInput.CharLimit = 200
	m.approvalDenyInput.SetWidth(60)
	m.panelMode = "approval"
	m.renderCacheValid = false
	return m, nil
}

// handleSearchKey processes key events when search mode is active.
// Returns (model, cmd, handled). If handled is true, Update() returns immediately.
func (m *cliModel) handleSearchKey(key tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case m.searchEditing:
		switch key.String() {
		case "enter":
			m.executeSearch()
			return m, nil, true
		case "esc":
			m.exitSearch()
			return m, nil, true
		}
		var cmd tea.Cmd
		m.searchTI, cmd = m.searchTI.Update(key)
		return m, cmd, true
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
			return m, nil, true
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
			return m, nil, true
		case "esc":
			m.exitSearch()
			return m, nil, true
		}
		return m, nil, true
	}
}

// handleEnterKey processes the Enter keypress for sending messages, queue management,
// and file completion. Returns (model, cmds, handled).
func (m *cliModel) handleEnterKey() (tea.Model, []tea.Cmd, bool) {
	var cmds []tea.Cmd

	// Plain Enter sends. Modified/newline-intent variants should fall through to
	// the textarea so its native multiline/internal-scroll behavior works,
	// especially once the input reaches MaxHeight.
	// Note: ctrl+j is handled earlier in Update() via isCtrlJ() → InsertString("\n").
	// Note: cycleModel uses Ctrl+N (not Ctrl+M), so no need to intercept here.
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
			return m, nil, true
		}
		return m, nil, true
	}
	// §8b @ 模式：Enter 进入目录或确认文件
	// Check fileCompletions even without Tab (fileCompActive=false):
	// typing @path auto-populates completions via input change handler.
	if len(m.fileCompletions) > 0 {
		input := m.textarea.Value()
		if ok, prefix := detectAtPrefix(input); ok {
			selected := m.fileCompletions[m.fileCompIdx]
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
	// NOTE: tick chain is started by startAgentTurn() inside sendMessage().
	// No need to emit tickCmd() here — doing so would create duplicate chains.
	return m, cmds, true
}
