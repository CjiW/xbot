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
	ProtoExec         = "exec"
	ProtoReadFile     = "read_file"
	ProtoWriteFile    = "write_file"
	ProtoStat         = "stat"
	ProtoReadDir      = "read_dir"
	ProtoMkdirAll     = "mkdir_all"
	ProtoRemove       = "remove"
	ProtoRemoveAll    = "remove_all"
	ProtoDownloadFile = "download_file"

	ProtoLLMGenerate = "llm_generate"
	ProtoLLMModels   = "llm_models"
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

// Stdio streaming protocol for remote MCP server support.
// Allows the server to start a long-running process on the runner and
// proxy its stdin/stdout over WebSocket.
const (
	ProtoStdioStart = "stdio_start" // Server → Runner: start process (request/response)
	ProtoStdioWrite = "stdio_write" // Server → Runner: write to stdin (fire-and-forget)
	ProtoStdioClose = "stdio_close" // Server → Runner: close stdin / stop process (request/response)
	ProtoStdioData  = "stdio_data"  // Runner → Server: stdout data (push)
	ProtoStdioExit  = "stdio_exit"  // Runner → Server: process exited (push)
)

// Background task protocol for remote sandbox.
// Allows the server to start commands asynchronously on the runner,
// poll status, stream output, and kill tasks.
const (
	ProtoBgExec   = "bg_exec"   // Server → Runner: start background command (request/response)
	ProtoBgKill   = "bg_kill"   // Server → Runner: kill a background task (request/response)
	ProtoBgStatus = "bg_status" // Server → Runner: query task status + output (request/response)
)

// Background task response types (Runner → Server).
const (
	ProtoBgStarted = "bg_started" // Task accepted, returns task_id
	ProtoBgOutput  = "bg_output"  // Task output snapshot (stdout/stderr so far)
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
	Shell     string `json:"shell,omitempty"`     // Runner's default shell path (e.g. /bin/bash)

	// LLM capability declaration (runner self-reports)
	LLMProvider string `json:"llm_provider,omitempty"` // "openai" or "anthropic", empty = no LLM
	LLMModel    string `json:"llm_model,omitempty"`    // default model name
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

	// RunAsUser is the OS username to execute as.
	// When set, the runner wraps the command with sudo -n -H -u <user> --
	RunAsUser string `json:"run_as_user,omitempty"`
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

// DownloadFileRequest requests the runner to download a file from a URL.
type DownloadFileRequest struct {
	URL        string `json:"url"`         // URL to download from
	OutputPath string `json:"output_path"` // absolute path to save the file
}

// DownloadFileResponse contains the download result.
type DownloadFileResponse struct {
	Size int64 `json:"size"` // downloaded file size in bytes
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

// Stdio streaming request/response types.

// StdioStartRequest asks the runner to start a long-running process.
type StdioStartRequest struct {
	StreamID string   `json:"stream_id"`
	Command  string   `json:"command"`
	Args     []string `json:"args,omitempty"`
	Env      []string `json:"env,omitempty"`
	Dir      string   `json:"dir,omitempty"`
}

// StdioStartResponse confirms the process was started.
type StdioStartResponse struct {
	StreamID string `json:"stream_id"`
}

// StdioWriteRequest sends data to the process's stdin.
type StdioWriteRequest struct {
	StreamID string `json:"stream_id"`
	Data     string `json:"data"` // base64-encoded
}

// StdioCloseRequest asks the runner to close stdin and/or kill the process.
type StdioCloseRequest struct {
	StreamID string `json:"stream_id"`
}

// StdioDataMessage carries stdout data from the process (runner → server push).
type StdioDataMessage struct {
	StreamID string `json:"stream_id"`
	Data     string `json:"data"` // base64-encoded
}

// StdioExitMessage notifies the server that the process exited (runner → server push).
type StdioExitMessage struct {
	StreamID string `json:"stream_id"`
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"`
}

// DefaultRequestTimeout is the default timeout for non-exec operations.
const DefaultRequestTimeout = 30 * time.Second

// --- Background Task Protocol Types ---

// BgExecRequest asks the runner to start a command in the background.
type BgExecRequest struct {
	TaskID  string   `json:"task_id"` // server-assigned task ID
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Shell   bool     `json:"shell"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Stdin   string   `json:"stdin,omitempty"`
}

// BgStartedResponse confirms a background task was started.
type BgStartedResponse struct {
	TaskID string `json:"task_id"`
}

// BgKillRequest asks the runner to kill a background task.
type BgKillRequest struct {
	TaskID string `json:"task_id"`
}

// BgStatusRequest asks the runner for the current status of a background task.
type BgStatusRequest struct {
	TaskID string `json:"task_id"`
}

// BgOutputResponse contains the current output snapshot of a background task.
type BgOutputResponse struct {
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`              // "running", "completed", "failed", "killed"
	ExitCode int    `json:"exit_code,omitempty"` // set when status is completed/failed
	Stdout   string `json:"stdout"`              // accumulated stdout so far
	Stderr   string `json:"stderr"`              // accumulated stderr so far
}
