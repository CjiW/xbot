//go:build windows

package runnerclient

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

// setProcessAttrs sets Windows process group attributes for the command.
func setProcessAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
}

// killProcessTree kills a process and all its children (Windows).
func killProcessTree(pid int) {
	exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}

// signalProcess sends a signal to a process (Windows).
// Windows doesn't have SIGTERM; maps to process kill.
func signalProcess(pid int, sig syscall.Signal) {
	proc, _ := os.FindProcess(pid)
	if proc != nil {
		proc.Kill() //nolint:errcheck
	}
}
