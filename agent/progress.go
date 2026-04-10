package agent

import (
	"fmt"
	"regexp"
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
	Phase            ProgressPhase
	Iteration        int
	ActiveTools      []ToolProgress
	CompletedTools   []ToolProgress
	ThinkingContent  string // assistant's text output (streaming, for display)
	ReasoningContent string // model's reasoning/thinking chain (reasoning_content field)
	TokenUsage       *TokenUsageSnapshot
	Todos            []TodoProgressItem
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
	Summary   string
}

// ToolStatus 工具执行状态。
type ToolStatus string

const (
	ToolPending ToolStatus = "pending"
	ToolRunning ToolStatus = "running"
	ToolDone    ToolStatus = "done"
	ToolError   ToolStatus = "error"
)

// TodoProgressItem represents a single TODO item for progress display.
type TodoProgressItem struct {
	ID   int
	Text string
	Done bool
}

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
// 会自动闭合截断位置处的 Markdown 行内语法标记（`、**、*、[text](、~~）。
func progressTruncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return "…"
	}
	truncated := string(runes[:maxRunes-1])
	return truncated + "…" + closeMarkdown(truncated)
}

// closeMarkdown 扫描字符串中的 Markdown 行内语法，返回需要追加的闭合后缀。
// 使用简易状态机追踪未闭合的标记：backtick、**、*、~~、[。
func closeMarkdown(s string) string {
	var (
		inCode     bool // 在行内代码中（`...`）
		boldOpen   bool // ** 未闭合
		italicOpen bool // * 未闭合
		strikeOpen bool // ~~ 未闭合
		linkOpen   bool // [ 未闭合
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if inCode {
			if r == '`' {
				inCode = false
			}
			continue
		}
		switch r {
		case '`':
			inCode = true
		case '*':
			// 向前看：连续两个 * 是粗体
			if i+1 < len(runes) && runes[i+1] == '*' {
				boldOpen = !boldOpen
				i++ // 跳过第二个 *
			} else {
				italicOpen = !italicOpen
			}
		case '~':
			if i+1 < len(runes) && runes[i+1] == '~' {
				strikeOpen = !strikeOpen
				i++ // 跳过第二个 ~
			}
		case '[':
			linkOpen = true
		case ']':
			linkOpen = false
		}
	}
	var buf strings.Builder
	if inCode {
		buf.WriteByte('`')
	}
	if boldOpen {
		buf.WriteString("**")
	}
	if italicOpen {
		buf.WriteByte('*')
	}
	if linkOpen {
		buf.WriteString("](…)")
	}
	if strikeOpen {
		buf.WriteString("~~")
	}
	return buf.String()
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

// --- 缩进测量与树构建 ---

// countFullWidthIndent 计算一行（去掉 "> " 前缀后）的全角空格缩进层数。
// 用于从扁平文本行重建子 Agent 层级树。
func countFullWidthIndent(line string) int {
	for strings.HasPrefix(line, "> ") {
		line = strings.TrimPrefix(line, "> ")
	}
	count := 0
	for _, r := range line {
		switch r {
		case '　':
			count++
		case ' ', '\t', '│', '├', '└', '─':
			continue
		default:
			return count
		}
	}
	return count
}

// indexedChild 带缩进深度的 childAgentStatus，用于 buildChildTree。
type indexedChild struct {
	depth int
	child childAgentStatus
}

// buildChildTree 根据缩进深度将扁平列表重建为嵌套树。
// 最小缩进层的 item 视为直接子节点，更深的 item 递归归属到前一个浅层节点。
func buildChildTree(items []indexedChild) []childAgentStatus {
	if len(items) == 0 {
		return nil
	}

	minDepth := items[0].depth
	for _, it := range items[1:] {
		if it.depth < minDepth {
			minDepth = it.depth
		}
	}

	var result []childAgentStatus
	for i := 0; i < len(items); {
		if items[i].depth == minDepth {
			child := items[i].child
			j := i + 1
			var sub []indexedChild
			for j < len(items) && items[j].depth > minDepth {
				sub = append(sub, items[j])
				j++
			}
			if len(sub) > 0 {
				child.Children = buildChildTree(sub)
			}
			result = append(result, child)
			i = j
		} else {
			i++
		}
	}
	return result
}

// --- 子 Agent 行识别与解析 ---

// childAgentStatus 表示从子 Agent 行中解析出的状态。
type childAgentStatus struct {
	Role     string             // 角色名
	Status   string             // "🔄" / "✅" / "❌" / "⏳"
	Desc     string             // 简短描述
	Children []childAgentStatus // 嵌套子 Agent（由 buildChildTree 构建）
}

// isSubAgentLine 检查一行是否是子 Agent 的进度行。
// 支持三种格式：
//  1. 树状格式（测试用/穿透场景）：  "├─ 🔄 role: desc" / "└─ ✅ role:"
//  2. 引用格式（实际运行时子 Agent 穿透上来的格式化行）："> 🔄 role: desc" / "> 　✅ role"
//  3. 占位行格式（子 Agent 初始占位）："> ⏳ SubAgent [role]..."
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

	// 占位行格式：⏳ SubAgent [...]...
	if strings.HasPrefix(line, "⏳ SubAgent") {
		return true
	}

	// 引用格式：以状态 emoji + 文本 + 冒号 开头
	line = strings.TrimLeft(line, "　 \t")
	return isStatusEmojiLine(line)
}

// isStatusEmojiLine 检查行是否以状态 emoji 开头并包含冒号（子 Agent 格式化输出的特征）。
// 注意：⏳ 不在此列表中 — ⏳ 仅用于工具占位行（> ⏳ ToolName: args），
// 工具占位行由 isSubAgentLine 的其他分支处理（⏳ SubAgent [...] 专用匹配）。
//
// 为避免将 LLM 输出中引用的工具结果（如 "❌ Shell: cmd"）误判为子 Agent 行，
// 冒号前的名称需通过 isPlausibleAgentRole 检查。
func isStatusEmojiLine(line string) bool {
	for _, prefix := range []string{"🔄 ", "✅ ", "❌ "} {
		if strings.HasPrefix(line, prefix) {
			rest := line[len(prefix):]
			if idx := strings.Index(rest, ":"); idx > 0 {
				candidate := strings.TrimSpace(rest[:idx])
				if isPlausibleAgentRole(candidate) {
					return true
				}
			}
		}
	}
	return false
}

// isPlausibleAgentRole 检查名称是否像 SubAgent 角色名而非工具名。
// SubAgent 角色名特征：
//   - 全小写英文 + 连字符/下划线，如 "crown-prince", "ministry-works", "explore"
//   - 中文角色名，如 "刑部", "工部", "中书省"
//
// 工具名特征：首字母大写英文，如 "Shell", "Read", "FileCreate", "SubAgent"。
// 也排除路径（含 /）和其他非角色名模式。
func isPlausibleAgentRole(name string) bool {
	if name == "" {
		return false
	}
	// 含路径分隔符 → 不是角色名
	if strings.Contains(name, "/") {
		return false
	}
	runes := []rune(name)
	firstRune := runes[0]
	// 首字母是大写 ASCII → 工具名（Shell, Read, FileCreate, SubAgent, Grep, ...）
	if firstRune >= 'A' && firstRune <= 'Z' {
		return false
	}
	// 首字母是非 ASCII（中文等）→ 角色名
	if firstRune > 127 {
		return true
	}
	// ASCII 小写开头 → 角色名（如 "explore", "crown-prince"）
	// 但需排除含空格的句子
	if strings.Contains(name, " ") {
		return false
	}
	return true
}

// parseSubAgentLine 解析子 Agent 进度行，提取角色名和状态。
// 支持三种输入格式：
//  1. 树状格式: "├─ 🔄 ministry-works: ⏳ Shell(ls) ..."
//  2. 引用格式: "🔄 ministry-works: ⏳ Shell(ls) ..." 或 "　🔄 ministry-works: ⏳ Shell(ls)"
//  3. 占位行格式: "⏳ SubAgent [ministry-works]..."
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

	// ⏳ 也可能是状态前缀（占位行 "⏳ SubAgent [role]: task"）
	if status == "🔄" && strings.HasPrefix(line, "⏳ ") {
		line = strings.TrimPrefix(line, "⏳ ")
		status = "⏳"
	}

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

	// 清理角色名：如果格式为 "SubAgent [actual-role]"，提取实际角色名
	if strings.HasPrefix(role, "SubAgent [") && strings.HasSuffix(role, "]") {
		role = role[10 : len(role)-1]
	}

	// Defense: reject tool names (PascalCase) parsed as agent roles.
	// This prevents LLM content like "❌ Shell: cmd" from being misidentified.
	if !isPlausibleAgentRole(role) {
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
//	"🔄×3 ⏳×2"  （超过 6 个时只显示状态统计）
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
			// 无角色名的进度状态行计入 running
			if c.Role == "" {
				running++
				continue
			}
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
		if c.Role == "" {
			// 无角色名的进度状态行（如 "💭 思考中..."）
			parts = append(parts, c.Desc)
			continue
		}
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

// ExtractSubAgentTree 从 ProgressEvent.Lines 中解析子 Agent 的层级树。
// 返回一个扁平列表（每个元素可选 Children），供上层（如 web 渠道）序列化为 JSON。
// 如果 Lines 中没有子 Agent 进度行，返回 nil。
func ExtractSubAgentTree(lines []string) []SubAgentNode {
	flat := flattenLines(lines)
	_, children := extractOwnAndChildProgress(flat)
	if len(children) == 0 {
		return nil
	}
	return convertChildTree(children)
}

// SubAgentNode 可序列化的子 Agent 状态节点（供 channel 层使用）。
type SubAgentNode struct {
	Role     string         `json:"role"`
	Status   string         `json:"status"` // "running" | "done" | "error" | "pending"
	Desc     string         `json:"desc,omitempty"`
	Children []SubAgentNode `json:"children,omitempty"`
}

// convertChildTree 将内部 childAgentStatus 转换为可序列化的 SubAgentNode。
func convertChildTree(children []childAgentStatus) []SubAgentNode {
	if len(children) == 0 {
		return nil
	}
	result := make([]SubAgentNode, 0, len(children))
	for _, c := range children {
		node := SubAgentNode{
			Role:   c.Role,
			Status: emojiToStatus(c.Status),
			Desc:   c.Desc,
		}
		if len(c.Children) > 0 {
			node.Children = convertChildTree(c.Children)
		}
		result = append(result, node)
	}
	return result
}

func emojiToStatus(emoji string) string {
	switch emoji {
	case "✅":
		return "done"
	case "❌":
		return "error"
	case "⏳":
		return "pending"
	default:
		return "running"
	}
}

// extractOwnAndChildProgress 从展平后的行中分离当前 Agent 自身进度和子 Agent 进度。
// 返回 (ownLastLine, childStatuses)。
//
// 子 Agent 行按全角空格缩进深度重建为嵌套树（Children 字段），
// 从而在父级渲染时保留层级关系。
//
// 分离规则：
//   - "> " 前缀 + 状态 emoji + 冒号 → 子 Agent 穿透的格式化输出（解析为 childAgentStatus）
//   - ├─ / └─ 树状行 → 子 Agent 穿透的树状行（解析为 childAgentStatus）
//   - "> ⏳ SubAgent [...]" 占位行 → 子 Agent 初始状态（解析为 childAgentStatus）
//   - 其他 "> " 前缀行（如 "> 💭 思考中..."、工具结果穿透等）→ 过滤掉
//   - 其他非空前缀行 → 当前 Agent 自身进度
//
// isToolCompletionLine 检查是否为工具完成行（如 "✅ Shell: go version (508ms)"）。
// 与子 Agent 完成行（如 "✅ ministry-works: 执行完成"）的区别是：工具完成行以耗时结尾。
func isToolCompletionLine(line string) bool {
	// 清理引用前缀
	for strings.HasPrefix(line, "> ") {
		line = strings.TrimPrefix(line, "> ")
	}
	line = strings.TrimLeft(line, "　 \t")
	// 工具完成行特征：以 ) 结尾（耗时格式如 (508ms)、(1.2s)）
	if !strings.HasSuffix(line, ")") {
		return false
	}
	// 检查包含 (数字 时间单位) 模式
	if idx := strings.LastIndex(line, "("); idx > 0 {
		suffix := line[idx:]
		if reToolDuration.MatchString(suffix) {
			return true
		}
	}
	return false
}

var reToolDuration = regexp.MustCompile(`^\(\d+(?:\.\d+)?(?:ms|s)\)$`)

func extractOwnAndChildProgress(flat []string) (string, []childAgentStatus) {
	var ownLines []string
	var indexed []indexedChild

	for _, line := range flat {
		if isSubAgentLine(line) {
			depth := countFullWidthIndent(line)
			if depth == 0 {
				// 无缩进的行需要区分：
				// 1. 当前 Agent 的工具完成行（如 "✅ Shell: cmd (508ms)"）→ 跳过
				// 2. 子 Agent 占位行（如 "⏳ SubAgent [role]..."）→ 保留
				if isToolCompletionLine(line) {
					continue
				}
			}
			if child, ok := parseSubAgentLine(line); ok {
				indexed = append(indexed, indexedChild{depth: depth, child: child})
			}
			continue
		}
		if strings.HasPrefix(line, "> ") {
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

	return ownLast, buildChildTree(indexed)
}

// --- 树状渲染 ---

const (
	treeChildDescMax  = 40 // 树子节点描述最大 rune 数
	treeStatsLimit    = 6  // 超过此数量的子 Agent 退化为统计摘要
	treeInlineSummMax = 60 // 内联摘要最大 rune 数
	treeMaxDepth      = 2  // 树状渲染最大递归深度
)

// renderChildrenTree 将子 Agent 列表渲染为多行缩进文本。
// 每行以 "> " 开头（飞书引用块兼容），用全角空格 "　" 表示层级（wrap-safe）。
// 不使用树状连线字符（├─/└─/│），避免飞书自动换行后视觉结构破碎。
//
// 输出示例（baseIndent=""，currentDepth=0）：
//
//	> 　🔄 中书: 💭 思考中
//	> 　🔄 尚书: 分派两部
//	> 　　🔄 工部: ⚡ Shell(ls)
//	> 　　✅ 刑部:
func renderChildrenTree(children []childAgentStatus, baseIndent string, currentDepth int) []string {
	if len(children) == 0 {
		return nil
	}

	childIndent := baseIndent + "　"

	// 子 Agent 过多 → 单行统计摘要
	if len(children) > treeStatsLimit {
		summary := formatChildAgentsSummary(children, treeInlineSummMax)
		return []string{fmt.Sprintf("> %s%s", childIndent, summary)}
	}

	var lines []string
	for _, c := range children {
		if len(c.Children) > 0 && currentDepth < treeMaxDepth {
			// 有子节点且未超深度限制：递归展开
			if c.Desc != "" {
				lines = append(lines, fmt.Sprintf("> %s%s %s: %s", childIndent, c.Status, c.Role, progressTruncate(c.Desc, 30)))
			} else {
				lines = append(lines, fmt.Sprintf("> %s%s %s:", childIndent, c.Status, c.Role))
			}
			lines = append(lines, renderChildrenTree(c.Children, childIndent, currentDepth+1)...)
		} else if len(c.Children) > 0 {
			// 超深度限制：子节点内联摘要
			summary := formatChildAgentsSummary(c.Children, treeInlineSummMax)
			if c.Desc != "" {
				lines = append(lines, fmt.Sprintf("> %s%s %s: %s %s", childIndent, c.Status, c.Role, progressTruncate(c.Desc, 20), summary))
			} else {
				lines = append(lines, fmt.Sprintf("> %s%s %s: %s", childIndent, c.Status, c.Role, summary))
			}
		} else {
			// 叶子节点
			if c.Desc != "" {
				lines = append(lines, fmt.Sprintf("> %s%s %s: %s", childIndent, c.Status, c.Role, progressTruncate(c.Desc, treeChildDescMax)))
			} else {
				lines = append(lines, fmt.Sprintf("> %s%s %s:", childIndent, c.Status, c.Role))
			}
		}
	}
	return lines
}

// --- 主格式化函数 ---

// formatSubAgentProgress 格式化 SubAgent 进度为文本。
// 每个 SubAgent 在父 Agent 的 progressLines 中占一个槽，
// 无子 Agent 时输出单行，有子 Agent 时输出多行缩进树（wrap-safe）。
//
// 设计目标：
//   - 用户能清楚看明白：几个 Agent、嵌套几层、在干什么、哪些并发
//   - 所有行以 "> " 开头（飞书引用块格式）
//   - 用全角空格做缩进层级（飞书不折叠，不依赖垂直对齐）
//
// 输出格式示例：
//
//	> 🔄 crown-prince: 💭 思考中...                    （直接子Agent，无缩进）
//	> 🔄 crown-prince: ⏳ Shell(go test) ...           （工具执行）
//	> ✅ crown-prince                                   （完成）
//	> 🔄 crown-prince: 调度中                           （有子Agent，多行）
//	> 　🔄 尚书: 分派两部                                ├── 子Agent（depth=3）
//	> 　　🔄 工部: ⚡ Shell(ls)                          │   └── 孙Agent（depth=4）
func formatSubAgentProgress(detail SubAgentProgressDetail) string {
	const maxContentRunes = 50

	flat := flattenLines(detail.Lines)
	ownLine, children := extractOwnAndChildProgress(flat)
	roleName := extractRoleName(detail.Path)
	// depth=2 表示直接子 Agent（无需缩进），每深一层加一个全角空格
	indentDepth := detail.Depth - 2
	if indentDepth < 0 {
		indentDepth = 0
	}
	indent := strings.Repeat("　", indentDepth)

	// 1. 完成状态：无内容也无子 Agent
	if ownLine == "" && len(children) == 0 {
		return fmt.Sprintf("> %s✅ %s", indent, roleName)
	}

	// 2. 有子 Agent → 多行缩进树
	if len(children) > 0 {
		var rootLine string
		if ownLine != "" {
			rootLine = fmt.Sprintf("> %s🔄 %s: %s", indent, roleName, progressTruncate(ownLine, maxContentRunes))
		} else {
			rootLine = fmt.Sprintf("> %s🔄 %s:", indent, roleName)
		}
		childLines := renderChildrenTree(children, indent, 0)
		return strings.Join(append([]string{rootLine}, childLines...), "\n")
	}

	// 3. 叶子节点（无子 Agent）→ 单行
	ownLine = progressTruncate(ownLine, maxContentRunes)
	return fmt.Sprintf("> %s🔄 %s: %s", indent, roleName, ownLine)
}
