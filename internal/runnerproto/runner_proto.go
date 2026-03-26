// Package runnerproto defines the shared WebSocket protocol types between
// the xbot server (tools/remote_sandbox.go) and the runner CLI (cmd/runner/).
// Both sides import this package to ensure protocol consistency.
package runnerproto

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// === WebSocket Protocol Messages ===

// Request types (Server → Runner)
const (
	ProtoExec      = "exec"
	ProtoReadFile  = "read_file"
	ProtoWriteFile = "write_file"
	ProtoStat      = "stat"
	ProtoReadDir   = "read_dir"
	ProtoMkdirAll  = "mkdir_all"
	ProtoRemove    = "remove"
	ProtoRemoveAll = "remove_all"
)

// Response types (Runner → Server)
const (
	ProtoExecResult  = "exec_result"
	ProtoFileContent = "file_content"
	ProtoFileInfo    = "file_info"
	ProtoDirEntries  = "dir_entries"
	ProtoError       = "error"
	ProtoOK          = "ok"
)

// RunnerMessage is the envelope for all WebSocket messages.
type RunnerMessage struct {
	ID     string          `json:"id,omitempty"`
	Type   string          `json:"type"`
	UserID string          `json:"user_id,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// RegisterRequest is sent by the runner on first connection.
type RegisterRequest struct {
	UserID    string `json:"user_id"`
	AuthToken string `json:"auth_token"`
	Workspace string `json:"workspace,omitempty"` // Runner's workspace root directory
}

// ExecRequest requests command execution on the runner.
type ExecRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Shell   bool     `json:"shell"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Stdin   string   `json:"stdin,omitempty"`
	Timeout int      `json:"timeout"` // seconds
}

// ExecResultResponse is the response for command execution.
type ExecResultResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
}

// ReadFileRequest requests file content.
type ReadFileRequest struct {
	Path string `json:"path"`
}

// FileContentResponse contains base64-encoded file content.
type FileContentResponse struct {
	Data string `json:"data"` // base64
}

// WriteFileRequest writes data to a file.
type WriteFileRequest struct {
	Path string `json:"path"`
	Data string `json:"data"` // base64
	Perm int    `json:"perm"` // os.FileMode
}

// StatRequest requests file metadata.
type StatRequest struct {
	Path string `json:"path"`
}

// StatResponse contains file metadata.
type StatResponse struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	ModTime string `json:"mod_time"` // RFC3339
	IsDir   bool   `json:"is_dir"`
}

// ReadDirRequest requests directory listing.
type ReadDirRequest struct {
	Path string `json:"path"`
}

// DirEntryResponse is a single directory entry.
type DirEntryResponse struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// DirEntriesResponse contains a list of directory entries.
type DirEntriesResponse struct {
	Entries []DirEntryResponse `json:"entries"`
}

// PathRequest is a simple path-based request (used by mkdir_all, remove, remove_all).
type PathRequest struct {
	Path string `json:"path"`
	Perm int    `json:"perm,omitempty"`
}

// ErrorResponse is a generic error response.
type ErrorResponse struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

// ProtoErrorCodes maps protocol error codes to Go errors.
var ProtoErrorCodes = map[string]error{
	"ENOENT":  os.ErrNotExist,
	"EEXIST":  os.ErrExist,
	"EPERM":   os.ErrPermission,
	"EISDIR":  fmt.Errorf("is a directory"),
	"ENOTDIR": fmt.Errorf("not a directory"),
	"EINVAL":  os.ErrInvalid,
}

// ProtoErrorCode converts a Go error to a protocol error code.
func ProtoErrorCode(err error) string {
	switch {
	case os.IsNotExist(err):
		return "ENOENT"
	case os.IsExist(err):
		return "EEXIST"
	case os.IsPermission(err):
		return "EPERM"
	default:
		return "EIO"
	}
}

// MakeResponse creates a RunnerMessage with the given type and body.
func MakeResponse(id, respType string, body interface{}) *RunnerMessage {
	data, _ := json.Marshal(body)
	return &RunnerMessage{ID: id, Type: respType, Body: data}
}

// MakeError creates an error RunnerMessage.
func MakeError(id string, code, message string) *RunnerMessage {
	return MakeResponse(id, ProtoError, ErrorResponse{Code: code, Message: message})
}

// MakeOK creates an OK RunnerMessage.
func MakeOK(id string) *RunnerMessage {
	return &RunnerMessage{ID: id, Type: ProtoOK}
}

// DefaultRequestTimeout is the default timeout for non-exec operations.
const DefaultRequestTimeout = 30 * time.Second
