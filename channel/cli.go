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

	"xbot/bus"
	log "xbot/logger"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
	"github.com/muesli/termenv"
)

func init() {
	// Prevent termenv from querying terminal background color via OSC 11.
	// Without this, termenv sends "\x1b]11;?\x1b\\" on first use, and the
	// terminal's response (e.g. "]11;rgb:1e1e/1e1e/1e1e\") leaks into the
	// textarea stdin and gets displayed as garbled text.
	termenv.SetDefaultOutput(termenv.NewOutput(os.Stdout, termenv.WithTTY(false)))
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	cliSenderID    = "cli_user"
	cliChatID      = cliSenderID
	cliChannelName = "cli"
	cliMsgBufSize  = 100
)

// cliCommands 已知命令列表（用于 Tab 补全，§8）
var cliCommands = []string{
	"/cancel", "/clear", "/compact", "/context", "/help",
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

// ---------------------------------------------------------------------------
// CLI Channel Config
// ---------------------------------------------------------------------------

// CLIChannelConfig CLI 渠道配置
type CLIChannelConfig struct {
	WorkDir string // 工作目录（用于标题栏显示）
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

	// 初始化 Bubble Tea model
	c.model = newCLIModel()
	c.model.SetMsgBus(c.msgBus)
	c.model.workDir = c.workDir

	// 创建 Bubble Tea program
	c.program = tea.NewProgram(c.model,
		tea.WithAltScreen(),       // 使用备用屏幕缓冲区
		tea.WithMouseCellMotion(), // 支持鼠标滚轮
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

// cliModel Bubble Tea 状态模型
type cliModel struct {
	viewport        viewport.Model        // 消息显示区
	textarea        textarea.Model        // 用户输入区
	spinner         spinner.Model         // 进度 spinner
	messages        []cliMessage          // 消息历史
	renderer        *glamour.TermRenderer // Markdown 渲染器
	ready           bool                  // 是否已初始化
	width           int                   // 终端宽度
	height          int                   // 终端高度
	typing          bool                  // agent 是否正在回复
	msgBus          *bus.MessageBus       // 消息总线引用
	streamingMsgIdx int                   // 当前流式消息的索引（-1 表示无流式消息）

	// 进度信息
	progress *CLIProgressPayload

	// 工作目录（标题栏显示用）
	workDir string

	// Smart quit
	pendingQuit bool // Ctrl+C during typing: cancel then quit
	shouldQuit  bool // Flag to quit after current operation completes

	// 输入就绪状态（agent 回复期间禁止发送）
	inputReady bool

	// --- §1 增量渲染 ---
	renderCacheValid bool   // 全局缓存是否有效（resize 后置 false）
	cachedHistory    string // 缓存的历史消息渲染结果（不含当前流式消息）

	// --- §2 工具可视化 ---
	lastCompletedTools []CLIToolProgress // 每轮结束时快照，不依赖 m.progress 生命周期

	// --- §8 Tab 补全 ---
	completions []string // 当前补全候选项
	compIdx     int      // 当前选中的补全索引

	// --- §9 Ctrl+K 上下文编辑 ---
	confirmDelete int // >0 时处于删除确认状态，值为待删除消息数
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
	tools []CLIToolProgress // 仅 role=="tool_summary" 时有值，复用已有类型
}

// newCLIModel 创建 CLI model
func newCLIModel() *cliModel {
	ta := textarea.New()
	ta.Placeholder = "输入消息，Enter 发送..."
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.CharLimit = 0 // 无限制

	// 样式 - 美化输入框
	ta.Prompt = "┃ "
	ta.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#64b5f6"))
	ta.FocusedStyle.Base = lipgloss.NewStyle().Foreground(lipgloss.Color("#e0e0e0"))
	ta.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("#666666"))
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.Color("#1a1a2e"))

	vp := viewport.New(80, 20)

	// Markdown 渲染器（固定暗色主题，避免查询终端背景色导致 OSC 11 响应泄漏到 textarea）
	renderer, _ := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(78),
	)

	// Spinner
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffb74d"))

	return &cliModel{
		viewport:        vp,
		textarea:        ta,
		spinner:         s,
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

// ---------------------------------------------------------------------------
// Bubble Tea Interface Implementation
// ---------------------------------------------------------------------------

// Init 初始化
func (m *cliModel) Init() tea.Cmd {
	// 清空 textarea，吸收可能残留的终端 OSC 响应序列（如 ]11;rgb:...）
	m.textarea.Reset()

	return tea.Batch(
		textarea.Blink, // 光标闪烁
		tickCmd(),      // 启动定时器
	)
}

// Update 处理消息
func (m *cliModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var (
		cmd  tea.Cmd
		cmds []tea.Cmd
	)

	// §8 Tab 补全：记录输入内容变化以重置补全状态
	prevText := m.textarea.Value()

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			// Ctrl+C 智能中断
			if m.typing {
				// Agent 正在处理：发送取消请求，延迟退出
				if m.msgBus != nil {
					m.msgBus.Inbound <- bus.InboundMessage{
						Channel:    cliChannelName,
						SenderID:   cliSenderID,
						ChatID:     cliChatID,
						ChatType:   "p2p",
						Content:    "/cancel",
						SenderName: "CLI User",
						Time:       time.Now(),
						RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
					}
				}
				m.pendingQuit = true
				m.messages = append(m.messages, cliMessage{
					role:      "system",
					content:   "已发送取消请求，等待当前操作完成...",
					timestamp: time.Now(),
					dirty:     true,
				})
				m.updateViewportContent()
				return m, tea.Batch(cmds...)
			}
			// 非处理状态：直接退出
			return m, tea.Quit

		case tea.KeyEsc:
			// Esc 退出
			return m, tea.Quit

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
		// 进度更新（只改状态栏，不触发 viewport 重建）
		m.progress = msg.payload
		if msg.payload != nil {
			// §2 工具可视化：快照 CompletedTools 到独立字段（不依赖 m.progress 生命周期）
			if len(msg.payload.CompletedTools) > 0 {
				m.lastCompletedTools = append(
					m.lastCompletedTools[:0], // 复用底层数组
					msg.payload.CompletedTools...,
				)
			}
			if msg.payload.Phase == "done" {
				m.progress = nil
			}
		}

	case cliTickMsg:
		// 定时刷新
		cmds = append(cmds, tickCmd())
	}

	// 更新 spinner（仅在 typing 状态时）
	if m.typing || m.progress != nil {
		m.spinner, cmd = m.spinner.Update(msg)
		cmds = append(cmds, cmd)
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

	// 动态计算布局比例
	headerHeight := 3
	footerHeight := 5
	progressHeight := 0
	if m.progress != nil {
		progressHeight = m.calculateProgressHeight()
	}

	// 调整 viewport 大小
	viewportHeight := height - headerHeight - footerHeight - progressHeight
	if viewportHeight < 5 {
		viewportHeight = 5 // 最小高度
	}
	m.viewport.Width = width
	m.viewport.Height = viewportHeight

	// 调整 textarea 宽度
	m.textarea.SetWidth(width - 4)

	// 调整 Markdown 渲染器的换行宽度
	if m.renderer != nil && width > 2 {
		// 重新创建渲染器以适应新宽度（固定暗色主题，避免 OSC 查询）
		m.renderer, _ = glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(width-6),
		)
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

// calculateProgressHeight 计算进度信息显示所需的行数
func (m *cliModel) calculateProgressHeight() int {
	if m.progress == nil {
		return 0
	}
	height := 1 // 基础行
	if len(m.progress.ActiveTools) > 0 {
		height += len(m.progress.ActiveTools)
	}
	if len(m.progress.SubAgents) > 0 {
		height += len(m.progress.SubAgents)
	}
	if m.progress.Thinking != "" {
		height += 1
	}
	return height
}

// View 渲染界面
func (m *cliModel) View() string {
	if !m.ready {
		return "\n  初始化中..."
	}

	// ========== 样式定义 ==========

	// 标题栏样式：渐变紫蓝色背景
	titleBarBg := lipgloss.NewStyle().
		Background(lipgloss.Color("#4a4e69")).
		Foreground(lipgloss.Color("#f2e9e4")).
		Bold(true).
		Padding(0, 2).
		Width(m.width)

	titleText := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f2e9e4")).
		Bold(true).
		Render(m.titleText())

	helpText := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#c9ada7")).
		Faint(true).
		Render("Enter 发送 | /help 帮助 | Ctrl+C 退出")

	titleBar := titleBarBg.Render(
		lipgloss.JoinHorizontal(
			lipgloss.Center,
			titleText,
			strings.Repeat(" ", max(0, m.width-lipgloss.Width(titleText)-lipgloss.Width(helpText)-4)),
			helpText,
		),
	)

	// 输入框样式：圆角边框 + 图标
	inputBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#5c6bc0")).
		Background(lipgloss.Color("#1a1a2e")).
		Padding(0, 1).
		Width(m.width - 4)

	inputIcon := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#64b5f6")).
		Bold(true).
		Render("💬 ")

	inputArea := lipgloss.JoinVertical(
		lipgloss.Left,
		inputIcon+m.textarea.View(),
	)

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
	// 分隔线
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#3d5a80")).
		Render(strings.Repeat("─", m.width))

	// 输入区
	input := inputBoxStyle.Render(inputArea)

	// §9 Ctrl+K 确认模式提示
	if m.confirmDelete > 0 {
		warningStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffb74d")).
			Bold(true).
			Padding(0, 1)
		warningText := warningStyle.Render(fmt.Sprintf("⚠ Ctrl+K: 删除最近 %d 条消息？(y/N, 数字调整数量)", m.confirmDelete))
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
		status = readyStatusStyle.Render("✓ 就绪")
	}

	// 组装界面
	return fmt.Sprintf(
		"%s\n%s\n%s\n%s\n%s",
		titleBar,
		m.viewport.View(),
		separator,
		status,
		input,
	)
}

// titleText 生成标题栏文字
func (m *cliModel) titleText() string {
	if m.workDir != "" {
		return fmt.Sprintf("🤖 xbot CLI [%s]", filepath.Base(m.workDir))
	}
	return "🤖 xbot CLI"
}

// renderProgressStatus 渲染进度状态
func (m *cliModel) renderProgressStatus(progressStyle, toolStyle lipgloss.Style) string {
	var sb strings.Builder

	// Spinner + 基础状态
	sb.WriteString(progressStyle.Render(m.spinner.View()))
	sb.WriteString(" ")

	if m.progress != nil {
		// 显示阶段
		switch m.progress.Phase {
		case "thinking":
			sb.WriteString("正在思考...")
		case "tool_exec":
			sb.WriteString("执行工具...")
		case "compressing":
			sb.WriteString("压缩上下文...")
		case "retrying":
			sb.WriteString("重试中...")
		case "done":
			sb.WriteString("完成")
		default:
			sb.WriteString("处理中...")
		}

		// 显示迭代次数
		if m.progress.Iteration > 0 {
			fmt.Fprintf(&sb, " (迭代 %d)", m.progress.Iteration)
		}

		// 显示活跃工具
		if len(m.progress.ActiveTools) > 0 {
			sb.WriteString("\n")
			for _, tool := range m.progress.ActiveTools {
				label := tool.Label
				if label == "" {
					label = tool.Name
				}
				elapsed := ""
				if tool.Elapsed > 0 {
					elapsed = fmt.Sprintf(" (%dms)", tool.Elapsed)
				}
				sb.WriteString("  ")
				sb.WriteString(toolStyle.Render(fmt.Sprintf("⚙ %s%s", label, elapsed)))
				sb.WriteString("\n")
			}
		}

		// 显示子 Agent 状态
		if len(m.progress.SubAgents) > 0 {
			sb.WriteString("\n")
			for _, sa := range m.progress.SubAgents {
				emoji := "🔄"
				switch sa.Status {
				case "done":
					emoji = "✅"
				case "error":
					emoji = "❌"
				}
				fmt.Fprintf(&sb, "  %s %s", emoji, sa.Role)
				if sa.Desc != "" {
					fmt.Fprintf(&sb, ": %s", sa.Desc)
				}
				sb.WriteString("\n")
			}
		}
	} else {
		sb.WriteString("正在思考...")
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
			ChatID:     cliChatID,
			ChatType:   "p2p",
			Content:    content,
			SenderName: "CLI User",
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		}
		m.typing = true
		m.inputReady = false
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
			ChatID:     cliChatID,
			ChatType:   "p2p",
			Content:    content,
			SenderName: "CLI User",
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
		}
		m.typing = true
		m.inputReady = false
	}
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
				ChatID:     cliChatID,
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

	case "/quit":
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
  /quit      - 退出 CLI
  /help      - 显示此帮助信息`
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
				ChatID:     cliChatID,
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

	// 处理飞书图片标签（终端无法渲染，替换为文本占位符）
	content = stripImageTags(content)

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
		// 清除进度信息
		m.progress = nil

		// §2 工具可视化：从独立快照生成工具摘要消息
		if len(m.lastCompletedTools) > 0 {
			toolMsg := cliMessage{
				role:      "tool_summary",
				content:   "",
				timestamp: time.Now(),
				tools:     append([]CLIToolProgress{}, m.lastCompletedTools...), // 复制
				dirty:     true,
			}
			// 插入到当前 assistant 消息之前
			insertIdx := len(m.messages) - 1
			if insertIdx < 0 {
				insertIdx = 0
			}
			m.messages = append(m.messages[:insertIdx], append([]cliMessage{toolMsg}, m.messages[insertIdx:]...)...)
			// 重置快照
			m.lastCompletedTools = nil
			// 由于插入了消息，缓存需要重建
			m.renderCacheValid = false
		}

		// 检查是否需要在操作完成后退出
		if m.pendingQuit {
			m.shouldQuit = true
		}
	}

	m.updateViewportContent()
}

// renderMessage 渲染单条消息为 ANSI 字符串（§1 增量渲染：自包含方法）
func (m *cliModel) renderMessage(msg *cliMessage) string {
	var sb strings.Builder

	// 计算气泡最大宽度
	maxBubbleWidth := int(float64(m.width) * 0.75)
	if maxBubbleWidth < 30 {
		maxBubbleWidth = 30
	}
	if maxBubbleWidth > 100 {
		maxBubbleWidth = 100
	}

	// 用户消息气泡样式
	userBubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#3d5a80")).
		Background(lipgloss.Color("#1e3a5f")).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1).
		MarginLeft(2).
		Width(maxBubbleWidth).
		Align(lipgloss.Right)

	// Agent 消息气泡样式
	assistantBubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#3d6b4f")).
		Background(lipgloss.Color("#1a3d2e")).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1).
		MarginRight(2).
		Width(maxBubbleWidth).
		Align(lipgloss.Left)

	// 流式消息样式
	streamingBubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#ff9800")).
		Background(lipgloss.Color("#2d2d2d")).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1).
		MarginRight(2).
		Width(maxBubbleWidth).
		Align(lipgloss.Left)

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
		// §2 工具可视化：渲染工具调用摘要
		toolSummaryStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#5c6bc0")).
			Background(lipgloss.Color("#1a1a2e")).
			Foreground(lipgloss.Color("#e0e0e0")).
			Padding(0, 1).
			Width(maxBubbleWidth).
			Align(lipgloss.Left)

		toolHeaderStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4dd0e1")).
			Bold(true)

		toolItemStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#a5d6a7"))

		var toolSb strings.Builder
		toolSb.WriteString(toolHeaderStyle.Render(fmt.Sprintf("⚙ 工具调用 (%d)", len(msg.tools))))
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
			toolSb.WriteString(toolItemStyle.Render(fmt.Sprintf("  ✅ %s%s", label, elapsed)))
			toolSb.WriteString("\n")
		}
		sb.WriteString(toolSummaryStyle.Render(toolSb.String()))
	case "system":
		sb.WriteString(systemMsgStyle.Render(msg.content))
	case "user":
		label := userLabelStyle.Render("👤 You")
		header := lipgloss.NewStyle().
			Width(m.width - 4).
			Align(lipgloss.Right).
			Render(fmt.Sprintf("%s %s", timeStr, label))
		sb.WriteString(header)
		sb.WriteString("\n")
		bubble := userBubbleStyle.Render(rendered)
		bubbleRight := lipgloss.NewStyle().
			Width(m.width).
			Align(lipgloss.Right).
			Render(bubble)
		sb.WriteString(bubbleRight)
	default:
		// assistant 消息
		var bubble string
		if msg.isPartial {
			label := streamingLabelStyle.Render("🤖 Assistant")
			header := fmt.Sprintf("%s %s ⚡", timeStr, label)
			sb.WriteString(header)
			bubble = streamingBubbleStyle.Render(rendered)
		} else {
			label := assistantLabelStyle.Render("🤖 Assistant")
			header := fmt.Sprintf("%s %s", timeStr, label)
			sb.WriteString(header)
			bubble = assistantBubbleStyle.Render(rendered)
		}
		sb.WriteString("\n")
		sb.WriteString(bubble)
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

	m.viewport.SetContent(sb.String())
	m.viewport.GotoBottom()
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

	// 拼接最终内容：历史 + 当前流式消息（如有）
	var finalContent string
	if m.streamingMsgIdx >= 0 {
		var sb strings.Builder
		sb.WriteString(m.cachedHistory)
		sb.WriteString(m.renderMessage(&m.messages[m.streamingMsgIdx]))
		finalContent = sb.String()
	} else {
		finalContent = m.cachedHistory
	}

	m.viewport.SetContent(finalContent)
	m.viewport.GotoBottom()
}

// tickCmd 定时器命令
func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return cliTickMsg{}
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
		content = stripImageTags(content)
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
