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
5. If 📂 [offload:...] markers exist, preserve them verbatim
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
				// 保留 offload 摘要完整，不截断
				pendingToolSummary.WriteString(msg.Content + "\n")
			} else {
				toolContent := truncateRunes(msg.Content, 200)
				fmt.Fprintf(&pendingToolSummary, "  → %s\n", toolContent)
			}
		}
	}
	flushPending(&result, &pendingToolSummary)
	return result
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
	messages, err := a.buildPrompt(ctx, msg, tenantSession, false)
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

// compressMessagesWithFingerprintAndLostItems 使用 LLM 压缩对话历史，并注入指纹和丢失信息引导。
// 与 compressMessagesWithFingerprint 相同，但在 prompt 中额外追加"之前丢失的信息"，
// 用于质量不达标时的重试，引导 LLM 补全遗漏的关键信息。
func compressMessagesWithFingerprintAndLostItems(ctx context.Context, messages []llm.ChatMessage, fp KeyInfoFingerprint, lostItems []string, client llm.LLM, model string) (*CompressResult, error) {
	// 构建增强指纹：将丢失项追加为 Decisions
	enhancedFP := fp
	for _, item := range lostItems {
		trimmed := strings.TrimPrefix(item, "file:")
		trimmed = strings.TrimPrefix(trimmed, "identifier:")
		trimmed = strings.TrimPrefix(trimmed, "error:")
		trimmed = strings.TrimPrefix(trimmed, "decision:")
		enhancedFP.Decisions = append(enhancedFP.Decisions, "[MUST INCLUDE] "+trimmed)
	}
	return compressMessagesWithFingerprint(ctx, messages, enhancedFP, client, model)
}

// thinTail 精简尾部旧工具组，保留最近 keepGroups 组完整内容。
// 一个"工具组"= 一条 assistant(tool_calls) + 紧随其后的所有 tool result 消息。
// 对更早的组：截断 Content/Arguments，strip think blocks，保留消息结构不变（API 兼容）。
// activeFiles 中的活跃文件不会被截断（保护当前工作文件）。
func thinTail(tail []llm.ChatMessage, keepGroups int, activeFiles []ActiveFile) []llm.ChatMessage {
	const (
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
		if isActive {
			continue
		}
		for j := grp.start; j <= grp.end; j++ {
			msg := result[j] // copy struct
			switch msg.Role {
			case "assistant":
				msg.Content = llm.StripThinkBlocks(msg.Content)
				msg.Content = truncateRunes(msg.Content, thinContentMax)
				if len(msg.ToolCalls) > 0 {
					tcs := make([]llm.ToolCall, len(msg.ToolCalls))
					copy(tcs, msg.ToolCalls)
					for k := range tcs {
						tcs[k].Arguments = truncateRunes(tcs[k].Arguments, thinArgsMax)
					}
					msg.ToolCalls = tcs
				}
			case "tool":
				msg.Content = truncateRunes(msg.Content, thinContentMax)
				msg.ToolArguments = truncateRunes(msg.ToolArguments, thinArgsMax)
			}
			result[j] = msg
		}
	}

	return result
}

// aggressiveThinTail 激进版 thinTail，用于 normal thinTail 压缩不足时的回退。
// 与 thinTail 相同逻辑，但截断长度更短（100 vs 300），且对 assistant(tool_calls)
// 消息也完全清空 Content（只保留 tool_calls 结构）。
// activeFiles 中的活跃文件不会被截断（保护当前工作文件）。
func aggressiveThinTail(tail []llm.ChatMessage, keepGroups int, activeFiles []ActiveFile) []llm.ChatMessage {
	const (
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
		if isActive {
			continue
		}
		for j := grp.start; j <= grp.end; j++ {
			msg := result[j]
			switch msg.Role {
			case "assistant":
				msg.Content = ""
				if len(msg.ToolCalls) > 0 {
					tcs := make([]llm.ToolCall, len(msg.ToolCalls))
					copy(tcs, msg.ToolCalls)
					for k := range tcs {
						tcs[k].Arguments = truncateRunes(tcs[k].Arguments, thinArgsMax)
					}
					msg.ToolCalls = tcs
				}
			case "tool":
				msg.Content = truncateRunes(msg.Content, thinContentMax)
				msg.ToolArguments = truncateRunes(msg.ToolArguments, thinArgsMax)
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
// 逻辑与现有 compressContext() 完全一致，仅为消除 phase1Manager 对 *Agent 的引用。
// 现有 compressContext() 改为调用此函数：
//
//	func (a *Agent) compressContext(...) { return compressMessages(...) }
func compressMessages(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	return compressMessagesWithFingerprint(ctx, messages, KeyInfoFingerprint{}, client, model)
}

// compressMessagesWithFingerprint 使用 LLM 压缩对话历史，并注入指纹引导。
// 与 compressMessages 相同，但在 prompt 中追加关键信息指纹，引导 LLM 保留重要信息。
// 当 fp 为空时行为与 compressMessages 完全一致。
func compressMessagesWithFingerprint(ctx context.Context, messages []llm.ChatMessage, fp KeyInfoFingerprint, client llm.LLM, model string) (*CompressResult, error) {
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
		llmView := make([]llm.ChatMessage, 0, len(systemMsgs)+len(thinnedTail))
		llmView = append(llmView, systemMsgs...)
		llmView = append(llmView, thinnedTail...)

		tailSummary := extractDialogueFromTail(thinnedTail)
		return &CompressResult{
			LLMView:     llmView,
			SessionView: tailSummary,
		}, nil
	}

	// 第四步：构建压缩 prompt
	var historyText strings.Builder
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
		if len([]rune(content)) > 2000 {
			content = string([]rune(content)[:2000]) + "..."
		}
		fmt.Fprintf(&historyText, "[%s] %s\n\n", role, content)
	}

	// 注入指纹引导 LLM 保留关键信息
	var fpSection strings.Builder
	hasFP := len(fp.FilePaths) > 0 || len(fp.Identifiers) > 0 || len(fp.Errors) > 0 || len(fp.Decisions) > 0 || len(fp.ActiveFiles) > 0
	if hasFP {
		fpSection.WriteString("\n\n## CRITICAL: Must-Preserve Key Information\n")
		fpSection.WriteString("The following information MUST be retained in the compressed output using the structured markers:\n\n")

		if len(fp.FilePaths) > 0 {
			fpSection.WriteString("Files:\n")
			for _, p := range fp.FilePaths {
				fmt.Fprintf(&fpSection, "  @file:%s\n", p)
			}
		}
		if len(fp.Identifiers) > 0 {
			shown := len(fp.Identifiers)
			if shown > 50 {
				shown = 50
			}
			fpSection.WriteString("Identifiers:\n")
			for _, id := range fp.Identifiers[:shown] {
				fmt.Fprintf(&fpSection, "  @func:%s\n", id)
			}
			if len(fp.Identifiers) > 50 {
				fmt.Fprintf(&fpSection, "  ... and %d more identifiers\n", len(fp.Identifiers)-50)
			}
		}
		if len(fp.Errors) > 0 {
			fpSection.WriteString("Errors (MUST preserve ALL):\n")
			for _, e := range fp.Errors {
				fmt.Fprintf(&fpSection, "  @error:%s\n", truncateRunes(e, 150))
			}
		}
		if len(fp.Decisions) > 0 {
			fpSection.WriteString("Decisions:\n")
			for _, d := range fp.Decisions {
				fmt.Fprintf(&fpSection, "  @decision:%s\n", truncateRunes(d, 150))
			}
		}
		if len(fp.ActiveFiles) > 0 {
			fpSection.WriteString("\n## ACTIVE FILES (must be fully preserved in output):\n")
			for _, af := range fp.ActiveFiles {
				fmt.Fprintf(&fpSection, "  @file:%s\n", af.Path)
				for _, fn := range af.Functions {
					fmt.Fprintf(&fpSection, "  @func:%s\n", fn)
				}
			}
		}
	}

	compressionPrompt := taskStatePrompt + fpSection.String() + `

## Conversation History (to compress)
` + historyText.String() + `

Output the compressed content directly, preserving as much context as possible.`

	// 第五步：调用 LLM 压缩
	resp, err := client.Generate(ctx, model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a context compression expert."),
		llm.NewUserMessage(compressionPrompt),
	}, nil, "")
	if err != nil {
		return nil, fmt.Errorf("LLM compress failed: %w", err)
	}

	compressed := llm.StripThinkBlocks(resp.Content)

	// 第六步：构建压缩后的消息结构
	if len(systemMsgs) > 1 {
		panic("assert: at most one system message in compress input; got " + fmt.Sprint(len(systemMsgs)))
	}
	summaryMsg := llm.NewUserMessage("[Previous conversation context]\n\n" + compressed)

	// LLM View: system + 压缩摘要 + thinnedTail（含 tool 消息）
	llmView := make([]llm.ChatMessage, 0, len(systemMsgs)+1+len(thinnedTail))
	llmView = append(llmView, systemMsgs...)
	llmView = append(llmView, summaryMsg)
	llmView = append(llmView, thinnedTail...)

	// BUG FIX: 最低缩减保证。
	// 如果 LLM compress + thinTail 后缩减不到 20%，说明 tail 中的 tool 消息过大。
	// 此时做激进截断：将 thinnedTail 中的旧 tool 组进一步压缩到 100 字符。
	originalTokens, _ := llm.CountMessagesTokens(messages, model)
	resultTokens, _ := llm.CountMessagesTokens(llmView, model)
	minTarget := int(float64(originalTokens) * 0.8) // 至少缩减 20%
	if resultTokens > minTarget && minTarget > 0 {
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
		}).Info("Phase 2 compress: normal result insufficient, using aggressive thinning")
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
