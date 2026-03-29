package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"
)

// executor is the global executor, initialized in main.go.
var executor Executor

// handleRequest dispatches an incoming request to the appropriate handler.
func handleRequest(msg RunnerMessage) *RunnerMessage {
	resp := dispatch(msg)

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

func dispatch(msg RunnerMessage) *RunnerMessage {
	switch msg.Type {
	case "exec":
		return handleExec(msg)
	case "read_file":
		return handleReadFile(msg)
	case "write_file":
		return handleWriteFile(msg)
	case "stat":
		return handleStat(msg)
	case "read_dir":
		return handleReadDir(msg)
	case "mkdir_all":
		return handleMkdirAll(msg)
	case "remove":
		return handleRemove(msg)
	case "remove_all":
		return handleRemoveAll(msg)
	case "download_file":
		return handleDownloadFile(msg)
	default:
		return makeError(msg.ID, "EINVAL", fmt.Sprintf("unknown request type: %s", msg.Type))
	}
}

func handleExec(msg RunnerMessage) *RunnerMessage {
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

	spec := ExecSpec{
		Command: req.Command,
		Args:    req.Args,
		Shell:   req.Shell,
		Dir:     req.Dir,
		Env:     req.Env,
		Stdin:   req.Stdin,
		Timeout: timeout,
	}

	// pathguard 检查工作目录
	if spec.Dir != "" {
		if err := validatePath(spec.Dir); err != nil {
			return makeError(msg.ID, "EPERM", err.Error())
		}
	}

	result, err := executor.Exec(ctx, spec)
	if err != nil {
		return makeError(msg.ID, "EIO", "exec error: "+err.Error())
	}

	log.Printf("  exec done  exit=%d  stdout=%dB  stderr=%dB", result.ExitCode, len(result.Stdout), len(result.Stderr))
	return makeResponse(msg.ID, "exec_result", ExecResultResponse{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
		TimedOut: result.TimedOut,
	})
}

func handleReadFile(msg RunnerMessage) *RunnerMessage {
	var req ReadFileRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	data, err := executor.ReadFile(path)
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

func handleWriteFile(msg RunnerMessage) *RunnerMessage {
	var req WriteFileRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return makeError(msg.ID, "EINVAL", "invalid base64: "+err.Error())
	}
	if err := executor.WriteFile(path, data, os.FileMode(req.Perm)); err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	if verboseLog {
		log.Printf("  write_file %s (%d bytes)", req.Path, len(data))
	}
	return makeOK(msg.ID)
}

func handleStat(msg RunnerMessage) *RunnerMessage {
	var req StatRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	info, err := executor.Stat(path)
	if err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	return makeResponse(msg.ID, "file_info", StatResponse{
		Name:    info.Name,
		Size:    info.Size,
		Mode:    uint32(info.Mode),
		ModTime: info.ModTime.Format(time.RFC3339),
		IsDir:   info.IsDir,
	})
}

func handleReadDir(msg RunnerMessage) *RunnerMessage {
	var req ReadDirRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	entries, err := executor.ReadDir(path)
	if err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	resp := DirEntriesResponse{Entries: make([]DirEntryResponse, 0, len(entries))}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, DirEntryResponse{
			Name:  e.Name,
			IsDir: e.IsDir,
			Size:  e.Size,
		})
	}
	if verboseLog {
		log.Printf("  read_dir %s (%d entries)", req.Path, len(resp.Entries))
	}
	return makeResponse(msg.ID, "dir_entries", resp)
}

func handleMkdirAll(msg RunnerMessage) *RunnerMessage {
	var req PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	if err := executor.MkdirAll(path, os.FileMode(req.Perm)); err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	if verboseLog {
		log.Printf("  mkdir_all %s", req.Path)
	}
	return makeOK(msg.ID)
}

func handleRemove(msg RunnerMessage) *RunnerMessage {
	var req PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	if err := executor.Remove(path); err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	if verboseLog {
		log.Printf("  remove %s", req.Path)
	}
	return makeOK(msg.ID)
}

func handleRemoveAll(msg RunnerMessage) *RunnerMessage {
	var req PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.Path)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}
	if err := executor.RemoveAll(path); err != nil {
		return makeError(msg.ID, protoErrorCode(err), err.Error())
	}
	if verboseLog {
		log.Printf("  remove_all %s", req.Path)
	}
	return makeOK(msg.ID)
}

func handleDownloadFile(msg RunnerMessage) *RunnerMessage {
	var req DownloadFileRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return makeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := safePath(req.OutputPath)
	if err != nil {
		return makeError(msg.ID, "EPERM", err.Error())
	}

	// 5-minute timeout for downloads
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	size, err := executor.DownloadFile(ctx, req.URL, path)
	if err != nil {
		return makeError(msg.ID, "EIO", "download failed: "+err.Error())
	}

	log.Printf("  download_file %s → %s (%d bytes)", req.URL, req.OutputPath, size)
	return makeResponse(msg.ID, ProtoOK, DownloadFileResponse{Size: size})
}
