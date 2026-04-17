package runnerclient

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"xbot/internal/runnerproto"
	"xbot/llm"
)

// Handler 处理从 server 收到的请求。
type Handler struct {
	Executor        Executor
	PathGuard       *PathGuard
	LLMClient       llm.LLM
	LLMModels       []string
	LLMProviderName string // provider name for self-reporting (e.g. "openai", "anthropic")
	Verbose         bool

	// 内部管理
	stdioMgr *stdioManager
	bgMgr    *bgTaskManager

	// 日志回调（nil 时静默）
	LogFunc LogFunc

	// 模式标记
	dockerMode bool
}

// HandlerOption 是 Handler 的可选配置函数。
type HandlerOption func(*Handler)

// WithVerbose 设置详细日志。
func WithVerbose(v bool) HandlerOption {
	return func(h *Handler) { h.Verbose = v }
}

// WithPathGuard 设置 PathGuard。
func WithPathGuard(pg *PathGuard) HandlerOption {
	return func(h *Handler) { h.PathGuard = pg }
}

// WithDockerMode 设置 Docker 模式。
func WithDockerMode(v bool) HandlerOption {
	return func(h *Handler) { h.dockerMode = v }
}

// WithLogFunc 设置日志回调函数（nil 时静默）。
func WithLogFunc(f LogFunc) HandlerOption {
	return func(h *Handler) { h.LogFunc = f }
}

// NewHandler 创建一个 Handler。
func NewHandler(exec Executor, opts ...HandlerOption) *Handler {
	h := &Handler{
		Executor: exec,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// InitLLM 初始化 LLM 客户端。
func (h *Handler) InitLLM(provider, baseURL, apiKey, model string) error {
	client, models, err := InitLLMClient(provider, baseURL, apiKey, model, h.LogFunc)
	if err != nil {
		return err
	}
	h.LLMClient = client
	h.LLMModels = models
	if client != nil {
		h.LLMProviderName = provider
	}
	return nil
}

// SetLLMClient 直接设置 LLM 客户端（用于 TUI runner 复用已有客户端）。
// provider 参数用于 runner 自报告 LLM 能力（传空字符串表示无 LLM）。
func (h *Handler) SetLLMClient(client llm.LLM, models []string, provider string) {
	h.LLMClient = client
	h.LLMModels = models
	if client != nil && provider != "" {
		h.LLMProviderName = provider
	}
}

// LLMProvider 返回 LLM provider 名称（空 = 无 LLM）。
func (h *Handler) LLMProvider() string {
	if h.LLMClient == nil {
		return ""
	}
	return h.LLMProviderName
}

// LLMModel 返回默认模型名称。
func (h *Handler) LLMModel() string {
	if len(h.LLMModels) > 0 {
		return h.LLMModels[0]
	}
	return ""
}

// SetWriteChannels 设置写通道（在启动 ReadLoop 前调用）。
func (h *Handler) SetWriteChannels(writeCh chan<- WriteMsg, writeDone <-chan struct{}) {
	h.ensureManagers()
	h.stdioMgr.SetWriteChannels(writeCh, writeDone)
}

// Cleanup 清理所有资源（stdio 进程、后台任务）。
func (h *Handler) Cleanup() {
	if h.stdioMgr != nil {
		h.stdioMgr.Cleanup()
	}
	if h.bgMgr != nil {
		h.bgMgr.Cleanup()
	}
}

// ensureManagers 确保 stdio 和 bg task 管理器已初始化。
func (h *Handler) ensureManagers() {
	if h.stdioMgr == nil {
		h.stdioMgr = newStdioManager(h.Verbose, h.dockerMode, h.LogFunc)
		h.stdioMgr.executor = h.Executor
	}
	if h.bgMgr == nil {
		ws := ""
		if h.PathGuard != nil {
			ws = h.PathGuard.Workspace
		}
		h.bgMgr = newBgTaskManager(h.Verbose, h.dockerMode, ws, h.LogFunc)
		h.bgMgr.executor = h.Executor
	}
}

// HandleRequest 处理一个请求并返回响应。
func (h *Handler) HandleRequest(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	resp := h.Dispatch(msg)

	if resp.Type == runnerproto.ProtoError {
		var e runnerproto.ErrorResponse
		if json.Unmarshal(resp.Body, &e) == nil {
			callLogf(h.LogFunc, "← %s [id=%s] error: %s — %s", msg.Type, msg.ID, e.Code, e.Message)
		}
	} else if h.Verbose {
		callLogf(h.LogFunc, "← %s [id=%s] ok", msg.Type, msg.ID)
	}

	return resp
}

// Dispatch 根据消息类型分发到对应的处理函数。
func (h *Handler) Dispatch(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	h.ensureManagers()

	switch msg.Type {
	case "exec":
		return h.handleExec(msg)
	case runnerproto.ProtoBgExec:
		return h.handleBgExec(msg)
	case runnerproto.ProtoBgKill:
		return h.handleBgKill(msg)
	case runnerproto.ProtoBgStatus:
		return h.handleBgStatus(msg)
	case runnerproto.ProtoLLMGenerate:
		return handleLLMGenerate(msg, h.LLMClient, h.LogFunc)
	case runnerproto.ProtoLLMModels:
		return handleLLMModels(msg, h.LLMClient, h.LLMModels, h.LogFunc)
	case "read_file":
		return h.handleReadFile(msg)
	case "write_file":
		return h.handleWriteFile(msg)
	case "stat":
		return h.handleStat(msg)
	case "read_dir":
		return h.handleReadDir(msg)
	case "mkdir_all":
		return h.handleMkdirAll(msg)
	case "remove":
		return h.handleRemove(msg)
	case "remove_all":
		return h.handleRemoveAll(msg)
	case "download_file":
		return h.handleDownloadFile(msg)
	case runnerproto.ProtoStdioStart:
		return h.stdioMgr.HandleStart(msg)
	case runnerproto.ProtoStdioClose:
		return h.stdioMgr.HandleClose(msg)
	default:
		return runnerproto.MakeError(msg.ID, "EINVAL", fmt.Sprintf("unknown request type: %s", msg.Type))
	}
}

// DispatchFireAndForget 处理不需要响应的消息。
func (h *Handler) DispatchFireAndForget(msg runnerproto.RunnerMessage) {
	h.ensureManagers()

	switch msg.Type {
	case runnerproto.ProtoStdioWrite:
		h.stdioMgr.HandleWrite(msg)
	}
}

func (h *Handler) handleExec(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.ExecRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid exec request: "+err.Error())
	}

	timeout := time.Duration(req.Timeout) * time.Second
	// Guard against integer overflow: cap at 1 hour
	if req.Timeout <= 0 || req.Timeout > 3600 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	spec := ExecSpec{
		Command:   req.Command,
		Args:      req.Args,
		Shell:     req.Shell,
		Dir:       req.Dir,
		Env:       req.Env,
		Stdin:     req.Stdin,
		Timeout:   timeout,
		RunAsUser: req.RunAsUser,
	}

	// pathguard 检查工作目录
	if spec.Dir != "" && h.PathGuard != nil {
		if err := h.PathGuard.Validate(spec.Dir); err != nil {
			return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
		}
	}

	result, err := h.Executor.Exec(ctx, spec)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "exec error: "+err.Error())
	}

	callLogf(h.LogFunc, "  exec done  exit=%d  stdout=%dB  stderr=%dB", result.ExitCode, len(result.Stdout), len(result.Stderr))
	return runnerproto.MakeResponse(msg.ID, "exec_result", runnerproto.ExecResultResponse{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
		TimedOut: result.TimedOut,
	})
}

func (h *Handler) handleReadFile(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.ReadFileRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(req.Path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}
	data, err := h.Executor.ReadFile(path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, runnerproto.ProtoErrorCode(err), err.Error())
	}
	if h.Verbose {
		callLogf(h.LogFunc, "  read_file %s (%d bytes)", req.Path, len(data))
	}
	return runnerproto.MakeResponse(msg.ID, "file_content", runnerproto.FileContentResponse{
		Data: base64.StdEncoding.EncodeToString(data),
	})
}

func (h *Handler) handleWriteFile(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.WriteFileRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(req.Path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}
	data, err := base64.StdEncoding.DecodeString(req.Data)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid base64: "+err.Error())
	}
	if err := h.Executor.WriteFile(path, data, os.FileMode(req.Perm)); err != nil {
		return runnerproto.MakeError(msg.ID, runnerproto.ProtoErrorCode(err), err.Error())
	}
	if h.Verbose {
		callLogf(h.LogFunc, "  write_file %s (%d bytes)", req.Path, len(data))
	}
	return runnerproto.MakeOK(msg.ID)
}

func (h *Handler) handleStat(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.StatRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(req.Path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}
	info, err := h.Executor.Stat(path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, runnerproto.ProtoErrorCode(err), err.Error())
	}
	return runnerproto.MakeResponse(msg.ID, "file_info", runnerproto.StatResponse{
		Name:    info.Name,
		Size:    info.Size,
		Mode:    uint32(info.Mode),
		ModTime: info.ModTime.Format(time.RFC3339),
		IsDir:   info.IsDir,
	})
}

func (h *Handler) handleReadDir(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.ReadDirRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(req.Path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}
	entries, err := h.Executor.ReadDir(path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, runnerproto.ProtoErrorCode(err), err.Error())
	}
	resp := runnerproto.DirEntriesResponse{Entries: make([]runnerproto.DirEntryResponse, 0, len(entries))}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, runnerproto.DirEntryResponse{
			Name:  e.Name,
			IsDir: e.IsDir,
			Size:  e.Size,
		})
	}
	if h.Verbose {
		callLogf(h.LogFunc, "  read_dir %s (%d entries)", req.Path, len(resp.Entries))
	}
	return runnerproto.MakeResponse(msg.ID, "dir_entries", resp)
}

func (h *Handler) handleMkdirAll(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(req.Path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}
	if err := h.Executor.MkdirAll(path, os.FileMode(req.Perm)); err != nil {
		return runnerproto.MakeError(msg.ID, runnerproto.ProtoErrorCode(err), err.Error())
	}
	if h.Verbose {
		callLogf(h.LogFunc, "  mkdir_all %s", req.Path)
	}
	return runnerproto.MakeOK(msg.ID)
}

func (h *Handler) handleRemove(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(req.Path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}
	if err := h.Executor.Remove(path); err != nil {
		return runnerproto.MakeError(msg.ID, runnerproto.ProtoErrorCode(err), err.Error())
	}
	if h.Verbose {
		callLogf(h.LogFunc, "  remove %s", req.Path)
	}
	return runnerproto.MakeOK(msg.ID)
}

func (h *Handler) handleRemoveAll(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.PathRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(req.Path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}
	if err := h.Executor.RemoveAll(path); err != nil {
		return runnerproto.MakeError(msg.ID, runnerproto.ProtoErrorCode(err), err.Error())
	}
	if h.Verbose {
		callLogf(h.LogFunc, "  remove_all %s", req.Path)
	}
	return runnerproto.MakeOK(msg.ID)
}

func (h *Handler) handleDownloadFile(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.DownloadFileRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", err.Error())
	}
	path, err := h.safePath(req.OutputPath)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
	}

	// 5 分钟超时
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	size, err := h.Executor.DownloadFile(ctx, req.URL, path)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "download failed: "+err.Error())
	}

	callLogf(h.LogFunc, "  download_file %s → %s (%d bytes)", req.URL, req.OutputPath, size)
	return runnerproto.MakeResponse(msg.ID, runnerproto.ProtoOK, runnerproto.DownloadFileResponse{Size: size})
}

func (h *Handler) handleBgExec(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.BgExecRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid bg_exec request: "+err.Error())
	}

	// pathguard 检查工作目录
	if req.Dir != "" && h.PathGuard != nil {
		if err := h.PathGuard.Validate(req.Dir); err != nil {
			return runnerproto.MakeError(msg.ID, "EPERM", err.Error())
		}
	}

	resp, err := h.bgMgr.Start(req)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "bg_exec failed: "+err.Error())
	}

	return runnerproto.MakeResponse(msg.ID, runnerproto.ProtoBgStarted, resp)
}

func (h *Handler) handleBgKill(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.BgKillRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid bg_kill request: "+err.Error())
	}

	if err := h.bgMgr.Kill(req); err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "bg_kill failed: "+err.Error())
	}

	return runnerproto.MakeOK(msg.ID)
}

func (h *Handler) handleBgStatus(msg runnerproto.RunnerMessage) *runnerproto.RunnerMessage {
	var req runnerproto.BgStatusRequest
	if err := json.Unmarshal(msg.Body, &req); err != nil {
		return runnerproto.MakeError(msg.ID, "EINVAL", "invalid bg_status request: "+err.Error())
	}

	resp, err := h.bgMgr.Status(req)
	if err != nil {
		return runnerproto.MakeError(msg.ID, "EIO", "bg_status failed: "+err.Error())
	}

	return runnerproto.MakeResponse(msg.ID, runnerproto.ProtoBgOutput, resp)
}

// safePath 是 PathGuard.SafePath 的便捷方法。
func (h *Handler) safePath(path string) (string, error) {
	if h.PathGuard == nil {
		return path, nil
	}
	return h.PathGuard.SafePath(path)
}
