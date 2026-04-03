package channel

import (
	"os"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/lipgloss"
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
	// --- completions ---
	CompSelected     lipgloss.Style
	CompItem         lipgloss.Style
	CompHint         lipgloss.Style
	CompHintBorder   lipgloss.Style
	// --- view helpers ---
	LineHint         lipgloss.Style
	WarningBold      lipgloss.Style
	PlaceholderSt    lipgloss.Style
	// --- splash ---
	VersionSt        lipgloss.Style
	// --- toast ---
	ToastIcon        lipgloss.Style
	// --- message render ---
	UserDotSep       lipgloss.Style
	UserHeader       lipgloss.Style
	UserContent      lipgloss.Style
	AssistantGuide   lipgloss.Style
	StreamCursor     lipgloss.Style
	// --- settings panel ---
	SettingsDivider  lipgloss.Style
	SettingsCat      lipgloss.Style
	SettingsSelBg    lipgloss.Style
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
	// --- search highlight ---

	// toolDisplayInfo
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
		ToolSummary:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c(t.Accent)).Foreground(c(t.TextPrimary)).Padding(0, 1).Width(cw).Align(lipgloss.Left),
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
		// --- completions ---
		CompSelected:     lipgloss.NewStyle().Bold(true).Underline(true).Foreground(c(t.Success)),
		CompItem:         lipgloss.NewStyle().Foreground(c(t.Success)),
		CompHint:         lipgloss.NewStyle().Padding(0, 1),
		CompHintBorder:   lipgloss.NewStyle().Foreground(c(t.Success)).Padding(0, 1),
		// --- view helpers ---
		LineHint:         lipgloss.NewStyle().Foreground(c(t.TextMuted)).Faint(true),
		WarningBold:      lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true).Padding(0, 1),
		PlaceholderSt:    lipgloss.NewStyle().Foreground(c(t.TextMuted)),
		// --- splash ---
		VersionSt:        lipgloss.NewStyle().Foreground(c(t.TextSecondary)),
		// --- toast ---
		ToastIcon:        lipgloss.NewStyle().Foreground(c(t.Success)).Bold(true),
		// --- message render ---
		UserDotSep:       lipgloss.NewStyle().Foreground(c(t.BarEmpty)),
		UserHeader:       lipgloss.NewStyle(),
		UserContent:      lipgloss.NewStyle().Foreground(c(t.TextPrimary)),
		AssistantGuide:   lipgloss.NewStyle().Foreground(c(t.Accent)),
		StreamCursor:     lipgloss.NewStyle().Foreground(c(t.Warning)).Bold(true),
		// --- settings panel ---
		SettingsDivider:  lipgloss.NewStyle().Foreground(c(t.Border)).Faint(true),
		SettingsCat:      lipgloss.NewStyle().Foreground(c(t.AccentAlt)).Bold(true),
		SettingsSelBg:    lipgloss.NewStyle().Background(c(t.BarEmpty)),
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
	}
}

// applyTAStyles 将缓存样式应用到 textarea 组件
func applyTAStyles(ta *textarea.Model, s *cliStyles) {
	ta.Cursor.Style = s.TACursor
	ta.FocusedStyle.Base = s.TABase
	ta.FocusedStyle.Placeholder = s.TAPlaceholder
	ta.FocusedStyle.CursorLine = s.TACursorLine
	ta.FocusedStyle.LineNumber = s.TALineNumber
	ta.FocusedStyle.EndOfBuffer = s.TAEndOfBuffer
	ta.BlurredStyle.CursorLine = s.TABlurredCursor
	ta.BlurredStyle.LineNumber = s.TABlurredLineNum
	ta.BlurredStyle.EndOfBuffer = s.TABlurredEOB
	ta.BlurredStyle.Text = s.TABlurredText
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

