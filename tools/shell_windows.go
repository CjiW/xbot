//go:build windows

package tools

import (
	"os/exec"
)

// setProcessAttrs 设置 Windows 平台的进程属性
// Windows 不支持进程组，使用默认行为
func setProcessAttrs(cmd *exec.Cmd) {
	// Windows 上不需要特殊设置
}

// killProcess 杀掉进程
func killProcess(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}

// SetProcessAttrs 是 setProcessAttrs 的导出版本，供其他包使用
func SetProcessAttrs(cmd *exec.Cmd) { setProcessAttrs(cmd) }

// KillProcess 是 killProcess 的导出版本，供其他包使用
func KillProcess(cmd *exec.Cmd) { killProcess(cmd) }
