package hooks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// truncateForLog truncates s to at most maxRunes runes, appending "..." if
// truncated.
func truncateForLog(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		return string(r[:maxRunes])
	}
	return string(r[:maxRunes]) + "..."
}

// toolArgsToString serialises a tool input map to a JSON string for logging.
// Returns "" on marshalling error.
func toolArgsToString(input map[string]any) string {
	if input == nil {
		return ""
	}
	b, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// LoggingCallback
// ---------------------------------------------------------------------------

// LoggingCallback returns a CallbackHook that logs tool execution
// start/completion/failure. It always allows execution.
func LoggingCallback() *CallbackHook {
	return &CallbackHook{
		Name: "logging",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			switch e := event.(type) {
			case *PreToolUseEvent:
				preview := truncateForLog(toolArgsToString(e.ToolInput_), 200)
				log.Ctx(ctx).WithField("tool", e.ToolName_).Infof("Tool call: %s(%s)", e.ToolName_, preview)
			case *PostToolUseEvent:
				fields := log.Fields{"tool": e.ToolName_, "elapsed": time.Duration(e.ToolElapsedMs) * time.Millisecond}
				if e.ToolError != "" {
					log.Ctx(ctx).WithFields(fields).Warn("Tool execution failed")
				} else {
					log.Ctx(ctx).WithFields(fields).Infof("Tool done")
				}
			case *PostToolUseFailureEvent:
				log.Ctx(ctx).WithField("tool", e.ToolName_).Warnf("Tool failed: %s", e.ToolError)
			}
			return &Result{Decision: "allow"}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// TimingCallback
// ---------------------------------------------------------------------------

// timingStat holds per-tool timing counters. Fields are updated atomically.
type timingStat struct {
	Count int64
	Total int64 // nanoseconds
	Min   int64
	Max   int64
}

// TimingSnapshot is a point-in-time copy of timing statistics for one tool.
type TimingSnapshot struct {
	Count   int64
	Total   time.Duration
	Average time.Duration
	Min     time.Duration
	Max     time.Duration
}

// TimingData collects per-tool execution timing. Expose Stats()/Reset() to
// callers (e.g. CLI) that need to query or clear statistics.
type TimingData struct {
	mu    sync.RWMutex
	stats map[string]*timingStat
}

// NewTimingData creates an empty TimingData collector.
func NewTimingData() *TimingData {
	return &TimingData{stats: make(map[string]*timingStat)}
}

// Stats returns a snapshot of all collected timing statistics.
func (td *TimingData) Stats() map[string]TimingSnapshot {
	td.mu.RLock()
	defer td.mu.RUnlock()

	result := make(map[string]TimingSnapshot, len(td.stats))
	for name, s := range td.stats {
		count := atomic.LoadInt64(&s.Count)
		total := atomic.LoadInt64(&s.Total)
		min := atomic.LoadInt64(&s.Min)
		max := atomic.LoadInt64(&s.Max)

		snap := TimingSnapshot{
			Count: count,
			Total: time.Duration(total),
			Min:   time.Duration(min),
			Max:   time.Duration(max),
		}
		if count > 0 {
			snap.Average = time.Duration(total / count)
		}
		result[name] = snap
	}
	return result
}

// Reset clears all timing statistics.
func (td *TimingData) Reset() {
	td.mu.Lock()
	defer td.mu.Unlock()
	td.stats = make(map[string]*timingStat)
}

// TimingCallback returns a CallbackHook that records per-tool elapsed time
// into the supplied TimingData. It always allows execution.
func TimingCallback(td *TimingData) *CallbackHook {
	return &CallbackHook{
		Name: "timing",
		Fn: func(_ context.Context, event Event) (*Result, error) {
			post, ok := event.(*PostToolUseEvent)
			if !ok {
				return &Result{Decision: "allow"}, nil
			}

			ns := post.ToolElapsedMs * int64(time.Millisecond)
			toolName := post.ToolName_

			// Get or create stats entry (map access needs mutex).
			td.mu.RLock()
			s, exists := td.stats[toolName]
			td.mu.RUnlock()

			if !exists {
				td.mu.Lock()
				// Double-check after acquiring write lock.
				s, exists = td.stats[toolName]
				if !exists {
					s = &timingStat{Min: ns, Max: ns}
					td.stats[toolName] = s
				}
				td.mu.Unlock()
			}

			// Atomic counter updates (no mutex needed for struct fields).
			atomic.AddInt64(&s.Count, 1)
			atomic.AddInt64(&s.Total, ns)

			// Atomic min/max update via CAS loop.
			for {
				old := atomic.LoadInt64(&s.Min)
				if ns >= old || atomic.CompareAndSwapInt64(&s.Min, old, ns) {
					break
				}
			}
			for {
				old := atomic.LoadInt64(&s.Max)
				if ns <= old || atomic.CompareAndSwapInt64(&s.Max, old, ns) {
					break
				}
			}

			return &Result{Decision: "allow"}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// ApprovalCallback
// ---------------------------------------------------------------------------

// ApprovalState holds the mutable state for the approval callback.
// The handler can be swapped at runtime via SetHandler.
type ApprovalState struct {
	mu      sync.RWMutex
	handler tools.ApprovalHandler
	timeout time.Duration
}

// NewApprovalState creates an ApprovalState with the given handler.
// If handler is nil, privileged operations will be denied.
func NewApprovalState(handler tools.ApprovalHandler) *ApprovalState {
	return &ApprovalState{
		handler: handler,
		timeout: 60 * time.Second,
	}
}

// SetHandler replaces the approval handler at runtime.
func (s *ApprovalState) SetHandler(h tools.ApprovalHandler) {
	s.mu.Lock()
	s.handler = h
	s.mu.Unlock()
}

// GetHandler returns the current approval handler.
func (s *ApprovalState) GetHandler() tools.ApprovalHandler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handler
}

// ApprovalCallback returns a CallbackHook that intercepts tool calls targeting
// privileged users and requires explicit user approval.
//
// ApprovalHandler / ApprovalRequest / ApprovalResult types remain in the tools
// package. PermUsers are read from ctx (injected by the engine per-request).
func ApprovalCallback(state *ApprovalState) *CallbackHook {
	return &CallbackHook{
		Name: "approval",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			pre, ok := event.(*PreToolUseEvent)
			if !ok {
				return &Result{Decision: "allow"}, nil
			}

			// Read user configuration from context (per-request, from user_settings).
			defaultUser, privilegedUser := tools.PermUsersFromContext(ctx)

			// Feature not configured — ignore any run_as/reason (may be stale LLM context).
			if defaultUser == "" && privilegedUser == "" {
				return &Result{Decision: "allow"}, nil
			}

			argsJSON := toolArgsToString(pre.ToolInput_)
			runAs, reason := extractRunAsAndReason(argsJSON)

			if (strings.TrimSpace(runAs) == "") != (strings.TrimSpace(reason) == "") {
				return &Result{
					Decision: "deny",
					Reason:   "run_as and reason must be provided together",
				}, nil
			}

			// No run_as specified — execute as current process user.
			if runAs == "" {
				return &Result{Decision: "allow"}, nil
			}

			// Validate run_as against configured users.
			if runAs == defaultUser {
				return &Result{Decision: "allow"}, nil
			}

			if runAs != privilegedUser {
				users := defaultUser
				if privilegedUser != "" {
					if users != "" {
						users += " or " + privilegedUser
					} else {
						users = privilegedUser
					}
				}
				return &Result{
					Decision: "deny",
					Reason:   fmt.Sprintf("unknown run_as user %q: must be %q", runAs, users),
				}, nil
			}

			// Privileged user — request approval.
			state.mu.RLock()
			handler := state.handler
			state.mu.RUnlock()

			if handler == nil {
				return &Result{
					Decision: "deny",
					Reason:   fmt.Sprintf("execution as %q requires approval but no approval handler is available (running in non-interactive channel?)", runAs),
				}, nil
			}

			approvalCtx, cancel := context.WithTimeout(ctx, state.timeout)
			defer cancel()

			req := tools.ApprovalRequest{
				ToolName: pre.ToolName_,
				ToolArgs: argsJSON,
				RunAs:    runAs,
			}

			populateApprovalDetails(&req, pre.ToolName_, argsJSON)

			result, err := handler.RequestApproval(approvalCtx, req)
			if err != nil {
				return &Result{
					Decision: "deny",
					Reason:   fmt.Sprintf("approval request failed: %s", err),
				}, nil
			}
			if !result.Approved {
				denyMsg := fmt.Sprintf("user denied execution as %q", runAs)
				if strings.TrimSpace(result.DenyReason) != "" {
					denyMsg = fmt.Sprintf("user denied execution as %q: %s", runAs, strings.TrimSpace(result.DenyReason))
				}
				return &Result{Decision: "deny", Reason: denyMsg}, nil
			}

			return &Result{Decision: "allow"}, nil
		},
	}
}

// extractRunAsAndReason parses run_as and reason from JSON tool arguments.
func extractRunAsAndReason(args string) (runAs, reason string) {
	var raw struct {
		RunAs  string `json:"run_as"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", ""
	}
	return raw.RunAs, raw.Reason
}

// truncateApprovalText truncates s to at most max bytes, appending "..." if
// truncated. Whitespace is trimmed first.
func truncateApprovalText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// populateApprovalDetails extracts human-readable details for the approval
// dialog.
func populateApprovalDetails(req *tools.ApprovalRequest, toolName, args string) {
	const maxDisplayLen = 160

	switch toolName {
	case "Shell":
		var p struct {
			Command string `json:"command"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.Command = truncateApprovalText(p.Command, maxDisplayLen)
			req.ArgsSummary = req.Command
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = truncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Execute command as %q", req.RunAs)
			}
		}
	case "FileCreate":
		var p struct {
			Path   string `json:"path"`
			RunAs  string `json:"run_as"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.FilePath = truncateApprovalText(p.Path, maxDisplayLen)
			req.ArgsSummary = req.FilePath
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = truncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Create file as %q", req.RunAs)
			}
		}
	case "FileReplace":
		var p struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
			Reason    string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.FilePath = truncateApprovalText(p.Path, maxDisplayLen)
			req.ArgsSummary = fmt.Sprintf("old=%q new=%q", truncateApprovalText(p.OldString, 40), truncateApprovalText(p.NewString, 40))
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = truncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Modify file as %q", req.RunAs)
			}
		}
	}
	if req.Reason == "" {
		req.Reason = fmt.Sprintf("Execute %s as %q", toolName, req.RunAs)
	}
}

// ---------------------------------------------------------------------------
// CheckpointCallback
// ---------------------------------------------------------------------------

// maxCheckpointFileSize is the maximum file size (in bytes) to snapshot.
// Files larger than this are skipped (1 MB).
const maxCheckpointFileSize = 1 << 20

// CheckpointState holds the mutable state for the checkpoint callback.
type CheckpointState struct {
	mu      sync.Mutex
	store   *tools.CheckpointStore
	turnIdx int
	// pending stores snapshots between Pre and Post events, keyed by file path.
	pending map[string]tools.FileSnapshot
}

// NewCheckpointState creates a CheckpointState backed by the given store.
func NewCheckpointState(store *tools.CheckpointStore) *CheckpointState {
	return &CheckpointState{
		store:   store,
		pending: make(map[string]tools.FileSnapshot),
	}
}

// SetTurnIdx sets the current turn index. Should be called before each agent
// turn (i.e., before each user message is processed by the agent).
func (cs *CheckpointState) SetTurnIdx(idx int) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.turnIdx = idx
}

// TurnIdx returns the current turn index.
func (cs *CheckpointState) TurnIdx() int {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.turnIdx
}

// Store returns the underlying CheckpointStore.
func (cs *CheckpointState) Store() *tools.CheckpointStore {
	return cs.store
}

// CheckpointCallback returns a CallbackHook that snapshots files before
// FileCreate/FileReplace and persists the snapshots after successful
// execution.
func CheckpointCallback(cs *CheckpointState) *CallbackHook {
	return &CallbackHook{
		Name: "checkpoint",
		Fn: func(ctx context.Context, event Event) (*Result, error) {
			switch e := event.(type) {
			case *PreToolUseEvent:
				handleCheckpointPre(ctx, cs, e)
			case *PostToolUseEvent:
				handleCheckpointPost(ctx, cs, e)
			}
			return &Result{Decision: "allow"}, nil
		},
	}
}

// handleCheckpointPre snapshots the file before a FileCreate/FileReplace
// operation.
func handleCheckpointPre(ctx context.Context, cs *CheckpointState, e *PreToolUseEvent) {
	toolName := e.ToolName_
	if toolName != "FileCreate" && toolName != "FileReplace" {
		return
	}

	filePath := extractFilePath(e.ToolInput_)
	if filePath == "" {
		log.WithField("tool", toolName).Warn("checkpoint hook: empty file path from input")
		return
	}

	// Resolve to absolute path using working directory from context.
	if !filepath.IsAbs(filePath) {
		wd := tools.WorkingDirFromContext(ctx)
		if wd == "" {
			wd, _ = os.Getwd()
		}
		if wd != "" {
			filePath = filepath.Join(wd, filePath)
		}
	}
	filePath = filepath.Clean(filePath)

	// Read current file content (if it exists).
	var content []byte
	existed := false
	if info, err := os.Stat(filePath); err == nil {
		if info.Size() > maxCheckpointFileSize {
			return // skip large files
		}
		content, err = os.ReadFile(filePath)
		if err != nil {
			return // can't read, skip
		}
		existed = true
	}

	cs.mu.Lock()
	cs.pending[filePath] = tools.FileSnapshot{
		TurnIdx:    cs.turnIdx,
		ToolName:   toolName,
		FilePath:   filePath,
		Existed:    existed,
		ContentB64: base64.StdEncoding.EncodeToString(content),
	}
	cs.mu.Unlock()
}

// handleCheckpointPost confirms the snapshot if the tool succeeded, or
// discards it on failure.
func handleCheckpointPost(ctx context.Context, cs *CheckpointState, e *PostToolUseEvent) {
	toolName := e.ToolName_
	if toolName != "FileCreate" && toolName != "FileReplace" {
		return
	}

	// Parse the file path from tool input to look up the pending entry.
	filePath := extractFilePath(e.ToolInput_)
	if filePath != "" {
		if !filepath.IsAbs(filePath) {
			wd := tools.WorkingDirFromContext(ctx)
			if wd == "" {
				wd, _ = os.Getwd()
			}
			if wd != "" {
				filePath = filepath.Join(wd, filePath)
			}
		}
		filePath = filepath.Clean(filePath)
	}

	cs.mu.Lock()
	snap, found := cs.pending[filePath]
	if found {
		delete(cs.pending, filePath)
	}
	cs.mu.Unlock()

	if !found {
		return
	}

	// Tool failed — discard the snapshot.
	if e.ToolError != "" {
		return
	}

	// Tool succeeded — write snapshot to store.
	if writeErr := cs.store.Write(snap); writeErr != nil {
		log.WithError(writeErr).Warn("checkpoint hook: failed to write snapshot")
	} else {
		log.WithFields(log.Fields{"turn": snap.TurnIdx, "tool": toolName, "file": snap.FilePath, "existed": snap.Existed}).Debug("checkpoint hook: snapshot saved")
	}
}

// extractFilePath extracts the file path from tool input map.
// It handles Windows backslash paths that may not be properly JSON-escaped.
func extractFilePath(input map[string]any) string {
	if input == nil {
		return ""
	}
	pathVal, ok := input["path"]
	if !ok {
		return ""
	}
	pathStr, ok := pathVal.(string)
	if !ok {
		return ""
	}
	return pathStr
}
