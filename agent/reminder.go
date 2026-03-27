package agent

import (
	"fmt"
	"strings"
	"xbot/llm"
)

// ReminderContext 提供构建系统提醒所需的上下文信息（token 预算等）。
type ReminderContext struct {
	// MaxContextTokens 最大上下文 token 数（0 = 不显示 context_edit 提示）
	MaxContextTokens int
	// UsedTokens 当前消息已用 token 数
	UsedTokens int
	// ToolDefTokens 工具定义占用的 token 数
	ToolDefTokens int
}

// BuildSystemReminder 构建系统提醒，追加到 tool message content 末尾。
// agentID 为 "main" 时是主 Agent，否则为 SubAgent。
// rc 为可选的上下文信息（nil 或 MaxContextTokens==0 时不显示 context_edit 提示）。
func BuildSystemReminder(messages []llm.ChatMessage, roundToolNames []string, todoSummary string, agentID string, rc *ReminderContext) string {
	if len(messages) == 0 {
		return ""
	}

	isSubAgent := agentID != "main"

	// 1. 提取任务目标：第一条 user message（去掉时间戳和引导文本）
	//   - 主 Agent：用户原始需求
	//   - SubAgent：父 Agent 分配的任务命令
	var taskGoal string
	for _, msg := range messages {
		if msg.Role == "user" && msg.Content != "" {
			taskGoal = extractUserGoal(msg.Content)
			break
		}
	}

	// 2. 统计 tool message 总数作为进度指标
	toolCount := 0
	for _, msg := range messages {
		if msg.Role == "tool" {
			toolCount++
		}
	}

	// 3. 构建提醒
	var parts []string

	if taskGoal != "" {
		if isSubAgent {
			parts = append(parts, fmt.Sprintf("执行任务: %s", taskGoal))
		} else {
			parts = append(parts, fmt.Sprintf("用户原始需求: %s", taskGoal))
		}
	}

	parts = append(parts, fmt.Sprintf("已完成 %d 次工具调用", toolCount))
	parts = append(parts, fmt.Sprintf("本轮使用: %s", strings.Join(roundToolNames, ", ")))

	if todoSummary != "" {
		parts = append(parts, fmt.Sprintf("TODO: %s", todoSummary))
	}

	parts = append(parts, "行为提醒:")
	parts = append(parts, "- 优先编辑已有文件，避免创建新文件")
	parts = append(parts, "- 修改后运行测试验证")
	parts = append(parts, "- 错误时先分析根因再修改")

	// 4. 双阶段 context_edit 提示
	// 根据上下文 token 使用率，引导 LLM 主动清理不再需要的中间过程。
	// 阶段 1（弱提示）：40%-60% → 温和建议
	// 阶段 2（警告）：>60% → 强烈建议，列出具体清理目标
	if rc != nil && rc.MaxContextTokens > 0 {
		totalTokens := rc.UsedTokens + rc.ToolDefTokens
		if totalTokens > 0 && rc.MaxContextTokens > 0 {
			ratio := float64(totalTokens) / float64(rc.MaxContextTokens)
			if ratio >= 0.4 {
				pct := int(ratio * 100)
				if ratio >= 0.6 {
					// 阶段 2：强烈警告
					parts = append(parts, "⚠️ 上下文压力警告：请立即清理不再需要的中间数据")
					parts = append(parts, fmt.Sprintf("- 当前上下文已占用 %d%%（%d / %d tokens）", pct, totalTokens, rc.MaxContextTokens))
					parts = append(parts, "- 用 context_edit action=list 检查消息，对已完成的旧步骤用 delete 删除")
					parts = append(parts, "- 对大段已读代码用 truncate 保留摘要即可，详细内容可通过 offload_recall 恢复")
				} else {
					// 阶段 1：温和建议
					parts = append(parts, fmt.Sprintf("- 上下文已占用 %d%%，如已完成某些步骤，可用 context_edit 清理中间过程", pct))
				}
			}
		}
	}

	return "<system-reminder>\n" + strings.Join(parts, "\n") + "\n</system-reminder>"
}

// stripSystemReminder removes the <system-reminder>...</system-reminder> block
// and any preceding blank line from a message's content.
func stripSystemReminder(content string) string {
	start := strings.Index(content, "\n\n<system-reminder>")
	if start == -1 {
		start = strings.Index(content, "<system-reminder>")
		if start == -1 {
			return content
		}
		return content[:start] + content[start+len("<system-reminder>"):]
	}
	end := strings.Index(content[start:], "</system-reminder>")
	if end == -1 {
		return content[:start]
	}
	return content[:start] + content[start+end+len("</system-reminder>"):]
}

// extractUserGoal 从 user message 中提取实际用户需求（去掉时间戳和系统引导文本）。
func extractUserGoal(content string) string {
	lines := strings.Split(content, "\n")
	var goalLines []string
	inGuide := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// 跳过时间戳行 [2026-03-21 23:08:51 CST]
		if len(trimmed) > 0 && trimmed[0] == '[' && strings.Contains(trimmed, "CST") {
			continue
		}
		// 跳过 [用户名] 标记行
		if len(trimmed) > 0 && trimmed[0] == '[' && strings.HasSuffix(trimmed, "]") && len(trimmed) < 50 {
			continue
		}
		// 跳过系统引导文本块
		if strings.Contains(trimmed, "[系统引导]") || strings.Contains(trimmed, "search_tools") || strings.Contains(trimmed, "WebSearch") || strings.Contains(trimmed, "Fetch") || strings.Contains(trimmed, "Skill") || strings.Contains(trimmed, "现在时间") {
			inGuide = true
			continue
		}
		if inGuide && trimmed == "" {
			inGuide = false
			continue
		}
		if inGuide {
			continue
		}
		goalLines = append(goalLines, line)
	}
	goal := strings.TrimSpace(strings.Join(goalLines, "\n"))
	runes := []rune(goal)
	if len(runes) > 500 {
		goal = string(runes[:500]) + "..."
	}
	return goal
}
