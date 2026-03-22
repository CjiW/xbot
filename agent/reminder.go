package agent

import (
	"fmt"
	"strings"
	"xbot/llm"
)

// BuildSystemReminder 构建系统提醒，追加到 tool message content 末尾。
// agentID 为 "main" 时是主 Agent，否则为 SubAgent。
func BuildSystemReminder(messages []llm.ChatMessage, roundToolNames []string, todoSummary string, agentID string) string {
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

	return "<system-reminder>\n" + strings.Join(parts, "\n") + "\n</system-reminder>"
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
