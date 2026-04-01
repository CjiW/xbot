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
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"xbot/bus"
	log "xbot/logger"

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

// ApplyTheme 切换当前配色方案。支持: midnight, ocean, forest, sunset, rose, mono。
// 无效名称回退到 midnight。
func ApplyTheme(name string) {
	if t, ok := themeRegistry[name]; ok {
		currentTheme = t
	} else {
		currentTheme = &themeMidnight
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

// newGlamourRenderer creates a glamour Markdown renderer with Document.Margin
// set to 0 (the default dark style uses Margin=2 which misaligns when lipgloss
// re-wraps lines inside a narrower bubble).
func newGlamourRenderer(wrapWidth int) *glamour.TermRenderer {
	style := glamour.DarkStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero
	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(wrapWidth),
	)
	return r
}

// cliCommands 已知命令列表（用于 Tab 补全，§8）
var cliCommands = []string{
	"/cancel", "/clear", "/compact", "/context", "/exit", "/help",
	"/model", "/models", "/new", "/quit",
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

// CLIChannelConfig CLI 渠道配置
type CLIChannelConfig struct {
	WorkDir          string                           // 工作目录（用于标题栏显示）
	ChatID           string                           // 会话 ID（按工作目录区分）
	HistoryLoader    func() ([]HistoryMessage, error) // 会话恢复：加载历史消息
	GetCurrentValues func() map[string]string         // 获取当前配置值（用于 settings panel 初始值）
	ApplySettings    func(values map[string]string)   // 应用设置变更（写 config.json + 更新运行时状态）
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
type animTicker struct {
	frames []string
	frame  int
	ticks  int64 // total ticks for phase-aware behavior
	style  lipgloss.Style
}

func newAnimTicker(frames []string, color string) *animTicker {
	return &animTicker{
		frames: frames,
		style:  lipgloss.NewStyle().Foreground(lipgloss.Color(color)),
	}
}

func (t *animTicker) tick() {
	t.ticks++
	t.frame = (t.frame + 1) % len(t.frames)
}

func (t *animTicker) view() string {
	return t.style.Render(t.frames[t.frame])
}

// viewFrames renders a frame from a given set using the ticker's current frame index.
func (t *animTicker) viewFrames(frames []string) string {
	idx := t.frame % len(frames)
	return t.style.Render(frames[idx])
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

// tickerTickMsg 是 ticker 定时 tick 消息
type tickerTickMsg struct{}

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

	// --- §9 Ctrl+K 上下文编辑 ---
	confirmDelete int // >0 时处于删除确认状态，值为待删除消息数

	// --- §10 TODO 进度条 ---
	todos            []CLITodoItem // 从 progress 事件同步的 TODO 列表
	todosDoneCleared bool          // 全完成后已被用户输入清除，阻止 progress 重填

	// --- §11 Tool Summary 折叠 ---
	toolSummaryExpanded bool // Ctrl+O 切换

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
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.FocusedStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.BlurredStyle.LineNumber = lipgloss.NewStyle()
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
		renderer:        renderer,
		ready:           false,
		typing:          false,
		streamingMsgIdx: -1,
		progress:        nil,
		inputReady:      true,
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

// Init 初始化
func (m *cliModel) Init() tea.Cmd {
	return textarea.Blink
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
			return m, nil
		}
	}

	// Ctrl+Enter 换行（终端发送的 raw sequence 不统一，需手动检测）
	if isCtrlEnter(msg) {
		m.textarea.InsertString("\n")
		return m, nil
	}

	// Ctrl+O 切换 tool summary 展开/折叠（同上，兼容 CSI u 协议）
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

		case tea.KeyEnter:
			// Enter 发送消息
			if !m.inputReady {
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
			// §9 Ctrl+K 上下文编辑
			if !m.typing && len(m.messages) > 0 {
				m.confirmDelete = 2 // 默认删除 2 条
				m.updateViewportContent()
			}
			return m, nil

		case tea.KeyCtrlO:
			// §11 Ctrl+O 切换 tool summary 展开/折叠
			m.toolSummaryExpanded = !m.toolSummaryExpanded
			m.renderCacheValid = false
			m.cachedHistory = ""
			for i := range m.messages {
				m.messages[i].dirty = true
			}
			m.updateViewportContent()
			return m, nil
		}

		// §9 Ctrl+K 确认模式：拦截字母和数字键
		if m.confirmDelete > 0 {
			switch msg.String() {
			case "y", "Y":
				// 确认删除
				if m.confirmDelete > len(m.messages) {
					m.confirmDelete = len(m.messages)
				}
				m.messages = m.messages[:len(m.messages)-m.confirmDelete]
				m.confirmDelete = 0
				m.renderCacheValid = false
				m.cachedHistory = ""
				m.updateViewportContent()
				return m, nil
			case "n", "N":
				// 取消删除
				m.confirmDelete = 0
				m.updateViewportContent()
				return m, nil
			default:
				// 检查数字键（调整删除数量）
				if msg.Type == tea.KeyRunes {
					runes := msg.Runes
					if len(runes) == 1 && runes[0] >= '1' && runes[0] <= '9' {
						m.confirmDelete = int(runes[0] - '0')
						m.updateViewportContent()
						return m, nil
					}
				}
				// 其他键也取消（包括 Esc）
				m.confirmDelete = 0
				m.updateViewportContent()
				return m, nil
			}
		}

	case tea.WindowSizeMsg:
		// 窗口大小变化 - 动态调整布局
		m.handleResize(msg.Width, msg.Height)

	case cliOutboundMsg:
		// 收到 agent 回复
		m.handleAgentMessage(msg.msg)

	case cliProgressMsg:
		prev := m.progress
		m.progress = msg.payload
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
				m.progress = nil
			}
		}
		m.updateViewportContent()

	case cliTickMsg:
		if m.typing || m.progress != nil {
			cmds = append(cmds, tickCmd())
			m.updateViewportContent()
		}

	case tickerTickMsg:
		// Ticker tick: advance frame and trigger viewport refresh
		if m.typing || m.progress != nil {
			m.ticker.tick()
			cmds = append(cmds, tickerCmd())
			m.updateViewportContent()
		}
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

	// Layout: titleBar(1) + viewport + separator(1) + status(1) + inputBox(5)
	// inputBox = textarea(3) + border_top(1) + border_bottom(1) = 5
	// Total non-viewport = 1 + 1 + 1 + 5 = 8
	reservedLines := 8
	viewportHeight := height - reservedLines
	if viewportHeight < 5 {
		viewportHeight = 5
	}
	m.viewport.Width = width
	m.viewport.Height = viewportHeight

	// inputBoxStyle uses Width(width-4) for content, Padding(0,1) adds 2, Border adds 2.
	// textarea must match the content width exactly.
	m.textarea.SetWidth(width - 4)

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

	// 更新内容
	m.updateViewportContent()

	// Resize 后始终滚到底部（无论之前用户是否上滚）
	m.viewport.GotoBottom()
}

// calculateProgressHeight returns 0 — progress is now rendered inside the viewport.
func (m *cliModel) calculateProgressHeight() int {
	return 0
}

// View 渲染界面
func (m *cliModel) View() string {
	if !m.ready {
		return "\n  初始化中..."
	}

	// ========== 样式定义 ==========

	// 标题栏：纯 ASCII，避免 emoji 导致宽度误算
	titleLeft := m.titleText()
	titleRight := "Enter send | Ctrl+J newline | /help"
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

	// 输入框样式：圆角边框，不设 Background（避免和 textarea ANSI 冲突导致颜色不填满）
	inputBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(currentTheme.Accent)).
		Padding(0, 1).
		Width(m.width - 4)

	inputArea := m.textarea.View()

	// 状态栏样式
	readyStatusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Success)).
		Bold(true).
		Padding(0, 1)

	thinkingStatusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Warning)).
		Padding(0, 1)

	// 进度样式
	progressStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Warning))

	toolStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Info))

	// ========== 渲染各部分 ==========
	// 分隔线：柔和的虚线
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.BarEmpty)).
		Render(strings.Repeat("─", m.width))

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

	// 进度状态栏
	var status string
	if m.typing || m.progress != nil {
		// 显示 spinner + 进度信息
		status = thinkingStatusStyle.Render(m.renderProgressStatus(progressStyle, toolStyle))
	} else {
		status = readyStatusStyle.Render("● ready")
	}

	// 组装界面
	// §12 Panel mode: render panel overlay instead of normal input
	if m.panelMode != "" {
		panel := m.viewPanel()
		return fmt.Sprintf(
			"%s\n%s\n%s",
			titleBar,
			m.viewport.View(),
			panel,
		)
	}

	todoBar := m.renderTodoBar()
	if todoBar != "" {
		return fmt.Sprintf(
			"%s\n%s\n%s\n%s\n%s\n%s",
			titleBar,
			m.viewport.View(),
			separator,
			status,
			todoBar,
			input,
		)
	}
	return fmt.Sprintf(
		"%s\n%s\n%s\n%s\n%s",
		titleBar,
		m.viewport.View(),
		separator,
		status,
		input,
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

	todoLabelSt := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextSecondary))
	todoBarFilledSt := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.BarFilled))
	todoBarEmptySt := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.BarEmpty))
	todoDoneSt := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Success))
	todoPendingSt := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.TextPrimary))

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

// renderProgressStatus renders a compact one-line status for the status bar.
func (m *cliModel) renderProgressStatus(progressStyle, toolStyle lipgloss.Style) string {
	var sb strings.Builder
	sb.WriteString(progressStyle.Render(m.ticker.view()))
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
				sb.WriteString(toolStyle.Render(" · " + label))
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

	return sb.String()
}

// ---------------------------------------------------------------------------
// Helper Methods
// ---------------------------------------------------------------------------

// handleTabComplete 处理 Tab 命令补全（§8）
func (m *cliModel) handleTabComplete() {
	input := strings.TrimSpace(m.textarea.Value())

	// 只在输入以 / 开头时补全
	if !strings.HasPrefix(input, "/") {
		return
	}

	if len(m.completions) == 0 {
		// 首次 Tab：计算匹配
		for _, cmd := range cliCommands {
			if strings.HasPrefix(cmd, input) {
				m.completions = append(m.completions, cmd)
			}
		}
		if len(m.completions) == 0 {
			return
		}
		m.compIdx = 0
	} else {
		// 后续 Tab：循环选择
		m.compIdx = (m.compIdx + 1) % len(m.completions)
	}

	m.textarea.SetValue(m.completions[m.compIdx] + " ")
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

	case "/quit", "/exit":
		m.shouldQuit = true

	case "/help":
		helpContent := `可用命令：
  /cancel    - 取消当前正在执行的操作
  /clear     - 清空聊天记录
  /compact   - 压缩上下文（减少 token 使用）
  /model     - 切换模型（用法: /model <模型名>）
  /models    - 列出可用模型
  /context   - 查看上下文信息
  /new       - 开始新会话
  /exit      - 退出 CLI
  /help      - 显示此帮助信息

快捷键：
  Ctrl+C/Esc - 有迭代时中止，无迭代时清空输入`
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
			// Extract question and options from Metadata
			question := ""
			var options []string
			if msg.Metadata != nil {
				question = msg.Metadata["ask_question"]
				if optsJSON := msg.Metadata["ask_options"]; optsJSON != "" {
					_ = json.Unmarshal([]byte(optsJSON), &options)
				}
			}
			// Fallback: search message history for ❓
			if question == "" {
				for i := len(m.messages) - 1; i >= 0; i-- {
					if strings.HasPrefix(m.messages[i].content, "❓") {
						question = strings.TrimSpace(strings.TrimPrefix(m.messages[i].content, "❓"))
						m.messages = append(m.messages[:i], m.messages[i+1:]...)
						break
					}
				}
			}
			question = strings.TrimSpace(strings.TrimPrefix(question, "Asked user: "))
			if question != "" {
				// Build ask items (support multiple questions separated by \n\n)
				items := m.buildAskItems(question, options)
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

		// §2 工具可视化：生成工具摘要消息（按迭代分组）
		if len(m.iterationHistory) > 0 {
			toolMsg := cliMessage{
				role:       "tool_summary",
				content:    "",
				timestamp:  time.Now(),
				iterations: append([]cliIterationSnapshot{}, m.iterationHistory...),
				dirty:      true,
			}
			insertIdx := len(m.messages) - 1
			if insertIdx < 0 {
				insertIdx = 0
			}
			m.messages = append(m.messages[:insertIdx], append([]cliMessage{toolMsg}, m.messages[insertIdx:]...)...)
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

	// Styles
	iterStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary)).
		Bold(true)

	thinkingStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary)).
		Italic(true)

	toolDoneStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Success))

	toolRunningStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Warning))

	toolErrorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Error))

	elapsedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary)).
		Faint(true)

	indentGuide := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextPrimary))

	dimStyle := lipgloss.NewStyle().
		Faint(true)

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

		// Active tools
		for _, tool := range m.progress.ActiveTools {
			if tool.Status == "done" || tool.Status == "error" {
				continue
			}
			label := tool.Label
			if label == "" {
				label = tool.Name
			}
			line := fmt.Sprintf("  │ %s %s", m.ticker.viewFrames(arrowFrames), label)
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
	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Accent)).
		Bold(true)
	header := headerStyle.Render("Progress") + elapsed

	// Wrap in border
	blockStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(currentTheme.Accent)).
		Padding(0, 1).
		Width(bubbleWidth)

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
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Warning))
		switch sa.Status {
		case "error":
			icon = "✗"
			style = lipgloss.NewStyle().Foreground(lipgloss.Color(currentTheme.Error))
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

// renderMessage 渲染单条消息为 ANSI 字符串（§1 增量渲染：自包含方法）
func (m *cliModel) renderMessage(msg *cliMessage) string {
	var sb strings.Builder

	contentWidth := m.width - 4 // 留边距

	// 时间戳样式
	timeStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary)).
		Faint(true)

	// 角色标签样式
	userLabelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Info)).
		Bold(true)

	assistantLabelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Success)).
		Bold(true)

	streamingLabelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Warning)).
		Bold(true)

	// 系统消息样式
	systemMsgStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary)).
		Italic(true).
		Width(m.width).
		Align(lipgloss.Center)

	// 渲染 Markdown（仅对 assistant 消息）
	var rendered string
	if msg.role == "assistant" {
		var err error
		rendered, err = m.renderer.Render(msg.content)
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
		// §2 工具可视化：按迭代分组渲染 thinking + tools
		toolSummaryStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(currentTheme.Accent)).
			Foreground(lipgloss.Color(currentTheme.TextPrimary)).
			Padding(0, 1).
			Width(contentWidth).
			Align(lipgloss.Left)

		toolHeaderStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color(currentTheme.Info)).
			Bold(true)

		toolItemStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color(currentTheme.Success))

		thinkingStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color(currentTheme.TextSecondary)).
			Italic(true)

		hintStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color(currentTheme.TextMuted))

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
						toolSb.WriteString(toolItemStyle.Render(fmt.Sprintf("    + %s%s", label, elapsed)))
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
					toolSb.WriteString(toolItemStyle.Render(fmt.Sprintf("  + %s%s", label, elapsed)))
					toolSb.WriteString("\n")
				}
			}
		} else {
			// 折叠模式：只显示统计摘要
			elapsedStr := formatElapsed(totalMs)
			toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("Tools %d calls · %s", totalTools, elapsedStr)))
			toolSb.WriteString("  ")
			toolSb.WriteString(hintStyle.Render("[Ctrl+O]"))
		}
		sb.WriteString(toolSummaryStyle.Render(toolSb.String()))
	case "system":
		sb.WriteString(systemMsgStyle.Render(msg.content))
	case "user":
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
		// assistant 消息：左对齐，无气泡边框
		if msg.isPartial {
			label := streamingLabelStyle.Render("Assistant")
			fmt.Fprintf(&sb, "%s %s ...", timeStr, label)
		} else {
			label := assistantLabelStyle.Render("Assistant")
			fmt.Fprintf(&sb, "%s %s", timeStr, label)
		}
		sb.WriteString("\n")
		// Agent 消息直接渲染（glamour 已处理 markdown）
		sb.WriteString(rendered)
	}

	sb.WriteString("\n\n")
	return sb.String()
}

// setViewportContent sets viewport content while preserving scroll position.
// If the user was at the bottom before the update, keep them at the bottom.
func (m *cliModel) setViewportContent(content string) {
	atBottom := m.viewport.AtBottom()
	m.viewport.SetContent(content)
	if atBottom {
		m.viewport.GotoBottom()
	}
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

	for i := range m.messages[:splitIdx] {
		needsRender := m.messages[i].dirty || m.messages[i].renderWidth != m.width
		if needsRender {
			rendered := m.renderMessage(&m.messages[i])
			m.messages[i].rendered = rendered
			m.messages[i].dirty = false
			m.messages[i].renderWidth = m.width
		}
		historyBuf.WriteString(m.messages[i].rendered)
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
	ta.SetWidth(60)
	ta.SetHeight(1)
	ta.CharLimit = 200
	m.panelEditTA = ta
}

// askItem represents a single question in the AskUser panel.
type askItem struct {
	Question string   // the question text
	Options  []string // choices (empty = free input only)
	Answer   string   // user's answer (set on submit)
	Other    string   // user's custom input when "Other" option selected
}

// buildAskItems creates askItem list from question and options.
// Multiple questions in a single call are split by double newline.
// Options with "..." prefix indicate a custom "Other" input.
func (m *cliModel) buildAskItems(question string, options []string) []askItem {
	// Check if question contains multiple questions (separated by double newline)
	parts := strings.Split(question, "\n\n")
	items := make([]askItem, 0, len(parts))
	for _, q := range parts {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		// For multi-question, each gets the same options
		// For single question, use the provided options directly
		if len(parts) == 1 {
			items = append(items, askItem{Question: q, Options: options})
		} else {
			items = append(items, askItem{Question: q, Options: options})
		}
	}
	return items
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
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.FocusedStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.BlurredStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.Text = lipgloss.NewStyle()
	ta.CharLimit = 0
	ta.SetWidth(50)
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")
	ta.Focus()
	m.panelAnswerTA = ta
	// Initialize Other single-line input
	ti := textinput.New()
	ti.Placeholder = "Type here..."
	ti.Prompt = ""
	ti.CharLimit = 200
	ti.Width = 40
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
				ta.CharLimit = 0
				ta.SetWidth(50)
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
				ta.CharLimit = 0
				ta.SetWidth(50)
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
				ta.CharLimit = 0
				ta.SetWidth(50)
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
	}
	return ""
}

func (m *cliModel) viewSettingsPanel() string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(currentTheme.Accent)).
		Padding(1, 2)

	headerStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Success)).
		Bold(true)

	valueStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Info))

	cursorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Warning)).
		Bold(true)

	descStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary)).
		Faint(true)

	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextMuted))

	var sb strings.Builder
	sb.WriteString(headerStyle.Render("⚙ Settings"))
	sb.WriteString("\n\n")

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

	return boxStyle.Render(sb.String())
}

func (m *cliModel) viewAskUserPanel() string {
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(currentTheme.Accent)).
		Padding(1, 2)

	questionStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Warning)).
		Bold(true)

	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextMuted))

	activeTabStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Success)).
		Bold(true)

	inactiveTabStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.TextSecondary))

	checkStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Info))

	cursorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Warning)).
		Bold(true)

	submitStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(currentTheme.Success)).
		Bold(true)

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

	return boxStyle.Render(sb.String())
}

// --- SettingsCapability implementation for CLIChannel ---

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
			Key:         "max_iterations",
			Label:       "最大迭代次数",
			Description: "单次对话最大工具调用迭代次数（默认 100）",
			Type:        SettingTypeNumber,
			Category:    "Agent",
		},
		{
			Key:         "max_concurrency",
			Label:       "最大并发数",
			Description: "同时处理的最大请求数（默认 3）",
			Type:        SettingTypeNumber,
			Category:    "Agent",
		},
		{
			Key:         "memory_window",
			Label:       "记忆窗口",
			Description: "LLM 上下文中保留的最大历史消息数（默认 100）",
			Type:        SettingTypeNumber,
			Category:    "Agent",
		},
		{
			Key:         "max_context_tokens",
			Label:       "最大上下文 Token",
			Description: "上下文最大 token 数（默认 0，表示不限制）",
			Type:        SettingTypeNumber,
			Category:    "Agent",
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
