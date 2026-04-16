package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// BgNotification is the interface for background notifications that flow through
// BgTaskManager.NotifyCh → bgNotifyLoop → bgRunPending → DrainBgNotifications.
// Both BackgroundTask (shell tasks) and SubAgentBgNotify (bg subagents) implement this.
type BgNotification interface {
	// SessionKey returns the session key (channel:chatID) for routing.
	SessionKey() string
}

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
	sessionKey string // session key for routing completion notifications
	cancel     context.CancelFunc
	mu         sync.Mutex  // protects Output for concurrent writes
	killed     bool        // set by Kill() before cancel()
	process    *os.Process // live OS process (set by Adopt, nil for Start-based tasks)
	exitCodeCh chan int    // optional: receives real exit code from cmd.Wait goroutine (Adopt only)
}

// BackgroundTaskManager manages background task lifecycle.
// Thread-safe, can be shared across goroutines.
type BackgroundTaskManager struct {
	mu       sync.RWMutex
	tasks    map[string]*BackgroundTask // taskID → task
	sessions map[string][]string        // sessionKey → []taskID

	// NotifyCh is a buffered channel that receives background notifications
	// (both BackgroundTask and SubAgentBgNotify).
	// The engine reads from this to inject results into the conversation.
	// Set by engine before starting the Run() loop.
	NotifyCh chan BgNotification

	// OnComplete callbacks per session: sessionKey → []callback
	callbacks map[string][]func(task *BackgroundTask)
}

// NewBackgroundTaskManager creates a new task manager.
func NewBackgroundTaskManager() *BackgroundTaskManager {
	return &BackgroundTaskManager{
		tasks:     make(map[string]*BackgroundTask),
		sessions:  make(map[string][]string),
		NotifyCh:  make(chan BgNotification, 16),
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
		ID:         id,
		Command:    command,
		Status:     BgTaskRunning,
		StartedAt:  time.Now(),
		ExitCode:   -1,
		sessionKey: sessionKey,
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

		task.mu.Lock()
		wasKilled := task.killed
		now := time.Now()
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
		} else if wasKilled {
			task.Status = BgTaskKilled
			task.Error = "killed by user"
		} else {
			task.Status = BgTaskDone
		}
		task.mu.Unlock()

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
// exitCodeCh optionally receives the real exit code from the original cmd.Wait()
// goroutine. If provided, Adopt uses it instead of the Signal(0) heuristic.
//
// ongoingOutput optionally reads the final full output from capture goroutines.
// Called after the process exits and all capture goroutines have completed.
func (m *BackgroundTaskManager) Adopt(
	sessionKey string,
	command string,
	proc *os.Process,
	partialOutput string,
	exitCodeCh chan int,
	ongoingOutput func() string,
) *BackgroundTask {
	id := generateTaskID()
	task := &BackgroundTask{
		ID:         id,
		Command:    command,
		Status:     BgTaskRunning,
		StartedAt:  time.Now(),
		ExitCode:   -1,
		Output:     partialOutput,
		process:    proc,
		exitCodeCh: exitCodeCh,
	}

	m.mu.Lock()
	m.tasks[id] = task
	m.sessions[sessionKey] = append(m.sessions[sessionKey], id)
	m.mu.Unlock()

	go func() {
		var exitCode int

		if exitCodeCh != nil {
			// Prefer real exit code from cmd.Wait goroutine.
			// Wait for either the exit code channel or process death via Signal(0).
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()

			select {
			case code := <-exitCodeCh:
				exitCode = code
			case <-ticker.C:
				// Poll fallback: channel might never fire if cmd.Wait already returned
				for {
					if !isProcessAlive(proc.Pid) {
						// Process dead, try non-blocking read from channel
						select {
						case code := <-exitCodeCh:
							exitCode = code
						default:
							exitCode = 0 // heuristic fallback
						}
						break
					}
					<-ticker.C
				}
			}
		} else {
			// No channel provided — use isProcessAlive polling heuristic.
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()

			for range ticker.C {
				if !isProcessAlive(proc.Pid) {
					exitCode = 0
					break
				}
			}
		}

		task.mu.Lock()
		wasKilled := task.killed

		// Capture final output from capture goroutines if available.
		// Safe to call: exitCodeCh fires after cmd.Wait() + wg.Wait() complete,
		// so all capture goroutines have finished writing.
		if ongoingOutput != nil {
			task.Output = ongoingOutput()
		}

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
		task.mu.Unlock()

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

	task.mu.Lock()
	if task.Status != BgTaskRunning {
		task.mu.Unlock()
		return fmt.Errorf("task %s is not running (status: %s)", taskID, task.Status)
	}
	if task.process != nil {
		killProcessTree(task.process)
	}
	task.killed = true
	task.Status = BgTaskKilled
	task.mu.Unlock()

	if task.cancel != nil {
		task.cancel()
	}
	return nil
}

// SessionKey returns the session key this task belongs to.
func (t *BackgroundTask) SessionKey() string { return t.sessionKey }

// IsKilled returns true if the task was killed by the user.
func (t *BackgroundTask) IsKilled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.killed
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
		if t, ok := m.tasks[id]; ok {
			t.mu.Lock()
			status := t.Status
			t.mu.Unlock()
			if status == BgTaskRunning {
				tasks = append(tasks, t)
			}
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
				if task.cancel != nil {
					task.mu.Lock()
					running := task.Status == BgTaskRunning
					task.mu.Unlock()
					if running {
						task.cancel()
					}
				}
				delete(m.tasks, id)
			}
		}
		delete(m.sessions, sessionKey)
	}
	delete(m.callbacks, sessionKey)
}

// SubAgentBgNotifyType distinguishes bg subagent notification types.
type SubAgentBgNotifyType string

const (
	// SubAgentBgNotifyProgress is sent when a bg subagent completes an iteration.
	// The parent agent sees it as an update within the current Run loop.
	SubAgentBgNotifyProgress SubAgentBgNotifyType = "progress"

	// SubAgentBgNotifyCompleted is sent when a bg subagent's Run() finishes.
	// The parent agent sees it as a completion notification.
	SubAgentBgNotifyCompleted SubAgentBgNotifyType = "completed"
)

// SubAgentBgNotify is a background notification for interactive subagent events.
// Implements BgNotification so it flows through the same NotifyCh pipeline.
type SubAgentBgNotify struct {
	Key      string               // channel:chatID for routing
	Type     SubAgentBgNotifyType // "progress" or "completed"
	Role     string               // subagent role name
	Instance string               // subagent instance ID
	Content  string               // formatted notification content for the LLM
	Elapsed  time.Duration        // total elapsed time (for completed notifications)
}

// SessionKey implements BgNotification.
func (n *SubAgentBgNotify) SessionKey() string { return n.Key }

// SendSubAgentNotify sends a subagent notification through BgTaskManager.NotifyCh.
// Safe to call from any goroutine. Drops silently if channel is full or closed.
func (m *BackgroundTaskManager) SendSubAgentNotify(n *SubAgentBgNotify) {
	if m.NotifyCh == nil {
		return
	}
	defer func() {
		recover() // send on closed channel panics — safe to ignore during shutdown
	}()
	select {
	case m.NotifyCh <- n:
	default:
		log.WithFields(log.Fields{
			"role":     n.Role,
			"instance": n.Instance,
			"type":     n.Type,
		}).Warn("Bg subagent notify channel full, dropping notification")
	}
}

// FormatSubAgentBgNotify formats a bg subagent notification for injection into
// the parent agent's conversation as a synthetic tool result.
func FormatSubAgentBgNotify(n *SubAgentBgNotify) string {
	var sb strings.Builder
	switch n.Type {
	case SubAgentBgNotifyProgress:
		fmt.Fprintf(&sb, "[System Notification] Background subagent %s", n.Role)
		if n.Instance != "" {
			fmt.Fprintf(&sb, " (instance=%s)", n.Instance)
		}
		fmt.Fprintf(&sb, " progress update.\n%s", n.Content)
	case SubAgentBgNotifyCompleted:
		fmt.Fprintf(&sb, "[System Notification] Background subagent %s", n.Role)
		if n.Instance != "" {
			fmt.Fprintf(&sb, " (instance=%s)", n.Instance)
		}
		fmt.Fprintf(&sb, " completed.")
		if n.Elapsed > 0 {
			fmt.Fprintf(&sb, " Elapsed: %s", n.Elapsed.Round(time.Second))
		}
		fmt.Fprintf(&sb, "\n%s\n", n.Content)
		fmt.Fprintf(&sb, "If this subagent is no longer needed, use SubAgent(action=\"unload\", instance=%q) to release its resources.", n.Instance)
	}
	return sb.String()
}
