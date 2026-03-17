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
	threshold := int(float64(a.maxContextTokens) * a.compressionThreshold)
	if err == nil && tokenCount < threshold {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("当前上下文 token 数 (%d) 未达到压缩阈值 (%d)，无需压缩。", tokenCount, threshold),
		}, nil
	}

	// 发送压缩开始进度
	_ = a.sendMessage(msg.Channel, msg.ChatID, "🔄 开始压缩上下文...")

	// 执行压缩
	compressed, err := a.compressContext(ctx, messages, llmClient, model)
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
		newTokenCount, _ := llm.CountMessagesTokens(compressed, model)
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩完成 (内存): %d → %d tokens (%d 条消息)", tokenCount, newTokenCount, len(compressed)),
		}, nil
	}
	allOk := true
	systemSkipped := 0
	for _, msg := range compressed {
		if msg.Role == "system" {
			systemSkipped++
			continue
		}
		assertNoSystemPersist(msg)
		if err := tenantSession.AddMessage(msg); err != nil {
			log.Ctx(ctx).WithError(err).Error("Partial write during compression, session may be corrupted")
			allOk = false
			break
		}
	}

	newTokenCount, _ := llm.CountMessagesTokens(compressed, model)
	if allOk {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: fmt.Sprintf("上下文压缩完成: %d → %d tokens (%d 条消息)", tokenCount, newTokenCount, len(compressed)),
		}, nil
	}
	// 部分写入失败，只返回内存结果
	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: fmt.Sprintf("上下文压缩完成 (内存): %d → %d tokens (%d 条消息)", tokenCount, newTokenCount, len(compressed)),
	}, nil
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

func truncateRunes(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "...[truncated]"
}

// compressContext 使用 LLM 压缩对话历史（Claude 风格）
// 核心原则：
// 1. 保留所有 tool 消息（tool_calls 和 tool result 必须配对，否则 API 报错）
// 2. 把压缩后的摘要作为 user prompt 直接调用 LLM
// 3. 保留 system 消息和最近的对话轮次
func (a *Agent) compressContext(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) ([]llm.ChatMessage, error) {
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
		var result []llm.ChatMessage
		result = append(result, systemMsgs...)
		result = append(result, thinnedTail...)
		return result, nil
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
		if len([]rune(content)) > 800 {
			content = string([]rune(content)[:800]) + "..."
		}
		fmt.Fprintf(&historyText, "[%s] %s\n\n", role, content)
	}

	compressionPrompt := `You are a context compression expert. Your task is to compress the conversation history into a concise summary while retaining ALL important information.

## Compression Rules
1. Retain ALL key facts, decisions, and important details
2. Keep track of what the user has asked for and what has been done
3. Preserve any file paths, code snippets, or technical details
4. Maintain the logical flow and context of the conversation
5. Note any errors or issues that were encountered

## Important
- This is NOT a summary - it's a compressed version that preserves context
- Include specific details like file names, function names, variable names
- Note what tools were used and their results if relevant

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
	var result []llm.ChatMessage

	result = append(result, systemMsgs...)
	result = append(result, llm.NewUserMessage("[Previous conversation context]\n\n"+compressed))
	result = append(result, thinnedTail...)

	return result, nil
}
