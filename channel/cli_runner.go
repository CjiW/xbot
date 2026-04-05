package channel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"xbot/internal/runnerclient"
	"xbot/llm"
)

// ---------------------------------------------------------------------------
// Runner Status & Stats
// ---------------------------------------------------------------------------

// RunnerStatus 表示 runner 连接状态
type RunnerStatus string

const (
	RunnerDisconnected RunnerStatus = "disconnected"
	RunnerConnecting   RunnerStatus = "connecting"
	RunnerConnected    RunnerStatus = "connected"
)

// RunnerStats 连接统计
type RunnerStats struct {
	ConnectedAt  time.Time
	RequestCount int64
	LastRequest  time.Time
}

// ---------------------------------------------------------------------------
// Bubble Tea Messages (runner → TUI)
// ---------------------------------------------------------------------------

// runnerStatusMsg 通知 TUI runner 连接状态变化
type runnerStatusMsg struct {
	status RunnerStatus
	err    error
}

// ---------------------------------------------------------------------------
// RunnerBridge — 管理 TUI 的 runner 连接生命周期
// ---------------------------------------------------------------------------

// RunnerBridge 管理 TUI 的 runner 连接
type RunnerBridge struct {
	mu        sync.Mutex
	status    RunnerStatus
	serverURL string
	token     string
	workspace string
	stats     RunnerStats

	// 内部状态
	handler *runnerclient.Handler
	stopCh  chan struct{}
	doneCh  chan struct{} // goroutine 退出信号

	// 回调（通过 tea.Cmd 通知 TUI）
	program *tea.Program

	// 日志文件
	logFile *os.File
	logPath string
}

// NewRunnerBridge 创建 RunnerBridge
func NewRunnerBridge(program *tea.Program) *RunnerBridge {
	return &RunnerBridge{
		status:  RunnerDisconnected,
		program: program,
	}
}

// Connect 连接到 server（异步，通过 program.Send 回报结果）
func (rb *RunnerBridge) Connect(serverURL, token, workspace string, llmClient llm.LLM, models []string) {
	rb.mu.Lock()
	if rb.status == RunnerConnected || rb.status == RunnerConnecting {
		rb.mu.Unlock()
		return
	}
	rb.status = RunnerConnecting
	rb.serverURL = serverURL
	rb.token = token
	rb.workspace = workspace
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	rb.stopCh = stopCh
	rb.doneCh = doneCh
	program := rb.program
	rb.mu.Unlock()

	go func() {
		defer close(doneCh)

		// 1. 创建日志文件
		cacheDir, _ := os.UserCacheDir()
		logDir := filepath.Join(cacheDir, "xbot", "runner-logs")
		os.MkdirAll(logDir, 0755)
		ts := time.Now().Format("20060102-150405")
		logPath := filepath.Join(logDir, fmt.Sprintf("runner-%s.log", ts))
		logFile, logErr := os.Create(logPath)
		if logErr != nil {
			logFile = nil // fallback: no logging
		}

		// 2. 创建日志回调
		logf := func(format string, args ...interface{}) {
			if logFile != nil {
				now := time.Now().Format("15:04:05")
				fmt.Fprintf(logFile, "[%s] "+format+"\n", append([]interface{}{now}, args...)...)
			}
		}

		// 保存到 RunnerBridge
		rb.mu.Lock()
		rb.logFile = logFile
		rb.logPath = logPath
		rb.mu.Unlock()

		// 3. 创建 NativeExecutor
		executor := runnerclient.NewNativeExecutor(workspace)

		// 4. 创建 Handler（带日志回调）
		handler := runnerclient.NewHandler(executor, runnerclient.WithLogFunc(logf))

		// 5. 设置 LLM 客户端（如果有）
		if llmClient != nil {
			handler.SetLLMClient(llmClient, models)
		}

		// 6. 解析 userID
		userID := parseUserID(serverURL)
		if userID == "" {
			program.Send(runnerStatusMsg{
				status: RunnerDisconnected,
				err:    fmt.Errorf("cannot parse userID from server URL"),
			})
			return
		}

		// 7. 确保有 ws:// 前缀
		wsURL := serverURL
		if !strings.Contains(wsURL, "://") {
			wsURL = "ws://" + wsURL
		}

		// 8. 连接 server（自报告 LLM 能力）
		shell := runnerclient.DetectShell(false, executor)
		var opts runnerclient.ConnectOptions
		opts.LogFunc = logf
		if handler.LLMProvider() != "" {
			opts.LLMProvider = handler.LLMProvider()
			opts.LLMModel = handler.LLMModel()
		}
		conn, err := runnerclient.Connect(wsURL, userID, token, workspace, shell, opts)
		if err != nil {
			program.Send(runnerStatusMsg{
				status: RunnerDisconnected,
				err:    err,
			})
			return
		}

		// 9. 保存内部状态
		rb.mu.Lock()
		rb.handler = handler
		rb.stats = RunnerStats{
			ConnectedAt: time.Now(),
		}
		rb.status = RunnerConnected
		rb.mu.Unlock()

		// 10. 启动 WritePump + ReadLoop
		writeCh := make(chan runnerclient.WriteMsg, 64)
		writeDone := make(chan struct{})
		handler.SetWriteChannels(writeCh, writeDone)

		// 启动 WritePump
		go runnerclient.WritePump(conn, writeCh, stopCh, writeDone, logf)

		// 启动 ReadLoop（阻塞直到连接断开）
		runnerclient.ReadLoop(conn, handler, writeCh, writeDone, logf)

		// ReadLoop 退出 → 连接断开
		rb.mu.Lock()
		rb.status = RunnerDisconnected
		handler.Cleanup()
		rb.handler = nil
		rb.mu.Unlock()

		program.Send(runnerStatusMsg{
			status: RunnerDisconnected,
			err:    nil,
		})
	}()
}

// Disconnect 断开连接
func (rb *RunnerBridge) Disconnect() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.status != RunnerConnected && rb.status != RunnerConnecting {
		return
	}

	rb.status = RunnerDisconnected

	// 关闭 stopCh → WritePump 退出 → ReadLoop 也会退出
	select {
	case <-rb.stopCh:
		// already closed
	default:
		close(rb.stopCh)
	}

	// 关闭日志文件
	if rb.logFile != nil {
		rb.logFile.Close()
		rb.logFile = nil
	}

	// 清理 handler 资源
	if rb.handler != nil {
		rb.handler.Cleanup()
		rb.handler = nil
	}
}

// Status 返回当前状态
func (rb *RunnerBridge) Status() RunnerStatus {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.status
}

// Stats 返回统计信息
func (rb *RunnerBridge) Stats() RunnerStats {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.stats
}

// ServerURL 返回当前 server URL
func (rb *RunnerBridge) ServerURL() string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.serverURL
}

// Workspace 返回当前 workspace
func (rb *RunnerBridge) Workspace() string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.workspace
}

// LogPath 返回当前日志文件路径。
func (rb *RunnerBridge) LogPath() string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.logPath
}

// parseUserID 从 server URL 中解析 userID
// 例如: ws://host:port/ws/abc123 → abc123
func parseUserID(serverURL string) string {
	// 去掉协议前缀
	u := serverURL
	if idx := strings.Index(u, "://"); idx >= 0 {
		u = u[idx+3:]
	}
	// 取最后一段路径
	if idx := strings.LastIndex(u, "/"); idx >= 0 {
		return u[idx+1:]
	}
	return ""
}
