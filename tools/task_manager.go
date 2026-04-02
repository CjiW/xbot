package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
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
	cancel context.CancelFunc
	mu     sync.Mutex // protects Output for concurrent writes
	killed bool       // set by Kill() before cancel()
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
			// Truncate output if it exceeds max size
			if len(task.Output)+len(s) > maxBgOutputSize {
				overflow := len(task.Output) + len(s) - maxBgOutputSize
				if overflow > 0 && len(s) > overflow {
					s = s[overflow:]
				}
			}
			task.Output += s
		}

		exitCode, execErr := execFn(ctx, outputBuf)

		now := time.Now()
		task.mu.Lock()
		task.killed = false // wasn't killed externally if we get here
		task.mu.Unlock()

		task.FinishedAt = &now
		task.ExitCode = exitCode

		if execErr != nil {
			task.mu.Lock()
			wasKilled := task.killed
			task.mu.Unlock()
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

// Kill terminates a running background task.
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

	task.cancel()
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
func (m *BackgroundTaskManager) OnComplete(sessionKey string, callback func(task *BackgroundTask)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks[sessionKey] = append(m.callbacks[sessionKey], callback)
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
