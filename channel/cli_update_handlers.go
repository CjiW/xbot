package channel

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// handleKeyPress processes key press events in the main update loop.
// Returns (model, cmds, handled). If handled is true, the caller should return
// immediately; otherwise, post-switch processing (viewport/textarea update) should continue.
func (m *cliModel) handleKeyPress(msg tea.KeyPressMsg, wasTyping bool) (tea.Model, []tea.Cmd, bool) {
	var cmds []tea.Cmd

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
		if m.panelMode == "" && (m.bgTaskCount > 0 || m.agentCount > 0) && m.inputHistoryIdx == -1 {
			m.openBgTasksPanel()
			return m, nil, true
		}

	case msg.Code == tea.KeyUp:
		// If textarea has content, let textarea own multiline vertical cursor movement.
		// Otherwise long pasted input cannot navigate back to earlier lines because
		// viewport/history steals Up before textarea sees it.
		if m.panelMode == "" && m.textarea.Value() != "" {
			break
		}
		// Viewport 不在底部时，方向键优先滚动 viewport（不触发 input history）
		if !m.viewport.AtBottom() {
			m.viewport.ScrollUp(1)
			return m, nil, true
		}
		// §Q 消息队列：typing 时 ↑ 追回最后一条排队消息编辑
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
			// 空输入时浏览历史：仅当有排队消息时才启用，避免误触覆盖输入。
			if len(m.messageQueue) > 0 && m.textarea.Value() == "" && len(m.inputHistory) > 0 {
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

	case msg.Code == tea.KeyDown:
		// If textarea has content, let textarea own multiline vertical cursor movement.
		if m.panelMode == "" && m.textarea.Value() != "" {
			break
		}
		// Viewport 不在底部时，方向键优先滚动 viewport
		if !m.viewport.AtBottom() {
			m.viewport.ScrollDown(1)
			return m, nil, true
		}
		if m.panelMode == "" && !m.typing && m.inputHistoryIdx >= 0 && len(m.messageQueue) > 0 {
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

	case msg.Code == tea.KeyEnter:
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

// handleProgressMsg processes progress update events from the agent.
func (m *cliModel) handleProgressMsg(msg cliProgressMsg) {
	turnID := m.agentTurnID // capture before any mutation
	prev := m.progress

	// Guard: ignore stale progress events arriving after the turn has ended.
	// Without this, a late progress event (especially from SubAgent callbacks
	// running in separate goroutines) can re-set m.progress to non-nil after
	// endAgentTurn cleared it, causing the progress panel to persist.
	// PhaseDone is still allowed through: it's idempotent (endAgentTurn checks turnID).
	if !m.typing && msg.payload != nil && msg.payload.Phase != "done" {
		return
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
	// Preserve StartedAt across progress updates so live timers don't reset.
	// Each structured progress event replaces ActiveTools entirely (StartedAt=zero),
	// so we must carry forward the previous StartedAt values by matching tool name.
	startedAtMap := make(map[string]time.Time)
	if prev != nil {
		for _, t := range prev.ActiveTools {
			if !t.StartedAt.IsZero() {
				startedAtMap[t.Name] = t.StartedAt
			}
		}
	}
	if m.progress != nil {
		for i := range m.progress.ActiveTools {
			t := &m.progress.ActiveTools[i]
			// Restore from previous progress if available
			if prev, ok := startedAtMap[t.Name]; ok {
				t.StartedAt = prev
			} else if t.StartedAt.IsZero() {
				// First appearance: bootstrap from Elapsed or now
				if t.Elapsed > 0 {
					t.StartedAt = time.Now().Add(-time.Duration(t.Elapsed) * time.Millisecond)
				} else {
					t.StartedAt = time.Now()
				}
			}
		}
		// Carry forward CompletedTools from previous progress within the same iteration.
		// Progress events may arrive without CompletedTools (e.g. a thinking-phase event
		// after tool completion), which would cause completed tools to flicker/disappear.
		if len(m.progress.CompletedTools) == 0 && prev != nil && len(prev.CompletedTools) > 0 {
			if m.progress.Iteration == prev.Iteration || m.progress.Iteration == 0 {
				m.progress.CompletedTools = prev.CompletedTools
			}
		}
		// Carry forward Reasoning/Thinking from previous progress within the same iteration.
		// When a tool completes, the server sends a new progress event (Phase="tool") that
		// may not include Reasoning — replacing progress would clear it mid-iteration.
		if prev != nil {
			sameIter := m.progress.Iteration == prev.Iteration || m.progress.Iteration == 0
			if m.progress.Reasoning == "" && prev.Reasoning != "" && sameIter {
				m.progress.Reasoning = prev.Reasoning
			}
			if m.progress.Thinking == "" && prev.Thinking != "" && sameIter {
				m.progress.Thinking = prev.Thinking
			}
			// ReasoningStreamContent: carry forward if new payload doesn't have it
			// and we're still in reasoning streaming phase (no StreamContent yet).
			if m.progress.ReasoningStreamContent == "" && prev.ReasoningStreamContent != "" && sameIter {
				if m.progress.StreamContent == "" {
					m.progress.ReasoningStreamContent = prev.ReasoningStreamContent
				}
			}
		}
	}
	// Preserve SubAgent tree across progress updates within the SAME iteration.
	// Progress events may arrive with incomplete subagent data (missing deep
	// nodes) or no subagent data at all. We preserve the deepest tree seen
	// during the current turn to prevent the TUI from losing deep agent nodes.
	// PhaseDone is the exception — it intentionally clears the tree.
	// When iteration changes, the tree MUST be cleared — there are no cross-iteration
	// active tools, and stale SubAgent markers in progressLines from previous
	// iterations would cause phantom agents to persist.
	if m.progress != nil && m.progress.Phase != "done" && prev != nil {
		iterationChanged := m.progress.Iteration != prev.Iteration && m.progress.Iteration > 0
		if iterationChanged {
			// New iteration started — clear stale SubAgent tree
			m.progress.SubAgents = nil
		} else {
			newDepth := maxTreeDepth(m.progress.SubAgents)
			prevDepth := maxTreeDepth(prev.SubAgents)
			if len(m.progress.SubAgents) == 0 && len(prev.SubAgents) > 0 {
				// New payload has no tree — carry forward old tree
				m.progress.SubAgents = prev.SubAgents
			} else if newDepth < prevDepth && newDepth > 0 {
				// New tree is shallower than old — carry forward old tree
				// (deeper nodes are still running even if this event didn't include them)
				m.progress.SubAgents = prev.SubAgents
			}
		}
	}
	// Update bg task count from callback
	if m.bgTaskCountFn != nil {
		m.bgTaskCount = m.bgTaskCountFn()
	}
	// Update agent count from callback
	if m.agentCountFn != nil {
		m.agentCount = m.agentCountFn()
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
					finalTools = append(finalTools, msg.payload.CompletedTools...)
					// Also include any from lastCompletedTools (race safety)
					for _, t := range m.lastCompletedTools {
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
func (m *cliModel) handleSuHistoryLoad(msg suHistoryLoadMsg) {
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
		return m, idleTickCmd()
	}
	// 兜底上限：~2 秒（40 帧）
	if msg.frame >= 40 {
		m.splashDone = true
		return m, idleTickCmd()
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
