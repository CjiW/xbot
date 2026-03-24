package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProgressEventCreation(t *testing.T) {
	now := time.Now()
	event := &ProgressEvent{
		Lines: []string{"> thinking...", "> ⏳ Read(file.go) ..."},
		Structured: &StructuredProgress{
			Phase:     PhaseThinking,
			Iteration: 0,
			ActiveTools: []ToolProgress{
				{Name: "Read", Label: "Read(file.go)", Status: ToolRunning, Iteration: 0},
			},
			CompletedTools: nil,
			TokenUsage: &TokenUsageSnapshot{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
				CacheHitTokens:   200,
			},
		},
		Timestamp: now,
	}

	if event.Structured.Phase != PhaseThinking {
		t.Errorf("expected phase %q, got %q", PhaseThinking, event.Structured.Phase)
	}
	if len(event.Structured.ActiveTools) != 1 {
		t.Fatalf("expected 1 active tool, got %d", len(event.Structured.ActiveTools))
	}
	if event.Structured.ActiveTools[0].Name != "Read" {
		t.Errorf("expected tool name 'Read', got %q", event.Structured.ActiveTools[0].Name)
	}
	if event.Structured.TokenUsage.TotalTokens != 1500 {
		t.Errorf("expected total tokens 1500, got %d", event.Structured.TokenUsage.TotalTokens)
	}
	if !event.Timestamp.Equal(now) {
		t.Error("timestamp mismatch")
	}
}

func TestStructuredProgress_Phases(t *testing.T) {
	sp := &StructuredProgress{
		Phase:     PhaseThinking,
		Iteration: 1,
	}

	// Transition through phases
	sp.Phase = PhaseToolExec
	if sp.Phase != "tool_exec" {
		t.Errorf("expected 'tool_exec', got %q", sp.Phase)
	}

	sp.Phase = PhaseCompressing
	if sp.Phase != "compressing" {
		t.Errorf("expected 'compressing', got %q", sp.Phase)
	}

	sp.Phase = PhaseRetrying
	if sp.Phase != "retrying" {
		t.Errorf("expected 'retrying', got %q", sp.Phase)
	}

	sp.Phase = PhaseDone
	if sp.Phase != "done" {
		t.Errorf("expected 'done', got %q", sp.Phase)
	}
}

func TestToolProgress_StatusTransitions(t *testing.T) {
	tp := ToolProgress{
		Name:   "Shell",
		Label:  "Shell(ls -la)",
		Status: ToolPending,
	}

	if tp.Status != "pending" {
		t.Errorf("expected 'pending', got %q", tp.Status)
	}

	tp.Status = ToolRunning
	if tp.Status != "running" {
		t.Errorf("expected 'running', got %q", tp.Status)
	}

	tp.Status = ToolDone
	tp.Elapsed = 150 * time.Millisecond
	if tp.Status != "done" {
		t.Errorf("expected 'done', got %q", tp.Status)
	}
	if tp.Elapsed != 150*time.Millisecond {
		t.Errorf("expected 150ms, got %v", tp.Elapsed)
	}

	tp.Status = ToolError
	if tp.Status != "error" {
		t.Errorf("expected 'error', got %q", tp.Status)
	}
}

func TestProgressEvent_NilStructured(t *testing.T) {
	event := &ProgressEvent{
		Lines:     []string{"> done"},
		Timestamp: time.Now(),
	}
	if event.Structured != nil {
		t.Error("Structured should be nil")
	}
	if len(event.Lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(event.Lines))
	}
}

func TestFormatSubAgentProgress(t *testing.T) {
	tests := []struct {
		name   string
		detail SubAgentProgressDetail
		want   string
	}{
		{
			name: "single line thinking",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"💭 思考中..."},
				Depth: 0,
			},
			want: "> ├─ 🔄 crown-prince: 💭 思考中...",
		},
		{
			name: "single line tool progress",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"⏳ Shell(ls) ..."},
				Depth: 0,
			},
			want: "> ├─ 🔄 crown-prince: ⏳ Shell(ls) ...",
		},
		{
			name: "completed (empty lines)",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{""},
				Depth: 0,
			},
			want: "> ├─ ✅ crown-prince",
		},
		{
			name: "completed (nil lines)",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: nil,
				Depth: 0,
			},
			want: "> ├─ ✅ crown-prince",
		},
		{
			name: "multi line - takes last own line",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"💭 思考中...", "⏳ Shell(ls) ...", "⏳ Shell(go test) ..."},
				Depth: 0,
			},
			want: "> ├─ 🔄 crown-prince: ⏳ Shell(go test) ...",
		},
		{
			name: "filters sub-agent tree lines from quote-prefixed content",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"> ├─ 🔄 department-state: ⏳ SubAgent [ministry-works]: ..."},
				Depth: 0,
			},
			// All lines are > -prefixed (from child SubAgent), so nothing remains → completed
			want: "> ├─ ✅ crown-prince",
		},
		{
			name: "filters nested tree lines, keeps own progress",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"【奏报】判定：🟢 直接执行", "> ├─ 🔄 department-state: ⏳ SubAgent [ministry-works]: ..."},
				Depth: 0,
			},
			// "> "-prefixed lines are filtered; own line "【奏报】判定：🟢 直接执行" remains
			want: "> ├─ 🔄 crown-prince: 【奏报】判定：🟢 直接执行",
		},
		{
			name: "real 3-layer scenario: crown-prince with child tree lines",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince"},
				Lines: []string{
					"【奏报】 -  圣旨：启动三层 SubAgent 并发测试\n-  判定：🟢 直接执行 → 尚书省 (department-state)\n-  理由：明确的调度测试任务，指令清晰，直接分发执行\n臣这就调度尚书省，令其并发派发三部执行。",
					"> ├─ 🔄 department-state: ⏳ SubAgent [ministry-works]: 💭 思考中...\n> ├─ 🔄 department-state: ✅ SubAgent [ministry-works]: done (4.66s)\n> ├─ 🔄 department-state: ⏳ SubAgent [ministry-justice]: 💭 思考中...",
				},
				Depth: 0,
			},
			// Multi-line own content: last own line is "臣这就调度尚书省，令其并发派发三部执行。"
			// The second element is all > -prefixed → filtered
			want: "> ├─ 🔄 crown-prince: 臣这就调度尚书省，令其并发派发三部执行。",
		},
		{
			name: "depth 1 - own progress only",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince", "main/crown-prince/ministry-works"},
				Lines: []string{"💭 审计中...", "⏳ Shell(go test) ..."},
				Depth: 1,
			},
			want: "> 　├─ 🔄 ministry-works: ⏳ Shell(go test) ...",
		},
		{
			name: "depth 1 completed",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince", "main/crown-prince/ministry-works"},
				Lines: []string{""},
				Depth: 1,
			},
			want: "> 　├─ ✅ ministry-works",
		},
		{
			name: "depth 2 own progress",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince", "main/crown-prince/department-state", "main/crown-prince/department-state/ministry-justice"},
				Lines: []string{"💭 运行测试...", "✅ Shell(go test) (1.2s)"},
				Depth: 2,
			},
			want: "> 　　├─ 🔄 ministry-justice: ✅ Shell(go test) (1.2s)",
		},
		{
			name: "empty path with content",
			detail: SubAgentProgressDetail{
				Path:  nil,
				Lines: []string{"some progress"},
				Depth: 0,
			},
			want: "> ├─ 🔄 : some progress",
		},
		{
			name: "empty path completed",
			detail: SubAgentProgressDetail{
				Path:  nil,
				Lines: nil,
				Depth: 0,
			},
			want: "> ├─ ✅ ",
		},
		{
			name: "double quote prefix on own content",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"> > ⏳ Shell(go test) ..."},
				Depth: 0,
			},
			// "> "-prefixed lines are filtered
			want: "> ├─ ✅ crown-prince",
		},
		{
			name: "filters tree-line characters (│) in content",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince", "main/crown-prince/ministry-rites"},
				Lines: []string{"> ├─ 🔄 ministry-works: 💭 审计中...", "│  more tree content"},
				Depth: 1,
			},
			// All lines filtered (first is > -prefixed, second has │)
			want: "> 　├─ ✅ ministry-rites",
		},
		{
			name: "mixed own and sub-agent lines - keeps own",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"调度三部执行", "> ├─ 🔄 department-state: ⏳ SubAgent ...", "⏳ SubAgent(ministry-works): done"},
				Depth: 0,
			},
			// "调度三部执行" (own), "> ├─..." (filtered), "⏳ SubAgent...: done" (own)
			want: "> ├─ 🔄 crown-prince: ⏳ SubAgent(ministry-works): done",
		},
		{
			name: "path without slash uses full string as role",
			detail: SubAgentProgressDetail{
				Path:  []string{"simple-role"},
				Lines: []string{"working"},
				Depth: 0,
			},
			want: "> ├─ 🔄 simple-role: working",
		},
		{
			name: "long content is truncated",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{strings.Repeat("这是一段非常长的进度内容用来测试截断功能是否正常工作", 10)},
				Depth: 0,
			},
			want: "> ├─ 🔄 crown-prince: " + truncateProgress(strings.Repeat("这是一段非常长的进度内容用来测试截断功能是否正常工作", 10), 80),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatSubAgentProgress(tt.detail)
			if got != tt.want {
				t.Errorf("formatSubAgentProgress() =\n  got: %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestCleanQuotePrefix(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"no prefix", "no prefix"},
		{"> single prefix", "single prefix"},
		{"> > double prefix", "double prefix"},
		{"> > > triple prefix", "triple prefix"},
		{">   leading spaces", "leading spaces"},
		{"> ", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := cleanQuotePrefix(tt.input)
			if got != tt.want {
				t.Errorf("cleanQuotePrefix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestFlattenLines(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  []string
	}{
		{name: "nil input", lines: nil, want: nil},
		{name: "empty input", lines: []string{}, want: nil},
		{name: "skip empty strings", lines: []string{"", "hello", ""}, want: []string{"hello"}},
		{name: "single line no newline", lines: []string{"hello"}, want: []string{"hello"}},
		{name: "multiline in single element", lines: []string{"line1\nline2\nline3"}, want: []string{"line1", "line2", "line3"}},
		{name: "multiple elements with newlines", lines: []string{"a\nb", "c\nd"}, want: []string{"a", "b", "c", "d"}},
		{name: "trailing newline", lines: []string{"hello\n"}, want: []string{"hello", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenLines(tt.lines)
			if len(got) != len(tt.want) {
				t.Errorf("flattenLines() len = %d, want %d\ngot:  %v\nwant: %v", len(got), len(tt.want), got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("flattenLines()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsSubAgentTreeLine(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"├─ 🔄 ministry-works: 💭 审计中...", true},
		{"└─ ✅ ministry-works", true},
		{"│  💭 思考中...", true},
		{"> ├─ 🔄 department-state: ⏳ ...", true},
		{"> > ├─ 🔄 nested: ...", true},
		{"💭 思考中...", false},
		{"⏳ Shell(ls) ...", false},
		{"【奏报】调度三部执行", false},
		{"✅ Shell(go test) (1.2s)", false},
		{"", false},
		{"> 💭 思考中...", false}, // quote prefix but no tree chars after cleaning
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := isSubAgentTreeLine(tt.input)
			if got != tt.want {
				t.Errorf("isSubAgentTreeLine(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractOwnProgress(t *testing.T) {
	tests := []struct {
		name string
		flat []string
		want string
	}{
		{
			name: "simple own lines",
			flat: []string{"💭 思考中...", "⏳ Shell(go test) ..."},
			want: "⏳ Shell(go test) ...",
		},
		{
			name: "filters quote-prefixed lines",
			flat: []string{"> ├─ 🔄 child: ...", "> │  more ..."},
			want: "",
		},
		{
			name: "keeps own, filters child quote lines",
			flat: []string{"调度三部", "> ├─ 🔄 child: ...", "执行完毕"},
			want: "执行完毕",
		},
		{
			name: "filters tree-line characters",
			flat: []string{"│  some tree", "├─ 🔄 role: content"},
			want: "",
		},
		{
			name: "mixed content with tree chars and own",
			flat: []string{"【奏报】判定", "│  tree line", "⏳ Shell(ls) ..."},
			want: "⏳ Shell(ls) ...",
		},
		{
			name: "nil input",
			flat: nil,
			want: "",
		},
		{
			name: "all empty",
			flat: []string{"", "> ", ""},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOwnProgress(tt.flat)
			if got != tt.want {
				t.Errorf("extractOwnProgress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncateProgress(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly 5", 5, "ex..."},
		{"exactly 6", 6, "exa..."},
		{"too long content here", 10, "too lon..."},
		{"中文字符串测试很长很长很长", 10, "中文字符串测试..."},
		{"emoji 🎉🎊🎈 content", 10, "emoji 🎉..."},
	}
	for _, tt := range tests {
		name := fmt.Sprintf("%s_len%d", string([]rune(tt.input)[:min(5, len([]rune(tt.input)))]), tt.maxLen)
		t.Run(name, func(t *testing.T) {
			got := truncateProgress(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateProgress(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}
