package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	log "xbot/logger"
)

// NoneSandbox implements Sandbox with direct os.* calls (no containerization).
type NoneSandbox struct{}

// maxNoneDownloadSize is the maximum download size for NoneSandbox (100MB).
const maxNoneDownloadSize = 100 * 1024 * 1024

// noneDownloadHTTPClient is a dedicated HTTP client for NoneSandbox downloads.
var noneDownloadHTTPClient = &http.Client{Timeout: 0} // use context timeout

func (s *NoneSandbox) Name() string              { return "none" }
func (s *NoneSandbox) Workspace(_ string) string { return "" }

func (s *NoneSandbox) Close() error                        { return nil }
func (s *NoneSandbox) CloseForUser(userID string) error    { return nil }
func (s *NoneSandbox) IsExporting(userID string) bool      { return false }
func (s *NoneSandbox) ExportAndImport(userID string) error { return nil }

func (s *NoneSandbox) GetShell(userID string, workspace string) (string, error) {
	return "/bin/bash", nil
}

func (s *NoneSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	// Apply timeout to context before creating the command (avoid duplicate cmd creation).
	if spec.Timeout > 0 && !spec.KeepAlive {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	// KeepAlive uses unmanaged cmd (exec.Command) so context cancel doesn't kill the process.
	cmd, err := buildCmdFromSpec(ctx, spec, !spec.KeepAlive)
	if err != nil {
		return nil, err
	}

	// Always use process group so we can kill the entire tree on cancel.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	} else {
		// Ensure stdin is never nil — prevents commands (e.g. sudo) from
		// opening /dev/tty and blocking the terminal in none-sandbox mode.
		// In docker/remote sandboxes the process is isolated so this isn't needed.
		cmd.Stdin = bytes.NewReader(nil)
	}

	// KeepAlive mode: use pipes so we can detach on timeout without killing the process.
	if spec.KeepAlive {
		return s.execKeepAlive(ctx, cmd, spec.Timeout)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	result := &ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			result.ExitCode = -1
			result.TimedOut = true
		} else {
			killProcessGroup(cmd.Process) // clean up on unexpected errors too
			return nil, err
		}
	}

	// Always kill the process group to clean up any orphaned children
	// (e.g. "go run" leaves compiled binary running after parent exits).
	killProcessGroup(cmd.Process)

	return result, nil
}

// execKeepAlive runs a command with streaming output via pipes.
// On timeout, the process is NOT killed — it continues running and
// the caller takes ownership via ExecResult.Process.
func (s *NoneSandbox) execKeepAlive(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (*ExecResult, error) {
	// Setpgid so we can kill the process group independently
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, stderrPipe, err := setupPipes(cmd)
	if err != nil {
		return nil, err
	}

	// Collect output from pipes
	var stdoutBuf, stderrBuf bytes.Buffer
	var pipesClosed bool    // guards against double pipe close
	var pipesMu sync.Mutex  // protects pipesClosed
	var wg sync.WaitGroup
	wg.Add(2)

	closePipes := func() {
		pipesMu.Lock()
		defer pipesMu.Unlock()
		if pipesClosed {
			return
		}
		pipesClosed = true
		stdoutPipe.Close()
		stderrPipe.Close()
	}

	capture := func(dst *bytes.Buffer, r io.Reader) {
		defer wg.Done()
		_, _ = io.Copy(dst, r) // capture goroutines own their respective buffers exclusively
	}
	go capture(&stdoutBuf, stdoutPipe)
	go capture(&stderrBuf, stderrPipe)

	// Wait for the command to finish or timeout/cancel.
	// NOTE: Use cmd.Process.Wait() instead of cmd.Wait() because cmd.Wait()
	// blocks until all IO copying completes. If child processes (e.g. from
	// login shell profile sourcing) inherit pipe FDs, io.Copy never gets EOF
	// and cmd.Wait() hangs forever. cmd.Process.Wait() only waits for the
	// direct child process to exit, then we explicitly close pipes to
	// unblock the IO goroutines.
	waitCh := make(chan int, 1)
	go func() {
		state, err := cmd.Process.Wait()
		code := -1
		if err == nil && state != nil {
			code = extractExitCodeFromState(state)
		}
		// Close pipes to unblock IO goroutines (even if grandchildren hold FDs)
		closePipes()
		wg.Wait()
		waitCh <- code
	}()

	// Build cancellation channel from context
	cancelCh := ctx.Done()

	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()

		select {
		case exitCode := <-waitCh:
			// Command finished before timeout
			result := &ExecResult{
				Stdout:   stdoutBuf.String(),
				Stderr:   stderrBuf.String(),
				ExitCode: exitCode,
			}
			return result, nil

		case <-timer.C:
			// Timeout — do NOT kill the process. Return it to the caller.
			// The background goroutine is still running (cmd.Process.Wait()).
			// Capture goroutines continue writing to stdoutBuf/stderrBuf.
			// OngoingOutput lets the caller (Adopt) read the final full output
			// once the process exits and all capture goroutines complete.
			exitCodeCh := make(chan int, 1)
			go func() {
				exitCodeCh <- <-waitCh
			}()
			ongoingOutput := func() string {
				wg.Wait() // ensure capture goroutines have finished writing
				var sb strings.Builder
				if stdoutBuf.Len() > 0 {
					sb.Write(stdoutBuf.Bytes())
				}
				if stderrBuf.Len() > 0 {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.Write(stderrBuf.Bytes())
				}
				return sb.String()
			}
			result := &ExecResult{
				Stdout:        stdoutBuf.String(),
				Stderr:        stderrBuf.String(),
				ExitCode:      -1,
				TimedOut:      true,
				Process:       cmd.Process,
				ExitCodeCh:    exitCodeCh,
				OngoingOutput: ongoingOutput,
			}
			return result, nil

		case <-cancelCh:
			// Context canceled (e.g. Ctrl+C) — kill entire process group immediately
			killProcessGroup(cmd.Process)
			closePipes()
			exitCode := <-waitCh
			result := &ExecResult{
				Stdout:   stdoutBuf.String(),
				Stderr:   stderrBuf.String(),
				ExitCode: exitCode,
			}
			return result, nil
		}
		}

		// No timeout — wait for completion or context cancel
		select {
		case exitCode := <-waitCh:
		result := &ExecResult{
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			ExitCode: exitCode,
		}
		return result, nil

		case <-cancelCh:
		// Context canceled (e.g. Ctrl+C) — kill entire process group immediately
		killProcessGroup(cmd.Process)
		closePipes()
		exitCode := <-waitCh
		result := &ExecResult{
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			ExitCode: exitCode,
		}
		return result, nil
		}
}

// killProcessGroup sends SIGKILL to the entire process group.
func killProcessGroup(proc *os.Process) {
	if proc == nil || proc.Pid == 0 {
		return
	}
	// Try process group first (-pid), fall back to single process
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
		proc.Kill()
	}
}

func (s *NoneSandbox) ReadFile(ctx context.Context, path string, userID string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxSandboxFileSize {
		return nil, fmt.Errorf("file exceeds maximum size of %d bytes (actual: %d)", MaxSandboxFileSize, info.Size())
	}
	return os.ReadFile(path)
}

func (s *NoneSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	if int64(len(data)) > MaxSandboxFileSize {
		return fmt.Errorf("data exceeds maximum size of %d bytes", MaxSandboxFileSize)
	}
	return os.WriteFile(path, data, perm)
}

func (s *NoneSandbox) Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &SandboxFileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (s *NoneSandbox) ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]DirEntry, len(entries))
	for i, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		result[i] = DirEntry{
			Name:  e.Name(),
			IsDir: info.IsDir(),
			Size:  info.Size(),
		}
	}
	return result, nil
}

func (s *NoneSandbox) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	return os.MkdirAll(path, perm)
}

func (s *NoneSandbox) Remove(ctx context.Context, path string, userID string) error {
	return os.Remove(path)
}

func (s *NoneSandbox) RemoveAll(ctx context.Context, path string, userID string) error {
	return os.RemoveAll(path)
}

func (s *NoneSandbox) DownloadFile(ctx context.Context, url, outputPath, userID string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	resp, err := noneDownloadHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, io.LimitReader(resp.Body, maxNoneDownloadSize))
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if written >= maxNoneDownloadSize {
		return fmt.Errorf("downloaded file exceeds maximum size (%d bytes)", maxNoneDownloadSize)
	}

	log.WithFields(log.Fields{"url": url, "output_path": outputPath, "size": written}).Info("File downloaded (none sandbox)")
	return nil
}

// noneSandboxExecAsync runs a command asynchronously with streaming output.
// Uses Setpgid to ensure all child processes are killed on context cancel.
func noneSandboxExecAsync(ctx context.Context, spec ExecSpec, outputBuf func(string)) (int, error) {
	cmd, err := buildCmdFromSpec(ctx, spec, true)
	if err != nil {
		return -1, err
	}
	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	} else {
		cmd.Stdin = bytes.NewReader(nil)
	}

	// Setpgid: create new process group so kill kills all children
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, stderrPipe, err := setupPipes(cmd)
	if err != nil {
		return -1, err
	}

	// Stream stdout and stderr concurrently
	var wg sync.WaitGroup
	wg.Add(2)

	stream := func(r io.Reader) {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := r.Read(buf)
			if n > 0 && outputBuf != nil {
				outputBuf(string(buf[:n]))
			}
			if readErr != nil {
				return
			}
		}
	}

	go stream(stdoutPipe)
	go stream(stderrPipe)

	// Wait for completion
	exitCode := extractExitCode(cmd.Wait())
	wg.Wait()

	if ctx.Err() != nil {
		return exitCode, ctx.Err()
	}
	return exitCode, nil
}

// --- Shared helpers for command execution ---

// buildCmdFromSpec creates an *exec.Cmd from an ExecSpec.
// If managedCtx is true, uses exec.CommandContext (context cancel kills the process).
// If false, uses exec.Command (caller manages process lifecycle manually, e.g. KeepAlive).
func buildCmdFromSpec(ctx context.Context, spec ExecSpec, managedCtx bool) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	if spec.Shell {
		if managedCtx {
			cmd = exec.CommandContext(ctx, "/bin/sh", "-c", spec.Command)
		} else {
			cmd = exec.Command("/bin/sh", "-c", spec.Command)
		}
	} else {
		if len(spec.Args) == 0 {
			return nil, fmt.Errorf("non-shell exec requires Args to be set")
		}
		if managedCtx {
			cmd = exec.CommandContext(ctx, spec.Args[0], spec.Args[1:]...)
		} else {
			cmd = exec.Command(spec.Args[0], spec.Args[1:]...)
		}
	}
	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	return cmd, nil
}

// setupPipes creates stdout and stderr pipes for a command, then starts it.
func setupPipes(cmd *exec.Cmd) (stdoutPipe, stderrPipe io.ReadCloser, err error) {
	stdoutPipe, err = cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err = cmd.StderrPipe()
	if err != nil {
		stdoutPipe.Close()
		return nil, nil, fmt.Errorf("stderr pipe: %w", err)
	}
	return stdoutPipe, stderrPipe, cmd.Start()
}

// extractExitCode returns the exit code from a cmd.Wait() error.
// Returns 0 if waitErr is nil, the real exit code for ExitError, or -1 otherwise.
func extractExitCode(waitErr error) int {
	if waitErr == nil {
		return 0
	}
	if exitErr, ok := waitErr.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return -1
}

// extractExitCodeFromState returns the exit code from an os.ProcessState.
func extractExitCodeFromState(state *os.ProcessState) int {
	if state == nil {
		return -1
	}
	if state.Success() {
		return 0
	}
	return state.ExitCode()
}
