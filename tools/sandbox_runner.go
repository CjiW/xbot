package tools

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/config"
	log "xbot/logger"
)

const (
	dockerCmdTimeout  = 30 * time.Second  // 普通 docker 命令超时
	dockerSlowTimeout = 120 * time.Second // 慢操作（export/import/commit）超时
)

// dockerExec runs a docker command with a timeout, returning combined output.
func dockerExec(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// dockerRun runs a docker command with a timeout, returning only error.
func dockerRun(timeout time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Run()
}

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
	// GetShell 获取用户在沙箱中的默认 shell（如 /bin/bash）
	GetShell(userID string, workspace string) (string, error)
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

func (s *NoneSandbox) GetShell(userID string, workspace string) (string, error) {
	// 返回系统默认 shell
	return "/bin/bash", nil
}

// dockerSandbox Docker 沙箱实现
// 使用 docker commit 持久化用户环境：Close 时将容器提交为用户专属镜像，
// 下次创建容器时优先使用该镜像，从而完整保留 apt install 等所有变更。
// 定期用 export+import 扁平化镜像，避免层累积浪费磁盘空间。
type dockerSandbox struct {
	image                 string // 基础镜像
	hostWorkDir           string // DinD: 宿主机上对应 WORK_DIR 的路径（空则不翻译）
	containerWorkDir      string // DinD: 容器内 WORK_DIR 的路径（空则不翻译）
	mu                    sync.Mutex
	containers            map[string]*dockerContainer // userID -> container
	commitSquashThreshold int                         // commit 达到此阈值时扁平化镜像
}

type dockerContainer struct {
	name    string
	started bool
	shell   string // 用户默认 shell（从容器内 /etc/passwd 获取）
}

func (s *dockerSandbox) Name() string { return "docker" }

// Close 关闭并清理所有 Docker 容器
// 优化流程：每个容器单独 stop → commit → rm
// 先 stop 让容器停止（避免 commit 时的并发写入），再 commit（更快更稳定），最后 rm
func (s *dockerSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 每个容器单独处理：stop → commit → rm
	for userID, c := range s.containers {
		if !c.started {
			continue
		}

		// 1. 先 stop（1秒足够，workspace 是 bind mount 不会丢数据）
		if err := dockerRun(dockerCmdTimeout, "stop", "-t", "1", c.name); err != nil {
			log.WithError(err).Warnf("Failed to stop container %s", c.name)
			// stop 失败，尝试直接 force remove
			if rmErr := dockerRun(dockerCmdTimeout, "rm", "-f", c.name); rmErr != nil {
				log.WithError(rmErr).Warnf("Failed to force remove container %s after stop failure", c.name)
			} else {
				log.Infof("Force removed Docker container %s (stop failed)", c.name)
				delete(s.containers, userID)
				continue
			}
		} else {
			log.Infof("Stopped Docker container %s", c.name)
		}

		// 2. 容器停止后 commit（更快更稳定，无需处理并发写入）
		s.commitIfDirty(c.name, userID)

		// 3. 最后 rm
		if err := dockerRun(dockerCmdTimeout, "rm", "-f", c.name); err != nil {
			log.WithError(err).Warnf("Failed to remove container %s", c.name)
		} else {
			log.Infof("Removed Docker container %s", c.name)
		}
	}

	s.containers = make(map[string]*dockerContainer)
	return nil
}

// commitCountLabel is the Docker image label key used to persist the commit
// count across xbot restarts. Previously this was stored in-memory on
// dockerContainer.commitCount, which reset to 0 on every restart, causing
// squash to never trigger and user images to bloat (8GB FS → 50GB image).
const commitCountLabel = "xbot.commit.count"

// readCommitCount reads the persisted commit count from a Docker image label.
// Returns 0 if the image doesn't exist.
// Returns -1 if the image exists but has no commit count label (legacy image,
// never been squashed — caller should treat this as "needs immediate squash").
func readCommitCount(imageName string) int {
	out, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f",
		fmt.Sprintf("{{.Config.Labels.%s}}", strings.ReplaceAll(commitCountLabel, ".", "_")), imageName)
	if err != nil {
		return 0 // image doesn't exist
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "<no value>" {
		return -1 // image exists but label missing (legacy)
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return -1
	}
	return n
}

// commitIfDirty 仅在容器有文件系统变更时 commit，保存用户环境状态。
// 通过定期 export+import 扁平化镜像，避免层累积浪费磁盘空间。
// commitCount 持久化在 Docker image label 上，跨重启存活。
// 调用方必须持有 s.mu 锁。
func (s *dockerSandbox) commitIfDirty(containerName, userID string) {
	if userID == "" || strings.HasPrefix(userID, "__") {
		log.Debugf("Skipping commit for system container %s (userID=%q)", containerName, userID)
		return
	}

	diffOut, err := dockerExec(dockerCmdTimeout, "diff", containerName)
	if err != nil {
		log.WithError(err).Warnf("Failed to check diff for container %s, skipping commit", containerName)
		return
	}
	if len(strings.TrimSpace(string(diffOut))) == 0 {
		log.Infof("Container %s has no changes, skipping commit", containerName)
		return
	}

	userImage := userImageName(userID)

	// Read persisted commit count from image label.
	// -1 = legacy image without label → treat as needing immediate squash.
	persistedCount := readCommitCount(userImage)
	commitCount := persistedCount + 1
	if persistedCount < 0 {
		// Legacy image: force squash on this commit regardless of threshold
		commitCount = s.commitSquashThreshold
		log.Infof("Legacy image %s detected (no commit count label), will squash after commit", userImage)
	}

	// Capture old image ID before commit
	var oldImageID string
	if out, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Id}}", userImage); err == nil {
		oldImageID = strings.TrimSpace(string(out))
	}

	// Commit with incremented count as image label
	if err := dockerRun(dockerSlowTimeout, "commit",
		"--change", fmt.Sprintf("LABEL %s=%d", commitCountLabel, commitCount),
		containerName, userImage); err != nil {
		log.WithError(err).Warnf("Failed to commit container %s to image %s", containerName, userImage)
		return
	}
	log.Infof("Committed container %s to image %s (commit count: %d)", containerName, userImage, commitCount)

	// Clean up old image layer
	if oldImageID != "" {
		if newOut, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Id}}", userImage); err == nil {
			newImageID := strings.TrimSpace(string(newOut))
			if newImageID != oldImageID {
				if err := dockerRun(dockerCmdTimeout, "rmi", oldImageID); err != nil {
					log.WithError(err).Debugf("Failed to remove old image %s (may still be referenced)", oldImageID)
				} else {
					log.Infof("Cleaned up old image %s", oldImageID[:12])
				}
			}
		}
	}

	if out, err := dockerExec(dockerCmdTimeout, "image", "prune", "-f"); err != nil {
		log.WithError(err).Debugf("Failed to prune dangling images")
	} else if strings.Contains(string(out), "Total") {
		log.Debugf("Pruned dangling images: %s", strings.TrimSpace(string(out)))
	}

	// Check if squash is needed (count persisted in image label survives restarts)
	if s.commitSquashThreshold > 0 && commitCount >= s.commitSquashThreshold {
		log.Infof("Commit count reached threshold (%d >= %d), squashing image %s",
			commitCount, s.commitSquashThreshold, userImage)
		if s.squashImage(containerName, userImage) {
			// squashImage resets the label to 0 via --change during import
			log.Infof("Squash complete, commit count reset for image %s", userImage)
		}
	}
}

// squashImage 用 export+import 扁平化镜像，生成单层镜像节省空间。
// 调用方必须持有 s.mu 锁（通过 commitIfDirty 调用）。
func (s *dockerSandbox) squashImage(containerName, userImage string) bool {
	// 1. 获取旧镜像的元数据（docker export/import 会丢失这些）
	inspectFmt := "{{json .Config.Cmd}}||{{json .Config.Entrypoint}}||{{.Config.WorkingDir}}||{{json .Config.Env}}"
	inspectOut, _ := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", inspectFmt, userImage)
	var changes []string
	if parts := strings.SplitN(strings.TrimSpace(string(inspectOut)), "||", 4); len(parts) == 4 {
		if cmd := parts[0]; cmd != "" && cmd != "null" {
			changes = append(changes, fmt.Sprintf("CMD %s", cmd))
		}
		if ep := parts[1]; ep != "" && ep != "null" {
			changes = append(changes, fmt.Sprintf("ENTRYPOINT %s", ep))
		}
		if wd := parts[2]; wd != "" {
			changes = append(changes, fmt.Sprintf("WORKDIR %s", wd))
		}
		if envJSON := parts[3]; envJSON != "" && envJSON != "null" {
			for _, env := range parseJSONStringArray(envJSON) {
				if !strings.HasPrefix(env, "PATH=") {
					changes = append(changes, fmt.Sprintf("ENV %s", env))
				}
			}
		}
	}

	// 2. export 容器文件系统到临时 tar
	tmpFile, err := os.CreateTemp("", "xbot-squash-*.tar")
	if err != nil {
		log.WithError(err).Warnf("Failed to create temp file for squash")
		return false
	}
	tmpTar := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpTar)

	if out, err := dockerExec(dockerSlowTimeout, "export", "-o", tmpTar, containerName); err != nil {
		log.WithError(err).Warnf("Failed to export container %s: %s", containerName, strings.TrimSpace(string(out)))
		return false
	}

	// 3. 记录旧镜像 ID
	var oldImageID string
	if out, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Id}}", userImage); err == nil {
		oldImageID = strings.TrimSpace(string(out))
	}

	// 4. import 为新镜像，用 --change 恢复元数据并重置 commitCount label
	importArgs := []string{"import"}
	// Reset commit count label (squash starts a new cycle)
	importArgs = append(importArgs, "--change", fmt.Sprintf("LABEL %s=0", commitCountLabel))
	for _, c := range changes {
		importArgs = append(importArgs, "--change", c)
	}
	importArgs = append(importArgs, tmpTar, userImage)
	if out, err := dockerExec(dockerSlowTimeout, importArgs...); err != nil {
		log.WithError(err).Warnf("Failed to import squashed image %s: %s", userImage, strings.TrimSpace(string(out)))
		return false
	}
	log.WithField("changes", len(changes)).Infof("Squashed image %s (single layer with metadata restored)", userImage)

	// 5. 删除旧镜像（如果 ID 不同，说明 import 生成了新镜像）
	if oldImageID != "" {
		if newOut, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Id}}", userImage); err == nil {
			newImageID := strings.TrimSpace(string(newOut))
			if newImageID != oldImageID {
				if err := dockerRun(dockerCmdTimeout, "rmi", oldImageID); err != nil {
					log.WithError(err).Debugf("Failed to remove old image %s after squash (may still be referenced)", oldImageID[:12])
				} else {
					log.Infof("Removed old multi-layer image %s", oldImageID[:12])
				}
			}
		}
	}

	// 6. 清理 dangling images
	if out, err := dockerExec(dockerCmdTimeout, "image", "prune", "-f"); err != nil {
		log.WithError(err).Debugf("Failed to prune dangling images after squash")
	} else if strings.Contains(string(out), "Total") {
		log.Debugf("Pruned dangling images: %s", strings.TrimSpace(string(out)))
	}

	return true
}

// parseJSONStringArray parses a JSON string array like ["foo","bar"] into a Go slice.
func parseJSONStringArray(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	s = s[1 : len(s)-1]
	if s == "" {
		return nil
	}
	var result []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if len(item) >= 2 && item[0] == '"' && item[len(item)-1] == '"' {
			result = append(result, item[1:len(item)-1])
		}
	}
	return result
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

	containerName, _, err := s.getOrCreateContainer(userID, ws)
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

	// 直接透传 command + args，不做 shell 包装
	// 职责边界：Wrap 只负责将调用方的命令透传给 docker exec
	// shell 包装（-l -c）由调用方在需要时自行构造，例如：
	//   - ShellTool: 用 login shell 自动加载 ~/.bashrc
	//   - RunInSandboxWithShell: 用 login shell
	//   - 测试: 直接传 command + args，按需自行决定
	dockerArgs = append(dockerArgs, containerName, command)
	dockerArgs = append(dockerArgs, args...)

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
	checkOutput, checkErr := dockerExec(dockerCmdTimeout, "inspect", "-f", "{{.State.Running}}", containerName)
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
			if startErr := dockerRun(dockerCmdTimeout, "start", containerName); startErr == nil {
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
	if err := dockerRun(dockerCmdTimeout, "image", "inspect", userImage); err == nil {
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

	output, err := dockerExec(dockerCmdTimeout, runArgs...)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w, output: %s", err, string(output))
	}

	// 检测用户的默认 shell
	shell = s.detectShell(containerName)
	s.containers[userID] = &dockerContainer{name: containerName, started: true, shell: shell}
	log.Infof("Docker container %s created successfully with shell %s", containerName, shell)

	return containerName, shell, nil
}

// GetShell 获取用户在沙箱中的默认 shell（如 /bin/bash）
// 如果容器不存在会自动创建
func (s *dockerSandbox) GetShell(userID string, workspace string) (string, error) {
	ws := workspace
	if ws == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		ws = cwd
	}
	ws, err := filepath.Abs(ws)
	if err != nil {
		return "", err
	}

	// 获取或创建容器，同时获取 shell
	_, shell, err := s.getOrCreateContainer(userID, ws)
	return shell, err
}

// detectShell 从容器内的 /etc/passwd 获取用户的默认 shell
func (s *dockerSandbox) detectShell(containerName string) string {
	// 获取 root 用户的默认 shell
	output, err := dockerExec(dockerCmdTimeout, "exec", containerName,
		"sh", "-c", "grep '^root:' /etc/passwd | cut -d: -f7")
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
	output, err := dockerExec(dockerCmdTimeout, "inspect", "-f",
		`{{range .Mounts}}{{if eq .Destination "/workspace"}}{{.Source}}{{end}}{{end}}`,
		containerName)
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
	return dockerRun(dockerCmdTimeout, "inspect", "-f", "{{.Id}}", containerName) == nil
}

// commitAndRemove commits a container (preserving installed packages etc.) then stops and removes it.
func (s *dockerSandbox) commitAndRemove(containerName, userID string) {
	s.commitIfDirty(containerName, userID)

	// Force-kill + remove in one step (most reliable for stale containers)
	if out, err := dockerExec(dockerCmdTimeout, "rm", "-f", containerName); err != nil {
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

	checkOutput, err := dockerExec(dockerCmdTimeout, "run", "--rm",
		"-v", oldHostUsers+":/dind_check:ro",
		s.image,
		"sh", "-c", "ls /dind_check 2>/dev/null | head -1")
	if err != nil || strings.TrimSpace(string(checkOutput)) == "" {
		return
	}

	log.Warnf("DinD migration: found misplaced workspace data at host:%s, migrating to host:%s", oldHostUsers, newHostUsers)

	if out, err := dockerExec(dockerSlowTimeout, "run", "--rm",
		"-v", oldHostUsers+":/old:ro",
		"-v", newHostUsers+":/new",
		s.image,
		"sh", "-c", "cp -a /old/. /new/"); err != nil {
		log.Warnf("DinD migration: copy failed: %v, output: %s", err, string(out))
		return
	}

	// Cleanup: mount the PARENT of containerWorkDir, remove the base dir
	parentDir := filepath.Dir(s.containerWorkDir)
	baseName := filepath.Base(s.containerWorkDir)
	if out, err := dockerExec(dockerCmdTimeout, "run", "--rm",
		"-v", parentDir+":/dind_cleanup",
		s.image,
		"sh", "-c", fmt.Sprintf("rm -rf /dind_cleanup/%s", baseName)); err != nil {
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
		s := &dockerSandbox{
			image:                 image,
			commitSquashThreshold: cfg.Sandbox.CommitSquashThreshold,
		}
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
	listOutput, err := dockerExec(dockerCmdTimeout, "ps", "-q")
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
		output, err := dockerExec(dockerCmdTimeout, "inspect", "-f",
			`{{range .Mounts}}{{.Destination}}={{.Source}}={{.Type}}`+"\n"+`{{end}}`,
			id)
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
