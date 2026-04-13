package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"strings"

	log "xbot/logger"
)

// maxCheckpointFileSize is the maximum file size (in bytes) to snapshot.
// Files larger than this are skipped (1 MB).
const maxCheckpointFileSize = 1 << 20

// FileSnapshot records the state of a file before an agent edit.
type FileSnapshot struct {
	TurnIdx  int    `json:"turn_idx"`
	ToolName string `json:"tool_name"`
	FilePath string `json:"file_path"`
	Existed  bool   `json:"existed"`
	// Base64-encoded file content before the edit. Nil/empty if file didn't exist.
	ContentB64 string `json:"content_b64,omitempty"`
}

// RewindResult summarizes the outcome of a rewind operation.
type RewindResult struct {
	Restored   []string // files restored to pre-edit state
	CreatedDel []string // agent-created files that were deleted
	Skipped    []string // files skipped (too large, sandbox, etc.)
	Errors     []string // files that failed to restore
}

// CheckpointStore persists file snapshots as JSONL.
// Thread-safe: all access is protected by a mutex.
type CheckpointStore struct {
	mu      sync.Mutex
	baseDir string // directory containing changes.jsonl
	file    *os.File
	dirty   bool
}

// NewCheckpointStore creates (or reopens) a checkpoint store for the given session.
// baseDir is typically ~/.xbot/checkpoints/{sessionKey}/
func NewCheckpointStore(baseDir string) (*CheckpointStore, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("checkpoint store mkdir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(baseDir, "changes.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("checkpoint store open: %w", err)
	}
	return &CheckpointStore{baseDir: baseDir, file: f}, nil
}

// Write appends a snapshot to the JSONL file.
func (s *CheckpointStore) Write(snap FileSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(snap)
	if err != nil {
		return fmt.Errorf("checkpoint marshal: %w", err)
	}
	if _, err := s.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("checkpoint write: %w", err)
	}
	s.dirty = true
	return nil
}

// ReadAll reads all snapshots from the JSONL file.
func (s *CheckpointStore) ReadAll() ([]FileSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(filepath.Join(s.baseDir, "changes.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("checkpoint read: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}

	var snapshots []FileSnapshot
	for _, line := range splitJSONLLines(data) {
		if len(line) == 0 {
			continue
		}
		var snap FileSnapshot
		if err := json.Unmarshal(line, &snap); err != nil {
			log.WithError(err).Warn("checkpoint store: skipping malformed line")
			continue
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, nil
}

// Rewind restores files to their state before the given turn index.
// All file edits from turnIdx onwards are reverted.
func (s *CheckpointStore) Rewind(turnIdx int) *RewindResult {
	snapshots, err := s.ReadAll()
	if err != nil {
		log.WithError(err).Warn("checkpoint rewind: failed to read snapshots")
		return &RewindResult{Errors: []string{fmt.Sprintf("read checkpoints: %v", err)}}
	}

	// Filter snapshots from the target turn onwards
	var affected []FileSnapshot
	for _, snap := range snapshots {
		if snap.TurnIdx >= turnIdx {
			affected = append(affected, snap)
		}
	}
	if len(affected) == 0 {
		log.WithField("turnIdx", turnIdx).Debug("checkpoint rewind: no snapshots found at or after turn")
		return &RewindResult{}
	}

	log.WithFields(log.Fields{"turnIdx": turnIdx, "snapshots": len(affected), "total": len(snapshots)}).Debug("checkpoint rewind: starting")

	// Group by file path, keep the earliest snapshot per file (pre-turn state)
	earliestPerFile := make(map[string]FileSnapshot)
	for _, snap := range affected {
		if existing, ok := earliestPerFile[snap.FilePath]; !ok || snap.TurnIdx < existing.TurnIdx {
			earliestPerFile[snap.FilePath] = snap
		}
	}

	result := &RewindResult{}

	for filePath, snap := range earliestPerFile {
		if snap.Existed {
			// Restore file to pre-edit content
			content, err := base64.StdEncoding.DecodeString(snap.ContentB64)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: decode error: %v", filePath, err))
				continue
			}
			if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: mkdir: %v", filePath, err))
				continue
			}
			if err := os.WriteFile(filePath, content, 0644); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: write: %v", filePath, err))
				continue
			}
			result.Restored = append(result.Restored, filePath)
		} else {
			// Agent created this file — delete it
			if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
				result.Errors = append(result.Errors, fmt.Sprintf("%s: delete: %v", filePath, err))
				continue
			}
			result.CreatedDel = append(result.CreatedDel, filePath)
		}
	}

	// After rewind, truncate the JSONL to only keep pre-turnIdx snapshots
	s.truncateTo(turnIdx)

	return result
}

// truncateTo removes all snapshots with turnIdx >= cutoff from the JSONL file.
func (s *CheckpointStore) truncateTo(cutoff int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshots, err := s.readAllInternal()
	if err != nil {
		log.WithError(err).Warn("checkpoint truncate: failed to read")
		return
	}

	// Close current file
	s.file.Close()
	s.file = nil

	// Rewrite with only pre-cutoff snapshots
	f, err := os.Create(filepath.Join(s.baseDir, "changes.jsonl"))
	if err != nil {
		log.WithError(err).Warn("checkpoint truncate: failed to recreate file")
		return
	}

	for _, snap := range snapshots {
		if snap.TurnIdx < cutoff {
			data, err := json.Marshal(snap)
			if err != nil {
				continue
			}
			f.Write(append(data, '\n'))
		}
	}
	s.file = f
}

func (s *CheckpointStore) readAllInternal() ([]FileSnapshot, error) {
	data, err := os.ReadFile(filepath.Join(s.baseDir, "changes.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var snapshots []FileSnapshot
	for _, line := range splitJSONLLines(data) {
		if len(line) == 0 {
			continue
		}
		var snap FileSnapshot
		if err := json.Unmarshal(line, &snap); err != nil {
			continue
		}
		snapshots = append(snapshots, snap)
	}
	return snapshots, nil
}

// Close flushes and closes the checkpoint store.
func (s *CheckpointStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.file.Close()
}

// Cleanup removes the entire checkpoint directory.
func (s *CheckpointStore) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.file.Close()
	return os.RemoveAll(s.baseDir)
}

// HasChanges returns true if there are any recorded file changes for the given turn or later.
func (s *CheckpointStore) HasChanges(turnIdx int) bool {
	snapshots, err := s.ReadAll()
	if err != nil {
		return false
	}
	for _, snap := range snapshots {
		if snap.TurnIdx >= turnIdx {
			return true
		}
	}
	return false
}

// CountChanges returns the number of distinct files affected from turnIdx onwards.
func (s *CheckpointStore) CountChanges(turnIdx int) int {
	snapshots, err := s.ReadAll()
	if err != nil {
		return 0
	}
	seen := make(map[string]bool)
	for _, snap := range snapshots {
		if snap.TurnIdx >= turnIdx {
			seen[snap.FilePath] = true
		}
	}
	return len(seen)
}

// CheckpointHook is a ToolHook that snapshots files before FileCreate/FileReplace operations.
type CheckpointHook struct {
	mu      sync.Mutex
	store   *CheckpointStore
	turnIdx int
	// pending stores snapshots that haven't been confirmed yet (pre-tool, waiting for post-tool)
	pending map[string]FileSnapshot // keyed by file path
}

// NewCheckpointHook creates a new checkpoint hook with the given store.
func NewCheckpointHook(store *CheckpointStore) *CheckpointHook {
	return &CheckpointHook{
		store:   store,
		pending: make(map[string]FileSnapshot),
	}
}

func (h *CheckpointHook) Name() string { return "checkpoint" }

// PreToolUse snapshots the file before a FileCreate/FileReplace operation.
func (h *CheckpointHook) PreToolUse(ctx context.Context, toolName string, args string) error {
	if toolName != "FileCreate" && toolName != "FileReplace" {
		return nil
	}

	filePath := parseFilePath(toolName, args)
	if filePath == "" {
		log.WithField("tool", toolName).Warn("checkpoint hook: empty file path from args")
		return nil
	}

	// Resolve to absolute path using working directory from context
	if !filepath.IsAbs(filePath) {
		wd := WorkingDirFromContext(ctx)
		if wd == "" {
			wd, _ = os.Getwd()
		}
		if wd != "" {
			filePath = filepath.Join(wd, filePath)
		}
	}
	filePath = filepath.Clean(filePath)

	// Read current file content (if it exists)
	var content []byte
	existed := false
	if info, err := os.Stat(filePath); err == nil {
		if info.Size() > maxCheckpointFileSize {
			return nil // skip large files
		}
		content, err = os.ReadFile(filePath)
		if err != nil {
			return nil // can't read, skip
		}
		existed = true
	}

	h.mu.Lock()
	h.pending[filePath] = FileSnapshot{
		TurnIdx:    h.turnIdx,
		ToolName:   toolName,
		FilePath:   filePath,
		Existed:    existed,
		ContentB64: base64.StdEncoding.EncodeToString(content),
	}
	h.mu.Unlock()

	return nil
}

// PostToolUse confirms the snapshot if the tool succeeded, or discards it on failure.
func (h *CheckpointHook) PostToolUse(_ context.Context, toolName string, _ string, _ *ToolResult, err error, _ time.Duration) {
	if toolName != "FileCreate" && toolName != "FileReplace" {
		return
	}

	h.mu.Lock()
	// Find and remove the pending entry (most recent)
	var snap FileSnapshot
	var found bool
	for p, s := range h.pending {
		snap = s
		delete(h.pending, p)
		found = true
		break
	}
	h.mu.Unlock()

	if !found {
		return
	}

	if err != nil {
		// Tool failed — discard the snapshot
		return
	}

	// Tool succeeded — write snapshot to store
	if writeErr := h.store.Write(snap); writeErr != nil {
		log.WithError(writeErr).Warn("checkpoint hook: failed to write snapshot")
	} else {
		log.WithFields(log.Fields{"turn": snap.TurnIdx, "tool": toolName, "file": snap.FilePath, "existed": snap.Existed}).Debug("checkpoint hook: snapshot saved")
	}
}

// SetTurnIdx sets the current turn index. Should be called before each agent turn
// (i.e., before each user message is processed by the agent).
func (h *CheckpointHook) SetTurnIdx(idx int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.turnIdx = idx
}

// TurnIdx returns the current turn index.
func (h *CheckpointHook) TurnIdx() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.turnIdx
}

// Store returns the underlying CheckpointStore.
func (h *CheckpointHook) Store() *CheckpointStore {
	return h.store
}

// parseFilePath extracts the file path from tool arguments JSON.
// It handles Windows backslash paths that may not be properly JSON-escaped
// (e.g. from test code that concatenates filepath.Join output into a JSON string).
func parseFilePath(toolName, args string) string {
	if args == "" {
		return ""
	}
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		// Fallback: try to extract "path" value manually for malformed JSON
		// (e.g. Windows backslash paths not properly escaped in test code).
		// This handles: {"path": "C:\Users\foo\bar.go", ...}
		if idx := strings.Index(args, `"path"`); idx >= 0 {
			rest := args[idx+len(`"path"`):]
			rest = strings.TrimLeft(rest, ": \t\n")
			if len(rest) > 0 && rest[0] == '"' {
				// Find closing quote (raw, not JSON-unescaped)
				if end := strings.Index(rest[1:], `"`); end >= 0 {
					return rest[1 : end+1]
				}
			}
		}
		return ""
	}
	return params.Path
}

// splitJSONLLines splits byte data into JSONL lines.
func splitJSONLLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
