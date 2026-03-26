package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// handleRequest dispatches an incoming request to the appropriate handler.
func handleRequest(msg RunnerMessage, workspace string) *RunnerMessage {
	switch msg.Type {
	case "exec":
		return handleExec(msg, workspace)
	case "read_file":
		return handleReadFile(msg, workspace)
	case "write_file":
		return handleWriteFile(msg, workspace)
	case "stat":
		return handleStat(msg, workspace)
	case "read_dir":
		return handleReadDir(msg, workspace)
	case "mkdir_all":
		return handleMkdirAll(msg, workspace)
	case "remove":
		return handleRemove(msg, workspace)
	case "remove_all":
		return handleRemoveAll(msg, workspace)
	default:
		return makeError(msg.ID, "EINVAL", fmt.Sprintf("unknown request type: %s", msg.Type))
	}
}

func handleExec(msg RunnerMessage, workspace string) *RunnerMessage {
	var req ExecRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", "invalid exec request: "+err.Error())
	}

	timeout := time.Duration(req.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var cmd *exec.Cmd
	if req.Shell {
		cmd = exec.CommandContext(ctx, "sh", "-c", req.Command)
	} else {
		args := req.Args
		if len(args) == 0 {
			// Non-shell mode requires explicit Args; strings.Fields doesn't handle quotes.
			// Return an error instead of producing incorrect argument splitting.
			return makeError(msg.ID, "EINVAL", "non-shell exec requires Args to be set explicitly")
		}
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
	}
	if cmd == nil {
		return makeError(msg.ID, "EINVAL", "no command specified")
	}
	// Create a new process group so we can kill all children on timeout.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if req.Dir != "" {
		if err := validatePath(req.Dir, workspace); err != nil {
			return makeError(msg.ID, "EPERM", err.Error())
		}
		cmd.Dir = filepath.Clean(req.Dir)
	}
	if len(req.Env) > 0 {
		cmd.Env = append(os.Environ(), req.Env...)
	}
	if req.Stdin != "" {
		cmd.Stdin = strings.NewReader(req.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	timedOut := false

	if ctx.Err() == context.DeadlineExceeded {
		timedOut = true
		exitCode = -1
		// Kill the entire process group to prevent child process leaks.
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	} else if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			}
		} else {
			return makeError(msg.ID, "EIO", "exec error: "+err.Error())
		}
	}

	return makeResponse(msg.ID, "exec_result", ExecResultResponse{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		TimedOut: timedOut,
	})
}

func handleReadFile(msg RunnerMessage, workspace string) *RunnerMessage {
	var req ReadFileRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path, workspace)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	return makeResponse(msg.ID, "file_content", FileContentResponse{
		Data: base64.StdEncoding.EncodeToString(data),
	})
}

func handleWriteFile(msg RunnerMessage, workspace string) *RunnerMessage {
	var req WriteFileRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path, workspace)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return makeError(msg.ID, "EINVAL", "invalid base64: "+err.Error())
	}
	if err := os.WriteFile(path, data, os.FileMode(req.Perm)); err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	return makeOK(msg.ID)
}

func handleStat(msg RunnerMessage, workspace string) *RunnerMessage {
	var req StatRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path, workspace)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	info, err := os.Stat(path)
	if err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	return makeResponse(msg.ID, "file_info", StatResponse{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    uint32(info.Mode()),
		ModTime: info.ModTime().Format(time.RFC3339),
		IsDir:   info.IsDir(),
	})
}

func handleReadDir(msg RunnerMessage, workspace string) *RunnerMessage {
	var req ReadDirRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path, workspace)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	resp := DirEntriesResponse{Entries: make([]DirEntryResponse, 0, len(entries))}
	for _, e := range entries {
		info, ierr := e.Info()
		var size int64
		if ierr == nil {
			size = info.Size()
		}
		resp.Entries = append(resp.Entries, DirEntryResponse{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	return makeResponse(msg.ID, "dir_entries", resp)
}

func handleMkdirAll(msg RunnerMessage, workspace string) *RunnerMessage {
	var req PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path, workspace)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	if err := os.MkdirAll(path, os.FileMode(req.Perm)); err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	return makeOK(msg.ID)
}

func handleRemove(msg RunnerMessage, workspace string) *RunnerMessage {
	var req PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path, workspace)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	if err := os.Remove(path); err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	return makeOK(msg.ID)
}

func handleRemoveAll(msg RunnerMessage, workspace string) *RunnerMessage {
	var req PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path, workspace)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	if err := os.RemoveAll(path); err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	return makeOK(msg.ID)
}
