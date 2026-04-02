package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	log "xbot/logger"
)

// maxBgOutputSize is the maximum output size per background task (50KB).
const maxBgOutputSize = 50 * 1024

// maxBgTaskLifetime is the safety upper bound for background task lifetime (24h).
const maxBgTaskLifetime = 24 * time.Hour

// BgTaskStatus represents the status of a background task.
type BgTaskStatus string

const (
	BgTaskRunning BgTaskStatus = "running"
	BgTaskDone    BgTaskStatus = "done"
	BgTaskError   BgTaskStatus = "error"
	BgTaskKilled  BgTaskStatus = "killed"
)

// BackgroundTask represents a running or completed background task.
type BackgroundTask struct {
	ID         string       `json:"id"`
	Command    string       `json:"command"`
	Status     BgTaskStatus `json:"status"`
	StartedAt  time.Time    `json:"started_at"`
	FinishedAt *time.Time   `json:"finished_at,omitempty"`
	Output     string       `json:"output"`
	ExitCode   int          `json:"exit_code"`
	Error      string       `json:"error,omitempty"`

	// Internal fields (not serialized to LLM)
	cancel  context.CancelFunc
	mu      sync.Mutex // protects Output for concurrent writes
	killed  bool       // set by Kill() before cancel()
	process *os.Process // live OS process (set by Adopt, nil for Start-based tasks)
}

// BackgroundTaskManager manages background task lifecycle.
// Thread-safe, can be shared across goroutines.
type BackgroundTaskManager struct {
	mu       sync.RWMutex
	tasks    map[string]*BackgroundTask // taskID → task
	sessions map[string][]string        // sessionKey → []taskID

	// NotifyCh is a buffered channel that receives completed background tasks.
	// The engine reads from this to inject results into the conversation.
	// Set by engine before starting the Run() loop.
	NotifyCh chan *BackgroundTask

	// OnComplete callbacks per session: sessionKey → []callback
	callbacks map[string][]func(task *BackgroundTask)
}

// NewBackgroundTaskManager creates a new task manager.
func NewBackgroundTaskManager() *BackgroundTaskManager {
	return &BackgroundTaskManager{
		tasks:     make(map[string]*BackgroundTask),
		sessions:  make(map[string][]string),
		NotifyCh:  make(chan *BackgroundTask, 16),
		callbacks: make(map[string][]func(task *BackgroundTask)),
	}
}

// generateTaskID generates a unique 8-char hex task ID.
func generateTaskID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Start launches a background task and returns immediately.
// The task runs in a goroutine; on completion, it's sent to NotifyCh.
func (m *BackgroundTaskManager) Start(
	sessionKey string,
	command string,
	execFn func(ctx context.Context, outputBuf func(string)) (exitCode int, execErr error),
) *BackgroundTask {
	id := generateTaskID()
	task := &BackgroundTask{
		ID:        id,
		Command:   command,
		Status:    BgTaskRunning,
		StartedAt: time.Now(),
		ExitCode:  -1,
	}

	// Safety timeout context (24h max lifetime)
	safetyCtx, safetyCancel := context.WithTimeout(context.Background(), maxBgTaskLifetime)

	// User-facing cancel context
	ctx, cancel := context.WithCancel(safetyCtx)
	task.cancel = func() {
		task.mu.Lock()
		task.killed = true
		task.mu.Unlock()
		cancel()
		safetyCancel()
	}

	m.mu.Lock()
	m.tasks[id] = task
	m.sessions[sessionKey] = append(m.sessions[sessionKey], id)
	m.mu.Unlock()

	go func() {
		defer cancel()
		defer safetyCancel()

		outputBuf := func(s string) {
			task.mu.Lock()
			defer task.mu.Unlock()
			task.Output += s
			// Keep only the tail (most recent output) when exceeding max size
			if len(task.Output) > maxBgOutputSize {
				task.Output = task.Output[len(task.Output)-maxBgOutputSize:]
			}
		}

		exitCode, execErr := execFn(ctx, outputBuf)

			now := time.Now()

			// Read killed flag ONCE and keep it — do NOT reset it.
			// Kill() sets killed=true then calls cancel(); resetting here
			// would race with status determination.
			task.mu.Lock()
			wasKilled := task.killed
			task.mu.Unlock()

			task.FinishedAt = &now
			task.ExitCode = exitCode

			if execErr != nil {
				if wasKilled || ctx.Err() != nil {
				task.Status = BgTaskKilled
				task.Error = "killed by user"
			} else {
				task.Status = BgTaskError
				task.Error = execErr.Error()
			}
		} else {
			task.Status = BgTaskDone
		}

		log.WithFields(log.Fields{
			"task_id":   id,
			"status":    task.Status,
			"exit_code": exitCode,
			"elapsed":   now.Sub(task.StartedAt).Round(time.Millisecond),
		}).Info("Background task completed")

		// Fire callbacks
		m.mu.RLock()
		cbs := m.callbacks[sessionKey]
		m.mu.RUnlock()
		for _, cb := range cbs {
			cb(task)
		}

		// Notify engine (non-blocking)
		select {
		case m.NotifyCh <- task:
		default:
			log.WithField("task_id", id).Warn("Background task notify channel full, dropping notification")
		}
	}()

	return task
}

// Adopt takes ownership of an already-running OS process (e.g., from a timed-out
// foreground command) and manages it as a background task. The process is NOT
// re-executed — Adopt monitors the existing process until it exits.
// partialOutput is any output collected before the timeout.
//
// Note: The caller must NOT call proc.Wait() — the original exec.Cmd goroutine
// already owns that responsibility. Adopt polls for process exit instead.
func (m *BackgroundTaskManager) Adopt(
	sessionKey string,
	command string,
	proc *os.Process,
	partialOutput string,
) *BackgroundTask {
	id := generateTaskID()
	task := &BackgroundTask{
		ID:        id,
		Command:   command,
		Status:    BgTaskRunning,
		StartedAt: time.Now(),
		ExitCode:  -1,
		Output:    partialOutput,
		process:   proc,
	}

	m.mu.Lock()
	m.tasks[id] = task
	m.sessions[sessionKey] = append(m.sessions[sessionKey], id)
	m.mu.Unlock()

	go func() {
		// Poll for process exit. We can't use proc.Wait() because the original
		// exec.Cmd goroutine already calls cmd.Wait() (which calls proc.Wait()).
		// Signal(0) returns nil if the process is still alive.
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		var exitCode int
		for range ticker.C {
			err := proc.Signal(syscall.Signal(0))
			if err != nil {
				// Process has exited. We can't retrieve the exact exit code
				// (the cmd.Wait goroutine owns that), so we use a heuristic.
				exitCode = 0 // assume success if we didn't kill it
				break
			}
		}

		task.mu.Lock()
		wasKilled := task.killed
		task.mu.Unlock()

		now := time.Now()
		task.FinishedAt = &now
		task.ExitCode = exitCode

		if wasKilled {
			task.Status = BgTaskKilled
			task.Error = "killed by user"
			task.ExitCode = -1
		} else {
			task.Status = BgTaskDone
		}

		log.WithFields(log.Fields{
			"task_id":   id,
			"status":    task.Status,
			"exit_code": task.ExitCode,
			"elapsed":   now.Sub(task.StartedAt).Round(time.Millisecond),
		}).Info("Adopted background task completed")

		// Fire callbacks
		m.mu.RLock()
		cbs := m.callbacks[sessionKey]
		m.mu.RUnlock()
		for _, cb := range cbs {
			cb(task)
		}

		// Notify engine (non-blocking)
		select {
		case m.NotifyCh <- task:
		default:
			log.WithField("task_id", id).Warn("Background task notify channel full, dropping notification")
		}
	}()

	return task
}
func (m *BackgroundTaskManager) Kill(taskID string) error {
	m.mu.RLock()
	task, ok := m.tasks[taskID]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("task %s not found", taskID)
	}

	if task.Status != BgTaskRunning {
		return fmt.Errorf("task %s is not running (status: %s)", taskID, task.Status)
	}

	// Kill the OS process group directly (covers Adopt tasks with no cancel func)
	task.mu.Lock()
	if task.process != nil {
		// Kill the entire process group (negative PID)
		syscall.Kill(-task.process.Pid, syscall.SIGKILL)
	}
	task.killed = true
	task.mu.Unlock()

	if task.cancel != nil {
		task.cancel()
	}
	return nil
}

// Status returns the current state of a task.
func (m *BackgroundTaskManager) Status(taskID string) (*BackgroundTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	task, ok := m.tasks[taskID]
	if !ok {
		return nil, fmt.Errorf("task %s not found", taskID)
	}
	return task, nil
}

// List returns all tasks for a session.
func (m *BackgroundTaskManager) List(sessionKey string) []*BackgroundTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := m.sessions[sessionKey]
	tasks := make([]*BackgroundTask, 0, len(ids))
	for _, id := range ids {
		if t, ok := m.tasks[id]; ok {
			tasks = append(tasks, t)
		}
	}
	return tasks
}

// ListRunning returns all currently running tasks for a session.
func (m *BackgroundTaskManager) ListRunning(sessionKey string) []*BackgroundTask {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := m.sessions[sessionKey]
	var tasks []*BackgroundTask
	for _, id := range ids {
		if t, ok := m.tasks[id]; ok && t.Status == BgTaskRunning {
			tasks = append(tasks, t)
		}
	}
	return tasks
}

// OnComplete registers a callback for task completion in a session.
// Only one callback per session is kept — subsequent calls replace the previous one.
func (m *BackgroundTaskManager) OnComplete(sessionKey string, callback func(task *BackgroundTask)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks[sessionKey] = []func(task *BackgroundTask){callback}
}

// CleanupSession removes all tasks and callbacks for a session.
func (m *BackgroundTaskManager) CleanupSession(sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if ids, ok := m.sessions[sessionKey]; ok {
		for _, id := range ids {
			if task, ok := m.tasks[id]; ok {
				if task.cancel != nil && task.Status == BgTaskRunning {
					task.cancel()
				}
				delete(m.tasks, id)
			}
		}
		delete(m.sessions, sessionKey)
	}
	delete(m.callbacks, sessionKey)
}
