package main

import (
	"encoding/json"
	"os"
)

// RunnerMessage is the WebSocket message envelope.
type RunnerMessage struct {
	ID     string          `json:"id,omitempty"`
	Type   string          `json:"type"`
	UserID string          `json:"user_id,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// RegisterRequest is the registration message sent on first connection.
type RegisterRequest struct {
	UserID    string `json:"user_id"`
	HTTPAddr  string `json:"http_addr"`
	AuthToken string `json:"auth_token"`
}

// Request types (Server → Runner)
type ExecRequest struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Shell   bool     `json:"shell"`
	Dir     string   `json:"dir,omitempty"`
	Env     []string `json:"env,omitempty"`
	Stdin   string   `json:"stdin,omitempty"`
	Timeout int      `json:"timeout"`
}

type ReadFileRequest struct {
	Path string `json:"path"`
}

type WriteFileRequest struct {
	Path string `json:"path"`
	Data string `json:"data"` // base64
	Perm int    `json:"perm"`
}

type StatRequest struct {
	Path string `json:"path"`
}

type ReadDirRequest struct {
	Path string `json:"path"`
}

type PathRequest struct {
	Path string `json:"path"`
	Perm int    `json:"perm,omitempty"`
}

// Response types (Runner → Server)
type ErrorResponse struct {
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

type ExecResultResponse struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
}

type FileContentResponse struct {
	Data string `json:"data"` // base64
}

type StatResponse struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	ModTime string `json:"mod_time"` // RFC3339
	IsDir   bool   `json:"is_dir"`
}

type DirEntryResponse struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

type DirEntriesResponse struct {
	Entries []DirEntryResponse `json:"entries"`
}

func makeResponse(id, respType string, body interface{}) *RunnerMessage {
	data, _ := json.Marshal(body)
	return &RunnerMessage{ID: id, Type: respType, Body: data}
}

func makeError(id string, code, message string) *RunnerMessage {
	return makeResponse(id, "error", ErrorResponse{Code: code, Message: message})
}

func makeOK(id string) *RunnerMessage {
	return &RunnerMessage{ID: id, Type: "ok"}
}

// protoErrorCode converts a Go error to a protocol error code.
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
