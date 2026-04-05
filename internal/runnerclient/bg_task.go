package runnerclient

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"xbot/internal/runnerproto"
)

// bgTask 表示一个后台任务。
type bgTask struct {
	id        string
	command   string
	req       runnerproto.BgExecRequest
	cmd       *exec.Cmd
	mu        sync.Mutex
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	exitCode  int
	status    string // "running", "completed", "failed", "killed"
	startedAt time.Time
}

// bgTaskManager 管理后台任务。
type bgTaskManager struct {
	mu      sync.RWMutex
	tasks   map[string]*bgTask
	verbose bool

	// 用于构建 docker 命令
	dockerMode bool
	workspace  string
	executor   Executor
	logf       LogFunc
}

func newBgTaskManager(verbose, dockerMode bool, workspace string, logf LogFunc) *bgTaskManager {
	return &bgTaskManager{
		tasks:      make(map[string]*bgTask),
		verbose:    verbose,
		dockerMode: dockerMode,
		workspace:  workspace,
		logf:       logf,
	}
}

// Start 启动一个后台命令（native 模式直接后台运行，docker 模式用 goroutine 包装）。
func (m *bgTaskManager) Start(req runnerproto.BgExecRequest) (*runnerproto.BgStartedResponse, error) {
	t := &bgTask{
		id:        req.TaskID,
		command:   req.Command,
		req:       req,
		status:    "running",
		startedAt: time.Now(),
	}

	m.mu.Lock()
	m.tasks[req.TaskID] = t
	m.mu.Unlock()

	go t.run(m)

	callLogf(m.logf, "  bg_exec started [id=%s]: %s", req.TaskID, req.Command)
	return &runnerproto.BgStartedResponse{TaskID: req.TaskID}, nil
}

// run 执行命令并在完成时更新任务状态。
func (t *bgTask) run(m *bgTaskManager) {
	var exitCode int
	var status string

	if m.dockerMode {
		exitCode, status = t.runDocker(m)
	} else {
		exitCode, status = t.runNative(m)
	}

	t.mu.Lock()
	t.exitCode = exitCode
	t.status = status
	t.mu.Unlock()

	callLogf(m.logf, "  bg_exec done [id=%s] status=%s exit=%d stdout=%dB stderr=%dB",
		t.id, t.status, t.exitCode, t.stdout.Len(), t.stderr.Len())
}

// runNative 以原生方式执行命令，支持进程组。
func (t *bgTask) runNative(m *bgTaskManager) (int, string) {
	var cmd *exec.Cmd
	if t.req.Shell {
		cmd = exec.Command("sh", "-c", t.req.Command)
	} else {
		if len(t.req.Args) == 0 {
			return -1, "failed"
		}
		cmd = exec.Command(t.req.Args[0], t.req.Args[1:]...)
	}

	// 创建进程组以便 kill 整个进程树
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	dir := t.req.Dir
	if dir == "" {
		dir = m.workspace
	}
	cmd.Dir = filepath.Clean(dir)

	if len(t.req.Env) > 0 {
		cmd.Env = append(getBaseEnv(), t.req.Env...)
	}
	if t.req.Stdin != "" {
		cmd.Stdin = strings.NewReader(t.req.Stdin)
	}

	cmd.Stdout = &t.stdout
	cmd.Stderr = &t.stderr
	t.cmd = cmd

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				return ws.ExitStatus(), "failed"
			}
		}
		return -1, "failed"
	}
	return 0, "completed"
}

// runDocker 在 docker 容器内同步执行命令。
func (t *bgTask) runDocker(m *bgTaskManager) (int, string) {
	de := m.executor.(*DockerExecutor)

	if t.req.Shell {
		args := []string{"exec", "-i", de.ContainerName, "sh", "-c", t.req.Command}
		return t.dockerRun(de, args, t.req.Stdin)
	}

	if len(t.req.Args) == 0 {
		return -1, "failed"
	}
	args := append([]string{"exec", "-i", de.ContainerName}, t.req.Args...)
	return t.dockerRun(de, args, t.req.Stdin)
}

// dockerRun 执行 docker 命令并捕获输出。
func (t *bgTask) dockerRun(de *DockerExecutor, args []string, stdin string) (int, string) {
	cmd := exec.Command("docker", args...)
	cmd.Dir = de.HostWorkspace
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	cmd.Stdout = &t.stdout
	cmd.Stderr = &t.stderr
	t.cmd = cmd

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				return ws.ExitStatus(), "failed"
			}
		}
		return -1, "failed"
	}
	return 0, "completed"
}

// Kill 发送 SIGKILL 给后台任务的进程组（native）或 docker exec 进程（docker）。
func (m *bgTaskManager) Kill(req runnerproto.BgKillRequest) error {
	m.mu.RLock()
	t, ok := m.tasks[req.TaskID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("task %s not found", req.TaskID)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.status != "running" {
		return fmt.Errorf("task %s is not running (status=%s)", req.TaskID, t.status)
	}

	if t.cmd != nil && t.cmd.Process != nil {
		if m.dockerMode {
			t.cmd.Process.Kill()
		} else {
			// Kill 整个进程组
			syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		}
		t.status = "killed"
		callLogf(m.logf, "  bg_kill [id=%s]: killed", req.TaskID)
	}

	return nil
}

// Status 返回后台任务的当前状态和输出快照。
func (m *bgTaskManager) Status(req runnerproto.BgStatusRequest) (*runnerproto.BgOutputResponse, error) {
	m.mu.RLock()
	t, ok := m.tasks[req.TaskID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("task %s not found", req.TaskID)
	}

	t.mu.Lock()
	resp := &runnerproto.BgOutputResponse{
		TaskID:   t.id,
		Status:   t.status,
		ExitCode: t.exitCode,
		Stdout:   t.stdout.String(),
		Stderr:   t.stderr.String(),
	}
	t.mu.Unlock()

	return resp, nil
}

// Cleanup 杀死所有运行中的后台任务（断开连接时调用）。
func (m *bgTaskManager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, t := range m.tasks {
		t.mu.Lock()
		if t.status == "running" && t.cmd != nil && t.cmd.Process != nil {
			if m.dockerMode {
				t.cmd.Process.Kill()
			} else {
				syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
			}
			t.status = "killed"
		}
		t.mu.Unlock()
		delete(m.tasks, id)
	}
	callLogf(m.logf, "  bg_tasks: cleaned up all tasks on disconnect")
}

// getBaseEnv 返回原生命令执行的基础环境。
func getBaseEnv() []string {
	return nil // exec.Command uses os.Environ by default when Env is nil
}
