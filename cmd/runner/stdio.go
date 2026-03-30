package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// sessionWriteCh and sessionWriteDone provide access to the active session's
// write channel so that stdio goroutines can push async messages to the server.
var (
	sessionWriteCh   chan<- writeMsg
	sessionWriteDone <-chan struct{}
)

type stdioProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	done  chan struct{} // closed when stdout forwarding finishes
}

var (
	stdioMu    sync.Mutex
	stdioProcs = make(map[string]*stdioProcess)
)

func handleStdioStart(msg RunnerMessage) *RunnerMessage {
	var req StdioStartRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", "invalid stdio_start request: "+err.Error())
	}
	if req.StreamID == "" {
		return makeError(msg.ID, "EINVAL", "stream_id is required")
	}

	stdioMu.Lock()
	if _, exists := stdioProcs[req.StreamID]; exists {
		stdioMu.Unlock()
		return makeError(msg.ID, "EEXIST", "stream already exists: "+req.StreamID)
	}
	stdioMu.Unlock()

	cmd, err := buildStdioCmd(req)
	if err != nil {
		return makeError(msg.ID, "EIO", "build command: "+err.Error())
	}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return makeError(msg.ID, "EIO", "stdin pipe: "+err.Error())
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return makeError(msg.ID, "EIO", "stdout pipe: "+err.Error())
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return makeError(msg.ID, "EIO", "stderr pipe: "+err.Error())
	}

	if err := cmd.Start(); err != nil {
		return makeError(msg.ID, "EIO", "start process: "+err.Error())
	}

	proc := &stdioProcess{
		cmd:   cmd,
		stdin: stdinPipe,
		done:  make(chan struct{}),
	}

	stdioMu.Lock()
	stdioProcs[req.StreamID] = proc
	stdioMu.Unlock()

	// Forward stdout → server (stdio_data push messages)
	go stdioForwardOutput(req.StreamID, stdoutPipe, proc)

	// Drain stderr (just log it on runner side)
	go stdioDrainStderr(req.StreamID, stderrPipe)

	// Wait for process exit and notify server
	go stdioWaitExit(req.StreamID, proc)

	log.Printf("  stdio_start stream=%s cmd=%s", req.StreamID, req.Command)
	return makeResponse(msg.ID, ProtoOK, StdioStartResponse{StreamID: req.StreamID})
}

func handleStdioWrite(msg RunnerMessage) {
	var req StdioWriteRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return
	}

	stdioMu.Lock()
	proc, ok := stdioProcs[req.StreamID]
	stdioMu.Unlock()
	if !ok {
		return
	}

	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return
	}
	proc.stdin.Write(data) //nolint:errcheck
}

func handleStdioClose(msg RunnerMessage) *RunnerMessage {
	var req StdioCloseRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", "invalid stdio_close request: "+err.Error())
	}

	stdioMu.Lock()
	proc, ok := stdioProcs[req.StreamID]
	stdioMu.Unlock()
	if !ok {
		return makeError(msg.ID, "ENOENT", "stream not found: "+req.StreamID)
	}

	// Close stdin first (signals EOF to the process).
	proc.stdin.Close()

	// Give process time to exit gracefully, then kill.
	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		proc.cmd.Process.Signal(syscall.SIGTERM) //nolint:errcheck
		select {
		case <-proc.done:
		case <-time.After(3 * time.Second):
			proc.cmd.Process.Kill() //nolint:errcheck
		}
	}

	log.Printf("  stdio_close stream=%s", req.StreamID)
	return makeOK(msg.ID)
}

func buildStdioCmd(req StdioStartRequest) (*exec.Cmd, error) {
	if dockerMode {
		de, ok := executor.(*dockerExecutor)
		if !ok {
			return nil, fmt.Errorf("docker executor not available")
		}

		args := []string{"exec", "-i"}
		dir := req.Dir
		if dir != "" && !filepath.IsAbs(dir) {
			dir = filepath.Join(de.ctrWorkspace, dir)
		}
		if dir == "" {
			dir = de.ctrWorkspace
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
		args = append(args, de.containerName, "sh", "-c", shellCmd)
		return exec.Command("docker", args...), nil
	}
	// Native mode
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

func stdioForwardOutput(streamID string, r io.Reader, proc *stdioProcess) {
	defer close(proc.done)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			encoded := base64.StdEncoding.EncodeToString(buf[:n])
			pushMsg := &RunnerMessage{
				Type: ProtoStdioData,
				Body: mustMarshal(StdioDataMessage{
					StreamID: streamID,
					Data:     encoded,
				}),
			}
			data, _ := json.Marshal(pushMsg)
			select {
			case sessionWriteCh <- writeMsg{data: data}:
			case <-sessionWriteDone:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func stdioDrainStderr(streamID string, r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 && verboseLog {
			log.Printf("  stdio_stderr stream=%s: %s", streamID, strings.TrimSpace(string(buf[:n])))
		}
		if err != nil {
			return
		}
	}
}

func stdioWaitExit(streamID string, proc *stdioProcess) {
	// Wait for stdout forwarding to finish first.
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

	pushMsg := &RunnerMessage{
		Type: ProtoStdioExit,
		Body: mustMarshal(StdioExitMessage{
			StreamID: streamID,
			ExitCode: exitCode,
			Error:    errMsg,
		}),
	}
	data, _ := json.Marshal(pushMsg)
	select {
	case sessionWriteCh <- writeMsg{data: data}:
	case <-sessionWriteDone:
	}

	stdioMu.Lock()
	delete(stdioProcs, streamID)
	stdioMu.Unlock()

	log.Printf("  stdio_exit stream=%s exit=%d", streamID, exitCode)
}

// cleanupStdioProcs kills all active stdio processes (called on session disconnect).
func cleanupStdioProcs() {
	stdioMu.Lock()
	defer stdioMu.Unlock()
	for id, proc := range stdioProcs {
		proc.stdin.Close()
		proc.cmd.Process.Kill() //nolint:errcheck
		log.Printf("  stdio cleanup stream=%s", id)
	}
	stdioProcs = make(map[string]*stdioProcess)
}

func mustMarshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
