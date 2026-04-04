package agent

import (
	"context"
	"fmt"
	"strings"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
)

// CompressResult holds the compaction output.
type CompressResult struct {
	LLMView     []llm.ChatMessage // Full messages for continuing the current Run()
	SessionView []llm.ChatMessage // User/assistant only, persisted to session
}

// compactionPrompt is the structured contract for LLM-based context compaction.
// Inspired by Claude Code's "working state" contract and Codex's cumulative history.
const compactionPrompt = `You are performing a CONTEXT COMPACTION. Create a structured working state
that allows another LLM to continue this task without re-asking any questions.

## Required Sections

### Historical Context (from previous compactions)
If this conversation contains a summary from a previous compaction (marked with
"[Compacted context]"), extract its key historical thread and include it here.
Each compaction should add 3-5 sentences. Never discard entries from previous
compactions — this is how long sessions preserve continuity.

### Task Summary
What the user asked for and current overall progress (1-3 sentences).

### Key Decisions
Decisions made during this session and WHY they were made (so they are not
re-litigated). Include rejected approaches and the reasoning.

### Active Files
Files currently being worked on (full paths). Include key function signatures
if relevant.

### Errors & Fixes
Errors encountered and how they were resolved. Preserve error messages verbatim.

### Current State & Next Steps
What was being worked on most recently and what should happen next.

## Constraints
- Preserve ALL file paths from active operations
- Preserve ALL error messages verbatim
- Be concise — focus on facts, not narrative
- If offload markers (📂 [offload:...]) exist, preserve the summary text but strip the IDs (e.g. "ol_abc123")
- If masked markers (📂 [masked:...]) exist, preserve the summary text but strip the IDs (e.g. "mk_def456")
- NEVER include offload IDs (ol_...) or mask IDs (mk_...) in your output — they are ephemeral references

## Memory Management (Optional)
If this conversation reveals important new information worth remembering long-term:
- Use the provided memory tools (core_memory_append, archival_memory_insert, etc.) to update memory
- Use archival_memory_search to check for existing similar memories before inserting to avoid duplicates
- This is OPTIONAL — if nothing important needs remembering, skip tool calls and just output the compaction summary`

// continuationMessage is injected after compaction to tell the LLM to resume work.
const continuationMessage = `This conversation was compacted from a longer session. The summary above covers earlier work. Continue from where you left off without re-asking the user any questions.`

// extractDialogueFromTail extracts a pure user/assistant view from a tail
// that may contain tool messages. Tool group summaries are folded into
// assistant messages.
func extractDialogueFromTail(tail []llm.ChatMessage) []llm.ChatMessage {
	var result []llm.ChatMessage
	var pendingToolSummary strings.Builder

	for _, msg := range tail {
		switch {
		case msg.Role == "user":
			flushPending(&result, &pendingToolSummary)
			result = append(result, llm.NewUserMessage(msg.Content))

		case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
			if msg.Content != "" {
				pendingToolSummary.WriteString(msg.Content + "\n")
			}
			for _, tc := range msg.ToolCalls {
				fmt.Fprintf(&pendingToolSummary, "🔧 %s(%s)\n", tc.Name, truncateArgs(tc.Arguments, 100))
			}

		case msg.Role == "assistant":
			flushPending(&result, &pendingToolSummary)
			result = append(result, llm.NewAssistantMessage(msg.Content))

		case msg.Role == "tool":
			if strings.HasPrefix(msg.Content, "📂 [offload:") {
				stripped := stripRecallID(msg.Content)
				pendingToolSummary.WriteString(truncateRunes(stripped, 800) + "\n")
			} else if strings.HasPrefix(msg.Content, "📂 [masked:") {
				stripped := stripRecallID(msg.Content)
				fmt.Fprintf(&pendingToolSummary, "  → %s\n", truncateRunes(stripped, 200))
			} else {
				toolContent := truncateRunes(msg.Content, 200)
				fmt.Fprintf(&pendingToolSummary, "  → %s\n", toolContent)
			}
		}
	}
	flushPending(&result, &pendingToolSummary)
	return result
}

// stripRecallID removes the offload/mask ID from a marker, keeping the rest.
func stripRecallID(content string) string {
	if idx := strings.Index(content, "] "); idx >= 0 {
		return "📂 " + content[idx+2:]
	}
	return content
}

func flushPending(result *[]llm.ChatMessage, builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	*result = append(*result, llm.NewAssistantMessage(builder.String()))
	builder.Reset()
}

func truncateArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) <= maxLen {
		return args
	}
	return string(runes[:maxLen]) + "..."
}

// handleCompress handles the /compress command: manually trigger context compaction.
func (a *Agent) handleCompress(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	llmClient, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	messages, err := a.buildPrompt(ctx, msg, tenantSession)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("构建上下文失败: %v", err),
		}, nil
	}

	if len(messages) == 0 {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "当前没有消息需要压缩。",
		}, nil
	}

	tokenCount, err := llm.CountMessagesTokens(messages, model)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to count tokens for compression")
	}

	threshold := int(float64(a.contextManagerConfig.MaxContextTokens) * a.contextManagerConfig.CompressionThreshold)
	if err == nil && tokenCount < threshold {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("当前上下文 token 数 (%d) 未达到压缩阈值 (%d)，无需压缩。", tokenCount, threshold),
		}, nil
	}

	_ = a.sendMessage(msg.Channel, msg.ChatID, "🔄 开始压缩上下文...")

	cm := a.GetContextManager()

	// Inject memory tools so manual compaction can archive important context
	// (same as the auto-compress path in engine.Run).
	if defs, exec := a.buildMemoryToolSetup(msg.Channel, msg.ChatID); defs != nil {
		cm.SetMemoryTools(defs, exec)
	}

	result, err := cm.ManualCompress(ctx, messages, llmClient, model)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩失败: %v", err),
		}, nil
	}

	if err := tenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to clear session for compression")
		newTokenCount, _ := llm.CountMessagesTokens(result.LLMView, model)
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩完成 (内存): %d → %d tokens (LLM %d 条, Session %d 条)", tokenCount, newTokenCount, len(result.LLMView), len(result.SessionView)),
		}, nil
	}
	allOk := true
	for _, msg := range result.SessionView {
		assertNoSystemPersist(msg)
		if err := tenantSession.AddMessage(msg); err != nil {
			log.Ctx(ctx).WithError(err).Error("Partial write during compression, session may be corrupted")
			allOk = false
			break
		}
	}

	newTokenCount, _ := llm.CountMessagesTokens(result.LLMView, model)
	if allOk {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩完成: %d → %d tokens (LLM %d 条, Session %d 条)", tokenCount, newTokenCount, len(result.LLMView), len(result.SessionView)),
		}, nil
	}
	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("上下文压缩完成 (内存): %d → %d tokens (LLM %d 条, Session %d 条)", tokenCount, newTokenCount, len(result.LLMView), len(result.SessionView)),
	}, nil
}

// formatCompactLine formats a single message for the compaction history text.
func formatCompactLine(msg llm.ChatMessage) string {
	role := strings.ToUpper(msg.Role)
	content := msg.Content
	if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
		var toolNames []string
		for _, tc := range msg.ToolCalls {
			toolNames = append(toolNames, tc.Name)
		}
		content += fmt.Sprintf(" [called tools: %s]", strings.Join(toolNames, ", "))
	}
	runes := []rune(content)
	if len(runes) > 2000 {
		content = string(runes[:2000]) + "..."
	}
	return fmt.Sprintf("[%s] %s\n\n", role, content)
}

// truncateRunes truncates a string to maxLen runes (multi-byte safe).
func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "...[truncated]"
}

// compactMessages performs a structured compaction of conversation history.
//
// Flow:
//  1. Find a safe cut point (last user message or plain assistant message)
//  2. Separate system messages from the history before the cut point
//  3. Build history text within token budget
//  4. Multi-turn LLM call with optional memory tools
//  5. Build result: [system] + [compaction summary] + [continuation] + [tail messages]
func compactMessages(
	ctx context.Context,
	messages []llm.ChatMessage,
	client llm.LLM,
	model string,
	maxContextTokens int,
	memTools []llm.ToolDefinition,
	memToolExec func(ctx context.Context, tc llm.ToolCall) (content string, err error),
) (*CompressResult, error) {
	// Step 1: find tail cut point — keep the last user message and everything after it
	tailStart := len(messages)
	for i := len(messages) - 1; i >= 1; i-- {
		msg := messages[i]
		if msg.Role == "user" {
			tailStart = i
			break
		}
		if msg.Role == "assistant" && len(msg.ToolCalls) == 0 {
			tailStart = i
			break
		}
		if i == 1 {
			tailStart = 1
		}
	}

	// Step 2: separate system messages from content to compress
	var systemMsgs []llm.ChatMessage
	var toCompress []llm.ChatMessage

	for i, msg := range messages {
		if i >= tailStart {
			break
		}
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		} else {
			toCompress = append(toCompress, msg)
		}
	}

	tail := messages[tailStart:]

	if len(toCompress) == 0 {
		// Nothing to compress — all non-system messages are in tail.
		// Return as-is (the caller's InputTooLong handler will retry if needed).
		llmView := make([]llm.ChatMessage, 0, len(systemMsgs)+len(tail))
		llmView = append(llmView, systemMsgs...)
		llmView = append(llmView, tail...)

		sessionView := extractDialogueFromTail(tail)
		return &CompressResult{
			LLMView:     llmView,
			SessionView: sessionView,
		}, nil
	}

	// Step 3: build the history text for the compaction prompt.
	// Scan toCompress from the END (most recent) so that when the token
	// budget is exhausted the oldest messages are dropped — not the most
	// recent work context that the LLM needs to preserve in the summary.
	//
	// Budget: the compaction LLM call is a separate request. Its input is
	//   [system_message] + [compaction_prompt + history_text] + output_tokens
	// We reserve overhead for the system message, prompt template, and the
	// LLM's output; the rest goes to history.
	var historyText strings.Builder

	// Pre-compute per-message token counts for toCompress.
	perMsgTokens := make([]int, len(toCompress))
	totalCompressTokens := 0
	for i, msg := range toCompress {
		t, _ := llm.CountMessagesTokens([]llm.ChatMessage{msg}, model)
		perMsgTokens[i] = t
		totalCompressTokens += t
	}

	// Overhead for the compaction call (system msg ~50t, prompt template ~300t,
	// output budget ~1000t).  Use 1500 as a safe reserve.
	const compactionOverhead = 1500
	historyBudget := maxContextTokens - compactionOverhead
	if historyBudget < 1000 {
		historyBudget = 1000
	}

	// Scan backwards to find how many messages fit.
	fitCount := 0
	usedTokens := 0
	for i := len(toCompress) - 1; i >= 0; i-- {
		if usedTokens+perMsgTokens[i] > historyBudget {
			break
		}
		usedTokens += perMsgTokens[i]
		fitCount++
	}

	omittedCount := len(toCompress) - fitCount
	if omittedCount > 0 {
		fmt.Fprintf(&historyText, "[Note: %d older messages omitted from compaction]\n\n", omittedCount)
	}
	for _, msg := range toCompress[omittedCount:] {
		historyText.WriteString(formatCompactLine(msg))
	}

	// Compute target budget
	originalTokens, _ := llm.CountMessagesTokens(messages, model)
	targetRunes := int(float64(originalTokens) * 0.3 * 1.5) // tokens → runes estimate
	if targetRunes < 500 {
		targetRunes = 500
	}
	if targetRunes > 5000 {
		targetRunes = 5000
	}

	prompt := compactionPrompt + fmt.Sprintf(`

## Output Length
Your output MUST be at most %d characters. Be concise — facts over narrative.

## Conversation History
`, targetRunes) + historyText.String() + `

Output the structured working state directly.`

	// Step 4: multi-turn LLM call with optional memory tools
	compactionMsgs := []llm.ChatMessage{
		llm.NewSystemMessage("You are a context compaction expert. Create a structured working state for task continuation. Stay under the specified length limit."),
		llm.NewUserMessage(prompt),
	}

	var compressed string
	maxToolRounds := 10
	for round := 0; round <= maxToolRounds; round++ {
		resp, err := client.Generate(ctx, model, compactionMsgs, memTools, "")
		if err != nil {
			return nil, fmt.Errorf("compaction failed: %w", err)
		}
		GlobalMetrics.TotalLLMCalls.Add(1)
		if resp != nil {
			GlobalMetrics.TotalInputTokens.Add(resp.Usage.PromptTokens)
			GlobalMetrics.TotalOutputTokens.Add(resp.Usage.CompletionTokens)
		}

		if !resp.HasToolCalls() {
			compressed = llm.StripThinkBlocks(resp.Content)
			break
		}

		// Memory tool calls — execute and append results
		assistantMsg := llm.ChatMessage{Role: "assistant", ToolCalls: resp.ToolCalls}
		compactionMsgs = append(compactionMsgs, assistantMsg)
		for _, tc := range resp.ToolCalls {
			var resultContent string
			if memToolExec != nil {
				resultContent, _ = memToolExec(ctx, tc)
			} else {
				resultContent = "Error: memory tools not available"
			}
			toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, resultContent)
			compactionMsgs = append(compactionMsgs, toolMsg)
		}
	}

	// Fallback: if the LLM exhausted all tool rounds without producing text,
	// send one final call WITHOUT tools to force a text summary.
	if compressed == "" {
		log.Ctx(ctx).WithField("tool_rounds", maxToolRounds).Warn("Compaction exhausted tool rounds, forcing final summary without tools")
		forceMsgs := append(compactionMsgs, llm.NewAssistantMessage("Memory operations complete. Now produce the compaction summary."))
		resp, err := client.Generate(ctx, model, forceMsgs, nil, "")
		if err != nil {
			return nil, fmt.Errorf("compaction fallback failed: %w", err)
		}
		GlobalMetrics.TotalLLMCalls.Add(1)
		if resp != nil {
			GlobalMetrics.TotalInputTokens.Add(resp.Usage.PromptTokens)
			GlobalMetrics.TotalOutputTokens.Add(resp.Usage.CompletionTokens)
		}
		compressed = llm.StripThinkBlocks(resp.Content)
	}

	if compressed == "" {
		return nil, fmt.Errorf("compaction LLM produced no output even after fallback")
	}

	// Step 5: build compacted message structure
	if len(systemMsgs) > 1 {
		log.Ctx(ctx).WithField("system_count", len(systemMsgs)).Error("assert: at most one system message in compact input")
		return nil, fmt.Errorf("compact: expected at most one system message, got %d", len(systemMsgs))
	}

	summaryMsg := llm.NewUserMessage("[Compacted context]\n\n" + compressed)
	continuationMsg := llm.NewUserMessage(continuationMessage)

	// LLM View: system + compaction summary + continuation instruction + tail
	llmView := make([]llm.ChatMessage, 0, len(systemMsgs)+2+len(tail))
	llmView = append(llmView, systemMsgs...)
	llmView = append(llmView, summaryMsg)
	llmView = append(llmView, continuationMsg)
	llmView = append(llmView, tail...)

	// Session View: compaction summary + tail dialogue (user/assistant only)
	tailDialogue := extractDialogueFromTail(tail)
	sessionView := make([]llm.ChatMessage, 0, 1+len(tailDialogue))
	sessionView = append(sessionView, summaryMsg)
	sessionView = append(sessionView, tailDialogue...)

	newTokens, _ := llm.CountMessagesTokens(llmView, model)
	log.Ctx(ctx).WithFields(map[string]interface{}{
		"original_tokens": originalTokens,
		"new_tokens":      newTokens,
		"tail_messages":   len(tail),
	}).Info("Context compaction completed")

	return &CompressResult{
		LLMView:     llmView,
		SessionView: sessionView,
	}, nil
}
