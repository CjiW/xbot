package runnerclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	dockerCmdTimeout  = 30 * time.Second
	dockerSlowTimeout = 120 * time.Second
	dockerPullTimeout = 5 * time.Minute

	// MaxSandboxFileSize 是文件读写的最大大小（500MB）。
	MaxSandboxFileSize int64 = 500 * 1024 * 1024
)

// DockerExecutor 通过 docker exec 在容器内执行操作。
type DockerExecutor struct {
	ContainerName string
	Image         string
	HostWorkspace string // 宿主机 workspace 路径（bind mount 源）
	CtrWorkspace  string // 容器内 workspace 路径（默认 /workspace）
}

// NewDockerExecutor 创建一个 DockerExecutor。
func NewDockerExecutor(userID, image, hostWorkspace string) (*DockerExecutor, error) {
	if err := CheckDockerAvailable(); err != nil {
		return nil, err
	}
	de := &DockerExecutor{
		ContainerName: fmt.Sprintf("xbot-runner-%s", userID),
		Image:         image,
		HostWorkspace: hostWorkspace,
		CtrWorkspace:  "/workspace",
	}
	if err := de.validateUserID(userID); err != nil {
		return nil, err
	}
	if err := de.getOrCreateContainer(); err != nil {
		return nil, fmt.Errorf("docker setup: %w", err)
	}
	return de, nil
}

func (de *DockerExecutor) Close() error {
	return de.dockerRun("stop", "-t", "1", de.ContainerName)
}

// --- 容器管理 ---

func (de *DockerExecutor) getOrCreateContainer() error {
	containerName := de.ContainerName

	// 1. 检查容器是否已在运行
	out, err := de.dockerExec("inspect", "-f", "{{.State.Running}}", containerName)
	if err == nil && strings.Contains(string(out), "true") {
		return nil
	}

	// 2. 容器存在但未运行 → start
	if de.containerExists() {
		return de.dockerRun("start", containerName)
	}

	// 3. 容器不存在 → create
	//    预检镜像，不存在则自动 pull
	if out, err := de.dockerExec("image", "inspect", de.Image); err != nil {
		ctx, cancel := context.WithTimeout(context.Background(), dockerPullTimeout)
		pullOut, pullErr := exec.CommandContext(ctx, "docker", "image", "pull", de.Image).CombinedOutput()
		cancel()
		if pullErr != nil {
			return fmt.Errorf("docker pull %q failed: %w, output: %s", de.Image, pullErr, strings.TrimSpace(string(pullOut)))
		}
		_ = out
	}

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", fmt.Sprintf("xbot-runner-%s", strings.TrimPrefix(containerName, "xbot-runner-")),
		"-v", fmt.Sprintf("%s:/workspace:rw", de.HostWorkspace),
		"-w", "/workspace",
		de.Image,
		"tail", "-f", "/dev/null",
	}
	out, err = de.dockerExec(args...)
	if err != nil {
		return fmt.Errorf("create container: %w, output: %s", err, string(out))
	}
	return nil
}

// --- Docker 辅助函数 ---

func (de *DockerExecutor) dockerExec(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerCmdTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

func (de *DockerExecutor) dockerExecSlow(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerSlowTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return out, nil
}

// dockerExecWithStdin 执行 docker 命令并通过 stdin 传入数据。
func (de *DockerExecutor) dockerExecWithStdin(timeout time.Duration, stdinData []byte, args ...string) ([]byte, error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = bytes.NewReader(stdinData)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return stderr.Bytes(), err
	}
	return stdout.Bytes(), nil
}

func (de *DockerExecutor) dockerRun(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), dockerCmdTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Run()
}

func (de *DockerExecutor) containerExists() bool {
	return de.dockerRun("inspect", "-f", "{{.Id}}", de.ContainerName) == nil
}

func (de *DockerExecutor) validateUserID(userID string) error {
	matched, _ := regexp.MatchString(`^[a-z0-9][a-z0-9_.-]{0,127}$`, userID)
	if !matched {
		return fmt.Errorf("invalid userID %q for Docker container naming", userID)
	}
	return nil
}

// CheckDockerAvailable 检查 Docker 是否可用。
func CheckDockerAvailable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker is not available: %w", err)
	}
	return nil
}

// shellEscape 对字符串进行单引号转义，防止 shell 注入。
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}

// --- Executor 接口实现 ---

func (de *DockerExecutor) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	dockerArgs := []string{"exec", "-i"}
	if spec.Dir != "" {
		dockerArgs = append(dockerArgs, "-w", spec.Dir)
	}
	for _, e := range spec.Env {
		dockerArgs = append(dockerArgs, "-e", e)
	}
	dockerArgs = append(dockerArgs, de.ContainerName)

	if spec.Shell {
		dockerArgs = append(dockerArgs, "sh", "-c", spec.Command)
	} else {
		if len(spec.Args) == 0 {
			return nil, fmt.Errorf("non-shell exec requires Args to be set explicitly")
		}
		dockerArgs = append(dockerArgs, spec.Args...)
	}

	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	err := cmd.Run()
	result := &ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			result.ExitCode = -1
			result.TimedOut = true
		} else {
			return nil, err
		}
	}
	return result, nil
}

func (de *DockerExecutor) ReadFile(path string) ([]byte, error) {
	// 预检文件大小
	if info, err := de.Stat(path); err == nil {
		if info.Size > MaxSandboxFileSize {
			return nil, fmt.Errorf("file exceeds maximum size of %d bytes (actual: %d)", MaxSandboxFileSize, info.Size)
		}
	}
	// path 作为独立参数传给 base64（不经过 shell），避免 shell 注入
	out, err := de.dockerExecSlow("exec", "-i", de.ContainerName, "base64", path)
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
	// 二次大小检查
	if int64(len(decoded)) > MaxSandboxFileSize {
		return nil, fmt.Errorf("file exceeds maximum size of %d bytes", MaxSandboxFileSize)
	}
	return decoded, nil
}

func (de *DockerExecutor) WriteFile(path string, data []byte, perm os.FileMode) error {
	if int64(len(data)) > MaxSandboxFileSize {
		return fmt.Errorf("data exceeds maximum size of %d bytes", MaxSandboxFileSize)
	}
	dir := filepath.Dir(path)
	if _, err := de.dockerExec("exec", "-i", de.ContainerName, "mkdir", "-p", dir); err != nil {
		return fmt.Errorf("docker exec mkdir -p: %w", err)
	}
	// 通过 dockerExecWithStdin 将 data 写入文件
	if _, err := de.dockerExecWithStdin(dockerSlowTimeout, data,
		"exec", "-i", de.ContainerName,
		"sh", "-c", fmt.Sprintf("cat > '%s'", shellEscape(path))); err != nil {
		return fmt.Errorf("docker exec write: %w", err)
	}
	if _, err := de.dockerExec("exec", "-i", de.ContainerName,
		"chmod", fmt.Sprintf("%o", uint32(perm)), path); err != nil {
		return fmt.Errorf("docker exec chmod: %w", err)
	}
	return nil
}

func (de *DockerExecutor) Stat(path string) (FileInfo, error) {
	out, err := de.dockerExec("exec", "-i", de.ContainerName, "stat", "--format", "%s|%a|%Y|%F", path)
	if err != nil {
		if strings.Contains(string(out), "No such file") || strings.Contains(string(out), "cannot stat") {
			return FileInfo{}, os.ErrNotExist
		}
		return FileInfo{}, fmt.Errorf("docker exec stat: %w: %s", err, string(out))
	}

	parts := strings.Split(strings.TrimSpace(string(out)), "|")
	if len(parts) != 4 {
		return FileInfo{}, fmt.Errorf("unexpected stat output format: %q", string(out))
	}

	size, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return FileInfo{}, fmt.Errorf("parse size: %w", err)
	}

	mode, err := strconv.ParseUint(parts[1], 8, 32)
	if err != nil {
		return FileInfo{}, fmt.Errorf("parse mode: %w", err)
	}

	modTime, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return FileInfo{}, fmt.Errorf("parse mtime: %w", err)
	}

	isDir := parts[3] == "directory"
	name := filepath.Base(path)
	return FileInfo{
		Name:    name,
		Size:    size,
		Mode:    os.FileMode(mode),
		ModTime: time.Unix(modTime, 0),
		IsDir:   isDir,
	}, nil
}

func (de *DockerExecutor) ReadDir(path string) ([]DirEntry, error) {
	out, err := de.dockerExec("exec", "-i", de.ContainerName, "ls", "-1p", path)
	if err != nil {
		if strings.Contains(string(out), "No such file") || strings.Contains(string(out), "cannot access") {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("docker exec ls: %w: %s", err, string(out))
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	entries := make([]DirEntry, 0, len(lines))
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
			// ls -1p 不包含文件大小，Size=0
		})
	}
	return entries, nil
}

func (de *DockerExecutor) MkdirAll(path string, perm os.FileMode) error {
	_, err := de.dockerExec("exec", "-i", de.ContainerName, "mkdir", "-p", "-m", fmt.Sprintf("%o", uint32(perm.Perm())), path)
	if err != nil {
		return fmt.Errorf("docker exec mkdir -p: %w", err)
	}
	return nil
}

func (de *DockerExecutor) Remove(path string) error {
	_, err := de.dockerExec("exec", "-i", de.ContainerName, "rm", path)
	if err != nil {
		return fmt.Errorf("docker exec rm: %w", err)
	}
	return nil
}

func (de *DockerExecutor) RemoveAll(path string) error {
	_, err := de.dockerExecSlow("exec", "-i", de.ContainerName, "rm", "-rf", path)
	if err != nil {
		return fmt.Errorf("docker exec rm -rf: %w", err)
	}
	return nil
}

func (de *DockerExecutor) DownloadFile(ctx context.Context, url, outputPath string) (int64, error) {
	dir := filepath.Dir(outputPath)
	if _, err := de.dockerExec("exec", "-i", de.ContainerName, "mkdir", "-p", dir); err != nil {
		return 0, fmt.Errorf("docker exec mkdir -p: %w", err)
	}

	// 优先使用 curl，回退 wget
	_, err := de.dockerExecSlow("exec", "-i", de.ContainerName, "curl", "-fsSL", "-o", outputPath, url)
	if err != nil {
		if _, wgetErr := de.dockerExecSlow("exec", "-i", de.ContainerName, "wget", "-q", "-O", outputPath, url); wgetErr != nil {
			return 0, fmt.Errorf("docker exec download (curl/wget): %w", err)
		}
	}

	statOut, err := de.dockerExec("exec", "-i", de.ContainerName, "stat", "--format", "%s", outputPath)
	if err != nil {
		return 0, fmt.Errorf("docker exec stat: %w", err)
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(statOut)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse file size: %w", err)
	}
	return size, nil
}
