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

// dockerSandbox Docker 沙箱实现
// 使用 docker commit 持久化用户环境：Close 时将容器提交为用户专属镜像，
// 下次创建容器时优先使用该镜像，从而完整保留 apt install 等所有变更。
type dockerSandbox struct {
	image            string // 基础镜像
	hostWorkDir      string // DinD: 宿主机上对应 WORK_DIR 的路径（空则不翻译）
	containerWorkDir string // DinD: 容器内 WORK_DIR 的路径（空则不翻译）
	mu               sync.Mutex
	containers       map[string]*dockerContainer // userID -> container
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
		if s.verifyWorkspaceMount(containerName, workspace) {
			s.containers[userID] = &dockerContainer{name: containerName, started: true}
			return containerName, nil
		}
		log.Warnf("Container %s has stale workspace mount, will recreate", containerName)
		s.commitAndRemove(containerName, userID)
	}

	// 容器已存在但未运行，尝试启动（先校验 mount 再决定是否复用）
	if s.containerExists(containerName) {
		if s.verifyWorkspaceMount(containerName, workspace) {
			startCmd := exec.Command("docker", "start", containerName)
			if _, startErr := startCmd.CombinedOutput(); startErr == nil {
				log.Infof("Started existing Docker container %s", containerName)
				s.containers[userID] = &dockerContainer{name: containerName, started: true}
				return containerName, nil
			}
		}
		log.Warnf("Container %s has stale workspace mount or failed to start, will recreate", containerName)
		s.commitAndRemove(containerName, userID)
	}

	// 容器不存在，选择镜像：优先用户专属镜像，否则基础镜像
	image := s.image
	userImage := userImageName(userID)
	inspectCmd := exec.Command("docker", "image", "inspect", userImage)
	if inspectCmd.Run() == nil {
		image = userImage
		log.Infof("Using user image %s for container %s", userImage, containerName)
	}

	hostPath := s.toHostPath(workspace)

	runArgs := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", fmt.Sprintf("xbot-%s", userID),
		"-v", fmt.Sprintf("%s:/workspace:rw", hostPath),
		"-w", "/workspace",
		image,
		"tail", "-f", "/dev/null",
	}

	log.Infof("Creating Docker container %s with image %s (mount %s → /workspace)", containerName, image, hostPath)

	runCmd := exec.Command("docker", runArgs...)
	output, err := runCmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to create container: %w, output: %s", err, string(output))
	}

	s.containers[userID] = &dockerContainer{name: containerName, started: true}
	log.Infof("Docker container %s created successfully", containerName)

	return containerName, nil
}

// toHostPath translates a container-local path to the Docker host path.
// In DinD scenarios, xbot runs inside a container where WORK_DIR=/app,
// but the Docker daemon sees the host path (e.g., /home/octopus).
// Returns the path unchanged if no DinD mapping is configured.
func (s *dockerSandbox) toHostPath(containerPath string) string {
	if s.hostWorkDir == "" || s.containerWorkDir == "" {
		return containerPath
	}
	if strings.HasPrefix(containerPath, s.containerWorkDir) {
		return s.hostWorkDir + containerPath[len(s.containerWorkDir):]
	}
	return containerPath
}

// verifyWorkspaceMount checks that the container's /workspace bind mount points to the expected host path.
func (s *dockerSandbox) verifyWorkspaceMount(containerName, expectedWorkspace string) bool {
	cmd := exec.Command("docker", "inspect", "-f",
		`{{range .Mounts}}{{if eq .Destination "/workspace"}}{{.Source}}{{end}}{{end}}`,
		containerName)
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	actual := strings.TrimSpace(string(output))
	expected := s.toHostPath(expectedWorkspace)
	if actual == expected {
		return true
	}
	log.WithFields(log.Fields{
		"container": containerName,
		"expected":  expected,
		"actual":    actual,
	}).Warn("Workspace mount mismatch")
	return false
}

// containerExists checks whether a Docker container exists (running or stopped).
func (s *dockerSandbox) containerExists(containerName string) bool {
	cmd := exec.Command("docker", "inspect", "-f", "{{.Id}}", containerName)
	return cmd.Run() == nil
}

// commitAndRemove commits a container (preserving installed packages etc.) then stops and removes it.
func (s *dockerSandbox) commitAndRemove(containerName, userID string) {
	s.commitIfDirty(containerName, userID)

	// Force-kill + remove in one step (most reliable for stale containers)
	forceRm := exec.Command("docker", "rm", "-f", containerName)
	if out, err := forceRm.CombinedOutput(); err != nil {
		log.WithError(err).Warnf("Failed to force-remove container %s: %s", containerName, strings.TrimSpace(string(out)))
	} else {
		log.Infof("Force-removed stale container %s", containerName)
	}
}

// migrateDinDWorkspaces migrates user workspace data that was written to the wrong
// host path due to DinD path mismatch. This happens when xbot runs in a container
// (e.g., WORK_DIR=/app) and creates sandbox containers with bind mounts using the
// container-internal path instead of the real host path. The Docker daemon interprets
// this as a host path, creating data at e.g. host:/app/.xbot/ instead of
// host:/home/user/.xbot/. This function detects and moves such misplaced data.
func (s *dockerSandbox) migrateDinDWorkspaces() {
	if s.hostWorkDir == "" || s.containerWorkDir == "" || s.hostWorkDir == s.containerWorkDir {
		return
	}

	// The wrong host path is where Docker daemon wrote data (using container-internal path as host path)
	// e.g., /app/.xbot/users  (on the real host filesystem)
	oldHostUsers := s.containerWorkDir + "/.xbot/users"
	newHostUsers := s.hostWorkDir + "/.xbot/users"

	// Quick check: see if the wrong path exists on the host by mounting it into a temp container
	checkCmd := exec.Command("docker", "run", "--rm",
		"-v", oldHostUsers+":/dind_check:ro",
		s.image,
		"sh", "-c", "ls /dind_check 2>/dev/null | head -1")
	checkOutput, err := checkCmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(checkOutput)) == "" {
		return
	}

	log.Warnf("DinD migration: found misplaced workspace data at host:%s, migrating to host:%s", oldHostUsers, newHostUsers)

	// Copy data from wrong location to correct location (preserve existing data with cp -a)
	migrateCmd := exec.Command("docker", "run", "--rm",
		"-v", oldHostUsers+":/old:ro",
		"-v", newHostUsers+":/new",
		s.image,
		"sh", "-c", "cp -a /old/. /new/")
	if out, err := migrateCmd.CombinedOutput(); err != nil {
		log.Warnf("DinD migration: copy failed: %v, output: %s", err, string(out))
		return
	}

	// Remove the wrong host path to prevent future confusion
	cleanCmd := exec.Command("docker", "run", "--rm",
		"-v", s.containerWorkDir+":/dind_cleanup",
		s.image,
		"sh", "-c", "rm -rf /dind_cleanup/.xbot")
	if out, err := cleanCmd.CombinedOutput(); err != nil {
		log.Warnf("DinD migration: cleanup failed: %v, output: %s", err, string(out))
	}

	log.Infof("DinD migration completed: host:%s → host:%s", oldHostUsers, newHostUsers)
}

// NewSandbox 创建沙箱实例
func NewSandbox(mode, image string) Sandbox {
	cfg := config.Load()
	switch mode {
	case "none":
		return &NoneSandbox{}
	case "docker":
		s := &dockerSandbox{image: image}
		s.detectDinD(cfg)
		return s
	default:
		return &dockerSandbox{image: image}
	}
}

// detectDinD auto-detects Docker-in-Docker and sets up host path mapping.
// When xbot runs inside a container, bind mount paths must be translated from
// the container-internal path (e.g., /app/.xbot/...) to the real host path
// (e.g., /home/user/.xbot/...) because the Docker daemon runs on the host.
func (s *dockerSandbox) detectDinD(cfg *config.Config) {
	absWorkDir, _ := filepath.Abs(cfg.Agent.WorkDir)

	// Priority 1: explicit override via HOST_WORK_DIR
	if cfg.Sandbox.HostWorkDir != "" {
		s.containerWorkDir = absWorkDir
		s.hostWorkDir = cfg.Sandbox.HostWorkDir
		log.Infof("DinD path mapping (explicit): container %s → host %s", absWorkDir, s.hostWorkDir)
		s.migrateDinDWorkspaces()
		return
	}

	// Priority 2: auto-detect by inspecting our own container
	if _, err := os.Stat("/.dockerenv"); err != nil {
		return // not running inside Docker
	}

	hostPath := s.autoDetectHostPath(absWorkDir)
	if hostPath == "" || hostPath == absWorkDir {
		return // not DinD or path is the same (direct host mount matches)
	}

	s.containerWorkDir = absWorkDir
	s.hostWorkDir = hostPath
	log.Infof("DinD path mapping (auto-detected): container %s → host %s", absWorkDir, hostPath)
	s.migrateDinDWorkspaces()
}

// autoDetectHostPath inspects xbot's own container to find the host-side path
// that corresponds to containerPath. Returns "" if detection fails.
func (s *dockerSandbox) autoDetectHostPath(containerPath string) string {
	containerID := detectOwnContainerID()
	if containerID == "" {
		log.Debug("DinD auto-detect: could not determine own container ID")
		return ""
	}

	// Inspect our own container's bind mounts
	cmd := exec.Command("docker", "inspect", "-f",
		`{{range .Mounts}}{{if eq .Type "bind"}}{{.Destination}}={{.Source}}`+"\n"+`{{end}}{{end}}`,
		containerID)
	output, err := cmd.Output()
	if err != nil {
		log.WithError(err).Debug("DinD auto-detect: docker inspect failed")
		return ""
	}

	// Find the mount whose Destination is containerPath or a parent of it
	var bestDest, bestSrc string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 || parts[0] == "" {
			continue
		}
		dest, src := parts[0], parts[1]
		if containerPath == dest || strings.HasPrefix(containerPath, dest+"/") {
			// Prefer the longest (most specific) match
			if len(dest) > len(bestDest) {
				bestDest, bestSrc = dest, src
			}
		}
	}

	if bestDest == "" {
		return ""
	}

	// Translate: if mount is /app→/home/user and containerPath is /app,
	// then host path is /home/user
	rel := strings.TrimPrefix(containerPath, bestDest)
	return bestSrc + rel
}

// detectOwnContainerID returns our container ID if we're running in Docker.
func detectOwnContainerID() string {
	// Method 1: hostname (Docker sets it to the short container ID by default)
	hostname, err := os.Hostname()
	if err == nil && hostname != "" {
		cmd := exec.Command("docker", "inspect", "-f", "{{.Id}}", hostname)
		if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
			return hostname
		}
	}

	// Method 2: /proc/self/cgroup (cgroup v1)
	if data, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			// Format: hierarchy-ID:controller-list:/docker/<container-id>
			if idx := strings.Index(line, "/docker/"); idx >= 0 {
				id := line[idx+len("/docker/"):]
				id = strings.TrimSpace(id)
				if len(id) >= 12 {
					return id[:12]
				}
			}
		}
	}

	// Method 3: /proc/1/cpuset
	if data, err := os.ReadFile("/proc/1/cpuset"); err == nil {
		content := strings.TrimSpace(string(data))
		if strings.HasPrefix(content, "/docker/") {
			id := strings.TrimPrefix(content, "/docker/")
			if len(id) >= 12 {
				return id[:12]
			}
		}
	}

	return ""
}

// WrapCommandForSandbox 将命令包装到沙箱执行（兼容旧接口）
// Deprecated: 使用 NewSandbox 创建的沙箱实例的 Wrap 方法
func WrapCommandForSandbox(command string, args []string, workspaceRoot string) (string, []string, error) {
	return WrapCommandForSandboxWithEnv(command, args, nil, workspaceRoot)
}

// WrapCommandForSandboxWithEnv 将命令包装到沙箱执行，带环境变量
func WrapCommandForSandboxWithEnv(command string, args []string, env []string, workspaceRoot string) (string, []string, error) {
	// 使用 docker 沙箱
	s := &dockerSandbox{image: "ubuntu:22.04"}
	return s.Wrap(command, args, env, workspaceRoot, "")
}
