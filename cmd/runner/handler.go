package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// handleRequest dispatches an incoming request to the appropriate handler.
func handleRequest(msg RunnerMessage, workspace string) *RunnerMessage {
	resp := dispatch(msg, workspace)

	if resp.Type == ProtoError {
		var e ErrorResponse
		if json.Unmarshal(resp.Body, &e) == nil {
			log.Printf("← %s [id=%s] error: %s — %s", msg.Type, msg.ID, e.Code, e.Message)
		}
	} else if verboseLog {
		log.Printf("← %s [id=%s] ok", msg.Type, msg.ID)
	}

	return resp
}

func dispatch(msg RunnerMessage, workspace string) *RunnerMessage {
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
		if verboseLog {
			log.Printf("  exec shell: %s  (dir=%s, timeout=%v)", req.Command, req.Dir, timeout)
		}
	} else {
		args := req.Args
		if len(args) == 0 {
			return makeError(msg.ID, "EINVAL", "non-shell exec requires Args to be set explicitly")
		}
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
		if verboseLog {
			log.Printf("  exec: %s  (dir=%s, timeout=%v)", strings.Join(args, " "), req.Dir, timeout)
		}
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
	} else {
		cmd.Dir = workspace
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

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	exitCode := 0
	timedOut := false

	if ctx.Err() == context.DeadlineExceeded {
		timedOut = true
		exitCode = -1
		// Kill the entire process group to prevent child process leaks.
		if cmd.Process != nil {
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		log.Printf("  exec timed out after %v: %s", elapsed, req.Command)
	} else if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			}
		} else {
			return makeError(msg.ID, "EIO", "exec error: "+err.Error())
		}
	}

	log.Printf("  exec done in %v  exit=%d  stdout=%dB  stderr=%dB", elapsed, exitCode, stdout.Len(), stderr.Len())

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
	if verboseLog {
		log.Printf("  read_file %s (%d bytes)", req.Path, len(data))
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
	if verboseLog {
		log.Printf("  write_file %s (%d bytes)", req.Path, len(data))
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
	if verboseLog {
		log.Printf("  read_dir %s (%d entries)", req.Path, len(resp.Entries))
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
	if verboseLog {
		log.Printf("  mkdir_all %s", req.Path)
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
	if verboseLog {
		log.Printf("  remove %s", req.Path)
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
	if verboseLog {
		log.Printf("  remove_all %s", req.Path)
	}
	return makeOK(msg.ID)
}
