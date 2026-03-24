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

// cleanQuotePrefix 清理飞书引用前缀 "> "（子 Agent 的 progressLines 以 "> " 开头），
// 避免在父 Agent 的引用块中产生嵌套引用。
func cleanQuotePrefix(s string) string {
	for strings.HasPrefix(s, "> ") {
		s = strings.TrimPrefix(s, "> ")
	}
	return strings.TrimSpace(s)
}

// flattenLines 将 Lines 展平为实际行（按 \n 分割）。
// 因为 notifyProgress 会将 progressLines join 成单个字符串作为 Lines[0]，
// 导致 Lines 的每个元素可能包含 \n，需要拆分后才能正确取到"最后一行"。
func flattenLines(lines []string) []string {
	var result []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		// 按 \n 拆分，每个子行单独处理
		result = append(result, strings.Split(line, "\n")...)
	}
	return result
}

// isSubAgentTreeLine 检查一行是否是子 Agent 的树状格式行（如 "├─ 🔄 ministry-works: ..."）。
// 这些行来自子 Agent 进度穿透，应被过滤掉，只显示当前 Agent 自身的状态。
func isSubAgentTreeLine(line string) bool {
	// 跳过引用前缀
	for strings.HasPrefix(line, "> ") {
		line = strings.TrimPrefix(line, "> ")
	}
	line = strings.TrimSpace(line)
	// 子 Agent 树状行的特征：以 ├─ 或 └─ 开头（全角或半角）
	return strings.HasPrefix(line, "├─") || strings.HasPrefix(line, "└─") ||
		strings.HasPrefix(line, "├─") || strings.HasPrefix(line, "└─") ||
		strings.HasPrefix(line, "│")
}

// extractOwnProgress 从展平后的行中提取当前 Agent 自身的进度（过滤掉子 Agent 穿透的行）。
// 规则：
//  1. 跳过以 "> " 开头的行（这些是飞书引用格式，属于子 Agent 的进度行）
//  2. 跳过包含树状格式字符（├─└─│）的行（子 Agent 的进度穿透）
//  3. 从剩余行中取最后一非空行作为当前 Agent 的状态
func extractOwnProgress(flat []string) string {
	var own []string
	for _, line := range flat {
		// 跳过引用前缀行（子 Agent 的进度行会带 > 前缀）
		if strings.HasPrefix(line, "> ") {
			continue
		}
		// 跳过子 Agent 树状行
		if isSubAgentTreeLine(line) {
			continue
		}
		cleaned := strings.TrimSpace(line)
		if cleaned != "" {
			own = append(own, cleaned)
		}
	}
	if len(own) == 0 {
		return ""
	}
	return own[len(own)-1]
}

// truncateProgress 截断进度文本到最大长度，超出部分用 "..." 省略。
func truncateProgress(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}

// formatSubAgentProgress 格式化 SubAgent 进度为单行文本。
// 每个 SubAgent 在父 Agent 的 progressLines 中只占一行，避免多行破坏飞书引用块格式。
// 对于嵌套 SubAgent 穿透上来的进度，只取当前 Agent 自身的最新状态（过滤子 Agent 的树状行）。
//
// 输出格式示例：
//
//	> ├─ 🔄 crown-prince: 💭 思考中...                    （单行：内容跟在角色名后）
//	> ├─ 🔄 crown-prince: ⏳ Shell(go test) ...           （工具执行）
//	> ├─ 🔄 crown-prince: 【奏报】调度三部执行...          （模型输出截断）
//	> ├─ ✅ crown-prince                                  （完成：简洁无内容）
//	> 　├─ 🔄 ministry-works: ⏳ Shell(ls) ...            （depth=1：全角空格缩进）
func formatSubAgentProgress(detail SubAgentProgressDetail) string {
	const maxContentLen = 80 // 进度内容最大字符数

	// 展平所有行
	flat := flattenLines(detail.Lines)

	// 提取当前 Agent 自身的进度（过滤子 Agent 穿透的行）
	lastLine := extractOwnProgress(flat)

	// 全角空格缩进（飞书不忽略全角空格）
	indent := strings.Repeat("　", detail.Depth)

	// 从 Path 提取当前子 Agent 角色名
	roleName := ""
	if len(detail.Path) > 0 {
		last := detail.Path[len(detail.Path)-1]
		if idx := strings.LastIndexByte(last, '/'); idx >= 0 {
			roleName = last[idx+1:]
		} else {
			roleName = last
		}
	}

	// 空内容表示子 Agent 完成
	if lastLine == "" {
		return fmt.Sprintf("> %s├─ ✅ %s", indent, roleName)
	}

	// 截断过长的内容
	lastLine = truncateProgress(lastLine, maxContentLen)

	return fmt.Sprintf("> %s├─ 🔄 %s: %s", indent, roleName, lastLine)
}
