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
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"
	"xbot/bus"
	"xbot/llm"
	log "xbot/logger"
	"xbot/tools"
	"xbot/version"
)

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
	c.model.refreshCachedModelName()
	c.model.SetMsgBus(c.msgBus)
	c.model.workDir = c.workDir
	c.model.senderID = "cli_user"
	c.model.chatID = c.config.ChatID

	// i18n: initialize locale from settings
	if c.settingsSvc != nil {
		if vals, err := c.settingsSvc.GetSettings(cliChannelName, "cli_user"); err == nil {
			if lang, ok := vals["language"]; ok {
				SetLocale(lang)
				c.model.locale = GetLocale(lang)
			}
		}
	}

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
		tea.WithOutput(origStdout),
	)
	c.programMu.Unlock()

	// Ctrl+Z 紧急退出：双保险
	// 1) Key event handler (cli_update.go): raw mode 下终端可能直接传 0x1A 字节
	// 2) SIGTSTP 信号兜底: 某些终端 emulator 在 raw mode 下仍发信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTSTP)
	go func() {
		<-sigCh
		// 恢复终端并直接退出，不依赖 bubbletea 的 Quit 流程
		_ = c.program.ReleaseTerminal()
		os.Stdout = origStdout
		os.Stderr = origStderr
		os.Exit(0)
	}()

	// 启动 outbound 消息处理 goroutine
	c.wg.Add(1)
	go c.handleOutbound()

	// §13 异步检查更新（不阻塞 TUI 启动）
	c.CheckUpdateAsync()

	// Runner auto-connect: inject RunnerBridge into model and connect
	if c.runnerAutoConnect != nil {
		c.programMu.Lock()
		if c.model != nil && c.program != nil {
			rb := NewRunnerBridge(c.program)
			c.model.runnerBridge = rb
		}
		c.programMu.Unlock()
		// Delay connection slightly to let TUI render first
		go func() {
			time.Sleep(500 * time.Millisecond)
			c.programMu.Lock()
			model := c.model
			c.programMu.Unlock()
			if model != nil && model.runnerBridge != nil {
				cfg := c.runnerAutoConnect
				model.runnerBridge.Connect(
					cfg.serverURL,
					cfg.token,
					cfg.workspace,
					c.getLLMClient(),
					c.getModelList(),
				)
			}
		}()
	}

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
	// Disconnect runner bridge if active
	c.programMu.Lock()
	if c.model != nil && c.model.runnerBridge != nil {
		c.model.runnerBridge.Disconnect()
	}
	c.programMu.Unlock()
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

// SetTrimHistoryFn 设置 Ctrl+K 截断历史后的数据库同步回调。
// keepCount 为保留的消息数，实现方应删除数据库中更早的消息。
func (c *CLIChannel) SetTrimHistoryFn(fn func(keepCount int) error) {
	c.programMu.Lock()
	defer c.programMu.Unlock()
	if c.model != nil {
		c.model.trimHistoryFn = fn
	}
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
			c.programMu.Lock()
			p := c.program
			c.programMu.Unlock()
			if p != nil {
				p.Send(cliOutboundMsg{msg: msg})
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

// SetRunnerLLM sets the LLM client and model list for the runner bridge.
func (c *CLIChannel) SetRunnerLLM(client llm.LLM, models []string) {
	c.configMu.Lock()
	defer c.configMu.Unlock()
	c.llmClient = client
	c.modelList = models
}

// getLLMClient returns the LLM client for runner use.
func (c *CLIChannel) getLLMClient() llm.LLM {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.llmClient
}

// getModelList returns the available model list for runner use.
func (c *CLIChannel) getModelList() []string {
	c.configMu.RLock()
	defer c.configMu.RUnlock()
	return c.modelList
}

// StartWithRunner starts the CLI channel and auto-connects as runner after TUI initializes.
func (c *CLIChannel) StartWithRunner(shareURL, token, workspace string) error {
	// Wrap the original Start to inject runner bridge before the TUI runs.
	// We set a callback that creates the RunnerBridge after model init.
	c.runnerAutoConnect = &runnerAutoConnectConfig{
		serverURL: shareURL,
		token:     token,
		workspace: workspace,
	}
	return c.Start()
}

// ensureRunnerBridge 确保 RunnerBridge 存在（供 settings 面板使用）。
func (c *CLIChannel) ensureRunnerBridge() {
	c.programMu.Lock()
	defer c.programMu.Unlock()
	if c.model != nil && c.model.runnerBridge == nil && c.program != nil {
		c.model.runnerBridge = NewRunnerBridge(c.program)
	}
}

// runnerAutoConnectConfig holds the auto-connect parameters.
type runnerAutoConnectConfig struct {
	serverURL string
	token     string
	workspace string
}
