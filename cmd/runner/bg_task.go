package main

import (
	"bytes"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// bgTask represents a background task running on the runner.
type bgTask struct {
	id        string
	command   string
	cmd       *exec.Cmd
	mu        sync.Mutex
	stdout    bytes.Buffer
	stderr    bytes.Buffer
	exitCode  int
	status    string // "running", "completed", "failed", "killed"
	startedAt time.Time
}

// bgTaskManager manages background tasks on the runner side.
type bgTaskManager struct {
	mu    sync.RWMutex
	tasks map[string]*bgTask
}

var bgMgr = &bgTaskManager{tasks: make(map[string]*bgTask)}

// startBgTask launches a command as a background task (native mode only).
// Docker mode falls back to synchronous execution wrapped in a goroutine.
func startBgTask(req BgExecRequest) (*BgStartedResponse, error) {
	t := &bgTask{
		id:        req.TaskID,
		command:   req.Command,
		status:    "running",
		startedAt: time.Now(),
	}

	bgMgr.mu.Lock()
	bgMgr.tasks[req.TaskID] = t
	bgMgr.mu.Unlock()

	go t.run(req)

	log.Printf("  bg_exec started [id=%s]: %s", req.TaskID, req.Command)
	return &BgStartedResponse{TaskID: req.TaskID}, nil
}

// run executes the command and updates the task status when done.
func (t *bgTask) run(req BgExecRequest) {
	var exitCode int
	var status string

	if dockerMode {
		exitCode, status = t.runDocker(req)
	} else {
		exitCode, status = t.runNative(req)
	}

	t.mu.Lock()
	t.exitCode = exitCode
	t.status = status
	t.mu.Unlock()

	log.Printf("  bg_exec done [id=%s] status=%s exit=%d stdout=%dB stderr=%dB",
		t.id, t.status, t.exitCode, t.stdout.Len(), t.stderr.Len())
}

// runNative executes a command natively with process group support.
func (t *bgTask) runNative(req BgExecRequest) (int, string) {
	var cmd *exec.Cmd
	if req.Shell {
		cmd = exec.Command("sh", "-c", req.Command)
	} else {
		if len(req.Args) == 0 {
			return -1, "failed"
		}
		cmd = exec.Command(req.Args[0], req.Args[1:]...)
	}

	// Create process group so we can kill the entire tree.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	dir := req.Dir
	if dir == "" {
		dir = execWorkspace
	}
	cmd.Dir = filepath.Clean(dir)

	if len(req.Env) > 0 {
		cmd.Env = append(getBaseEnv(), req.Env...)
	}
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
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

// runDocker executes a command inside the docker container synchronously.
// Process group is managed by docker itself (no host-level pgid).
func (t *bgTask) runDocker(req BgExecRequest) (int, string) {
	de := executor.(*dockerExecutor)

	if req.Shell {
		args := []string{"exec", "-i", de.containerName, "sh", "-c", req.Command}
		return t.dockerRun(de, args, req.Stdin)
	}

	if len(req.Args) == 0 {
		return -1, "failed"
	}
	args := append([]string{"exec", "-i", de.containerName}, req.Args...)
	return t.dockerRun(de, args, req.Stdin)
}

// dockerRun executes a docker command and captures output.
func (t *bgTask) dockerRun(de *dockerExecutor, args []string, stdin string) (int, string) {
	cmd := exec.Command("docker", args...)
	cmd.Dir = de.hostWorkspace
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

// killBgTask sends SIGKILL to a background task's process group (native)
// or kills the docker exec process (docker).
func killBgTask(req BgKillRequest) error {
	bgMgr.mu.RLock()
	t, ok := bgMgr.tasks[req.TaskID]
	bgMgr.mu.RUnlock()
	if !ok {
		return fmt.Errorf("task %s not found", req.TaskID)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.status != "running" {
		return fmt.Errorf("task %s is not running (status=%s)", req.TaskID, t.status)
	}

	if t.cmd != nil && t.cmd.Process != nil {
		if dockerMode {
			t.cmd.Process.Kill()
		} else {
			// Kill entire process group.
			syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
		}
		t.status = "killed"
		log.Printf("  bg_kill [id=%s]: killed", req.TaskID)
	}

	return nil
}

// statusBgTask returns the current status and output snapshot of a background task.
func statusBgTask(req BgStatusRequest) (*BgOutputResponse, error) {
	bgMgr.mu.RLock()
	t, ok := bgMgr.tasks[req.TaskID]
	bgMgr.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("task %s not found", req.TaskID)
	}

	t.mu.Lock()
	resp := &BgOutputResponse{
		TaskID:   t.id,
		Status:   t.status,
		ExitCode: t.exitCode,
		Stdout:   t.stdout.String(),
		Stderr:   t.stderr.String(),
	}
	t.mu.Unlock()

	return resp, nil
}

// cleanupBgTasks kills all running background tasks (called on disconnect).
func cleanupBgTasks() {
	bgMgr.mu.Lock()
	defer bgMgr.mu.Unlock()

	for id, t := range bgMgr.tasks {
		t.mu.Lock()
		if t.status == "running" && t.cmd != nil && t.cmd.Process != nil {
			if dockerMode {
				t.cmd.Process.Kill()
			} else {
				syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
			}
			t.status = "killed"
		}
		t.mu.Unlock()
		delete(bgMgr.tasks, id)
	}
	log.Printf("  bg_tasks: cleaned up all tasks on disconnect")
}

// getBaseEnv returns the base environment for native command execution.
func getBaseEnv() []string {
	return nil // exec.Command uses os.Environ by default when Env is nil
}
