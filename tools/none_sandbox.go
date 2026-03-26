package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// NoneSandbox implements Sandbox with direct os.* calls (no containerization).
type NoneSandbox struct{}

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
