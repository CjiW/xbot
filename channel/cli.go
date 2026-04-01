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
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
)

func init() {
	// Prevent termenv / lipgloss from querying terminal background color
	// via OSC 11.  Pre-set dark background and TrueColor on the lipgloss
	// default renderer so AdaptiveColor never triggers a lazy query.
	// The termenv default output uses WithTTY(false) so no code path can
	// accidentally send OSC sequences to the real terminal.
	lipgloss.SetHasDarkBackground(true)
	lipgloss.SetColorProfile(termenv.TrueColor)
	termenv.SetDefaultOutput(termenv.NewOutput(os.Stdout, termenv.WithTTY(false)))
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
	Name    string
	Label   string
	Status  string
	Elapsed int64 // milliseconds
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
	WorkDir       string                           // 工作目录（用于标题栏显示）
	ChatID        string                           // 会话 ID（按工作目录区分）
	HistoryLoader func() ([]HistoryMessage, error) // 会话恢复：加载历史消息
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
	program *tea.Program
	model   *cliModel

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup
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
	c.program = tea.NewProgram(c.model,
		tea.WithAltScreen(),
		tea.WithOutput(origStdout),
	)

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
	if c.program != nil {
		c.program.Quit()
	}
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
	todos []CLITodoItem // 从 progress 事件同步的 TODO 列表

	// --- §11 Tool Summary 折叠 ---
	toolSummaryExpanded bool // Ctrl+O 切换
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
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#64b5f6"))
	ta.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0e0e0"))
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.CursorLineNumber = lipgloss.NewStyle()
	ta.FocusedStyle.EndOfBuffer = lipgloss.NewStyle()
	ta.FocusedStyle.LineNumber = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLine = lipgloss.NewStyle()
	ta.BlurredStyle.CursorLineNumber = lipgloss.NewStyle()
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
	tk := newAnimTicker(dotFrames, "#e0af68")

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

	// Ctrl+Enter 换行（终端发送的 raw sequence 不统一，需手动检测）
	if isCtrlEnter(msg) {
		m.textarea.InsertString("\n")
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
				m.sendMessage(content)
				m.textarea.Reset()
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
				m.todos = make([]CLITodoItem, len(msg.payload.Todos))
				copy(m.todos, msg.payload.Todos)
			} else {
				m.todos = nil
			}
			// Detect iteration change: snapshot previous iteration into history
			if msg.payload.Iteration > m.lastSeenIteration && m.lastSeenIteration >= 0 && prev != nil {
				if len(prev.CompletedTools) > 0 || prev.Thinking != "" {
					snap := cliIterationSnapshot{
						Iteration: m.lastSeenIteration,
						Thinking:  prev.Thinking,
						Tools:     append([]CLIToolProgress{}, prev.CompletedTools...),
					}
					m.iterationHistory = append(m.iterationHistory, snap)
				}
				// Clear lastCompletedTools to prevent stale tools from being
				// re-snapshotted when the final iteration is snapshotted in handleAgentMessage.
				m.lastCompletedTools = m.lastCompletedTools[:0]
			}
			m.lastSeenIteration = msg.payload.Iteration

			// §2 工具可视化：快照 CompletedTools 到独立字段
			if len(msg.payload.CompletedTools) > 0 {
				m.lastCompletedTools = append(
					m.lastCompletedTools[:0],
					msg.payload.CompletedTools...,
				)
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
		Background(lipgloss.Color("#4a4e69")).
		Foreground(lipgloss.Color("#f2e9e4")).
		Bold(true).
		Width(m.width).
		Render(titleLeft + strings.Repeat(" ", titlePad) + titleRight)

	// 输入框样式：圆角边框，不设 Background（避免和 textarea ANSI 冲突导致颜色不填满）
	inputBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5c6bc0")).
		Padding(0, 1).
		Width(m.width - 4)

	inputArea := m.textarea.View()

	// 状态栏样式
	readyStatusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#81c784")).
		Bold(true).
		Padding(0, 1)

	thinkingStatusStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ffb74d")).
		Padding(0, 1)

	// 进度样式
	progressStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ffb74d"))

	toolStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#4dd0e1"))

	// ========== 渲染各部分 ==========
	// 分隔线：柔和的虚线
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#2a2a3a")).
		Render(strings.Repeat("─", m.width))

	// 输入区
	input := inputBoxStyle.Render(inputArea)

	// §9 Ctrl+K 确认模式提示
	if m.confirmDelete > 0 {
		warningStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffb74d")).
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

	// All done — hide the bar
	if done == total {
		return ""
	}

	// Progress bar: filled portion
	barWidth := 20
	filled := 0
	if total > 0 {
		filled = done * barWidth / total
	}

	doneStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#81c784"))
	pendingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#e0e0e0"))
	barFilledStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#5c6bc0"))
	barEmptyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#2a2a3a"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#90a4ae"))

	barFilled := strings.Repeat("█", filled)
	barEmpty := strings.Repeat("░", barWidth-filled)

	var sb strings.Builder
	// Header: TODO label + count + progress bar
	sb.WriteString(labelStyle.Render(" TODO "))
	fmt.Fprintf(&sb, "%d/%d ", done, total)
	sb.WriteString(barFilledStyle.Render(barFilled))
	sb.WriteString(barEmptyStyle.Render(barEmpty))
	sb.WriteString("\n")
	// Items
	for i, item := range m.todos {
		text := item.Text
		if utf8.RuneCountInString(text) > 60 {
			text = string([]rune(text)[:59]) + "…"
		}
		if item.Done {
			sb.WriteString("  ")
			sb.WriteString(doneStyle.Render("✓"))
			sb.WriteString(" ")
			sb.WriteString(pendingStyle.Render(text))
		} else {
			sb.WriteString("  ")
			sb.WriteString(labelStyle.Render("○"))
			sb.WriteString(" ")
			sb.WriteString(pendingStyle.Render(text))
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
		m.typing = false
		m.inputReady = true
		// 清除进度信息（保留 TODO，可跨 turn 存活）
		m.progress = nil

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
				m.iterationHistory = append(m.iterationHistory, cliIterationSnapshot{
					Iteration: m.lastSeenIteration,
					Tools:     append([]CLIToolProgress{}, m.lastCompletedTools...),
				})
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
		Foreground(lipgloss.Color("#888888")).
		Bold(true)

	thinkingStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#888888")).
		Italic(true)

	toolDoneStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#81c784"))

	toolRunningStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ffb74d"))

	toolErrorStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ef5350"))

	elapsedStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#888888")).
		Faint(true)

	indentGuide := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#333333"))

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

		// Completed tools in current iteration
		for _, tool := range m.progress.CompletedTools {
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
		Foreground(lipgloss.Color("#5c6bc0")).
		Bold(true)
	header := headerStyle.Render("Progress") + elapsed

	// Wrap in border
	blockStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5c6bc0")).
		Padding(0, 1).
		Width(bubbleWidth)

	return blockStyle.Render(header+"\n"+content) + "\n\n"
}

// renderSubAgentTree renders nested sub-agents with indentation.
func (m *cliModel) renderSubAgentTree(sb *strings.Builder, agents []CLISubAgent, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, sa := range agents {
		icon := m.ticker.viewFrames(waveFrames)
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffb74d"))
		switch sa.Status {
		case "done":
			icon = "✓"
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("#81c784"))
		case "error":
			icon = "✗"
			style = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef5350"))
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
		Foreground(lipgloss.Color("#888888")).
		Faint(true)

	// 角色标签样式
	userLabelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#64b5f6")).
		Bold(true)

	assistantLabelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#81c784")).
		Bold(true)

	streamingLabelStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ffb74d")).
		Bold(true)

	// 系统消息样式
	systemMsgStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#888888")).
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
			BorderForeground(lipgloss.Color("#5c6bc0")).
			Foreground(lipgloss.Color("#e0e0e0")).
			Padding(0, 1).
			Width(contentWidth).
			Align(lipgloss.Left)

		toolHeaderStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4dd0e1")).
			Bold(true)

		toolItemStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#a5d6a7"))

		thinkingStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#90a4ae")).
			Italic(true)

		hintStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#666666"))

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
		// 用户消息：右对齐，左侧竖线指示器
		userStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#e0e0e0")).
			Width(contentWidth).
			Align(lipgloss.Right)
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
		m.viewport.SetContent(sb.String())
		if m.viewport.AtBottom() {
			m.viewport.GotoBottom()
		}
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

	m.viewport.SetContent(sb.String())
	if m.viewport.AtBottom() {
		m.viewport.GotoBottom()
	}
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

	m.viewport.SetContent(sb.String())
	if m.viewport.AtBottom() {
		m.viewport.GotoBottom()
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
