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
	"sync"
	"syscall"

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
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	var cmd *exec.Cmd
	if spec.Shell {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", spec.Command)
	} else {
		if len(spec.Args) == 0 {
			return nil, fmt.Errorf("non-shell exec requires Args to be set")
		}
		cmd = exec.CommandContext(ctx, spec.Args[0], spec.Args[1:]...)
	}

	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	// When spec.Env is set, append to os.Environ() to inherit host environment variables,
	// consistent with the runner's behavior (append(os.Environ(), req.Env...)).
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	} else {
		// Ensure stdin is never nil — prevents commands (e.g. sudo) from
		// opening /dev/tty and blocking the terminal in none-sandbox mode.
		// In docker/remote sandboxes the process is isolated so this isn't needed.
		cmd.Stdin = bytes.NewReader(nil)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

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
			return nil, err
		}
	}

	return result, nil
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
	var cmd *exec.Cmd
	if spec.Shell {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", spec.Command)
	} else {
		if len(spec.Args) == 0 {
			return -1, fmt.Errorf("non-shell exec requires Args to be set")
		}
		cmd = exec.CommandContext(ctx, spec.Args[0], spec.Args[1:]...)
	}

	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	} else {
		cmd.Stdin = bytes.NewReader(nil)
	}

	// Setpgid: create new process group so kill kills all children
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Create pipes for streaming output
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return -1, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return -1, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return -1, fmt.Errorf("start command: %w", err)
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
	waitErr := cmd.Wait()
	wg.Wait()

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		if ctx.Err() != nil {
			return -1, ctx.Err()
		}
		return -1, waitErr
	}
	return 0, nil
}
