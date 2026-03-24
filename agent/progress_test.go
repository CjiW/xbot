package agent

import (
	"fmt"
	"strings"
	"testing"
)

// ==================== flattenLines ====================

func TestFlattenLines(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  int // expected number of result lines
	}{
		{"nil", nil, 0},
		{"empty", []string{}, 0},
		{"single", []string{"hello"}, 1},
		{"multi elements", []string{"a", "b", "c"}, 3},
		{"newline in element", []string{"a\nb\nc"}, 3},
		{"mixed", []string{"a", "b\nc", "d"}, 4},
		{"empty elements filtered", []string{"", "a", "", "b"}, 2},
		{"newline empty elements", []string{"a\n\nb"}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := flattenLines(tt.lines)
			if len(got) != tt.want {
				t.Errorf("flattenLines() len = %d, want %d (got: %v)", len(got), tt.want, got)
			}
		})
	}
}

// ==================== progressTruncate ====================

func TestProgressTruncate(t *testing.T) {
	tests := []struct {
		s        string
		maxRunes int
		want     string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 3, "he…"},
		{"hello", 1, "…"},
		{"hello", 0, "…"},
		{"你好世界", 4, "你好世界"},
		{"你好世界", 3, "你好…"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%d", tt.s, tt.maxRunes), func(t *testing.T) {
			got := progressTruncate(tt.s, tt.maxRunes)
			if got != tt.want {
				t.Errorf("progressTruncate(%q, %d) = %q, want %q", tt.s, tt.maxRunes, got, tt.want)
			}
		})
	}
}

// ==================== extractRoleName ====================

func TestExtractRoleName(t *testing.T) {
	tests := []struct {
		path []string
		want string
	}{
		{[]string{"main/crown-prince"}, "crown-prince"},
		{[]string{"a/b", "a/b/c"}, "c"},
		{[]string{"simple"}, "simple"},
		{[]string{"deep/nested/path"}, "path"},
		{nil, ""},
		{[]string{}, ""},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path, ","), func(t *testing.T) {
			got := extractRoleName(tt.path)
			if got != tt.want {
				t.Errorf("extractRoleName(%v) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ==================== isSubAgentLine ====================

func TestIsSubAgentLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		// 树状格式
		{"├─ 🔄 ministry-works: ⏳ Shell(ls)", true},
		{"└─ ✅ 刑部:", true},
		{"│ 🔄 工部: running", true},
		// 引用格式（实际运行时子 Agent 穿透上来的格式化行）
		{"> 🔄 crown-prince: 💭 思考中...", true},
		{"> ✅ ministry-works:", true},
		{"> ❌ ministry-justice: Error: test failed", true},
		{"> 　🔄 department-state: 分派三部", true},
		// 带全角缩进（子 Agent 格式化输出）
		{"　🔄 ministry-works: ⏳ Shell(ls)", true},
		{"　✅ ministry-justice:", true},
		// 不是子 Agent 行
		{"> 💭 思考中...", false},           // 引用前缀但无冒号
		{"> ⏳ Shell(ls) ...", false},    // 引用前缀但无冒号
		{"> > ⏳ Shell(go test)", false}, // 嵌套引用
		{"💭 思考中...", false},             // 无冒号
		{"⏳ Shell(ls) ...", false},      // 无冒号
		{"some random text", false},
		{"", false},
		{"  ", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isSubAgentLine(tt.line)
			if got != tt.want {
				t.Errorf("isSubAgentLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// ==================== isStatusEmojiLine ====================

func TestIsStatusEmojiLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"🔄 role: desc", true},
		{"✅ role:", true},
		{"❌ role: error", true},
		{"⏳ role: pending", true},
		{"🔄 role", false},     // 无冒号
		{"💡 thinking", false}, // 非 status emoji
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isStatusEmojiLine(tt.line)
			if got != tt.want {
				t.Errorf("isStatusEmojiLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// ==================== parseSubAgentLine ====================

func TestParseSubAgentLine(t *testing.T) {
	tests := []struct {
		line       string
		wantOK     bool
		wantRole   string
		wantStatus string
		wantDesc   string
	}{
		// 树状格式
		{"├─ 🔄 ministry-works: ⏳ Shell(ls) ...", true, "ministry-works", "🔄", "⏳ Shell(ls) ..."},
		{"└─ ✅ 刑部:", true, "刑部", "✅", ""},
		{"│ 🔄 工部: running", true, "工部", "🔄", "running"},
		// 引用格式
		{"> 🔄 crown-prince: 💭 思考中...", true, "crown-prince", "🔄", "💭 思考中..."},
		{"> ✅ ministry-works:", true, "ministry-works", "✅", ""},
		{"> 　🔄 department-state: 分派三部", true, "department-state", "🔄", "分派三部"},
		{"　🔄 ministry-works: ⏳ Shell(ls)", true, "ministry-works", "🔄", "⏳ Shell(ls)"},
		// 失败场景
		{"> 💭 思考中...", false, "", "", ""},       // 不是子 Agent 格式
		{"some random text", false, "", "", ""}, // 空白
		{"", false, "", "", ""},                 // 空
	}
	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got, ok := parseSubAgentLine(tt.line)
			if ok != tt.wantOK {
				t.Errorf("parseSubAgentLine(%q) ok = %v, want %v", tt.line, ok, tt.wantOK)
				return
			}
			if !tt.wantOK {
				return
			}
			if got.Role != tt.wantRole || got.Status != tt.wantStatus || got.Desc != tt.wantDesc {
				t.Errorf("parseSubAgentLine(%q) = %+v, want {Role:%q Status:%q Desc:%q}",
					tt.line, got, tt.wantRole, tt.wantStatus, tt.wantDesc)
			}
		})
	}
}

// ==================== formatChildAgentsSummary ====================

func TestFormatChildAgentsSummary(t *testing.T) {
	tests := []struct {
		name string
		c    []childAgentStatus
		max  int
		want string
	}{
		{
			"empty",
			nil, 100, "",
		},
		{
			"single running",
			[]childAgentStatus{{Role: "工部", Status: "🔄", Desc: "⏳ Shell(ls)"}},
			100, "🔄 工部(⏳ Shell(ls))",
		},
		{
			"single completed no desc",
			[]childAgentStatus{{Role: "刑部", Status: "✅"}},
			100, "✅ 刑部",
		},
		{
			"3 mixed",
			[]childAgentStatus{
				{Role: "工部", Status: "🔄", Desc: "⏳ Shell(go version)"},
				{Role: "刑部", Status: "✅"},
				{Role: "礼部", Status: "🔄", Desc: "💭 思考中"},
			},
			100, "🔄 工部(⏳ Shell(go version)) · ✅ 刑部 · 🔄 礼部(💭 思考中)",
		},
		{
			"all completed",
			[]childAgentStatus{
				{Role: "工部", Status: "✅"},
				{Role: "刑部", Status: "✅"},
				{Role: "礼部", Status: "✅"},
			},
			100, "✅ 工部 · ✅ 刑部 · ✅ 礼部",
		},
		{
			"with failure",
			[]childAgentStatus{
				{Role: "工部", Status: "✅"},
				{Role: "刑部", Status: "❌", Desc: "Error: test failed"},
				{Role: "礼部", Status: "🔄", Desc: "⏳ running"},
			},
			100, "✅ 工部 · ❌ 刑部(Error: test failed) · 🔄 礼部(⏳ running)",
		},
		{
			"desc truncated",
			[]childAgentStatus{{Role: "工部", Status: "🔄", Desc: "this is a very long description that should be truncated"}},
			100, "🔄 工部(this is a very long…)",
		},
		{
			"total truncated",
			[]childAgentStatus{
				{Role: "a", Status: "🔄", Desc: "very long desc"},
				{Role: "b", Status: "✅", Desc: "another long desc"},
				{Role: "c", Status: "🔄", Desc: "yet another long desc"},
			},
			30, "🔄 a(very long desc) · ✅ b(ano…",
		},
		{
			"many agents - stats only",
			[]childAgentStatus{
				{Role: "a", Status: "🔄"}, {Role: "b", Status: "🔄"}, {Role: "c", Status: "🔄"},
				{Role: "d", Status: "✅"}, {Role: "e", Status: "✅"}, {Role: "f", Status: "✅"}, {Role: "g", Status: "❌"},
			},
			100, "🔄×3 · ✅×3 · ❌×1",
		},
		{
			"many agents with pending",
			[]childAgentStatus{
				{Role: "a", Status: "🔄"}, {Role: "b", Status: "⏳"}, {Role: "c", Status: "✅"},
				{Role: "d", Status: "✅"}, {Role: "e", Status: "✅"}, {Role: "f", Status: "✅"}, {Role: "g", Status: "❌"},
			},
			100, "🔄×1 · ⏳×1 · ✅×4 · ❌×1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatChildAgentsSummary(tt.c, tt.max)
			if got != tt.want {
				t.Errorf("formatChildAgentsSummary() =\n  got: %q\n  want: %q", got, tt.want)
			}
		})
	}
}

// ==================== extractOwnAndChildProgress ====================

func TestExtractOwnAndChildProgress(t *testing.T) {
	tests := []struct {
		name          string
		flat          []string
		wantOwn       string
		wantChildLen  int
		wantChildRole string // first child role (if any)
	}{
		{
			"own lines only",
			[]string{"💭 思考中...", "⏳ Shell(ls)"},
			"⏳ Shell(ls)", 0, "",
		},
		{
			"tree lines only",
			[]string{"├─ 🔄 工部: ⏳ ls", "├─ ✅ 刑部:"},
			"", 2, "工部",
		},
		{
			"quoted child lines (actual runtime format)",
			[]string{"> 🔄 ministry-works: ⏳ Shell(go version)", "> ✅ ministry-justice:"},
			"", 2, "ministry-works",
		},
		{
			"own + tree children",
			[]string{"分派三部", "├─ 🔄 工部: ⏳ ls", "├─ ✅ 刑部:"},
			"分派三部", 2, "工部",
		},
		{
			"own + quoted children (actual runtime format)",
			[]string{"分派三部并行执行", "> 🔄 ministry-works: ⏳ Shell(go version)", "> ✅ ministry-justice:"},
			"分派三部并行执行", 2, "ministry-works",
		},
		{
			"mixed: own + quoted children + deep quoted lines",
			[]string{
				"三部执行中",
				"> 🔄 ministry-works: ⏳ Shell(go version)",
				"> ✅ ministry-justice:",
				"> 💭 思考中...", // deep quote, not child format → filtered
			},
			"三部执行中", 2, "ministry-works",
		},
		{
			"quoted non-child filtered",
			[]string{"> 💭 思考中...", "> ⏳ Shell(ls)"},
			"", 0, "",
		},
		{
			"multiline own content",
			[]string{"【奏报】判定：🟢 直接执行\n理由：任务清晰\n→ 尚书省"},
			"→ 尚书省", 0, "",
		},
		{
			"multiline with children mixed in",
			[]string{
				"【奏报】判定：🟢 直接执行\n理由：任务清晰\n→ 尚书省",
				"> 🔄 department-state: 分派三部",
			},
			"→ 尚书省", 1, "department-state",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flat := flattenLines(tt.flat)
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
		// === 子 Agent 并发摘要（树状行格式）===
		{
			name: "own + 3 child agents tree format",
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
			want: "> 🔄 crown-prince: → 尚书省并发派发三部 → 🔄 工部(⏳ Shell(go version)) · ✅ 刑部 · 🔄 礼部(💭 思考中)",
		},
		{
			name: "own + all children completed tree format",
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
			name: "only child progress no own tree format",
			detail: SubAgentProgressDetail{
				Path: []string{"main/department-state"},
				Lines: []string{
					"├─ 🔄 工部: ⏳ Shell(ls)",
					"├─ ✅ 刑部:",
				},
				Depth: 0,
			},
			want: "> 🔄 department-state: 🔄 工部(⏳ Shell(ls)) · ✅ 刑部",
		},
		{
			name: "child with failure tree format",
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
			want: "> 🔄 department-state: 三部执行中 → ✅ 工部 · ❌ 刑部(Error: test failed) · 🔄 礼部(⏳ running)",
		},
		// === 子 Agent 并发摘要（引用格式 - 实际运行时穿透）===
		{
			name: "own + quoted child agents (actual runtime format)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince"},
				Lines: []string{
					"→ 尚书省并发派发三部",
					"> 🔄 department-state: ⏳ SubAgent [ministry-works]...",
					"> 🔄 department-state: ⏳ SubAgent [ministry-justice]...",
				},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: → 尚书省并发派发三部 → 🔄 department-state(⏳ SubAgent [ministr…) · 🔄 department-state(⏳ SubAgent [minis…",
		},
		{
			name: "quoted children completed (actual runtime format)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/department-state"},
				Lines: []string{
					"三部全部完成",
					"> ✅ ministry-works:",
					"> ✅ ministry-justice:",
					"> ✅ ministry-rites:",
				},
				Depth: 0,
			},
			want: "> 🔄 department-state: 三部全部完成 → ✅ ministry-works · ✅ ministry-justice · ✅ ministry-rites",
		},
		{
			name: "quoted children mixed (actual runtime format)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/department-state"},
				Lines: []string{
					"分派三部并行执行",
					"> 🔄 ministry-works: ⏳ Shell(go version) ...",
					"> ✅ ministry-justice: ✅ Shell(go version) (4.66s)",
					"> 🔄 ministry-rites: 💭 思考中...",
				},
				Depth: 1,
			},
			want: "> 　🔄 department-state: 分派三部并行执行 → 🔄 ministry-works(⏳ Shell(go version)…) · ✅ ministry-justice(✅ Shell(go version)…",
		},
		{
			name: "太子多层穿透 - 有子Agent (quoted format)",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince"},
				Lines: []string{
					"【奏报】判定：🟢 直接执行 → 尚书省\n理由：明确的调度测试任务\n臣这就调度尚书省",
					"> 🔄 department-state: → 🔄 工部(⏳ls) · ✅ 刑部",
				},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: 臣这就调度尚书省 → 🔄 department-state(→ 🔄 工部(⏳ls) · ✅ 刑部)",
		},
		// === 混合引用前缀 + 树状行 ===
		{
			name: "quoted lines filtered, own + tree kept",
			detail: SubAgentProgressDetail{
				Path: []string{"main/crown-prince"},
				Lines: []string{
					"> 💭 思考中...",    // 引用前缀行但不是子 Agent → 过滤
					"> ⏳ Shell(ls)", // 引用前缀行但不是子 Agent → 过滤
					"├─ 🔄 工部: ⏳ ls", // 树状行 → 子Agent
					"├─ ✅ 刑部:",      // 树状行 → 子Agent
				},
				Depth: 0,
			},
			want: "> 🔄 crown-prince: 🔄 工部(⏳ ls) · ✅ 刑部",
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
			Path: []string{"main/department-state"},
			Lines: []string{
				"分派三部",
				"> 🔄 ministry-works: ⏳ Shell(go version)",
				"> ✅ ministry-justice:",
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
	testDetails := []SubAgentProgressDetail{
		{Path: []string{"a"}, Lines: []string{"x"}, Depth: 0},
		{Path: []string{"a"}, Lines: nil, Depth: 0},
		{Path: []string{"a", "a/b"}, Lines: []string{"y"}, Depth: 1},
		{
			Path: []string{"a"}, Lines: []string{
				"own", "> 🔄 child: desc",
			}, Depth: 0,
		},
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

// ==================== 真实三层嵌套场景模拟 ====================

func TestFormatSubAgentProgress_ThreeLayerScenario(t *testing.T) {
	// 模拟三层并发场景的实际数据流：
	// L1: 主Agent (上柱国)
	// L2: 太子 (crown-prince) → 调度尚书省
	// L3: 尚书省 (department-state) → 并发派发三部

	// 场景1: 尚书省正在并发执行三部（实际运行时引用格式穿透）
	t.Run("department-state concurrent execution", func(t *testing.T) {
		detail := SubAgentProgressDetail{
			Path: []string{"main/crown-prince", "main/crown-prince/department-state"},
			Lines: []string{
				"分派三部并行执行",
				"> 　🔄 ministry-works: ⏳ Shell(go version) ...",
				"> 　✅ ministry-justice:",
				"> 　🔄 ministry-rites: 💭 思考中...",
			},
			Depth: 1,
		}
		got := formatSubAgentProgress(detail)
		// 验证: 单行、带缩进、包含三个子Agent状态
		if strings.Contains(got, "\n") {
			t.Errorf("should be single line: %q", got)
		}
		if !strings.Contains(got, "　") {
			t.Errorf("should have fullwidth indent for depth=1: %q", got)
		}
		if !strings.Contains(got, "ministry-works") || !strings.Contains(got, "ministry-justice") || !strings.Contains(got, "ministry-rites") {
			t.Errorf("should contain all three child agents: %q", got)
		}
	})

	// 场景2: 太子收到尚书省的穿透进度（引用格式）
	t.Run("crown-prince receives department-state penetration", func(t *testing.T) {
		detail := SubAgentProgressDetail{
			Path: []string{"main/crown-prince"},
			Lines: []string{
				"【奏报】判定：🟢 直接执行 → 尚书省",
				"> 🔄 department-state: 分派三部并行执行 → 🔄 ministry-works(⏳ Shell(go…) · ✅ ministry-justice · 🔄 ministry-rites(💭)",
			},
			Depth: 0,
		}
		got := formatSubAgentProgress(detail)
		if strings.Contains(got, "\n") {
			t.Errorf("should be single line: %q", got)
		}
		// 应该能识别 department-state 是子Agent
		if !strings.Contains(got, "department-state") {
			t.Errorf("should identify department-state as child agent: %q", got)
		}
	})

	// 场景3: 尚书省所有子Agent完成
	t.Run("department-state all children done", func(t *testing.T) {
		detail := SubAgentProgressDetail{
			Path: []string{"main/crown-prince", "main/crown-prince/department-state"},
			Lines: []string{
				"三部全部完成，汇总结果",
				"> 　✅ ministry-works:",
				"> 　✅ ministry-justice:",
				"> 　✅ ministry-rites:",
			},
			Depth: 1,
		}
		got := formatSubAgentProgress(detail)
		if !strings.Contains(got, "✅ ministry-works") {
			t.Errorf("should show completed ministry-works: %q", got)
		}
		if !strings.Contains(got, "✅ ministry-justice") {
			t.Errorf("should show completed ministry-justice: %q", got)
		}
		if !strings.Contains(got, "✅ ministry-rites") {
			t.Errorf("should show completed ministry-rites: %q", got)
		}
	})
}
