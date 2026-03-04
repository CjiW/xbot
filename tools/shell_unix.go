//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setProcessAttrs 设置 Unix 平台的进程属性
// 使用进程组，超时时可以杀掉整棵进程树
func setProcessAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcess 杀掉进程组
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
