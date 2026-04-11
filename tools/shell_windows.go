//go:build windows

package tools

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// defaultWindowsShell is the shell used on Windows (none sandbox).
const defaultWindowsShell = "powershell.exe"

// defaultShell returns the default shell for the current platform.
func defaultShell() string { return defaultWindowsShell }

// loginShellArgs returns the command-line arguments for executing a command in a login shell.
// PowerShell: ["powershell.exe", "-Command", command] (loads profile by default)
func loginShellArgs(shell, command string) []string {
	return []string{shell, "-Command", command}
}

// setProcessAttrs sets Windows-specific process attributes.
// CREATE_NEW_PROCESS_GROUP enables killing the process tree via taskkill.
func setProcessAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// killProcess kills the process tree of the given command.
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		killProcessTree(cmd.Process)
	}
}

// killProcessTree kills a process and all its children on Windows.
// Uses taskkill /T /F which is the Windows equivalent of Unix kill(-pgid, SIGKILL).
func killProcessTree(proc *os.Process) {
	if proc == nil || proc.Pid == 0 {
		return
	}
	// Best-effort: ignore errors (process may have already exited).
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(proc.Pid)).Run()
}

// isProcessAlive checks whether a process with the given PID is still running.
// Uses OpenProcess + GetExitCodeProcess on Windows.
func isProcessAlive(pid int) bool {
	const (
		_PROCESS_QUERY_LIMITED_INFORMATION = 0x1000
		_STILL_ACTIVE                      = 259
	)
	handle, err := syscall.OpenProcess(_PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	err = syscall.GetExitCodeProcess(handle, &exitCode)
	return err == nil && exitCode == _STILL_ACTIVE
}

// SetProcessAttrs is the exported version of setProcessAttrs.
func SetProcessAttrs(cmd *exec.Cmd) { setProcessAttrs(cmd) }

// KillProcess is the exported version of killProcess.
func KillProcess(cmd *exec.Cmd) { killProcess(cmd) }
