package channel

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/lipgloss/v2"
	"github.com/muesli/termenv"
	"image/color"
	"os"
)

func init() {
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
	TitleText string // 标题栏文字
	// Surface
	Surface  string // 分隔线/标题栏背景
	Gradient string // 渐变辅助色（分隔线、装饰）
	// Glamour 渲染色（Markdown 内容跟随主题）
	GDocumentText   string // 文档正文色
	GHeadingText    string // 标题色
	GCodeBlock      string // 代码块背景色
	GCodeText       string // 代码文本色
	GLinkText       string // 链接色
	GBlockQuote     string // 引用边框色
	GListItem       string // 列表标记色
	GHorizontalRule string // 水平分隔线色
}

var (
	themeMidnight = cliTheme{
		// Inspired by Material Design Indigo — deep, professional, elegant
		TextPrimary:     "#e8eaed",
		TextSecondary:   "#9aa0a6",
		TextMuted:       "#5f6368",
		Success:         "#81c995",
		Warning:         "#fdd663",
		Error:           "#f28b82",
		Info:            "#8ab4f8",
		Accent:          "#8c9eff",
		AccentAlt:       "#c58af9",
		BarFilled:       "#8c9eff",
		BarEmpty:        "#292a3d",
		Border:          "#3c4043",
		TitleText:       "#e8eaed",
		Surface:         "#1e1f2e",
		Gradient:        "#667eea",
		GDocumentText:   "#e8eaed",
		GHeadingText:    "#8ab4f8",
		GCodeBlock:      "#1e1f2e",
		GCodeText:       "#c9d1d9",
		GLinkText:       "#8ab4f8",
		GBlockQuote:     "#c58af9",
		GListItem:       "#8c9eff",
		GHorizontalRule: "#667eea",
	}
	themeOcean = cliTheme{
		// Deep ocean blues with cyan highlights — calm, focused
		TextPrimary:     "#c3e8f0",
		TextSecondary:   "#6fb3c4",
		TextMuted:       "#3d6b7a",
		Success:         "#5eead4",
		Warning:         "#fbbf24",
		Error:           "#fb7185",
		Info:            "#7dd3fc",
		Accent:          "#22d3ee",
		AccentAlt:       "#67e8f9",
		BarFilled:       "#22d3ee",
		BarEmpty:        "#0f2b3c",
		Border:          "#1e4976",
		TitleText:       "#ecfeff",
		Surface:         "#0c1929",
		Gradient:        "#0ea5e9",
		GDocumentText:   "#c3e8f0",
		GHeadingText:    "#7dd3fc",
		GCodeBlock:      "#0c1929",
		GCodeText:       "#b0d8e8",
		GLinkText:       "#7dd3fc",
		GBlockQuote:     "#67e8f9",
		GListItem:       "#22d3ee",
		GHorizontalRule: "#0ea5e9",
	}
	themeForest = cliTheme{
		// Nordic forest greens — organic, natural, soothing
		TextPrimary:     "#d1e7dd",
		TextSecondary:   "#7dba8a",
		TextMuted:       "#4a6b50",
		Success:         "#86efac",
		Warning:         "#fde68a",
		Error:           "#fca5a5",
		Info:            "#93c5fd",
		Accent:          "#4ade80",
		AccentAlt:       "#a3e635",
		BarFilled:       "#4ade80",
		BarEmpty:        "#0f2419",
		Border:          "#1a4d2e",
		TitleText:       "#dcfce7",
		Surface:         "#0a1f14",
		Gradient:        "#059669",
		GDocumentText:   "#d1e7dd",
		GHeadingText:    "#93c5fd",
		GCodeBlock:      "#0a1f14",
		GCodeText:       "#b8d4c0",
		GLinkText:       "#93c5fd",
		GBlockQuote:     "#a3e635",
		GListItem:       "#4ade80",
		GHorizontalRule: "#059669",
	}
	themeSunset = cliTheme{
		// Warm amber/coral palette — energetic, inviting
		TextPrimary:     "#fef3c7",
		TextSecondary:   "#fdba74",
		TextMuted:       "#78716c",
		Success:         "#fde68a",
		Warning:         "#fdba74",
		Error:           "#fca5a5",
		Info:            "#93c5fd",
		Accent:          "#fb923c",
		AccentAlt:       "#fbbf24",
		BarFilled:       "#fb923c",
		BarEmpty:        "#1c1917",
		Border:          "#44403c",
		TitleText:       "#fffbeb",
		Surface:         "#1c1917",
		Gradient:        "#ea580c",
		GDocumentText:   "#fef3c7",
		GHeadingText:    "#fbbf24",
		GCodeBlock:      "#1c1917",
		GCodeText:       "#fde68a",
		GLinkText:       "#93c5fd",
		GBlockQuote:     "#fbbf24",
		GListItem:       "#fb923c",
		GHorizontalRule: "#ea580c",
	}
	themeRose = cliTheme{
		// Soft pink/magenta — modern, playful, expressive
		TextPrimary:     "#fce7f3",
		TextSecondary:   "#f9a8d4",
		TextMuted:       "#6b4c5e",
		Success:         "#fbcfe8",
		Warning:         "#fdba74",
		Error:           "#fca5a5",
		Info:            "#c4b5fd",
		Accent:          "#f472b6",
		AccentAlt:       "#c084fc",
		BarFilled:       "#f472b6",
		BarEmpty:        "#1f1522",
		Border:          "#4a2040",
		TitleText:       "#fdf2f8",
		Surface:         "#1a0f1e",
		Gradient:        "#db2777",
		GDocumentText:   "#fce7f3",
		GHeadingText:    "#c4b5fd",
		GCodeBlock:      "#1a0f1e",
		GCodeText:       "#f0b8d8",
		GLinkText:       "#c4b5fd",
		GBlockQuote:     "#c084fc",
		GListItem:       "#f472b6",
		GHorizontalRule: "#db2777",
	}
	themeMono = cliTheme{
		// Clean grayscale with red accent — minimalist, hacker aesthetic
		TextPrimary:     "#c9d1d9",
		TextSecondary:   "#8b949e",
		TextMuted:       "#484f58",
		Success:         "#7ee787",
		Warning:         "#e3b341",
		Error:           "#ff7b72",
		Info:            "#79c0ff",
		Accent:          "#f0f6fc",
		AccentAlt:       "#8b949e",
		BarFilled:       "#f0f6fc",
		BarEmpty:        "#21262d",
		Border:          "#30363d",
		TitleText:       "#f0f6fc",
		Surface:         "#161b22",
		Gradient:        "#484f58",
		GDocumentText:   "#c9d1d9",
		GHeadingText:    "#79c0ff",
		GCodeBlock:      "#161b22",
		GCodeText:       "#c9d1d9",
		GLinkText:       "#79c0ff",
		GBlockQuote:     "#8b949e",
		GListItem:       "#f0f6fc",
		GHorizontalRule: "#484f58",
	}
	themeNord = cliTheme{
		// Nord color scheme — arctic, blue-ish, muted elegance
		TextPrimary:     "#d8dee9",
		TextSecondary:   "#81a1c1",
		TextMuted:       "#4c566a",
		Success:         "#a3be8c",
		Warning:         "#ebcb8b",
		Error:           "#bf616a",
		Info:            "#81a1c1",
		Accent:          "#88c0d0",
		AccentAlt:       "#b48ead",
		BarFilled:       "#88c0d0",
		BarEmpty:        "#3b4252",
		Border:          "#434c5e",
		TitleText:       "#eceff4",
		Surface:         "#2e3440",
		Gradient:        "#5e81ac",
		GDocumentText:   "#d8dee9",
		GHeadingText:    "#88c0d0",
		GCodeBlock:      "#2e3440",
		GCodeText:       "#d8dee9",
		GLinkText:       "#81a1c1",
		GBlockQuote:     "#b48ead",
		GListItem:       "#88c0d0",
		GHorizontalRule: "#5e81ac",
	}
	themeDracula = cliTheme{
		// Dracula — iconic dark purple theme, high contrast
		TextPrimary:     "#f8f8f2",
		TextSecondary:   "#bd93f9",
		TextMuted:       "#6272a4",
		Success:         "#50fa7b",
		Warning:         "#f1fa8c",
		Error:           "#ff5555",
		Info:            "#8be9fd",
		Accent:          "#bd93f9",
		AccentAlt:       "#ff79c6",
		BarFilled:       "#bd93f9",
		BarEmpty:        "#21222c",
		Border:          "#44475a",
		TitleText:       "#f8f8f2",
		Surface:         "#1e1f29",
		Gradient:        "#6272a4",
		GDocumentText:   "#f8f8f2",
		GHeadingText:    "#bd93f9",
		GCodeBlock:      "#1e1f29",
		GCodeText:       "#f8f8f2",
		GLinkText:       "#8be9fd",
		GBlockQuote:     "#ff79c6",
		GListItem:       "#bd93f9",
		GHorizontalRule: "#6272a4",
	}

	themeCatppuccin = cliTheme{
		// Catppuccin Mocha — soft pastel dark theme, community favorite
		TextPrimary:     "#cdd6f4", // Text
		TextSecondary:   "#a6adc8", // Overlay0
		TextMuted:       "#585b70", // Overlay2
		Success:         "#a6e3a1", // Green
		Warning:         "#f9e2af", // Yellow
		Error:           "#f38ba8", // Red
		Info:            "#89b4fa", // Blue
		Accent:          "#cba6f7", // Mauve
		AccentAlt:       "#f5c2e7", // Pink
		BarFilled:       "#cba6f7", // Mauve
		BarEmpty:        "#313244", // Surface0
		Border:          "#45475a", // Surface1
		TitleText:       "#cdd6f4", // Text
		Surface:         "#1e1e2e", // Base
		Gradient:        "#89b4fa", // Blue
		GDocumentText:   "#cdd6f4", // Text
		GHeadingText:    "#cba6f7", // Mauve
		GCodeBlock:      "#181825", // Mantle
		GCodeText:       "#cdd6f4", // Text
		GLinkText:       "#89b4fa", // Blue
		GBlockQuote:     "#f5c2e7", // Pink
		GListItem:       "#cba6f7", // Mauve
		GHorizontalRule: "#89b4fa", // Blue
	}

	themeRegistry = map[string]*cliTheme{
		"midnight":   &themeMidnight,
		"ocean":      &themeOcean,
		"forest":     &themeForest,
		"sunset":     &themeSunset,
		"rose":       &themeRose,
		"mono":       &themeMono,
		"nord":       &themeNord,
		"dracula":    &themeDracula,
		"catppuccin": &themeCatppuccin,
	}

	currentTheme = &themeMidnight
)

// ---------------------------------------------------------------------------
// §20 样式缓存系统 — 避免每帧重建 lipgloss.Style（第 7 轮重构）
// ---------------------------------------------------------------------------
// 每个 View() 调用创建 200+ 个 lipgloss.NewStyle() → 改为缓存，只在主题/resize 时重建。

type cliStyles struct {
	TitleBar           lipgloss.Style
	TitleText          lipgloss.Style
	ReadyStatus        lipgloss.Style
	ThinkingSt         lipgloss.Style
	Progress           lipgloss.Style
	Tool               lipgloss.Style
	Separator          lipgloss.Style
	InputBox           lipgloss.Style
	Time               lipgloss.Style
	UserLabel          lipgloss.Style
	AssistLabel        lipgloss.Style
	StreamingLabel     lipgloss.Style
	SystemMsg          lipgloss.Style
	ErrorMsg           lipgloss.Style
	ToolSummary        lipgloss.Style
	ToolHeader         lipgloss.Style
	ToolItem           lipgloss.Style
	ToolErrorItem      lipgloss.Style
	ToolThinking       lipgloss.Style
	ToolReasoning      lipgloss.Style // reasoning chain (more dim than thinking)
	ToolReasoningGuide lipgloss.Style // │ guide for reasoning
	ToolThinkingGuide  lipgloss.Style // │ guide for thinking
	ToolHint           lipgloss.Style
	ProgressHeader     lipgloss.Style
	ProgressIter       lipgloss.Style
	ProgressThinking   lipgloss.Style
	ProgressDone       lipgloss.Style
	ProgressRunning    lipgloss.Style
	ProgressError      lipgloss.Style
	ProgressElapsed    lipgloss.Style
	ProgressIndent     lipgloss.Style
	ProgressDim        lipgloss.Style
	ProgressBlock      lipgloss.Style
	Accent             lipgloss.Style
	TextMutedSt        lipgloss.Style
	WarningSt          lipgloss.Style
	InfoSt             lipgloss.Style
	TokenUsage         lipgloss.Style
	Footer             lipgloss.Style
	ToastBg            lipgloss.Style
	ToastText          lipgloss.Style
	TodoLabel          lipgloss.Style
	TodoFilled         lipgloss.Style
	TodoEmpty          lipgloss.Style
	TodoDone           lipgloss.Style
	TodoPending        lipgloss.Style
	PanelBox           lipgloss.Style
	PanelHeader        lipgloss.Style
	PanelCursor        lipgloss.Style
	PanelDesc          lipgloss.Style
	PanelHint          lipgloss.Style
	PanelDivider       lipgloss.Style
	PanelEmpty         lipgloss.Style
	FileCompDir        lipgloss.Style
	FileCompFile       lipgloss.Style
	FileCompSel        lipgloss.Style
	HelpTitle          lipgloss.Style
	HelpCmd            lipgloss.Style
	HelpDesc           lipgloss.Style
	HelpGroup          lipgloss.Style
	HelpKey            lipgloss.Style
	HelpPanel          lipgloss.Style
	// --- completions ---
	CompSelected   lipgloss.Style
	CompItem       lipgloss.Style
	CompHint       lipgloss.Style
	CompHintBorder lipgloss.Style
	// --- view helpers ---
	LineHint      lipgloss.Style
	WarningBold   lipgloss.Style
	PlaceholderSt lipgloss.Style
	// --- splash ---
	VersionSt lipgloss.Style
	// --- toast ---
	ToastIcon lipgloss.Style
	// --- message render ---
	UserDotSep     lipgloss.Style
	UserHeader     lipgloss.Style
	UserContent    lipgloss.Style
	AssistantGuide lipgloss.Style
	StreamCursor   lipgloss.Style
	// --- settings panel ---
	SettingsDivider lipgloss.Style
	SettingsCat     lipgloss.Style
	SettingsSelBg   lipgloss.Style
	// --- textarea presets ---
	TACursor         lipgloss.Style
	TABase           lipgloss.Style
	TAPlaceholder    lipgloss.Style
	TACursorLine     lipgloss.Style
	TALineNumber     lipgloss.Style
	TAEndOfBuffer    lipgloss.Style
	TABlurredCursor  lipgloss.Style
	TABlurredLineNum lipgloss.Style
	TABlurredEOB     lipgloss.Style
	TABlurredText    lipgloss.Style
	TIPrompt         lipgloss.Style
	TIText           lipgloss.Style
	TICursor         lipgloss.Style
	TIPlaceholder    lipgloss.Style
	// --- key hints (footer) ---
	KeyLabelSt       lipgloss.Style
	KeyDescSt        lipgloss.Style
	ProgressGradient lipgloss.Style
	ProgressGlow     lipgloss.Style

	// --- search (§21) ---
	SearchBar       lipgloss.Style
	SearchIndicator lipgloss.Style

	// toolDisplayInfo
}

func buildStyles(width int) cliStyles {
	t := currentTheme
	c := func(s string) color.Color { return lipgloss.Color(s) }
	cw := width - 4
	if cw < 10 {
		cw = 10
	}
	return cliStyles{
		TitleBar:           lipgloss.NewStyle().Background(c(t.Border)).Foreground(c(t.TitleText)).Bold(true).Width(width),
		TitleText:          lipgloss.NewStyle(),
		ReadyStatus:        lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true).Padding(0, 1),
		ThinkingSt:         lipgloss.NewStyle().Foreground(c(t.Warning)).Padding(0, 1),
		Progress:           lipgloss.NewStyle().Foreground(c(t.Warning)),
		Tool:               lipgloss.NewStyle().Foreground(c(t.Info)),
		Separator:          lipgloss.NewStyle().Foreground(c(t.Gradient)).Background(c(t.Surface)),
		InputBox:           lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1).Width(width - 4),
		Time:               lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		UserLabel:          lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		AssistLabel:        lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true),
		StreamingLabel:     lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		SystemMsg:          lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true).Width(width).Align(lipgloss.Center),
		ErrorMsg:           lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Error)).Foreground(c(t.Error)).Bold(true).Padding(0, 1).Width(cw),
		ToolSummary:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Foreground(c(t.TextPrimary)).Padding(0, 1).Width(cw).Align(lipgloss.Left),
		ToolHeader:         lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		ToolItem:           lipgloss.NewStyle().Foreground(c(t.Success)),
		ToolErrorItem:      lipgloss.NewStyle().Foreground(c(t.Error)),
		ToolThinking:       lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true),
			ToolReasoning:      lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true).Italic(true),
			ToolReasoningGuide: lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true),
			ToolThinkingGuide:  lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		ToolHint:           lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		ProgressHeader:     lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		ProgressIter:       lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Bold(true),
		ProgressThinking:   lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true),
		ProgressDone:       lipgloss.NewStyle().Foreground(c(t.Success)),
		ProgressRunning:    lipgloss.NewStyle().Foreground(c(t.Warning)),
		ProgressError:      lipgloss.NewStyle().Foreground(c(t.Error)),
		ProgressElapsed:    lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		ProgressIndent:     lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		ProgressDim:        lipgloss.NewStyle().Faint(true),
		ProgressBlock:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1).Width(cw),
		Accent:             lipgloss.NewStyle().Foreground(c(t.Accent)),
		TextMutedSt:        lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		WarningSt:          lipgloss.NewStyle().Foreground(c(t.Warning)),
		InfoSt:             lipgloss.NewStyle().Foreground(c(t.Info)),
		TokenUsage:         lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true),
		Footer:             lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		ToastBg:            lipgloss.NewStyle().Width(width).Padding(0, 1),
		ToastText:          lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		TodoLabel:          lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		TodoFilled:         lipgloss.NewStyle().Foreground(c(t.BarFilled)),
		TodoEmpty:          lipgloss.NewStyle().Foreground(c(t.BarEmpty)),
		TodoDone:           lipgloss.NewStyle().Foreground(c(t.Success)),
		TodoPending:        lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		PanelBox:           lipgloss.NewStyle().Width(width).Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1),
		PanelHeader:        lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true),
		PanelCursor:        lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		PanelDesc:          lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Faint(true),
		PanelHint:          lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		PanelDivider:       lipgloss.NewStyle().Foreground(c(t.Border)).Faint(true),
		PanelEmpty:         lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true).Width(width - 8).Align(lipgloss.Center),
		FileCompDir:        lipgloss.NewStyle().Foreground(c(t.Info)),
		FileCompFile:       lipgloss.NewStyle().Foreground(c(t.Info)),
		FileCompSel:        lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true).Underline(true),
		HelpTitle:          lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		HelpCmd:            lipgloss.NewStyle().Foreground(c(t.Info)).Bold(true).Width(12),
		HelpDesc:           lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		HelpGroup:          lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		HelpKey:            lipgloss.NewStyle().Foreground(c(t.TextPrimary)).Bold(true).Width(14),
		HelpPanel:          lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Padding(0, 1).Width(cw),
		// --- completions ---
		CompSelected:   lipgloss.NewStyle().Bold(true).Underline(true).Foreground(c(t.Success)),
		CompItem:       lipgloss.NewStyle().Foreground(c(t.Success)),
		CompHint:       lipgloss.NewStyle().Padding(0, 1),
		CompHintBorder: lipgloss.NewStyle().Foreground(c(t.Success)).Padding(0, 1),
		// --- view helpers ---
		LineHint:      lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true),
		WarningBold:   lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true).Padding(0, 1),
		PlaceholderSt: lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		// --- splash ---
		VersionSt: lipgloss.NewStyle().Foreground(c(t.TextSecondary)).Italic(true),
		// --- toast ---
		ToastIcon: lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true),
		// --- message render ---
		UserDotSep:     lipgloss.NewStyle().Foreground(c(t.Gradient)),
		UserHeader:     lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		UserContent:    lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		AssistantGuide: lipgloss.NewStyle().Foreground(c(t.Gradient)),
		StreamCursor:   lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		// --- settings panel ---
		SettingsDivider: lipgloss.NewStyle().Foreground(c(t.Border)).Faint(true),
		SettingsCat:     lipgloss.NewStyle().Foreground(c(t.AccentAlt)).Bold(true),
		SettingsSelBg:   lipgloss.NewStyle(),
		// --- textarea presets ---
		TACursor:         lipgloss.NewStyle().Foreground(c(t.Info)),
		TABase:           lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		TAPlaceholder:    lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		TACursorLine:     lipgloss.NewStyle(),
		TALineNumber:     lipgloss.NewStyle(),
		TAEndOfBuffer:    lipgloss.NewStyle(),
		TABlurredCursor:  lipgloss.NewStyle(),
		TABlurredLineNum: lipgloss.NewStyle(),
		TABlurredEOB:     lipgloss.NewStyle(),
		TABlurredText:    lipgloss.NewStyle(),
		TIPrompt:         lipgloss.NewStyle(),
		TIText:           lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		TICursor:         lipgloss.NewStyle().Foreground(c(t.Info)),
		TIPlaceholder:    lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		// --- key hints (footer) ---
		KeyLabelSt:       lipgloss.NewStyle().Foreground(c(t.TextMuted)).Bold(true),
		KeyDescSt:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		ProgressGradient: lipgloss.NewStyle().Foreground(c(t.BarFilled)).Bold(true),
		ProgressGlow:     lipgloss.NewStyle().Foreground(c(t.Accent)).Bold(true),
		// --- search (§21) ---
		SearchBar:       lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Info)).Padding(0, 1).Width(width - 4),
		SearchIndicator: lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
	}
}

// applyTAStyles 将缓存样式应用到 textarea 组件
func applyTAStyles(ta *textarea.Model, s *cliStyles) {
	styles := ta.Styles()
	styles.Cursor.Color = s.TACursor.GetForeground()
	styles.Cursor.Blink = false // 禁用光标闪烁：避免 IME 输入时字符因闪烁竞态而视觉消失
	styles.Focused.Base = s.TABase
	styles.Focused.Placeholder = s.TAPlaceholder
	styles.Focused.CursorLine = s.TACursorLine
	styles.Focused.LineNumber = s.TALineNumber
	styles.Focused.EndOfBuffer = s.TAEndOfBuffer
	styles.Blurred.CursorLine = s.TABlurredCursor
	styles.Blurred.LineNumber = s.TABlurredLineNum
	styles.Blurred.EndOfBuffer = s.TABlurredEOB
	styles.Blurred.Text = s.TABlurredText
	ta.SetStyles(styles)
}

// newPanelTextArea creates a configured textarea for panel editing.
func (m *cliModel) newPanelTextArea(value string, width, height int) textarea.Model {
	ta := textarea.New()
	ta.Prompt = "  "
	applyTAStyles(&ta, &m.styles)
	ta.CharLimit = 0
	ta.SetWidth(m.panelWidth(width))
	ta.SetHeight(height)
	ta.SetValue(value)
	ta.CursorEnd()
	ta.Focus()
	return ta
}

// ApplyTheme 切换当前配色方案。支持: midnight, ocean, forest, sunset, rose, mono。
// 无效名称回退到 midnight。变更后通过 themeChangeCh 通知运行中的 model。
var themeChangeCh = make(chan struct{}, 1)

// currentThemeName tracks the active theme name for themeChangeCh handler.
var currentThemeName string

// setTheme 更新 currentTheme 但不发 channel 通知。
// 供 applyThemeAndRebuild 等需要同步完成所有工作的调用方使用，
// 避免后续 Update 周期再触发一次冗余的 fullRebuild。
func setTheme(name string) {
	if t, ok := themeRegistry[name]; ok {
		currentTheme = t
		currentThemeName = name
	} else {
		currentTheme = &themeMidnight
		currentThemeName = "midnight"
	}
}

func ApplyTheme(name string) {
	setTheme(name)
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
