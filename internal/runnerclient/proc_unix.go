//go:build !windows

package runnerclient

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessAttrs sets Unix process group attributes for the command.
func setProcessAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree kills the entire process group (Unix).
func killProcessTree(pid int) {
	syscall.Kill(-pid, syscall.SIGKILL)
}

// signalProcess sends a signal to a process (Unix).
func signalProcess(pid int, sig syscall.Signal) {
	proc, _ := os.FindProcess(pid)
	if proc != nil {
		proc.Signal(sig) //nolint:errcheck
	}
}
