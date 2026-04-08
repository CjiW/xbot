package channel

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/mattn/go-runewidth"
	"strings"
	"sync"
	"time"
	"xbot/bus"
	"xbot/llm"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	cliMsgBufSize = 100
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

// hardWrapRunes wraps a line at maxW columns, breaking at character boundaries.
// ANSI escape sequences are preserved across wrapped segments.
// Returns the original line if it fits within maxW.
func hardWrapRunes(line string, maxW int) string {
	if lipgloss.Width(line) <= maxW {
		return line
	}
	var lines []string
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
			lines = append(lines, buf.String())
			buf.Reset()
			w = 0
		}
		buf.WriteRune(r)
		w += rw
	}
	if buf.Len() > 0 {
		lines = append(lines, buf.String())
	}
	return strings.Join(lines, "\n")
}

// newGlamourRenderer creates a glamour Markdown renderer.
// Document.Margin=0 prevents misalignment inside lipgloss bubbles.
// WordWrap is disabled (set to 0) — viewport's hardWrapRunes handles wrapping
// by rune width, ensuring text never wraps at spaces.
// Color styles follow currentTheme for visual consistency.
func newGlamourRenderer(wrapWidth int) *glamour.TermRenderer {
	t := currentTheme
	c := func(s string) *string { return &s }

	style := styles.DarkStyleConfig
	zero := uint(0)
	style.Document.Margin = &zero

	// 文档正文
	if t.GDocumentText != "" {
		style.Document.Color = c(t.GDocumentText)
	}
	// 标题 (H1–H4)
	if t.GHeadingText != "" {
		style.Heading.Color = c(t.GHeadingText)
		style.H1.Color = c(t.GHeadingText)
		style.H2.Color = c(t.GHeadingText)
		style.H3.Color = c(t.GHeadingText)
		style.H4.Color = c(t.GHeadingText)
	}
	// 代码块背景与文本
	if t.GCodeBlock != "" {
		style.CodeBlock.BackgroundColor = c(t.GCodeBlock)
		if style.CodeBlock.Chroma != nil {
			style.CodeBlock.Chroma.Background.BackgroundColor = c(t.GCodeBlock)
		}
	}
	if t.GCodeText != "" {
		style.CodeBlock.Color = c(t.GCodeText)
		if style.CodeBlock.Chroma != nil {
			style.CodeBlock.Chroma.Text.Color = c(t.GCodeText)
		}
	}
	// 链接
	if t.GLinkText != "" {
		style.Link.Color = c(t.GLinkText)
		style.LinkText.Color = c(t.GLinkText)
	}
	// 引用
	if t.GBlockQuote != "" {
		style.BlockQuote.Color = c(t.GBlockQuote)
		style.BlockQuote.IndentToken = c("│ ")
	}
	// 列表项
	if t.GListItem != "" {
		style.Item.Color = c(t.GListItem)
	}
	// 水平分隔线
	if t.GHorizontalRule != "" {
		style.HorizontalRule.Color = c(t.GHorizontalRule)
	}

	r, _ := glamour.NewTermRenderer(
		glamour.WithStyles(style),
		glamour.WithWordWrap(0),
	)
	return r
}

// cliCommands 已知命令列表（用于 Tab 补全，§8）
var cliCommands = []string{
	"/cancel", "/clear", "/compact", "/context", "/exit", "/help",
	"/new", "/quit", "/search", "/settings", "/setup", "/tasks", "/update",
}

// §19 长消息折叠阈值
const (
	msgFoldThresholdLines = 20
	msgFoldPreviewLines   = 6
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
	Summary   string
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
	WorkDir              string                                                     // 工作目录（用于标题栏显示）
	ChatID               string                                                     // 会话 ID（按工作目录区分）
	HistoryLoader        func() ([]HistoryMessage, error)                           // 会话恢复：加载历史消息
	DynamicHistoryLoader func(channelName, chatID string) ([]HistoryMessage, error) // /su 切换用户后加载目标用户历史
	GetCurrentValues     func() map[string]string                                   // 获取当前配置值（用于 settings panel 初始值）
	ApplySettings        func(values map[string]string)                             // 应用设置变更（写 config.json + 更新运行时状态）
	IsFirstRun           bool                                                       // 首次运行标志，TUI 启动时自动打开 setup panel
	ClearMemory          func(targetType string) error                              // 清空记忆（danger zone）
	GetMemoryStats       func() map[string]string                                   // 获取记忆统计（danger zone）
	SwitchLLM            func(provider, baseURL, apiKey, model string) error        // 切换活跃 LLM（config + factory + save）
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
	settingsSvc SettingsService // interface for GetSettings/SetSetting
	configMu    sync.RWMutex    // protects runner LLM fields (llmClient, modelList, llmProvider)
	modelLister ModelLister     // provides available model names for combo

	// Multi-subscription management
	subscriptionMgr SubscriptionManager // manages LLM subscriptions
	llmSubscriber   LLMSubscriber       // switches active LLM (propagated to model)

	// Background tasks
	bgTaskMgr *tools.BackgroundTaskManager

	// Runner LLM access
	llmClient    llm.LLM
	modelList    []string
	llmProvider  string
	bgSessionKey string

	runnerAutoConnect *runnerAutoConnectConfig // auto-connect as runner after TUI init

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

// Subscription represents a LLM subscription for display/selection.
type Subscription struct {
	ID       string
	Name     string
	Provider string
	BaseURL  string
	APIKey   string
	Model    string
	Active   bool
}

// SubscriptionManager manages user LLM subscriptions.
type SubscriptionManager interface {
	List(senderID string) ([]Subscription, error)
	GetDefault(senderID string) (*Subscription, error)
	Add(sub *Subscription) error
	Remove(id string) error
	SetDefault(id string) error
	SetModel(id, model string) error
	Rename(id, name string) error
}

// LLMSubscriber switches the active LLM for a user (called when subscription changes).
type LLMSubscriber interface {
	SwitchSubscription(senderID string, sub *Subscription) error
	SwitchModel(senderID, model string)
	GetDefaultModel() string
}

// NewCLIChannel 创建 CLI 渠道
