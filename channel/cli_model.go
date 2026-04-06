package channel

import (
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"fmt"
	"github.com/charmbracelet/glamour"
	"time"
	"xbot/bus"
	"xbot/tools"
	"xbot/version"
)

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
	// waveFrames: rotating crescent moon phases — subagent feel
	waveFrames = []string{"◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒", "◐", "◓", "◑", "◒"}
	// orbitFrames: spinning orbit — processing feel
	orbitFrames = []string{"◌", "◔", "◕", "●", "◕", "◔", "◌", "◔", "◕", "●", "◕", "◔"}
	// splashFrames: loading bar animation — 启动画面进度条
	splashFrames = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	// pulseFrames: pulsing circle — tool completion pulse
	pulseFrames = []string{"◌", "◎", "◉", "◎", "◌"}
)

// errorKeywords — system 消息中的错误检测关键词
var errorKeywords = []string{"error", "failed", "失败", "错误", "exception", "denied", "refused"}

// pickVerb returns a deterministic verb based on tick count (changes every ~2s at 10 FPS).
func (m *cliModel) pickVerb(ticks int64) string {
	verbs := m.locale.ThinkingVerbs
	if len(verbs) == 0 {
		return "Thinking"
	}
	idx := (ticks / 20) % int64(len(verbs))
	return verbs[idx]
}

// pickIdlePlaceholder 根据时间返回轮换的 placeholder（每 5 秒切换）
func (m *cliModel) pickIdlePlaceholder() string {
	placeholders := m.locale.IdlePlaceholders
	if len(placeholders) == 0 {
		return ""
	}
	idx := int(time.Now().Unix()/5) % len(placeholders)
	return placeholders[idx]
}

// updatePlaceholder refreshes the placeholder text based on typing state.
// We store it in m.placeholderText instead of m.textarea.Placeholder to avoid
// CJK rendering bugs caused by textarea's internal placeholder↔normal view switch.
func (m *cliModel) updatePlaceholder() {
	if m.typing {
		m.placeholderText = m.locale.ProcessingPlaceholder
	} else {
		m.placeholderText = m.pickIdlePlaceholder()
	}
}

// tickerTickMsg 是 ticker 定时 tick 消息
type tickerTickMsg struct{}

// splashTickMsg 启动画面定时 tick 消息
type splashTickMsg struct {
	frame int // 当前帧索引
}

// splashDoneMsg 启动画面结束消息
type splashDoneMsg struct{}

// suHistoryLoadMsg /su 切换用户后的历史加载完成消息
type suHistoryLoadMsg struct {
	history []HistoryMessage
	err     error
}

// cliToastItem 单条 Toast 通知数据
type cliToastItem struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastMsg Toast 通知消息（入队显示，自动消失）
type cliToastMsg struct {
	text string
	icon string // "✓" | "✗" | "ℹ" 等
}

// cliToastClearMsg Toast 通知自动清除消息（弹出队列头部）
type cliToastClearMsg struct{}

// cliModel Bubble Tea 状态模型
type cliModel struct {
	// --- Core UI ---
	viewport      viewport.Model // 消息显示区
	panelViewport viewport.Model // Panel 内容区（与主 viewport 统一滚动逻辑）
	textarea      textarea.Model // 用户输入区

	// §22 输入历史
	inputHistory    []string    // 已发送输入历史（新 → 旧），仅会话内
	inputHistoryIdx int         // -1 = 不在浏览模式, >=0 = 当前浏览索引
	inputDraft      string      // 进入历史浏览前的输入草稿
	ticker          *animTicker // 进度动画 ticker
	width           int         // 终端宽度
	height          int         // 终端高度
	styles          cliStyles
	locale          *UILocale // i18n: current UI locale

	// §23 Placeholder: stored separately from textarea to avoid CJK rendering bug.
	// Textarea's built-in Placeholder causes a view-mode switch (placeholder→normal)
	// that triggers cellbuf incremental diff issues on Windows Terminal with CJK chars.
	placeholderText string // current placeholder string to display in View

	// --- Message state ---
	messages        []cliMessage          // 消息历史
	renderer        *glamour.TermRenderer // Markdown 渲染器
	streamingMsgIdx int                   // 当前流式消息的索引（-1 表示无流式消息）
	newContentHint  bool                  // 有新内容但用户未在底部（显示 ↓ 提示）
	ready           bool                  // 是否已初始化

	// --- Agent state ---
	typing          bool                      // agent 是否正在回复
	typingStartTime time.Time                 // 本次处理开始时间
	inputReady      bool                      // 输入就绪状态（agent 回复期间禁止发送）
	msgBus          *bus.MessageBus           // 消息总线引用
	tempStatus      string                    // 临时状态提示（自动过期）
	shouldQuit      bool                      // Smart quit: quit after current operation completes
	trimHistoryFn   func(keepCount int) error // Ctrl+K 确认删除后回调：截断数据库中的 session messages

	// --- Message queue (typing 期间排队的消息) ---
	messageQueue   []string // 排队等待发送的消息
	queueEditing   bool     // true = 正在编辑/查看最后一条排队消息
	queueEditBuf   string   // 编辑中的排队消息内容
	needFlushQueue bool     // true = handleAgentMessage 后需要刷新队列

	// --- Background tasks ---
	bgTaskCount   int        // running background tasks (0 = no indicator)
	bgTaskCountFn func() int // callback to get current bg task count (set by channel)

	// --- Progress ---
	progress          *CLIProgressPayload
	iterationHistory  []cliIterationSnapshot // 已完成迭代快照
	lastSeenIteration int                    // 上次进度事件的迭代号

	// --- Session ---
	workDir       string // 工作目录（标题栏显示用）
	senderID      string // 当前身份 ID（默认 "cli_user"，/su 命令可切换）
	channelName   string // 当前 channel（默认 "cli"，/su 切换时可能变为 "web"）
	defaultChatID string // 默认 chatID（/su 切换回来时恢复）
	chatID        string // 会话 ID（按工作目录区分）

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

	// --- Danger Zone Panel ---
	panelDangerItems   []dangerItem
	panelDangerCursor  int
	panelDangerConfirm bool // true = showing confirm input
	panelDangerInput   textinput.Model
	panelDangerOnExec  func(targetType string) error // callback to execute clear

	// --- §13 Update Check ---

	// --- Runner Panel ---
	panelRunnerServerTI  textinput.Model     // server URL 输入
	panelRunnerTokenTI   textinput.Model     // token 输入
	panelRunnerWorkspace textinput.Model     // workspace 输入
	panelRunnerEditField int                 // 当前编辑字段 (0=server, 1=token, 2=workspace)
	updateNotice         *version.UpdateInfo // nil=nothing, non-nil=show notice
	checkingUpdate       bool                // true while /update is in progress

	// --- §14 Splash 画面 ---
	splashDone  bool // true = splash 动画结束，进入正常界面
	splashFrame int  // 当前 splash 动画帧索引
	suLoading   bool // true = /su 切换用户后正在加载历史，显示 loading 画面

	// --- §16 Toast 通知队列 ---
	toasts     []cliToastItem // Toast 队列（头部=当前显示）
	toastTimer bool           // true = toast 消除计时器已启动

	// --- §19 长消息折叠 ---
	msgLineOffsets []int // 每条消息在 viewport 折行后 content 中的起始行号

	// --- §21 消息搜索 /search ---
	searchMode    bool            // 是否处于搜索模式
	searchQuery   string          // 搜索关键词
	searchResults []int           // 匹配的消息索引列表
	searchIdx     int             // 当前导航到的搜索结果索引（-1 = 未选择）
	searchEditing bool            // true = 编辑搜索词, false = 导航结果
	searchTI      textinput.Model // 搜索输入框

	// toolDisplayInfo

	// --- 🥚 Easter Eggs 彩蛋 ---
	easterEgg       easterEggMode // 当前激活的彩蛋类型（"" = 无）
	easterEggCustom string        // 彩蛋自定义内容（版本成就 art 等）
	konamiBuffer    []string      // Konami Code 按键缓冲区
	matrixCols      int           // Matrix 代码雨列数
	matrixRows      int           // Matrix 代码雨行数
	matrixDrops     []int         // Matrix 每列头部位置
	matrixSpeeds    []int         // Matrix 每列下落速度
	matrixTrailLen  []int         // Matrix 每列拖尾长度
	matrixBuffer    [][]rune      // Matrix 字符缓冲区
	versionHitTimes []time.Time   // /version 命令调用时间戳（三连检测）

	channel         *CLIChannel // back-reference to owning channel (set during Start)
	cachedModelName string      // cached model name for View() performance

	// === Runner Bridge ===
	runnerBridge *RunnerBridge
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

	// --- §19 长消息折叠 ---
	renderedLines         int  // 渲染后的总行数（每次 dirty 重算）
	originalRenderedLines int  // fold 前的原始行数（fold 时保存，用于 unfold 判断）
	folded                bool // 是否折叠
}

// newCLIModel 创建 CLI model
func newCLIModel() *cliModel {
	ta := textarea.New()
	ta.Placeholder = "" // disabled; placeholder rendered in View() to avoid CJK bug
	ta.Focus()
	ta.SetWidth(72)
	ta.CharLimit = 0
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	// Enable DynamicHeight so textarea auto-grows/shrinks based on visual lines
	// (including soft wraps from CJK characters). This replaces our manual autoExpandInput.
	ta.DynamicHeight = true
	ta.MinHeight = minTaHeight
	ta.MaxHeight = maxTaHeight
	ta.SetHeight(minTaHeight)
	initStyles := buildStyles(76)
	applyTAStyles(&ta, &initStyles)

	// Enter = send, Ctrl+Enter/Ctrl+J = newline (Ctrl+Enter raw sequences vary by terminal)
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j")

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))

	// Panel viewport — 与主 viewport 统一滚动逻辑
	panelVP := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))

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
		panelViewport:   panelVP,
		textarea:        ta,
		ticker:          tk,
		placeholderText: GetLocale(currentLocaleLang).IdlePlaceholders[0],
		messages:        make([]cliMessage, 0, cliMsgBufSize),
		styles:          buildStyles(80),
		renderer:        renderer,
		ready:           false,
		typing:          false,
		streamingMsgIdx: -1,
		progress:        nil,
		inputReady:      true,
		locale:          GetLocale(""),
		inputHistory:    make([]string, 0, 100),
		inputHistoryIdx: -1,
		inputDraft:      "",
		senderID:        "cli_user",
		channelName:     "cli",
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

// idleTickMsg 低频定时刷新（用于 placeholder 轮转）
type idleTickMsg struct{}

// cliTempStatusClearMsg 临时状态提示自动清除
type cliTempStatusClearMsg struct{}

// cliSettingsSavedMsg settings save completed (async callback result)
type cliSettingsSavedMsg struct {
	themeChanged bool
	theme        string
	langChanged  bool
	lang         string
	modelChanged bool
	model        string
	baseURL      string
	feedbackMsg  string
}

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

// refreshCachedModelName caches the current model name to avoid repeated lookups in View().
// Should be called after channel init, config changes, and settings saves.
func (m *cliModel) refreshCachedModelName() {
	if m.channel == nil {
		return
	}
	m.channel.configMu.RLock()
	if m.channel.modelOverride != "" {
		m.cachedModelName = m.channel.modelOverride
	}
	m.channel.configMu.RUnlock()
	if m.cachedModelName == "" {
		if m.channel.config.GetCurrentValues != nil {
			m.cachedModelName = m.channel.config.GetCurrentValues()["llm_model"]
		}
		if m.cachedModelName == "" && m.channel.settingsSvc != nil {
			if vals, err := m.channel.settingsSvc.GetSettings("cli", "cli_user"); err == nil {
				m.cachedModelName = vals["llm_model"]
			}
		}
	}
}

// Init 初始化 — 启动 splash 画面动画（最小展示 1 秒）
func (m *cliModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.splashTick(0))
}

// splashTick 生成启动画面动画的 tick 命令
func (m *cliModel) splashTick(frame int) tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return splashTickMsg{frame: frame + 1}
	})
}

// suLoadHistoryCmd 异步加载 /su 目标用户的历史消息
func (m *cliModel) suLoadHistoryCmd() tea.Cmd {
	chatID := m.chatID
	loader := m.channel.config.DynamicHistoryLoader
	if loader == nil {
		return func() tea.Msg { return suHistoryLoadMsg{err: fmt.Errorf("no dynamic history loader")} }
	}
	return func() tea.Msg {
		history, err := loader("", chatID)
		return suHistoryLoadMsg{history: history, err: err}
	}
}
