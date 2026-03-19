package agent

import "time"

// ProgressEvent 结构化进度事件，供上层消费（如飞书卡片渲染）。
type ProgressEvent struct {
	Lines      []string
	Structured *StructuredProgress
	Timestamp  time.Time
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
