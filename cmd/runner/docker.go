package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"log"
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

	// MaxSandboxFileSize is the maximum file size for read/write operations (500MB).
	MaxSandboxFileSize int64 = 500 * 1024 * 1024
)

// dockerExecutor 通过 docker exec 在容器内执行操作。
type dockerExecutor struct {
	containerName string
	image         string
	hostWorkspace string // 宿主机 workspace 路径（bind mount 源）
	ctrWorkspace  string // 容器内 workspace 路径（默认 /workspace）
}

func newDockerExecutor(userID, image, hostWorkspace string) (*dockerExecutor, error) {
	if err := checkDockerAvailable(); err != nil {
		return nil, err
	}
	de := &dockerExecutor{
		containerName: fmt.Sprintf("xbot-runner-%s", userID),
		image:         image,
		hostWorkspace: hostWorkspace,
		ctrWorkspace:  "/workspace",
	}
	if err := de.validateUserID(userID); err != nil {
		return nil, err
	}
	if err := de.getOrCreateContainer(); err != nil {
		return nil, fmt.Errorf("docker setup: %w", err)
	}
	return de, nil
}

func (de *dockerExecutor) Close() error {
	log.Printf("Stopping container %s", de.containerName)
	return de.dockerRun("stop", "-t", "1", de.containerName)
}

// --- 容器管理 ---

func (de *dockerExecutor) getOrCreateContainer() error {
	containerName := de.containerName

	// 1. 检查容器是否已在运行
	out, err := de.dockerExec("inspect", "-f", "{{.State.Running}}", containerName)
	if err == nil && strings.Contains(string(out), "true") {
		log.Printf("Docker container %s is already running", containerName)
		return nil
	}

	// 2. 容器存在但未运行 → start
	if de.containerExists() {
		log.Printf("Starting existing container %s", containerName)
		return de.dockerRun("start", containerName)
	}

	// 3. 容器不存在 → create
	//    优先预检镜像是否存在本地，不存在则直接报错（不尝试 pull，避免挂起）
	if out, err := de.dockerExec("image", "inspect", de.image); err != nil {
		return fmt.Errorf("docker image %q not found locally (please pull first): %s", de.image, strings.TrimSpace(string(out)))
	}

	args := []string{
		"run", "-d",
		"--name", containerName,
		"--hostname", fmt.Sprintf("xbot-runner-%s", strings.TrimPrefix(containerName, "xbot-runner-")),
		"-v", fmt.Sprintf("%s:/workspace:rw", de.hostWorkspace),
		"-w", "/workspace",
		de.image,
		"tail", "-f", "/dev/null",
	}
	log.Printf("Creating container %s (image=%s, mount=%s:/workspace)", containerName, de.image, de.hostWorkspace)
	out, err = de.dockerExec(args...)
	if err != nil {
		return fmt.Errorf("create container: %w, output: %s", err, string(out))
	}
	log.Printf("Container %s created", containerName)
	return nil
}

// --- Docker 辅助函数 ---

func (de *dockerExecutor) dockerExec(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerCmdTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

func (de *dockerExecutor) dockerExecSlow(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerSlowTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// dockerExecWithStdin 执行 docker 命令并通过 stdin 传入数据。
func (de *dockerExecutor) dockerExecWithStdin(timeout time.Duration, stdinData []byte, args ...string) ([]byte, error) {
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

func (de *dockerExecutor) dockerRun(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), dockerCmdTimeout)
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Run()
}

func (de *dockerExecutor) containerExists() bool {
	return de.dockerRun("inspect", "-f", "{{.Id}}", de.containerName) == nil
}

func (de *dockerExecutor) validateUserID(userID string) error {
	matched, _ := regexp.MatchString(`^[a-z0-9][a-z0-9_.-]{0,127}$`, userID)
	if !matched {
		return fmt.Errorf("invalid userID %q for Docker container naming", userID)
	}
	return nil
}

// checkDockerAvailable 检查 Docker 是否可用。
func checkDockerAvailable() error {
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

func (de *dockerExecutor) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	dockerArgs := []string{"exec", "-i"}
	if spec.Dir != "" {
		dockerArgs = append(dockerArgs, "-w", spec.Dir)
	}
	for _, e := range spec.Env {
		dockerArgs = append(dockerArgs, "-e", e)
	}
	dockerArgs = append(dockerArgs, de.containerName)

	if spec.Shell {
		dockerArgs = append(dockerArgs, "sh", "-c", spec.Command)
	} else {
		if len(spec.Args) == 0 {
			return nil, fmt.Errorf("non-shell exec requires Args to be set explicitly")
		}
		dockerArgs = append(dockerArgs, spec.Args...)
	}

	// 直接使用传入的 ctx（handleExec 已设置 deadline），不重复创建 WithTimeout
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

func (de *dockerExecutor) ReadFile(path string) ([]byte, error) {
	// 预检文件大小
	if info, err := de.Stat(path); err == nil {
		if info.Size > MaxSandboxFileSize {
			return nil, fmt.Errorf("file exceeds maximum size of %d bytes (actual: %d)", MaxSandboxFileSize, info.Size)
		}
	}
	// path 作为独立参数传给 base64（不经过 shell），避免 shell 注入
	out, err := de.dockerExecSlow("exec", "-i", de.containerName, "base64", path)
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

func (de *dockerExecutor) WriteFile(path string, data []byte, perm os.FileMode) error {
	if int64(len(data)) > MaxSandboxFileSize {
		return fmt.Errorf("data exceeds maximum size of %d bytes", MaxSandboxFileSize)
	}
	dir := filepath.Dir(path)
	if _, err := de.dockerExec("exec", "-i", de.containerName, "mkdir", "-p", dir); err != nil {
		return fmt.Errorf("docker exec mkdir -p: %w", err)
	}
	// 通过 dockerExecWithStdin 将 data 写入文件
	if _, err := de.dockerExecWithStdin(dockerSlowTimeout, data,
		"exec", "-i", de.containerName,
		"sh", "-c", fmt.Sprintf("cat > '%s'", shellEscape(path))); err != nil {
		return fmt.Errorf("docker exec write: %w", err)
	}
	if _, err := de.dockerExec("exec", "-i", de.containerName,
		"chmod", fmt.Sprintf("%o", uint32(perm)), path); err != nil {
		return fmt.Errorf("docker exec chmod: %w", err)
	}
	return nil
}

func (de *dockerExecutor) Stat(path string) (FileInfo, error) {
	out, err := de.dockerExec("exec", "-i", de.containerName, "stat", "--format", "%s|%a|%Y|%F", path)
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

func (de *dockerExecutor) ReadDir(path string) ([]DirEntry, error) {
	out, err := de.dockerExec("exec", "-i", de.containerName, "ls", "-1p", path)
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

func (de *dockerExecutor) MkdirAll(path string, perm os.FileMode) error {
	_, err := de.dockerExec("exec", "-i", de.containerName, "mkdir", "-p", "-m", fmt.Sprintf("%o", uint32(perm)), path)
	if err != nil {
		return fmt.Errorf("docker exec mkdir -p: %w", err)
	}
	return nil
}

func (de *dockerExecutor) Remove(path string) error {
	_, err := de.dockerExec("exec", "-i", de.containerName, "rm", path)
	if err != nil {
		return fmt.Errorf("docker exec rm: %w", err)
	}
	return nil
}

func (de *dockerExecutor) RemoveAll(path string) error {
	_, err := de.dockerExecSlow("exec", "-i", de.containerName, "rm", "-rf", path)
	if err != nil {
		return fmt.Errorf("docker exec rm -rf: %w", err)
	}
	return nil
}
