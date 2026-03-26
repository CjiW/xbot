package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	log "xbot/logger"
)

// RemoteSandboxConfig holds configuration for creating a RemoteSandbox.
type RemoteSandboxConfig struct {
	Addr           string   // WebSocket listen address (e.g., "0.0.0.0:8080")
	AuthToken      string   // Authentication token for runners
	AllowedOrigins []string // Allowed WebSocket origins (empty = allow all, for development)
}

// runnerConnection represents a connected xbot-runner instance.
type runnerConnection struct {
	mu       sync.Mutex
	wsConn   *websocket.Conn
	httpAddr string // Runner's HTTP address (e.g., "http://192.168.1.1:12345")
	userID   string
}

// RemoteSandbox implements the Sandbox interface via WebSocket communication
// with xbot-runner instances running on users' machines.
type RemoteSandbox struct {
	connections sync.Map // userID → *runnerConnection
	wsServer    *http.Server
	authToken   string
	addr        string
	pendingMu   sync.Mutex
	pending     map[string]chan *RunnerMessage // request ID → response channel
	upgrader    websocket.Upgrader             // per-instance upgrader with origin check
}

// NewRemoteSandbox creates and starts a RemoteSandbox server.
func NewRemoteSandbox(cfg RemoteSandboxConfig) (*RemoteSandbox, error) {
	if cfg.Addr == "" {
		cfg.Addr = "0.0.0.0:8080"
	}

	// Build per-instance upgrader with origin validation.
	var checkOrigin func(r *http.Request) bool
	if len(cfg.AllowedOrigins) == 0 {
		// No origins configured — allow all (development mode).
		checkOrigin = func(r *http.Request) bool { return true }
	} else {
		allowedSet := make(map[string]struct{}, len(cfg.AllowedOrigins))
		for _, o := range cfg.AllowedOrigins {
			allowedSet[o] = struct{}{}
		}
		checkOrigin = func(r *http.Request) bool {
			_, ok := allowedSet[r.Header.Get("Origin")]
			return ok
		}
	}

	rs := &RemoteSandbox{
		authToken: cfg.AuthToken,
		addr:      cfg.Addr,
		pending:   make(map[string]chan *RunnerMessage),
		upgrader: websocket.Upgrader{
			CheckOrigin: checkOrigin,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", rs.handleWebSocket)

	rs.wsServer = &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,
	}

	go func() {
		log.Infof("RemoteSandbox WebSocket server listening on %s", cfg.Addr)
		if err := rs.wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Error("RemoteSandbox server error")
		}
	}()

	return rs, nil
}

// handleWebSocket handles incoming WebSocket connections from runners.
func (rs *RemoteSandbox) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := rs.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("WebSocket upgrade failed")
		return
	}
	defer conn.Close()

	// Read registration message
	_, raw, err := conn.ReadMessage()
	if err != nil {
		log.WithError(err).Error("Failed to read registration message")
		return
	}

	var msg RunnerMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.WithError(err).Error("Invalid registration message")
		return
	}
	if msg.Type != "register" {
		log.WithField("type", msg.Type).Error("Expected register message")
		return
	}

	var reg RegisterRequest
	if err := json.Unmarshal(msg.Body, &reg); err != nil {
		log.WithError(err).Error("Invalid registration body")
		return
	}
	if reg.AuthToken != rs.authToken {
		log.WithField("user_id", reg.UserID).Warn("Runner authentication failed")
		return
	}
	if reg.UserID == "" {
		log.Warn("Runner registration missing user_id")
		return
	}

	rc := &runnerConnection{
		wsConn:   conn,
		httpAddr: reg.HTTPAddr,
		userID:   reg.UserID,
	}
	rs.connections.Store(reg.UserID, rc)
	log.WithFields(log.Fields{
		"user_id":   reg.UserID,
		"http_addr": reg.HTTPAddr,
	}).Info("Runner connected")

	// Keep reading messages (responses and heartbeats)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.WithError(err).WithField("user_id", reg.UserID).Debug("Runner disconnected")
			rs.connections.Delete(reg.UserID)
			return
		}
		// Handle response: find pending request and deliver
		var resp RunnerMessage
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		if resp.ID != "" {
			rs.pendingMu.Lock()
			if ch, ok := rs.pending[resp.ID]; ok {
				select {
				case ch <- &resp:
				default:
				}
				delete(rs.pending, resp.ID)
			}
			rs.pendingMu.Unlock()
		}
	}
}

// getRunner returns the connection for a user, or an error.
func (rs *RemoteSandbox) getRunner(userID string) (*runnerConnection, error) {
	val, ok := rs.connections.Load(userID)
	if !ok {
		return nil, fmt.Errorf("no runner connected for user %q", userID)
	}
	return val.(*runnerConnection), nil
}

// sendRequest sends a request to the runner and waits for a response.
func (rs *RemoteSandbox) sendRequest(ctx context.Context, rc *runnerConnection, msg *RunnerMessage, timeout time.Duration) (*RunnerMessage, error) {
	rs.pendingMu.Lock()
	ch := make(chan *RunnerMessage, 1)
	rs.pending[msg.ID] = ch
	rs.pendingMu.Unlock()

	defer func() {
		rs.pendingMu.Lock()
		delete(rs.pending, msg.ID)
		rs.pendingMu.Unlock()
	}()

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	rc.mu.Lock()
	err = rc.wsConn.WriteMessage(websocket.TextMessage, data)
	rc.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("request %s timed out after %v", msg.ID, timeout)
	}
}

// generateID generates a unique request ID.
func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("req_%x", b)
}

// === Sandbox Interface Implementation ===

func (rs *RemoteSandbox) Name() string { return "remote" }

func (rs *RemoteSandbox) Close() error {
	return rs.wsServer.Close()
}

func (rs *RemoteSandbox) CloseForUser(userID string) error {
	val, ok := rs.connections.Load(userID)
	if !ok {
		return nil
	}
	rc := val.(*runnerConnection)
	rc.wsConn.Close()
	rs.connections.Delete(userID)
	return nil
}

func (rs *RemoteSandbox) IsExporting(_ string) bool            { return false }
func (rs *RemoteSandbox) ExportAndImport(_ string) error       { return nil }
func (rs *RemoteSandbox) GetShell(_, _ string) (string, error) { return "/bin/sh", nil }

func (rs *RemoteSandbox) Wrap(command string, args []string, env []string, workspace, userID string) (string, []string, error) {
	// Remote mode doesn't use Wrap — commands are sent via Exec
	_ = env
	_ = workspace
	return command, args, nil
}

func (rs *RemoteSandbox) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	rc, err := rs.getRunner(spec.UserID)
	if err != nil {
		return nil, err
	}

	timeout := spec.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	reqBody, _ := json.Marshal(ExecRequest{
		Command: spec.Command,
		Args:    spec.Args,
		Shell:   spec.Shell,
		Dir:     spec.Dir,
		Env:     spec.Env,
		Stdin:   spec.Stdin,
		Timeout: int(timeout / time.Second),
	})

	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   ProtoExec,
		UserID: spec.UserID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, timeout+5*time.Second)
	if err != nil {
		return nil, err
	}

	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		return nil, fmt.Errorf("exec error: %s", e.Message)
	}

	var result ExecResultResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal exec result: %w", err)
	}

	return &ExecResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
		TimedOut: result.TimedOut,
	}, nil
}

func (rs *RemoteSandbox) ReadFile(ctx context.Context, path, userID string) ([]byte, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}

	// Stat first to check size for HTTP fallback
	statResp, err := rs.Stat(ctx, path, userID)
	if err != nil {
		return nil, err
	}

	// Large file → HTTP
	if statResp.Size > wsFileThreshold {
		return rs.readFileHTTP(ctx, rc, path, userID)
	}

	// Small file → WebSocket
	reqBody, _ := json.Marshal(ReadFileRequest{Path: path})
	msg := &RunnerMessage{ID: generateID(), Type: ProtoReadFile, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		if e.Code == "ENOENT" {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("read file: %s", e.Message)
	}
	var fc FileContentResponse
	if err := json.Unmarshal(resp.Body, &fc); err != nil {
		return nil, fmt.Errorf("unmarshal file content: %w", err)
	}
	return base64.StdEncoding.DecodeString(fc.Data)
}

// readFileHTTP downloads a large file via HTTP from the runner.
func (rs *RemoteSandbox) readFileHTTP(ctx context.Context, rc *runnerConnection, path, userID string) ([]byte, error) {
	reqURL := fmt.Sprintf("%s/api/v1/files?path=%s&user_id=%s&token=%s",
		rc.httpAddr, url.PathEscape(path), userID, rs.authToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create HTTP request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if resp.StatusCode == http.StatusNotFound {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("HTTP download failed (%d): %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(io.LimitReader(resp.Body, MaxSandboxFileSize))
}

func (rs *RemoteSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}

	// Large file → HTTP
	if len(data) > wsFileThreshold {
		return rs.writeFileHTTP(ctx, rc, path, data, userID)
	}

	// Small file → WebSocket (base64)
	reqBody, _ := json.Marshal(WriteFileRequest{
		Path: path,
		Data: base64.StdEncoding.EncodeToString(data),
		Perm: int(perm),
	})
	msg := &RunnerMessage{ID: generateID(), Type: ProtoWriteFile, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		return fmt.Errorf("write file: %s", e.Message)
	}
	return nil
}

// writeFileHTTP uploads a large file via HTTP to the runner.
func (rs *RemoteSandbox) writeFileHTTP(ctx context.Context, rc *runnerConnection, path string, data []byte, userID string) error {
	reqURL := fmt.Sprintf("%s/api/v1/files?path=%s&user_id=%s&token=%s",
		rc.httpAddr, url.PathEscape(path), userID, rs.authToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create HTTP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("HTTP upload failed (%d): %s", resp.StatusCode, string(body))
	}
	return nil
}

func (rs *RemoteSandbox) Stat(ctx context.Context, path, userID string) (*SandboxFileInfo, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}
	reqBody, _ := json.Marshal(StatRequest{Path: path})
	msg := &RunnerMessage{ID: generateID(), Type: ProtoStat, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		if e.Code == "ENOENT" {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("stat: %s", e.Message)
	}
	var sr StatResponse
	if err := json.Unmarshal(resp.Body, &sr); err != nil {
		return nil, fmt.Errorf("unmarshal stat: %w", err)
	}
	modTime, _ := time.Parse(time.RFC3339, sr.ModTime)
	return &SandboxFileInfo{
		Name:    sr.Name,
		Size:    sr.Size,
		Mode:    os.FileMode(sr.Mode),
		ModTime: modTime,
		IsDir:   sr.IsDir,
	}, nil
}

func (rs *RemoteSandbox) ReadDir(ctx context.Context, path, userID string) ([]DirEntry, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}
	reqBody, _ := json.Marshal(ReadDirRequest{Path: path})
	msg := &RunnerMessage{ID: generateID(), Type: ProtoReadDir, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		return nil, fmt.Errorf("read_dir: %s", e.Message)
	}
	var de DirEntriesResponse
	if err := json.Unmarshal(resp.Body, &de); err != nil {
		return nil, fmt.Errorf("unmarshal dir entries: %w", err)
	}
	entries := make([]DirEntry, len(de.Entries))
	for i, e := range de.Entries {
		entries[i] = DirEntry{Name: e.Name, IsDir: e.IsDir, Size: e.Size} //nolint:staticcheck
	}
	return entries, nil
}

func (rs *RemoteSandbox) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, _ := json.Marshal(PathRequest{Path: path, Perm: int(perm)})
	msg := &RunnerMessage{ID: generateID(), Type: ProtoMkdirAll, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		return fmt.Errorf("mkdir_all: %s", e.Message)
	}
	return nil
}

func (rs *RemoteSandbox) Remove(ctx context.Context, path, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, _ := json.Marshal(PathRequest{Path: path})
	msg := &RunnerMessage{ID: generateID(), Type: ProtoRemove, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		return fmt.Errorf("remove: %s", e.Message)
	}
	return nil
}

func (rs *RemoteSandbox) RemoveAll(ctx context.Context, path, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, _ := json.Marshal(PathRequest{Path: path})
	msg := &RunnerMessage{ID: generateID(), Type: ProtoRemoveAll, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		json.Unmarshal(resp.Body, &e)
		return fmt.Errorf("remove_all: %s", e.Message)
	}
	return nil
}
