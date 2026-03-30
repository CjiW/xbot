// xbot CLI Channel implementation
// A terminal-based chat interface using Bubble Tea TUI framework

package channel

import (
	"fmt"
	"os"
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
	// 可扩展配置项
}

// ---------------------------------------------------------------------------
// CLI Channel (implements Channel interface)
// ---------------------------------------------------------------------------

// CLIChannel CLI 渠道实现
type CLIChannel struct {
	config  CLIChannelConfig
	msgBus  *bus.MessageBus
	msgChan chan bus.OutboundMessage // 接收 agent 回复的通道

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
	inputReady      bool                  // 输入框是否准备好
	streamingMsgIdx int                   // 当前流式消息的索引（-1 表示无流式消息）

	// 进度信息
	progress *CLIProgressPayload
}

// cliMessage 单条消息
type cliMessage struct {
	role      string    // "user" 或 "assistant"
	content   string    // 消息内容
	timestamp time.Time //
	isPartial bool      // 是否为流式部分消息
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
		inputReady:      true,
		streamingMsgIdx: -1,
		progress:        nil,
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

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			// Ctrl+C 退出
			return m, tea.Quit

		case tea.KeyEsc:
			// Esc 退出
			return m, tea.Quit

		case tea.KeyEnter:
			// Enter 发送消息
			content := strings.TrimSpace(m.textarea.Value())
			if content != "" && m.inputReady {
				m.sendMessage(content)
				m.textarea.Reset()
			}
			return m, tea.Batch(cmds...)
		}

	case tea.WindowSizeMsg:
		// 窗口大小变化 - 动态调整布局
		m.handleResize(msg.Width, msg.Height)

	case cliOutboundMsg:
		// 收到 agent 回复
		m.handleAgentMessage(msg.msg)

	case cliProgressMsg:
		// 进度更新
		m.progress = msg.payload
		if msg.payload != nil && msg.payload.Phase == "done" {
			// 进度完成，清除进度信息
			m.progress = nil
		}
		m.updateViewportContent()

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
		Render("🤖 xbot CLI")

	helpText := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#c9ada7")).
		Faint(true).
		Render("Enter 发送 | Ctrl+C 退出")

	titleBar := titleBarBg.Render(
		lipgloss.JoinHorizontal(
			lipgloss.Center,
			titleText,
			strings.Repeat(" ", maxInt(0, m.width-lipgloss.Width(titleText)-lipgloss.Width(helpText)-4)),
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
	
	// 进度状态栏
	var status string
	if m.typing || m.progress != nil {
		// 显示 spinner + 进度信息
		status = thinkingStatusStyle.Render(m.renderProgressStatus(progressStyle, toolStyle))
	} else {
		status = readyStatusStyle.Render("✓ 就绪")
	}

	// 输入区
	input := inputBoxStyle.Render(inputArea)

	// 分隔线
	separator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#3d5a80")).
		Render(strings.Repeat("─", m.width))

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
			sb.WriteString(fmt.Sprintf(" (迭代 %d)", m.progress.Iteration))
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
				sb.WriteString(fmt.Sprintf("  %s %s", emoji, sa.Role))
				if sa.Desc != "" {
					sb.WriteString(fmt.Sprintf(": %s", sa.Desc))
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

// sendMessage 发送用户消息
func (m *cliModel) sendMessage(content string) {
	// 添加用户消息到历史
	m.messages = append(m.messages, cliMessage{
		role:      "user",
		content:   content,
		timestamp: time.Now(),
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
		m.inputReady = false // 禁用输入直到回复完成
	}
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
		} else {
			// 创建新的流式消息
			m.streamingMsgIdx = len(m.messages)
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: true,
			})
		}
	} else {
		// 完整消息
		if m.streamingMsgIdx >= 0 && m.streamingMsgIdx < len(m.messages) {
			// 更新流式消息为完整消息
			m.messages[m.streamingMsgIdx].content = content
			m.messages[m.streamingMsgIdx].isPartial = false
		} else {
			// 新增完整的 assistant 消息
			m.messages = append(m.messages, cliMessage{
				role:      "assistant",
				content:   content,
				timestamp: time.Now(),
				isPartial: false,
			})
		}
		// 重置流式状态
		m.streamingMsgIdx = -1
		m.typing = false
		m.inputReady = true
		// 清除进度信息
		m.progress = nil
	}

	m.updateViewportContent()
}

// updateViewportContent 更新 viewport 显示内容
func (m *cliModel) updateViewportContent() {
	var sb strings.Builder

	// 计算气泡最大宽度（屏幕宽度的 75%）
	maxBubbleWidth := int(float64(m.width) * 0.75)
	if maxBubbleWidth < 30 {
		maxBubbleWidth = 30
	}
	if maxBubbleWidth > 100 {
		maxBubbleWidth = 100
	}

	// 用户消息气泡样式：右对齐、蓝色背景、圆角边框
	userBubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#3d5a80")).
		Background(lipgloss.Color("#1e3a5f")).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1).
		MarginLeft(2).
		Width(maxBubbleWidth).
		Align(lipgloss.Right)

	// Agent 消息气泡样式：左对齐、绿色背景、圆角边框
	assistantBubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#3d6b4f")).
		Background(lipgloss.Color("#1a3d2e")).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1).
		MarginRight(2).
		Width(maxBubbleWidth).
		Align(lipgloss.Left)

	// 流式消息样式（橙色边框提示）
	streamingBubbleStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#ff9800")).
		Background(lipgloss.Color("#2d2d2d")).
		Foreground(lipgloss.Color("#ffffff")).
		Padding(0, 1).
		MarginRight(2).
		Width(maxBubbleWidth).
		Align(lipgloss.Left)

	// 时间戳样式：灰色、淡化效果
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

	for _, msg := range m.messages {
		var rendered string
		var err error

		// 渲染 Markdown（仅对 assistant 消息）
		if msg.role == "assistant" {
			rendered, err = m.renderer.Render(msg.content)
			if err != nil {
				rendered = msg.content // fallback to raw text
			}
			// 清理渲染后的多余空行
			rendered = strings.TrimSpace(rendered)
		} else {
			rendered = msg.content
		}

		// 时间戳
		timeStr := timeStyle.Render(msg.timestamp.Format("15:04:05"))

		// 根据角色渲染气泡
		if msg.role == "user" {
			// 用户消息 - 右对齐
			label := userLabelStyle.Render("👤 You")
			header := lipgloss.NewStyle().
				Width(m.width - 4).
				Align(lipgloss.Right).
				Render(fmt.Sprintf("%s %s", timeStr, label))

			sb.WriteString(header)
			sb.WriteString("\n")

			// 用户气泡（右对齐）
			bubble := userBubbleStyle.Render(rendered)
			bubbleRight := lipgloss.NewStyle().
				Width(m.width).
				Align(lipgloss.Right).
				Render(bubble)
			sb.WriteString(bubbleRight)
		} else {
			// Agent 消息 - 左对齐
			// Agent 消息 - 左对齐
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
	}

	m.viewport.SetContent(sb.String())
	m.viewport.GotoBottom()
}

// tickCmd 定时器命令
func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg {
		return cliTickMsg{}
	})
}

// maxInt 返回两个整数中的较大值
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
