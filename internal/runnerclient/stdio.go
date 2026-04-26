package runnerclient

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"xbot/internal/runnerproto"
)

// stdioProcess 表示一个正在运行的 stdio 进程。
type stdioProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{} // stdout 转发完成时关闭
}

// stdioManager 管理 stdio 进程的生命周期。
type stdioManager struct {
	mu    sync.Mutex
	procs map[string]*stdioProcess

	writeCh    chan<- WriteMsg
	writeDone  <-chan struct{}
	verbose    bool
	dockerMode bool
	executor   Executor
	logf       LogFunc
}

func newStdioManager(verbose, dockerMode bool, logf LogFunc) *stdioManager {
	return &stdioManager{
		procs:      make(map[string]*stdioProcess),
		verbose:    verbose,
		dockerMode: dockerMode,
		logf:       logf,
	}
}

// SetWriteChannels 设置写通道（由 ReadLoop 在启动时调用）。
func (sm *stdioManager) SetWriteChannels(writeCh chan<- WriteMsg, writeDone <-chan struct{}) {
	sm.writeCh = writeCh
	sm.writeDone = writeDone
}

// HandleStart 处理 stdio_start 请求。
func (sm *stdioManager) HandleStart(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.StdioStartRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid stdio_start request: "+err.Error())
	}
	if req.StreamID == "" {
		return runnerproto.MakeError(msg.ID, "EINVAL", "stream_id is required")
	}

	sm.mu.Lock()
	if _, exists := sm.procs[req.StreamID]; exists {
		sm.mu.Unlock()
		return runnerproto.MakeError(msg.ID, "EEXIST", "stream already exists: "+req.StreamID)
	}
	sm.mu.Unlock()

	cmd, err := sm.buildCmd(req)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "build command: "+err.Error())
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "stdin pipe: "+err.Error())
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "stdout pipe: "+err.Error())
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "stderr pipe: "+err.Error())
	}

	if err := cmd.Start(); err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "start process: "+err.Error())
	}

	proc := &stdioProcess{
		cmd:   cmd,
		stdin: stdinPipe,
		done:  make(chan struct{}),
	}

	sm.mu.Lock()
	sm.procs[req.StreamID] = proc
	sm.mu.Unlock()

	// 转发 stdout → server（stdio_data 推送消息）
	go sm.forwardOutput(req.StreamID, stdoutPipe, proc)

	// 排空 stderr（runner 端日志）
	go sm.drainStderr(req.StreamID, stderrPipe)

	// 等待进程退出并通知 server
	go sm.waitExit(req.StreamID, proc)

	callLogf(sm.logf, "  stdio_start stream=%s cmd=%s", req.StreamID, req.Command)
	return runnerproto.MakeResponse(msg.ID, runnerproto.ProtoOK, runnerproto.StdioStartResponse{StreamID: req.StreamID})
}

// HandleWrite 处理 stdio_write 请求（fire-and-forget，无响应）。
func (sm *stdioManager) HandleWrite(msg runnerproto.RunnerMessage) {
	var req runnerproto.StdioWriteRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return
	}

	sm.mu.Lock()
	proc, ok := sm.procs[req.StreamID]
	sm.mu.Unlock()
	if !ok {
		return
	}

	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return
	}
	proc.stdin.Write(data) //nolint:errcheck
}

// HandleClose 处理 stdio_close 请求。
func (sm *stdioManager) HandleClose(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.StdioCloseRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid stdio_close request: "+err.Error())
	}

	sm.mu.Lock()
	proc, ok := sm.procs[req.StreamID]
	sm.mu.Unlock()
	if !ok {
		return runnerproto.MakeError(msg.ID, "ENOENT", "stream not found: "+req.StreamID)
	}

	// 先关闭 stdin（向进程发信号 EOF）
	proc.stdin.Close()

	// 给进程优雅退出的时间，然后 kill
	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		signalProcess(proc.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-proc.done:
		case <-time.After(3 * time.Second):
			proc.cmd.Process.Kill() //nolint:errcheck
		}
	}

	callLogf(sm.logf, "  stdio_close stream=%s", req.StreamID)
	return runnerproto.MakeOK(msg.ID)
}

// buildCmd 根据请求构建命令。
func (sm *stdioManager) buildCmd(req runnerproto.StdioStartRequest) (*exec.Cmd, error) {
	if sm.dockerMode {
		de, ok := sm.executor.(*DockerExecutor)
		if !ok {
			return nil, fmt.Errorf("docker executor not available")
		}

		args := []string{"exec", "-i"}
		dir := req.Dir
		if dir != "" && !filepath.IsAbs(dir) {
			dir = filepath.Join(de.CtrWorkspace, dir)
		}
		if dir == "" {
			dir = de.CtrWorkspace
		}
		if dir != "" {
			args = append(args, "-w", dir)
		}

		hasPath := false
		for _, e := range req.Env {
			args = append(args, "-e", e)
			if strings.HasPrefix(e, "PATH=") {
				hasPath = true
			}
		}
		if !hasPath {
			args = append(args, "-e", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
		}

		shellCmd := "exec " + stdioShellQuote(req.Command, req.Args)
		args = append(args, de.ContainerName, "sh", "-c", shellCmd)
		return exec.Command("docker", args...), nil
	}
	// Native 模式
	cmd := exec.Command(req.Command, req.Args...)
	cmd.Env = append(cmd.Environ(), req.Env...)
	return cmd, nil
}

func stdioShellQuote(command string, args []string) string {
	quote := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	parts := []string{quote(command)}
	for _, a := range args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

func (sm *stdioManager) forwardOutput(streamID string, r io.Reader, proc *stdioProcess) {
	defer close(proc.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			pushMsg := &runnerproto.RunnerMessage{
				Type: runnerproto.ProtoStdioData,
				Body: mustMarshal(runnerproto.StdioDataMessage{
					StreamID: streamID,
					Data:     encoded,
				}),
			}
			data, _ := json.Marshal(pushMsg)
			select {
			case sm.writeCh <- WriteMsg{Data: data}:
			case <-sm.writeDone:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func (sm *stdioManager) drainStderr(streamID string, r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 && sm.verbose {
			callLogf(sm.logf, "  stdio_stderr stream=%s: %s", streamID, strings.TrimSpace(string(buf[:n])))
		}
		if err != nil {
			return
		}
	}
}

func (sm *stdioManager) waitExit(streamID string, proc *stdioProcess) {
	// 先等 stdout 转发完成
	<-proc.done

	exitCode := 0
	errMsg := ""
	if err := proc.cmd.Wait(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
			errMsg = err.Error()
		}
	}

	pushMsg := &runnerproto.RunnerMessage{
		Type: runnerproto.ProtoStdioExit,
		Body: mustMarshal(runnerproto.StdioExitMessage{
			StreamID: streamID,
			ExitCode: exitCode,
			Error:    errMsg,
		}),
	}
	data, _ := json.Marshal(pushMsg)
	select {
	case sm.writeCh <- WriteMsg{Data: data}:
	case <-sm.writeDone:
	}

	sm.mu.Lock()
	delete(sm.procs, streamID)
	sm.mu.Unlock()

	callLogf(sm.logf, "  stdio_exit stream=%s exit=%d", streamID, exitCode)
}

// Cleanup 杀死所有活跃的 stdio 进程（session 断开时调用）。
func (sm *stdioManager) Cleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	for id, proc := range sm.procs {
		proc.stdin.Close()
		proc.cmd.Process.Kill() //nolint:errcheck
		callLogf(sm.logf, "  stdio cleanup stream=%s", id)
	}
	sm.procs = make(map[string]*stdioProcess)
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
