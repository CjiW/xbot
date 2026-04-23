//go:build !windows

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// defaultShell returns the user's login shell from /etc/passwd.
// Falls back to /bin/sh if lookup fails.
func defaultShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	// Fallback
	return "/bin/sh"
}

// loginShellArgs returns the command-line arguments for executing a command
// in a shell that loads the user's environment (PATH, aliases, etc.).
//
// Shell source order differs:
//
//	bash -l -c   → .bash_profile → .bashrc (login sources rc via profile chain)
//	zsh  -l -c   → .zshenv + .zprofile, but NOT .zshrc (rc is interactive-only)
//	zsh  -c      → .zshenv only
//
// User PATH config (go, cuda, nvm, etc.) typically lives in .zshrc / .bashrc.
// For zsh we explicitly source .zshrc so the user's environment is available
// in non-interactive mode without the overhead of -i (prompts, completion, etc.).
func loginShellArgs(shell, command string) []string {
	name := filepath.Base(shell)
	switch name {
	case "zsh":
		return []string{shell, "-c", "source ~/.zshrc 2>/dev/null; " + command}
	default:
		// bash, sh, dash, etc.: -l login shell sources profile → rc chain.
		return []string{shell, "-l", "-c", command}
	}
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
