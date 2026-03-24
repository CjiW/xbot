package agent

import (
	"fmt"
	"strings"
	"time"
)

// ProgressEvent 结构化进度事件，供上层消费（如飞书卡片渲染）。
type ProgressEvent struct {
	Lines      []string
	Structured *StructuredProgress
	Timestamp  time.Time
}

// FullText returns all progress lines joined into a single string.
// Consumers should use this instead of only accessing Lines[0].
func (e *ProgressEvent) FullText() string {
	if len(e.Lines) == 0 {
		return ""
	}
	return strings.Join(e.Lines, "\n")
}

// StructuredProgress 结构化进度信息，描述 Agent 当前状态。
type StructuredProgress struct {
	Phase           ProgressPhase
	Iteration       int
	ActiveTools     []ToolProgress
	CompletedTools  []ToolProgress
	ThinkingContent string
	TokenUsage      *TokenUsageSnapshot
}

// ProgressPhase Agent 运行阶段。
type ProgressPhase string

const (
	PhaseThinking    ProgressPhase = "thinking"
	PhaseToolExec    ProgressPhase = "tool_exec"
	PhaseCompressing ProgressPhase = "compressing"
	PhaseRetrying    ProgressPhase = "retrying"
	PhaseDone        ProgressPhase = "done"
)

// ToolProgress 单个工具的执行进度。
type ToolProgress struct {
	Name      string
	Label     string
	Status    ToolStatus
	Elapsed   time.Duration
	Iteration int
}

// ToolStatus 工具执行状态。
type ToolStatus string

const (
	ToolPending ToolStatus = "pending"
	ToolRunning ToolStatus = "running"
	ToolDone    ToolStatus = "done"
	ToolError   ToolStatus = "error"
)

// TokenUsageSnapshot Token 用量快照。
type TokenUsageSnapshot struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CacheHitTokens   int64
}

// SubAgentProgressDetail 携带层级信息的 SubAgent 进度回调参数。
// 用于递归 SubAgent 场景，让深层子 Agent 的进度能穿透到最顶层。
type SubAgentProgressDetail struct {
	Path  []string // 调用链: ["工部", "ministry-works/audit"]
	Lines []string // 进度内容（所有行，已清理换行）
	Depth int      // 嵌套深度（0 = 直接子 Agent）
}

// --- 辅助函数 ---

// flattenLines 将 Lines 展平为实际行（按 \n 分割）。
// 因为 notifyProgress 会将 progressLines join 成单个字符串作为 Lines[0]，
// 导致 Lines 的每个元素可能包含 \n，需要拆分后才能正确处理。
func flattenLines(lines []string) []string {
	var result []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		result = append(result, strings.Split(line, "\n")...)
	}
	return result
}

// progressTruncate 截断字符串到最大 rune 数，超出部分用 "…" 省略（紧凑版）。
func progressTruncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

// extractRoleName 从 Path 末尾提取角色名（去掉路径中的 / 部分）。
func extractRoleName(path []string) string {
	if len(path) == 0 {
		return ""
	}
	last := path[len(path)-1]
	if idx := strings.LastIndexByte(last, '/'); idx >= 0 {
		return last[idx+1:]
	}
	return last
}

// --- 子 Agent 行识别与解析 ---

// childAgentStatus 表示从子 Agent 行中解析出的状态。
type childAgentStatus struct {
	Role   string // 角色名
	Status string // "🔄" / "✅" / "❌" / "⏳"
	Desc   string // 简短描述
}

// isSubAgentLine 检查一行是否是子 Agent 的进度行。
// 支持两种格式：
//  1. 树状格式（测试用/穿透场景）：  "├─ 🔄 role: desc" / "└─ ✅ role:"
//  2. 引用格式（实际运行时子 Agent 穿透上来的格式化行）："> 🔄 role: desc" / "> 　✅ role"
func isSubAgentLine(line string) bool {
	// 清理引用前缀
	for strings.HasPrefix(line, "> ") {
		line = strings.TrimPrefix(line, "> ")
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}

	// 树状格式：├─ / └─ / │ 开头
	if strings.HasPrefix(line, "├─") || strings.HasPrefix(line, "└─") || strings.HasPrefix(line, "│") {
		return true
	}

	// 引用格式：以状态 emoji + 文本 + 冒号 开头
	line = strings.TrimLeft(line, "　 \t")
	return isStatusEmojiLine(line)
}

// isStatusEmojiLine 检查行是否以状态 emoji 开头并包含冒号（子 Agent 格式化输出的特征）。
func isStatusEmojiLine(line string) bool {
	for _, prefix := range []string{"🔄 ", "✅ ", "❌ ", "⏳ "} {
		if strings.HasPrefix(line, prefix) {
			if idx := strings.Index(line, ":"); idx > 0 {
				return true
			}
		}
	}
	return false
}

// parseSubAgentLine 解析子 Agent 进度行，提取角色名和状态。
// 支持两种输入格式：
//  1. 树状格式: "├─ 🔄 ministry-works: ⏳ Shell(ls) ..."
//  2. 引用格式: "🔄 ministry-works: ⏳ Shell(ls) ..." 或 "　🔄 ministry-works: ⏳ Shell(ls)"
func parseSubAgentLine(line string) (childAgentStatus, bool) {
	// 清理引用前缀
	for strings.HasPrefix(line, "> ") {
		line = strings.TrimPrefix(line, "> ")
	}

	// 清理树状线和全角缩进
	line = strings.TrimLeft(line, "　 \t│├└─")
	line = strings.TrimSpace(line)
	if line == "" {
		return childAgentStatus{}, false
	}

	// 提取 emoji 状态前缀
	status := "🔄"
	for _, s := range []string{"✅", "❌", "🔄"} {
		if strings.HasPrefix(line, s) {
			status = s
			line = strings.TrimPrefix(line, s)
			break
		}
	}
	line = strings.TrimSpace(line)

	// 提取角色名（第一个冒号之前的部分）
	colonIdx := strings.Index(line, ":")
	if colonIdx <= 0 {
		return childAgentStatus{}, false
	}

	role := strings.TrimSpace(line[:colonIdx])
	desc := strings.TrimSpace(line[colonIdx+1:])

	if role == "" {
		return childAgentStatus{}, false
	}

	return childAgentStatus{Role: role, Status: status, Desc: desc}, true
}

// formatChildAgentsSummary 将多个子 Agent 状态格式化为紧凑的单行摘要。
// 目标：清晰展示有几个 Agent、各自状态、并发关系。
//
// 输出示例：
//
//	"🔄 工部(⏳ go version) · ✅ 刑部 · 🔄 礼部(💭)"
//	"✅ 工部 · ✅ 刑部 · ✅ 礼部"
//	"🔄×3 ⏳×2"  （超过 6 个时只显示统计）
func formatChildAgentsSummary(children []childAgentStatus, maxTotalRunes int) string {
	if len(children) == 0 {
		return ""
	}

	const (
		sep        = " · "
		descMax    = 20 // 每个 Agent 描述最大长度
		totalLimit = 6  // 超过这个数量只显示状态统计
	)

	if len(children) > totalLimit {
		// 太多了，只统计状态
		running, done, failed, pending := 0, 0, 0, 0
		for _, c := range children {
			switch c.Status {
			case "✅":
				done++
			case "❌":
				failed++
			case "⏳":
				pending++
			default:
				running++
			}
		}
		parts := []string{}
		if running > 0 {
			parts = append(parts, fmt.Sprintf("🔄×%d", running))
		}
		if pending > 0 {
			parts = append(parts, fmt.Sprintf("⏳×%d", pending))
		}
		if done > 0 {
			parts = append(parts, fmt.Sprintf("✅×%d", done))
		}
		if failed > 0 {
			parts = append(parts, fmt.Sprintf("❌×%d", failed))
		}
		return strings.Join(parts, sep)
	}

	var parts []string
	for _, c := range children {
		if c.Desc != "" {
			shortDesc := progressTruncate(c.Desc, descMax)
			parts = append(parts, fmt.Sprintf("%s %s(%s)", c.Status, c.Role, shortDesc))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s", c.Status, c.Role))
		}
	}

	result := strings.Join(parts, sep)
	return progressTruncate(result, maxTotalRunes)
}

// extractOwnAndChildProgress 从展平后的行中分离当前 Agent 自身进度和子 Agent 进度。
// 返回 (ownLastLine, childStatuses)。
//
// 分离规则：
//   - "> " 前缀 + 状态 emoji + 冒号 → 子 Agent 穿透的格式化输出（解析为 childAgentStatus）
//   - ├─ / └─ 树状行 → 子 Agent 穿透的树状行（解析为 childAgentStatus）
//   - "> " 前缀但不是子 Agent 格式 → 更深层的穿透行（过滤掉）
//   - 其他非空前缀行 → 当前 Agent 自身进度
func extractOwnAndChildProgress(flat []string) (string, []childAgentStatus) {
	var ownLines []string
	var children []childAgentStatus

	for _, line := range flat {
		if isSubAgentLine(line) {
			if child, ok := parseSubAgentLine(line); ok {
				children = append(children, child)
			}
			continue
		}
		if strings.HasPrefix(line, "> ") {
			// 引用前缀行但不是子 Agent 格式 → 更深层穿透，过滤掉
			continue
		}
		cleaned := strings.TrimSpace(line)
		if cleaned != "" {
			ownLines = append(ownLines, cleaned)
		}
	}

	ownLast := ""
	if len(ownLines) > 0 {
		ownLast = ownLines[len(ownLines)-1]
	}

	return ownLast, children
}

// --- 主格式化函数 ---

// formatSubAgentProgress 格式化 SubAgent 进度为单行文本。
// 每个 SubAgent 在父 Agent 的 progressLines 中只占一个槽（一行），
// 但这一行会优雅地展示它自身状态及其并发子 Agent 的状态摘要。
//
// 设计目标：
//   - 用户能清楚看明白：几个 Agent、嵌套几层、在干什么、哪些并发
//   - 输出始终是单行（不破坏飞书引用块格式）
//   - 以 "> " 开头（飞书引用块格式）
//
// 输出格式示例：
//
//	> 🔄 crown-prince: 💭 思考中...                                     （自身状态）
//	> 🔄 crown-prince: ⏳ Shell(go test) ...                            （工具执行）
//	> 🔄 crown-prince: → 🔄 工部(⏳ ls) · ✅ 刑部 · 🔄 礼部(💭)         （子Agent并发摘要）
//	> 　🔄 department-state: 分派三部 → ✅ 工部 · ✅ 刑部 · ✅ 礼部       （带缩进）
//	> ✅ crown-prince                                                      （完成）
func formatSubAgentProgress(detail SubAgentProgressDetail) string {
	const (
		maxContentRunes = 50  // 自身进度内容最大字符数
		maxChildRunes   = 80  // 子Agent摘要最大字符数
		maxTotalRunes   = 120 // 单行总最大字符数（不含前缀）
	)

	// 展平所有行
	flat := flattenLines(detail.Lines)

	// 分离自身进度和子 Agent 进度
	ownLine, children := extractOwnAndChildProgress(flat)

	// 提取角色名
	roleName := extractRoleName(detail.Path)

	// 全角空格缩进（飞书不忽略全角空格）
	indent := strings.Repeat("　", detail.Depth)

	// 1. 完成状态：无内容也无子 Agent
	if ownLine == "" && len(children) == 0 {
		return fmt.Sprintf("> %s✅ %s", indent, roleName)
	}

	// 2. 只有子 Agent 进度（当前 Agent 没有自身输出，但子 Agent 有状态）
	if ownLine == "" && len(children) > 0 {
		summary := formatChildAgentsSummary(children, maxChildRunes)
		return fmt.Sprintf("> %s🔄 %s: %s", indent, roleName, summary)
	}

	// 3. 只有自身进度（叶子节点）
	if len(children) == 0 {
		ownLine = progressTruncate(ownLine, maxContentRunes)
		return fmt.Sprintf("> %s🔄 %s: %s", indent, roleName, ownLine)
	}

	// 4. 自身进度 + 子 Agent 摘要
	summary := formatChildAgentsSummary(children, maxChildRunes)
	// 截断自身内容，为子 Agent 摘要留空间
	availableForOwn := maxContentRunes
	if len([]rune(summary)) > 30 {
		availableForOwn = maxContentRunes - 15
		if availableForOwn < 15 {
			availableForOwn = 15
		}
	}
	ownLine = progressTruncate(ownLine, availableForOwn)

	line := fmt.Sprintf("%s → %s", ownLine, summary)
	line = progressTruncate(line, maxTotalRunes)
	return fmt.Sprintf("> %s🔄 %s: %s", indent, roleName, line)
}
