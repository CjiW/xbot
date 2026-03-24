package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ==================== 基础类型测试 ====================

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
	sp := &StructuredProgress{Phase: PhaseThinking, Iteration: 1}
	for _, tc := range []struct {
		phase ProgressPhase
		want  ProgressPhase
	}{
		{PhaseToolExec, PhaseToolExec},
		{PhaseCompressing, PhaseCompressing},
		{PhaseRetrying, PhaseRetrying},
		{PhaseDone, PhaseDone},
	} {
		sp.Phase = tc.phase
		if sp.Phase != tc.want {
			t.Errorf("expected %q, got %q", tc.want, sp.Phase)
		}
	}
}

func TestToolProgress_StatusTransitions(t *testing.T) {
	tp := ToolProgress{Name: "Shell", Label: "Shell(ls -la)", Status: ToolPending}
	for _, tc := range []struct {
		status ToolStatus
		want   ToolStatus
	}{
		{ToolPending, ToolPending},
		{ToolRunning, ToolRunning},
		{ToolDone, ToolDone},
		{ToolError, ToolError},
	} {
		tp.Status = tc.status
		if tp.Status != tc.want {
			t.Errorf("expected %q, got %q", tc.want, tp.Status)
		}
	}
	tp.Elapsed = 150 * time.Millisecond
	if tp.Elapsed != 150*time.Millisecond {
		t.Errorf("expected 150ms, got %v", tp.Elapsed)
	}
}

func TestProgressEvent_NilStructured(t *testing.T) {
	event := &ProgressEvent{Lines: []string{"> done"}, Timestamp: time.Now()}
	if event.Structured != nil {
		t.Error("Structured should be nil")
	}
	if len(event.Lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(event.Lines))
	}
}

// ==================== 辅助函数测试 ====================

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

func TestProgressTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxRunes int
		want     string
	}{
		{"short", 10, "short"},
		{"exact", 5, "exact"},
		{"你好世界", 6, "你好世界"},
		{"too long", 5, "too …"},
		{"abcdef", 6, "abcdef"},
		{"a", 1, "a"},
		{"", 10, ""},
		{"hello world", 3, "he…"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := progressTruncate(tt.input, tt.maxRunes)
			if got != tt.want {
				t.Errorf("progressTruncate(%q, %d) = %q, want %q", tt.input, tt.maxRunes, got, tt.want)
			}
		})
	}
}

func TestExtractRoleName(t *testing.T) {
	tests := []struct {
		path []string
		want string
	}{
		{[]string{"main/crown-prince"}, "crown-prince"},
		{[]string{"a/b", "a/b/c"}, "c"},
		{[]string{"simple-role"}, "simple-role"},
		{[]string{}, ""},
		{nil, ""},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path, "/"), func(t *testing.T) {
			got := extractRoleName(tt.path)
			if got != tt.want {
				t.Errorf("extractRoleName(%v) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ==================== 子 Agent 树状行解析测试 ====================

func TestIsSubAgentTreeLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"├─ 🔄 crown-prince: 💭 思考中...", true},
		{"└─ ✅ ministry-works: ✅ done", true},
		{"│  💭 thinking", true},
		{"　├─ 🔄 ministry-works: ⏳ Shell(ls)", true},    // 全角空格缩进
		{"> ├─ 🔄 crown-prince: 💭 思考中...", true},        // 带引用前缀
		{"> > ├─ 🔄 ministry-works: ⏳ Shell(ls)", true}, // 双引用前缀
		{"💭 思考中...", false},                            // 普通行
		{"⏳ Shell(ls) ...", false},
		{"【奏报】调度三部执行", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isSubAgentTreeLine(tt.line)
			if got != tt.want {
				t.Errorf("isSubAgentTreeLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestParseSubAgentTreeLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   childAgentStatus
		wantOK bool
	}{
		{
			name:   "running with desc",
			line:   "├─ 🔄 ministry-works: ⏳ Shell(ls) ...",
			want:   childAgentStatus{Role: "ministry-works", Status: "🔄", Desc: "⏳ Shell(ls) ..."},
			wantOK: true,
		},
		{
			name:   "completed with desc",
			line:   "├─ ✅ ministry-justice: ✅ Shell(go test) (4.66s)",
			want:   childAgentStatus{Role: "ministry-justice", Status: "✅", Desc: "✅ Shell(go test) (4.66s)"},
			wantOK: true,
		},
		{
			name:   "failed",
			line:   "├─ ❌ ministry-works: Error: timeout",
			want:   childAgentStatus{Role: "ministry-works", Status: "❌", Desc: "Error: timeout"},
			wantOK: true,
		},
		{
			name:   "running no desc",
			line:   "├─ 🔄 ministry-rites:",
			want:   childAgentStatus{Role: "ministry-rites", Status: "🔄", Desc: ""},
			wantOK: true,
		},
		{
			name:   "with quote prefix",
			line:   "> ├─ 🔄 crown-prince: 💭 思考中...",
			want:   childAgentStatus{Role: "crown-prince", Status: "🔄", Desc: "💭 思考中..."},
			wantOK: true,
		},
		{
			name:   "with full-width indent",
			line:   "　├─ 🔄 department-state: ⏳ SubAgent [ministry-works]...",
			want:   childAgentStatus{Role: "department-state", Status: "🔄", Desc: "⏳ SubAgent [ministry-works]..."},
			wantOK: true,
		},
		{
			name:   "empty line",
			line:   "",
			want:   childAgentStatus{},
			wantOK: false,
		},
		{
			name:   "no colon",
			line:   "├─ 🔄 no-colon-here",
			want:   childAgentStatus{},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseSubAgentTreeLine(tt.line)
			if ok != tt.wantOK {
				t.Errorf("parseSubAgentTreeLine(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
				return
			}
			if ok && got != tt.want {
				t.Errorf("parseSubAgentTreeLine(%q) = %+v, want %+v", tt.line, got, tt.want)
			}
		})
	}
}

func TestFormatChildAgentsSummary(t *testing.T) {
	tests := []struct {
		name     string
		children []childAgentStatus
		max      int
		want     string
	}{
		{
			name:     "nil",
			children: nil,
			max:      60,
			want:     "",
		},
		{
			name:     "empty",
			children: []childAgentStatus{},
			max:      60,
			want:     "",
		},
		{
			name: "single running",
			children: []childAgentStatus{
				{Role: "工部", Status: "🔄", Desc: "⏳ Shell(ls)"},
			},
			max:  60,
			want: "🔄 工部(⏳ Shell(ls))",
		},
		{
			name: "single completed no desc",
			children: []childAgentStatus{
				{Role: "工部", Status: "✅", Desc: ""},
			},
			max:  60,
			want: "✅ 工部",
		},
		{
			name: "three agents mixed status",
			children: []childAgentStatus{
				{Role: "工部", Status: "🔄", Desc: "⏳ Shell(go version)"},
				{Role: "刑部", Status: "✅", Desc: ""},
				{Role: "礼部", Status: "🔄", Desc: "💭 思考中"},
			},
			max:  60,
			want: "🔄 工部(⏳ Shell(go ver…) · ✅ 刑部 · 🔄 礼部(💭 思考中)",
		},
		{
			name: "all completed",
			children: []childAgentStatus{
				{Role: "工部", Status: "✅", Desc: ""},
				{Role: "刑部", Status: "✅", Desc: ""},
				{Role: "礼部", Status: "✅", Desc: ""},
			},
			max:  60,
			want: "✅ 工部 · ✅ 刑部 · ✅ 礼部",
		},
		{
			name: "with failure",
			children: []childAgentStatus{
				{Role: "工部", Status: "✅", Desc: ""},
				{Role: "刑部", Status: "❌", Desc: "Error"},
				{Role: "礼部", Status: "🔄", Desc: "⏳ running"},
			},
			max:  60,
			want: "✅ 工部 · ❌ 刑部(Error) · 🔄 礼部(⏳ running)",
		},
		{
			name: "many agents - shows count",
			children: []childAgentStatus{
				{Role: "a", Status: "🔄", Desc: ""},
				{Role: "b", Status: "✅", Desc: ""},
				{Role: "c", Status: "🔄", Desc: ""},
				{Role: "d", Status: "✅", Desc: ""},
				{Role: "e", Status: "🔄", Desc: ""},
				{Role: "f", Status: "✅", Desc: ""},
				{Role: "g", Status: "❌", Desc: ""},
			},
			max:  60,
			want: "🔄3 · ✅3 · ❌1",
		},
		{
			name: "truncate to max runes",
			children: []childAgentStatus{
				{Role: "very-long-role-name-a", Status: "🔄", Desc: "this is a very long description"},
				{Role: "very-long-role-name-b", Status: "✅", Desc: "this is also long"},
			},
			max:  30,
			want: "🔄 very-long-role-name-a(this …",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatChildAgentsSummary(tt.children, tt.max)
			if got != tt.want {
				t.Errorf("formatChildAgentsSummary() =\n  got: %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestExtractOwnAndChildProgress(t *testing.T) {
	tests := []struct {
		name          string
		lines         []string
		wantOwn       string
		wantChildLen  int
		wantChildRole string // check first child role if >0
	}{
		{
			name:         "only own progress",
			lines:        []string{"💭 思考中...", "⏳ Shell(ls)"},
			wantOwn:      "⏳ Shell(ls)",
			wantChildLen: 0,
		},
		{
			name:         "own + child tree lines",
			lines:        []string{"思考中...", "├─ 🔄 工部: ⏳ Shell(ls)", "├─ ✅ 刑部:"},
			wantOwn:      "思考中...",
			wantChildLen: 2,
		},
		{
			name: "own + quoted child lines",
			lines: []string{
				"思考中...",
				"> ├─ 🔄 工部: ⏳ Shell(ls)",
				"> ├─ ✅ 刑部:",
			},
			wantOwn:      "思考中...",
			wantChildLen: 0, // quoted lines are filtered as deeper child agent
		},
		{
			name: "multiline own + child tree lines",
			lines: []string{
				"【奏报】\n- 判定：🟢 直接执行\n→ 尚书省",
				"├─ 🔄 工部: ⏳ Shell(go version)",
				"├─ ✅ 刑部:",
				"├─ 🔄 礼部: 💭 思考中",
			},
			wantOwn:      "→ 尚书省",
			wantChildLen: 3,
		},
		{
			name:         "only child tree lines no own",
			lines:        []string{"├─ 🔄 工部: ⏳ ls", "├─ ✅ 刑部:"},
			wantOwn:      "",
			wantChildLen: 2,
		},
		{
			name:         "empty input",
			lines:        nil,
			wantOwn:      "",
			wantChildLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flat := flattenLines(tt.lines)
			own, children := extractOwnAndChildProgress(flat)
			if own != tt.wantOwn {
				t.Errorf("own = %q, want %q", own, tt.wantOwn)
			}
			if len(children) != tt.wantChildLen {
				t.Errorf("children len = %d, want %d", len(children), tt.wantChildLen)
			}
			if tt.wantChildLen > 0 && tt.wantChildRole != "" && children[0].Role != tt.wantChildRole {
				t.Errorf("first child role = %q, want %q", children[0].Role, tt.wantChildRole)
			}
		})
	}
}

// ==================== formatSubAgentProgress 主测试 ====================

func TestFormatSubAgentProgress(t *testing.T) {
	tests := []struct {
		name   string
		detail SubAgentProgressDetail
		want   string
	}{
		// === 基础场景 ===
		{
			name: "single line thinking",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"💭 思考中..."},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: 💭 思考中...",
		},
		{
			name: "single line tool progress",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"⏳ Shell(ls) ..."},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: ⏳ Shell(ls) ...",
		},
		{
			name: "completed (empty lines)",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{""},
				Depth: 0,
			},
			want: "> ✅ crown-prince",
		},
		{
			name: "completed (nil lines)",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: nil,
				Depth: 0,
			},
			want: "> ✅ crown-prince",
		},
		{
			name: "empty path with content",
			detail: SubAgentProgressDetail{
				Path:  nil,
				Lines: []string{"some progress"},
				Depth: 0,
			},
			want: "> 🔄 : some progress",
		},
		{
			name: "empty path completed",
			detail: SubAgentProgressDetail{
				Path:  nil,
				Lines: nil,
				Depth: 0,
			},
			want: "> ✅ ",
		},
		{
			name: "path without slash",
			detail: SubAgentProgressDetail{
				Path:  []string{"simple-role"},
				Lines: []string{"working"},
				Depth: 0,
			},
			want: "> 🔄 simple-role: working",
		},
		// === 多行内容 ===
		{
			name: "multi line content - takes last non-empty",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"💭 思考中...", "⏳ Shell(ls) ...", "⏳ Shell(go test) ..."},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: ⏳ Shell(go test) ...",
		},
		{
			name: "multiline content with newlines in single element",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"【奏报】圣旨：启动三层测试\n判定：🟢 直接执行\n→ 尚书省"},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: → 尚书省",
		},
		{
			name: "double quote prefix cleanup",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"> > ⏳ Shell(go test) ..."},
				Depth: 0,
			},
			want: "> ✅ crown-prince",
		},
		// === 深度缩进 ===
		{
			name: "depth 1 multi line",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince", "main/crown-prince/ministry-works"},
				Lines: []string{"💭 审计中...", "⏳ Shell(go test) ..."},
				Depth: 1,
			},
			want: "> 　🔄 ministry-works: ⏳ Shell(go test) ...",
		},
		{
			name: "depth 1 completed",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince", "main/crown-prince/ministry-works"},
				Lines: []string{""},
				Depth: 1,
			},
			want: "> 　✅ ministry-works",
		},
		{
			name: "depth 2 multi line",
			detail: SubAgentProgressDetail{
				Path:  []string{"a/b", "a/b/c", "a/b/c/d"},
				Lines: []string{"💭 运行测试...", "✅ Shell(go test) (1.2s)"},
				Depth: 2,
			},
			want: "> 　　🔄 d: ✅ Shell(go test) (1.2s)",
		},
		// === 子 Agent 并发摘要（fancy 样式）===
		{
			name: "own + 3 child agents mixed status",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince"},
				Lines: []string{
					"→ 尚书省并发派发三部",
					"├─ 🔄 工部: ⏳ Shell(go version)",
					"├─ ✅ 刑部:",
					"├─ 🔄 礼部: 💭 思考中",
				},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: → 尚书省并发派发三部 → 🔄 工部(⏳ Shell(go ver…) · ✅ 刑部 · 🔄 礼部(💭 思考中)",
		},
		{
			name: "own + all children completed",
			detail: SubAgentProgressDetail{
				Path: []string{"main/department-state"},
				Lines: []string{
					"三部任务已分派完毕",
					"├─ ✅ 工部:",
					"├─ ✅ 刑部:",
					"├─ ✅ 礼部:",
				},
				Depth: 0,
			},
			want: "> 🔄 department-state: 三部任务已分派完毕 → ✅ 工部 · ✅ 刑部 · ✅ 礼部",
		},
		{
			name: "only child progress no own (rare case)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/department-state"},
				Lines: []string{
					"├─ 🔄 工部: ⏳ Shell(ls)",
					"├─ ✅ 刑部:",
				},
				Depth: 0,
			},
			want: "> 🔄 department-state: → 🔄 工部(⏳ Shell(ls)) · ✅ 刑部",
		},
		{
			name: "child with failure",
			detail: SubAgentProgressDetail{
				Path: []string{"main/department-state"},
				Lines: []string{
					"三部执行中",
					"├─ ✅ 工部:",
					"├─ ❌ 刑部: Error: test failed",
					"├─ 🔄 礼部: ⏳ running",
				},
				Depth: 0,
			},
			want: "> 🔄 department-state: 三部执行中 → ✅ 工部 · ❌ 刑部(Error: test fa…) · 🔄 礼部(⏳ running)",
		},
		// === 真实场景模拟 ===
		{
			name: "太子多层穿透 - 有子Agent并发",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince"},
				Lines: []string{
					"【奏报】判定：🟢 直接执行 → 尚书省\n理由：明确的调度测试任务\n臣这就调度尚书省",
					"├─ 🔄 department-state: ⏳ SubAgent [ministry-works]...",
					"├─ 🔄 department-state: ⏳ SubAgent [ministry-justice]...",
				},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: 臣这就调度尚书省 → 🔄 department-state(⏳ SubAgent [mi…) · 🔄 department-state(⏳ …",
		},
		{
			name: "尚书省展示子Agent并发 - 核心场景",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince", "main/crown-prince/department-state"},
				Lines: []string{
					"分派三部并行执行",
					"├─ 🔄 ministry-works: ⏳ Shell(go version) ...",
					"├─ ✅ ministry-justice: ✅ Shell(go version) (4.66s)",
					"├─ 🔄 ministry-rites: 💭 思考中...",
				},
				Depth: 1,
			},
			want: "> 　🔄 department-state: 分派三部并行执行 → 🔄 ministry-works(⏳ Shell(go ver…) · ✅ ministry-justice(✅ Sh…",
		},
		{
			name: "尚书省所有子Agent完成",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince", "main/crown-prince/department-state"},
				Lines: []string{
					"三部全部完成，汇总结果",
					"├─ ✅ ministry-works:",
					"├─ ✅ ministry-justice:",
					"├─ ✅ ministry-rites:",
				},
				Depth: 1,
			},
			want: "> 　🔄 department-state: 三部全部完成，汇总结果 → ✅ ministry-works · ✅ ministry-justice · ✅ ministry-rites",
		},
		// === 长文本截断 ===
		{
			name: "long content truncated",
			detail: SubAgentProgressDetail{
				Path:  []string{"main/crown-prince"},
				Lines: []string{"这是一段非常非常非常非常非常非常非常非常非常非常长的进度文本用来测试截断功能是否正常工作"},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: 这是一段非常非常非常非常非常非常非常非常非常非常长的进度文本用来测试截断功能是否正常工作",
		},
		// === 混合引用前缀 + 树状行 ===
		{
			name: "quoted lines filtered, own + tree kept",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince"},
				Lines: []string{
					"> 💭 思考中...",    // 引用前缀行 → 过滤
					"> ⏳ Shell(ls)", // 引用前缀行 → 过滤
					"├─ 🔄 工部: ⏳ ls", // 树状行 → 子Agent
					"├─ ✅ 刑部:",      // 树状行 → 子Agent
				},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: → 🔄 工部(⏳ ls) · ✅ 刑部",
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

// ==================== 输出格式验证测试 ====================

func TestFormatSubAgentProgress_SingleLine(t *testing.T) {
	// 确保所有输出都是单行（不包含 \n）
	testDetails := []SubAgentProgressDetail{
		{
			Path:  []string{"main/crown-prince"},
			Lines: []string{"💭 thinking", "⏳ Shell(ls)", "done"},
			Depth: 0,
		},
		{
			Path: []string{"main/department-state"},
			Lines: []string{
				"分派三部\n并行执行",
				"├─ 🔄 工部: ⏳ Shell(go version)",
				"├─ ✅ 刑部:",
				"├─ 🔄 礼部: 💭 思考中",
			},
			Depth: 0,
		},
		{
			Path:  []string{"a", "a/b", "a/b/c"},
			Lines: []string{"deep nesting", "├─ 🔄 d: working"},
			Depth: 3,
		},
	}
	for i, detail := range testDetails {
		t.Run(fmt.Sprintf("singleline_%d", i), func(t *testing.T) {
			got := formatSubAgentProgress(detail)
			if strings.Contains(got, "\n") {
				t.Errorf("output contains newline: %q", got)
			}
		})
	}
}

func TestFormatSubAgentProgress_StartsWithQuote(t *testing.T) {
	// 确保所有输出都以 "> " 开头（飞书引用块格式）
	testDetails := []SubAgentProgressDetail{
		{Path: []string{"a"}, Lines: []string{"x"}, Depth: 0},
		{Path: []string{"a"}, Lines: nil, Depth: 0},
		{Path: []string{"a", "a/b"}, Lines: []string{"y"}, Depth: 1},
	}
	for i, detail := range testDetails {
		t.Run(fmt.Sprintf("quote_prefix_%d", i), func(t *testing.T) {
			got := formatSubAgentProgress(detail)
			if !strings.HasPrefix(got, "> ") {
				t.Errorf("output does not start with '> ': %q", got)
			}
		})
	}
}
