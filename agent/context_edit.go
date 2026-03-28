package agent

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
)

// ContextEditAction 定义 context_edit 工具的操作类型。
type ContextEditAction string

const (
	ContextEditDelete    ContextEditAction = "delete"
	ContextEditDeleteTurn ContextEditAction = "delete_turn"
	ContextEditTruncate  ContextEditAction = "truncate"
	ContextEditReplace   ContextEditAction = "replace"
	ContextEditList      ContextEditAction = "list"
)

// ContextEditRequest 是 context_edit 工具的请求参数。
type ContextEditRequest struct {
	Action     ContextEditAction `json:"action"`
	MessageIdx int               `json:"message_idx"`
	MaxChars   int               `json:"max_chars"`
	OldText    string            `json:"old_text"`
	NewText    string            `json:"new_text"`
	Reason     string            `json:"reason"`
}

// ContextEditResult 是 context_edit 的执行结果。
type ContextEditResult struct {
	Action     ContextEditAction `json:"action"`
	MessageIdx int               `json:"message_idx"`
	Role       string            `json:"role"`
	Reason     string            `json:"reason"`
	Before     string            `json:"before_chars"`
	After      string            `json:"after_chars"`
	EditedAt   time.Time         `json:"edited_at"`
}

// ContextEditStore 管理 context editing 的历史记录。
type ContextEditStore struct {
	mu      sync.RWMutex
	history []ContextEditResult
	maxSize int
}

// NewContextEditStore 创建 ContextEditStore。
func NewContextEditStore(maxSize int) *ContextEditStore {
	if maxSize <= 0 {
		maxSize = 100
	}
	return &ContextEditStore{maxSize: maxSize}
}

// Record 记录一次 context edit 操作。
func (s *ContextEditStore) Record(result ContextEditResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.history) >= s.maxSize {
		s.history = s.history[1:]
	}
	s.history = append(s.history, result)
}

// History 返回编辑历史（最近优先）。
func (s *ContextEditStore) History() []ContextEditResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]ContextEditResult, len(s.history))
	copy(result, s.history)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

// ContextEditor 执行 context editing 操作。
// 它持有一个指向 messages slice 的指针，由 engine.go 在每次 Run 开始时设置。
type ContextEditor struct {
	Store    *ContextEditStore
	messages []llm.ChatMessage // 当前对话消息，由 engine 在 Run 时设置
	mu       sync.RWMutex      // 保护 messages 引用
}

// NewContextEditor 创建 ContextEditor。
func NewContextEditor(store *ContextEditStore) *ContextEditor {
	return &ContextEditor{Store: store}
}

// SetMessages 设置当前 messages slice 引用（engine 在每次 Run 开始时调用）。
func (e *ContextEditor) SetMessages(messages []llm.ChatMessage) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.messages = messages
}

// HandleRequest 处理 context_edit 请求，直接修改 messages slice。
func (e *ContextEditor) HandleRequest(action string, params map[string]interface{}) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	msgs := e.messages

	if msgs == nil {
		return "", fmt.Errorf("messages not available (editor not initialized)")
	}

	switch ContextEditAction(action) {
	case ContextEditList:
		return listMessagesByTurn(msgs), nil
	case ContextEditDeleteTurn:
		return e.deleteTurn(msgs, params)
	case ContextEditDelete, ContextEditTruncate, ContextEditReplace:
		return e.applyEdit(msgs, action, params)
	default:
		return "", fmt.Errorf("unknown action: %s (valid: list, delete, delete_turn, truncate, replace)", action)
	}
}

// applyEdit 执行编辑操作并修改 messages slice。
func (e *ContextEditor) applyEdit(messages []llm.ChatMessage, action string, params map[string]interface{}) (string, error) {
	req := ContextEditRequest{
		Action: ContextEditAction(action),
	}

	if v, ok := params["message_idx"].(float64); ok {
		req.MessageIdx = int(v)
	} else {
		return "", fmt.Errorf("message_idx is required for %s action", action)
	}

	if v, ok := params["max_chars"].(float64); ok {
		req.MaxChars = int(v)
	}
	if v, ok := params["old_text"].(string); ok {
		req.OldText = v
	}
	if v, ok := params["new_text"].(string); ok {
		req.NewText = v
	}
	if v, ok := params["reason"].(string); ok {
		req.Reason = v
	}
	if req.Reason == "" {
		req.Reason = "not specified"
	}

	// 将用户可见索引映射到 messages slice 索引
	actualIdx := userVisibleIndex(messages, req.MessageIdx)
	if actualIdx < 0 || actualIdx >= len(messages) {
		return "", fmt.Errorf("message index %d out of range (valid: 0-%d)", req.MessageIdx, countUserVisible(messages)-1)
	}

	msg := messages[actualIdx]

	// 安全检查：不允许编辑 system 消息
	if msg.Role == "system" {
		return "", fmt.Errorf("cannot edit system messages")
	}

	// 安全检查：不允许编辑最近的 3 条消息
	visibleCount := countUserVisible(messages)
	if req.MessageIdx >= visibleCount-3 {
		return "", fmt.Errorf("cannot edit recent messages (last 3 messages are protected)")
	}

	beforeChars := fmt.Sprintf("%d chars", len([]rune(msg.Content)))
	var afterChars string

	switch req.Action {
	case ContextEditDelete:
		placeholder := fmt.Sprintf("[context edited: %s — deleted %s at %s]", req.Reason, beforeChars, time.Now().Format("15:04:05"))
		messages[actualIdx].Content = placeholder
		messages[actualIdx].ToolCalls = nil
		afterChars = "0 chars"

	case ContextEditTruncate:
		if req.MaxChars <= 0 {
			req.MaxChars = 200
		}
		runes := []rune(msg.Content)
		if len(runes) <= req.MaxChars {
			return "", fmt.Errorf("message content (%d chars) is already within limit (%d chars)", len(runes), req.MaxChars)
		}
		truncated := string(runes[:req.MaxChars])
		messages[actualIdx].Content = truncated + fmt.Sprintf("\n\n[context edited: truncated from %s to %d chars — %s]", beforeChars, req.MaxChars, req.Reason)
		afterChars = fmt.Sprintf("%d chars", req.MaxChars)

	case ContextEditReplace:
		if req.OldText == "" {
			return "", fmt.Errorf("old_text is required for replace action")
		}
		var newContent string
		var matched bool
		if strings.HasPrefix(req.OldText, "regex:") {
			pattern := strings.TrimPrefix(req.OldText, "regex:")
			re, err := regexp.Compile(pattern)
			if err != nil {
				return "", fmt.Errorf("invalid regex pattern: %w", err)
			}
			newContent = re.ReplaceAllString(msg.Content, req.NewText)
			matched = newContent != msg.Content
		} else {
			if !strings.Contains(msg.Content, req.OldText) {
				return "", fmt.Errorf("old_text not found in message content")
			}
			newContent = strings.ReplaceAll(msg.Content, req.OldText, req.NewText)
			matched = true
		}
		if !matched {
			return "", fmt.Errorf("old_text pattern did not match any content")
		}
		messages[actualIdx].Content = newContent + fmt.Sprintf("\n\n[context edited: replaced text — %s]", req.Reason)
		afterChars = fmt.Sprintf("%d chars", len([]rune(newContent)))
	}

	result := ContextEditResult{
		Action:     req.Action,
		MessageIdx: req.MessageIdx,
		Role:       msg.Role,
		Reason:     req.Reason,
		Before:     beforeChars,
		After:      afterChars,
		EditedAt:   time.Now(),
	}

	if e.Store != nil {
		e.Store.Record(result)
	}

	log.WithFields(map[string]interface{}{
		"action":      req.Action,
		"message_idx": req.MessageIdx,
		"role":        msg.Role,
		"before":      beforeChars,
		"after":       afterChars,
		"reason":      req.Reason,
	}).Info("Context edit applied")

	return fmt.Sprintf("✅ %s message #%d [%s]: %s → %s — %s", req.Action, req.MessageIdx, msg.Role, beforeChars, afterChars, req.Reason), nil
}

// countUserVisible 统计非 system 消息的数量。
func countUserVisible(messages []llm.ChatMessage) int {
	count := 0
	for _, m := range messages {
		if m.Role != "system" {
			count++
		}
	}
	return count
}

// userVisibleIndex 将用户可见的消息索引转换为 messages slice 的实际索引。
func userVisibleIndex(messages []llm.ChatMessage, visibleIdx int) int {
	visibleCount := 0
	for i, m := range messages {
		if m.Role != "system" {
			if visibleCount == visibleIdx {
				return i
			}
			visibleCount++
		}
	}
	return -1
}

// conversationTurn 代表一轮对话（一条 user 消息 + 所有关联的 assistant/tool 消息）。
type conversationTurn struct {
	TurnIdx      int   // 轮次编号（0-based，用户可见）
	StartSliceIdx int  // messages slice 中的起始索引
	EndSliceIdx   int  // messages slice 中的结束索引（inclusive）
	UserSliceIdx  int  // user 消息在 slice 中的索引
	MsgCount      int  // 该轮次包含的消息数量
	ToolCount     int  // tool 消息数量
	TotalChars    int  // 总字符数
	UserPreview   string // user 消息预览
}

// identifyTurns 将 messages slice 按对话轮次分组。
// 一轮从一条 user 消息开始，到下一条 user 消息之前结束。
// system 消息不属于任何轮次。
func identifyTurns(messages []llm.ChatMessage) []conversationTurn {
	var turns []conversationTurn
	currentTurn := -1 // 当前轮次索引

	for i, m := range messages {
		if m.Role == "system" {
			continue
		}

		if m.Role == "user" {
			// 结束上一轮
			if currentTurn >= 0 {
				t := &turns[currentTurn]
				t.EndSliceIdx = i - 1
				t.MsgCount = i - t.StartSliceIdx
			}
			// 开始新一轮
			currentTurn = len(turns)
			preview := m.Content
			if len([]rune(preview)) > 80 {
				preview = string([]rune(preview)[:80]) + "..."
			}
			turns = append(turns, conversationTurn{
				TurnIdx:       currentTurn,
				StartSliceIdx: i,
				UserSliceIdx:  i,
				UserPreview:   preview,
			})
		}

		// 累计当前轮次的统计
		if currentTurn >= 0 {
			t := &turns[currentTurn]
			t.TotalChars += len([]rune(m.Content))
			if m.Role == "tool" {
				t.ToolCount++
			}
		}
	}

	// 结束最后一轮
	if currentTurn >= 0 && len(turns) > 0 {
		t := &turns[currentTurn]
		t.EndSliceIdx = len(messages) - 1
		t.MsgCount = t.EndSliceIdx - t.StartSliceIdx + 1
	}

	return turns
}

// listMessagesByTurn 按对话轮次生成消息列表摘要。
func listMessagesByTurn(messages []llm.ChatMessage) string {
	turns := identifyTurns(messages)

	var sb strings.Builder
	sb.WriteString("📋 Conversation Turns:\n\n")

	if len(turns) == 0 {
		sb.WriteString("(no conversation turns found)\n")
		return sb.String()
	}

	totalMsgs := 0
	for _, t := range turns {
		totalMsgs += t.MsgCount

		fmt.Fprintf(&sb, "Turn %d: 👤 user (%d chars)", t.TurnIdx, len([]rune(messages[t.UserSliceIdx].Content)))
		if t.UserPreview != "" {
			fmt.Fprintf(&sb, " \"%s\"", t.UserPreview)
		}
		sb.WriteString("\n")

		// 统计 assistant 消息数（含 tool call 的迭代轮次）
		assistantCount := 0
		for i := t.StartSliceIdx; i <= t.EndSliceIdx; i++ {
			if messages[i].Role == "assistant" {
				assistantCount++
			}
		}

		fmt.Fprintf(&sb, "  └─ %d messages: %d iterations, %d tool calls, %d total chars\n",
			t.MsgCount, assistantCount, t.ToolCount, t.TotalChars)
	}

	fmt.Fprintf(&sb, "\nTotal: %d turns, %d messages. Use delete_turn to remove entire turns, or use message-level actions (delete/truncate/replace) for fine-grained edits.", len(turns), totalMsgs)
	return sb.String()
}

// deleteTurn 删除整个对话轮次（user 消息 + 所有关联的 assistant/tool 消息）。
func (e *ContextEditor) deleteTurn(messages []llm.ChatMessage, params map[string]interface{}) (string, error) {
	turnIdx, ok := params["turn_idx"].(float64)
	if !ok {
		return "", fmt.Errorf("turn_idx is required for delete_turn action")
	}

	turns := identifyTurns(messages)
	idx := int(turnIdx)
	if idx < 0 || idx >= len(turns) {
		return "", fmt.Errorf("turn index %d out of range (valid: 0-%d)", idx, len(turns)-1)
	}

	// 安全检查：不允许删除最后一轮（当前对话）
	if idx == len(turns)-1 {
		return "", fmt.Errorf("cannot delete the current (last) turn — it is protected")
	}

	t := turns[idx]
	reason := "not specified"
	if v, ok := params["reason"].(string); ok && v != "" {
		reason = v
	}

	// 替换该轮次所有消息的 content 为 placeholder
	placeholder := fmt.Sprintf("[context edited: deleted turn %d (%d messages, %d tool calls) — %s — %s]",
		idx, t.MsgCount, t.ToolCount, reason, time.Now().Format("15:04:05"))

	deletedMsgCount := 0
	for i := t.StartSliceIdx; i <= t.EndSliceIdx; i++ {
		messages[i].Content = placeholder
		messages[i].ToolCalls = nil
		messages[i].ToolName = ""
		messages[i].ToolArguments = ""
		deletedMsgCount++
	}

	if e.Store != nil {
		e.Store.Record(ContextEditResult{
			Action:     ContextEditDeleteTurn,
			MessageIdx: idx,
			Role:       "turn",
			Reason:     reason,
			Before:     fmt.Sprintf("%d msgs", t.MsgCount),
			After:      "0 chars",
			EditedAt:   time.Now(),
		})
	}

	log.WithFields(map[string]interface{}{
		"action":      "delete_turn",
		"turn_idx":    idx,
		"msg_count":   deletedMsgCount,
		"tool_count":  t.ToolCount,
		"reason":      reason,
	}).Info("Context edit: deleted turn")

	return fmt.Sprintf("✅ Deleted turn %d (%d messages, %d tool calls, %d total chars) — %s",
		idx, deletedMsgCount, t.ToolCount, t.TotalChars, reason), nil
}
