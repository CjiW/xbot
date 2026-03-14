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
	shell   string // 用户默认 shell（从容器内 /etc/passwd 获取）
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

	containerName, shell, err := s.getOrCreateContainer(userID, ws)
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

	// 使用检测到的 shell 执行命令
	// 注意：不使用 -l (login shell)，因为：
	// 1. login shell 会 source /etc/profile，可能覆盖通过 -e 传入的环境变量
	// 2. 环境变量由 docker run -e 传入，已经在容器内生效
	shellCmd := command
	if len(args) > 0 {
		shellCmd = command + " " + strings.Join(args, " ")
	}
	dockerArgs = append(dockerArgs, containerName, shell, "-c", shellCmd)

	return "docker", dockerArgs, nil
}

// getOrCreateContainer 获取或创建用户的 Docker 容器
// 优先使用用户专属镜像（由 docker commit 生成），不存在则用基础镜像
// 返回容器名称和检测到的用户默认 shell
func (s *dockerSandbox) getOrCreateContainer(userID, workspace string) (containerName, shell string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.containers == nil {
		s.containers = make(map[string]*dockerContainer)
	}

	if c, ok := s.containers[userID]; ok && c.started {
		return c.name, c.shell, nil
	}

	containerName = fmt.Sprintf("xbot-%s", userID)

	// 检查容器是否已在运行
	checkCmd := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", containerName)
	checkOutput, checkErr := checkCmd.Output()
	if checkErr == nil && strings.Contains(string(checkOutput), "true") {
		if s.verifyWorkspaceMount(containerName, workspace) {
			shell := s.detectShell(containerName)
			s.containers[userID] = &dockerContainer{name: containerName, started: true, shell: shell}
			return containerName, shell, nil
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
				shell := s.detectShell(containerName)
				s.containers[userID] = &dockerContainer{name: containerName, started: true, shell: shell}
				return containerName, shell, nil
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
		return "", "", fmt.Errorf("failed to create container: %w, output: %s", err, string(output))
	}

	// 检测用户的默认 shell
	shell = s.detectShell(containerName)
	s.containers[userID] = &dockerContainer{name: containerName, started: true, shell: shell}
	log.Infof("Docker container %s created successfully with shell %s", containerName, shell)

	return containerName, shell, nil
}

// detectShell 从容器内的 /etc/passwd 获取用户的默认 shell
func (s *dockerSandbox) detectShell(containerName string) string {
	// 获取 root 用户的默认 shell
	cmd := exec.Command("docker", "exec", containerName,
		"sh", "-c", "grep '^root:' /etc/passwd | cut -d: -f7")
	output, err := cmd.Output()
	if err != nil || len(strings.TrimSpace(string(output))) == 0 {
		log.WithError(err).Warnf("Failed to detect shell for container %s, using /bin/sh", containerName)
		return "/bin/sh"
	}
	shell := strings.TrimSpace(string(output))
	log.Debugf("Detected shell %s for container %s", shell, containerName)
	return shell
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
// host path due to DinD path mismatch. Before the fix, sandbox bind mounts used the
// container-internal path (containerWorkDir) as a host path, causing Docker daemon to
// create data at host:<containerWorkDir>/users/ instead of host:<hostWorkDir>/users/.
//
// Example: containerWorkDir=/app/.xbot, hostWorkDir=/home/octopus/.xbot
//   - Wrong location on host:   /app/.xbot/users/...
//   - Correct location on host: /home/octopus/.xbot/users/...
func (s *dockerSandbox) migrateDinDWorkspaces() {
	if s.hostWorkDir == "" || s.containerWorkDir == "" || s.hostWorkDir == s.containerWorkDir {
		return
	}

	// The wrong host path is containerWorkDir used verbatim as a host path
	oldHostUsers := s.containerWorkDir + "/users"
	newHostUsers := s.hostWorkDir + "/users"

	checkCmd := exec.Command("docker", "run", "--rm",
		"-v", oldHostUsers+":/dind_check:ro",
		s.image,
		"sh", "-c", "ls /dind_check 2>/dev/null | head -1")
	checkOutput, err := checkCmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(checkOutput)) == "" {
		return
	}

	log.Warnf("DinD migration: found misplaced workspace data at host:%s, migrating to host:%s", oldHostUsers, newHostUsers)

	migrateCmd := exec.Command("docker", "run", "--rm",
		"-v", oldHostUsers+":/old:ro",
		"-v", newHostUsers+":/new",
		s.image,
		"sh", "-c", "cp -a /old/. /new/")
	if out, err := migrateCmd.CombinedOutput(); err != nil {
		log.Warnf("DinD migration: copy failed: %v, output: %s", err, string(out))
		return
	}

	// Cleanup: mount the PARENT of containerWorkDir, remove the base dir
	parentDir := filepath.Dir(s.containerWorkDir)
	baseName := filepath.Base(s.containerWorkDir)
	cleanCmd := exec.Command("docker", "run", "--rm",
		"-v", parentDir+":/dind_cleanup",
		s.image,
		"sh", "-c", fmt.Sprintf("rm -rf /dind_cleanup/%s", baseName))
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
//
// The mount can be at workDir itself (/home/octopus → /app) or at a sub-path
// (/home/octopus/.xbot → /app/.xbot). Both cases are handled.
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

	// Priority 2: auto-detect by scanning running containers for a bind mount
	// that covers or is under our WORK_DIR.
	containerMount, hostMount := s.autoDetectDinDMount(absWorkDir)
	if containerMount == "" || hostMount == "" || containerMount == hostMount {
		return
	}

	s.containerWorkDir = containerMount
	s.hostWorkDir = hostMount
	log.Infof("DinD path mapping (auto-detected): container %s → host %s", containerMount, hostMount)
	s.migrateDinDWorkspaces()
}

// autoDetectDinDMount scans all running Docker containers to find a bind mount
// related to workDir. It matches mounts whose destination:
//   - equals workDir or is an ancestor (/app when workDir is /app/.xbot)
//   - is a descendant of workDir (/app/.xbot when workDir is /app)
//
// Returns (mountDest, mountSrc) directly — caller uses them as containerWorkDir/hostWorkDir.
func (s *dockerSandbox) autoDetectDinDMount(workDir string) (containerMount, hostMount string) {
	listCmd := exec.Command("docker", "ps", "-q")
	listOutput, err := listCmd.Output()
	if err != nil {
		log.Warnf("DinD auto-detect: docker ps failed: %v", err)
		return "", ""
	}

	ids := strings.Fields(strings.TrimSpace(string(listOutput)))
	log.Infof("DinD auto-detect: scanning %d containers for mount related to %s", len(ids), workDir)
	if len(ids) == 0 {
		return "", ""
	}

	var bestDest, bestSrc string
	for _, id := range ids {
		cmd := exec.Command("docker", "inspect", "-f",
			`{{range .Mounts}}{{.Destination}}={{.Source}}={{.Type}}`+"\n"+`{{end}}`,
			id)
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(output), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			eqIdx := strings.Index(line, "=")
			if eqIdx <= 0 {
				continue
			}
			dest := line[:eqIdx]
			rest := line[eqIdx+1:]
			lastEq := strings.LastIndex(rest, "=")
			if lastEq < 0 {
				continue
			}
			src := rest[:lastEq]

			// Match: dest is workDir, ancestor of workDir, or descendant of workDir
			matched := dest == workDir ||
				strings.HasPrefix(workDir, dest+"/") ||
				strings.HasPrefix(dest, workDir+"/")

			if matched && len(dest) > len(bestDest) {
				bestDest, bestSrc = dest, src
				log.Infof("DinD auto-detect: candidate mount %s → %s (container %s)", dest, src, id[:12])
			}
		}
	}

	if bestDest == "" {
		log.Warnf("DinD auto-detect: no mount found related to %s among %d containers", workDir, len(ids))
		return "", ""
	}

	return bestDest, bestSrc
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
