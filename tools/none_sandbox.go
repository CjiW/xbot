package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// NoneSandbox implements Sandbox with direct os.* calls (no containerization).
type NoneSandbox struct{}

func (s *NoneSandbox) Name() string { return "none" }

func (s *NoneSandbox) Close() error                        { return nil }
func (s *NoneSandbox) CloseForUser(userID string) error    { return nil }
func (s *NoneSandbox) IsExporting(userID string) bool      { return false }
func (s *NoneSandbox) ExportAndImport(userID string) error { return nil }

func (s *NoneSandbox) GetShell(userID string, workspace string) (string, error) {
	return "/bin/bash", nil
}

func (s *NoneSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	var cmd *exec.Cmd
	if spec.Shell {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", spec.Command)
	} else {
		cmd = exec.CommandContext(ctx, spec.Command, spec.Args...)
	}

	if spec.Dir != "" {
		cmd.Dir = spec.Dir
	}
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env
	}
	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
		cmd = exec.CommandContext(ctx, cmd.Path, cmd.Args...)
		if spec.Dir != "" {
			cmd.Dir = spec.Dir
		}
		if len(spec.Env) > 0 {
			cmd.Env = spec.Env
		}
		if spec.Stdin != "" {
			cmd.Stdin = bytes.NewBufferString(spec.Stdin)
		}
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
	}

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
	return os.ReadFile(path)
}

func (s *NoneSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
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

func (s *NoneSandbox) Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		return "", nil, fmt.Errorf("command execution is disabled on Windows")
	}
	return command, args, nil
}
