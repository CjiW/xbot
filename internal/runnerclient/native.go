package runnerclient

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
	"time"

	"xbot/internal/cmdbuilder"
)

// maxDownloadSize 是下载操作的最大文件大小（100MB）。
const maxDownloadSize = 100 * 1024 * 1024

// httpClient 是下载操作的专用 HTTP 客户端。
var httpClient = &http.Client{Timeout: 0} // 使用 context timeout

// NativeExecutor 使用 os.* 原生 API 执行操作。
type NativeExecutor struct {
	Workspace string
}

// NewNativeExecutor 创建一个 NativeExecutor。
func NewNativeExecutor(workspace string) *NativeExecutor {
	return &NativeExecutor{Workspace: workspace}
}

func (e *NativeExecutor) Close() error { return nil }

func (e *NativeExecutor) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	cmd, err := cmdbuilder.Build(ctx, spec.Shell, spec.Command, spec.Args,
		"", spec.Env, cmdbuilder.Config{RunAsUser: spec.RunAsUser})
	if err != nil {
		return nil, err
	}

	// 创建新进程组，超时时可以 kill 所有子进程
	setProcessAttrs(cmd)
	if spec.Dir != "" {
		cmd.Dir = filepath.Clean(spec.Dir)
	} else {
		cmd.Dir = e.Workspace
	}
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err = cmd.Run()
	_ = time.Since(start)

	exitCode := 0
	timedOut := false

	if ctx.Err() == context.DeadlineExceeded {
		timedOut = true
		exitCode = -1
		if cmd.Process != nil {
			killProcessTree(cmd.Process.Pid)
		}
	} else if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}

	return &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		TimedOut: timedOut,
	}, nil
}

func (e *NativeExecutor) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (e *NativeExecutor) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (e *NativeExecutor) Stat(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (e *NativeExecutor) ReadDir(path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		var size int64
		if ierr == nil {
			size = info.Size()
		}
		result = append(result, DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	return result, nil
}

func (e *NativeExecutor) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (e *NativeExecutor) Remove(path string) error {
	return os.Remove(path)
}

func (e *NativeExecutor) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

func (e *NativeExecutor) DownloadFile(ctx context.Context, url, outputPath string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create dir: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, io.LimitReader(resp.Body, maxDownloadSize))
	if err != nil {
		return 0, fmt.Errorf("write file: %w", err)
	}
	if written >= maxDownloadSize {
		return 0, fmt.Errorf("file exceeds maximum size (%d bytes)", maxDownloadSize)
	}
	return written, nil
}
