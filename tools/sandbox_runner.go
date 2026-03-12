package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"xbot/config"
	log "xbot/logger"
)

// 全局沙箱实例
var globalSandbox Sandbox
var sandboxInitOnce sync.Once

// GetSandbox 获取全局沙箱实例
func GetSandbox() Sandbox {
	sandboxInitOnce.Do(func() {
		cfg := config.Load()
		globalSandbox = NewSandbox(cfg.Sandbox.Mode, cfg.Sandbox.DockerImage, cfg.Sandbox.DockerVolumeDir)
		log.Infof("Sandbox initialized: %s", globalSandbox.Name())
	})
	return globalSandbox
}

// SetSandbox 设置全局沙箱实例（用于测试）
func SetSandbox(s Sandbox) {
	globalSandbox = s
}

// Sandbox 沙箱接口
type Sandbox interface {
	// Wrap 将命令包装到沙箱执行，返回可直接用于 exec.Command 的 command 与 args
	Wrap(command string, args []string, workspace string, userID string) (string, []string, error)
	// Name 返回沙箱名称
	Name() string
}

// NoneSandbox 无沙箱模式，直接执行
type NoneSandbox struct{}

func (s *NoneSandbox) Name() string { return "none" }

func (s *NoneSandbox) Wrap(command string, args []string, workspace string, userID string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		return "", nil, fmt.Errorf("command execution is disabled on Windows")
	}
	return command, args, nil
}

// BwrapSandbox bwrap 沙箱实现
type BwrapSandbox struct{}

func (s *BwrapSandbox) Name() string { return "bwrap" }

func (s *BwrapSandbox) Wrap(command string, args []string, workspace string, userID string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		return "", nil, fmt.Errorf("command execution is disabled on Windows")
	}

	ws := workspace
	if ws == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		ws = cwd
	}
	ws, err := filepath.Abs(ws)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", nil, err
	}
	_ = os.MkdirAll(filepath.Join(ws, ".tmp"), 0o755)

	bwrapArgs := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-pid",
		"--ro-bind", "/", "/",
		"--proc", "/proc",
		"--dev", "/dev",
		"--bind", ws, ws,
		"--chdir", ws,
		"--setenv", "HOME", ws,
		"--setenv", "TMPDIR", filepath.Join(ws, ".tmp"),
		"--",
		command,
	}
	bwrapArgs = append(bwrapArgs, args...)
	return "bwrap", bwrapArgs, nil
}

// NsjailSandbox nsjail 沙箱实现
type NsjailSandbox struct{}

func (s *NsjailSandbox) Name() string { return "nsjail" }

func (s *NsjailSandbox) Wrap(command string, args []string, workspace string, userID string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		return "", nil, fmt.Errorf("command execution is disabled on Windows")
	}

	ws := workspace
	if ws == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		ws = cwd
	}
	ws, err := filepath.Abs(ws)
	if err != nil {
		return "", nil, err
	}

	nsArgs := []string{
		"-Mo",
		"--cwd", ws,
		"--",
		command,
	}
	nsArgs = append(nsArgs, args...)
	return "nsjail", nsArgs, nil
}

// dockerSandbox Docker 沙箱实现
type dockerSandbox struct {
	image      string
	volumeDir  string
	mu         sync.Mutex
	containers map[string]*dockerContainer // userID -> container
}

type dockerContainer struct {
	name    string
	volume  string
	started bool
}

func (s *dockerSandbox) Name() string { return "docker" }

func (s *dockerSandbox) Wrap(command string, args []string, workspace string, userID string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		return "", nil, fmt.Errorf("command execution is disabled on Windows")
	}

	ws := workspace
	if ws == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", nil, err
		}
		ws = cwd
	}
	ws, err := filepath.Abs(ws)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", nil, err
	}
	_ = os.MkdirAll(filepath.Join(ws, ".tmp"), 0o755)

	// 获取或创建用户容器
	containerName, _, err := s.getOrCreateContainer(userID, ws)
	if err != nil {
		return "", nil, err
	}

	// 使用 docker exec 执行命令
	// 注意：-w 参数需要使用容器内的路径 /workspace，而不是宿主机的路径
	dockerArgs := []string{
		"exec",
		"-i",
		"-e", "PATH=/root/.local/usr_local/bin:/root/.local/opt/bin:/root/.local/usr_bin:/root/.local/bin:/root/.local/usr_sbin:/root/.local/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"-e", "LD_LIBRARY_PATH=/root/.local/usr_lib:/root/.local/lib:/usr/lib:/lib",
		"-w", "/workspace",
		containerName,
		command,
	}
	dockerArgs = append(dockerArgs, args...)

	return "docker", dockerArgs, nil
}

// getOrCreateContainer 获取或创建用户的 Docker 容器
func (s *dockerSandbox) getOrCreateContainer(userID, workspace string) (containerName, volumeName string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 初始化容器映射
	if s.containers == nil {
		s.containers = make(map[string]*dockerContainer)
	}

	// 如果容器已存在且在运行，直接返回
	if c, ok := s.containers[userID]; ok && c.started {
		return c.name, c.volume, nil
	}

	// 创建新的容器
	containerName = fmt.Sprintf("xbot-%s", userID)
	volumeName = fmt.Sprintf("xbot-%s", userID)

	// 检查容器是否已存在（可能是之前创建的但未在运行）
	checkCmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName)
	checkOutput, checkErr := checkCmd.Output()
	if checkErr == nil && strings.Contains(string(checkOutput), "true") {
		// 容器已在运行
		s.containers[userID] = &dockerContainer{
			name:    containerName,
			volume:  volumeName,
			started: true,
		}
		return containerName, volumeName, nil
	}

	// 容器已存在但未运行，尝试启动
	startCmd := exec.Command("docker", "start", containerName)
	_, startErr := startCmd.CombinedOutput()
	if startErr == nil {
		log.Infof("Started existing Docker container %s", containerName)
		// 确保符号链接存在
		if volumeName != "" {
			s.ensureSymlinks(containerName)
		}
		s.containers[userID] = &dockerContainer{
			name:    containerName,
			volume:  volumeName,
			started: true,
		}
		return containerName, volumeName, nil
	}

	// 容器不存在，需要创建
	// 创建 volume
	createVolumeCmd := exec.Command("docker", "volume", "create", volumeName)
	if err := createVolumeCmd.Run(); err != nil {
		log.WithError(err).Warnf("Failed to create volume %s, continuing without persistent volume", volumeName)
		volumeName = "" // 不使用持久化 volume
	}

	// 构建 docker run 命令启动容器
	runArgs := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", fmt.Sprintf("xbot-%s", userID),
	}

	// 挂载 volume（用于持久化用户环境）
	if volumeName != "" {
		runArgs = append(runArgs, "-v", fmt.Sprintf("%s:/root", volumeName))
	}

	// 挂载 workspace
	runArgs = append(runArgs, "-v", fmt.Sprintf("%s:/workspace:rw", workspace))

	// 设置工作目录
	runArgs = append(runArgs, "-w", "/workspace")

	// 添加镜像
	runArgs = append(runArgs, s.image)

	// 添加入口点保持容器运行
	runArgs = append(runArgs, "tail", "-f", "/dev/null")

	log.Infof("Creating Docker container %s with image %s", containerName, s.image)

	runCmd := exec.Command("docker", runArgs...)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w, output: %s", err, string(output))
	}

	// 设置符号链接：将 /usr/local, /opt 和系统 bin/lib 目录指向 volume 中的持久化目录
	// 这样用户安装的系统级工具也能持久化
	if volumeName != "" {
		binLibSetupCmds := []string{
			// 创建 volume 中的目录
			"mkdir -p /root/.local/usr_local /root/.local/opt /root/.local/usr_bin /root/.local/bin /root/.local/usr_lib /root/.local/lib /root/.local/usr_sbin /root/.local/sbin",

			// /usr/local
			"[ -d /usr/local ] && [ ! -L /usr/local ] && rm -rf /usr/local.bak 2>/dev/null; mv /usr/local /usr/local.bak 2>/dev/null || true",
			"ln -sf /root/.local/usr_local /usr/local",

			// /opt
			"[ -d /opt ] && [ ! -L /opt ] && rm -rf /opt.bak 2>/dev/null; mv /opt /opt.bak 2>/dev/null || true",
			"ln -sf /root/.local/opt /opt",

			// /usr/bin
			"[ -d /usr/bin ] && [ ! -L /usr/bin ] && rm -rf /usr/bin.bak 2>/dev/null; mv /usr/bin /usr/bin.bak 2>/dev/null || true",
			"ln -sf /root/.local/usr_bin /usr/bin",

			// /bin
			"[ -d /bin ] && [ ! -L /bin ] && rm -rf /bin.bak 2>/dev/null; mv /bin /bin.bak 2>/dev/null || true",
			"ln -sf /root/.local/bin /bin",

			// /usr/lib
			"[ -d /usr/lib ] && [ ! -L /usr/lib ] && rm -rf /usr/lib.bak 2>/dev/null; mv /usr/lib /usr/lib.bak 2>/dev/null || true",
			"ln -sf /root/.local/usr_lib /usr/lib",

			// /lib
			"[ -d /lib ] && [ ! -L /lib ] && rm -rf /lib.bak 2>/dev/null; mv /lib /lib.bak 2>/dev/null || true",
			"ln -sf /root/.local/lib /lib",

			// /usr/sbin
			"[ -d /usr/sbin ] && [ ! -L /usr/sbin ] && rm -rf /usr/sbin.bak 2>/dev/null; mv /usr/sbin /usr/sbin.bak 2>/dev/null || true",
			"ln -sf /root/.local/usr_sbin /usr/sbin",

			// /sbin
			"[ -d /sbin ] && [ ! -L /sbin ] && rm -rf /sbin.bak 2>/dev/null; mv /sbin /sbin.bak 2>/dev/null || true",
			"ln -sf /root/.local/sbin /sbin",
		}
		for _, cmd := range binLibSetupCmds {
			setupCmd := exec.Command("docker", "exec", containerName, "sh", "-c", cmd)
			if out, err := setupCmd.CombinedOutput(); err != nil {
				log.WithError(err).Warnf("Failed to setup symlinks: %s, output: %s", cmd, string(out))
			}
		}
		log.Infof("Setup symlinks for /usr/local, /opt, /usr/bin, /bin, /usr/lib, /lib, /usr/sbin, /sbin in container %s", containerName)
	}

	// 记录容器
	s.containers[userID] = &dockerContainer{
		name:    containerName,
		volume:  volumeName,
		started: true,
	}

	log.Infof("Docker container %s created successfully", containerName)

	return containerName, volumeName, nil
}

// ensureSymlinks 确保容器内的符号链接存在（容器重启后调用）
func (s *dockerSandbox) ensureSymlinks(containerName string) {
	setupCmds := []string{
		// 创建持久化目录（如果不存在）
		"mkdir -p /root/.local/usr_local /root/.local/opt /root/.local/usr_bin /root/.local/bin /root/.local/usr_lib /root/.local/lib /root/.local/usr_sbin /root/.local/sbin",

		// 如果 /usr/local 是目录且不是符号链接，迁移它
		"[ -d /usr/local ] && [ ! -L /usr/local ] && rm -rf /usr/local.bak 2>/dev/null; mv /usr/local /usr/local.bak 2>/dev/null || true",
		// 创建符号链接（-fn 强制创建，-n 处理目录已存在的情况）
		"ln -sfn /root/.local/usr_local /usr/local",

		// 同样处理 /opt
		"[ -d /opt ] && [ ! -L /opt ] && rm -rf /opt.bak 2>/dev/null; mv /opt /opt.bak 2>/dev/null || true",
		"ln -sfn /root/.local/opt /opt",

		// /usr/bin
		"[ -d /usr/bin ] && [ ! -L /usr/bin ] && rm -rf /usr/bin.bak 2>/dev/null; mv /usr/bin /usr/bin.bak 2>/dev/null || true",
		"ln -sfn /root/.local/usr_bin /usr/bin",

		// /bin
		"[ -d /bin ] && [ ! -L /bin ] && rm -rf /bin.bak 2>/dev/null; mv /bin /bin.bak 2>/dev/null || true",
		"ln -sfn /root/.local/bin /bin",

		// /usr/lib
		"[ -d /usr/lib ] && [ ! -L /usr/lib ] && rm -rf /usr/lib.bak 2>/dev/null; mv /usr/lib /usr/lib.bak 2>/dev/null || true",
		"ln -sfn /root/.local/usr_lib /usr/lib",

		// /lib
		"[ -d /lib ] && [ ! -L /lib ] && rm -rf /lib.bak 2>/dev/null; mv /lib /lib.bak 2>/dev/null || true",
		"ln -sfn /root/.local/lib /lib",

		// /usr/sbin
		"[ -d /usr/sbin ] && [ ! -L /usr/sbin ] && rm -rf /usr/sbin.bak 2>/dev/null; mv /usr/sbin /usr/sbin.bak 2>/dev/null || true",
		"ln -sfn /root/.local/usr_sbin /usr/sbin",

		// /sbin
		"[ -d /sbin ] && [ ! -L /sbin ] && rm -rf /sbin.bak 2>/dev/null; mv /sbin /sbin.bak 2>/dev/null || true",
		"ln -sfn /root/.local/sbin /sbin",
	}
	for _, cmd := range setupCmds {
		setupCmd := exec.Command("docker", "exec", containerName, "sh", "-c", cmd)
		if out, err := setupCmd.CombinedOutput(); err != nil {
			log.WithError(err).Debugf("Symlink setup: %s, output: %s", cmd, string(out))
		}
	}
}

// NewSandbox 创建沙箱实例
func NewSandbox(mode, image, volumeDir string) Sandbox {
	switch mode {
	case "none":
		return &NoneSandbox{}
	case "bwrap":
		return &BwrapSandbox{}
	case "nsjail":
		return &NsjailSandbox{}
	case "docker":
		ds := &dockerSandbox{
			image:     image,
			volumeDir: volumeDir,
		}
		return ds
	default:
		// 默认使用 Docker
		ds := &dockerSandbox{
			image:     image,
			volumeDir: volumeDir,
		}
		return ds
	}
}

// WrapCommandForSandbox 将命令包装到沙箱执行（兼容旧接口）
// Deprecated: 使用 NewSandbox 创建的沙箱实例的 Wrap 方法
func WrapCommandForSandbox(command string, args []string, workspaceRoot string) (string, []string, error) {
	// 尝试查找可用的沙箱
	if _, err := exec.LookPath("bwrap"); err == nil {
		s := &BwrapSandbox{}
		return s.Wrap(command, args, workspaceRoot, "")
	}

	if _, err := exec.LookPath("nsjail"); err == nil {
		s := &NsjailSandbox{}
		return s.Wrap(command, args, workspaceRoot, "")
	}

	// 如果没有任何沙箱可用，返回错误
	return "", nil, fmt.Errorf("no sandbox runner found, install bwrap or nsjail")
}
