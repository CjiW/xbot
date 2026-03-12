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
	// env 参数指定要传递到沙箱的环境变量（格式：KEY=VALUE）
	Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error)
	// Name 返回沙箱名称
	Name() string
	// Close 关闭并清理沙箱资源
	Close() error
}

// NoneSandbox 无沙箱模式，直接执行
type NoneSandbox struct{}

func (s *NoneSandbox) Name() string { return "none" }

func (s *NoneSandbox) Close() error { return nil }

func (s *NoneSandbox) Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		return "", nil, fmt.Errorf("command execution is disabled on Windows")
	}
	return command, args, nil
}

// BwrapSandbox bwrap 沙箱实现
type BwrapSandbox struct{}

func (s *BwrapSandbox) Name() string { return "bwrap" }

func (s *BwrapSandbox) Close() error { return nil }

func (s *BwrapSandbox) Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error) {
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
	}

	// 添加自定义环境变量（必须在 -- 之前）
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			bwrapArgs = append(bwrapArgs, "--setenv", parts[0], parts[1])
		}
	}

	bwrapArgs = append(bwrapArgs, "--", command)
	bwrapArgs = append(bwrapArgs, args...)
	return "bwrap", bwrapArgs, nil
}

// NsjailSandbox nsjail 沙箱实现
type NsjailSandbox struct{}

func (s *NsjailSandbox) Name() string { return "nsjail" }

func (s *NsjailSandbox) Close() error { return nil }

func (s *NsjailSandbox) Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error) {
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
	// nsjail 通过 --env 可以传递环境变量
	for _, e := range env {
		nsArgs = append(nsArgs, "--env", e)
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

// Close 关闭并清理所有 Docker 容器（保留 volume 以便重启后恢复）
func (s *dockerSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for userID, c := range s.containers {
		if c.started {
			// 停止容器
			stopCmd := exec.Command("docker", "stop", "-t", "10", c.name)
			if err := stopCmd.Run(); err != nil {
				log.WithError(err).Warnf("Failed to stop container %s", c.name)
			} else {
				log.Infof("Stopped Docker container %s", c.name)
			}
			// 删除容器（保留 volume 以便重启后恢复）
			rmCmd := exec.Command("docker", "rm", c.name)
			if err := rmCmd.Run(); err != nil {
				log.WithError(err).Warnf("Failed to remove container %s", c.name)
			} else {
				log.Infof("Removed Docker container %s", c.name)
			}
		}
		_ = userID // silence unused variable warning
	}
	s.containers = make(map[string]*dockerContainer)
	return nil
}

func (s *dockerSandbox) Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error) {
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

	// 传递所有环境变量到容器
	dockerArgs := []string{
		"exec",
		"-i",
		"-w", "/workspace",
	}

	// 传递调用者传入的环境变量（多会话场景下每个用户独立）
	for _, e := range env {
		dockerArgs = append(dockerArgs, "-e", e)
	}

	// 添加自定义 PATH 和 LD_LIBRARY_PATH（覆盖宿主机的）
	dockerArgs = append(dockerArgs,
		"-e", "PATH=/root/.local/usr_local/bin:/root/.local/opt/bin:/root/.local/usr_bin:/root/.local/bin:/root/.local/usr_sbin:/root/.local/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"-e", "LD_LIBRARY_PATH=/root/.local/usr_lib:/root/.local/lib:/usr/lib:/lib",
	)

	dockerArgs = append(dockerArgs, containerName, command)
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
	// 重要：必须先复制内容到 volume 目录，再创建符号链接，否则容器会失去基本命令
	if volumeName != "" {
		// 第一步：将宿主机的 bin/lib 复制到 volume（此时容器内 /root 已经是 volume 挂载点）
		hostBinLibCmds := []string{
			// 创建 volume 中的目录
			"mkdir -p /root/.local/usr_local /root/.local/opt /root/.local/usr_bin /root/.local/bin /root/.local/usr_lib /root/.local/lib /root/.local/usr_sbin /root/.local/sbin",

			// 从宿主机复制内容到 volume（宿主机目录作为源）
			"cp -a /usr/local/. /root/.local/usr_local/ 2>/dev/null || true",
			"cp -a /opt/. /root/.local/opt/ 2>/dev/null || true",
			"cp -a /usr/bin/. /root/.local/usr_bin/ 2>/dev/null || true",
			"cp -a /bin/. /root/.local/bin/ 2>/dev/null || true",
			"cp -a /usr/lib/. /root/.local/usr_lib/ 2>/dev/null || true",
			"cp -a /lib/. /root/.local/lib/ 2>/dev/null || true",
			"cp -a /usr/sbin/. /root/.local/usr_sbin/ 2>/dev/null || true",
			"cp -a /sbin/. /root/.local/sbin/ 2>/dev/null || true",
		}
		for _, cmd := range hostBinLibCmds {
			setupCmd := exec.Command("docker", "exec", containerName, "sh", "-c", cmd)
			if out, err := setupCmd.CombinedOutput(); err != nil {
				log.WithError(err).Warnf("Failed to copy bin/lib to volume: %s, output: %s", cmd, string(out))
			}
		}

		// 第二步：先移动系统目录（此时原位置还有命令可用）
		tmpPath := "/root/.local/usr_local/bin:/root/.local/opt/bin:/root/.local/usr_bin:/root/.local/bin:/root/.local/usr_sbin:/root/.local/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"

		mvCmds := []string{
			"[ -d /usr/local ] && [ ! -L /usr/local ] && rm -rf /usr/local.bak 2>/dev/null; mv /usr/local /usr/local.bak 2>/dev/null || true",
			"[ -d /opt ] && [ ! -L /opt ] && rm -rf /opt.bak 2>/dev/null; mv /opt /opt.bak 2>/dev/null || true",
			"[ -d /usr/bin ] && [ ! -L /usr/bin ] && rm -rf /usr/bin.bak 2>/dev/null; mv /usr/bin /usr/bin.bak 2>/dev/null || true",
			"[ -d /bin ] && [ ! -L /bin ] && rm -rf /bin.bak 2>/dev/null; mv /bin /bin.bak 2>/dev/null || true",
			"[ -d /usr/lib ] && [ ! -L /usr/lib ] && rm -rf /usr/lib.bak 2>/dev/null; mv /usr/lib /usr_lib.bak 2>/dev/null || true",
			"[ -d /lib ] && [ ! -L /lib ] && rm -rf /lib.bak 2>/dev/null; mv /lib /lib.bak 2>/dev/null || true",
			"[ -d /usr/sbin ] && [ ! -L /usr/sbin ] && rm -rf /usr/sbin.bak 2>/dev/null; mv /usr/sbin /usr_sbin.bak 2>/dev/null || true",
			"[ -d /sbin ] && [ ! -L /sbin ] && rm -rf /sbin.bak 2>/dev/null; mv /sbin /sbin.bak 2>/dev/null || true",
		}
		for _, cmd := range mvCmds {
			setupCmd := exec.Command("docker", "exec", containerName, "sh", "-c", cmd)
			if out, err := setupCmd.CombinedOutput(); err != nil {
				log.WithError(err).Warnf("Failed to move system directory: %s, output: %s", cmd, string(out))
			}
		}

		// 然后创建符号链接（使用 -e PATH 让 sh 能找到 ln）
		lnCmds := []string{
			"ln -sf /root/.local/usr_local /usr/local",
			"ln -sf /root/.local/opt /opt",
			"ln -sf /root/.local/usr_bin /usr/bin",
			"ln -sf /root/.local/bin /bin",
			"ln -sf /root/.local/usr_lib /usr_lib",
			"ln -sf /root/.local/lib /lib",
			"ln -sf /root/.local/usr_sbin /usr_sbin",
			"ln -sf /root/.local/sbin /sbin",
		}
		for _, cmd := range lnCmds {
			setupCmd := exec.Command("docker", "exec", "-e", "PATH="+tmpPath, containerName, "sh", "-c", cmd)
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
	// 设置临时 PATH 指向 volume 中的 bin/lib
	tmpPath := "/root/.local/usr_local/bin:/root/.local/opt/bin:/root/.local/usr_bin:/root/.local/bin:/root/.local/usr_sbin:/root/.local/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
	mvCmds := []string{
		"[ -d /usr/local ] && [ ! -L /usr/local ] && rm -rf /usr/local.bak 2>/dev/null; mv /usr/local /usr/local.bak 2>/dev/null || true",
		"[ -d /opt ] && [ ! -L /opt ] && rm -rf /opt.bak 2>/dev/null; mv /opt /opt.bak 2>/dev/null || true",
		"[ -d /usr/bin ] && [ ! -L /usr/bin ] && rm -rf /usr/bin.bak 2>/dev/null; mv /usr/bin /usr/bin.bak 2>/dev/null || true",
		"[ -d /bin ] && [ ! -L /bin ] && rm -rf /bin.bak 2>/dev/null; mv /bin /bin.bak 2>/dev/null || true",
		"[ -d /usr/lib ] && [ ! -L /usr/lib ] && rm -rf /usr/lib.bak 2>/dev/null; mv /usr/lib /usr_lib.bak 2>/dev/null || true",
		"[ -d /lib ] && [ ! -L /lib ] && rm -rf /lib.bak 2>/dev/null; mv /lib /lib.bak 2>/dev/null || true",
		"[ -d /usr/sbin ] && [ ! -L /usr/sbin ] && rm -rf /usr/sbin.bak 2>/dev/null; mv /usr/sbin /usr_sbin.bak 2>/dev/null || true",
		"[ -d /sbin ] && [ ! -L /sbin ] && rm -rf /sbin.bak 2>/dev/null; mv /sbin /sbin.bak 2>/dev/null || true",
	}
	for _, cmd := range mvCmds {
		setupCmd := exec.Command("docker", "exec", containerName, "sh", "-c", cmd)
		if out, err := setupCmd.CombinedOutput(); err != nil {
			log.WithError(err).Warnf("Failed to move system directory: %s, output: %s", cmd, string(out))
		}
	}

	// 然后创建符号链接（使用 -e PATH 让 sh 能找到 ln）
	lnCmds := []string{
		"ln -sf /root/.local/usr_local /usr/local",
		"ln -sf /root/.local/opt /opt",
		"ln -sf /root/.local/usr_bin /usr/bin",
		"ln -sf /root/.local/bin /bin",
		"ln -sf /root/.local/usr_lib /usr_lib",
		"ln -sf /root/.local/lib /lib",
		"ln -sf /root/.local/usr_sbin /usr_sbin",
		"ln -sf /root/.local/sbin /sbin",
	}
	for _, cmd := range lnCmds {
		setupCmd := exec.Command("docker", "exec", "-e", "PATH="+tmpPath, containerName, "sh", "-c", cmd)
		if out, err := setupCmd.CombinedOutput(); err != nil {
			log.WithError(err).Warnf("Failed to setup symlinks: %s, output: %s", cmd, string(out))
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
	return WrapCommandForSandboxWithEnv(command, args, nil, workspaceRoot)
}

// WrapCommandForSandboxWithEnv 将命令包装到沙箱执行，带环境变量
func WrapCommandForSandboxWithEnv(command string, args []string, env []string, workspaceRoot string) (string, []string, error) {
	// 尝试查找可用的沙箱
	if _, err := exec.LookPath("bwrap"); err == nil {
		s := &BwrapSandbox{}
		return s.Wrap(command, args, env, workspaceRoot, "")
	}

	if _, err := exec.LookPath("nsjail"); err == nil {
		s := &NsjailSandbox{}
		return s.Wrap(command, args, env, workspaceRoot, "")
	}

	// 如果没有任何沙箱可用，返回错误
	return "", nil, fmt.Errorf("no sandbox runner found, install bwrap or nsjail")
}
