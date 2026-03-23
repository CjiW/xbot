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

// CompressResult 压缩结果，区分 LLM 视图和 Session 视图。
type CompressResult struct {
	LLMView     []llm.ChatMessage // 含 tool 消息，继续当前 Run()
	SessionView []llm.ChatMessage // 纯 user/assistant，持久化到 session
}

// taskStatePrompt 任务状态提取提示词，引导 LLM 输出结构化的任务状态文档。
const taskStatePrompt = `You are a task state extraction expert. Extract the current task state from the conversation into a structured document.

## Goal
Transform verbose conversation history into a concise, structured task state document.
Focus on WHAT has been done, WHAT is in progress, and WHAT remains.

## Output Format
Use these structured sections and markers:

### 📋 Task Summary
Brief overview of what the user asked for and current progress.

### 📁 Active Files
@file:{path} — Files currently being worked on (MUST include ALL active file references)
@func:{signature} — Key function signatures from active files

### ✅ Completed Steps
- What has been done so far (with file paths and specific details)

### 🔄 Current Step
What is being worked on right now. Include:
- Current file being edited/read
- Pending modifications
- Context needed to continue

### ❌ Errors (MUST preserve ALL)
@error:{description} — Every error encountered (essential for debugging)

### 📌 Decisions
@decision:{description} — All decisions made during this session

### 📝 Pending Tasks
@todo:{description} — Tasks not yet started

## Compression Rules
1. PRESERVE ALL file paths that appear in active file operations
2. PRESERVE ALL error messages verbatim
3. PRESERVE all function signatures from active files
4. Include specific details (variable names, line numbers, code snippets)
5. If 📂 [offload:...] markers exist, preserve the SUMMARY text but STRIP the offload ID (ol_xxx) — the data cannot be recalled later
6. Prioritize RECENT information over old history
7. This is NOT a summary — it's a task state document for continuing work`

// extractDialogueFromTail 从含 tool 消息的尾部提取纯对话视图。
// 每个 tool group 的摘要融入 assistant 消息。
func extractDialogueFromTail(tail []llm.ChatMessage) []llm.ChatMessage {
	var result []llm.ChatMessage
	var pendingToolSummary strings.Builder

	for _, msg := range tail {
		switch {
		case msg.Role == "user":
			flushPending(&result, &pendingToolSummary)
			result = append(result, llm.NewUserMessage(msg.Content))

		case msg.Role == "assistant" && len(msg.ToolCalls) > 0:
			// assistant 发起了 tool call
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
				// Strip offload ID to prevent stale recall in future turns
				// (offload data is cleaned between turns by CleanSession).
				// Keep summary text: "📂 [offload:ol_xxx] Read(...)\nsummary" → "📂 Read(...)\nsummary"
				stripped := stripRecallID(msg.Content)
				pendingToolSummary.WriteString(truncateRunes(stripped, 800) + "\n")
			} else if strings.HasPrefix(msg.Content, "📂 [masked:") {
				// Strip mask ID — MaskStore is in-memory and doesn't survive across turns.
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
// "📂 [offload:ol_xxx] Read(...)\nsummary"  → "📂 Read(...)\nsummary"
// "📂 [masked:mk_xxx] Shell(cat) — 500 chars — ..."  → "📂 Shell(cat) — 500 chars — ..."
func stripRecallID(content string) string {
	if idx := strings.Index(content, "] "); idx >= 0 {
		return "📂 " + content[idx+2:]
	}
	return content
}

// flushPending 将累积的 tool 执行摘要作为 assistant 消息添加到结果
func flushPending(result *[]llm.ChatMessage, builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	*result = append(*result, llm.NewAssistantMessage(builder.String()))
	builder.Reset()
}

// truncateArgs 截断工具参数用于摘要显示
func truncateArgs(args string, maxLen int) string {
	runes := []rune(args)
	if len(runes) <= maxLen {
		return args
	}
	return string(runes[:maxLen]) + "..."
}

// handleCompress 处理 /compress 命令：手动触发上下文压缩
func (a *Agent) handleCompress(ctx context.Context, msg bus.InboundMessage, tenantSession *session.TenantSession) (*bus.OutboundMessage, error) {
	// 注意：手动 /compress 命令不受 enableAutoCompress 开关限制
	// 用户可能不想自动压缩但偶尔需要手动压缩一下

	// 获取用户特定的 LLM 客户端
	llmClient, model, _, _ := a.llmFactory.GetLLM(msg.SenderID)

	// 使用 buildPrompt 获取完整上下文（包含 system、skills、memory 等）
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

	// 计算完整上下文的 token 数
	tokenCount, err := llm.CountMessagesTokens(messages, model)
	if err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to count tokens for compression")
		// 用户手动触发压缩时，计数失败应该强制执行或报错，而不是静默跳过
	}

	// 检查是否需要压缩（计数失败时也执行，用户明确要求压缩）
	// 直接访问 config 字段而非通过 ContextManager 接口，因为：
	// 1. handleCompress 是手动触发路径，不涉及并发竞争
	// 2. contextManagerConfig 是 Agent 的私有字段，生命周期与 Agent 相同
	// 3. 阈值配置是启动时设定的不变值，不需要锁保护
	threshold := int(float64(a.contextManagerConfig.MaxContextTokens) * a.contextManagerConfig.CompressionThreshold)
	if err == nil && tokenCount < threshold {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("当前上下文 token 数 (%d) 未达到压缩阈值 (%d)，无需压缩。", tokenCount, threshold),
		}, nil
	}

	// 发送压缩开始进度
	_ = a.sendMessage(msg.Channel, msg.ChatID, "🔄 开始压缩上下文...")

	// 执行压缩（通过 ContextManager，保证 /compress 始终可用）
	result, err := a.GetContextManager().ManualCompress(ctx, messages, llmClient, model)
	if err != nil {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩失败: %v", err),
		}, nil
	}

	// 替换会话消息
	// 先收集，全部成功才持久化，避免部分写入导致数据损坏
	if err := tenantSession.Clear(); err != nil {
		log.Ctx(ctx).WithError(err).Warn("Failed to clear session for compression")
		// Clear 失败时只返回压缩结果，不持久化，避免数据损坏
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
	// 部分写入失败，只返回内存结果
	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("上下文压缩完成 (内存): %d → %d tokens (LLM %d 条, Session %d 条)", tokenCount, newTokenCount, len(result.LLMView), len(result.SessionView)),
	}, nil
}

// thinTail 精简尾部旧工具组，保留最近 keepGroups 组完整内容。
// 一个"工具组"= 一条 assistant(tool_calls) + 紧随其后的所有 tool result 消息。
// 对更早的组：截断 Content/Arguments，strip think blocks，保留消息结构不变（API 兼容）。
// thinTail 精简尾部旧工具组，保留最近 keepGroups 组完整内容。
// 一个"工具组"= 一条 assistant(tool_calls) + 紧随其后的所有 tool result 消息。
// 对更早的组：截断 Content/Arguments，strip think blocks，保留消息结构不变（API 兼容）。
//
// BUG FIX: activeFiles 保护从"完全跳过截断"改为"轻量截断"。
// 旧逻辑：涉及活跃文件的组完全不截断 → 编程会话中几乎所有组都涉及活跃文件 → thinTail 完全无效。
// 新逻辑：active 组做轻量截断（保留更多上下文），non-active 组做强截断。
func thinTail(tail []llm.ChatMessage, keepGroups int, activeFiles []ActiveFile) []llm.ChatMessage {
	const (
		// active 组：轻量截断，保留更多上下文（因为涉及当前工作文件）
		activeContentMax = 800
		activeArgsMax    = 300
		// non-active 组：强截断
		thinContentMax = 300
		thinArgsMax    = 200
	)
	if keepGroups <= 0 {
		keepGroups = 3
	}

	// 识别工具组边界：每个 assistant(tool_calls) 开始一个新组，后续 tool 消息属于该组
	type toolGroup struct{ start, end int }
	var groups []toolGroup

	for i := range tail {
		if tail[i].Role == "assistant" && len(tail[i].ToolCalls) > 0 {
			g := toolGroup{start: i, end: i}
			for j := i + 1; j < len(tail) && tail[j].Role == "tool"; j++ {
				g.end = j
			}
			groups = append(groups, g)
		}
	}

	thinCount := len(groups) - keepGroups
	if thinCount <= 0 {
		return tail
	}

	// 构建活跃文件路径集合
	activePaths := make(map[string]bool)
	for _, af := range activeFiles {
		activePaths[af.Path] = true
	}

	result := make([]llm.ChatMessage, len(tail))
	copy(result, tail)

	for g := range thinCount {
		grp := groups[g]
		// 检查是否涉及活跃文件
		isActive := false
		for j := grp.start; j <= grp.end && !isActive; j++ {
			msg := tail[j]
			if msg.Role == "assistant" {
				for _, tc := range msg.ToolCalls {
					paths := extractPathsFromToolArgs(tc.Name, tc.Arguments)
					for _, p := range paths {
						if activePaths[p] {
							isActive = true
							break
						}
					}
				}
			}
		}
		// 根据是否 active 选择截断强度
		contentMax := thinContentMax
		argsMax := thinArgsMax
		if isActive {
			contentMax = activeContentMax
			argsMax = activeArgsMax
		}
		for j := grp.start; j <= grp.end; j++ {
			msg := result[j] // copy struct
			switch msg.Role {
			case "assistant":
				msg.Content = llm.StripThinkBlocks(msg.Content)
				msg.Content = truncateRunes(msg.Content, contentMax)
				if len(msg.ToolCalls) > 0 {
					tcs := make([]llm.ToolCall, len(msg.ToolCalls))
					copy(tcs, msg.ToolCalls)
					for k := range tcs {
						tcs[k].Arguments = truncateRunes(tcs[k].Arguments, argsMax)
					}
					msg.ToolCalls = tcs
				}
			case "tool":
				msg.Content = truncateRunes(msg.Content, contentMax)
				msg.ToolArguments = truncateRunes(msg.ToolArguments, argsMax)
			}
			result[j] = msg
		}
	}

	return result
}

// aggressiveThinTail 激进版 thinTail，用于 normal thinTail 压缩不足时的回退。
// 与 thinTail 相同逻辑，但截断长度更短（100 vs 300），且对 assistant(tool_calls)
// 消息也完全清空 Content（只保留 tool_calls 结构）。
//
// BUG FIX: 同 thinTail，activeFiles 从"完全跳过"改为"轻量截断"。
func aggressiveThinTail(tail []llm.ChatMessage, keepGroups int, activeFiles []ActiveFile) []llm.ChatMessage {
	const (
		// active 组：轻量截断
		activeContentMax = 200
		activeArgsMax    = 120
		// non-active 组：激进截断
		thinContentMax = 100
		thinArgsMax    = 80
	)
	if keepGroups <= 0 {
		keepGroups = 1
	}

	type toolGroup struct{ start, end int }
	var groups []toolGroup
	for i := range tail {
		if tail[i].Role == "assistant" && len(tail[i].ToolCalls) > 0 {
			g := toolGroup{start: i, end: i}
			for j := i + 1; j < len(tail) && tail[j].Role == "tool"; j++ {
				g.end = j
			}
			groups = append(groups, g)
		}
	}

	thinCount := len(groups) - keepGroups
	if thinCount <= 0 {
		return tail
	}

	// 构建活跃文件路径集合
	activePaths := make(map[string]bool)
	for _, af := range activeFiles {
		activePaths[af.Path] = true
	}

	result := make([]llm.ChatMessage, len(tail))
	copy(result, tail)

	for g := range thinCount {
		grp := groups[g]
		// 跳过涉及活跃文件的组
		isActive := false
		for j := grp.start; j <= grp.end && !isActive; j++ {
			msg := tail[j]
			if msg.Role == "assistant" {
				for _, tc := range msg.ToolCalls {
					paths := extractPathsFromToolArgs(tc.Name, tc.Arguments)
					for _, p := range paths {
						if activePaths[p] {
							isActive = true
							break
						}
					}
				}
			}
		}
		// 根据是否 active 选择截断强度
		contentMax := thinContentMax
		argsMax := thinArgsMax
		if isActive {
			contentMax = activeContentMax
			argsMax = activeArgsMax
		}
		for j := grp.start; j <= grp.end; j++ {
			msg := result[j]
			switch msg.Role {
			case "assistant":
				msg.Content = ""
				if isActive {
					// active 组保留部分 content（而非完全清空）
					msg.Content = truncateRunes(llm.StripThinkBlocks(msg.Content), contentMax)
				}
				if len(msg.ToolCalls) > 0 {
					tcs := make([]llm.ToolCall, len(msg.ToolCalls))
					copy(tcs, msg.ToolCalls)
					for k := range tcs {
						tcs[k].Arguments = truncateRunes(tcs[k].Arguments, argsMax)
					}
					msg.ToolCalls = tcs
				}
			case "tool":
				msg.Content = truncateRunes(msg.Content, contentMax)
				msg.ToolArguments = truncateRunes(msg.ToolArguments, argsMax)
			}
			result[j] = msg
		}
	}

	return result
}

// truncateRunes 截断到 maxLen 个 rune（多字节安全）。
func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "...[truncated]"
}

// compressMessages 使用 LLM 压缩对话历史（独立函数，不依赖 Agent receiver）。
// 不使用 fingerprint 体系，直接压缩。
func compressMessages(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string, topicDetector *TopicDetector) (*CompressResult, error) {
	// 第一步：找到尾部安全切割点
	tailStart := len(messages) // 默认不保留任何尾部消息
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

	// 第二步：精简尾部旧工具组（保留最近 1 组完整，截断更早的组）
	// BUG FIX: keepGroups 从 3 降到 1。
	// 之前保留 3 组完整，当 tool 结果很大时，thinnedTail 仍占大量 token，
	// 导致 LLM 压缩后 new_tokens ≈ original_tokens（压缩无效）。
	var thinnedTail []llm.ChatMessage
	activeFiles := ExtractActiveFiles(messages, 3)
	if tailStart < len(messages) {
		thinnedTail = thinTail(messages[tailStart:], 1, activeFiles)
	}

	// 第三步：分离消息
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

	if len(toCompress) == 0 {
		// 所有非 system 消息都在 tail 中，无需 LLM 压缩，直接 thinTail
		// 但仍需检查缩减效果
		llmView := make([]llm.ChatMessage, 0, len(systemMsgs)+len(thinnedTail))
		llmView = append(llmView, systemMsgs...)
		llmView = append(llmView, thinnedTail...)

		// 检查缩减效果，不足时升级
		originalTokens, _ := llm.CountMessagesTokens(messages, model)
		resultTokens, _ := llm.CountMessagesTokens(llmView, model)
		minTarget := int(float64(originalTokens) * 0.8)
		if resultTokens > minTarget && minTarget > 0 && tailStart < len(messages) {
			// 升级为 aggressive thinning
			aggressiveTail := aggressiveThinTail(messages[tailStart:], 1, activeFiles)
			llmView = make([]llm.ChatMessage, 0, len(systemMsgs)+len(aggressiveTail))
			llmView = append(llmView, systemMsgs...)
			llmView = append(llmView, aggressiveTail...)
		}

		tailSummary := extractDialogueFromTail(llmView[len(systemMsgs):])
		return &CompressResult{
			LLMView:     llmView,
			SessionView: tailSummary,
		}, nil
	}

	// 第三步（话题分区）：Topic-Aware 压缩
	// 如果 topicDetector 可用且检测到多个话题分区，只压缩历史话题，保留当前话题原文。
	// 这避免了"当前正在工作的文件内容被摘要丢失"的问题。
	var currentTopicMsgs []llm.ChatMessage
	if topicDetector != nil && len(toCompress) >= DefaultMinHistory {
		segments, detectErr := topicDetector.Detect(toCompress)
		if detectErr != nil {
			log.Ctx(ctx).WithError(detectErr).Warn("compressMessages: topic detection failed, falling back to standard compress")
		} else if len(segments) > 1 {
			// 找到当前话题（最后一个 IsCurrent=true 的分区）
			var historicalMsgs []llm.ChatMessage
			for _, seg := range segments {
				segMsgs := toCompress[seg.StartIdx:seg.EndIdx]
				if seg.IsCurrent {
					currentTopicMsgs = segMsgs
				} else {
					historicalMsgs = append(historicalMsgs, segMsgs...)
				}
			}
			if len(historicalMsgs) > 0 {
				log.Ctx(ctx).WithFields(map[string]interface{}{
					"total_segments":      len(segments),
					"historical_messages": len(historicalMsgs),
					"current_topic_msgs":  len(currentTopicMsgs),
				}).Info("compressMessages: topic-aware mode, compressing historical topics only")
				toCompress = historicalMsgs
			}
		}
	}

	// 第四步：构建压缩 prompt
	// BUG FIX: 之前每条消息截断到 2000 rune 但消息数不限，导致 prompt 可能非常长，
	// LLM 压缩后摘要比原文还长（压缩无效）。
	// 新策略：计算 toCompress 总 token 预估，动态调整每条消息截断长度。
	var historyText strings.Builder
	totalRunes := 0
	maxHistoryRunes := 16000 // 控制压缩 prompt 总长度，确保 LLM 有足够余量生成摘要

	for _, msg := range toCompress {
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
		msgLine := fmt.Sprintf("[%s] %s\n\n", role, content)

		// 动态截断：剩余预算不足时缩短
		remaining := maxHistoryRunes - totalRunes
		if remaining <= 0 {
			break // 预算用完，停止添加
		}
		if len(runes) > 2000 {
			runes = runes[:2000]
			msgLine = fmt.Sprintf("[%s] %s...\n\n", role, string(runes))
		}
		msgRunes := len([]rune(msgLine))
		if msgRunes > remaining {
			// 截断整条到剩余预算
			truncated := string([]rune(msgLine)[:remaining])
			historyText.WriteString(truncated)
			break
		}
		historyText.WriteString(msgLine)
		totalRunes += msgRunes
	}

	// 计算目标摘要长度：确保压缩后比原文短
	originalTokens, _ := llm.CountMessagesTokens(messages, model)
	// 摘要目标 = original 的 30%（至少 500，最多 5000 rune）
	targetSummaryRunes := int(float64(originalTokens) * 0.3 * 1.5) // tokens → runes 粗估
	if targetSummaryRunes < 500 {
		targetSummaryRunes = 500
	}
	if targetSummaryRunes > 5000 {
		targetSummaryRunes = 5000
	}

	compressionPrompt := taskStatePrompt + fmt.Sprintf(`

## IMPORTANT: Output Length Constraint
Your compressed output MUST be at most %d characters. Be extremely concise.
Prioritize: errors, file paths, current state, pending tasks. Drop verbose details.

## Conversation History (to compress)
`, targetSummaryRunes) + historyText.String() + `

Output the compressed content directly.`

	// 第五步：调用 LLM 压缩
	resp, err := client.Generate(ctx, model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a context compression expert. You MUST keep your output concise and under the specified length limit."),
		llm.NewUserMessage(compressionPrompt),
	}, nil, "")
	if err != nil {
		return nil, fmt.Errorf("LLM compress failed: %w", err)
	}
	GlobalMetrics.TotalLLMCalls.Add(1)
	if resp != nil {
		GlobalMetrics.TotalInputTokens.Add(resp.Usage.PromptTokens)
		GlobalMetrics.TotalOutputTokens.Add(resp.Usage.CompletionTokens)
	}

	compressed := llm.StripThinkBlocks(resp.Content)

	// 如果 LLM 输出超过目标长度，重新压缩而非截断（截断会丢信息）
	compressedRunes := []rune(compressed)
	if len(compressedRunes) > targetSummaryRunes {
		log.Ctx(ctx).WithFields(map[string]interface{}{
			"compressed_runes":     len(compressedRunes),
			"target_summary_runes": targetSummaryRunes,
		}).Warn("compressMessages: LLM output exceeded target, re-compressing with stricter prompt")
		// 第二轮压缩：让 LLM 在自身输出基础上进一步精简
		recompressPrompt := fmt.Sprintf(`The following compressed context is still too long (%d chars, target: %d chars).
Compress it further. Be extremely aggressive: keep ONLY file paths, error messages, and current task state.
Drop ALL verbose explanations, step-by-step descriptions, and redundant details.
Output at most %d characters.

## Content to re-compress:
%s`, len(compressedRunes), targetSummaryRunes, targetSummaryRunes, compressed)

		resp2, err := client.Generate(ctx, model, []llm.ChatMessage{
			llm.NewSystemMessage("You are a context compression expert. Output MUST be under the specified character limit."),
			llm.NewUserMessage(recompressPrompt),
		}, nil, "")
		if err != nil {
			log.Ctx(ctx).WithError(err).Warn("compressMessages: re-compress failed, keeping original LLM output")
		} else {
			GlobalMetrics.TotalLLMCalls.Add(1)
			if resp2 != nil {
				GlobalMetrics.TotalInputTokens.Add(resp2.Usage.PromptTokens)
				GlobalMetrics.TotalOutputTokens.Add(resp2.Usage.CompletionTokens)
			}
			recompressed := llm.StripThinkBlocks(resp2.Content)
			reRunes := []rune(recompressed)
			if len(reRunes) < len(compressedRunes) {
				compressed = recompressed
			}
		}
	}

	// 第六步：构建压缩后的消息结构
	if len(systemMsgs) > 1 {
		// R-01 修复：panic 改为 error 返回，避免运行时崩溃
		log.Ctx(ctx).WithField("system_count", len(systemMsgs)).Error("assert: at most one system message in compress input")
		return nil, fmt.Errorf("compress: expected at most one system message, got %d", len(systemMsgs))
	}
	summaryMsg := llm.NewUserMessage("[Previous conversation context]\n\n" + compressed)

	// LLM View: system + 压缩摘要 + [当前话题原文] + thinnedTail（含 tool 消息）
	llmViewCap := len(systemMsgs) + 1 + len(currentTopicMsgs) + len(thinnedTail)
	llmView := make([]llm.ChatMessage, 0, llmViewCap)
	llmView = append(llmView, systemMsgs...)
	llmView = append(llmView, summaryMsg)
	llmView = append(llmView, currentTopicMsgs...) // 话题分区：当前话题保留原文
	llmView = append(llmView, thinnedTail...)

	// BUG FIX: ineffective 检测 + aggressiveThinTail 升级。
	// 如果 LLM compress + thinTail 后缩减不到 20%，对 tail 做更激进截断。
	// 不再用 mechanicalTruncate（会丢信息），而是只对 tail 部分加大力度。
	resultTokens, _ := llm.CountMessagesTokens(llmView, model)
	minTarget := int(float64(originalTokens) * 0.8) // 至少缩减 20%
	if resultTokens > minTarget && minTarget > 0 && tailStart < len(messages) {
		aggressiveTail := aggressiveThinTail(messages[tailStart:], 1, activeFiles)
		llmView = make([]llm.ChatMessage, 0, len(systemMsgs)+1+len(aggressiveTail))
		llmView = append(llmView, systemMsgs...)
		llmView = append(llmView, summaryMsg)
		llmView = append(llmView, aggressiveTail...)
		aggressiveTokens, _ := llm.CountMessagesTokens(llmView, model)
		log.Ctx(ctx).WithFields(map[string]interface{}{
			"original_tokens":   originalTokens,
			"normal_tokens":     resultTokens,
			"aggressive_tokens": aggressiveTokens,
			"min_target":        minTarget,
		}).Info("Phase 1 compress: normal result insufficient, using aggressive thinning")

		// 如果 aggressiveThinTail 后仍然无效，对 summaryMsg 重新压缩（不用 shortenSummary 截断）
		if aggressiveTokens > minTarget {
			log.Ctx(ctx).WithFields(map[string]interface{}{
				"aggressive_tokens": aggressiveTokens,
				"min_target":        minTarget,
			}).Warn("compressMessages: aggressive thinTail still insufficient, re-compressing summary")
			// 重新压缩：给 LLM 更严格的长度限制
			newTarget := targetSummaryRunes * 2 / 3 // 比 targetSummaryRunes 更短
			recompressPrompt := fmt.Sprintf(`The following compressed context must be shortened to at most %d characters.
Keep ONLY: file paths, active functions, current task state, errors, pending decisions.
Drop everything else. Be ruthlessly concise.

## Content:
%s`, newTarget, compressed)

			resp3, err := client.Generate(ctx, model, []llm.ChatMessage{
				llm.NewSystemMessage("You are a context compression expert. Output MUST be under the specified character limit."),
				llm.NewUserMessage(recompressPrompt),
			}, nil, "")
			if err != nil {
				log.Ctx(ctx).WithError(err).Warn("compressMessages: summary re-compress failed, keeping current")
			} else {
				GlobalMetrics.TotalLLMCalls.Add(1)
				if resp3 != nil {
					GlobalMetrics.TotalInputTokens.Add(resp3.Usage.PromptTokens)
					GlobalMetrics.TotalOutputTokens.Add(resp3.Usage.CompletionTokens)
				}
				recompressed := llm.StripThinkBlocks(resp3.Content)
				if len([]rune(recompressed)) < len([]rune(compressed)) {
					compressed = recompressed
					summaryMsg = llm.NewUserMessage("[Previous conversation context]\n\n" + compressed)
				}
			}
			llmView = make([]llm.ChatMessage, 0, len(systemMsgs)+1+len(aggressiveTail))
			llmView = append(llmView, systemMsgs...)
			llmView = append(llmView, summaryMsg)
			llmView = append(llmView, aggressiveTail...)
			finalTokens, _ := llm.CountMessagesTokens(llmView, model)
			log.Ctx(ctx).WithFields(map[string]interface{}{
				"final_tokens": finalTokens,
			}).Info("Phase 1 compress: re-compressed summary after aggressive still insufficient")
		}
	}

	// Session View: 压缩摘要 + 尾部对话摘要（纯 user/assistant）
	var sessionTailSummary []llm.ChatMessage
	if len(llmView) > len(systemMsgs)+1 {
		sessionTailSummary = extractDialogueFromTail(llmView[len(systemMsgs)+1:])
	}
	sessionView := make([]llm.ChatMessage, 0, 1+len(sessionTailSummary))
	sessionView = append(sessionView, summaryMsg)
	sessionView = append(sessionView, sessionTailSummary...)

	return &CompressResult{
		LLMView:     llmView,
		SessionView: sessionView,
	}, nil
}
