package tools

import (
	"bytes"
	"context"
	"encoding/base64"
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

// DockerSandbox Docker 沙箱实现
// 容器生命周期：Close 时仅 stop（不 rm），下次直接 start 复用。
// export+import 仅在用户主动触发 cleanup 时执行（由 settings 中的 sandbox_cleanup 控制）。
// 始终使用 export+import（而非 docker commit），避免镜像层累积迅速耗尽磁盘空间。
type DockerSandbox struct {
	image            string // 基础镜像
	hostWorkDir      string // DinD: 宿主机上对应 WORK_DIR 的路径（空则不翻译）
	containerWorkDir string // DinD: 容器内 WORK_DIR 的路径（空则不翻译）
	mu               sync.Mutex
	containers       map[string]*dockerContainer // userID -> container
	exportingUsers   map[string]bool             // userID -> 正在 export+import 中
}

type dockerContainer struct {
	name    string
	started bool
	shell   string // 用户默认 shell（从容器内 /etc/passwd 获取）
}

func (s *DockerSandbox) Name() string { return "docker" }

// Close 关闭所有 Docker 容器（仅 stop，不 rm 也不 export/import）。
// 容器保留在磁盘上，下次 getOrCreateContainer 时直接 docker start 复用。
func (s *DockerSandbox) Close() error {
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
// 容器保留在磁盘上，下次直接 docker start 复用。
func (s *DockerSandbox) CloseForUser(userID string) error {
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

// IsExporting 检查该用户是否正在进行 export+import（用于引擎层排队阻塞）
func (s *DockerSandbox) IsExporting(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exportingUsers == nil {
		return false
	}
	return s.exportingUsers[userID]
}

// ExportAndImport 同步执行 export+import 持久化（由 settings 中的 cleanup 触发）。
// 调用期间 IsExporting(userID) 返回 true，引擎层会阻塞该用户的后续请求。
func (s *DockerSandbox) ExportAndImport(userID string) error {
	s.mu.Lock()
	if s.exportingUsers == nil {
		s.exportingUsers = make(map[string]bool)
	}
	if s.exportingUsers[userID] {
		s.mu.Unlock()
		return fmt.Errorf("export already in progress for user %s", userID)
	}
	s.exportingUsers[userID] = true
	c, ok := s.containers[userID]
	if !ok || !c.started {
		s.exportingUsers[userID] = false
		s.mu.Unlock()
		return fmt.Errorf("no active container for user %s", userID)
	}
	containerName := c.name
	s.mu.Unlock()

	log.Infof("Starting export+import for user %s (container %s)", userID, containerName)

	// 调用 exportImportIfDirty（不持有锁，因为内部不获取锁）
	s.exportImportIfDirty(containerName, userID)

	s.mu.Lock()
	s.exportingUsers[userID] = false
	s.mu.Unlock()

	log.Infof("Export+import completed for user %s", userID)
	return nil
}

// exportImportIfDirty 仅在容器有文件系统变更时，用 export+import 持久化为单层镜像。
// 始终使用 export+import（而非 docker commit），确保镜像永远只有一层，避免磁盘空间膨胀。
// 注意：此方法不获取 s.mu 锁，调用方需确保不在持锁状态下调用。
func (s *DockerSandbox) exportImportIfDirty(containerName, userID string) {
	if userID == "" || strings.HasPrefix(userID, "__") {
		log.Debugf("Skipping export for system container %s (userID=%q)", containerName, userID)
		return
	}

	// 验证容器仍然存在且在运行（防止锁释放后被 CloseForUser 关闭导致操作无效容器）
	if err := dockerRun(dockerCmdTimeout, "container", "inspect", containerName); err != nil {
		log.WithError(err).Warnf("Container %s no longer exists, skipping export", containerName)
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
// 不获取 s.mu 锁。
func (s *DockerSandbox) exportImportFallback(containerName, userImage string, changes []string) {
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

func (s *DockerSandbox) Wrap(command string, args []string, env []string, workspace string, userID string) (string, []string, error) {
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

// dockerExecInContainer runs a command inside a Docker container, returning combined output.
func (s *DockerSandbox) dockerExecInContainer(ctx context.Context, userID, workspace string, timeout time.Duration, args ...string) ([]byte, error) {
	containerName, _, err := s.getOrCreateContainer(userID, workspace)
	if err != nil {
		return nil, err
	}

	dockerArgs := []string{"exec", "-i"}
	dockerArgs = append(dockerArgs, containerName)
	dockerArgs = append(dockerArgs, args...)

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	if err != nil {
		return stderr.Bytes(), err
	}
	return stdout.Bytes(), nil
}

func (s *DockerSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	containerName, _, err := s.getOrCreateContainer(spec.UserID, spec.Workspace)
	if err != nil {
		return nil, err
	}

	dockerArgs := []string{"exec", "-i"}
	if spec.Dir != "" {
		dockerArgs = append(dockerArgs, "-w", spec.Dir)
	}
	for _, e := range spec.Env {
		dockerArgs = append(dockerArgs, "-e", e)
	}
	dockerArgs = append(dockerArgs, containerName)

	if spec.Shell {
		dockerArgs = append(dockerArgs, "sh", "-c", spec.Command)
	} else {
		dockerArgs = append(dockerArgs, spec.Command)
		dockerArgs = append(dockerArgs, spec.Args...)
	}

	cmdCtx := ctx
	var cancel context.CancelFunc
	if spec.Timeout > 0 {
		cmdCtx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(cmdCtx, "docker", dockerArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if spec.Stdin != "" {
		cmd.Stdin = bytes.NewBufferString(spec.Stdin)
	}

	err = cmd.Run()
	result := &ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else if cmdCtx.Err() == context.DeadlineExceeded {
			result.ExitCode = -1
			result.TimedOut = true
		} else {
			return nil, err
		}
	}
	return result, nil
}

func (s *DockerSandbox) ReadFile(ctx context.Context, path string, userID string) ([]byte, error) {
	// Pre-check file size to avoid base64-encoding large files unnecessarily.
	// DockerSandbox.Stat is a lightweight "stat" call (no data transfer).
	if info, err := s.Stat(ctx, path, userID); err == nil {
		if info.Size > MaxSandboxFileSize {
			return nil, fmt.Errorf("file exceeds maximum size of %d bytes (actual: %d)", MaxSandboxFileSize, info.Size)
		}
	}
	// Pass path as a separate argument to base64 (not via shell), avoiding shell injection.
	// Use dockerSlowTimeout for large files that may take longer to transfer.
	out, err := s.dockerExecInContainer(ctx, userID, "", dockerSlowTimeout,
		"base64", path)
	if err != nil {
		if strings.Contains(string(out), "No such file") || strings.Contains(string(out), "cannot open") {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("docker exec base64: %w: %s", err, string(out))
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	if int64(len(decoded)) > MaxSandboxFileSize {
		return nil, fmt.Errorf("file exceeds maximum size of %d bytes", MaxSandboxFileSize)
	}
	return decoded, nil
}

// dockerExecWithStdin runs a command inside a container and writes stdinData to the process's stdin.
// This avoids shell injection when piping data into commands like "base64 -d > path".
func (s *DockerSandbox) dockerExecWithStdin(ctx context.Context, userID, workspace string, timeout time.Duration, stdinData []byte, args ...string) ([]byte, error) {
	containerName, _, err := s.getOrCreateContainer(userID, workspace)
	if err != nil {
		return nil, err
	}

	dockerArgs := []string{"exec", "-i"}
	dockerArgs = append(dockerArgs, containerName)
	dockerArgs = append(dockerArgs, args...)

	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = bytes.NewReader(stdinData)

	err = cmd.Run()
	if err != nil {
		return stderr.Bytes(), err
	}
	return stdout.Bytes(), nil
}

func (s *DockerSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	if int64(len(data)) > MaxSandboxFileSize {
		return fmt.Errorf("data exceeds maximum size of %d bytes", MaxSandboxFileSize)
	}
	dir := filepath.Dir(path)
	if _, err := s.dockerExecInContainer(ctx, userID, "", dockerCmdTimeout, "mkdir", "-p", dir); err != nil {
		return fmt.Errorf("docker exec mkdir -p: %w", err)
	}
	// Write raw data via stdin to "cat > path" (redirect to file, stdout discarded by docker exec).
	// path is shell-escaped to prevent injection, shell used only for the redirect operator.
	if _, err := s.dockerExecWithStdin(ctx, userID, "", dockerSlowTimeout, data,
		"sh", "-c", fmt.Sprintf("cat > '%s'", shellEscape(path))); err != nil {
		return fmt.Errorf("docker exec write: %w", err)
	}
	if _, err := s.dockerExecInContainer(ctx, userID, "", dockerCmdTimeout, "chmod", fmt.Sprintf("%o", uint32(perm)), path); err != nil {
		return fmt.Errorf("docker exec chmod: %w", err)
	}
	return nil
}

func (s *DockerSandbox) Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error) {
	out, err := s.dockerExecInContainer(ctx, userID, "", dockerCmdTimeout,
		"stat", "--format", "%s|%a|%Y|%F", path)
	if err != nil {
		if strings.Contains(string(out), "No such file") || strings.Contains(string(out), "cannot stat") {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("docker exec stat: %w: %s", err, string(out))
	}

	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) != 4 {
		return nil, fmt.Errorf("unexpected stat output format: %q", string(out))
	}

	size, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse size: %w", err)
	}

	mode, err := strconv.ParseUint(parts[1], 8, 32)
	if err != nil {
		return nil, fmt.Errorf("parse mode: %w", err)
	}

	modTime, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse mtime: %w", err)
	}

	isDir := parts[3] == "directory"
	name := filepath.Base(path)
	return &SandboxFileInfo{
		Name:    name,
		Size:    size,
		Mode:    os.FileMode(mode),
		ModTime: time.Unix(modTime, 0),
		IsDir:   isDir,
	}, nil
}

func (s *DockerSandbox) ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error) {
	out, err := s.dockerExecInContainer(ctx, userID, "", dockerCmdTimeout,
		"ls", "-1p", path)
	if err != nil {
		if strings.Contains(string(out), "No such file") || strings.Contains(string(out), "cannot access") {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("docker exec ls: %w: %s", err, string(out))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var entries []DirEntry
	for _, line := range lines {
		if line == "" {
			continue
		}
		isDir := strings.HasSuffix(line, "/")
		name := strings.TrimSuffix(line, "/")
		if name == "" {
			continue
		}
		entries = append(entries, DirEntry{
			Name:  name,
			IsDir: isDir,
		})
	}
	return entries, nil
}

func (s *DockerSandbox) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	_, err := s.dockerExecInContainer(ctx, userID, "", dockerCmdTimeout, "mkdir", "-p", "-m", fmt.Sprintf("%o", uint32(perm)), path)
	if err != nil {
		return fmt.Errorf("docker exec mkdir -p: %w", err)
	}
	return nil
}

func (s *DockerSandbox) Remove(ctx context.Context, path string, userID string) error {
	_, err := s.dockerExecInContainer(ctx, userID, "", dockerCmdTimeout, "rm", path)
	if err != nil {
		return fmt.Errorf("docker exec rm: %w", err)
	}
	return nil
}

func (s *DockerSandbox) RemoveAll(ctx context.Context, path string, userID string) error {
	_, err := s.dockerExecInContainer(ctx, userID, "", dockerSlowTimeout, "rm", "-rf", path)
	if err != nil {
		return fmt.Errorf("docker exec rm -rf: %w", err)
	}
	return nil
}

// getOrCreateContainer 获取或创建用户的 Docker 容器
// 优先使用用户专属镜像（由 export+import 生成），不存在则用基础镜像
// 返回容器名称和检测到的用户默认 shell
func (s *DockerSandbox) getOrCreateContainer(userID, workspace string) (containerName, shell string, err error) {
	s.mu.Lock()

	if s.containers == nil {
		s.containers = make(map[string]*dockerContainer)
	}

	// Validate userID to prevent command injection via Docker container/image names
	if err := validateUserID(userID); err != nil {
		s.mu.Unlock()
		return "", "", err
	}

	if c, ok := s.containers[userID]; ok && c.started {
		containerName = c.name
		shell = c.shell
		s.mu.Unlock()
		return containerName, shell, nil
	}

	containerName = fmt.Sprintf("xbot-%s", userID)

	// Check if container is already running (under lock — only state checks).
	checkOutput, checkErr := dockerExec(dockerCmdTimeout, "inspect", "-f", "{{.State.Running}}", containerName)
	if checkErr == nil && strings.Contains(string(checkOutput), "true") {
		if s.verifyWorkspaceMount(containerName, workspace) {
			// detectShell does docker exec — release lock first to avoid blocking other users.
			s.mu.Unlock()
			shell = s.detectShell(containerName)
			s.mu.Lock()
			s.containers[userID] = &dockerContainer{name: containerName, started: true, shell: shell}
			s.mu.Unlock()
			return containerName, shell, nil
		}
		log.Warnf("Container %s has stale workspace mount, will recreate", containerName)
		s.saveAndRemove(containerName, userID)
	}

	// Container exists but not running, try to start it.
	if s.containerExists(containerName) {
		if startErr := dockerRun(dockerCmdTimeout, "start", containerName); startErr == nil {
			log.Infof("Started existing Docker container %s", containerName)
			// detectShell does docker exec — release lock first.
			s.mu.Unlock()
			shell = s.detectShell(containerName)
			s.mu.Lock()
			s.containers[userID] = &dockerContainer{name: containerName, started: true, shell: shell}
			s.mu.Unlock()
			return containerName, shell, nil
		}
		log.Warnf("Container %s failed to start, will recreate", containerName)
		s.saveAndRemove(containerName, userID)
	}

	// Container does not exist — choose image: prefer user-specific image, otherwise base.
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

	// Release lock before docker run (network I/O) and detectShell (network I/O).
	// Re-acquire only to update the containers map.
	s.mu.Unlock()

	output, err := dockerExec(dockerCmdTimeout, runArgs...)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w, output: %s", err, string(output))
	}

	// Detect user's default shell (docker exec — network I/O, must be outside lock).
	shell = s.detectShell(containerName)

	s.mu.Lock()
	s.containers[userID] = &dockerContainer{name: containerName, started: true, shell: shell}
	s.mu.Unlock()
	log.Infof("Docker container %s created successfully with shell %s", containerName, shell)

	return containerName, shell, nil
}

// GetShell 获取用户在沙箱中的默认 shell（如 /bin/bash）
// 如果容器不存在会自动创建
func (s *DockerSandbox) GetShell(userID string, workspace string) (string, error) {
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
func (s *DockerSandbox) detectShell(containerName string) string {
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
func (s *DockerSandbox) toHostPath(containerPath string) string {
	if s.hostWorkDir == "" || s.containerWorkDir == "" {
		return containerPath
	}
	if strings.HasPrefix(containerPath, s.containerWorkDir) {
		return s.hostWorkDir + containerPath[len(s.containerWorkDir):]
	}
	return containerPath
}

// verifyWorkspaceMount checks that the container's /workspace bind mount points to the expected host path.
func (s *DockerSandbox) verifyWorkspaceMount(containerName, expectedWorkspace string) bool {
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
func (s *DockerSandbox) containerExists(containerName string) bool {
	return dockerRun(dockerCmdTimeout, "inspect", "-f", "{{.Id}}", containerName) == nil
}

// saveAndRemove exports a container (preserving installed packages etc.) then stops and removes it.
func (s *DockerSandbox) saveAndRemove(containerName, userID string) {
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
func (s *DockerSandbox) migrateDinDWorkspaces() {
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

// NewDockerSandbox creates a new Docker sandbox instance.
func NewDockerSandbox(sandboxCfg config.SandboxConfig, workDir string) *DockerSandbox {
	s := &DockerSandbox{
		image: sandboxCfg.DockerImage,
	}
	s.detectDinD(sandboxCfg, workDir)
	return s
}

// NewSandbox 创建沙箱实例（backward compatible）
func NewSandbox(sandboxCfg config.SandboxConfig, workDir string) Sandbox {
	switch sandboxCfg.Mode {
	case "none":
		return &NoneSandbox{}
	case "docker":
		return NewDockerSandbox(sandboxCfg, workDir)
	case "remote":
		wsPort := sandboxCfg.WSPort
		if wsPort == 0 {
			wsPort = 8080
		}
		rs, err := NewRemoteSandbox(RemoteSandboxConfig{
			Addr:      fmt.Sprintf("0.0.0.0:%d", wsPort),
			AuthToken: sandboxCfg.AuthToken,
		})
		if err != nil {
			log.WithError(err).Errorf("Failed to start remote sandbox server: %v", err)
			return &NoneSandbox{}
		}
		return rs
	default:
		return NewDockerSandbox(sandboxCfg, workDir)
	}
}

// detectDinD auto-detects Docker-in-Docker and sets up host path mapping.
// When xbot runs inside a container, bind mount paths must be translated from
// the container-internal path (e.g., /app/.xbot/...) to the real host path
// (e.g., /home/user/.xbot/...) because the Docker daemon runs on the host.
//
// The mount can be at workDir itself (/home/octopus → /app) or at a sub-path
// (/home/octopus/.xbot → /app/.xbot). Both cases are handled.
func (s *DockerSandbox) detectDinD(sandboxCfg config.SandboxConfig, workDir string) {
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
func (s *DockerSandbox) autoDetectDinDMount(workDir string) (containerMount, hostMount string) {
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
