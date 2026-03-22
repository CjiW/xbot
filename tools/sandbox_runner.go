package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"xbot/config"
	log "xbot/logger"
)

const (
	dockerCmdTimeout  = 30 * time.Second  // 普通 docker 命令超时
	dockerSlowTimeout = 120 * time.Second // 慢操作（export/import）超时
)

// dockerExec runs a docker command with a timeout (0 = no timeout), returning combined output.
func dockerExec(timeout time.Duration, args ...string) ([]byte, error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// dockerRun runs a docker command with a timeout (0 = no timeout), returning only error.
func dockerRun(timeout time.Duration, args ...string) error {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Run()
}

// dockerPipelineExportImport pipes docker export stdout into docker import stdin,
// avoiding a large intermediate tar file on disk. Falls back to temp-file approach on error.
func dockerPipelineExportImport(ctx context.Context, containerName string, importArgs []string) ([]byte, error) {
	exportCmd := exec.CommandContext(ctx, "docker", "export", containerName)
	importCmd := exec.CommandContext(ctx, "docker", importArgs...)

	importCmd.Stdin, _ = exportCmd.StdoutPipe()
	importCmd.Stderr = nil // will be captured via CombinedOutput on importCmd

	var importOut bytes.Buffer
	importCmd.Stdout = &importOut
	importCmd.Stderr = &importOut

	if err := exportCmd.Start(); err != nil {
		return nil, fmt.Errorf("start export: %w", err)
	}
	if err := importCmd.Start(); err != nil {
		exportCmd.Process.Kill()
		exportCmd.Wait()
		return nil, fmt.Errorf("start import: %w", err)
	}

	exportErr := exportCmd.Wait()
	importErr := importCmd.Wait()

	out := importOut.Bytes()
	if exportErr != nil {
		return out, fmt.Errorf("export: %w", exportErr)
	}
	if importErr != nil {
		return out, fmt.Errorf("import: %w", importErr)
	}
	return out, nil
}

// 全局沙箱实例
var globalSandbox Sandbox
var sandboxInitOnce sync.Once

// InitSandbox 初始化全局沙箱实例（由 main.go 在启动时调用）
// 启动时自动清理上次残留的临时文件和悬空 Docker 资源。
func InitSandbox(sandboxCfg config.SandboxConfig, workDir string) {
	sandboxInitOnce.Do(func() {
		if sandboxCfg.Mode == "docker" {
			cleanupStaleTmpFiles()
			pruneDockerResources()
		}
		globalSandbox = NewSandbox(sandboxCfg, workDir)
		log.Infof("Sandbox initialized: %s", globalSandbox.Name())
	})
}

// GetSandbox 获取全局沙箱实例
func GetSandbox() Sandbox {
	sandboxInitOnce.Do(func() {
		// Fallback: 如果 InitSandbox 未被调用（例如测试场景），使用 NoneSandbox
		log.Warn("GetSandbox called before InitSandbox, falling back to NoneSandbox")
		globalSandbox = &NoneSandbox{}
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
	// CloseForUser 关闭并清理指定用户的沙箱资源（仅 stop）
	CloseForUser(userID string) error
	// PostExec 命令执行后异步触发 export+import 持久化（dirty check）
	PostExec(userID string)
}

// NoneSandbox 无沙箱模式，直接执行
type NoneSandbox struct{}

func (s *NoneSandbox) Name() string { return "none" }

func (s *NoneSandbox) Close() error                     { return nil }
func (s *NoneSandbox) CloseForUser(userID string) error { return nil }
func (s *NoneSandbox) PostExec(userID string)           {}

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
// 容器生命周期：Close 时仅 stop（不 rm），下次直接 start 复用。
// export+import 仅在命令执行产生文件系统变更时触发，确保用户环境持久化。
// 始终使用 export+import（而非 docker commit），避免镜像层累积迅速耗尽磁盘空间。
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

// Close 关闭所有 Docker 容器（仅 stop，不 rm 也不 export/import）。
// export/import 仅在命令执行时触发（见 Wrap），Close 只负责快速停止。
// 容器保留在磁盘上，下次 getOrCreateContainer 时直接 docker start 复用。
func (s *dockerSandbox) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for userID, c := range s.containers {
		if !c.started {
			continue
		}
		if err := dockerRun(dockerCmdTimeout, "stop", "-t", "1", c.name); err != nil {
			log.WithError(err).Warnf("Failed to stop container %s", c.name)
			dockerRun(dockerCmdTimeout, "rm", "-f", c.name)
			delete(s.containers, userID)
		} else {
			c.started = false
			log.Infof("Stopped Docker container %s", c.name)
		}
	}
	return nil
}

// CloseForUser 关闭指定用户的容器（仅 stop，不 rm 也不 export/import）。
// export/import 仅在命令执行时触发，CloseForUser 只负责快速停止（空闲超时卸载）。
// 容器保留在磁盘上，下次直接 docker start 复用。
func (s *dockerSandbox) CloseForUser(userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.containers[userID]
	if !ok || !c.started {
		return nil
	}

	if err := dockerRun(dockerCmdTimeout, "stop", "-t", "1", c.name); err != nil {
		log.WithError(err).Warnf("Failed to stop container %s for idle cleanup", c.name)
	} else {
		c.started = false
		log.Infof("Stopped Docker container %s (idle cleanup for user %s)", c.name, userID)
	}
	return nil
}

// PostExec 命令执行后异步触发 export+import 持久化（仅在有文件系统变更时）。
// 异步执行，不阻塞命令返回。export+import 可能耗时数十秒（大型 FS），
// 但对用户透明——下次 getOrCreateContainer 会直接 docker start 复用现有容器。
func (s *dockerSandbox) PostExec(userID string) {
	s.mu.Lock()
	c, ok := s.containers[userID]
	if !ok || !c.started {
		s.mu.Unlock()
		return
	}
	containerName := c.name
	s.mu.Unlock()

	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		// 再次检查容器是否仍然存在且已启动（可能已被 Close 停止）
		if current, ok := s.containers[userID]; !ok || !current.started {
			return
		}
		s.exportImportIfDirty(containerName, userID)
	}()
}

// cleanupStaleTmpFiles 清理上次异常退出残留的 export 临时文件。
// 进程被 OOM kill 或系统重启时，defer os.Remove 不会执行，tar 文件会残留在 /tmp。
func cleanupStaleTmpFiles() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "xbot-export-*.tar"))
	if err != nil {
		return
	}
	for _, f := range matches {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		// 只清理超过 10 分钟的文件（避免误删正在使用的）
		if time.Since(info.ModTime()) > 10*time.Minute {
			if err := os.Remove(f); err == nil {
				log.Infof("Cleaned up stale tmp file: %s (%.1f MB)", f, float64(info.Size())/(1024*1024))
			}
		}
	}
}

// pruneDockerResources 清理悬空 Docker 资源（停止的容器、悬空镜像）。
// 启动时执行一次，防止上次异常退出遗留的僵尸容器和镜像占用磁盘。
func pruneDockerResources() {
	// 清理已停止的 xbot 容器
	if out, err := dockerExec(dockerCmdTimeout, "container", "ls", "-a", "-q", "--filter", "name=xbot-", "--filter", "status=exited"); err == nil {
		containers := strings.TrimSpace(string(out))
		if containers != "" {
			for _, id := range strings.Split(containers, "\n") {
				id = strings.TrimSpace(id)
				if id != "" {
					dockerRun(dockerCmdTimeout, "rm", "-f", id)
				}
			}
			log.Infof("Pruned stopped xbot containers")
		}
	}
	// 清理悬空镜像（<none>:<none>），这些是异常退出时未被 rmi 的旧镜像
	if out, err := dockerExec(dockerCmdTimeout, "image", "prune", "-f"); err == nil {
		log.Debugf("Docker image prune: %s", strings.TrimSpace(string(out)))
	}
	// 二次清理：确保所有悬空镜像都被删除
	// docker image prune 可能因镜像被容器引用而遗漏，再执行一次 builder prune
	dockerRun(dockerCmdTimeout, "image", "prune", "-f", "--filter", "until=168h")
}

// exportImportIfDirty 仅在容器有文件系统变更时，用 export+import 持久化为单层镜像。
// 始终使用 export+import（而非 docker commit），确保镜像永远只有一层，避免磁盘空间膨胀。
// 调用方必须持有 s.mu 锁。
func (s *dockerSandbox) exportImportIfDirty(containerName, userID string) {
	if userID == "" || strings.HasPrefix(userID, "__") {
		log.Debugf("Skipping export for system container %s (userID=%q)", containerName, userID)
		return
	}

	diffOut, err := dockerExec(dockerCmdTimeout, "diff", containerName)
	if err != nil {
		log.WithError(err).Warnf("Failed to check diff for container %s, skipping export", containerName)
		return
	}
	if len(strings.TrimSpace(string(diffOut))) == 0 {
		log.Infof("Container %s has no changes, skipping export", containerName)
		return
	}

	userImage := userImageName(userID)

	// 1. 获取当前镜像的元数据（docker export/import 会丢失 CMD/ENTRYPOINT/ENV 等）
	//    优先从已有用户镜像读取，不存在则从基础镜像读取
	sourceImage := userImage
	if err := dockerRun(dockerCmdTimeout, "image", "inspect", sourceImage); err != nil {
		sourceImage = s.image
	}
	inspectFmt := "{{json .Config.Cmd}}||{{json .Config.Entrypoint}}||{{.Config.WorkingDir}}||{{json .Config.Env}}"
	inspectOut, _ := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", inspectFmt, sourceImage)
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

	// 2. 记录旧镜像 ID（用于后续清理）
	var oldImageID string
	if out, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Id}}", userImage); err == nil {
		oldImageID = strings.TrimSpace(string(out))
	}

	// 3. 管道化 export → import：docker export stdout 直接流入 docker import stdin，
	//    避免写入大临时文件（典型 2GB FS 省掉一次完整磁盘写入）。
	//    降级到临时文件方式（DinD 某些场景管道可能失败）。
	importArgs := []string{"import"}
	for _, c := range changes {
		importArgs = append(importArgs, "--change", c)
	}
	importArgs = append(importArgs, "-", userImage) // "-" 表示从 stdin 读取

	ctx, cancel := context.WithCancel(context.Background())
	out, err := dockerPipelineExportImport(ctx, containerName, importArgs)
	cancel()
	if err != nil {
		log.WithError(err).Warnf("Pipeline export failed for container %s, falling back to temp file: %s",
			containerName, strings.TrimSpace(string(out)))
		s.exportImportFallback(containerName, userImage, changes)
		return
	}
	log.WithField("changes", len(changes)).Infof("Pipeline exported container %s to single-layer image %s", containerName, userImage)

	// 5. 删除旧镜像（如果 ID 不同，说明 import 生成了新镜像）
	if oldImageID != "" {
		if newOut, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Id}}", userImage); err == nil {
			newImageID := strings.TrimSpace(string(newOut))
			if newImageID != oldImageID {
				if err := dockerRun(dockerCmdTimeout, "rmi", oldImageID); err != nil {
					log.WithError(err).Debugf("Failed to remove old image %s (may still be referenced)", oldImageID[:12])
				} else {
					log.Infof("Removed old image %s", oldImageID[:12])
				}
			}
		}
	}

	// 6. 不做全局 image prune，避免误删用户安装的开发环境镜像
	// 旧镜像已在第 5 步通过 rmi oldImageID 精确清理
}

// exportImportFallback 降级方案：export 到临时文件再 import（兼容 DinD 等管道不工作的场景）。
// 调用方必须持有 s.mu 锁。
func (s *dockerSandbox) exportImportFallback(containerName, userImage string, changes []string) {
	tmpFile, err := os.CreateTemp("", "xbot-export-*.tar")
	if err != nil {
		log.WithError(err).Warnf("Failed to create temp file for export fallback")
		return
	}
	tmpTar := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpTar)

	if out, err := dockerExec(0, "export", "-o", tmpTar, containerName); err != nil {
		log.WithError(err).Warnf("Failed to export container %s: %s", containerName, strings.TrimSpace(string(out)))
		return
	}

	// 记录旧镜像 ID
	var oldImageID string
	if out, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Id}}", userImage); err == nil {
		oldImageID = strings.TrimSpace(string(out))
	}

	importArgs := []string{"import"}
	for _, c := range changes {
		importArgs = append(importArgs, "--change", c)
	}
	importArgs = append(importArgs, tmpTar, userImage)
	if out, err := dockerExec(0, importArgs...); err != nil {
		log.WithError(err).Warnf("Failed to import image %s: %s", userImage, strings.TrimSpace(string(out)))
		return
	}
	log.WithField("changes", len(changes)).Infof("Fallback exported container %s to single-layer image %s", containerName, userImage)

	// 删除旧镜像
	if oldImageID != "" {
		if newOut, err := dockerExec(dockerCmdTimeout, "image", "inspect", "-f", "{{.Id}}", userImage); err == nil {
			newImageID := strings.TrimSpace(string(newOut))
			if newImageID != oldImageID {
				if err := dockerRun(dockerCmdTimeout, "rmi", oldImageID); err != nil {
					log.WithError(err).Debugf("Failed to remove old image %s (may still be referenced)", oldImageID[:12])
				} else {
					log.Infof("Removed old image %s", oldImageID[:12])
				}
			}
		}
	}
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

// validUserIDRe validates userID format for Docker container/image naming.
// Only allows lowercase alphanumeric, underscores, hyphens, and dots —
// the safe subset of Docker's [a-zA-Z0-9][a-zA-Z0-9_.-]+ naming rules.
var validUserIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

// validateUserID checks that userID contains only characters safe for Docker
// container and image names. Returns an error if the userID is invalid.
func validateUserID(userID string) error {
	if userID == "" {
		return fmt.Errorf("userID must not be empty")
	}
	if !validUserIDRe.MatchString(userID) {
		return fmt.Errorf("invalid userID %q: must match ^[a-z0-9][a-z0-9_.-]{0,127}$ (Docker-safe characters only)", userID)
	}
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
// 优先使用用户专属镜像（由 export+import 生成），不存在则用基础镜像
// 返回容器名称和检测到的用户默认 shell
func (s *dockerSandbox) getOrCreateContainer(userID, workspace string) (containerName, shell string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.containers == nil {
		s.containers = make(map[string]*dockerContainer)
	}

	// Validate userID to prevent command injection via Docker container/image names
	if err := validateUserID(userID); err != nil {
		return "", "", err
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
		s.saveAndRemove(containerName, userID)
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
		s.saveAndRemove(containerName, userID)
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

// saveAndRemove exports a container (preserving installed packages etc.) then stops and removes it.
func (s *dockerSandbox) saveAndRemove(containerName, userID string) {
	s.exportImportIfDirty(containerName, userID)

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
func NewSandbox(sandboxCfg config.SandboxConfig, workDir string) Sandbox {
	switch sandboxCfg.Mode {
	case "none":
		return &NoneSandbox{}
	case "docker":
		s := &dockerSandbox{
			image: sandboxCfg.DockerImage,
		}
		s.detectDinD(sandboxCfg, workDir)
		return s
	default:
		return &dockerSandbox{image: sandboxCfg.DockerImage}
	}
}

// detectDinD auto-detects Docker-in-Docker and sets up host path mapping.
// When xbot runs inside a container, bind mount paths must be translated from
// the container-internal path (e.g., /app/.xbot/...) to the real host path
// (e.g., /home/user/.xbot/...) because the Docker daemon runs on the host.
//
// The mount can be at workDir itself (/home/octopus → /app) or at a sub-path
// (/home/octopus/.xbot → /app/.xbot). Both cases are handled.
func (s *dockerSandbox) detectDinD(sandboxCfg config.SandboxConfig, workDir string) {
	absWorkDir, _ := filepath.Abs(workDir)

	// Priority 1: explicit override via HOST_WORK_DIR
	if sandboxCfg.HostWorkDir != "" {
		s.containerWorkDir = absWorkDir
		s.hostWorkDir = sandboxCfg.HostWorkDir
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
