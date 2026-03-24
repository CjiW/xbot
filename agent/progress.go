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

// formatSubAgentProgress 格式化 SubAgent 进度，生成树形缩进的多行文本。
// detail.Lines 中的每一行都会带树形连接符缩进显示。
//
// 输出格式示例（单行）：
//
//	> ├─ 🔄 crown-prince: 💭 思考中...
//
// 输出格式示例（多行）：
//
//	> ├─ 🔄 crown-prince:
//	> │  💭 思考中...
//	> │  ⏳ Shell(ls) ...
//	> │  ⏳ Shell(go test) ...
//
// 输出格式示例（嵌套 depth=1）：
//
//	> 　├─ 🔄 ministry-works:
//	> 　│  💭 思考中...
//	> 　│  ├─ ✅ sub-task-1
func formatSubAgentProgress(detail SubAgentProgressDetail) string {
	// 清理每行中的引用前缀
	var cleaned []string
	for _, l := range detail.Lines {
		l = cleanQuotePrefix(l)
		cleaned = append(cleaned, l)
	}

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

	// 确定状态图标
	icon := "🔄"
	if len(cleaned) == 0 || (len(cleaned) == 1 && cleaned[0] == "") {
		// 空行表示子 Agent 完成
		return fmt.Sprintf("> %s├─ ✅ %s", indent, roleName)
	}

	var buf strings.Builder
	for i, line := range cleaned {
		if i == 0 {
			// 第一行：角色头
			if len(cleaned) == 1 {
				// 单行：内容直接跟在角色名后面
				fmt.Fprintf(&buf, "> %s├─ %s %s: %s", indent, icon, roleName, line)
			} else {
				// 多行：角色名单独一行，内容也输出
				fmt.Fprintf(&buf, "> %s├─ %s %s:", indent, icon, roleName)
				if line != "" {
					fmt.Fprintf(&buf, "\n> %s│  %s", indent, line)
				}
			}
		} else {
			// 后续行：用 │ 连接线缩进
			fmt.Fprintf(&buf, "\n> %s│  %s", indent, line)
		}
	}
	return buf.String()
}
