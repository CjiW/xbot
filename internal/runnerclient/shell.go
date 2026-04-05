package runnerclient

import (
	"os"
	"os/exec"
	"strings"
)

// DetectShell 检测最佳可用的 shell。
// Docker 模式：查询容器内的 /etc/passwd（与 DockerSandbox.detectShell 相同）。
// Native 模式：检查宿主机文件系统。
func DetectShell(dockerMode bool, executor Executor) string {
	if dockerMode {
		de, ok := executor.(*DockerExecutor)
		if ok {
			out, err := exec.Command("docker", "exec", "-i", de.ContainerName,
				"sh", "-c", "grep '^root:' /etc/passwd | cut -d: -f7").Output()
			if err == nil {
				shell := strings.TrimSpace(string(out))
				if shell != "" {
					return shell
				}
			}
		}
	}
	// 回退：检查宿主机或默认
	for _, candidate := range []string{"/bin/bash", "/usr/bin/bash", "/bin/sh"} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "/bin/sh"
}
