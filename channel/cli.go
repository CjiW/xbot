// Package channel provides the CLI (Command Line Interface) channel for xbot.
//
// It implements a terminal-based chat interface using the Bubble Tea TUI framework,
// featuring:
//   - Incremental streaming rendering (markdown + code blocks)
//   - Tool call visualization with live status indicators
//   - Built-in slash commands: /model, /models, /context, /new
//   - Tab completion for commands and input history
//   - Ctrl+K line deletion with confirmation
//   - Non-interactive (pipe) mode with streaming output
//   - Session restore via --new/--resume flags

package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/tools"
	"xbot/version"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

func init() {
	lipgloss.SetHasDarkBackground(true) // 所有配色方案都基于深色终端背景
	lipgloss.SetColorProfile(termenv.TrueColor)
	termenv.SetDefaultOutput(termenv.NewOutput(os.Stdout, termenv.WithTTY(false)))
}

// --- Theme system ---
//
// Theme = color scheme only. Terminal background is not controlled by xbot.
// All schemes are designed for dark terminal backgrounds.
type cliTheme struct {
	// Text
	TextPrimary   string // 主文本色
	TextSecondary string // 次要文本
	TextMuted     string // 弱化文本/占位符
	// Semantic
	Success string // 成功/完成
	Warning string // 警告/进行中
	Error   string // 错误
	Info    string // 信息/链接
	// UI
	Accent    string // 强调色（边框、焦点）
	AccentAlt string // 次要强调
	BarFilled string // 进度条填充
	BarEmpty  string // 进度条空
	Border    string // 边框
	TitleText string // 标题栏文字（title bar foreground）
	// Surface（第 3 轮增强）
	Surface  string // 标题栏/面板背景（比终端背景稍亮，营造"浮起"效果）
	Overlay  string // 进度框/工具摘要背景（比 Surface 更亮，形成视觉层级）
	Gradient string // 渐变辅助色（用于分隔线、提示条渐变等装饰元素）
}

var (
	themeMidnight = cliTheme{
		TextPrimary:   "#e0e0e0",
		TextSecondary: "#90a4ae",
		TextMuted:     "#666666",
		Success:       "#81c784",
		Warning:       "#ffb74d",
		Error:         "#ef5350",
		Info:          "#64b5f6",
		Accent:        "#5c6bc0",
		AccentAlt:     "#ce93d8",
		BarFilled:     "#5c6bc0",
		BarEmpty:      "#2a2a3a",
		Border:        "#4a4e69",
		TitleText:     "#f2e9e4",
		Surface:       "#2a2a3e",
		Overlay:       "#353550",
		Gradient:      "#3949ab",
	}
	themeOcean = cliTheme{
		TextPrimary:   "#e0f2f1",
		TextSecondary: "#80cbc4",
		TextMuted:     "#546e7a",
		Success:       "#69f0ae",
		Warning:       "#ffe082",
		Error:         "#ff8a80",
		Info:          "#80d8ff",
		Accent:        "#00acc1",
		AccentAlt:     "#80deea",
		BarFilled:     "#00acc1",
		BarEmpty:      "#1a2a3a",
		Border:        "#37474f",
		TitleText:     "#e0f7fa",
		Surface:       "#1a2a3a",
		Overlay:       "#1e3345",
		Gradient:      "#00838f",
	}
	themeForest = cliTheme{
		TextPrimary:   "#c8e6c9",
		TextSecondary: "#81c784",
		TextMuted:     "#5d6d5e",
		Success:       "#a5d6a7",
		Warning:       "#ffe082",
		Error:         "#ef9a9a",
		Info:          "#a5d6a7",
		Accent:        "#66bb6a",
		AccentAlt:     "#aed581",
		BarFilled:     "#66bb6a",
		BarEmpty:      "#1a2e1a",
		Border:        "#2e4a2e",
		TitleText:     "#e8f5e9",
		Surface:       "#1a2e1a",
		Overlay:       "#223a22",
		Gradient:      "#388e3c",
	}
	themeSunset = cliTheme{
		TextPrimary:   "#fff3e0",
		TextSecondary: "#ffcc80",
		TextMuted:     "#6d5d4b",
		Success:       "#ffe082",
		Warning:       "#ffab91",
		Error:         "#ef5350",
		Info:          "#ffe082",
		Accent:        "#ff7043",
		AccentAlt:     "#ffab91",
		BarFilled:     "#ff7043",
		BarEmpty:      "#2e2a1a",
		Border:        "#4e3e2e",
		TitleText:     "#fff8e1",
		Surface:       "#2e2a1a",
		Overlay:       "#3a3020",
		Gradient:      "#e64a19",
	}
	themeRose = cliTheme{
		TextPrimary:   "#fce4ec",
		TextSecondary: "#f48fb1",
		TextMuted:     "#6d4b5b",
		Success:       "#f8bbd0",
		Warning:       "#ffab91",
		Error:         "#ef5350",
		Info:          "#f48fb1",
		Accent:        "#ec407a",
		AccentAlt:     "#ce93d8",
		BarFilled:     "#ec407a",
		BarEmpty:      "#2e1a2a",
		Border:        "#4e2e3e",
		TitleText:     "#fce4ec",
		Surface:       "#2e1a2a",
		Overlay:       "#3a2035",
		Gradient:      "#c2185b",
	}
	themeMono = cliTheme{
		TextPrimary:   "#d0d0d0",
		TextSecondary: "#888888",
		TextMuted:     "#555555",
		Success:       "#aaaaaa",
		Warning:       "#cccccc",
		Error:         "#ff6666",
		Info:          "#aaaaaa",
		Accent:        "#ffffff",
		AccentAlt:     "#888888",
		BarFilled:     "#ffffff",
		BarEmpty:      "#333333",
		Border:        "#555555",
		TitleText:     "#ffffff",
		Surface:       "#222222",
		Overlay:       "#2a2a2a",
		Gradient:      "#666666",
	}

	themeRegistry = map[string]*cliTheme{
		"midnight": &themeMidnight,
		"ocean":    &themeOcean,
		"forest":   &themeForest,
		"sunset":   &themeSunset,
		"rose":     &themeRose,
		"mono":     &themeMono,
	}

	currentTheme = &themeMidnight
)

// ---------------------------------------------------------------------------
// §20 样式缓存系统 — 避免每帧重建 lipgloss.Style（第 7 轮重构）
// ---------------------------------------------------------------------------
// 每个 View() 调用创建 200+ 个 lipgloss.NewStyle() → 改为缓存，只在主题/resize 时重建。

type cliStyles struct {
	TitleBar         lipgloss.Style
	TitleText        lipgloss.Style
	ReadyStatus      lipgloss.Style
	ThinkingSt       lipgloss.Style
	Progress         lipgloss.Style
	Tool             lipgloss.Style
	Separator        lipgloss.Style
	InputBox         lipgloss.Style
	Time             lipgloss.Style
	UserLabel        lipgloss.Style
	AssistLabel      lipgloss.Style
	StreamingLabel   lipgloss.Style
	SystemMsg        lipgloss.Style
	ErrorMsg         lipgloss.Style
	ToolSummary      lipgloss.Style
	ToolHeader       lipgloss.Style
	ToolItem         lipgloss.Style
	ToolErrorItem    lipgloss.Style
	ToolThinking     lipgloss.Style
	ToolHint         lipgloss.Style
	ProgressHeader   lipgloss.Style
	ProgressIter     lipgloss.Style
	ProgressThinking lipgloss.Style
	ProgressDone     lipgloss.Style
	ProgressRunning  lipgloss.Style
	ProgressError    lipgloss.Style
	ProgressElapsed  lipgloss.Style
	ProgressIndent   lipgloss.Style
	ProgressDim      lipgloss.Style
	ProgressBlock    lipgloss.Style
	Accent           lipgloss.Style
	TextMutedSt      lipgloss.Style
	WarningSt        lipgloss.Style
	InfoSt           lipgloss.Style
	TokenUsage       lipgloss.Style
	Footer           lipgloss.Style
	ToastBg          lipgloss.Style
	ToastText        lipgloss.Style
	TodoLabel        lipgloss.Style
	TodoFilled       lipgloss.Style
	TodoEmpty        lipgloss.Style
	TodoDone         lipgloss.Style
	TodoPending      lipgloss.Style
	PanelBox         lipgloss.Style
	PanelHeader      lipgloss.Style
	PanelCursor      lipgloss.Style
	PanelDesc        lipgloss.Style
	PanelHint        lipgloss.Style
	PanelDivider     lipgloss.Style
	PanelEmpty       lipgloss.Style
	FileCompDir      lipgloss.Style
	FileCompFile     lipgloss.Style
	FileCompSel      lipgloss.Style
	HelpTitle        lipgloss.Style
	HelpCmd          lipgloss.Style
	HelpDesc         lipgloss.Style
	HelpGroup        lipgloss.Style
	HelpKey          lipgloss.Style
	HelpPanel        lipgloss.Style
}

func buildStyles(width int) cliStyles {
	t := currentTheme
	c := func(s string) lipgloss.Color { return lipgloss.Color(s) }
	cw := width - 4
	if cw < 10 {
		cw = 10
	}
	return cliStyles{
		TitleBar:         lipgloss.NewStyle().Background(c(t.Border)).Foreground(c(t.TitleText)).Bold(true).Width(width),
		TitleText:        lipgloss.NewStyle(),
		ReadyStatus:      lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true).Padding(0, 1),
		ThinkingSt:       lipgloss.NewStyle().Foreground(c(t.Warning)).Padding(0, 1),
		Progress:         lipgloss.NewStyle().Foreground(c(t.Warning)),
		Tool:             lipgloss.NewStyle().Foreground(c(t.Info)),
		Separator:        lipgloss.NewStyle().Foreground(c(t.BarEmpty)),
		InputBox:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1).Width(width - 4),
		Time:             lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		UserLabel:        lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		AssistLabel:      lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true),
		StreamingLabel:   lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		SystemMsg:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true).Width(width).Align(lipgloss.Center),
		ErrorMsg:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Error)).Foreground(c(t.Error)).Bold(true).Padding(0, 1).Width(cw),
		ToolSummary:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Background(c(t.Overlay)).Foreground(c(t.TextPrimary)).Padding(0, 1).Width(cw).Align(lipgloss.Left),
		ToolHeader:       lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		ToolItem:         lipgloss.NewStyle().Foreground(c(t.Success)),
		ToolErrorItem:    lipgloss.NewStyle().Foreground(c(t.Error)),
		ToolThinking:     lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true),
		ToolHint:         lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		ProgressHeader:   lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		ProgressIter:     lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Bold(true),
		ProgressThinking: lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true),
		ProgressDone:     lipgloss.NewStyle().Foreground(c(t.Success)),
		ProgressRunning:  lipgloss.NewStyle().Foreground(c(t.Warning)),
		ProgressError:    lipgloss.NewStyle().Foreground(c(t.Error)),
		ProgressElapsed:  lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		ProgressIndent:   lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		ProgressDim:      lipgloss.NewStyle().Faint(true),
		ProgressBlock:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1).Width(cw),
		Accent:           lipgloss.NewStyle().Foreground(c(t.Accent)),
		TextMutedSt:      lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		WarningSt:        lipgloss.NewStyle().Foreground(c(t.Warning)),
		InfoSt:           lipgloss.NewStyle().Foreground(c(t.Info)),
		TokenUsage:       lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true),
		Footer:           lipgloss.NewStyle().Background(c(t.Surface)).Foreground(c(t.TextSecondary)),
		ToastBg:          lipgloss.NewStyle().Background(c(t.Surface)).Width(width).Padding(0, 1),
		ToastText:        lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		TodoLabel:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		TodoFilled:       lipgloss.NewStyle().Foreground(c(t.BarFilled)),
		TodoEmpty:        lipgloss.NewStyle().Foreground(c(t.BarEmpty)),
		TodoDone:         lipgloss.NewStyle().Foreground(c(t.Success)),
		TodoPending:      lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		PanelBox:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1),
		PanelHeader:      lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		PanelCursor:      lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		PanelDesc:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		PanelHint:        lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		PanelDivider:     lipgloss.NewStyle().Foreground(c(t.Border)).Faint(true),
		PanelEmpty:       lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true).Width(width - 8).Align(lipgloss.Center),
		FileCompDir:      lipgloss.NewStyle().Foreground(c(t.Info)),
		FileCompFile:     lipgloss.NewStyle().Foreground(c(t.Info)),
		FileCompSel:      lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true).Underline(true),
		HelpTitle:        lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		HelpCmd:          lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true).Width(12),
		HelpDesc:         lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		HelpGroup:        lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		HelpKey:          lipgloss.NewStyle().Foreground(c(t.TextPrimary)).Bold(true).Width(14),
		HelpPanel:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Background(c(t.Overlay)).Padding(0, 1).Width(cw),
	}
}

// ApplyTheme 切换当前配色方案。支持: midnight, ocean, forest, sunset, rose, mono。
// 无效名称回退到 midnight。变更后通过 themeChangeCh 通知运行中的 model。
var themeChangeCh = make(chan struct{}, 1)

func ApplyTheme(name string) {
	if t, ok := themeRegistry[name]; ok {
		currentTheme = t
	} else {
		currentTheme = &themeMidnight
	}
	// Non-blocking send; if model is already processing a theme change, skip.
	select {
	case themeChangeCh <- struct{}{}:
	default:
	}
}

// ThemeNames returns the list of available theme names.
func ThemeNames() []string {
	names := make([]string, 0, len(themeRegistry))
	for name := range themeRegistry {
		names = append(names, name)
	}
	return names
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	cliSenderID    = "cli_user"
	cliChannelName = "cli"
	cliMsgBufSize  = 100
)

// syncWriter wraps an *os.File with DEC Synchronized Output (mode 2026).
// Terminals that support this (GNOME Terminal/VTE 0.68+, iTerm2, foot, etc.)
// will batch all writes between the begin/end markers into a single
// atomic frame, eliminating flicker caused by partial repaints.
// Terminals that don't support mode 2026 simply ignore the sequences.

// maxBubbleWidth returns the content width used for message rendering.
// Full width minus small margins for readability.
func maxBubbleWidth(termWidth int) int {
	w := termWidth - 2
	if w < 30 {
		w = 30
	}
	return w
}

// truncateToWidth truncates s so its display width (accounting for wide CJK
// characters) fits within maxWidth columns.  If truncated, "..." is appended.
// This avoids slicing mid-UTF-8-byte which would corrupt terminal rendering.
func truncateToWidth(s string, maxWidth int) string {
	if runewidth.StringWidth(s) <= maxWidth {
		return s
	}
	ellipsis := "..."
	target := maxWidth - runewidth.StringWidth(ellipsis)
	if target <= 0 {
		return ellipsis[:maxWidth]
	}
	w := 0
	for i, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > target {
			return s[:i] + ellipsis
		}
		w += rw
	}
	return s
}

// truncateRunes truncates a line to maxW display columns, preserving ANSI escape
// sequences and handling wide runes correctly. Uses runewidth for display width.
func truncateRunes(line string, maxW int) string {
	if lipgloss.Width(line) <= maxW {
		return line
	}
	var buf strings.Builder
	w := 0
	inEscape := false
	for _, r := range line {
		if r == '\x1b' {
			inEscape = true
			buf.WriteRune(r)
			continue
		}
		if inEscape {
			buf.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		rw := runewidth.RuneWidth(r)
		if w+rw > maxW {
			break
		}
		buf.WriteRune(r)
		w += rw
	}
	return buf.String()
}

// newGlamourRenderer creates a glamour Markdown renderer with custom styling.
// - Document.Margin=0 to prevent misalignment inside lipgloss bubbles
// - Code blocks: accent-colored prefix + keyword/string highlighting
// - Inline code: subtle accent background for readability
// - Headings: accent color for visual hierarchy
func newGlamourRenderer(wrapWidth int) *glamour.TermRenderer {
	style := glamour.DarkStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero

	// 代码块：使用强调色前缀标记代码区域，语法高亮用主题色
	style.CodeBlock.Margin = &zero
	accentPrefix := "▌"
	style.CodeBlock.Prefix = accentPrefix
	// 语法高亮：关键字用强调色，字符串用成功色，注释用弱化色
	accentColor := currentTheme.Accent
	style.CodeBlock.Chroma.Keyword.Color = &accentColor
	style.CodeBlock.Chroma.KeywordType.Color = &accentColor
	style.CodeBlock.Chroma.KeywordReserved.Color = &accentColor
	strColor := currentTheme.Success
	style.CodeBlock.Chroma.LiteralString.Color = &strColor
	infoColor := currentTheme.Info
	style.CodeBlock.Chroma.NameFunction.Color = &infoColor
	style.CodeBlock.Chroma.NameBuiltin.Color = &infoColor
	mutedColor := currentTheme.TextSecondary
	style.CodeBlock.Chroma.Comment.Color = &mutedColor
	style.CodeBlock.Chroma.CommentPreproc.Color = &mutedColor

	// 内联代码：强调色前缀，提升代码片段辨识度
	style.Code.Prefix = accentPrefix
	style.Code.Suffix = accentPrefix

	// 标题：使用强调色建立视觉层次（H1/H2 大标题用强调色，H3 用 Info 色）
	trueVal := true
	style.H1.Color = &accentColor
	style.H1.Bold = &trueVal
	style.H2.Color = &accentColor
	style.H2.Bold = &trueVal
	style.H3.Color = &infoColor
	style.H3.Bold = &trueVal

	// 加粗文本：用主文本色增强对比度
	textPrimary := currentTheme.TextPrimary
	style.Strong.Color = &textPrimary
	style.Strong.Bold = &trueVal

	// 引用块：用次要文本色 + 竖线缩进
	pipeToken := "│"
	style.BlockQuote.IndentToken = &pipeToken
	style.BlockQuote.Color = &mutedColor

	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(wrapWidth),
	)
	return r
}

// ---------------------------------------------------------------------------
// 代码块渲染升级（第 4 轮）：lipgloss 边框 + 语言标签 + 深色背景
// ---------------------------------------------------------------------------

// codeBlockRe 匹配 markdown 代码块: ```lang\ncode\n```
var codeBlockRe = regexp.MustCompile("(?s)```([a-zA-Z0-9_+-]*)\\s*\n(.*?)```")

// renderCodeBlockInline 将代码块渲染为 lipgloss 边框包裹的精美样式。
// 返回渲染后的 ANSI 字符串。
func renderCodeBlockInline(lang, code string, maxWidth int) string {
	// 语言标签
	langLabel := lang
	if langLabel == "" {
		langLabel = "text"
	}
	langTag := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextMuted)).
		Render(" " + langLabel + " ")

	// 代码内容：保持原始缩进，不添加额外前缀（glamour 已处理语法高亮）
	// 限制最大宽度
	lines := strings.Split(strings.TrimRight(code, "\n"), "\n")
	contentWidth := maxWidth - 4 // border(2) + padding(2)
	var codeLines []string
	for _, line := range lines {
		if lipgloss.Width(line) > contentWidth {
			line = truncateRunes(line, contentWidth)
		}
		codeLines = append(codeLines, line)
	}
	codeContent := strings.Join(codeLines, "\n")

	// 代码块容器：深色背景 + 圆角边框
	blockStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(currentTheme.Accent)).
		Background(lipgloss.Color(currentTheme.Overlay)).
		Padding(0, 1).
		Width(maxWidth)

	// 组装：语言标签行 + 代码内容
	inner := langTag + "\n" + codeContent
	return blockStyle.Render(inner)
}

// prettifyCodeBlocks 在 markdown 渲染前提取代码块并替换为 lipgloss 渲染版本。
// 返回 (preprocessed markdown, nil) — glamour 不再处理代码块。
// 如果输入不包含代码块，直接返回原文本。
func prettifyCodeBlocks(markdown string, maxWidth int) string {
	if maxWidth < 10 {
		maxWidth = 76
	}
	return codeBlockRe.ReplaceAllStringFunc(markdown, func(match string) string {
		sub := codeBlockRe.FindStringSubmatch(match)
		if len(sub) < 3 {
			return match
		}
		lang := sub[1]
		code := sub[2]
		rendered := renderCodeBlockInline(lang, code, maxWidth)
		// 用 HTML 注释标记包裹，让 glamour 把它当普通文本透传
		return "<!--CODEBLOCK_START-->\n" + rendered + "\n<!--CODEBLOCK_END-->"
	})
}

// cliCommands 已知命令列表（用于 Tab 补全，§8）
var cliCommands = []string{
	"/cancel", "/clear", "/compact", "/context", "/exit", "/help",
	"/model", "/models", "/new", "/quit", "/settings", "/setup", "/update",
}

// ---------------------------------------------------------------------------
// CLI Progress Payload (for structured progress events)
// ---------------------------------------------------------------------------

// CLIProgressPayload 结构化进度消息负载（对应 agent.StructuredProgress）。
type CLIProgressPayload struct {
	Phase          string
	Iteration      int
	ActiveTools    []CLIToolProgress
	CompletedTools []CLIToolProgress
	Thinking       string
	SubAgents      []CLISubAgent
	Todos          []CLITodoItem
	TokenUsage     *CLITokenUsage // Token 用量快照（实时更新）
}

// CLITokenUsage Token 使用量（对应 agent.TokenUsageSnapshot）
type CLITokenUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CacheHitTokens   int64
}

// CLITodoItem represents a TODO item for CLI display.
type CLITodoItem struct {
	ID   int
	Text string
	Done bool
}

// CLIToolProgress 单个工具的执行进度。
type CLIToolProgress struct {
	Name      string
	Label     string
	Status    string
	Elapsed   int64 // milliseconds
	Iteration int   // 所属迭代 ID
}

// CLISubAgent 子 Agent 的结构化进度状态。
type CLISubAgent struct {
	Role     string
	Status   string // "running" | "done" | "error"
	Desc     string
	Children []CLISubAgent
}

// cliIterationSnapshot captures a completed iteration for the progress panel.
type cliIterationSnapshot struct {
	Iteration int
	Thinking  string
	Tools     []CLIToolProgress
}

// formatElapsed formats milliseconds into a human-friendly duration string.
func formatElapsed(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	mins := ms / 60000
	secs := (ms % 60000) / 1000
	return fmt.Sprintf("%dm%ds", mins, secs)
}

// ---------------------------------------------------------------------------
// CLI Channel Config
// ---------------------------------------------------------------------------

// HistoryIteration 历史迭代快照（用于会话恢复的 tool_summary 渲染）
type HistoryIteration struct {
	Iteration int
	Thinking  string
	Tools     []CLIToolProgress
}

// HistoryMessage 历史消息（用于会话恢复）
type HistoryMessage struct {
	Role       string // "user", "assistant", "tool_summary", "system"
	Content    string
	Timestamp  time.Time
	Iterations []HistoryIteration // 仅 role=="tool_summary" 时有值，按迭代顺序
}

// iterSnapshot mirrors agent.IterationSnapshot for JSON unmarshaling Detail field.
type iterSnapshot struct {
	Iteration int            `json:"iteration"`
	Thinking  string         `json:"thinking,omitempty"`
	Tools     []iterToolSnap `json:"tools"`
}

type iterToolSnap struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"`
	ElapsedMS int64  `json:"elapsed_ms"`
}

// ConvertMessagesToHistory converts raw DB messages into HistoryMessages for CLI display.
// It handles three scenarios:
//  1. Normal completed turn: assistant with Detail → one tool_summary + assistant
//  2. Cancelled/interrupted turn: intermediate assistant(ToolCalls) without Detail → pending tool_summary
//  3. Mixed: some turns completed, last one cancelled
func ConvertMessagesToHistory(msgs []llm.ChatMessage) []HistoryMessage {
	var history []HistoryMessage
	var pendingIters []HistoryIteration
	var curIterTools []CLIToolProgress
	var curIterIdx int
	var curIterThinking string

	finishCurIter := func() {
		if len(curIterTools) > 0 || curIterThinking != "" {
			pendingIters = append(pendingIters, HistoryIteration{
				Iteration: curIterIdx,
				Thinking:  curIterThinking,
				Tools:     curIterTools,
			})
		}
		curIterTools = nil
		curIterThinking = ""
	}

	flushPending := func() {
		finishCurIter()
		if len(pendingIters) > 0 {
			history = append(history, HistoryMessage{
				Role:       "tool_summary",
				Iterations: pendingIters,
			})
			pendingIters = nil
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			if m.Detail != "" {
				// Detail has authoritative iteration history. Discard pending iters
				// from intermediate assistant messages — they lack elapsed/label data.
				finishCurIter()
				pendingIters = nil

				var snaps []iterSnapshot
				if jsonErr := json.Unmarshal([]byte(m.Detail), &snaps); jsonErr == nil {
					iters := make([]HistoryIteration, 0, len(snaps))
					for _, snap := range snaps {
						toolList := make([]CLIToolProgress, len(snap.Tools))
						for i, t := range snap.Tools {
							label := t.Label
							if label == "" {
								label = t.Name
							}
							toolList[i] = CLIToolProgress{
								Name:      t.Name,
								Label:     label,
								Status:    t.Status,
								Elapsed:   t.ElapsedMS,
								Iteration: snap.Iteration,
							}
						}
						iters = append(iters, HistoryIteration{
							Iteration: snap.Iteration,
							Thinking:  snap.Thinking,
							Tools:     toolList,
						})
					}
					if len(iters) > 0 {
						history = append(history, HistoryMessage{
							Role:       "tool_summary",
							Timestamp:  m.Timestamp,
							Iterations: iters,
						})
					}
				}
				if m.Content != "" {
					history = append(history, HistoryMessage{
						Role:      "assistant",
						Content:   m.Content,
						Timestamp: m.Timestamp,
					})
				}
			} else if len(m.ToolCalls) > 0 {
				// Intermediate assistant with tool_calls from incremental persistence.
				// Accumulate into pending — don't flush yet.
				finishCurIter()
				curIterIdx++
				curIterThinking = m.Content
				for _, tc := range m.ToolCalls {
					curIterTools = append(curIterTools, CLIToolProgress{
						Name:      tc.Name,
						Label:     tc.Name,
						Status:    "done",
						Elapsed:   0,
						Iteration: curIterIdx,
					})
				}
			} else if m.Content != "" {
				flushPending()
				history = append(history, HistoryMessage{
					Role:      "assistant",
					Content:   m.Content,
					Timestamp: m.Timestamp,
				})
			}
		default:
			flushPending()
			if m.Content != "" {
				history = append(history, HistoryMessage{
					Role:      m.Role,
					Content:   m.Content,
					Timestamp: m.Timestamp,
				})
			}
		}
	}
	flushPending()
	return history
}

// CLIChannelConfig CLI 渠道配置
type CLIChannelConfig struct {
	WorkDir          string                           // 工作目录（用于标题栏显示）
	ChatID           string                           // 会话 ID（按工作目录区分）
	HistoryLoader    func() ([]HistoryMessage, error) // 会话恢复：加载历史消息
	GetCurrentValues func() map[string]string         // 获取当前配置值（用于 settings panel 初始值）
	ApplySettings    func(values map[string]string)   // 应用设置变更（写 config.json + 更新运行时状态）
	IsFirstRun       bool                             // 首次运行标志，TUI 启动时自动打开 setup panel
}

// ---------------------------------------------------------------------------
// CLI Channel (implements Channel interface)
// ---------------------------------------------------------------------------

// CLIChannel CLI 渠道实现
type CLIChannel struct {
	config  CLIChannelConfig
	msgBus  *bus.MessageBus
	msgChan chan bus.OutboundMessage // 接收 agent 回复的通道
	workDir string                   // 工作目录

	// Bubble Tea
	program   *tea.Program
	programMu sync.Mutex // protects program field
	model     *cliModel

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup

	// Services (injected by Agent or main)
	settingsSvc     SettingsService // interface for GetSettings/SetSetting
	configMu        sync.RWMutex    // protects config override fields
	modelOverride   string          // user-overridden model from /settings panel
	baseURLOverride string          // user-overridden base URL from /settings panel
	modelLister     ModelLister     // provides available model names for combo

	// Background tasks
	bgTaskMgr    *tools.BackgroundTaskManager
	bgSessionKey string
}

// SettingsService is the interface needed by CLIChannel for settings panel.
type SettingsService interface {
	GetSettings(channelName, senderID string) (map[string]string, error)
	SetSetting(channelName, senderID, key, value string) error
}

// ModelLister provides available model names for the settings combo box.
type ModelLister interface {
	ListModels() []string
}

// NewCLIChannel 创建 CLI 渠道
func NewCLIChannel(cfg CLIChannelConfig, msgBus *bus.MessageBus) *CLIChannel {
	return &CLIChannel{
		config:  cfg,
		msgBus:  msgBus,
		workDir: cfg.WorkDir,
		msgChan: make(chan bus.OutboundMessage, cliMsgBufSize),
		stopCh:  make(chan struct{}),
	}
}

// Name 返回渠道名称
func (c *CLIChannel) Name() string {
	return cliChannelName
}

// Start 启动 CLI 渠道（阻塞运行）
func (c *CLIChannel) Start() error {
	log.Info("CLI channel starting...")

	// Capture the real stdout for bubbletea, then redirect os.Stdout and
	// os.Stderr to /dev/null so that background goroutines (logger cleanup,
	// third-party libs, stray fmt.Print, etc.) cannot write to the terminal
	// and cause flickering or garbled output in the alt-screen TUI.
	origStdout := os.Stdout
	origStderr := os.Stderr
	if devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
		os.Stdout = devNull
		os.Stderr = devNull
		defer func() {
			os.Stdout = origStdout
			os.Stderr = origStderr
			_ = devNull.Close()
		}()
	}

	// 初始化 Bubble Tea model
	c.model = newCLIModel()
	c.model.channel = c
	c.model.SetMsgBus(c.msgBus)
	c.model.workDir = c.workDir
	c.model.chatID = c.config.ChatID

	// Setup bg task count callback
	c.updateBgTaskCountFn()

	// 加载历史消息（会话恢复）
	if c.config.HistoryLoader != nil {
		if history, err := c.config.HistoryLoader(); err == nil && len(history) > 0 {
			for _, hm := range history {
				cm := cliMessage{
					role:      hm.Role,
					content:   hm.Content,
					timestamp: hm.Timestamp,
					isPartial: false,
					dirty:     true,
				}
				// 映射迭代快照
				if len(hm.Iterations) > 0 {
					cm.iterations = make([]cliIterationSnapshot, len(hm.Iterations))
					for i, hi := range hm.Iterations {
						cm.iterations[i] = cliIterationSnapshot(hi)
					}
				}
				c.model.messages = append(c.model.messages, cm)
			}
			log.WithField("count", len(history)).Info("Restored session history")
		} else if err != nil {
			log.WithError(err).Warn("Failed to load session history")
		}
	}

	// 首次运行：打开 setup panel
	if c.config.IsFirstRun {
		c.model.openSetupPanel()
	}

	// 创建 Bubble Tea program
	c.programMu.Lock()
	c.program = tea.NewProgram(c.model,
		tea.WithAltScreen(),
		tea.WithOutput(origStdout),
	)
	c.programMu.Unlock()

	// 启动 outbound 消息处理 goroutine
	c.wg.Add(1)
	go c.handleOutbound()

	// §13 异步检查更新（不阻塞 TUI 启动）
	c.CheckUpdateAsync()

	// 运行 Bubble Tea（阻塞）
	if _, err := c.program.Run(); err != nil {
		log.WithError(err).Error("CLI channel exited with error")
		return err
	}

	log.Info("CLI channel stopped")
	return nil
}

// Stop 停止 CLI 渠道
func (c *CLIChannel) Stop() {
	log.Info("CLI channel stopping...")
	close(c.stopCh)
	c.programMu.Lock()
	if c.program != nil {
		c.program.Quit()
	}
	c.programMu.Unlock()
	c.wg.Wait()
	log.Info("CLI channel stopped")
}

// Send 发送消息到 CLI（实现 Channel 接口）
func (c *CLIChannel) Send(msg bus.OutboundMessage) (string, error) {
	msgID := strings.ReplaceAll(uuid.New().String(), "-", "")

	// 发送到消息通道，由 handleOutbound 处理
	select {
	case c.msgChan <- msg:
	default:
		log.Warn("CLI message channel full, dropping message")
	}

	return msgID, nil
}

// SendProgress 发送结构化进度事件到 CLI（非阻塞）。
func (c *CLIChannel) SendProgress(chatID string, payload *CLIProgressPayload) {
	if payload == nil || c.program == nil {
		return
	}
	c.program.Send(cliProgressMsg{payload: payload})
}

// SetBgTaskManager configures the background task manager for status display.
func (c *CLIChannel) SetBgTaskManager(mgr *tools.BackgroundTaskManager, sessionKey string) {
	c.bgTaskMgr = mgr
	c.bgSessionKey = sessionKey
	c.updateBgTaskCountFn()
}

// InjectUserMessage 通知 CLI 有 user 消息被 agent 注入（如 bg task 完成通知）。
// 在 CLI 界面上显示为一条 user 消息，和用户手动输入的效果一致。
func (c *CLIChannel) InjectUserMessage(content string) {
	if c.program != nil {
		c.program.Send(cliInjectedUserMsg{content: content})
	}
}

// updateBgTaskCountFn updates the model's bg task count callback.
func (c *CLIChannel) updateBgTaskCountFn() {
	if c.model == nil {
		return
	}
	if c.bgTaskMgr != nil && c.bgSessionKey != "" {
		key := c.bgSessionKey
		c.model.bgTaskCountFn = func() int {
			return len(c.bgTaskMgr.ListRunning(key))
		}
	}
}

// CheckUpdateAsync starts a background goroutine to check for updates.
// The result is sent to the TUI via program.Send.
func (c *CLIChannel) CheckUpdateAsync() {
	if c.program == nil {
		return
	}
	go func() {
		info := version.CheckUpdate(context.Background())
		c.program.Send(cliUpdateCheckMsg{info: info})
	}()
}

// handleOutbound 处理从 agent 发来的消息
func (c *CLIChannel) handleOutbound() {
	defer c.wg.Done()

	for {
		select {
		case <-c.stopCh:
			return
		case msg := <-c.msgChan:
			if c.program != nil {
				c.program.Send(cliOutboundMsg{msg: msg})
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Bubble Tea Model
// ---------------------------------------------------------------------------

// animTicker 是一个简单的字符动画 ticker，不依赖 bubbles/spinner。
// 支持双色呼吸效果：颜色在 Accent 和 AccentAlt 之间平滑过渡。
type animTicker struct {
	frames   []string
	frame    int
	ticks    int64          // total ticks for phase-aware behavior
	style    lipgloss.Style // 主色调
	styleAlt lipgloss.Style // 备选色（呼吸效果用）
	color    string         // 主色值（主题切换时重建样式用）
	colorAlt string         // 备选色值
}

func newAnimTicker(frames []string, color string) *animTicker {
	altColor := currentTheme.AccentAlt
	return &animTicker{
		frames:   frames,
		style:    lipgloss.NewStyle().Foreground(lipgloss.Color(color)),
		styleAlt: lipgloss.NewStyle().Foreground(lipgloss.Color(altColor)),
		color:    color,
		colorAlt: altColor,
	}
}

// refreshStyles 在主题切换时重建 ticker 的颜色样式
func (t *animTicker) refreshStyles() {
	t.style = lipgloss.NewStyle().Foreground(lipgloss.Color(t.color))
	t.styleAlt = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.AccentAlt))
}

func (t *animTicker) tick() {
	t.ticks++
	t.frame = (t.frame + 1) % len(t.frames)
}

// view 渲染当前帧，带双色呼吸效果（每 10 tick 在两种颜色间切换）
func (t *animTicker) view() string {
	if t.ticks%20 < 10 {
		return t.style.Render(t.frames[t.frame])
	}
	return t.styleAlt.Render(t.frames[t.frame])
}

// viewFrames renders a frame from a given set using the ticker's current frame index.
// 同样带呼吸效果。
func (t *animTicker) viewFrames(frames []string) string {
	idx := t.frame % len(frames)
	if t.ticks%20 < 10 {
		return t.style.Render(frames[idx])
	}
	return t.styleAlt.Render(frames[idx])
}

// Ticker frame presets
var (
	// dotFrames: smooth braille dot sweep — 24 frames for a fluid loop
	dotFrames = []string{
		"⠁", "⠃", "⠇", "⡇", "⣇", "⣧", "⣷", "⣿",
		"⣾", "⣽", "⣻", "⢿", "⡿", "⠿", "⠟", "⠛",
		"⠫", "⠭", "⠮", "⡮", "⡯", "⣯", "⣽", "⣾",
	}
	// arrowFrames: pulsing arrow — tool execution feel
	arrowFrames = []string{"›", "▸", "▶", "▸", "›", "▸", "▶", "▸"}
	// waveFrames: rotating crescent moon phases — subagent feel
	waveFrames = []string{"◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒"}
	// orbitFrames: spinning orbit — processing feel
	orbitFrames = []string{"◌", "◔", "◕", "●", "◕", "◔", "◌", "◔", "◕", "●", "◕", "◔"}
	// splashFrames: loading bar animation — 启动画面进度条
	splashFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	// neonFrames: neon diamond — 用于错误/重试状态，醒目的视觉提示
	neonFrames = []string{"◇", "◆", "◆", "◇", "◇", "◆", "◆", "◇"}
	// pulseFrames: expanding/contracting dots — 用于特殊状态（搜索、扫描等）
	pulseFrames = []string{"·", "∙", "●", "∙", "·", "∙", "●", "∙"}
)

// thinkingVerbs — 类似 Claude Code 的随机动词
var thinkingVerbs = []string{
	"Thinking",
	"Reasoning",
	"Analyzing",
	"Considering",
	"Evaluating",
	"Reflecting",
	"Processing",
	"Contemplating",
}

// pickVerb returns a deterministic verb based on tick count (changes every ~2s at 10 FPS).
func pickVerb(ticks int64) string {
	// Change verb every 20 ticks (2 seconds)
	idx := (ticks / 20) % int64(len(thinkingVerbs))
	return thinkingVerbs[idx]
}

// idlePlaceholders — 就绪态下轮换显示的输入提示，帮助用户发现隐藏功能。
// 每 ~5 秒自动切换（通过 cliTickMsg 驱动）。
var idlePlaceholders = []string{
	"Enter send · Ctrl+J newline · /help",
	"Type /model to switch model",
	"Ctrl+K delete · Ctrl+O tools · Ctrl+E collapse",
	"@filepath to attach files",
	"↑ open background tasks panel",
	"Type /compact to compress context",
	"Type /settings to configure",
	"Type /new to start fresh session",
}

// pickIdlePlaceholder 根据时间返回轮换的 placeholder（每 5 秒切换）
func pickIdlePlaceholder() string {
	idx := int(time.Now().Unix()/5) % len(idlePlaceholders)
	return idlePlaceholders[idx]
}

// tickerTickMsg 是 ticker 定时 tick 消息
type tickerTickMsg struct{}

// splashTickMsg 启动画面定时 tick 消息
type splashTickMsg struct {
	frame int // 当前帧索引
}

// splashDoneMsg 启动画面结束消息
type splashDoneMsg struct{}

// cliToastMsg Toast 通知消息（自动浮现+消失）
type cliToastMsg struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastClearMsg Toast 通知自动清除消息
type cliToastClearMsg struct{}

// cliModel Bubble Tea 状态模型
type cliModel struct {
	viewport        viewport.Model        // 消息显示区
	textarea        textarea.Model        // 用户输入区
	ticker          *animTicker           // 进度动画 ticker
	messages        []cliMessage          // 消息历史
	renderer        *glamour.TermRenderer // Markdown 渲染器
	ready           bool                  // 是否已初始化
	width           int                   // 终端宽度
	height          int                   // 终端高度
	typing          bool                  // agent 是否正在回复
	msgBus          *bus.MessageBus       // 消息总线引用
	streamingMsgIdx int                   // 当前流式消息的索引（-1 表示无流式消息）
	newContentHint  bool                  // 有新内容但用户未在底部（显示 ↓ 提示）
	tempStatus      string                // 临时状态提示（自动过期）
	bgTaskCount     int                   // running background tasks (0 = no indicator)
	bgTaskCountFn   func() int            // callback to get current bg task count (set by channel)

	// 进度信息
	progress          *CLIProgressPayload
	iterationHistory  []cliIterationSnapshot // 已完成迭代快照
	typingStartTime   time.Time              // 本次处理开始时间
	lastSeenIteration int                    // 上次进度事件的迭代号

	// 工作目录（标题栏显示用）
	workDir string

	// 会话 ID（按工作目录区分）
	chatID string

	// Smart quit
	shouldQuit bool // Flag to quit after current operation completes

	// 输入就绪状态（agent 回复期间禁止发送）
	inputReady bool

	// --- §1 增量渲染 ---
	renderCacheValid bool   // 全局缓存是否有效（resize 后置 false）
	cachedHistory    string // 缓存的历史消息渲染结果（不含当前流式消息）
	cachedMsgCount   int    // messages count when cache was built

	// --- §2 工具可视化 ---
	lastCompletedTools []CLIToolProgress // 每轮结束时快照，不依赖 m.progress 生命周期

	// --- §8 Tab 补全 ---
	completions []string // 当前补全候选项
	compIdx     int      // 当前选中的补全索引

	// --- §8b @ 文件引用补全 ---
	fileCompletions []string // @ 文件路径补全候选项
	fileCompIdx     int      // 当前选中的文件补全索引
	fileCompActive  bool     // true = Tab 循环中，阻止重新 glob

	// --- §9 Ctrl+K 上下文编辑 ---
	confirmDelete int // >0 时处于删除确认状态，值为待删除消息数

	// --- §10 TODO 进度条 ---
	todos            []CLITodoItem // 从 progress 事件同步的 TODO 列表
	todosDoneCleared bool          // 全完成后已被用户输入清除，阻止 progress 重填

	// --- §11 Tool Summary 折叠 ---
	toolSummaryExpanded bool // Ctrl+O 切换

	// --- §19 长消息折叠 ---
	msgCollapsed map[int]bool // 折叠状态追踪（key=消息索引）

	// --- §11b Pending Tool Summary ---
	// PhaseDone may arrive before handleAgentMessage. Store the tool_summary
	// here so handleAgentMessage can insert it at the correct position.
	pendingToolSummary *cliMessage

	// --- §12 Interactive Panel ---
	// panelMode: ""=normal, "settings"=settings panel, "askuser"=ask user panel
	panelMode     string
	panelCursor   int            // settings panel: selected item index
	panelEdit     bool           // settings panel: editing current item
	panelEditTA   textarea.Model // settings panel: inline editor
	panelCombo    bool           // settings panel: combo dropdown open
	panelComboIdx int            // settings panel: combo selected option index
	// --- AskUser panel ---
	panelItems     []askItem                       // askuser panel: question items
	panelTab       int                             // askuser panel: current tab (question index)
	panelOptSel    map[int]map[int]bool            // askuser panel: selected option indices per question
	panelOptCursor map[int]int                     // askuser panel: highlighted option index per question
	panelAnswerTA  textarea.Model                  // askuser panel: free-input editor (no-options mode)
	panelOtherTI   textinput.Model                 // askuser panel: single-line Other input
	panelSchema    []SettingDefinition             // settings panel: schema copy
	panelValues    map[string]string               // settings panel: current values
	panelOnSubmit  func(values map[string]string)  // callback on settings submit
	panelOnAnswer  func(answers map[string]string) // callback on askuser answers (key=index, value=answer)
	panelOnCancel  func()                          // callback on cancel

	// --- Bg Tasks Panel ---
	panelBgTasks    []*tools.BackgroundTask // cached task list
	panelBgCursor   int                     // selected task index
	panelBgViewing  bool                    // true = viewing log of selected task
	panelBgScroll   int                     // log view scroll offset
	panelBgLogLines []string                // cached log lines for viewing

	// --- §13 Update Check ---
	updateNotice   *version.UpdateInfo // nil=nothing, non-nil=show notice
	checkingUpdate bool                // true while /update is in progress

	// --- §14 Splash 画面 ---
	splashDone  bool // true = splash 动画结束，进入正常界面
	splashFrame int  // 当前 splash 动画帧索引

	// --- §16 Toast 通知 ---
	toast      string // 非空 = 显示 toast 通知
	toastIcon  string // toast 图标（✓/✗/ℹ 等）
	toastTimer bool   // true = toast 消除计时器已启动

	styles  cliStyles
	channel *CLIChannel // back-reference to owning channel (set during Start)
}

// cliMessage 单条消息
type cliMessage struct {
	role      string
	content   string
	timestamp time.Time
	isPartial bool
	// --- §1 增量渲染 ---
	rendered    string // 缓存的渲染结果（ANSI 字符串）
	dirty       bool   // 是否需要重新渲染
	renderWidth int    // 渲染时的终端宽度（用于 resize 失效检测）

	// --- §2 工具可视化 ---
	tools      []CLIToolProgress      // 扁平化工具列表（兼容旧逻辑）
	iterations []cliIterationSnapshot // 按迭代分组的快照（优先使用）

	// --- §19 消息折叠 ---
	collapsed bool // 长消息是否折叠（仅对完成的 assistant 消息生效）
}

// newCLIModel 创建 CLI model
func newCLIModel() *cliModel {
	ta := textarea.New()
	ta.Placeholder = "Enter send · Ctrl+J newline · /help"
	ta.Focus()
	ta.SetWidth(76)
	ta.SetHeight(3)
	ta.CharLimit = 0
	ta.Prompt = "> "
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Info))
	ta.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle() // no background — let terminal bg show through
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.FocusedStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.BlurredStyle.Text = lipgloss.NewStyle()

	// Enter = send, Ctrl+Enter/Ctrl+J = newline (Ctrl+Enter raw sequences vary by terminal)
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")

	vp := viewport.New(80, 20)

	// 禁用 viewport 的字母快捷键，避免和用户输入冲突
	// 只保留方向键翻页，鼠标滚轮（MouseWheelEnabled 默认已开启）
	vp.KeyMap.Up.SetKeys("up")
	vp.KeyMap.Down.SetKeys("down")
	vp.KeyMap.PageUp.SetKeys("pgup")
	vp.KeyMap.PageDown.SetKeys("pgdown")
	vp.KeyMap.HalfPageUp.SetKeys()
	vp.KeyMap.HalfPageDown.SetKeys()

	renderer := newGlamourRenderer(maxBubbleWidth(80) - 2)

	// Ticker
	tk := newAnimTicker(dotFrames, currentTheme.Warning)

	return &cliModel{
		viewport:        vp,
		textarea:        ta,
		ticker:          tk,
		messages:        make([]cliMessage, 0, cliMsgBufSize),
		styles:          buildStyles(80),
		renderer:        renderer,
		ready:           false,
		typing:          false,
		streamingMsgIdx: -1,
		progress:        nil,
		inputReady:      true,
		msgCollapsed:    make(map[int]bool),
	}
}

// SetMsgBus 设置消息总线（用于发送用户消息）
func (m *cliModel) SetMsgBus(msgBus *bus.MessageBus) {
	m.msgBus = msgBus
}

// ---------------------------------------------------------------------------
// Bubble Tea Messages (内部消息类型)
// ---------------------------------------------------------------------------

// cliOutboundMsg 从 agent 收到的消息
type cliOutboundMsg struct {
	msg bus.OutboundMessage
}

// cliProgressMsg 进度更新消息
type cliProgressMsg struct {
	payload *CLIProgressPayload
}

// cliTickMsg 定时刷新（用于流式输出动画）
type cliTickMsg struct{}

// cliTempStatusClearMsg 临时状态提示自动清除
type cliTempStatusClearMsg struct{}

// cliInjectedUserMsg 通知 CLI 有 user 消息被注入（如 bg task 完成通知）
type cliInjectedUserMsg struct {
	content string
}

// cliUpdateCheckMsg 更新检查结果消息
type cliUpdateCheckMsg struct {
	info *version.UpdateInfo
}

// isCtrlEnter 检测 Ctrl+Enter 按键。
// 终端对 Ctrl+Enter 没有统一标准，常见 raw sequences：
//   - CSI u 协议: \x1b[13;5u   (kitty, Ghostty, Windows Terminal)
//   - 旧格式:     \x1b[27;5;13~ (部分 xterm 变体)
//
// 注意：Bubble Tea 不识别这些序列，会作为 unknownCSISequenceMsg 传递，
// 其 String() 格式为 "?CSI[49 51 59 53 117]?"（%+v 对 []byte 输出字节值数组）。
// 因此需要同时匹配 KeyMsg 和 unknownCSISequenceMsg 的字符串表示。
func isCtrlEnter(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	// CSI u 协议: \x1b[13;5u → "?CSI[49 51 59 53 117]?" 或 KeyRunes "\x1b[13;5u"
	// 旧格式:     \x1b[27;5;13~ → "?CSI[50 55 59 53 59 49 51 126]?" 或 KeyRunes "\x1b[27;5;13~"
	return s == "?CSI[49 51 59 53 117]?" || s == "\x1b[13;5u" ||
		s == "?CSI[50 55 59 53 59 49 51 126]?" || s == "\x1b[27;5;13~"
}

// isCtrlO 检测 Ctrl+O 按键（部分终端发送 CSI u 序列，Bubble Tea 无法识别）。
// Ctrl+O = ASCII 15, CSI u 协议: \x1b[15;5u → "?CSI[49 53 59 53 117]?"
func isCtrlO(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	return s == "?CSI[49 53 59 53 117]?" || s == "\x1b[15;5u"
}

// isCtrlJ detects Ctrl+J (newline). Ctrl+J = ASCII 10.
// CSI u protocol: \x1b[10;5u → "?CSI[49 48 59 53 117]?"
func isCtrlJ(msg tea.Msg) bool {
	s := fmt.Sprintf("%v", msg)
	return s == "?CSI[49 48 59 53 117]?" || s == "\x1b[10;5u" || s == "ctrl+j"
}

// ---------------------------------------------------------------------------
// Bubble Tea Interface Implementation
// ---------------------------------------------------------------------------

// Init 初始化 — 启动 splash 画面动画
func (m *cliModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.splashTick(0))
}

// splashTick 生成启动画面动画的 tick 命令
func (m *cliModel) splashTick(frame int) tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg {
		return splashTickMsg{frame: frame + 1}
	})
}

// Update 处理消息
func (m *cliModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	// §8 Tab 补全：记录输入内容变化以重置补全状态
	prevText := m.textarea.Value()

	wasTyping := m.typing

	// 主题变更通知：重建样式缓存 + glamour 渲染器
	select {
	case <-themeChangeCh:
		if m.width > 4 {
			m.renderer = newGlamourRenderer(m.width - 4)
		}
		// §20 重建样式缓存
		m.styles = buildStyles(m.width)
		// 刷新 textarea 样式（初始化时一次性绑定，theme 切换后需重建）
		m.textarea.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Info))
		m.textarea.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))
		m.textarea.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
		// 刷新 ticker 颜色
		m.ticker.style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Warning))
		m.renderCacheValid = false
		for i := range m.messages {
			m.messages[i].dirty = true
		}
		m.updateViewportContent()
	default:
	}

	// §12 Panel mode: intercept all key events when panel is active
	if key, ok := msg.(tea.KeyMsg); ok && m.panelMode != "" {
		handled, newModel, cmd := m.updatePanel(key)
		if handled {
			return newModel, cmd
		}
	}

	// Home/End 跳顶部/底部
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "home":
			m.viewport.GotoTop()
			return m, nil
		case "end":
			m.viewport.GotoBottom()
			m.newContentHint = false
			return m, nil
		}
	}

	// Ctrl+Enter 换行（终端发送的 raw sequence 不统一，需手动检测）
	if isCtrlEnter(msg) {
		m.textarea.InsertString("\n")
		return m, nil
	}

	// Ctrl+O 切换 tool summary 展开/折叠（CSI u 协议兼容层，kitty/Ghostty 等）
	if isCtrlO(msg) {
		m.toolSummaryExpanded = !m.toolSummaryExpanded
		m.renderCacheValid = false
		m.cachedHistory = ""
		for i := range m.messages {
			m.messages[i].dirty = true
		}
		m.updateViewportContent()
		return m, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// §9 Ctrl+K 确认模式：必须在 switch msg.Type 之前拦截所有按键
		if m.confirmDelete > 0 {
			groups := visibleMsgGroupIndices(m.messages)
			switch msg.String() {
			case "y", "Y":
				// 确认删除：根据 group 索引截断
				if m.confirmDelete > len(groups) {
					m.confirmDelete = len(groups)
				}
				cutIdx := groups[len(groups)-m.confirmDelete]
				m.messages = m.messages[:cutIdx]
				m.confirmDelete = 0
				m.renderCacheValid = false
				m.cachedHistory = ""
				m.updateViewportContent()
				return m, nil
			case "n", "N":
				// 取消删除
				m.confirmDelete = 0
				m.renderCacheValid = false
				m.updateViewportContent()
				return m, nil
			default:
				// 检查数字键（调整删除数量）
				if msg.Type == tea.KeyRunes {
					runes := msg.Runes
					if len(runes) == 1 && runes[0] >= '1' && runes[0] <= '9' {
						newDel := int(runes[0] - '0')
						if newDel > len(groups) {
							newDel = len(groups)
						}
						m.confirmDelete = newDel
						m.renderCacheValid = false
						m.updateViewportContent()
						return m, nil
					}
				}
				// 其他键也取消（包括 Esc）
				m.confirmDelete = 0
				m.renderCacheValid = false
				m.updateViewportContent()
				return m, nil
			}
		}

		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			// Ctrl+C / Esc：有迭代时中止，无迭代时清空输入
			if m.typing {
				if m.msgBus != nil {
					m.msgBus.Inbound <- bus.InboundMessage{
						Channel:    cliChannelName,
						SenderID:   cliSenderID,
						ChatID:     m.chatID,
						ChatType:   "p2p",
						Content:    "/cancel",
						SenderName: "CLI User",
						Time:       time.Now(),
						RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
					}
				}
				m.messages = append(m.messages, cliMessage{
					role:      "system",
					content:   "已发送取消请求",
					timestamp: time.Now(),
					dirty:     true,
				})
				m.updateViewportContent()
				return m, tea.Batch(cmds...)
			}
			// 非处理状态：清空输入
			if m.textarea.Value() != "" {
				m.textarea.Reset()
			}
			return m, nil

		case tea.KeyUp:
			// ↑ with bg tasks running + empty input → open bg tasks panel
			if m.bgTaskCount > 0 && m.textarea.Value() == "" && m.inputReady {
				m.openBgTasksPanel()
				return m, nil
			}

		case tea.KeyEnter:
			// Enter 发送消息
			if !m.inputReady {
				if m.textarea.Value() != "" {
					m.tempStatus = "... waiting for previous operation to complete..."
					return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return cliTempStatusClearMsg{} })
				}
				return m, nil
			}
			// §8b @ 模式：Enter 进入目录或确认文件
			if m.fileCompActive && len(m.fileCompletions) > 0 {
				selected := m.fileCompletions[m.fileCompIdx]
				input := m.textarea.Value()
				_, prefix := detectAtPrefix(input)
				atStart := len(input) - len(prefix) - 1
				if isDir(selected) {
					// 目录：进入下一层，手动触发 glob
					newInput := input[:atStart] + "@" + selected + "/"
					m.textarea.SetValue(newInput)
					m.fileCompActive = false
					m.populateFileCompletions(selected + "/")
				} else {
					// 文件：加空格退出 @ 模式
					newInput := input[:atStart] + "@" + selected + " "
					m.textarea.SetValue(newInput)
					m.fileCompActive = false
					m.fileCompletions = nil
					m.fileCompIdx = 0
				}
				return m, nil
			}
			content := strings.TrimSpace(m.textarea.Value())
			if content != "" {
				if m.allTodosDone() {
					m.todos = nil
					m.todosDoneCleared = true
				}
				m.sendMessage(content)
				m.textarea.Reset()
				m.viewport.GotoBottom()
				m.newContentHint = false
			}
			if m.typing {
				cmds = append(cmds, tickCmd())
			}
			// Kick off ticker chain when processing just started
			if m.typing && !wasTyping {
				cmds = append(cmds, tickerCmd())
			}
			return m, tea.Batch(cmds...)

		case tea.KeyTab:
			// §8 Tab 命令补全
			m.handleTabComplete()
			return m, nil

		case tea.KeyCtrlK:
			// §9 Ctrl+K 上下文编辑（按可见消息组计数，tool_summary 合并到 assistant）
			if !m.typing && len(m.messages) > 0 {
				groups := visibleMsgGroupIndices(m.messages)
				defaultDel := 2
				if defaultDel > len(groups) {
					defaultDel = len(groups)
				}
				m.confirmDelete = defaultDel
				m.renderCacheValid = false
				m.updateViewportContent()
			} else if !m.typing {
				m.tempStatus = "[!] no messages to delete"
				return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return cliTempStatusClearMsg{} })
			}
			return m, nil

		case tea.KeyCtrlO:
			// §11 Ctrl+O 切换 tool summary 展开/折叠（兼容非 CSI-u 终端）
			m.toolSummaryExpanded = !m.toolSummaryExpanded
			m.renderCacheValid = false
			m.cachedHistory = ""
			for i := range m.messages {
				m.messages[i].dirty = true
			}
			m.updateViewportContent()
			return m, nil

		case tea.KeyCtrlE:
			// §19 Ctrl+E 切换最后一条 assistant 消息的折叠/展开
			if !m.typing {
				lastAssistant := -1
				for i := len(m.messages) - 1; i >= 0; i-- {
					if m.messages[i].role == "assistant" && !m.messages[i].isPartial {
						lastAssistant = i
						break
					}
				}
				if lastAssistant >= 0 {
					m.messages[lastAssistant].collapsed = !m.messages[lastAssistant].collapsed
					m.renderCacheValid = false
					for i := range m.messages {
						m.messages[i].dirty = true
					}
					m.updateViewportContent()
				}
			}
			return m, nil

		} // end switch msg.Type

	case tea.WindowSizeMsg:
		// 窗口大小变化 - 动态调整布局
		m.handleResize(msg.Width, msg.Height)

	case cliOutboundMsg:
		// 收到 agent 回复
		m.handleAgentMessage(msg.msg)

	case cliProgressMsg:
		prev := m.progress
		m.progress = msg.payload
		// Update bg task count from callback
		if m.bgTaskCountFn != nil {
			m.bgTaskCount = m.bgTaskCountFn()
		}
		if msg.payload != nil {
			// Sync todo items from progress event
			if len(msg.payload.Todos) > 0 {
				allDone := true
				for _, t := range msg.payload.Todos {
					if !t.Done {
						allDone = false
						break
					}
				}
				if m.todosDoneCleared && allDone {
					// Already cleared by user input; don't re-accept stale all-done list
				} else {
					m.todos = make([]CLITodoItem, len(msg.payload.Todos))
					copy(m.todos, msg.payload.Todos)
					m.todosDoneCleared = false
				}
			} else {
				m.todos = nil
			}
			// Detect iteration change: snapshot previous iteration into history
			if msg.payload.Iteration > m.lastSeenIteration && m.lastSeenIteration >= 0 && prev != nil {
				// Filter CompletedTools by Iteration field for the previous iteration
				var prevIterTools []CLIToolProgress
				for _, t := range prev.CompletedTools {
					if t.Iteration == m.lastSeenIteration {
						prevIterTools = append(prevIterTools, t)
					}
				}
				if len(prevIterTools) > 0 || prev.Thinking != "" {
					snap := cliIterationSnapshot{
						Iteration: m.lastSeenIteration,
						Thinking:  prev.Thinking,
						Tools:     prevIterTools,
					}
					m.iterationHistory = append(m.iterationHistory, snap)
				}
				// Clear lastCompletedTools to prevent stale tools from being
				// re-snapshotted when the final iteration is snapshotted in handleAgentMessage.
				m.lastCompletedTools = m.lastCompletedTools[:0]
			}
			m.lastSeenIteration = msg.payload.Iteration

			// §2 工具可视化：快照 CompletedTools 到独立字段
			// Only keep tools matching the current iteration to avoid cross-iteration leakage.
			if len(msg.payload.CompletedTools) > 0 {
				filtered := m.lastCompletedTools[:0]
				for _, t := range msg.payload.CompletedTools {
					if t.Iteration == msg.payload.Iteration {
						filtered = append(filtered, t)
					}
				}
				m.lastCompletedTools = filtered
			}
			if msg.payload.Phase == "done" {
				// Snapshot the final iteration before clearing progress.
				// This handles the case where PhaseDone arrives before
				// handleAgentMessage (e.g. agent error/cancel).
				// Skip if handleAgentMessage already processed (m.typing == false
				// means the reply arrived and cleaned up iteration state).
				if m.typing && m.lastSeenIteration >= 0 {
					alreadySnapped := false
					for _, s := range m.iterationHistory {
						if s.Iteration == m.lastSeenIteration {
							alreadySnapped = true
							break
						}
					}
					if !alreadySnapped {
						var finalTools []CLIToolProgress
						// Check progress.CompletedTools first (set by progressFinalizer)
						for _, t := range msg.payload.CompletedTools {
							if t.Iteration == m.lastSeenIteration {
								finalTools = append(finalTools, t)
							}
						}
						// Also include any from lastCompletedTools (race safety)
						for _, t := range m.lastCompletedTools {
							if t.Iteration == m.lastSeenIteration {
								dup := false
								for _, existing := range finalTools {
									if existing.Name == t.Name && existing.Label == t.Label {
										dup = true
										break
									}
								}
								if !dup {
									finalTools = append(finalTools, t)
								}
							}
						}
						if len(finalTools) > 0 {
							m.iterationHistory = append(m.iterationHistory, cliIterationSnapshot{
								Iteration: m.lastSeenIteration,
								Tools:     finalTools,
							})
						}
					}
					// Generate tool_summary if we have iteration history.
					// Append to end immediately so cancel/error cases (no handleAgentMessage)
					// still display the summary. handleAgentMessage will relocate it before
					// the assistant reply if one follows.
					if len(m.iterationHistory) > 0 {
						m.pendingToolSummary = &cliMessage{
							role:       "tool_summary",
							content:    "",
							timestamp:  time.Now(),
							iterations: append([]cliIterationSnapshot{}, m.iterationHistory...),
							dirty:      true,
						}
						m.messages = append(m.messages, *m.pendingToolSummary)
						m.renderCacheValid = false
					}
				}
				// Reset all iteration tracking state (always, even if handleAgentMessage ran first)
				m.lastCompletedTools = nil
				m.iterationHistory = nil
				m.lastSeenIteration = 0
				m.typingStartTime = time.Time{}
				m.todos = nil
				m.todosDoneCleared = false
				m.progress = nil
				m.typing = false
			}
		}
		m.updateViewportContent()

	case cliTickMsg:
		// Always refresh bg task count on tick so status bar updates immediately
		// when a bg task completes (even when no progress event is coming)
		if m.bgTaskCountFn != nil {
			m.bgTaskCount = m.bgTaskCountFn()
		}
		if m.typing || m.progress != nil {
			cmds = append(cmds, tickCmd())
			m.updateViewportContent()
		}

	case cliTempStatusClearMsg:
		m.tempStatus = ""

	case cliInjectedUserMsg:
		// Agent injected a user message (e.g. bg task completion notification).
		// Display it identically to a manually typed user message and start spinner.
		m.messages = append(m.messages, cliMessage{
			role:      "user",
			content:   msg.content,
			timestamp: time.Now(),
			dirty:     true,
		})
		m.typing = true
		m.inputReady = false
		m.resetProgressState()
		// Refresh bg task count on injection
		if m.bgTaskCountFn != nil {
			m.bgTaskCount = m.bgTaskCountFn()
		}
		m.renderCacheValid = false
		// §16 触发 toast 通知（后台任务完成提示）
		// 提取首行作为 toast 文本，避免内容过长
		firstLine := msg.content
		if idx := strings.Index(msg.content, "\n"); idx >= 0 {
			firstLine = msg.content[:idx]
		}
		if len([]rune(firstLine)) > 50 {
			firstLine = string([]rune(firstLine)[:47]) + "..."
		}
		// 检测是否为完成或失败消息
		icon := "ℹ"
		lower := strings.ToLower(firstLine)
		if strings.Contains(lower, "done") || strings.Contains(lower, "completed") || strings.Contains(lower, "完成") {
			icon = "✓"
		} else if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
			icon = "✗"
		}
		cmds = append(cmds, func() tea.Msg {
			return cliToastMsg{text: firstLine, icon: icon}
		})

	case cliUpdateCheckMsg:
		m.checkingUpdate = false
		if msg.info != nil {
			m.updateNotice = msg.info
			if msg.info.HasUpdate {
				content := fmt.Sprintf("发现新版本: %s → %s\n升级命令: curl -fsSL https://raw.githubusercontent.com/CjiW/xbot/master/scripts/install.sh | bash\n%s", msg.info.Current, msg.info.Latest, msg.info.URL)
				m.messages = append(m.messages, cliMessage{
					role:      "system",
					content:   content,
					timestamp: time.Now(),
					dirty:     true,
				})
				m.updateViewportContent()
			} else {
				content := fmt.Sprintf("当前版本 %s 已是最新", msg.info.Current)
				m.messages = append(m.messages, cliMessage{
					role:      "system",
					content:   content,
					timestamp: time.Now(),
					dirty:     true,
				})
				m.updateViewportContent()
			}
		} else {
			m.messages = append(m.messages, cliMessage{
				role:      "system",
				content:   "更新检查失败（网络超时或无法连接 GitHub API）",
				timestamp: time.Now(),
				dirty:     true,
			})
			m.updateViewportContent()
		}

	case tickerTickMsg:
		// Ticker tick: advance frame and trigger viewport refresh
		if m.typing || m.progress != nil {
			m.ticker.tick()
			cmds = append(cmds, tickerCmd())
			m.updateViewportContent()
		}

	case splashTickMsg:
		// §14 启动画面动画帧推进
		m.splashFrame = msg.frame
		if m.ready {
			// 初始化完成（收到 WindowSizeMsg），结束 splash
			m.splashDone = true
			return m, nil
		}
		// 持续动画（最多 ~3 秒 = 37 帧）
		if msg.frame >= 37 {
			m.splashDone = true
			return m, nil
		}
		cmds = append(cmds, m.splashTick(msg.frame))
		return m, tea.Batch(cmds...)

	case splashDoneMsg:
		// §14 启动画面结束确认
		m.splashDone = true

	case cliToastMsg:
		// §16 Toast 通知显示
		m.toast = msg.text
		m.toastIcon = msg.icon
		if !m.toastTimer {
			m.toastTimer = true
			cmds = append(cmds, tea.Tick(3*time.Second, func(time.Time) tea.Msg {
				return cliToastClearMsg{}
			}))
		}

	case cliToastClearMsg:
		// §16 Toast 通知自动清除
		m.toast = ""
		m.toastIcon = ""
		m.toastTimer = false
	}

	// Kick off ticker + tick chains when processing just started
	if m.typing && !wasTyping {
		cmds = append(cmds, tickerCmd(), tickCmd())
	}

	// 更新 viewport
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	// 更新 textarea
	m.textarea, cmd = m.textarea.Update(msg)
	cmds = append(cmds, cmd)

	// §8 Tab 补全：输入内容变化时重置补全状态
	newVal := m.textarea.Value()
	if newVal != prevText {
		m.completions = nil
		m.compIdx = 0
		m.fileCompActive = false
		// 用户手动输入：根据当前 @ prefix 重新 glob
		// 但如果 fileCompActive（Tab 循环中），不重新 glob
		if !m.fileCompActive {
			if ok, prefix := detectAtPrefix(newVal); ok {
				m.populateFileCompletions(prefix)
			} else {
				m.fileCompletions = nil
				m.fileCompIdx = 0
			}
		}
	}

	// 检查是否需要退出
	if m.shouldQuit {
		return m, tea.Quit
	}

	return m, tea.Batch(cmds...)
}

// handleResize 处理窗口大小变化
func (m *cliModel) handleResize(width, height int) {
	m.width = width
	m.height = height

	// §20 重建样式缓存
	m.styles = buildStyles(width)

	// Layout: titleBar(1) + viewport + separator(1) + status(1) + inputBox(5) + footer(1)
	reservedLines := 9
	// §20b 小终端适配：极小窗口下动态缩减布局
	if height < 12 {
		reservedLines = 7
	}
	if height < 8 {
		reservedLines = 5
	}
	if height < 5 {
		reservedLines = 4
	}
	viewportHeight := height - reservedLines
	if viewportHeight < 3 {
		viewportHeight = 3
	}
	m.viewport.Width = width
	m.viewport.Height = viewportHeight

	// inputBoxStyle uses Width(width-4) for content, Padding(0,1) adds 2, Border adds 2.
	// textarea must match the content width exactly.
	iw := width - 4
	if iw < 10 {
		iw = 10
	}
	m.textarea.SetWidth(iw)

	// Glamour word-wrap must match viewport width so that lines
	// don't get re-wrapped by lipgloss (which would lose the margin).
	if width > 4 {
		m.renderer = newGlamourRenderer(width - 4)
	}

	if !m.ready {
		m.ready = true
	}

	// §1 增量渲染：resize 后缓存全部失效
	m.renderCacheValid = false
	for i := range m.messages {
		m.messages[i].dirty = true
	}

	// 更新内容（保持用户滚动位置）
	wasAtBottom := m.viewport.AtBottom()
	m.updateViewportContent()
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// calculateProgressHeight returns 0 — progress is now rendered inside the viewport.

// panelWidth returns a width suitable for panel textareas,
// adapting to the current terminal width (with sensible bounds).
func (m *cliModel) panelWidth(want int) int {
	maxW := m.width - 8 // room for panel border + padding
	if want > maxW {
		return maxW
	}
	if want < 30 {
		return 30
	}
	return want
}

// renderCompletionsHint returns the dynamic border color and completions hint string
// based on the current input content (slash commands, @ file references, etc.).
func (m *cliModel) renderCompletionsHint(inputValue string) (borderColor lipgloss.Color, hint string) {
	borderColor = lipgloss.Color(currentTheme.Accent)

	if strings.HasPrefix(inputValue, "!") {
		borderColor = lipgloss.Color(currentTheme.Error)
		return
	}

	if strings.HasPrefix(inputValue, "/") {
		borderColor = lipgloss.Color(currentTheme.Success)
		if len(m.completions) > 0 {
			parts := make([]string, len(m.completions))
			for i, c := range m.completions {
				if i == m.compIdx {
					parts[i] = lipgloss.NewStyle().
						Bold(true).Underline(true).
						Foreground(lipgloss.Color(currentTheme.Success)).
						Render(c)
				} else {
					parts[i] = lipgloss.NewStyle().
						Foreground(lipgloss.Color(currentTheme.Success)).
						Render(c)
				}
			}
			hint = lipgloss.NewStyle().Padding(0, 1).Render(strings.Join(parts, " · "))
		} else {
			var matches []string
			for _, cmd := range cliCommands {
				if strings.HasPrefix(cmd, inputValue) {
					matches = append(matches, cmd)
				}
			}
			if len(matches) > 0 {
				hint = lipgloss.NewStyle().
					Foreground(lipgloss.Color(currentTheme.Success)).
					Padding(0, 1).
					Render("[Tab] " + strings.Join(matches, " · "))
			}
		}
		return
	}

	// §20c @ 文件引用补全（带目录/文件图标区分 + 截断）
	rawInput := m.textarea.Value()
	if ok, _ := detectAtPrefix(rawInput); ok {
		borderColor = lipgloss.Color(currentTheme.Info)
		if len(m.fileCompletions) > 0 {
			parts := make([]string, len(m.fileCompletions))
			for i, c := range m.fileCompletions {
				base := filepath.Base(c)
				dir := isDir(c)
				if dir {
					base += "/"
				}
				// 截断过长文件名
				if utf8.RuneCountInString(base) > 20 {
					runes := []rune(base)
					base = string(runes[:18]) + "…"
				}
				icon := "📄 "
				if dir {
					icon = "📁 "
				}
				display := icon + base
				if i == m.fileCompIdx {
					parts[i] = m.styles.FileCompSel.Render(display)
				} else {
					parts[i] = m.styles.FileCompFile.Render(display)
				}
			}
			hint = m.styles.TextMutedSt.Padding(0, 1).
				Render("[Tab] " + strings.Join(parts, " · "))
		} else {
			hint = m.styles.TextMutedSt.Padding(0, 1).
				Render("[Tab] 无匹配文件")
		}
		return
	}

	return
}
func (m *cliModel) calculateProgressHeight() int {
	return 0
}

// View 渲染界面
func (m *cliModel) View() string {
	// §14 启动画面：品牌展示动画（~2.4 秒后自动消失）
	if !m.splashDone {
		return m.renderSplash()
	}

	if !m.ready {
		return "\n  初始化中..."
	}

	// ========== 样式定义 ==========

	// 标题栏：纯 ASCII，避免 emoji 导致宽度误算
	titleLeft := m.titleText()
	// 标题栏右侧快捷键提示：紧凑的点分隔，比 | 更柔和
	titleRight := "Enter send · Ctrl+J newline · /help"
	if m.updateNotice != nil && m.updateNotice.HasUpdate {
		titleRight = fmt.Sprintf("%s→%s · /update · /help", m.updateNotice.Current, m.updateNotice.Latest)
	}
	titlePad := m.width - lipgloss.Width(titleLeft) - lipgloss.Width(titleRight)
	if titlePad < 1 {
		titlePad = 1
	}
	titleBar := lipgloss.NewStyle().
		Background(lipgloss.Color(currentTheme.Border)).
		Foreground(lipgloss.Color(currentTheme.TitleText)).
		Bold(true).
		Width(m.width).
		Render(titleLeft + strings.Repeat(" ", titlePad) + titleRight)

		// 输入框样式：根据输入内容动态设置边框颜色
		// ! 开头 → 错误色，/ 开头 → 成功色，默认 → 主题强调色
	inputValue := strings.TrimSpace(m.textarea.Value())
	borderColor, completionsHint := m.renderCompletionsHint(inputValue)

	inputBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(m.width - 4)

	inputArea := m.textarea.View()

	// 多行输入行数指示（§17）：当输入超过 1 行时，右下角显示行数
	lineCount := strings.Count(m.textarea.Value(), "\n") + 1
	if lineCount > 1 && m.inputReady {
		lineHint := lipgloss.NewStyle().
			Foreground(lipgloss.Color(currentTheme.TextMuted)).
			Faint(true).
			Render(fmt.Sprintf("Ln %d", lineCount))
		inputArea += " " + lineHint + "\n"
	}

	// 状态栏样式
	readyStatusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Success)).
		Bold(true).
		Padding(0, 1)

	// §20 使用缓存样式
	thinkingStatusStyle := m.styles.ThinkingSt

	// §20 进度样式 → 缓存
	progressStyle := m.styles.Progress
	toolStyle := m.styles.Tool

	// ========== 渲染各部分 ==========
	// 分隔线：精致的点线交替图案，比纯虚线更有设计感
	sepWidth := m.width
	if sepWidth < 4 {
		sepWidth = 4
	}
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.BarEmpty)).
		Render("┈" + strings.Repeat("┈", sepWidth-2))

	// 输入区
	input := inputBoxStyle.Render(inputArea)

	// §9 Ctrl+K 确认模式提示
	if m.confirmDelete > 0 {
		warningStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color(currentTheme.Warning)).
			Bold(true).
			Padding(0, 1)
		warningText := warningStyle.Render(fmt.Sprintf("[!] Ctrl+K: delete last %d messages? (y/N, number to adjust)", m.confirmDelete))
		return fmt.Sprintf(
			"%s\n%s\n%s\n%s\n%s",
			titleBar,
			m.viewport.View(),
			separator,
			warningText,
			input,
		)
	}

	// 动态 placeholder：处理中 vs 就绪（就绪态使用轮换提示）
	if m.typing {
		m.textarea.Placeholder = "[Processing...] (Ctrl+C to cancel)"
		m.textarea.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
	} else {
		m.textarea.Placeholder = pickIdlePlaceholder()
		m.textarea.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
	}

	// 进度状态栏
	var status string
	if m.typing || m.progress != nil {
		// 显示 spinner + 进度信息
		status = thinkingStatusStyle.Render(m.renderProgressStatus(progressStyle, toolStyle))
	} else if m.checkingUpdate {
		status = thinkingStatusStyle.Render("⟳ checking for updates...")
	} else if completionsHint != "" {
		// 显示补全候选提示
		status = completionsHint
	} else {
		// 就绪态：显示消息计数 + 当前模型（如果有覆盖）
		readyParts := []string{"● ready"}
		// 消息计数
		msgCount := len(m.messages)
		if msgCount > 0 {
			readyParts = append(readyParts, fmt.Sprintf("%d msg%s", msgCount, func() string {
				if msgCount > 1 {
					return "s"
				}
				return ""
			}()))
		}
		// 模型名称（如果用户通过 /settings 覆盖了）
		if m.channel != nil {
			m.channel.configMu.RLock()
			modelName := m.channel.modelOverride
			m.channel.configMu.RUnlock()
			if modelName != "" {
				readyParts = append(readyParts, modelName)
			}
		}
		status = readyStatusStyle.Render(strings.Join(readyParts, " · "))
	}
	// 临时状态提示（自动过期）
	if m.tempStatus != "" {
		ts := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Warning)).Render(m.tempStatus)
		if status != "" {
			status += "  " + ts
		} else {
			status = ts
		}
	}
	// 新消息提示：用户上滚且有新内容时显示
	if m.newContentHint {
		hint := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Info)).Render("↓ new content")
		if status != "" {
			status += "  " + hint
		} else {
			status = hint
		}
	}
	// Background task indicator
	if m.bgTaskCount > 0 {
		bgHint := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Warning)).Render(
			fmt.Sprintf("[bg: %d task%s running — ↑ to manage]", m.bgTaskCount, func() string {
				if m.bgTaskCount > 1 {
					return "s"
				}
				return ""
			}()))
		if status != "" {
			status += "  " + bgHint
		} else {
			status = bgHint
		}
	}

	// §16 Toast 通知渲染
	toastStr := m.renderToast()

	// 组装界面
	// 组装界面
	// §12 Panel mode: render panel overlay instead of normal input
	if m.panelMode != "" {
		panel := m.viewPanel()
		// Panel 模式下也显示 footer 快捷键提示
		panelFooter := m.renderFooter()
		return fmt.Sprintf(
			"%s\n%s\n%s%s%s",
			titleBar,
			m.viewport.View(),
			panel,
			panelFooter,
			toastStr,
		)
	}

	todoBar := m.renderTodoBar()
	if todoBar != "" {
		return fmt.Sprintf(
			"%s\n%s\n%s\n%s\n%s\n%s%s",
			titleBar,
			m.viewport.View(),
			separator,
			status,
			todoBar,
			input,
			toastStr,
		)
	}
	// 底部快捷键提示条（第 4 轮：激活已定义但未使用的 renderFooter）
	footer := m.renderFooter()
	if footer != "" {
		return fmt.Sprintf(
			"%s\n%s\n%s\n%s\n%s\n%s%s",
			titleBar,
			m.viewport.View(),
			separator,
			status,
			footer,
			input,
			toastStr,
		)
	}
	return fmt.Sprintf(
		"%s\n%s\n%s\n%s\n%s%s",
		titleBar,
		m.viewport.View(),
		separator,
		status,
		input,
		toastStr,
	)
}

// allTodosDone returns true when todos exist and every item is marked done.
func (m *cliModel) allTodosDone() bool {
	if len(m.todos) == 0 {
		return false
	}
	for _, t := range m.todos {
		if !t.Done {
			return false
		}
	}
	return true
}

// renderTodoBar renders a compact TODO progress bar between status and input.
// Returns empty string when no todos are active.
func (m *cliModel) renderTodoBar() string {
	if len(m.todos) == 0 {
		return ""
	}

	done := 0
	total := len(m.todos)
	for _, item := range m.todos {
		if item.Done {
			done++
		}
	}

	// All done — still show bar (cleared on next user message)
	// if done == total { return "" }

	// Progress bar: filled portion
	barWidth := 20
	filled := 0
	if total > 0 {
		filled = done * barWidth / total
	}

	barFilled := strings.Repeat("█", filled)
	barEmpty := strings.Repeat("░", barWidth-filled)

	// §20
	s := &m.styles
	todoLabelSt := s.TodoLabel
	todoBarFilledSt := s.TodoFilled
	todoBarEmptySt := s.TodoEmpty
	todoDoneSt := s.TodoDone
	todoPendingSt := s.TodoPending

	var sb strings.Builder
	// Header: TODO label + count + progress bar
	sb.WriteString(todoLabelSt.Render(" TODO "))
	fmt.Fprintf(&sb, "%d/%d ", done, total)
	sb.WriteString(todoBarFilledSt.Render(barFilled))
	sb.WriteString(todoBarEmptySt.Render(barEmpty))
	sb.WriteString("\n")
	// Items
	for i, item := range m.todos {
		text := item.Text
		if utf8.RuneCountInString(text) > 60 {
			text = string([]rune(text)[:59]) + "…"
		}
		if item.Done {
			sb.WriteString("  ")
			sb.WriteString(todoDoneSt.Render("✓"))
			sb.WriteString(" ")
			sb.WriteString(todoPendingSt.Render(text))
		} else {
			sb.WriteString("  ")
			sb.WriteString(todoLabelSt.Render("○"))
			sb.WriteString(" ")
			sb.WriteString(todoPendingSt.Render(text))
		}
		if i < len(m.todos)-1 {
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// titleText 生成标题栏文字（纯 ASCII，避免 emoji 宽度不一致）
func (m *cliModel) titleText() string {
	if m.workDir != "" {
		return fmt.Sprintf(" xbot CLI [%s]", filepath.Base(m.workDir))
	}
	return " xbot CLI"
}

// ---------------------------------------------------------------------------
// §14 启动画面 (Splash Screen)
// ---------------------------------------------------------------------------

// xbotLogo — ASCII art logo（纯 ASCII，兼容所有终端）
var xbotLogo = []string{
	"  ╦ ╦╔═╗╔╗   ╔═╗╦ ╦╔═╗╦  ╦╔═╗╦  ╦",
	"  ║║║║╣ ╠╩╗  ║  ╠═╣║╣ ║  ║║╣ ╚╗╔╝",
	"  ╚╩╝╚═╝╚═╝  ╚═╝╩ ╩╚═╝╩═╝╩╚═╝ ╚╝",
}

// renderSplash 渲染启动画面 — 品牌 logo + 版本号 + 加载动画
func (m *cliModel) renderSplash() string {
	// 中心化计算
	screenW := m.width
	if screenW < 40 {
		screenW = 40
	}
	screenH := m.height
	if screenH < 10 {
		screenH = 10
	}

	// §20 使用缓存样式
	logoStyle := m.styles.Accent.Bold(true)
	versionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextSecondary))
	descStyle := m.styles.TextMutedSt
	loadingStyle := m.styles.WarningSt

	// 组装 splash 内容
	var lines []string
	for _, line := range xbotLogo {
		// 居中 logo
		logoW := lipgloss.Width(logoStyle.Render(line))
		pad := (screenW - logoW) / 2
		if pad < 0 {
			pad = 0
		}
		lines = append(lines, strings.Repeat(" ", pad)+logoStyle.Render(line))
	}

	// 空行
	lines = append(lines, "")

	// 版本号居中
	versionText := versionStyle.Render(fmt.Sprintf("xbot %s · %s", version.Version, version.Commit))
	vW := lipgloss.Width(versionText)
	vPad := (screenW - vW) / 2
	if vPad < 0 {
		vPad = 0
	}
	lines = append(lines, strings.Repeat(" ", vPad)+versionText)

	// 描述居中
	descText := descStyle.Render("AI-powered terminal agent")
	dW := lipgloss.Width(descText)
	dPad := (screenW - dW) / 2
	if dPad < 0 {
		dPad = 0
	}
	lines = append(lines, strings.Repeat(" ", dPad)+descText)

	// 空行
	lines = append(lines, "")

	// 加载动画
	frame := splashFrames[m.splashFrame%len(splashFrames)]
	loadingText := loadingStyle.Render("  " + frame + "  initializing...")
	lW := lipgloss.Width(loadingText)
	lPad := (screenW - lW) / 2
	if lPad < 0 {
		lPad = 0
	}
	lines = append(lines, strings.Repeat(" ", lPad)+loadingText)

	// 垂直居中
	emptyLinesBefore := (screenH - len(lines)) / 2
	if emptyLinesBefore < 2 {
		emptyLinesBefore = 2
	}

	var sb strings.Builder
	for i := 0; i < emptyLinesBefore; i++ {
		sb.WriteString("\n")
	}
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	return sb.String()
}

// ---------------------------------------------------------------------------
// §15 底部快捷键提示条 (Footer Bar)
// ---------------------------------------------------------------------------

// renderFooter 渲染底部快捷键提示条。
// 根据当前状态动态显示最相关的快捷键，避免信息过载。
func (m *cliModel) renderFooter() string {
	// 收集当前上下文最相关的快捷键提示
	var hints []string

	if m.typing {
		// 处理中：显示取消快捷键
		hints = append(hints, ctrlKey("c", "cancel"))
	} else {
		// 就绪态：显示核心快捷键
		if m.textarea.Value() == "" {
			hints = append(hints, ctrlKey("k", "delete"), keyHint("/", "commands"), keyHint("tab", "complete"))
			if m.bgTaskCount > 0 {
				hints = append(hints, keyHint("↑", "bg tasks"))
			}
		} else {
			hints = append(hints, ctrlKey("j", "newline"), keyHint("tab", "complete"), ctrlKey("k", "delete"))
		}
	}

	if len(hints) == 0 {
		return ""
	}

	// §20 使用缓存样式
	footerText := strings.Join(hints, "  ")
	helpHint := m.styles.TextMutedSt.Render("/help")
	footerText = padBetween(footerText, helpHint, m.width)

	return m.styles.Footer.Width(m.width).Render(footerText)
}

// ctrlKey 渲染 Ctrl+X 快捷键标签（灰色键帽 + 彩色描述）
func ctrlKey(key string, desc string) string {
	k := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextMuted)).
		Bold(true).
		Render("Ctrl+" + key)
	d := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary)).
		Render(desc)
	return k + " " + d
}

// keyHint 渲染普通按键标签
func keyHint(key, desc string) string {
	k := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextMuted)).
		Bold(true).
		Render(key)
	d := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary)).
		Render(desc)
	return k + " " + d
}

// padBetween 在左右文本之间填充空格，使总宽度达到 width
func padBetween(left, right string, width int) string {
	w := lipgloss.Width(left) + lipgloss.Width(right)
	if w >= width {
		return left + " " + right
	}
	return left + strings.Repeat(" ", width-w) + right
}

// renderToast 渲染底部 Toast 通知条（§16）。
// Toast 用于短暂提示（如后台任务完成），3 秒后自动消失。
// 浮在界面最底部，使用 Surface 背景与主题保持一致。
func (m *cliModel) renderToast() string {
	if m.toast == "" {
		return ""
	}

	// 图标颜色：根据 icon 类型选择
	iconColor := currentTheme.Success
	switch m.toastIcon {
	case "✗", "⚠":
		iconColor = currentTheme.Error
	case "ℹ":
		iconColor = currentTheme.Info
	}

	// §20 使用缓存样式（iconSty 需动态颜色，保留）
	iconSty := lipgloss.NewStyle().Foreground(lipgloss.Color(iconColor)).Bold(true)
	toastContent := iconSty.Render(" "+m.toastIcon+" ") + " " + m.styles.ToastText.Render(m.toast)

	return "\n" + m.styles.ToastBg.Render(toastContent)
}

// renderProgressStatus renders a compact one-line status for the status bar.
func (m *cliModel) renderProgressStatus(progressStyle, toolStyle lipgloss.Style) string {
	s := &m.styles // §20
	var sb strings.Builder
	sb.WriteString(s.Progress.Render(m.ticker.view()))
	sb.WriteString(" ")

	if m.progress != nil {
		fmt.Fprintf(&sb, "#%d", m.progress.Iteration)

		// Show first active tool name
		hasActive := false
		for _, tool := range m.progress.ActiveTools {
			if tool.Status != "done" && tool.Status != "error" {
				hasActive = true
				label := tool.Label
				if label == "" {
					label = tool.Name
				}
				sb.WriteString(s.Tool.Render(" · " + label))
				break
			}
		}

		// Phase hint when no active tool
		if !hasActive {
			switch m.progress.Phase {
			case "thinking":
				sb.WriteString(" · " + pickVerb(m.ticker.ticks))
			case "compressing":
				sb.WriteString(" · compressing")
			case "retrying":
				sb.WriteString(" · retrying")
			default:
				if len(m.progress.CompletedTools) > 0 {
					sb.WriteString(" · done")
				}
			}
		}
	} else {
		sb.WriteString(pickVerb(m.ticker.ticks) + "...")
	}

	// Total elapsed
	if !m.typingStartTime.IsZero() {
		elapsed := time.Since(m.typingStartTime).Milliseconds()
		sb.WriteString(" · ")
		sb.WriteString(formatElapsed(elapsed))
	}

	// §18 Token 使用量显示
	if m.progress != nil && m.progress.TokenUsage != nil && m.progress.TokenUsage.TotalTokens > 0 {
		tu := m.progress.TokenUsage
		// §20 tokenStyle → s.TokenUsage
		sb.WriteString(" · ")
		sb.WriteString(s.TokenUsage.Render(formatTokenCount(tu)))
	}

	return sb.String()
}

// formatTokenCount 格式化 Token 使用量为紧凑字符串
func formatTokenCount(tu *CLITokenUsage) string {
	if tu.TotalTokens < 1000 {
		return fmt.Sprintf("tokens: %d", tu.TotalTokens)
	}
	parts := []string{}
	if tu.PromptTokens > 0 {
		parts = append(parts, fmt.Sprintf("↑%d", tu.PromptTokens))
	}
	if tu.CompletionTokens > 0 {
		parts = append(parts, fmt.Sprintf("↓%d", tu.CompletionTokens))
	}
	if len(parts) > 0 {
		return "tokens: " + strings.Join(parts, " ") + fmt.Sprintf(" = %d", tu.TotalTokens)
	}
	return fmt.Sprintf("tokens: %d", tu.TotalTokens)
}

// ---------------------------------------------------------------------------
// Helper Methods
// ---------------------------------------------------------------------------

// handleTabComplete 处理 Tab 补全（§8：/ 命令补全，§8b：@ 文件路径补全）
func (m *cliModel) handleTabComplete() {
	input := m.textarea.Value()

	// 检测 @ 文件引用补全（从输入末尾检测）
	atOk, atPrefix := detectAtPrefix(input)
	if atOk {
		m.handleFileTabComplete(input, atPrefix)
		return
	}

	// / 命令补全
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") {
		return
	}

	if len(m.completions) == 0 {
		for _, cmd := range cliCommands {
			if strings.HasPrefix(cmd, trimmed) {
				m.completions = append(m.completions, cmd)
			}
		}
		if len(m.completions) == 0 {
			return
		}
		m.compIdx = 0
	} else {
		m.compIdx = (m.compIdx + 1) % len(m.completions)
	}

	m.textarea.SetValue(m.completions[m.compIdx] + " ")
}

// detectAtPrefix 检测输入文本末尾是否有 @ 触发文件补全。
// ok=true 表示检测到 @（即使后面无字符也应触发 glob）。
// prefix 是 @ 之后到文本末尾的部分。
func detectAtPrefix(input string) (ok bool, prefix string) {
	if len(input) == 0 || input[len(input)-1] == ' ' {
		return false, ""
	}
	i := len(input) - 1
	for i >= 0 && input[i] != ' ' && input[i] != '@' {
		i--
	}
	if i < 0 || input[i] != '@' {
		return false, ""
	}
	if i > 0 && input[i-1] != ' ' {
		return false, ""
	}
	return true, input[i+1:]
}

// populateFileCompletions 根据 prefix 执行 glob 搜索并填充 fileCompletions
func (m *cliModel) populateFileCompletions(prefix string) {
	pattern := prefix
	if !strings.Contains(pattern, "*") {
		if strings.HasSuffix(pattern, "/") {
			pattern += "*"
		} else {
			pattern += "*"
		}
	}
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		m.fileCompletions = nil
		m.fileCompIdx = 0
		return
	}
	// 过滤隐藏文件（以 . 开头）
	filtered := matches[:0]
	for _, f := range matches {
		base := filepath.Base(f)
		if len(base) > 0 && base[0] != '.' {
			filtered = append(filtered, f)
		}
	}
	matches = filtered
	sort.Slice(matches, func(i, j int) bool {
		di, dj := isDir(matches[i]), isDir(matches[j])
		if di != dj {
			return di
		}
		return matches[i] < matches[j]
	})
	if len(matches) > 20 {
		matches = matches[:20]
	}
	m.fileCompletions = matches
	m.fileCompIdx = 0
}

// handleFileTabComplete 处理 @ 文件路径 Tab 补全
func (m *cliModel) handleFileTabComplete(input string, prefix string) {
	if !m.fileCompActive || len(m.fileCompletions) == 0 {
		// 首次 Tab 或候选被清空：glob 并进入循环模式
		m.populateFileCompletions(prefix)
		if len(m.fileCompletions) == 0 {
			return
		}
		m.fileCompActive = true
	} else {
		// 循环模式：切换到下一个候选
		m.fileCompIdx = (m.fileCompIdx + 1) % len(m.fileCompletions)
	}

	selected := m.fileCompletions[m.fileCompIdx]
	if isDir(selected) {
		selected += "/"
	}
	atStart := len(input) - len(prefix) - 1
	newInput := input[:atStart] + "@" + selected
	m.textarea.SetValue(newInput)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sendToAgent 发送命令到 agent，并添加用户消息到历史（§3 命令透传机制）
func (m *cliModel) sendToAgent(content string) {
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})
	if m.msgBus != nil {
		m.msgBus.Inbound <- bus.InboundMessage{
			Channel:    cliChannelName,
			SenderID:   cliSenderID,
			ChatID:     m.chatID,
			ChatType:   "p2p",
			Content:    content,
			SenderName: "CLI User",
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
			Metadata:   map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional},
		}
		m.typing = true
		m.inputReady = false
		m.resetProgressState()
	}
}

// sendMessage 发送用户消息
func (m *cliModel) sendMessage(content string) {
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "/") {
		m.handleSlashCommand(content)
		return
	}

	// 解析 @ 文件引用，提取文件路径
	media := parseFileReferences(content)

	// 添加用户消息到历史
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   content,
		timestamp: time.Now(),
		dirty:     true,
	})

	// 更新显示
	m.updateViewportContent()

	// 发送到消息总线
	if m.msgBus != nil {
		msg := bus.InboundMessage{
			Channel:    cliChannelName,
			SenderID:   cliSenderID,
			ChatID:     m.chatID,
			ChatType:   "p2p",
			Content:    content,
			Media:      media,
			SenderName: "CLI User",
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
			Metadata:   map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional},
		}
		m.msgBus.Inbound <- msg
		m.typing = true
		m.inputReady = false
		m.resetProgressState()
	}
}

// parseFileReferences 从用户消息中提取 @path 文件引用。
// 匹配 @ 后跟非空格字符的路径，验证文件存在后返回。
func parseFileReferences(content string) []string {
	var files []string
	seen := make(map[string]bool)
	for i := 0; i < len(content); i++ {
		if content[i] == '@' {
			// @ 必须在词首
			if i > 0 && content[i-1] != ' ' {
				continue
			}
			// 提取 @ 后的路径
			j := i + 1
			for j < len(content) && content[j] != ' ' {
				j++
			}
			path := content[i+1 : j]
			// 去掉末尾的 /
			path = strings.TrimRight(path, "/")
			if path != "" && !seen[path] {
				if _, err := os.Stat(path); err == nil {
					files = append(files, path)
					seen[path] = true
				}
			}
			i = j
		}
	}
	return files
}

// resetProgressState resets iteration tracking for a new agent turn.
func (m *cliModel) resetProgressState() {
	m.iterationHistory = nil
	m.lastSeenIteration = 0
	m.typingStartTime = time.Now()
}

// collectAllTools gathers all tools from iteration history into a flat slice.
func (m *cliModel) collectAllTools() []CLIToolProgress {
	var all []CLIToolProgress
	for _, snap := range m.iterationHistory {
		all = append(all, snap.Tools...)
	}
	return all
}

// handleSlashCommand 处理斜杠命令
func (m *cliModel) handleSlashCommand(cmd string) {
	cmd = strings.TrimSpace(cmd)
	// 提取命令部分（去掉参数）
	parts := strings.Fields(cmd)
	command := ""
	if len(parts) > 0 {
		command = strings.ToLower(parts[0])
	}

	switch command {
	// --- 本地命令 ---
	case "/cancel":
		if m.msgBus != nil {
			m.msgBus.Inbound <- bus.InboundMessage{
				Channel:    cliChannelName,
				SenderID:   cliSenderID,
				ChatID:     m.chatID,
				ChatType:   "p2p",
				Content:    "/cancel",
				SenderName: "CLI User",
				Time:       time.Now(),
				RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
			}
		}
		m.messages = append(m.messages, cliMessage{
			role:      "system",
			content:   "已发送取消请求",
			timestamp: time.Now(),
			dirty:     true,
		})

	case "/clear":
		m.messages = make([]cliMessage, 0, cliMsgBufSize)
		m.renderCacheValid = false
		m.cachedHistory = ""
		m.updateViewportContent()

	case "/settings":
		// Open interactive settings panel locally
		if m.channel != nil {
			schema := m.channel.SettingsSchema()
			if len(schema) == 0 {
				m.messages = append(m.messages, cliMessage{
					role:      "system",
					content:   "当前渠道没有可配置的设置项。",
					timestamp: time.Now(),
					dirty:     true,
				})
				m.updateViewportContent()
			} else {
				// Get current values: start from config, overlay with SettingsService
				currentValues := make(map[string]string)
				if m.channel.config.GetCurrentValues != nil {
					for k, v := range m.channel.config.GetCurrentValues() {
						currentValues[k] = v
					}
				}
				if m.channel.settingsSvc != nil {
					vals, err := m.channel.settingsSvc.GetSettings(cliChannelName, cliSenderID)
					if err == nil {
						for k, v := range vals {
							currentValues[k] = v
						}
					}
				}
				// Inject model list into combo options
				if m.channel.modelLister != nil {
					models := m.channel.modelLister.ListModels()
					for i, s := range schema {
						if s.Key == "llm_model" && len(models) > 0 {
							opts := make([]SettingOption, len(models))
							for j, m := range models {
								opts[j] = SettingOption{Label: m, Value: m}
							}
							schema[i].Options = opts
							break
						}
					}
				}
				m.openSettingsPanel(schema, currentValues, func(values map[string]string) {
					// Persist to SettingsService (SQLite)
					if m.channel.settingsSvc != nil {
						for k, v := range values {
							_ = m.channel.settingsSvc.SetSetting(cliChannelName, cliSenderID, k, v)
						}
					}
					// Apply settings: write config.json + update runtime state
					if m.channel.config.ApplySettings != nil {
						m.channel.config.ApplySettings(values)
					}
					// Apply theme immediately
					if theme, ok := values["theme"]; ok {
						ApplyTheme(theme)
						// Rebuild glamour renderer with new theme
						if m.width > 4 {
							m.renderer = newGlamourRenderer(m.width - 4)
						}
						m.renderCacheValid = false
						// Mark all messages dirty so fullRebuild re-renders with new colors
						for j := range m.messages {
							m.messages[j].dirty = true
						}
					}
					// Update live config overrides (model, base_url)
					if model, ok := values["llm_model"]; ok && model != "" {
						m.channel.UpdateConfig(model, values["llm_base_url"])
					}
					m.messages = append(m.messages, cliMessage{
						role:      "system",
						content:   "✅ 设置已保存",
						timestamp: time.Now(),
						dirty:     true,
					})
					m.updateViewportContent()
				})
			}
		}

	case "/setup":
		m.openSetupPanel()

	case "/update":
		if m.checkingUpdate {
			m.messages = append(m.messages, cliMessage{
				role:      "system",
				content:   "正在检查更新...",
				timestamp: time.Now(),
				dirty:     true,
			})
		} else {
			m.checkingUpdate = true
			m.updateNotice = nil
			if m.channel != nil {
				m.channel.CheckUpdateAsync()
			}
			m.messages = append(m.messages, cliMessage{
				role:      "system",
				content:   "正在检查更新...",
				timestamp: time.Now(),
				dirty:     true,
			})
			m.updateViewportContent()
		}

	case "/quit", "/exit":
		m.shouldQuit = true

	case "/help":
		helpContent := m.renderHelpPanel()
		m.messages = append(m.messages, cliMessage{
			role:      "system",
			content:   helpContent,
			timestamp: time.Now(),
			dirty:     true,
		})

	case "/compact":
		// 保留本地处理（system 消息样式），发送到 msgBus 但不作为用户气泡
		if m.msgBus != nil {
			m.msgBus.Inbound <- bus.InboundMessage{
				Channel:    cliChannelName,
				SenderID:   cliSenderID,
				ChatID:     m.chatID,
				ChatType:   "p2p",
				Content:    "/compact",
				SenderName: "CLI User",
				Time:       time.Now(),
				RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
			}
		}
		m.messages = append(m.messages, cliMessage{
			role:      "system",
			content:   "已发送上下文压缩请求",
			timestamp: time.Now(),
			dirty:     true,
		})

	// --- 透传命令（发送到 agent） ---
	case "/model":
		// /model <name> → /set-model <name>
		if len(parts) < 2 {
			m.messages = append(m.messages, cliMessage{
				role:      "system",
				content:   "用法: /model <模型名>\n使用 /models 查看可用模型",
				timestamp: time.Now(),
				dirty:     true,
			})
		} else {
			m.sendToAgent(fmt.Sprintf("/set-model %s", strings.Join(parts[1:], " ")))
		}

	case "/models":
		m.sendToAgent("/models")

	case "/context":
		m.sendToAgent(cmd) // 直接透传，agent 层会解析

	case "/new":
		m.sendToAgent("/new")

	case "/tasks":
		// /tasks — show running background tasks
		if m.bgTaskCountFn != nil {
			count := m.bgTaskCountFn()
			if count == 0 {
				m.messages = append(m.messages, cliMessage{
					role:      "system",
					content:   "No background tasks running.",
					timestamp: time.Now(),
					dirty:     true,
				})
			} else {
				// Get full task list from channel
				ch := m.channel
				if ch.bgTaskMgr != nil {
					tasks := tools.ListBgTasks(ch.bgTaskMgr, ch.bgSessionKey)
					m.messages = append(m.messages, cliMessage{
						role:      "system",
						content:   tasks,
						timestamp: time.Now(),
						dirty:     true,
					})
				}
			}
		} else {
			m.messages = append(m.messages, cliMessage{
				role:      "system",
				content:   "Background tasks not supported.",
				timestamp: time.Now(),
				dirty:     true,
			})
		}

	default:
		// 未知命令尝试透传到 agent（agent 层可能认识）
		m.sendToAgent(cmd)
	}

	m.updateViewportContent()
}

// handleAgentMessage 处理 agent 回复
func (m *cliModel) handleAgentMessage(msg bus.OutboundMessage) {
	content := msg.Content

	// 处理 __FEISHU_CARD__ 协议（简化显示）
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		content = ConvertFeishuCard(content)
	}

	if msg.IsPartial {
		// 流式输出：追加到当前消息
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// 追加到现有流式消息
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].dirty = true
		} else {
			// 创建新的流式消息
			m.streamingMsgIdx = len(m.messages)
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: true,
				dirty:     true,
			})
		}
	} else {
		// 完整消息
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// 更新流式消息为完整消息
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].isPartial = false
			m.messages[m.streamingMsgIdx].dirty = true
		} else {
			// 新增完整的 assistant 消息
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: false,
				dirty:     true,
			})
		}
		// 重置流式状态
		m.streamingMsgIdx = -1
		// 清除进度信息（保留 TODO，可跨 turn 存活）
		m.progress = nil

		// §12 AskUser panel: detect WaitingUser and open interactive panel
		if msg.WaitingUser {
			var items []askItem
			if msg.Metadata != nil {
				if qJSON := msg.Metadata["ask_questions"]; qJSON != "" {
					// Multi-question mode: parse questions array
					var qs []askQItem
					if json.Unmarshal([]byte(qJSON), &qs) == nil {
						for _, q := range qs {
							items = append(items, askItem{Question: q.Question, Options: q.Options})
						}
					}
				}
			}
			// Fallback: search message history for ❓ (legacy single-question format)
			if len(items) == 0 {
				for i := len(m.messages) - 1; i >= 0; i-- {
					if strings.HasPrefix(m.messages[i].content, "❓") {
						question := strings.TrimSpace(strings.TrimPrefix(m.messages[i].content, "❓"))
						m.messages = append(m.messages[:i], m.messages[i+1:]...)
						if question != "" {
							items = append(items, askItem{Question: question})
						}
						break
					}
				}
			}
			if len(items) > 0 {
				m.updateViewportContent()
				m.openAskUserPanel(items, func(answers map[string]string) {
					// Format answers as tool-call style message
					var parts []string
					for i, item := range items {
						key := fmt.Sprintf("q%d", i)
						ans := answers[key]
						parts = append(parts, fmt.Sprintf("Q: %s\nA: %s", item.Question, ans))
					}
					content := strings.Join(parts, "\n\n")
					// Send to agent as tool result replacement (not a new user message)
					if m.msgBus != nil {
						m.msgBus.Inbound <- bus.InboundMessage{
							Channel:    cliChannelName,
							SenderID:   cliSenderID,
							ChatID:     m.chatID,
							ChatType:   "p2p",
							Content:    content,
							SenderName: "CLI User",
							Time:       time.Now(),
							RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
							Metadata:   map[string]string{"ask_user_answered": "true"},
						}
					}
					// Render as tool call style (not user message)
					m.messages = append(m.messages, cliMessage{
						role:       "tool_summary",
						content:    "AskUser",
						timestamp:  time.Now(),
						dirty:      true,
						iterations: nil,
						tools: []CLIToolProgress{
							{
								Name:    "AskUser",
								Label:   fmt.Sprintf("asked %d question(s)", len(items)),
								Status:  "completed",
								Elapsed: 0,
							},
						},
					})
					// Show answers as system message
					var answerParts []string
					for i, item := range items {
						key := fmt.Sprintf("q%d", i)
						ans := answers[key]
						answerParts = append(answerParts, fmt.Sprintf("  %s → %s", item.Question, ans))
					}
					m.messages = append(m.messages, cliMessage{
						role:      "system",
						content:   strings.Join(answerParts, "\n"),
						timestamp: time.Now(),
						dirty:     true,
					})
					m.typing = true
					m.inputReady = false
					m.resetProgressState()
					m.updateViewportContent()
				}, func() {
					m.messages = append(m.messages, cliMessage{
						role:      "system",
						content:   "已取消提问",
						timestamp: time.Now(),
						dirty:     true,
					})
					m.typing = false
					m.inputReady = true
					m.resetProgressState()
					m.updateViewportContent()
				})
				return
			}
		}

		// Snapshot the final iteration before clearing
		if m.lastSeenIteration >= 0 && len(m.lastCompletedTools) > 0 {
			alreadySnapped := false
			for _, s := range m.iterationHistory {
				if s.Iteration == m.lastSeenIteration {
					alreadySnapped = true
					break
				}
			}
			if !alreadySnapped {
				// Filter tools by Iteration field to ensure correct attribution
				var finalTools []CLIToolProgress
				for _, t := range m.lastCompletedTools {
					if t.Iteration == m.lastSeenIteration {
						finalTools = append(finalTools, t)
					}
				}
				if len(finalTools) > 0 {
					m.iterationHistory = append(m.iterationHistory, cliIterationSnapshot{
						Iteration: m.lastSeenIteration,
						Tools:     finalTools,
					})
				}
			}
		}

		// §2 工具可视化：在 assistant 消息之前插入 tool_summary
		// Build iterations from pendingToolSummary (PhaseDone) + local iterationHistory.
		// If PhaseDone already appended a placeholder, remove it first.
		var toolSummaryIterations []cliIterationSnapshot
		if m.pendingToolSummary != nil {
			toolSummaryIterations = append(toolSummaryIterations, m.pendingToolSummary.iterations...)
			// Remove the placeholder that PhaseDone appended at the end
			for i := len(m.messages) - 1; i >= 0; i-- {
				if m.messages[i].role == "tool_summary" && len(m.messages[i].iterations) > 0 &&
					len(m.messages[i].iterations) == len(m.pendingToolSummary.iterations) {
					m.messages = append(m.messages[:i], m.messages[i+1:]...)
					break
				}
			}
			m.pendingToolSummary = nil
		}
		if len(m.iterationHistory) > 0 {
			toolSummaryIterations = append(toolSummaryIterations, m.iterationHistory...)
		}
		if len(toolSummaryIterations) > 0 {
			toolMsg := cliMessage{
				role:       "tool_summary",
				content:    "",
				timestamp:  time.Now(),
				iterations: toolSummaryIterations,
				dirty:      true,
			}
			// Find the assistant message we just added and insert before it
			assistantIdx := len(m.messages) - 1
			if assistantIdx >= 0 && m.messages[assistantIdx].role == "assistant" {
				m.messages = append(m.messages[:assistantIdx], append([]cliMessage{toolMsg}, m.messages[assistantIdx:]...)...)
			} else {
				// Fallback: append at end
				m.messages = append(m.messages, toolMsg)
			}
			m.renderCacheValid = false
		}

		// 重置迭代追踪状态
		m.lastCompletedTools = nil
		m.iterationHistory = nil
		m.lastSeenIteration = 0
		m.typingStartTime = time.Time{}
		m.typing = false
		m.inputReady = true

	}

	m.updateViewportContent()
}

// renderProgressBlock renders the iteration progress panel for the viewport.
func (m *cliModel) renderProgressBlock() string {
	if !m.typing && m.progress == nil {
		return ""
	}

	bubbleWidth := m.width - 4
	innerWidth := bubbleWidth - 4 // border(2) + padding(2)

	// §20 使用缓存样式
	s := &m.styles
	iterStyle := s.ProgressIter
	thinkingStyle := s.ProgressThinking
	toolDoneStyle := s.ProgressDone
	toolRunningStyle := s.ProgressRunning
	toolErrorStyle := s.ProgressError
	elapsedStyle := s.ProgressElapsed
	indentGuide := s.ProgressIndent
	dimStyle := s.ProgressDim

	var sb strings.Builder

	// Render completed iterations (dimmed)
	for _, snap := range m.iterationHistory {
		sb.WriteString(dimStyle.Render(iterStyle.Render(fmt.Sprintf("#%d", snap.Iteration))))
		sb.WriteString("\n")
		if snap.Thinking != "" {
			// Collapse multi-line thinking text into a single line to avoid
			// command output bleeding into subsequent progress lines.
			text := truncateToWidth(strings.ReplaceAll(snap.Thinking, "\n", " "), innerWidth-4)
			sb.WriteString(dimStyle.Render(indentGuide.Render("  │ ") + thinkingStyle.Render(text)))
			sb.WriteString("\n")
		}
		for _, tool := range snap.Tools {
			label := tool.Label
			if label == "" {
				label = tool.Name
			}
			icon := "✓"
			style := toolDoneStyle
			if tool.Status == "error" {
				icon = "✗"
				style = toolErrorStyle
			}
			line := fmt.Sprintf("  │ %s %s", icon, label)
			if tool.Elapsed > 0 {
				pad := innerWidth - lipgloss.Width(line) - len(formatElapsed(tool.Elapsed))
				if pad < 1 {
					pad = 1
				}
				line += strings.Repeat(" ", pad) + elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			sb.WriteString(dimStyle.Render(style.Render(line)))
			sb.WriteString("\n")
		}
	}

	// Render current iteration
	if m.progress != nil {
		sb.WriteString(iterStyle.Render(fmt.Sprintf("#%d", m.progress.Iteration)))
		sb.WriteString("\n")

		if m.progress.Thinking != "" {
			// Collapse multi-line thinking text into a single line to avoid
			// command output bleeding into subsequent progress lines.
			text := truncateToWidth(strings.ReplaceAll(m.progress.Thinking, "\n", " "), innerWidth-4)
			sb.WriteString(indentGuide.Render("  │ ") + thinkingStyle.Render(text))
			sb.WriteString("\n")
		}

		// Completed tools in current iteration — filter by Iteration field
		for _, tool := range m.progress.CompletedTools {
			if tool.Iteration != m.progress.Iteration {
				continue
			}
			label := tool.Label
			if label == "" {
				label = tool.Name
			}
			style := toolDoneStyle
			icon := "✓"
			if tool.Status == "error" {
				style = toolErrorStyle
				icon = "✗"
			}
			line := fmt.Sprintf("  │ %s %s", icon, label)
			if tool.Elapsed > 0 {
				pad := innerWidth - lipgloss.Width(line) - len(formatElapsed(tool.Elapsed))
				if pad < 1 {
					pad = 1
				}
				line += strings.Repeat(" ", pad) + elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			sb.WriteString(style.Render(line))
			sb.WriteString("\n")
		}

		// Active tools — 带迷你脉冲进度条动画
		for _, tool := range m.progress.ActiveTools {
			if tool.Status == "done" || tool.Status == "error" {
				continue
			}
			label := tool.Label
			if label == "" {
				label = tool.Name
			}
			// 迷你进度条：braille 波浪流动动画（第 4 轮升级）
			// 使用 braille 点阵创造流畅的波浪填充效果
			waveBarFrames := []string{
				"⠁⠁⠃⠃⠇⠇⠟⠟", "⠃⠃⠇⠇⠟⠟⠧⠧", "⠇⠇⠟⠟⠧⠧⡧⡧", "⠟⠟⠧⠧⡧⡧⣧⣧",
				"⠧⠧⡧⡧⣧⣧⣿⣿", "⡧⡧⣧⣧⣿⣿⣿⣿", "⣧⣧⣿⣿⣿⣿⣿⣿", "⣿⣿⣿⣿⣿⣿⣿⣿",
				"⣿⣿⣿⣿⣿⣿⣧⣧", "⣿⣿⣿⣿⣧⣧⡧⡧", "⣿⣿⣧⣧⡧⡧⠧⠧", "⣧⣧⡧⡧⠧⠧⠇⠇",
				"⡧⡧⠧⠧⠇⠇⠃⠃", "⠧⠧⠇⠇⠃⠃⠁⠁", "⠇⠇⠃⠃⠁⠁⠁⠁",
			}
			barIdx := int(m.ticker.ticks) % len(waveBarFrames)
			miniBar := s.WarningSt.Render(waveBarFrames[barIdx])
			line := fmt.Sprintf("  │ %s %s  %s", m.ticker.viewFrames(arrowFrames), label, miniBar)
			if tool.Elapsed > 0 {
				pad := innerWidth - lipgloss.Width(line) - len(formatElapsed(tool.Elapsed))
				if pad < 1 {
					pad = 1
				}
				line += strings.Repeat(" ", pad) + elapsedStyle.Render(formatElapsed(tool.Elapsed))
			}
			sb.WriteString(toolRunningStyle.Render(line))
			sb.WriteString("\n")
		}

		// Phase-specific fallback when no tools are shown
		hasTools := len(m.progress.ActiveTools) > 0 || len(m.progress.CompletedTools) > 0
		if !hasTools {
			switch m.progress.Phase {
			case "thinking":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.view())
				sb.WriteString(thinkingStyle.Render(" " + pickVerb(m.ticker.ticks) + "..."))
				sb.WriteString("\n")
			case "compressing":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" compressing..."))
				sb.WriteString("\n")
			case "retrying":
				sb.WriteString("  ")
				sb.WriteString(m.ticker.viewFrames(orbitFrames))
				sb.WriteString(thinkingStyle.Render(" retrying..."))
				sb.WriteString("\n")
			}
		}

		// SubAgent tree
		if len(m.progress.SubAgents) > 0 {
			sb.WriteString("\n")
			m.renderSubAgentTree(&sb, m.progress.SubAgents, 1)
		}
	} else if m.typing {
		sb.WriteString("  ")
		sb.WriteString(m.ticker.viewFrames(orbitFrames))
		sb.WriteString(thinkingStyle.Render(" " + pickVerb(m.ticker.ticks) + "..."))
		sb.WriteString("\n")
	}

	content := strings.TrimRight(sb.String(), "\n")
	if content == "" {
		return ""
	}

	// Total elapsed
	elapsed := ""
	if !m.typingStartTime.IsZero() {
		elapsed = " " + elapsedStyle.Render(formatElapsed(time.Since(m.typingStartTime).Milliseconds()))
	}

	// Header
	headerStyle := s.ProgressHeader
	header := headerStyle.Render("Progress") + elapsed

	// Wrap in border
	blockStyle := s.ProgressBlock.Width(bubbleWidth)

	return blockStyle.Render(header+"\n"+content) + "\n\n"
}

// renderSubAgentTree renders nested sub-agents with indentation.
// Only renders running/pending agents — completed ones are already captured
// in the tool summary and shouldn't linger in the progress panel.
func (m *cliModel) renderSubAgentTree(sb *strings.Builder, agents []CLISubAgent, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, sa := range agents {
		// Skip completed sub-agents — their results are in the tool summary
		if sa.Status == "done" {
			continue
		}
		icon := m.ticker.viewFrames(waveFrames)
		style := m.styles.ProgressRunning // §20
		switch sa.Status {
		case "error":
			icon = "✗"
			style = m.styles.ProgressError
		}
		line := fmt.Sprintf("%s%s %s", indent, icon, sa.Role)
		if sa.Desc != "" {
			line += ": " + sa.Desc
		}
		sb.WriteString(style.Render(line))
		sb.WriteString("\n")
		if len(sa.Children) > 0 {
			m.renderSubAgentTree(sb, sa.Children, depth+1)
		}
	}
}

// renderHelpPanel 渲染格式化的帮助面板（第 4 轮）。
// 使用 lipgloss 边框 + 分组布局 + 状态图标，替代纯文本。
func (m *cliModel) renderHelpPanel() string {
	contentWidth := m.width - 4
	if contentWidth < 40 {
		contentWidth = 40
	}

	// §20 使用缓存样式
	s := &m.styles
	titleStyle := s.HelpTitle
	cmdStyle := s.HelpCmd
	descStyle := s.HelpDesc
	groupStyle := s.HelpGroup
	keyStyle := s.HelpKey
	panelStyle := s.HelpPanel.Width(contentWidth)

	// 命令列表
	commands := []struct{ cmd, desc string }{
		{"/cancel", "取消当前操作"},
		{"/clear", "清空聊天记录"},
		{"/compact", "压缩上下文"},
		{"/model", "切换模型"},
		{"/models", "列出可用模型"},
		{"/context", "查看上下文信息"},
		{"/new", "开始新会话"},
		{"/settings", "打开设置面板"},
		{"/setup", "重新运行配置引导"},
		{"/update", "检查更新"},
		{"/help", "显示此帮助"},
	}

	// 快捷键列表
	shortcuts := []struct{ key, desc string }{
		{"Enter", "发送消息"},
		{"Ctrl+C/Esc", "中止/清空"},
		{"Ctrl+J", "输入框换行"},
		{"Ctrl+K", "上下文删除"},
		{"Ctrl+O", "展开/折叠工具"},
		{"Ctrl+E", "折叠/展开消息"},
		{"Tab", "命令/路径补全"},
		{"Home/End", "跳到顶/底部"},
		{"↑", "后台任务面板"},
	}

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("xbot Help"))
	sb.WriteString("\n")

	sb.WriteString(groupStyle.Render(" Commands "))
	sb.WriteString("\n")
	for _, c := range commands {
		sb.WriteString("  " + cmdStyle.Render(c.cmd) + " " + descStyle.Render(c.desc))
		sb.WriteString("\n")
	}

	sb.WriteString(groupStyle.Render(" Shortcuts "))
	sb.WriteString("\n")
	for _, s := range shortcuts {
		sb.WriteString("  " + keyStyle.Render(s.key) + " " + descStyle.Render(s.desc))
		sb.WriteString("\n")
	}

	return panelStyle.Render(sb.String())
}

// renderMessage 渲染单条消息为 ANSI 字符串（§1 增量渲染：自包含方法）
func (m *cliModel) renderMessage(msg *cliMessage) string {
	// §20 使用缓存样式
	s := &m.styles
	var sb strings.Builder
	contentWidth := m.width - 4 // 留边距
	timeStyle := s.Time
	userLabelStyle := s.UserLabel
	assistantLabelStyle := s.AssistLabel
	streamingLabelStyle := s.StreamingLabel
	systemMsgStyle := s.SystemMsg
	errorMsgStyle := s.ErrorMsg

	// 渲染 Markdown（仅对 assistant 消息）
	var rendered string
	if msg.role == "assistant" {
		// Pre-process: 代码块升级渲染（第 4 轮）+ mermaid 图表
		// 先渲染 mermaid 图表为 ASCII art
		mermaidProcessed := renderMermaidBlocks(msg.content, m.width-4)
		// 提取代码块并用 lipgloss 边框包裹（glamour 不再处理代码块）
		preprocessed := prettifyCodeBlocks(mermaidProcessed, m.width-4)
		var err error
		rendered, err = m.renderer.Render(preprocessed)
		if err != nil {
			rendered = msg.content
		}
		rendered = strings.TrimSpace(rendered)
	} else {
		rendered = msg.content
	}

	timeStr := timeStyle.Render(msg.timestamp.Format("15:04:05"))

	switch msg.role {
	case "tool_summary":
		// §20 使用缓存样式
		toolSummaryStyle := s.ToolSummary
		toolHeaderStyle := s.ToolHeader
		toolItemStyle := s.ToolItem
		toolErrorItemStyle := s.ToolErrorItem
		thinkingStyle := s.ToolThinking
		hintStyle := s.ToolHint

		// 统计总工具数和总耗时
		// 统计总工具数和总耗时
		totalTools := 0
		totalMs := int64(0)
		if len(msg.iterations) > 0 {
			for _, it := range msg.iterations {
				totalTools += len(it.Tools)
				for _, tool := range it.Tools {
					totalMs += tool.Elapsed
				}
			}
		} else {
			totalTools = len(msg.tools)
			for _, tool := range msg.tools {
				totalMs += tool.Elapsed
			}
		}

		var toolSb strings.Builder

		if m.toolSummaryExpanded {
			// 展开模式：完整渲染
			if len(msg.iterations) > 0 {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d iterations, %d calls)", len(msg.iterations), totalTools)))
				toolSb.WriteString("\n")
				for _, it := range msg.iterations {
					if it.Thinking != "" {
						toolSb.WriteString(thinkingStyle.Render(fmt.Sprintf("  [%d] %s", it.Iteration, it.Thinking)))
						toolSb.WriteString("\n")
					}
					for _, tool := range it.Tools {
						label := tool.Label
						if label == "" {
							label = tool.Name
						}
						elapsed := ""
						if tool.Elapsed > 0 {
							elapsed = fmt.Sprintf(" (%dms)", tool.Elapsed)
						}
						// 根据工具状态选择图标和颜色
						icon := "\u2713"
						sty := toolItemStyle
						if tool.Status == "error" {
							icon = "\u2717"
							sty = toolErrorItemStyle
						}
						toolSb.WriteString(sty.Render(fmt.Sprintf("    %s %s%s", icon, label, elapsed)))
						toolSb.WriteString("\n")
					}
				}
			} else {
				toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools (%d)", totalTools)))
				toolSb.WriteString("\n")
				for _, tool := range msg.tools {
					label := tool.Label
					if label == "" {
						label = tool.Name
					}
					elapsed := ""
					if tool.Elapsed > 0 {
						elapsed = fmt.Sprintf(" (%dms)", tool.Elapsed)
					}
					icon := "\u2713"
					sty := toolItemStyle
					if tool.Status == "error" {
						icon = "\u2717"
						sty = toolErrorItemStyle
					}
					toolSb.WriteString(sty.Render(fmt.Sprintf("  %s %s%s", icon, label, elapsed)))
					toolSb.WriteString("\n")
				}
			}
		} else {
			// 折叠模式升级（第 4 轮）：统计摘要 + 成功/失败状态图标
			elapsedStr := formatElapsed(totalMs)
			// 统计成功/失败工具数
			successCount, errorCount := 0, 0
			if len(msg.iterations) > 0 {
				for _, it := range msg.iterations {
					for _, tool := range it.Tools {
						if tool.Status == "error" {
							errorCount++
						} else {
							successCount++
						}
					}
				}
			} else {
				for _, tool := range msg.tools {
					if tool.Status == "error" {
						errorCount++
					} else {
						successCount++
					}
				}
			}
			// 状态摘要图标
			var statusIcons string
			if errorCount > 0 {
				statusIcons = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Error)).Render("✗") +
					lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted)).Render(fmt.Sprintf("%d", errorCount))
			}
			if successCount > 0 && errorCount > 0 {
				statusIcons += " "
			}
			if successCount > 0 {
				statusIcons += lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Success)).Render("✓") +
					lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted)).Render(fmt.Sprintf("%d", successCount))
			}
			toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools %d calls · %s", totalTools, elapsedStr)))
			if statusIcons != "" {
				toolSb.WriteString("  ")
				toolSb.WriteString(statusIcons)
			}
			toolSb.WriteString("  ")
			toolSb.WriteString(hintStyle.Render("[Ctrl+O]"))
		}
		sb.WriteString(toolSummaryStyle.Render(toolSb.String()))
	case "system":
		// 检测是否为错误消息（包含 error/failed/失败 等关键词）
		isError := false
		lowerContent := strings.ToLower(msg.content)
		for _, kw := range []string{"error", "failed", "失败", "错误", "exception", "denied", "refused"} {
			if strings.Contains(lowerContent, kw) {
				isError = true
				break
			}
		}
		if isError {
			sb.WriteString(errorMsgStyle.Render("⚠ " + msg.content))
		} else {
			sb.WriteString(systemMsgStyle.Render(msg.content))
		}
	case "user":
		// 用户消息上方：右侧柔和光点分隔，与 assistant 的左侧竖线形成对称
		dotSep := lipgloss.NewStyle().
			Foreground(lipgloss.Color(currentTheme.BarEmpty)).
			Width(contentWidth).
			Align(lipgloss.Right).
			Render("···")
		sb.WriteString(dotSep)
		sb.WriteString("\n")
		label := userLabelStyle.Render("You")
		header := lipgloss.NewStyle().
			Width(contentWidth).
			Align(lipgloss.Right).
			Render(fmt.Sprintf("%s %s", timeStr, label))
		sb.WriteString(header)
		sb.WriteString("\n")
		// 用户消息：右对齐气泡效果
		// 计算内容最大行宽，整块右对齐而非每行拉伸
		lines := strings.Split(rendered, "\n")
		maxWidth := 0
		for _, line := range lines {
			w := lipgloss.Width(line)
			if w > maxWidth {
				maxWidth = w
			}
		}
		maxBubble := contentWidth * 3 / 4
		userStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))
		if maxWidth <= maxBubble {
			// 内容够窄，左填充实现气泡靠右
			userStyle = userStyle.PaddingLeft(contentWidth - maxWidth)
		}
		// 内容超宽时退回左对齐，避免终端折行后跑到最左边
		sb.WriteString(userStyle.Render(rendered))
	default:
		// assistant 消息：左侧竖线引导 + 标签
		guideColor := currentTheme.Accent
		if msg.isPartial {
			guideColor = currentTheme.Warning
		}
		guide := lipgloss.NewStyle().Foreground(lipgloss.Color(guideColor)).Render("│")
		if msg.isPartial {
			label := streamingLabelStyle.Render("Assistant")
			fmt.Fprintf(&sb, "%s %s %s ...", guide, timeStr, label)
		} else {
			label := assistantLabelStyle.Render("Assistant")
			fmt.Fprintf(&sb, "%s %s %s", guide, timeStr, label)
		}
		sb.WriteString("\n")
		// §19 长消息折叠：完成的 assistant 消息超过 15 行时自动折叠
		// collapsed 字段区分"自动折叠"和"用户手动展开"
		msgLines := strings.Count(rendered, "\n") + 1
		if !msg.isPartial && msgLines > 15 && !msg.collapsed {
			// 首次渲染长消息：自动折叠
			msg.collapsed = true
		}
		if !msg.isPartial && msg.collapsed {
			allLines := strings.Split(rendered, "\n")
			visibleCount := 4
			if visibleCount > len(allLines) {
				visibleCount = len(allLines)
			}
			visible := strings.Join(allLines[:visibleCount], "\n")
			collapseHint := lipgloss.NewStyle().
				Foreground(lipgloss.Color(currentTheme.TextMuted)).
				Faint(true).
				Render(fmt.Sprintf("  ── ▼ %d more lines (Ctrl+E to expand) ──", len(allLines)-visibleCount))
			sb.WriteString(visible)
			sb.WriteString("\n")
			sb.WriteString(collapseHint)
			sb.WriteString("\n")
		} else {
			// Agent 消息直接渲染（glamour 已处理 markdown）
			sb.WriteString(rendered)
		}
		// 流式输出时追加闪烁光标，让用户感知"正在生成"
		if msg.isPartial && rendered != "" {
			streamCursor := lipgloss.NewStyle().
				Foreground(lipgloss.Color(currentTheme.Warning)).
				Bold(true)
			sb.WriteString(streamCursor.Render("▋"))
		}
	}

	sb.WriteString("\n\n")
	return sb.String()
}

// setViewportContent sets viewport content while preserving scroll position.
// If the user was at the bottom before the update, keep them at the bottom.
// Lines wider than the viewport are truncated to prevent layout breakage.
func (m *cliModel) setViewportContent(content string) {
	if m.width > 0 {
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			// Strip trailing whitespace first — mermaid-ascii and wide tables
			// pad lines with spaces that inflate lipgloss.Width() far beyond
			// the actual visible content, causing premature truncation.
			line = strings.TrimRight(line, " \t")
			if lipgloss.Width(line) > m.width {
				line = truncateRunes(line, m.width)
			}
			lines[i] = line
		}
		content = strings.Join(lines, "\n")
	}
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(content)
	if atBottom {
		m.viewport.GotoBottom()
		m.newContentHint = false
	} else {
		m.newContentHint = true
	}
}

// renderDeleteBoundaryLine 渲染 Ctrl+K 删除边界红线。
func (m *cliModel) renderDeleteBoundaryLine() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	redStyle := m.styles.ProgressError // §20
	label := " ✂ delete below "
	// label 的可见宽度（不含 ANSI 转义）
	labelWidth := lipgloss.Width(redStyle.Bold(true).Render(label))
	totalPad := w - labelWidth
	if totalPad < 0 {
		totalPad = 0
	}
	leftPad := totalPad / 2
	rightPad := totalPad - leftPad
	line := redStyle.Bold(true).Render(
		strings.Repeat("━", leftPad) + label + strings.Repeat("━", rightPad),
	)
	return "\n" + line + "\n"
}

// visibleMsgGroupIndices 返回每个"可见消息组"的起始 slice 索引。
// tool_summary 与前一条 assistant 消息合并为一组，不单独计数。
func visibleMsgGroupIndices(messages []cliMessage) []int {
	var groups []int
	for i, msg := range messages {
		if msg.role == "tool_summary" {
			continue
		}
		groups = append(groups, i)
	}
	return groups
}

// scrollToDeleteLine 确保 Ctrl+K 红线在 viewport 可见区域内。
func (m *cliModel) scrollToDeleteLine(content string) {
	contentLines := strings.Split(content, "\n")
	totalLines := len(contentLines)
	vpHeight := m.viewport.Height
	if vpHeight <= 0 {
		return
	}
	// 找到红线行（包含 "✂ delete above" 的行）
	redLineIdx := -1
	for i, line := range contentLines {
		if strings.Contains(line, "✂ delete below") {
			redLineIdx = i
			break
		}
	}
	if redLineIdx < 0 {
		return
	}
	// 将红线定位到视口中央偏上（留 3 行上方边距）
	targetYOffset := redLineIdx - 3
	if targetYOffset < 0 {
		targetYOffset = 0
	}
	maxOffset := totalLines - vpHeight
	if maxOffset < 0 {
		maxOffset = 0
	}
	if targetYOffset > maxOffset {
		targetYOffset = maxOffset
	}
	m.viewport.SetYOffset(targetYOffset)
}

// updateViewportContent 更新 viewport 显示内容（§1 增量渲染）
func (m *cliModel) updateViewportContent() {
	// 快速路径：流式消息 + 缓存有效
	if m.streamingMsgIdx >= 0 && m.renderCacheValid {
		m.updateStreamingOnly()
		return
	}

	// 快速路径：缓存有效 + 无流式消息 + 消息数未变，只刷新 progress block（tick 场景）
	if m.renderCacheValid && m.streamingMsgIdx < 0 && m.cachedMsgCount == len(m.messages) {
		var sb strings.Builder
		sb.WriteString(m.cachedHistory)
		sb.WriteString(m.renderProgressBlock())
		m.setViewportContent(sb.String())
		return
	}

	// 慢速路径：全量重建
	m.fullRebuild()
}

// updateStreamingOnly 只重新渲染当前流式消息（快速路径）
func (m *cliModel) updateStreamingOnly() {
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)

	// 只渲染当前流式消息
	msg := &m.messages[m.streamingMsgIdx]
	msg.dirty = true
	sb.WriteString(m.renderMessage(msg))

	// Append progress block
	sb.WriteString(m.renderProgressBlock())

	m.setViewportContent(sb.String())
}

// fullRebuild 全量重建渲染缓存（慢速路径）
func (m *cliModel) fullRebuild() {
	var historyBuf strings.Builder

	// splitIdx 确保当前流式消息不进入 cachedHistory
	splitIdx := len(m.messages)
	if m.streamingMsgIdx >= 0 {
		splitIdx = m.streamingMsgIdx
	}

	// §9 Ctrl+K 红线：根据可见消息组计算删除边界 slice 索引
	var redLineInsertIdx = -1
	if m.confirmDelete > 0 {
		groups := visibleMsgGroupIndices(m.messages)
		if m.confirmDelete <= len(groups) {
			redLineInsertIdx = groups[len(groups)-m.confirmDelete] - 1
		}
	}

	for i := range m.messages[:splitIdx] {
		needsRender := m.messages[i].dirty || m.messages[i].renderWidth != m.width
		if needsRender {
			rendered := m.renderMessage(&m.messages[i])
			m.messages[i].rendered = rendered
			m.messages[i].dirty = false
			m.messages[i].renderWidth = m.width
		}
		historyBuf.WriteString(m.messages[i].rendered)
		// §9 Ctrl+K 红线：在删除边界处插入红线指示器
		if redLineInsertIdx >= 0 && i == redLineInsertIdx {
			historyBuf.WriteString(m.renderDeleteBoundaryLine())
		}
	}

	m.cachedHistory = historyBuf.String()
	m.renderCacheValid = true
	m.cachedMsgCount = len(m.messages)

	// 拼接最终内容：历史 + 当前流式消息（如有） + progress block
	var sb strings.Builder
	sb.WriteString(m.cachedHistory)
	if m.streamingMsgIdx >= 0 {
		sb.WriteString(m.renderMessage(&m.messages[m.streamingMsgIdx]))
	}
	sb.WriteString(m.renderProgressBlock())

	m.setViewportContent(sb.String())

	// §9 Ctrl+K 红线：自动滚动到红线位置
	if m.confirmDelete > 0 {
		m.scrollToDeleteLine(sb.String())
	}
}

// tickCmd 定时器命令
func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return cliTickMsg{}
	})
}

// tickerCmd returns a cmd that sends tickerTickMsg at ~10 FPS.
func tickerCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return tickerTickMsg{}
	})
}

// ---------------------------------------------------------------------------
// NonInteractiveChannel (非交互模式，单次执行)
// ---------------------------------------------------------------------------

// NonInteractiveChannel 非交互模式渠道，用于管道/参数模式。
// 收到完整消息后打印到 stdout 并设置退出标志。
type NonInteractiveChannel struct {
	msgBus *bus.MessageBus
	msgCh  chan bus.OutboundMessage
	done   chan struct{}
}

// NewNonInteractiveChannel 创建非交互模式渠道
func NewNonInteractiveChannel(msgBus *bus.MessageBus) *NonInteractiveChannel {
	ch := &NonInteractiveChannel{
		msgBus: msgBus,
		msgCh:  make(chan bus.OutboundMessage, 64),
		done:   make(chan struct{}),
	}
	// 启动消息接收 goroutine
	go ch.run()
	return ch
}

func (c *NonInteractiveChannel) run() {
	var prevContent string
	for msg := range c.msgCh {
		content := msg.Content
		if strings.HasPrefix(content, "__FEISHU_CARD__") {
			content = ConvertFeishuCard(content)
		}
		if msg.IsPartial {
			// 流式部分消息：只输出增量部分
			if len(content) > len(prevContent) {
				diff := content[len(prevContent):]
				fmt.Print(diff)
			}
			prevContent = content
		} else {
			// 完整消息：输出剩余差异部分，然后换行
			if len(content) > len(prevContent) {
				diff := content[len(prevContent):]
				fmt.Print(diff)
			}
			fmt.Println()
			close(c.done)
			return
		}
	}
}

func (c *NonInteractiveChannel) Name() string { return "cli" }
func (c *NonInteractiveChannel) Start() error { return nil }
func (c *NonInteractiveChannel) Stop()        {}
func (c *NonInteractiveChannel) Send(msg bus.OutboundMessage) (string, error) {
	select {
	case c.msgCh <- msg:
	default:
	}
	return "", nil
}
func (c *NonInteractiveChannel) WaitDone() { <-c.done }

// --- §12 Interactive Panel ---

// openSettingsPanel activates the settings panel overlay.
func (m *cliModel) openSettingsPanel(schema []SettingDefinition, values map[string]string, onSubmit func(map[string]string)) {
	m.panelMode = "settings"
	m.panelCursor = 0
	m.panelEdit = false
	m.panelSchema = make([]SettingDefinition, len(schema))
	copy(m.panelSchema, schema)
	m.panelValues = make(map[string]string, len(values))
	for k, v := range values {
		m.panelValues[k] = v
	}
	// Fill defaults
	for _, def := range m.panelSchema {
		if _, ok := m.panelValues[def.Key]; !ok && def.DefaultValue != "" {
			m.panelValues[def.Key] = def.DefaultValue
		}
	}
	m.panelOnSubmit = onSubmit
	m.panelOnCancel = nil
	// Pre-create textarea for editing
	ta := textarea.New()
	ta.Placeholder = "输入新值..."
	ta.SetWidth(m.panelWidth(60))
	ta.SetHeight(1)
	ta.CharLimit = 200
	m.panelEditTA = ta
}

// openSetupPanel opens the first-run setup wizard as a settings-style panel.
func (m *cliModel) openSetupPanel() {
	schema := cliSetupSchema()
	values := make(map[string]string)
	// Pre-fill defaults from schema
	for _, def := range schema {
		if def.DefaultValue != "" {
			values[def.Key] = def.DefaultValue
		}
	}
	// Pre-fill current values if available
	if m.channel != nil && m.channel.config.GetCurrentValues != nil {
		for k, v := range m.channel.config.GetCurrentValues() {
			if v != "" {
				values[k] = v
			}
		}
	}
	m.openSettingsPanel(schema, values, func(vals map[string]string) {
		// Apply all settings including setup-only keys (provider, api_key, sandbox, memory)
		if m.channel.config.ApplySettings != nil {
			m.channel.config.ApplySettings(vals)
		}
		// Apply theme immediately
		if theme, ok := vals["theme"]; ok && theme != "" {
			ApplyTheme(theme)
			if m.width > 4 {
				m.renderer = newGlamourRenderer(m.width - 4)
			}
			m.renderCacheValid = false
		}
		msg := "✅ 初始配置完成，可以开始使用了。随时用 /settings 修改配置，/setup 重新引导。"
		if vals["memory_provider"] == "letta" {
			msg += "\n\n[!] letta memory mode requires embedding service:\n  1. Install Ollama: https://ollama.ai\n  2. Pull embedding model: `ollama pull nomic-embed-text`\n  3. Set embedding endpoint in config or env"
		}
		m.messages = append(m.messages, cliMessage{
			role:      "system",
			content:   msg,
			timestamp: time.Now(),
			dirty:     true,
		})
		m.updateViewportContent()
	})
}

// askItem represents a single question in the AskUser panel.
type askItem struct {
	Question string   // the question text
	Options  []string // choices (empty = free input only)
	Answer   string   // user's answer (set on submit)
	Other    string   // user's custom input when "Other" option selected
}

// askQItem is the JSON structure for questions metadata from the AskUser tool.
type askQItem struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// openAskUserPanel activates the ask-user panel overlay.
func (m *cliModel) openAskUserPanel(items []askItem, onAnswer func(map[string]string), onCancel func()) {
	m.panelMode = "askuser"
	m.panelItems = items
	m.panelTab = 0
	m.panelOptSel = make(map[int]map[int]bool)
	m.panelOptCursor = make(map[int]int)
	ta := textarea.New()
	ta.Placeholder = "Type your answer..."
	ta.Prompt = "  "
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Info))
	ta.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.FocusedStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.BlurredStyle.Text = lipgloss.NewStyle()
	ta.CharLimit = 0
	ta.SetWidth(m.panelWidth(50))
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.Focus()
	m.panelAnswerTA = ta
	// Initialize Other single-line input
	ti := textinput.New()
	ti.Placeholder = "Type here..."
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.Width = m.panelWidth(40)
	ti.PromptStyle = lipgloss.NewStyle()
	ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Info))
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
	ti.Focus()
	m.panelOtherTI = ti
	m.panelOnAnswer = onAnswer
	m.panelOnCancel = onCancel
}

// closePanel deactivates any active panel.
func (m *cliModel) closePanel() {
	m.panelMode = ""
	m.panelEdit = false
	m.panelCombo = false
	m.panelSchema = nil
	m.panelValues = nil
	m.panelOnSubmit = nil
	m.panelItems = nil
	m.panelTab = 0
	m.panelOptSel = nil
	m.panelOptCursor = nil
	// Bg tasks panel cleanup
	m.panelBgTasks = nil
	m.panelBgViewing = false
	m.panelBgScroll = 0
	m.panelBgLogLines = nil
}

// openBgTasksPanel opens the background task management panel.
func (m *cliModel) openBgTasksPanel() {
	if m.channel == nil || m.channel.bgTaskMgr == nil {
		return
	}
	m.panelMode = "bgtasks"
	m.panelBgTasks = m.channel.bgTaskMgr.List(m.channel.bgSessionKey)
	m.panelBgCursor = 0
	m.panelBgViewing = false
	m.panelBgScroll = 0
	m.panelBgLogLines = nil
	// Clamp cursor
	if len(m.panelBgTasks) == 0 {
		m.panelBgCursor = -1
	} else if m.panelBgCursor >= len(m.panelBgTasks) {
		m.panelBgCursor = len(m.panelBgTasks) - 1
	}
}

// updateBgTasksPanel handles key events in the bg tasks panel.
// Returns (handled, newModel, cmd).
func (m *cliModel) updateBgTasksPanel(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	// Refresh task list
	if m.channel != nil && m.channel.bgTaskMgr != nil {
		m.panelBgTasks = m.channel.bgTaskMgr.List(m.channel.bgSessionKey)
	}

	// Log viewing sub-mode
	if m.panelBgViewing {
		switch msg.Type {
		case tea.KeyEsc, tea.KeyCtrlC:
			m.panelBgViewing = false
			m.panelBgScroll = 0
			m.panelBgLogLines = nil
			return true, m, nil
		case tea.KeyUp:
			m.panelBgScroll -= 5
			if m.panelBgScroll < 0 {
				m.panelBgScroll = 0
			}
			return true, m, nil
		case tea.KeyDown:
			maxScroll := len(m.panelBgLogLines) - 20
			if maxScroll < 0 {
				maxScroll = 0
			}
			m.panelBgScroll += 5
			if m.panelBgScroll > maxScroll {
				m.panelBgScroll = maxScroll
			}
			return true, m, nil
		case tea.KeyPgUp:
			m.panelBgScroll -= 18
			if m.panelBgScroll < 0 {
				m.panelBgScroll = 0
			}
			return true, m, nil
		default:
			// PgDn: bubbletea doesn't have a constant, match by string
			if msg.String() == "pgdown" {
				maxScroll := len(m.panelBgLogLines) - 20
				if maxScroll < 0 {
					maxScroll = 0
				}
				m.panelBgScroll += 18
				if m.panelBgScroll > maxScroll {
					m.panelBgScroll = maxScroll
				}
				return true, m, nil
			}
		}
		return true, m, nil
	}

	// Task list mode
	switch msg.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		m.closePanel()
		if m.typing {
			return true, m, tea.Batch(tickerCmd(), tickCmd())
		}
		return true, m, nil

	case tea.KeyUp, tea.KeyCtrlK:
		if m.panelBgCursor > 0 {
			m.panelBgCursor--
		}
		return true, m, nil

	case tea.KeyDown, tea.KeyCtrlJ:
		if m.panelBgCursor < len(m.panelBgTasks)-1 {
			m.panelBgCursor++
		}
		return true, m, nil

	case tea.KeyEnter:
		// View log of selected task
		if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
			task := m.panelBgTasks[m.panelBgCursor]
			// Output is written atomically by the task runner, safe to read
			m.panelBgLogLines = splitLines(task.Output)
			if len(m.panelBgLogLines) == 0 {
				m.panelBgLogLines = []string{"(no output)"}
			}
			m.panelBgViewing = true
			m.panelBgScroll = 0
		}
		return true, m, nil

	case tea.KeyDelete, tea.KeyCtrlD:
		// Kill selected running task
		if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
			task := m.panelBgTasks[m.panelBgCursor]
			if task.Status == tools.BgTaskRunning {
				if m.channel != nil && m.channel.bgTaskMgr != nil {
					if err := m.channel.bgTaskMgr.Kill(task.ID); err != nil {
						m.tempStatus = fmt.Sprintf("Kill failed: %s", err)
						return true, m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
							return cliTempStatusClearMsg{}
						})
					}
					// Refresh list after kill
					m.panelBgTasks = m.channel.bgTaskMgr.List(m.channel.bgSessionKey)
					if m.panelBgCursor >= len(m.panelBgTasks) {
						m.panelBgCursor = len(m.panelBgTasks) - 1
					}
					return true, m, nil
				}
			}
		}
		return true, m, nil
	}

	return true, m, nil
}

// viewBgTasksPanel renders the bg tasks panel.
func (m *cliModel) viewBgTasksPanel() string {
	if m.panelBgViewing {
		return m.viewBgTaskLog()
	}
	return m.viewBgTaskList()
}

// viewBgTaskList renders the task list view.
func (m *cliModel) viewBgTaskList() string {
	// §20 使用缓存样式
	s := &m.styles
	cursorStyle := s.PanelCursor
	header := s.PanelHeader.Render("Background Tasks")
	help := s.PanelDesc.Render("↑↓ navigate  Enter view log  Del kill  Esc close")

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")
	sb.WriteString(s.PanelDivider.Render("┈" + strings.Repeat("┈", 40)))

	if len(m.panelBgTasks) == 0 {
		sb.WriteString(s.PanelEmpty.Render("No background tasks running"))
	} else {
		for i, task := range m.panelBgTasks {
			elapsed := time.Since(task.StartedAt).Round(time.Second)
			if task.FinishedAt != nil {
				elapsed = task.FinishedAt.Sub(task.StartedAt).Round(time.Second)
			}
			statusIcon := "●"
			statusStyle := s.ProgressRunning
			if task.Status == tools.BgTaskDone {
				if task.Error != "" || task.ExitCode != 0 {
					statusIcon = "✗"
					statusStyle = s.ProgressError
				} else {
					statusIcon = "✓"
					statusStyle = s.ProgressDone
				}
			}

			prefix := "  "
			if i == m.panelBgCursor {
				prefix = cursorStyle.Render("▸")
			}

			cmd := task.Command
			if len(cmd) > 50 {
				cmd = cmd[:47] + "..."
			}

			line := fmt.Sprintf("%s %s  %-8s %s  %s",
				prefix,
				statusStyle.Render(statusIcon),
				task.ID,
				formatElapsed(int64(elapsed.Milliseconds())),
				cmd,
			)
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}

	return m.styles.PanelBox.Render(sb.String())
}

// viewBgTaskLog renders the log viewer for a selected task.
func (m *cliModel) viewBgTaskLog() string {
	// §20 使用缓存样式
	s := &m.styles

	var title string
	if m.panelBgCursor >= 0 && m.panelBgCursor < len(m.panelBgTasks) {
		task := m.panelBgTasks[m.panelBgCursor]
		cmd := task.Command
		if len(cmd) > 40 {
			cmd = cmd[:37] + "..."
		}
		title = fmt.Sprintf("Log: %s — %s", task.ID, cmd)
	}
	help := s.PanelDesc.Render("↑↓ scroll  Esc back")

	maxLines := 18
	start := m.panelBgScroll
	if start > len(m.panelBgLogLines) {
		start = len(m.panelBgLogLines)
	}
	end := start + maxLines
	if end > len(m.panelBgLogLines) {
		end = len(m.panelBgLogLines)
	}

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render(title))
	sb.WriteString("  ")
	sb.WriteString(help)
	sb.WriteString("\n")

	for _, line := range m.panelBgLogLines[start:end] {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	if end < len(m.panelBgLogLines) {
		sb.WriteString(s.PanelDesc.Render(fmt.Sprintf("  ... %d more lines (↑↓ scroll)", len(m.panelBgLogLines)-end)))
		sb.WriteString("\n")
	}

	return m.styles.PanelBox.Render(sb.String())
}

// splitLines splits a string into lines, preserving trailing empty line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// updatePanel handles key events when a panel is active.
// Returns (handled, newModel, cmd).
func (m *cliModel) updatePanel(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelMode == "" {
		return false, m, nil
	}

	switch m.panelMode {
	case "settings":
		return m.updateSettingsPanel(msg)
	case "askuser":
		return m.updateAskUserPanel(msg)
	case "bgtasks":
		return m.updateBgTasksPanel(msg)
	}
	return false, m, nil
}

func (m *cliModel) updateSettingsPanel(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelEdit {
		// Editing mode
		switch msg.Type {
		case tea.KeyEnter:
			// Save value
			newVal := strings.TrimSpace(m.panelEditTA.Value())
			if m.panelCursor < len(m.panelSchema) {
				key := m.panelSchema[m.panelCursor].Key
				m.panelValues[key] = newVal
			}
			m.panelEdit = false
			return true, m, nil
		case tea.KeyEsc:
			m.panelEdit = false
			return true, m, nil
		default:
			// Delegate to textarea
			var cmd tea.Cmd
			m.panelEditTA, cmd = m.panelEditTA.Update(msg)
			return true, m, cmd
		}
	}

	// Combo dropdown mode
	if m.panelCombo {
		if m.panelCursor < len(m.panelSchema) {
			def := m.panelSchema[m.panelCursor]
			opts := def.Options
			switch msg.Type {
			case tea.KeyEsc:
				m.panelCombo = false
				return true, m, nil
			case tea.KeyUp:
				if m.panelComboIdx > 0 {
					m.panelComboIdx--
				}
				return true, m, nil
			case tea.KeyDown:
				if m.panelComboIdx < len(opts)-1 {
					m.panelComboIdx++
				}
				return true, m, nil
			case tea.KeyEnter:
				if m.panelComboIdx < len(opts) {
					m.panelValues[def.Key] = opts[m.panelComboIdx].Value
				}
				m.panelCombo = false
				return true, m, nil
			case tea.KeySpace:
				m.panelCombo = false
				// Start typing to filter / enter custom value → switch to edit mode
				m.panelEdit = true
				// Re-initialize textarea with proper styles for panel context
				ta := textarea.New()
				ta.Prompt = "  "
				ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Info))
				ta.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))
				ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
				ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
				ta.CharLimit = 0
				ta.SetWidth(m.panelWidth(50))
				ta.SetHeight(1)
				ta.SetValue(m.panelValues[def.Key])
				ta.CursorEnd()
				ta.Focus()
				var cmd tea.Cmd
				m.panelEditTA, cmd = ta.Update(msg)
				return true, m, cmd
			}
		}
		return true, m, nil
	}

	// Navigation mode
	switch msg.Type {
	case tea.KeyEsc:
		m.closePanel()
		return true, m, nil
	case tea.KeyCtrlS:
		// Submit all settings
		if m.panelOnSubmit != nil {
			m.panelOnSubmit(m.panelValues)
		}
		m.closePanel()
		return true, m, nil
	case tea.KeyUp, tea.KeyShiftTab:
		if m.panelCursor > 0 {
			m.panelCursor--
		}
		return true, m, nil
	case tea.KeyDown, tea.KeyTab:
		if m.panelCursor < len(m.panelSchema)-1 {
			m.panelCursor++
		}
		return true, m, nil
	case tea.KeyEnter:
		if m.panelCursor < len(m.panelSchema) {
			def := m.panelSchema[m.panelCursor]
			switch def.Type {
			case SettingTypeToggle:
				// Toggle on Enter
				cur := m.panelValues[def.Key]
				if cur == "true" {
					m.panelValues[def.Key] = "false"
				} else {
					m.panelValues[def.Key] = "true"
				}
				return true, m, nil
			case SettingTypeSelect:
				// Cycle through options
				cur := m.panelValues[def.Key]
				found := false
				for i, opt := range def.Options {
					if opt.Value == cur && i < len(def.Options)-1 {
						m.panelValues[def.Key] = def.Options[i+1].Value
						found = true
						break
					}
				}
				if !found && len(def.Options) > 0 {
					m.panelValues[def.Key] = def.Options[0].Value
				}
				return true, m, nil
			case SettingTypeCombo:
				// Open combo dropdown if options available, otherwise edit
				if len(def.Options) > 0 {
					m.panelCombo = true
					m.panelComboIdx = 0
					// Pre-select current value if it matches an option
					cur := m.panelValues[def.Key]
					for i, opt := range def.Options {
						if opt.Value == cur {
							m.panelComboIdx = i
							break
						}
					}
					return true, m, nil
				}
				// No options: fall through to edit mode
				m.panelEdit = true
				ta := textarea.New()
				ta.Prompt = "  "
				ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Info))
				ta.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))
				ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
				ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
				ta.CharLimit = 0
				ta.SetWidth(m.panelWidth(50))
				ta.SetHeight(1)
				ta.SetValue(m.panelValues[def.Key])
				ta.CursorEnd()
				ta.Focus()
				m.panelEditTA = ta
				return true, m, nil
			default:
				// Enter edit mode for text/number/textarea/combo(fallback)
				m.panelEdit = true
				ta := textarea.New()
				ta.Prompt = "  "
				ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Info))
				ta.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))
				ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextMuted))
				ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
				ta.CharLimit = 0
				ta.SetWidth(m.panelWidth(50))
				ta.SetHeight(1)
				ta.SetValue(m.panelValues[def.Key])
				ta.CursorEnd()
				ta.Focus()
				m.panelEditTA = ta
				return true, m, nil
			}
		}
		return true, m, nil
	}
	return true, m, nil
}

func (m *cliModel) updateAskUserPanel(msg tea.KeyMsg) (bool, tea.Model, tea.Cmd) {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return true, m, nil
	}
	item := &m.panelItems[m.panelTab]
	numOpts := len(item.Options)
	hasOpts := numOpts > 0
	// Cursor: 0..numOpts-1 (checkbox), numOpts (Other input), numOpts+1 (Submit)
	cursor := m.panelOptCursor[m.panelTab]
	onOther := hasOpts && cursor == numOpts
	onSubmit := hasOpts && cursor == numOpts+1

	switch msg.Type {
	case tea.KeyCtrlS:
		answers := m.collectAskAnswers()
		if m.panelOnAnswer != nil {
			m.panelOnAnswer(answers)
		}
		m.closePanel()
		if m.typing {
			return true, m, tea.Batch(tickerCmd(), tickCmd())
		}
		return true, m, nil
	case tea.KeyEsc:
		if m.panelOnCancel != nil {
			m.panelOnCancel()
		}
		m.closePanel()
		return true, m, nil
	case tea.KeyRight, tea.KeyTab:
		if len(m.panelItems) > 1 && m.panelTab < len(m.panelItems)-1 {
			m.saveCurrentFreeInput()
			m.panelTab++
			m.restoreFreeInput()
		}
		return true, m, nil
	case tea.KeyShiftTab, tea.KeyLeft:
		if len(m.panelItems) > 1 && m.panelTab > 0 {
			m.saveCurrentFreeInput()
			m.panelTab--
			m.restoreFreeInput()
		}
		return true, m, nil
	case tea.KeyUp:
		if hasOpts {
			if onOther {
				m.panelOptCursor[m.panelTab] = numOpts - 1
				return true, m, nil
			}
			if cursor > 0 {
				m.panelOptCursor[m.panelTab] = cursor - 1
			}
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case tea.KeyDown:
		if hasOpts {
			if onOther {
				m.panelOptCursor[m.panelTab] = numOpts + 1
				return true, m, nil
			}
			if cursor < numOpts+1 {
				m.panelOptCursor[m.panelTab] = cursor + 1
			}
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case tea.KeyEnter:
		if hasOpts {
			if onSubmit {
				answers := m.collectAskAnswers()
				if m.panelOnAnswer != nil {
					m.panelOnAnswer(answers)
				}
				m.closePanel()
				if m.typing {
					return true, m, tea.Batch(tickerCmd(), tickCmd())
				}
				return true, m, nil
			}
			// On checkbox: toggle; on Other: do nothing (let user type)
			if !onOther {
				m.toggleOptAtCursor()
			}
			return true, m, nil
		}
		answers := m.collectAskAnswers()
		if m.panelOnAnswer != nil {
			m.panelOnAnswer(answers)
		}
		m.closePanel()
		if m.typing {
			return true, m, tea.Batch(tickerCmd(), tickCmd())
		}
		return true, m, nil
	case tea.KeySpace:
		if hasOpts && !onOther {
			if cursor < numOpts {
				m.toggleOptAtCursor()
			}
			if cursor < numOpts+1 {
				m.panelOptCursor[m.panelTab] = cursor + 1
			}
			return true, m, nil
		}
		if onOther {
			// Other 输入框：空格传给 textinput
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		// No options: fall through to textarea
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	case tea.KeyRunes:
		if hasOpts && !onOther {
			m.panelOptCursor[m.panelTab] = numOpts
			m.restoreOtherInput()
		}
		if onOther {
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		if hasOpts {
			// With options, all input goes through Other textinput
			return true, m, nil
		}
		// No options: textarea
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	default:
		if isCtrlJ(msg) {
			if !hasOpts {
				m.panelAnswerTA.InsertString("\n")
				m.autoExpandAskTA()
			}
			return true, m, nil
		}
		if onOther {
			var cmd tea.Cmd
			m.panelOtherTI, cmd = m.panelOtherTI.Update(msg)
			return true, m, cmd
		}
		if hasOpts {
			return true, m, nil
		}
		m.autoExpandAskTA()
		var cmd tea.Cmd
		m.panelAnswerTA, cmd = m.panelAnswerTA.Update(msg)
		return true, m, cmd
	}

}

// toggleOptAtCursor toggles the checkbox at the current cursor position.
func (m *cliModel) toggleOptAtCursor() {
	tab := m.panelTab
	if m.panelOptSel[tab] == nil {
		m.panelOptSel[tab] = make(map[int]bool)
	}
	cursor := m.panelOptCursor[tab]
	m.panelOptSel[tab][cursor] = !m.panelOptSel[tab][cursor]
}

// collectAskAnswers gathers answers from all questions.
func (m *cliModel) collectAskAnswers() map[string]string {
	answers := make(map[string]string)
	for i, item := range m.panelItems {
		key := fmt.Sprintf("q%d", i)
		hasOpts := len(item.Options) > 0
		var parts []string
		if hasOpts {
			if sel, ok := m.panelOptSel[i]; ok && len(sel) > 0 {
				for idx := range sel {
					if idx < len(item.Options) {
						parts = append(parts, item.Options[idx])
					}
				}
			}
			var otherText string
			if i == m.panelTab {
				otherText = strings.TrimSpace(m.panelOtherTI.Value())
			} else {
				otherText = strings.TrimSpace(item.Other)
			}
			if otherText != "" {
				parts = append(parts, otherText)
			}
			answers[key] = strings.Join(parts, ", ")
		} else {
			if i == m.panelTab {
				answers[key] = strings.TrimSpace(m.panelAnswerTA.Value())
			} else {
				answers[key] = strings.TrimSpace(item.Other)
			}
		}
	}
	return answers
}

// saveCurrentFreeInput saves textarea/textinput content for the current tab.
func (m *cliModel) saveCurrentFreeInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := &m.panelItems[m.panelTab]
	if len(item.Options) > 0 {
		item.Other = m.panelOtherTI.Value()
	} else {
		item.Other = m.panelAnswerTA.Value()
	}
}

// restoreFreeInput restores textarea/textinput content for the current tab.
func (m *cliModel) restoreFreeInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	item := m.panelItems[m.panelTab]
	if len(item.Options) > 0 {
		m.panelOtherTI.SetValue(item.Other)
		m.panelOtherTI.CursorEnd()
	} else {
		m.panelAnswerTA.SetValue(item.Other)
		m.panelAnswerTA.CursorEnd()
		m.autoExpandAskTA()
	}
}

// restoreOtherInput restores the Other textinput for the current tab (options mode).
func (m *cliModel) restoreOtherInput() {
	if m.panelTab < 0 || m.panelTab >= len(m.panelItems) {
		return
	}
	m.panelOtherTI.SetValue(m.panelItems[m.panelTab].Other)
	m.panelOtherTI.CursorEnd()
}

// autoExpandAskTA adjusts textarea height based on content.
func (m *cliModel) autoExpandAskTA() {
	lines := strings.Count(m.panelAnswerTA.Value(), "\n") + 1
	if lines < 2 {
		lines = 2
	}
	if lines > 6 {
		lines = 6
	}
	if m.panelAnswerTA.Height() != lines {
		m.panelAnswerTA.SetHeight(lines)
	}
}

// viewPanel renders the active panel as a string.
func (m *cliModel) viewPanel() string {
	switch m.panelMode {
	case "settings":
		return m.viewSettingsPanel()
	case "askuser":
		return m.viewAskUserPanel()
	case "bgtasks":
		return m.viewBgTasksPanel()
	}
	return ""
}

func (m *cliModel) viewSettingsPanel() string {

	// §20 使用缓存样式
	s := &m.styles
	valueStyle := s.InfoSt
	cursorStyle := s.PanelCursor
	descStyle := s.PanelDesc
	hintStyle := s.PanelHint

	var sb strings.Builder
	sb.WriteString(s.PanelHeader.Render("⚙ Settings"))
	sb.WriteString("\n")
	// 表头下方精致分割线，区分标题与内容
	dividerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Border)).
		Faint(true)
	sb.WriteString(dividerStyle.Render("┈" + strings.Repeat("┈", 30)))

	// Group by category
	lastCat := ""
	for i, def := range m.panelSchema {
		if def.Category != lastCat {
			lastCat = def.Category
			sb.WriteString("\n")
			catStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color(currentTheme.AccentAlt)).
				Bold(true)
			sb.WriteString(catStyle.Render("▸ " + lastCat))
			sb.WriteString("\n")
		}

		cur := m.panelValues[def.Key]
		var prefix string
		if i == m.panelCursor && !m.panelEdit {
			prefix = cursorStyle.Render("▸")
		} else {
			prefix = "  "
		}
		// 选中项：添加微妙的强调色背景提升辨识度
		selectedBgStyle := lipgloss.NewStyle().
			Background(lipgloss.Color(currentTheme.BarEmpty))

		// Format value display
		var displayVal string
		switch def.Type {
		case SettingTypeToggle:
			if cur == "true" {
				displayVal = valueStyle.Render("● ON ")
			} else {
				displayVal = valueStyle.Render("○ OFF")
			}
		case SettingTypeSelect:
			// Find label for current value
			displayVal = cur
			for _, opt := range def.Options {
				if opt.Value == cur {
					displayVal = valueStyle.Render(opt.Label)
					break
				}
			}
		case SettingTypeCombo:
			// Show current value with dropdown hint
			if cur == "" {
				displayVal = descStyle.Render("(未设置)")
			} else {
				displayVal = valueStyle.Render(cur)
			}
			if len(def.Options) > 0 {
				displayVal += descStyle.Render(" ▾")
			}
		default:
			if cur == "" {
				displayVal = descStyle.Render("(未设置)")
			} else {
				displayVal = valueStyle.Render(cur)
			}
		}

		line := fmt.Sprintf("%s %s: %s", prefix, def.Label, displayVal)
		if i == m.panelCursor && !m.panelEdit {
			line = selectedBgStyle.Width(m.width - 6).Render(line)
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Editing overlay
	if m.panelEdit && m.panelCursor < len(m.panelSchema) {
		def := m.panelSchema[m.panelCursor]
		sb.WriteString("\n")
		editLabel := cursorStyle.Render("  ✎ " + def.Label + ": ")
		sb.WriteString(editLabel)
		sb.WriteString(m.panelEditTA.View())
		sb.WriteString("\n")
		sb.WriteString(descStyle.Render("  Enter 确认 · Esc 取消"))
	} else if m.panelCombo && m.panelCursor < len(m.panelSchema) {
		def := m.panelSchema[m.panelCursor]
		sb.WriteString("\n")
		comboTitle := cursorStyle.Render("  ▾ " + def.Label + ":")
		sb.WriteString(comboTitle)
		sb.WriteString("\n")
		maxShow := 8
		start := 0
		if m.panelComboIdx >= maxShow {
			start = m.panelComboIdx - maxShow + 1
		}
		end := start + maxShow
		if end > len(def.Options) {
			end = len(def.Options)
		}
		for j := start; j < end; j++ {
			opt := def.Options[j]
			label := opt.Label
			// Truncate long model names to prevent box overflow
			runes := []rune(label)
			if len(runes) > 40 {
				label = string(runes[:37]) + "..."
			}
			if j == m.panelComboIdx {
				sb.WriteString(cursorStyle.Render("  ▸ " + label))
			} else {
				sb.WriteString("    " + label)
			}
			sb.WriteString("\n")
		}
		sb.WriteString(descStyle.Render("  ↑↓ 选择 · Enter 确认 · 输入自定义 · Esc 取消"))
	} else {
		sb.WriteString("\n")
		sb.WriteString(hintStyle.Render("  ↑↓ 导航 · Enter 编辑/切换 · Ctrl+S 保存 · Esc 关闭"))
	}

	return m.styles.PanelBox.Render(sb.String())
}

func (m *cliModel) viewAskUserPanel() string {

	// §20 使用缓存样式
	s := &m.styles
	questionStyle := s.WarningSt.Bold(true)
	hintStyle := s.PanelHint
	activeTabStyle := s.PanelHeader
	inactiveTabStyle := s.PanelDesc
	checkStyle := s.ToolItem
	cursorStyle := s.PanelCursor
	submitStyle := s.TodoDone

	var sb strings.Builder

	// Tab bar (if multiple questions)
	if len(m.panelItems) > 1 {
		for i := range m.panelItems {
			label := fmt.Sprintf(" %d ", i+1)
			if i == m.panelTab {
				sb.WriteString(activeTabStyle.Render(label))
			} else {
				sb.WriteString(inactiveTabStyle.Render(label))
			}
			if i < len(m.panelItems)-1 {
				sb.WriteString(inactiveTabStyle.Render("│"))
			}
		}
		sb.WriteString("\n\n")
	}

	// Current question
	if m.panelTab >= 0 && m.panelTab < len(m.panelItems) {
		item := m.panelItems[m.panelTab]
		sb.WriteString(questionStyle.Render("❓ " + item.Question))
		sb.WriteString("\n")

		hasOpts := len(item.Options) > 0

		if hasOpts {
			sb.WriteString("\n")
			sel := m.panelOptSel[m.panelTab]
			cursor := m.panelOptCursor[m.panelTab]
			numOpts := len(item.Options)

			for i, opt := range item.Options {
				checked := sel != nil && sel[i]
				var box string
				if checked {
					box = "☑"
				} else {
					box = "☐"
				}
				var line string
				if i == cursor {
					prefix := cursorStyle.Render("▸ ")
					if checked {
						line = checkStyle.Render(prefix + box + " " + opt)
					} else {
						line = prefix + box + " " + opt
					}
				} else {
					if checked {
						line = checkStyle.Render("  " + box + " " + opt)
					} else {
						line = "  " + box + " " + opt
					}
				}
				sb.WriteString(line)
				sb.WriteString("\n")
			}

			// Other input (single-line)
			otherLabel := "Other: "
			if cursor == numOpts {
				sb.WriteString(cursorStyle.Render("▸ ") + otherLabel)
			} else {
				sb.WriteString("  " + otherLabel)
			}
			sb.WriteString(m.panelOtherTI.View())
			sb.WriteString("\n")

			// Submit button
			submitLabel := "Submit →"
			if cursor == numOpts+1 {
				sb.WriteString(cursorStyle.Render("▸ ") + submitStyle.Render(submitLabel))
			} else {
				sb.WriteString("  " + submitStyle.Render(submitLabel))
			}
			sb.WriteString("\n")
		} else {
			sb.WriteString("\n")
			sb.WriteString(m.panelAnswerTA.View())
			sb.WriteString("\n")
		}
	}

	// Hints
	sb.WriteString("\n")
	hints := []string{}
	if len(m.panelItems) > 1 {
		hints = append(hints, "←→/Tab 切换问题")
	}
	if len(m.panelItems) > 0 && m.panelTab < len(m.panelItems) {
		item := m.panelItems[m.panelTab]
		if len(item.Options) > 0 {
			hints = append(hints, "Space/Enter 勾选", "↓ Other 输入", "Enter 提交")
		} else {
			hints = append(hints, "Ctrl+J 换行")
		}
	}
	hints = append(hints, "Esc 取消")
	sb.WriteString(hintStyle.Render("  " + strings.Join(hints, " · ")))

	return m.styles.PanelBox.Render(sb.String())
}

// --- SettingsCapability implementation for CLIChannel ---

// cliSetupSchema returns the settings definitions for the first-run setup wizard.
// Covers core fields needed to get started; other settings available via /settings.
func cliSetupSchema() []SettingDefinition {
	return []SettingDefinition{
		{
			Key:         "llm_provider",
			Label:       "LLM 提供商",
			Description: "选择 LLM 服务提供商",
			Type:        SettingTypeSelect,
			Category:    "LLM",
			Options: []SettingOption{
				{Label: "OpenAI (及兼容 API)", Value: "openai"},
				{Label: "Anthropic (Claude)", Value: "anthropic"},
			},
			DefaultValue: "openai",
		},
		{
			Key:         "llm_api_key",
			Label:       "API Key",
			Description: "LLM 服务的 API Key（必填）",
			Type:        SettingTypeText,
			Category:    "LLM",
		},
		{
			Key:          "llm_base_url",
			Label:        "Base URL",
			Description:  "LLM API 地址",
			Type:         SettingTypeText,
			Category:     "LLM",
			DefaultValue: "https://api.openai.com/v1",
		},
		{
			Key:          "llm_model",
			Label:        "模型名称",
			Description:  "选择或输入 LLM 模型名称",
			Type:         SettingTypeText,
			Category:     "LLM",
			DefaultValue: "gpt-4o",
		},
		{
			Key:         "tavily_api_key",
			Label:       "Tavily API Key",
			Description: "网络搜索服务密钥（可选，留空则无法使用 WebSearch）",
			Type:        SettingTypeText,
			Category:    "LLM",
		},
		{
			Key:         "sandbox_mode",
			Label:       "沙箱模式",
			Description: "命令执行隔离方式",
			Type:        SettingTypeSelect,
			Category:    "环境",
			Options: []SettingOption{
				{Label: "none — 直接执行（推荐）", Value: "none"},
				{Label: "docker — 容器隔离", Value: "docker"},
			},
			DefaultValue: "none",
		},
		{
			Key:         "memory_provider",
			Label:       "记忆模式",
			Description: "记忆系统实现方式",
			Type:        SettingTypeSelect,
			Category:    "环境",
			Options: []SettingOption{
				{Label: "flat — 全量注入（推荐）", Value: "flat"},
				{Label: "letta — 分层记忆", Value: "letta"},
			},
			DefaultValue: "flat",
		},
		{
			Key:         "theme",
			Label:       "配色方案",
			Description: "CLI 界面配色",
			Type:        SettingTypeSelect,
			Category:    "外观",
			Options: []SettingOption{
				{Label: "Midnight（默认）", Value: "midnight"},
				{Label: "Ocean", Value: "ocean"},
				{Label: "Forest", Value: "forest"},
				{Label: "Sunset", Value: "sunset"},
				{Label: "Rose", Value: "rose"},
				{Label: "Mono", Value: "mono"},
			},
			DefaultValue: "midnight",
		},
	}
}

// cliSettingsSchema returns the settings definitions for CLI channel.
func cliSettingsSchema() []SettingDefinition {
	return []SettingDefinition{
		{
			Key:         "llm_model",
			Label:       "LLM 模型",
			Description: "选择或输入 LLM 模型名称",
			Type:        SettingTypeCombo,
			Category:    "LLM",
		},
		{
			Key:         "llm_base_url",
			Label:       "LLM Base URL",
			Description: "LLM API 地址（兼容 OpenAI 格式的第三方服务可修改此项）",
			Type:        SettingTypeText,
			Category:    "LLM",
		},
		{
			Key:         "context_mode",
			Label:       "上下文模式",
			Description: "控制上下文管理策略",
			Type:        SettingTypeSelect,
			Category:    "Agent",
			Options: []SettingOption{
				{Label: "自动（默认）", Value: "auto"},
				{Label: "手动压缩", Value: "manual"},
				{Label: "不压缩", Value: "none"},
			},
			DefaultValue: "auto",
		},
		{
			Key:          "max_iterations",
			Label:        "最大迭代次数",
			Description:  "单次对话最大工具调用迭代次数（默认 100）",
			Type:         SettingTypeNumber,
			Category:     "Agent",
			DefaultValue: "100",
		},
		{
			Key:          "max_concurrency",
			Label:        "最大并发数",
			Description:  "同时处理的最大请求数（默认 3）",
			Type:         SettingTypeNumber,
			Category:     "Agent",
			DefaultValue: "3",
		},
		{
			Key:          "memory_window",
			Label:        "记忆窗口",
			Description:  "LLM 上下文中保留的最大历史消息数（默认 50）",
			Type:         SettingTypeNumber,
			Category:     "Agent",
			DefaultValue: "50",
		},
		{
			Key:          "max_context_tokens",
			Label:        "最大上下文 Token",
			Description:  "上下文最大 token 数（默认 200000）",
			Type:         SettingTypeNumber,
			Category:     "Agent",
			DefaultValue: "200000",
		},
		{
			Key:         "enable_auto_compress",
			Label:       "自动压缩",
			Description: "上下文过长时自动压缩（默认开启）",
			Type:        SettingTypeSelect,
			Category:    "Agent",
			Options: []SettingOption{
				{Label: "开启", Value: "true"},
				{Label: "关闭", Value: "false"},
			},
			DefaultValue: "true",
		},
		{
			Key:         "theme",
			Label:       "配色",
			Description: "CLI 界面配色方案",
			Type:        SettingTypeSelect,
			Category:    "外观",
			Options: []SettingOption{
				{Label: "Midnight（默认）", Value: "midnight"},
				{Label: "Ocean", Value: "ocean"},
				{Label: "Forest", Value: "forest"},
				{Label: "Sunset", Value: "sunset"},
				{Label: "Rose", Value: "rose"},
				{Label: "Mono", Value: "mono"},
			},
			DefaultValue: "midnight",
		},
	}
}

// SettingsSchema returns the settings definitions for CLI channel.
func (c *CLIChannel) SettingsSchema() []SettingDefinition {
	return cliSettingsSchema()
}

// HandleSettingSubmit processes a setting value submission from the CLI channel.
func (c *CLIChannel) HandleSettingSubmit(ctx context.Context, rawInput string) (map[string]string, error) {
	// CLI uses interactive panel, this is for programmatic access
	return nil, fmt.Errorf("CLI uses interactive settings panel")
}

// SetSettingsService injects the settings service for the interactive panel.
func (c *CLIChannel) SetSettingsService(svc SettingsService) {
	c.settingsSvc = svc
}

// SetModelLister injects the model lister for combo settings.
func (c *CLIChannel) SetModelLister(lister ModelLister) {
	c.modelLister = lister
}

// UpdateConfig updates the live LLM configuration (model, base_url).
// These overrides are picked up by the Agent on next message.
func (c *CLIChannel) UpdateConfig(model, baseURL string) {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	if model != "" {
		c.modelOverride = model
	}
	if baseURL != "" {
		c.baseURLOverride = baseURL
	}
}

// GetModelOverride returns the user-overridden model name (empty if not set).
func (c *CLIChannel) GetModelOverride() string {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.modelOverride
}

// GetBaseURLOverride returns the user-overridden base URL (empty if not set).
func (c *CLIChannel) GetBaseURLOverride() string {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.baseURLOverride
}
