//go:build !windows

package tools

import (
	"os"
	"os/exec"
	"syscall"
)

// defaultShell returns the default shell for the current platform.
func defaultShell() string { return "/bin/bash" }

// loginShellArgs returns the command-line arguments for executing a command in a login shell.
// Bash: ["bash", "-l", "-c", command] (-l = login shell, loads profile)
func loginShellArgs(shell, command string) []string {
	return []string{shell, "-l", "-c", command}
}

// setProcessAttrs 设置 Unix 平台的进程属性
// 使用进程组，超时时可以杀掉整棵进程树
func setProcessAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcess 杀掉进程组
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		killProcessTree(cmd.Process)
	}
}

// killProcessTree kills a process and its entire process group on Unix.
// Equivalent to kill(-pgid, SIGKILL).
func killProcessTree(proc *os.Process) {
	if proc == nil || proc.Pid == 0 {
		return
	}
	// Try process group first (-pid), fall back to single process
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil {
		proc.Kill()
	}
}

// isProcessAlive checks whether a process with the given PID is still running.
// Uses Signal(0) on Unix (doesn't actually send a signal, just checks existence).
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// SetProcessAttrs 是 setProcessAttrs 的导出版本，供其他包使用
func SetProcessAttrs(cmd *exec.Cmd) { setProcessAttrs(cmd) }

// KillProcess 是 killProcess 的导出版本，供其他包使用
func KillProcess(cmd *exec.Cmd) { killProcess(cmd) }
