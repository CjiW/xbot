package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// nativeExecutor 使用 os.* 原生 API 执行操作。
type nativeExecutor struct {
	workspace string
}

func newNativeExecutor(workspace string) *nativeExecutor {
	return &nativeExecutor{workspace: workspace}
}

func (e *nativeExecutor) Close() error { return nil }

func (e *nativeExecutor) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	var cmd *exec.Cmd
	if spec.Shell {
		cmd = exec.CommandContext(ctx, "sh", "-c", spec.Command)
		if verboseLog {
			log.Printf("  exec shell: %s  (dir=%s, timeout=%v)", spec.Command, spec.Dir, spec.Timeout)
		}
	} else {
		args := spec.Args
		if len(args) == 0 {
			return nil, fmt.Errorf("non-shell exec requires Args to be set explicitly")
		}
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
		if verboseLog {
			log.Printf("  exec: %s  (dir=%s, timeout=%v)", joinArgs(args), spec.Dir, spec.Timeout)
		}
	}

	// Create a new process group so we can kill all children on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if spec.Dir != "" {
		cmd.Dir = filepath.Clean(spec.Dir)
	} else {
		cmd.Dir = e.workspace
	}
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	exitCode := 0
	timedOut := false

	if ctx.Err() == context.DeadlineExceeded {
		timedOut = true
		exitCode = -1
		// Kill the entire process group to prevent child process leaks.
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		log.Printf("  exec timed out after %v: %s", elapsed, spec.Command)
	} else if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			}
		} else {
			return nil, err
		}
	}

	log.Printf("  exec done in %v  exit=%d  stdout=%dB  stderr=%dB", elapsed, exitCode, stdout.Len(), stderr.Len())

	return &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		TimedOut: timedOut,
	}, nil
}

func (e *nativeExecutor) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (e *nativeExecutor) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (e *nativeExecutor) Stat(path string) (FileInfo, error) {
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

func (e *nativeExecutor) ReadDir(path string) ([]DirEntry, error) {
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

func (e *nativeExecutor) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (e *nativeExecutor) Remove(path string) error {
	return os.Remove(path)
}

func (e *nativeExecutor) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// joinArgs joins command args for logging.
func joinArgs(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(a)
	}
	return b.String()
}
