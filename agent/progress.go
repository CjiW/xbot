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
	Line  string   // 进度内容（单行，已清理换行）
	Depth int      // 嵌套深度（0 = 直接子 Agent）
}

// formatSubAgentProgress 格式化 SubAgent 进度行，生成树形缩进的纯文本。
// detail.Line 可能包含飞书引用前缀 "> "（来自子 Agent 的 progressLines），
// 需要清理后再拼接，避免在飞书 markdown 中产生嵌套引用块。
//
// 输出格式示例：
//
//	> ├─ 🔄 crown-prince: 💭 思考中...
//	> ├─ ✅ ministry-works
//	> 　　├─ 🔄 ministry-works/audit: ⏳ Shell(ls) ...
func formatSubAgentProgress(detail SubAgentProgressDetail) string {
	line := detail.Line

	// 清理 detail.Line 中的飞书引用前缀 "> "（子 Agent 的 progressLines 以 "> " 开头），
	// 避免在父 Agent 的引用块中产生嵌套引用。
	for strings.HasPrefix(line, "> ") {
		line = strings.TrimPrefix(line, "> ")
	}
	line = strings.TrimSpace(line)

	// 全角空格缩进（飞书不忽略全角空格）
	indent := strings.Repeat("　", detail.Depth)

	connector := "├─"
	icon := "🔄"

	// 空行表示子 Agent 完成
	if line == "" {
		icon = "✅"
	}

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

	// 完成状态：只显示角色名
	if line == "" {
		return fmt.Sprintf("> %s%s %s %s", indent, connector, icon, roleName)
	}

	// 进行中状态：显示角色名 + 进度内容
	return fmt.Sprintf("> %s%s %s %s: %s", indent, connector, icon, roleName, line)
}
