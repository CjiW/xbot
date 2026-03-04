package tools

import (
	"os"
	"os/exec"
	"path/filepath"
)

// BwrapConfig bwrap 沙箱配置
type BwrapConfig struct {
	WorkDir    string   // 租户工作目录（宿主机路径）
	SharedDirs []string // 共享只读目录（如 .xbot/skills）
	NetworkOff bool     // 是否禁用网络
}

// DefaultBwrapConfig 返回默认 bwrap 配置
func DefaultBwrapConfig(workDir string) *BwrapConfig {
	return &BwrapConfig{
		WorkDir: workDir,
	}
}

// BuildBwrapCmd 构建 bwrap 命令
// 沙箱内工作目录固定为 /workspace
func BuildBwrapCmd(cfg *BwrapConfig, command string) *exec.Cmd {
	args := []string{
		// 系统目录只读挂载
		"--ro-bind", "/usr", "/usr",
		"--ro-bind", "/bin", "/bin",
		"--ro-bind", "/sbin", "/sbin",
		"--ro-bind", "/lib", "/lib",
		"--ro-bind", "/lib64", "/lib64",
		"--ro-bind", "/etc", "/etc",
	}

	// /usr/local 可能不存在，检查后挂载
	if _, err := os.Stat("/usr/local"); err == nil {
		args = append(args, "--ro-bind", "/usr/local", "/usr/local")
	}

	// 租户工作目录可读写
	args = append(args,
		"--bind", cfg.WorkDir, "/workspace",
	)

	// 共享只读目录
	for _, dir := range cfg.SharedDirs {
		base := filepath.Base(dir)
		args = append(args, "--ro-bind", dir, filepath.Join("/workspace", base))
	}

	// 基础文件系统
	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--tmpfs", "/tmp",
	)

	// 工作目录
	args = append(args, "--chdir", "/workspace")

	// PID 隔离
	args = append(args, "--unshare-pid")

	// 网络隔离（可选）
	if cfg.NetworkOff {
		args = append(args, "--unshare-net")
	}

	// 父进程退出时自动清理
	args = append(args, "--die-with-parent")

	// 执行命令
	args = append(args, "bash", "-c", command)

	return exec.Command("bwrap", args...)
}
