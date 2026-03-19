package agent

import (
	"context"
	"fmt"
	"sort"
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

// structuredCompressionPrompt 结构化压缩提示词，引导 LLM 使用标记化输出。
const structuredCompressionPrompt = `You are a context compression expert. Your task is to compress the conversation history into a concise summary while retaining ALL important information.

## Compression Rules
1. Retain ALL key facts, decisions, and important details
2. Keep track of what the user has asked for and what has been done
3. Preserve any file paths, code snippets, or technical details
4. Maintain the logical flow and context of the conversation
5. CRITICAL: MUST preserve ALL @error: items — errors are essential for debugging continuity
6. If the conversation contains 📂 [offload:...] markers, preserve the marker text as-is (it references disk-stored data)
7. If the conversation involves multiple topics, use ## Topic: headers to separate them

## OUTPUT FORMAT
Use these structured markers to tag key information:
@file:{path} — File references
@func:{name} — Function signatures
@type:{name} — Type definitions
@error:{description} — Errors encountered (MUST preserve ALL of these)
@decision:{description} — Decisions made
@todo:{description} — Pending tasks
@config:{key=value} — Config changes

## Topic Separation
When the conversation covers multiple distinct topics, use section headers:
## Topic: {topic name}
...content for this topic...

## Important
- This is NOT a summary — it's a compressed version that preserves context
- Include specific details like file names, function names, variable names
- Note what tools were used and their results if relevant
- Never drop error information or pending decisions`

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

// ----------------------------------------------------------------
// Information density eviction (Phase 2)
// ----------------------------------------------------------------

// DensityScore 信息密度评分结果
type DensityScore struct {
	Score float64
	Index int // 在 messages 切片中的索引
}

// defaultDensityScorer 默认信息密度评分。
// 分数越高 = 越重要，越不应被驱逐。
func defaultDensityScorer(msg llm.ChatMessage) float64 {
	score := 0.0
	content := msg.Content

	// Offload 标记消息：已是摘要，不再驱逐（保留引用标记供 offload_recall 使用）
	if strings.HasPrefix(content, "📂 [offload:") {
		return 1.0 // 中性分数，不会被优先驱逐
	}

	// 高密度信号（重要信息）
	if containsErrorPattern(content) {
		score += 3.0
	}
	if containsDecisionPattern(content) {
		score += 2.5
	}
	if len(extractFilePaths(content)) > 0 {
		score += 1.0
	}
	if len([]rune(content)) < 500 {
		score += 1.5 // 短消息信息密度高
	}

	// Low-density signal penalties
	if isLargeCodeDump(content) {
		score -= 3.0 // raised from -2.0: large code dump is the strongest eviction signal
	}
	if isRepetitiveGrepResult(content) {
		score -= 1.5
	}
	if msg.Role == "tool" && len([]rune(content)) > 3000 {
		score -= 2.5 // raised from -2.0: large content without value signals = prime eviction target
	}
	if msg.Role == "tool" && len([]rune(content)) > 1000 && !containsErrorPattern(content) && !containsDecisionPattern(content) {
		score -= 1.0 // medium-length content without value signals
	}

	return score
}

// containsErrorPattern 检测 error/panic/failed 等错误关键词。
func containsErrorPattern(text string) bool {
	lower := strings.ToLower(text)
	patterns := []string{"error", "panic", "failed", "failure", "fatal", "exception"}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// containsDecisionPattern 检测 decided to/chose/will use 等决策关键词。
func containsDecisionPattern(text string) bool {
	lower := strings.ToLower(text)
	patterns := []string{"decided to", "chose to", "will use", "going to use", "agreed to", "plan to"}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// isLargeCodeDump 检测大代码块（超过 2000 字符且含 3 个以上连续的函数定义/代码行）。
func isLargeCodeDump(text string) bool {
	if len([]rune(text)) <= 2000 {
		return false
	}
	// 统计函数定义模式
	funcPatterns := []string{"func ", "function ", "def ", "fn "}
	count := 0
	for _, p := range funcPatterns {
		count += strings.Count(text, p)
	}
	return count >= 3
}

// isRepetitiveGrepResult 检测重复性 grep 结果（很多匹配行但信息密度低）。
func isRepetitiveGrepResult(text string) bool {
	lines := strings.Split(text, "\n")
	if len(lines) < 15 {
		return false
	}
	// grep 结果格式：file:line: content
	matchCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 3)
		if len(parts) >= 3 {
			matchCount++
		}
	}
	// 超过 15 行匹配且占比 > 60% 视为重复 grep
	totalNonEmpty := 0
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			totalNonEmpty++
		}
	}
	if totalNonEmpty == 0 {
		return false
	}
	return matchCount >= 15 && float64(matchCount)/float64(totalNonEmpty) > 0.6
}

// evictToolGroup 一个工具组的边界 [start, end]。
type evictToolGroup struct {
	start, end int
}

// evictByDensity 按信息密度驱逐旧 tool result。
// 保留尾部 keepGroups 组完整，对剩余 tool 消息按密度评分从低到高驱逐。
// 仅替换 tool result 的 Content，保留 assistant(tool_calls) 消息结构（API 兼容）。
func evictByDensity(messages []llm.ChatMessage, keepGroups int, targetTokens int, model string) []llm.ChatMessage {
	if keepGroups < 0 {
		keepGroups = 0
	}

	// 1. 识别工具组（复用 thinTail 的组识别逻辑）
	var groups []evictToolGroup
	for i := range messages {
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			g := evictToolGroup{start: i, end: i}
			for j := i + 1; j < len(messages) && messages[j].Role == "tool"; j++ {
				g.end = j
			}
			groups = append(groups, g)
		}
	}

	// 2. 保留尾部 keepGroups 组完整
	totalGroups := len(groups)
	if totalGroups <= keepGroups {
		return messages
	}
	thinCount := totalGroups - keepGroups

	// 3. 对可驱逐组的 tool 消息按密度评分排序（从低到高）
	type evictableTool struct {
		score    float64
		msgIndex int
	}
	var candidates []evictableTool
	for g := 0; g < thinCount; g++ {
		grp := groups[g]
		for j := grp.start; j <= grp.end; j++ {
			if messages[j].Role == "tool" {
				// 跳过 offload 标记消息
				if strings.HasPrefix(messages[j].Content, "📂 [offload:") {
					continue
				}
				candidates = append(candidates, evictableTool{
					score:    defaultDensityScorer(messages[j]),
					msgIndex: j,
				})
			}
		}
	}

	// 按分数从低到高排序（低分 = 先驱逐）
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].score < candidates[j].score
	})

	// 4. 从低密度开始驱逐，直到 targetTokens 以下或无可驱逐的消息
	// 先做浅拷贝
	result := make([]llm.ChatMessage, len(messages))
	copy(result, messages)

	currentTokens, _ := llm.CountMessagesTokens(result, model)
	for _, cand := range candidates {
		if currentTokens <= targetTokens {
			break
		}
		msg := result[cand.msgIndex]
		// 计算驱逐此消息可节省的 tokens
		msgTokens, _ := llm.CountMessagesTokens([]llm.ChatMessage{msg}, model)
		if msgTokens <= 0 {
			continue
		}
		// 替换 tool result 的 Content
		argsPreview := truncateRunes(msg.ToolArguments, 80)
		evictedContent := fmt.Sprintf("[evicted] %s(%s) — %d tokens evicted", msg.ToolName, argsPreview, msgTokens)
		msg.Content = evictedContent
		result[cand.msgIndex] = msg
		currentTokens -= msgTokens
		// 加上 eviction marker 的 token 数（远小于原内容）
		markerTokens, _ := llm.CountMessagesTokens([]llm.ChatMessage{msg}, model)
		currentTokens += markerTokens
	}

	return result
}

// buildCompressResultFromEvicted 从 evict 后的消息构建 CompressResult。
// 类似 compressMessages 的构建逻辑：分离 system/tail/compressed。
func buildCompressResultFromEvicted(messages []llm.ChatMessage, ctx context.Context, model string) (*CompressResult, error) {
	// 1. 找 tailStart（同 compressMessages 逻辑）
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

	// 2. 分离 system 消息和 tail
	var systemMsgs []llm.ChatMessage
	for i, msg := range messages {
		if i >= tailStart {
			break
		}
		if msg.Role == "system" {
			systemMsgs = append(systemMsgs, msg)
		}
	}
	// 3. LLMView = system + non-system evicted messages（全部）
	//    BUG FIX: 原代码直接 append(messages...) 导致 system 消息重复
	//    （messages 本身包含 system 消息，已单独提取到 systemMsgs 中）
	llmView := make([]llm.ChatMessage, 0, len(messages))
	llmView = append(llmView, systemMsgs...)
	for _, msg := range messages {
		if msg.Role != "system" {
			llmView = append(llmView, msg)
		}
	}

	// 4. SessionView = extractDialogueFromTail(tail part)
	var tailPart []llm.ChatMessage
	if tailStart < len(messages) {
		tailPart = messages[tailStart:]
	}
	tailSummary := extractDialogueFromTail(tailPart)

	// 5. 如果有非 tail 非 system 的消息，将 evicted 后的前半部分作为压缩摘要
	var sessionView []llm.ChatMessage
	if tailStart > 0 {
		// 从 evicted 的前半部分提取对话摘要
		evictedPart := make([]llm.ChatMessage, 0)
		for i, msg := range messages {
			if i >= tailStart {
				break
			}
			if msg.Role != "system" {
				evictedPart = append(evictedPart, msg)
			}
		}
		if len(evictedPart) > 0 {
			evictedSummary := extractDialogueFromTail(evictedPart)
			sessionView = append(sessionView, evictedSummary...)
		}
	}
	sessionView = append(sessionView, tailSummary...)

	return &CompressResult{
		LLMView:     llmView,
		SessionView: sessionView,
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
func thinTail(tail []llm.ChatMessage, keepGroups int) []llm.ChatMessage {
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

	result := make([]llm.ChatMessage, len(tail))
	copy(result, tail)

	for g := range thinCount {
		grp := groups[g]
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

// truncateRunes 截断到 maxLen 个 rune（多字节安全）。
func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "...[truncated]"
}

// countEvictedMessages 统计被 evictByDensity 驱逐的消息数量。
// 通过比较驱逐前后每条 tool 消息的 Content 是否以 "[evicted]" 开头来判断。
func countEvictedMessages(original, evicted []llm.ChatMessage) int {
	count := 0
	for i := range evicted {
		if i >= len(original) {
			break
		}
		if evicted[i].Role == "tool" && original[i].Role == "tool" {
			if strings.HasPrefix(evicted[i].Content, "[evicted]") && !strings.HasPrefix(original[i].Content, "[evicted]") {
				count++
			}
		}
	}
	return count
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

	// 第二步：精简尾部旧工具组（保留最近 3 组完整，截断更早的组）
	var thinnedTail []llm.ChatMessage
	if tailStart < len(messages) {
		thinnedTail = thinTail(messages[tailStart:], 3)
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
	hasFP := len(fp.FilePaths) > 0 || len(fp.Identifiers) > 0 || len(fp.Errors) > 0 || len(fp.Decisions) > 0
	if hasFP {
		fpSection.WriteString("\n\n## CRITICAL: Must-Preserve Key Information\n")
		fpSection.WriteString("The following information MUST be retained in the compressed output using the structured markers:\n\n")

		if len(fp.FilePaths) > 0 {
			fpSection.WriteString("Files:\n")
			for _, p := range fp.FilePaths {
				fpSection.WriteString(fmt.Sprintf("  @file:%s\n", p))
			}
		}
		if len(fp.Identifiers) > 0 {
			shown := len(fp.Identifiers)
			if shown > 50 {
				shown = 50
			}
			fpSection.WriteString("Identifiers:\n")
			for _, id := range fp.Identifiers[:shown] {
				fpSection.WriteString(fmt.Sprintf("  @func:%s\n", id))
			}
			if len(fp.Identifiers) > 50 {
				fpSection.WriteString(fmt.Sprintf("  ... and %d more identifiers\n", len(fp.Identifiers)-50))
			}
		}
		if len(fp.Errors) > 0 {
			fpSection.WriteString("Errors (MUST preserve ALL):\n")
			for _, e := range fp.Errors {
				fpSection.WriteString(fmt.Sprintf("  @error:%s\n", truncateRunes(e, 150)))
			}
		}
		if len(fp.Decisions) > 0 {
			fpSection.WriteString("Decisions:\n")
			for _, d := range fp.Decisions {
				fpSection.WriteString(fmt.Sprintf("  @decision:%s\n", truncateRunes(d, 150)))
			}
		}
	}

	compressionPrompt := structuredCompressionPrompt + fpSection.String() + `

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

	// Session View: 压缩摘要 + 尾部对话摘要（纯 user/assistant）
	tailSummary := extractDialogueFromTail(thinnedTail)
	sessionView := make([]llm.ChatMessage, 0, 1+len(tailSummary))
	sessionView = append(sessionView, summaryMsg)
	sessionView = append(sessionView, tailSummary...)

	return &CompressResult{
		LLMView:     llmView,
		SessionView: sessionView,
	}, nil
}
