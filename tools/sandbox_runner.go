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
		globalSandbox = NewSandbox(cfg.Sandbox.Mode, cfg.Sandbox.DockerImage)
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
// 使用 docker commit 持久化用户环境：Close 时将容器提交为用户专属镜像，
// 下次创建容器时优先使用该镜像，从而完整保留 apt install 等所有变更。
type dockerSandbox struct {
	image      string // 基础镜像
	mu         sync.Mutex
	containers map[string]*dockerContainer // userID -> container
}

type dockerContainer struct {
	name    string
	started bool
}

func (s *dockerSandbox) Name() string { return "docker" }

// Close 关闭并清理所有 Docker 容器
// 分两阶段执行：先 commit 所有用户容器（保证数据持久化优先），再 stop+rm。
// 这样即使进程在 stop 阶段被外层 Docker SIGKILL，用户数据也已 commit。
func (s *dockerSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Phase 1: commit all user containers (fast, critical for data persistence)
	for userID, c := range s.containers {
		if !c.started {
			continue
		}
		s.commitIfDirty(c.name, userID)
	}

	// Phase 2: stop + rm all containers (may be slow, less critical)
	for _, c := range s.containers {
		if !c.started {
			continue
		}

		stopCmd := exec.Command("docker", "stop", "-t", "10", c.name)
		if err := stopCmd.Run(); err != nil {
			log.WithError(err).Warnf("Failed to stop container %s", c.name)
		} else {
			log.Infof("Stopped Docker container %s", c.name)
		}

		rmCmd := exec.Command("docker", "rm", c.name)
		if err := rmCmd.Run(); err != nil {
			log.WithError(err).Warnf("Failed to remove container %s", c.name)
		} else {
			log.Infof("Removed Docker container %s", c.name)
		}
	}
	s.containers = make(map[string]*dockerContainer)
	return nil
}

// commitIfDirty 仅在容器有文件系统变更时 commit，并清理旧的 dangling 镜像
func (s *dockerSandbox) commitIfDirty(containerName, userID string) {
	if userID == "" || strings.HasPrefix(userID, "__") {
		log.Debugf("Skipping commit for system container %s (userID=%q)", containerName, userID)
		return
	}

	diffCmd := exec.Command("docker", "diff", containerName)
	diffOutput, err := diffCmd.Output()
	if err != nil {
		log.WithError(err).Warnf("Failed to check diff for container %s, skipping commit", containerName)
		return
	}
	if len(strings.TrimSpace(string(diffOutput))) == 0 {
		log.Infof("Container %s has no changes, skipping commit", containerName)
		return
	}

	userImage := userImageName(userID)

	// 记录旧镜像 ID（如果存在），用于 commit 后清理
	var oldImageID string
	idCmd := exec.Command("docker", "image", "inspect", "-f", "{{.Id}}", userImage)
	if out, err := idCmd.Output(); err == nil {
		oldImageID = strings.TrimSpace(string(out))
	}

	commitCmd := exec.Command("docker", "commit", containerName, userImage)
	if err := commitCmd.Run(); err != nil {
		log.WithError(err).Warnf("Failed to commit container %s to image %s", containerName, userImage)
		return
	}
	log.Infof("Committed container %s to image %s", containerName, userImage)

	// 清理旧镜像：commit 后 tag 指向新镜像，旧镜像变为 dangling
	if oldImageID != "" {
		newIDCmd := exec.Command("docker", "image", "inspect", "-f", "{{.Id}}", userImage)
		if newOut, err := newIDCmd.Output(); err == nil {
			newImageID := strings.TrimSpace(string(newOut))
			if newImageID != oldImageID {
				rmCmd := exec.Command("docker", "rmi", oldImageID)
				if err := rmCmd.Run(); err != nil {
					log.WithError(err).Debugf("Failed to remove old image %s (may still be referenced)", oldImageID)
				} else {
					log.Infof("Cleaned up old image %s", oldImageID[:12])
				}
			}
		}
	}
}

// userImageName 返回用户专属镜像名称
func userImageName(userID string) string {
	return fmt.Sprintf("xbot-%s:latest", userID)
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

	containerName, err := s.getOrCreateContainer(userID, ws)
	if err != nil {
		return "", nil, err
	}

	dockerArgs := []string{
		"exec",
		"-i",
		"-w", "/workspace",
	}

	for _, e := range env {
		dockerArgs = append(dockerArgs, "-e", e)
	}

	dockerArgs = append(dockerArgs, containerName, command)
	dockerArgs = append(dockerArgs, args...)

	return "docker", dockerArgs, nil
}

// getOrCreateContainer 获取或创建用户的 Docker 容器
// 优先使用用户专属镜像（由 docker commit 生成），不存在则用基础镜像
func (s *dockerSandbox) getOrCreateContainer(userID, workspace string) (containerName string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.containers == nil {
		s.containers = make(map[string]*dockerContainer)
	}

	if c, ok := s.containers[userID]; ok && c.started {
		return c.name, nil
	}

	containerName = fmt.Sprintf("xbot-%s", userID)

	// 检查容器是否已在运行
	checkCmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName)
	checkOutput, checkErr := checkCmd.Output()
	if checkErr == nil && strings.Contains(string(checkOutput), "true") {
		s.containers[userID] = &dockerContainer{name: containerName, started: true}
		return containerName, nil
	}

	// 容器已存在但未运行，尝试启动
	startCmd := exec.Command("docker", "start", containerName)
	if _, startErr := startCmd.CombinedOutput(); startErr == nil {
		log.Infof("Started existing Docker container %s", containerName)
		s.containers[userID] = &dockerContainer{name: containerName, started: true}
		return containerName, nil
	}

	// 容器不存在，选择镜像：优先用户专属镜像，否则基础镜像
	image := s.image
	userImage := userImageName(userID)
	inspectCmd := exec.Command("docker", "image", "inspect", userImage)
	if inspectCmd.Run() == nil {
		image = userImage
		log.Infof("Using user image %s for container %s", userImage, containerName)
	}

	runArgs := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", fmt.Sprintf("xbot-%s", userID),
		"-v", fmt.Sprintf("%s:/workspace:rw", workspace),
		"-w", "/workspace",
		image,
		"tail", "-f", "/dev/null",
	}

	log.Infof("Creating Docker container %s with image %s", containerName, image)

	runCmd := exec.Command("docker", runArgs...)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w, output: %s", err, string(output))
	}

	s.containers[userID] = &dockerContainer{name: containerName, started: true}
	log.Infof("Docker container %s created successfully", containerName)

	return containerName, nil
}

// NewSandbox 创建沙箱实例
func NewSandbox(mode, image string) Sandbox {
	switch mode {
	case "none":
		return &NoneSandbox{}
	case "bwrap":
		return &BwrapSandbox{}
	case "nsjail":
		return &NsjailSandbox{}
	case "docker":
		return &dockerSandbox{image: image}
	default:
		return &dockerSandbox{image: image}
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
