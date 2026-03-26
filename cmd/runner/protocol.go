package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Protocol types for xbot-runner communication.
// NOTE: These types are duplicated from xbot/internal/runnerproto to keep
// cmd/runner as a standalone module. When changing the protocol, update both locations.

// === WebSocket Protocol Messages ===

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

const (
	ProtoExecResult  = "exec_result"
	ProtoFileContent = "file_content"
	ProtoFileInfo    = "file_info"
	ProtoDirEntries  = "dir_entries"
	ProtoError       = "error"
	ProtoOK          = "ok"
)

type RunnerMessage struct {
	ID     string          `json:"id,omitempty"`
	Type   string          `json:"type"`
	UserID string          `json:"user_id,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type RegisterRequest struct {
	UserID    string `json:"user_id"`
	HTTPAddr  string `json:"http_addr"`
	AuthToken string `json:"auth_token"`
}

type ExecRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Shell   bool     `json:"shell"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Stdin   string   `json:"stdin,omitempty"`
	Timeout int      `json:"timeout"`
}

type ExecResultResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
}

type ReadFileRequest struct {
	Path string `json:"path"`
}

type FileContentResponse struct {
	Data string `json:"data"`
}

type WriteFileRequest struct {
	Path string `json:"path"`
	Data string `json:"data"`
	Perm int    `json:"perm"`
}

type StatRequest struct {
	Path string `json:"path"`
}

type StatResponse struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	ModTime string `json:"mod_time"`
	IsDir   bool   `json:"is_dir"`
}

type ReadDirRequest struct {
	Path string `json:"path"`
}

type DirEntryResponse struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type DirEntriesResponse struct {
	Entries []DirEntryResponse `json:"entries"`
}

type PathRequest struct {
	Path string `json:"path"`
	Perm int    `json:"perm,omitempty"`
}

type ErrorResponse struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

var ProtoErrorCodes = map[string]error{
	"ENOENT":  os.ErrNotExist,
	"EEXIST":  os.ErrExist,
	"EPERM":   os.ErrPermission,
	"EISDIR":  fmt.Errorf("is a directory"),
	"ENOTDIR": fmt.Errorf("not a directory"),
	"EINVAL":  os.ErrInvalid,
}

func protoErrorCode(err error) string {
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

func makeResponse(id, respType string, body interface{}) *RunnerMessage {
	data, _ := json.Marshal(body)
	return &RunnerMessage{ID: id, Type: respType, Body: data}
}

func makeError(id string, code, message string) *RunnerMessage {
	return makeResponse(id, ProtoError, ErrorResponse{Code: code, Message: message})
}

func makeOK(id string) *RunnerMessage {
	return &RunnerMessage{ID: id, Type: ProtoOK}
}

const WsFileThreshold = 4 * 1024 * 1024

const DefaultRequestTimeout = 30 * time.Second
