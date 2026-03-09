package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// WrapCommandForSandbox 将命令包装到沙箱执行。
// 返回可直接用于 exec.Command/stdio transport 的 command 与 args。
func WrapCommandForSandbox(command string, args []string, workspaceRoot string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		return "", nil, fmt.Errorf("command execution is disabled on Windows")
	}

	workspace := workspaceRoot
	if workspace == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		workspace = cwd
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		return "", nil, err
	}
	_ = os.MkdirAll(filepath.Join(workspace, ".tmp"), 0o755)

	if _, err := exec.LookPath("bwrap"); err == nil {
		bwrapArgs := []string{
			"--die-with-parent",
			"--new-session",
			"--unshare-pid",
			"--ro-bind", "/", "/",
			"--proc", "/proc",
			"--dev", "/dev",
			"--bind", workspace, workspace,
			"--chdir", workspace,
			"--setenv", "HOME", workspace,
			"--setenv", "TMPDIR", filepath.Join(workspace, ".tmp"),
			"--",
			command,
		}
		bwrapArgs = append(bwrapArgs, args...)
		return "bwrap", bwrapArgs, nil
	}

	if _, err := exec.LookPath("nsjail"); err == nil {
		nsArgs := []string{
			"-Mo",
			"--cwd", workspace,
			"--",
			command,
		}
		nsArgs = append(nsArgs, args...)
		return "nsjail", nsArgs, nil
	}

	return "", nil, fmt.Errorf("no sandbox runner found, install bwrap or nsjail")
}

func shellWrapForSandbox(shellCommand string, workspaceRoot string) (string, []string, error) {
	baseCmd, baseArgs, err := WrapCommandForSandbox("sh", []string{"-c", shellCommand}, workspaceRoot)
	if err != nil {
		if strings.Contains(err.Error(), "disabled on Windows") {
			return "", nil, err
		}
		// No sandbox runner found: fall back to direct execution
		return "sh", []string{"-c", shellCommand}, nil
	}
	return baseCmd, baseArgs, nil
}
