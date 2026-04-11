package tools

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"xbot/internal/runnerproto"
	"xbot/llm"
	log "xbot/logger"
)

// RemoteSandboxConfig holds configuration for creating a RemoteSandbox.
type RemoteSandboxConfig struct {
	Addr           string            // WebSocket listen address (e.g., "0.0.0.0:8080")
	AuthToken      string            // Authentication token for runners
	AllowedOrigins []string          // Allowed WebSocket origins (empty = allow all, for development)
	TokenStore     *RunnerTokenStore // Per-user token store (optional, for per-user tokens)
}

// RemoteSandboxSyncConfig holds directories to sync to runners on registration.
type RemoteSandboxSyncConfig struct {
	GlobalSkillDirs []string // Global skill directories (server-side)
	AgentsDir       string   // Global agents directory (server-side)
}

// runnerConnection represents a connected xbot-runner instance.
// All writes to the WebSocket go through sendCh, consumed by writePump.
type runnerConnection struct {
	wsConn     *websocket.Conn
	userID     string
	runnerName string // runner name from DB
	workspace  string
	shell      string         // runner's default shell (e.g. /bin/bash)
	sendCh     chan sendEntry // buffered channel for serialized writes
	done       chan struct{}  // closed when writePump exits
}

// userRunnersEntry holds all runner connections for a single user.
type userRunnersEntry struct {
	mu      sync.RWMutex
	runners map[string]*runnerConnection // runnerName → conn
	active  string                       // currently active runnerName
}

// sendEntry represents a write to be sent by writePump.
type sendEntry struct {
	data []byte // TextMessage payload (nil for control-only entries)
	err  chan error
}

// RemoteSandbox implements the Sandbox interface via WebSocket communication
// with xbot-runner instances running on users' machines.
type RemoteSandbox struct {
	connections          sync.Map // userID → *userRunnersEntry
	wsServer             *http.Server
	authToken            string
	addr                 string
	tokenStore           *RunnerTokenStore
	pendingMu            sync.Mutex
	pending              map[string]chan *RunnerMessage // request ID → response channel
	upgrader             websocket.Upgrader             // per-instance upgrader with origin check
	globalSkillDirs      []string                       // global skill dirs to sync to runner on registration
	agentsDir            string                         // global agents dir to sync to runner on registration
	syncMu               sync.Mutex
	synced               map[string]bool // userID → whether initial sync has completed
	syncing              map[string]bool // userID → sync in progress (prevent concurrent syncs)
	stdioMu              sync.Mutex
	stdioStreams         map[string]*stdioStream // streamID → active stdio stream
	OnRunnerStatusChange func(userID, runnerName string, online bool)
	OnSyncProgress       func(userID string, phase string, message string)
}

// NewRemoteSandbox creates and starts a RemoteSandbox server.
func NewRemoteSandbox(cfg RemoteSandboxConfig, syncCfg RemoteSandboxSyncConfig) (*RemoteSandbox, error) {
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
		authToken:  cfg.AuthToken,
		addr:       cfg.Addr,
		tokenStore: cfg.TokenStore,
		pending:    make(map[string]chan *RunnerMessage),
		upgrader: websocket.Upgrader{
			CheckOrigin: checkOrigin,
		},
		globalSkillDirs: syncCfg.GlobalSkillDirs,
		agentsDir:       syncCfg.AgentsDir,
		synced:          make(map[string]bool),
		syncing:         make(map[string]bool),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", rs.handleWebSocket)

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
// The URL path must be /ws/{userID} — the userID is bound to this connection,
// preventing a token-holder from registering as a different user.
func (rs *RemoteSandbox) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Extract userID from URL path: /ws/{userID}
	pathUserID := strings.TrimPrefix(r.URL.Path, "/ws/")
	if pathUserID == "" || pathUserID == r.URL.Path {
		http.Error(w, "user ID required in URL path (/ws/{userID})", http.StatusBadRequest)
		return
	}

	conn, err := rs.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("WebSocket upgrade failed")
		return
	}

	// Set up ping/pong keep-alive.
	const (
		pongWait   = 60 * time.Second
		pingPeriod = 30 * time.Second
		writeWait  = 10 * time.Second
	)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	// Reset read deadline when we receive a pong (response to our pings).
	// Do NOT set SetPingHandler — the default auto-replies pong to the runner's pings.
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

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
	authenticated := (rs.tokenStore != nil && rs.tokenStore.Validate(reg.AuthToken, reg.UserID)) ||
		(rs.authToken != "" && subtle.ConstantTimeCompare([]byte(reg.AuthToken), []byte(rs.authToken)) == 1)
	if !authenticated {
		log.WithFields(log.Fields{
			"user_id":    reg.UserID,
			"has_store":  rs.tokenStore != nil,
			"has_global": rs.authToken != "",
		}).Warn("Runner authentication failed")
		rs.sendRegisterError(conn, "AUTH_FAILED", "authentication failed")
		return
	}
	if reg.UserID == "" {
		log.Warn("Runner registration missing user_id")
		rs.sendRegisterError(conn, "INVALID", "missing user_id")
		return
	}
	// S6: Bind token to userID — the URL path determines identity, not the claim.
	if reg.UserID != pathUserID {
		log.WithFields(log.Fields{
			"path_user_id": pathUserID,
			"claimed_id":   reg.UserID,
		}).Warn("Runner userID mismatch (potential impersonation)")
		rs.sendRegisterError(conn, "FORBIDDEN", "user_id mismatch")
		return
	}

	shell := reg.Shell
	if shell == "" {
		shell = "/bin/sh"
	}

	// Look up runner name from token.
	runnerName := ""
	if rs.tokenStore != nil {
		if uid, rname, err := rs.tokenStore.FindByToken(reg.AuthToken); err == nil && uid == reg.UserID {
			runnerName = rname
		} else {
			// Fallback: check legacy runner_tokens table for backward compat.
			uid := rs.tokenStore.FindByTokenInRunnerTokens(reg.AuthToken)
			if uid == reg.UserID {
				// Legacy token: use "default" as name.
				runnerName = "default"
			}
		}
	}
	if runnerName == "" {
		runnerName = "default"
	}

	rc := &runnerConnection{
		wsConn:     conn,
		userID:     reg.UserID,
		runnerName: runnerName,
		workspace:  reg.Workspace,
		shell:      shell,
		sendCh:     make(chan sendEntry, 64),
		done:       make(chan struct{}),
	}

	// Register connection in userRunnersEntry.
	newEntry := &userRunnersEntry{
		runners: map[string]*runnerConnection{runnerName: rc},
		active:  runnerName,
	}
	actual, loaded := rs.connections.LoadOrStore(reg.UserID, newEntry)
	entry, ok := actual.(*userRunnersEntry)
	if !ok {
		log.WithField("user_id", reg.UserID).Error("invalid runner connection type")
		conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","body":{"code":"internal_error","message":"invalid connection state"}}`))
		conn.Close()
		return
	}
	if loaded {
		entry.mu.Lock()
		entry.runners[runnerName] = rc
		if entry.active == "" {
			entry.active = runnerName
		}
		entry.mu.Unlock()
	}

	defer func() {
		// On disconnect, remove this runner from the entry.
		entry.mu.Lock()
		delete(entry.runners, runnerName)
		// If the disconnected runner was active, fallback to first available.
		if entry.active == runnerName {
			for name := range entry.runners {
				entry.active = name
				break
			}
			if entry.active == "" {
				// No runners left — remove entry entirely.
				entry.mu.Unlock()
				rs.connections.Delete(reg.UserID)
				if rs.OnRunnerStatusChange != nil {
					go rs.OnRunnerStatusChange(reg.UserID, runnerName, false)
				}
				return
			}
		}
		if rs.OnRunnerStatusChange != nil {
			go rs.OnRunnerStatusChange(reg.UserID, runnerName, false)
		}
		entry.mu.Unlock()
	}()

	// Send registration acknowledgment
	okBody, err := json.Marshal(map[string]string{"status": "ok"})
	if err != nil {
		log.WithError(err).Error("Failed to marshal register_ok body")
		conn.Close()
		return
	}
	okMsg, err := json.Marshal(RunnerMessage{Type: "register_ok", Body: okBody})
	if err != nil {
		log.WithError(err).Error("Failed to marshal register_ok message")
		conn.Close()
		return
	}
	conn.WriteMessage(websocket.TextMessage, okMsg)

	log.WithFields(log.Fields{
		"user_id":     reg.UserID,
		"runner_name": runnerName,
		"workspace":   reg.Workspace,
	}).Info("Runner connected")

	// If runner declares LLM capability, update DB record (for injectProxyLLM to query)
	if reg.LLMProvider != "" && rs.tokenStore != nil {
		rs.tokenStore.UpdateRunnerLLM(reg.UserID, runnerName, RunnerLLMSettings{
			Provider: reg.LLMProvider,
			Model:    reg.LLMModel,
			// APIKey and BaseURL are not needed here — runner holds them locally
		})
		log.WithFields(log.Fields{
			"user_id":      reg.UserID,
			"runner_name":  runnerName,
			"llm_provider": reg.LLMProvider,
			"llm_model":    reg.LLMModel,
		}).Info("Runner LLM capability recorded")
	}

	// Notify runner status change
	if rs.OnRunnerStatusChange != nil {
		go rs.OnRunnerStatusChange(reg.UserID, runnerName, true)
	}

	// Single writer goroutine: handles both request writes and ping heartbeats.
	go rs.writePump(rc, pingPeriod, writeWait)

	// Sync global skills and agents to the runner in the background
	go rs.syncToRunner(reg.UserID, reg.Workspace)

	// Keep reading messages (responses, heartbeats, and stdio push messages)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"user_id":     reg.UserID,
				"runner_name": runnerName,
			}).Debug("Runner disconnected")
			return
		}
		var resp RunnerMessage
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		// Handle stdio push messages (stdio_data, stdio_exit) before request matching.
		if rs.handleStdioPush(&resp) {
			continue
		}
		if resp.ID != "" {
			rs.pendingMu.Lock()
			if ch, ok := rs.pending[resp.ID]; ok {
				select {
				case ch <- &resp:
				default:
					log.WithField("request_id", resp.ID).Warn("Runner: pending channel full, response dropped")
				}
				delete(rs.pending, resp.ID)
			}
			rs.pendingMu.Unlock()
		}
	}

}

// getRunner returns the active connection for a user, or an error.
func (rs *RemoteSandbox) getRunner(userID string) (*runnerConnection, error) {
	val, ok := rs.connections.Load(userID)
	if !ok {
		return nil, fmt.Errorf("no runner connected for user %q", userID)
	}
	entry, ok := val.(*userRunnersEntry)
	if !ok {
		return nil, fmt.Errorf("invalid runner connection type for user %q", userID)
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	if entry.active == "" || len(entry.runners) == 0 {
		return nil, fmt.Errorf("no active runner for user %q", userID)
	}
	rc, ok := entry.runners[entry.active]
	if !ok {
		return nil, fmt.Errorf("active runner %q not connected for user %q", entry.active, userID)
	}
	return rc, nil
}

// HasUser reports whether the given user has any active runner connection.
// Used by SandboxRouter for per-user routing decisions.
func (rs *RemoteSandbox) HasUser(userID string) bool {
	val, ok := rs.connections.Load(userID)
	if !ok {
		return false
	}
	entry, ok := val.(*userRunnersEntry)
	if !ok {
		return false
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	return len(entry.runners) > 0
}

// IsRunnerOnline reports whether a specific named runner is connected for the user.
func (rs *RemoteSandbox) IsRunnerOnline(userID, runnerName string) bool {
	val, ok := rs.connections.Load(userID)
	if !ok {
		return false
	}
	entry, ok := val.(*userRunnersEntry)
	if !ok {
		return false
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	_, ok = entry.runners[runnerName]
	return ok
}

// GetConnectionInfo returns the actual workspace and shell reported by the runner's connection.
// Returns empty strings if the runner is not connected.
func (rs *RemoteSandbox) GetConnectionInfo(userID, runnerName string) (workspace, shell string) {
	val, ok := rs.connections.Load(userID)
	if !ok {
		return "", ""
	}
	entry, ok := val.(*userRunnersEntry)
	if !ok {
		return "", ""
	}
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	rc, ok := entry.runners[runnerName]
	if !ok {
		return "", ""
	}
	return rc.workspace, rc.shell
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

	// Send through the single writer goroutine.
	errCh := make(chan error, 1)
	select {
	case rc.sendCh <- sendEntry{data: data, err: errCh}:
	case <-rc.done:
		return nil, fmt.Errorf("runner disconnected")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if err = <-errCh; err != nil {
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

// sendOnly sends a message to the runner without waiting for a response.
func (rs *RemoteSandbox) sendOnly(rc *runnerConnection, msg *RunnerMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	errCh := make(chan error, 1)
	select {
	case rc.sendCh <- sendEntry{data: data, err: errCh}:
	case <-rc.done:
		return fmt.Errorf("runner disconnected")
	}
	return <-errCh
}

// writePump is the single writer goroutine for a runner connection.
// It consumes entries from rc.sendCh (text messages from sendRequest) and sends
// periodic pings to detect dead connections.
func (rs *RemoteSandbox) writePump(rc *runnerConnection, pingPeriod, writeWait time.Duration) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		rc.wsConn.Close()
		close(rc.done)
	}()

	for {
		select {
		case entry := <-rc.sendCh:
			if entry.data != nil {
				err := rc.wsConn.WriteMessage(websocket.TextMessage, entry.data)
				if entry.err != nil {
					entry.err <- err
				}
				if err != nil {
					return
				}
			}
		case <-ticker.C:
			if err := rc.wsConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				log.WithError(err).WithField("user_id", rc.userID).Debug("Ping to runner failed")
				return
			}
		}
	}
}

// sendRegisterError sends a registration error to the runner and closes the connection.
func (rs *RemoteSandbox) sendRegisterError(conn *websocket.Conn, code, message string) {
	errBody, err := json.Marshal(ErrorResponse{Code: code, Message: message})
	if err != nil {
		log.WithError(err).Error("Failed to marshal error response")
		conn.Close()
		return
	}
	errMsg, err := json.Marshal(RunnerMessage{Type: "error", Body: errBody})
	if err != nil {
		log.WithError(err).Error("Failed to marshal error message")
		conn.Close()
		return
	}
	conn.WriteMessage(websocket.TextMessage, errMsg)
	conn.Close()
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

// SetTokenStore sets or replaces the token store.
func (rs *RemoteSandbox) SetTokenStore(store *RunnerTokenStore) {
	rs.tokenStore = store
}

// Workspace returns the runner's workspace root directory for the given user.
// Returns empty string if the runner is not connected or hasn't reported a workspace.
func (rs *RemoteSandbox) Workspace(userID string) string {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return ""
	}
	return rc.workspace
}

func (rs *RemoteSandbox) Close() error {
	return rs.wsServer.Close()
}

func (rs *RemoteSandbox) CloseForUser(userID string) error {
	val, ok := rs.connections.Load(userID)
	if !ok {
		return nil
	}
	entry, ok := val.(*userRunnersEntry)
	if !ok {
		return nil
	}
	entry.mu.Lock()
	for _, rc := range entry.runners {
		rc.wsConn.Close()
	}
	entry.runners = make(map[string]*runnerConnection)
	entry.active = ""
	entry.mu.Unlock()
	rs.connections.Delete(userID)
	return nil
}

// DisconnectRunner closes a specific runner connection by name.
func (rs *RemoteSandbox) DisconnectRunner(userID, runnerName string) bool {
	val, ok := rs.connections.Load(userID)
	if !ok {
		return false
	}
	entry, ok := val.(*userRunnersEntry)
	if !ok {
		return false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	rc, ok := entry.runners[runnerName]
	if !ok {
		return false
	}
	rc.wsConn.Close()
	delete(entry.runners, runnerName)
	// If active was this runner, fallback to first available
	if entry.active == runnerName {
		entry.active = ""
		for name := range entry.runners {
			entry.active = name
			break
		}
	}
	if len(entry.runners) == 0 {
		rs.connections.Delete(userID)
	}
	return true
}

func (rs *RemoteSandbox) IsExporting(_ string) bool      { return false }
func (rs *RemoteSandbox) ExportAndImport(_ string) error { return nil }
func (rs *RemoteSandbox) GetShell(userID string, _ string) (string, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return "/bin/sh", nil
	}
	return rc.shell, nil
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

	reqBody, err := json.Marshal(ExecRequest{
		Command: spec.Command,
		Args:    spec.Args,
		Shell:   spec.Shell,
		Dir:     spec.Dir,
		Env:     spec.Env,
		Stdin:   spec.Stdin,
		Timeout: int(timeout / time.Second),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

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
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return nil, fmt.Errorf("exec error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return nil, fmt.Errorf("exec error (raw: %s)", string(resp.Body))
		}
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

// ExecBg starts a background command on the runner.
// Returns immediately with the task ID — the command runs asynchronously on the runner.
func (rs *RemoteSandbox) ExecBg(ctx context.Context, spec ExecSpec, taskID string) error {
	rc, err := rs.getRunner(spec.UserID)
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(runnerproto.BgExecRequest{
		TaskID:  taskID,
		Command: spec.Command,
		Args:    spec.Args,
		Shell:   spec.Shell,
		Dir:     spec.Dir,
		Env:     spec.Env,
		Stdin:   spec.Stdin,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoBgExec,
		UserID: spec.UserID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}

	if resp.Type == runnerproto.ProtoError {
		var e runnerproto.ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return fmt.Errorf("bg_exec error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return fmt.Errorf("bg_exec error (raw: %s)", string(resp.Body))
		}
		return fmt.Errorf("bg_exec error: %s", e.Message)
	}

	return nil
}

// KillBg kills a background task on the runner.
func (rs *RemoteSandbox) KillBg(ctx context.Context, userID, taskID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(runnerproto.BgKillRequest{TaskID: taskID})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoBgKill,
		UserID: userID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}

	if resp.Type == runnerproto.ProtoError {
		var e runnerproto.ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return fmt.Errorf("bg_kill error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return fmt.Errorf("bg_kill error (raw: %s)", string(resp.Body))
		}
		return fmt.Errorf("bg_kill error: %s", e.Message)
	}

	return nil
}

// StatusBg queries the current status and output of a background task on the runner.
func (rs *RemoteSandbox) StatusBg(ctx context.Context, userID, taskID string) (*RemoteBgTaskStatus, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(runnerproto.BgStatusRequest{TaskID: taskID})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoBgStatus,
		UserID: userID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}

	if resp.Type == runnerproto.ProtoError {
		var e runnerproto.ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return nil, fmt.Errorf("bg_status error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return nil, fmt.Errorf("bg_status error (raw: %s)", string(resp.Body))
		}
		return nil, fmt.Errorf("bg_status error: %s", e.Message)
	}

	var result runnerproto.BgOutputResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal bg_status result: %w", err)
	}

	return &RemoteBgTaskStatus{
		TaskID:   result.TaskID,
		Status:   result.Status,
		ExitCode: result.ExitCode,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
	}, nil
}

// RemoteBgTaskStatus holds the status snapshot of a remote background task.
type RemoteBgTaskStatus struct {
	TaskID   string
	Status   string // "running", "completed", "failed", "killed"
	ExitCode int
	Stdout   string
	Stderr   string
}

// LLMGenerate sends an LLM generation request to the runner and returns the response.
// This is used by ProxyLLM to forward LLM calls to runners with local LLM configured.
func (rs *RemoteSandbox) LLMGenerate(ctx context.Context, userID, model string, messages []llm.ChatMessage, tools []llm.ToolDefinition, thinkingMode string) (*llm.LLMResponse, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(llm.LLMProxyRequest{
		Model:        model,
		Messages:     messages,
		Tools:        llm.SerializeTools(tools),
		ThinkingMode: thinkingMode,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoLLMGenerate,
		UserID: userID,
		Body:   reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, llm.ProxyRequestTimeout)
	if err != nil {
		return nil, err
	}

	if resp.Type == runnerproto.ProtoError {
		var e runnerproto.ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return nil, fmt.Errorf("llm_generate error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return nil, fmt.Errorf("llm_generate error (raw: %s)", string(resp.Body))
		}
		return nil, fmt.Errorf("llm_generate error: %s", e.Message)
	}

	var result llm.LLMResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal llm_generate result: %w", err)
	}

	return &result, nil
}

// LLMModels queries available models from the runner's local LLM.
func (rs *RemoteSandbox) LLMModels(ctx context.Context, userID string) ([]string, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}

	msg := &RunnerMessage{
		ID:     generateID(),
		Type:   runnerproto.ProtoLLMModels,
		UserID: userID,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}

	if resp.Type == runnerproto.ProtoError {
		var e runnerproto.ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return nil, fmt.Errorf("llm_models error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return nil, fmt.Errorf("llm_models error (raw: %s)", string(resp.Body))
		}
		return nil, fmt.Errorf("llm_models error: %s", e.Message)
	}

	var result llm.LLMListModelsResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal llm_models result: %w", err)
	}

	return result.Models, nil
}

func (rs *RemoteSandbox) ReadFile(ctx context.Context, path, userID string) ([]byte, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(ReadFileRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoReadFile, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return nil, fmt.Errorf("read file error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Code == "ENOENT" {
			return nil, os.ErrNotExist
		}
		if e.Message == "" {
			return nil, fmt.Errorf("read file error (raw: %s)", string(resp.Body))
		}
		return nil, fmt.Errorf("read file: %s", e.Message)
	}
	var fc FileContentResponse
	if err := json.Unmarshal(resp.Body, &fc); err != nil {
		return nil, fmt.Errorf("unmarshal file content: %w", err)
	}
	return base64.StdEncoding.DecodeString(fc.Data)
}

func (rs *RemoteSandbox) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(WriteFileRequest{
		Path: path,
		Data: base64.StdEncoding.EncodeToString(data),
		Perm: int(perm),
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoWriteFile, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return fmt.Errorf("write file error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return fmt.Errorf("write file error (raw: %s)", string(resp.Body))
		}
		return fmt.Errorf("write file: %s", e.Message)
	}
	return nil
}

func (rs *RemoteSandbox) Stat(ctx context.Context, path, userID string) (*SandboxFileInfo, error) {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return nil, err
	}
	reqBody, err := json.Marshal(StatRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoStat, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return nil, fmt.Errorf("stat error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Code == "ENOENT" {
			return nil, os.ErrNotExist
		}
		if e.Message == "" {
			return nil, fmt.Errorf("stat error (raw: %s)", string(resp.Body))
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
	reqBody, err := json.Marshal(ReadDirRequest{Path: path})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoReadDir, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return nil, fmt.Errorf("read_dir error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return nil, fmt.Errorf("read_dir error (raw: %s)", string(resp.Body))
		}
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
	reqBody, err := json.Marshal(PathRequest{Path: path, Perm: int(perm)})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoMkdirAll, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return fmt.Errorf("mkdir_all error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return fmt.Errorf("mkdir_all error (raw: %s)", string(resp.Body))
		}
		return fmt.Errorf("mkdir_all: %s", e.Message)
	}
	return nil
}

func (rs *RemoteSandbox) Remove(ctx context.Context, path, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(PathRequest{Path: path})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoRemove, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return fmt.Errorf("remove error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return fmt.Errorf("remove error (raw: %s)", string(resp.Body))
		}
		return fmt.Errorf("remove: %s", e.Message)
	}
	return nil
}

func (rs *RemoteSandbox) RemoveAll(ctx context.Context, path, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(PathRequest{Path: path})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoRemoveAll, UserID: userID, Body: reqBody}
	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return fmt.Errorf("remove_all error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return fmt.Errorf("remove_all error (raw: %s)", string(resp.Body))
		}
		return fmt.Errorf("remove_all: %s", e.Message)
	}
	return nil
}

func (rs *RemoteSandbox) DownloadFile(ctx context.Context, url, outputPath, userID string) error {
	rc, err := rs.getRunner(userID)
	if err != nil {
		return err
	}
	reqBody, err := json.Marshal(DownloadFileRequest{
		URL:        url,
		OutputPath: outputPath,
	})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	msg := &RunnerMessage{ID: generateID(), Type: ProtoDownloadFile, UserID: userID, Body: reqBody}
	// 5-minute timeout for downloads
	resp, err := rs.sendRequest(ctx, rc, msg, 5*time.Minute)
	if err != nil {
		return err
	}
	if resp.Type == ProtoError {
		var e ErrorResponse
		if err := json.Unmarshal(resp.Body, &e); err != nil {
			return fmt.Errorf("download_file error (raw: %s): unmarshal failed: %w", string(resp.Body), err)
		}
		if e.Message == "" {
			return fmt.Errorf("download_file error (raw: %s)", string(resp.Body))
		}
		return fmt.Errorf("download_file: %s", e.Message)
	}
	return nil
}

// === Runner sync (server → runner file sync on registration) ===

// syncToRunner syncs global skills and agents from the server to the runner.
// Runs in a background goroutine; errors are logged but not fatal.
func (rs *RemoteSandbox) syncToRunner(userID, workspace string) {
	if workspace == "" {
		log.WithField("user_id", userID).Warn("syncToRunner: workspace is empty, skipping sync")
		return
	}

	rs.syncMu.Lock()
	rs.syncing[userID] = true
	rs.syncMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	log.WithFields(log.Fields{
		"user_id":           userID,
		"workspace":         workspace,
		"global_skill_dirs": rs.globalSkillDirs,
		"agents_dir":        rs.agentsDir,
	}).Info("syncToRunner: starting sync")

	// Notify sync start
	if rs.OnSyncProgress != nil {
		rs.OnSyncProgress(userID, "start", "正在同步 skills 和 agents...")
	}

	// Sync each global skill directory
	for _, skillDir := range rs.globalSkillDirs {
		dstDir := filepath.Join(workspace, "skills")
		rs.syncDirToRunner(ctx, userID, workspace, skillDir, dstDir)
	}

	// Sync embedded skills (skipped if external version already exists)
	dstSkillsDir := filepath.Join(workspace, "skills")
	for _, name := range ListEmbeddedSkills() {
		rs.syncEmbeddedSkillToRunner(ctx, userID, workspace, name, dstSkillsDir)
	}

	// Sync global agents
	if rs.agentsDir != "" {
		dstDir := filepath.Join(workspace, "agents")
		rs.syncAgentsToRunner(ctx, userID, workspace, rs.agentsDir, dstDir)
	}

	// Sync embedded agents (skipped if external version already exists)
	dstAgentsDir := filepath.Join(workspace, "agents")
	for _, name := range ListEmbeddedAgents() {
		rs.syncEmbeddedAgentToRunner(ctx, userID, workspace, name, dstAgentsDir)
	}

	log.WithFields(log.Fields{
		"user_id":   userID,
		"workspace": workspace,
	}).Info("Runner sync completed")

	// Notify sync done
	if rs.OnSyncProgress != nil {
		rs.OnSyncProgress(userID, "done", "同步完成")
	}
	// Mark sync as completed (even if some dirs failed, we don't retry individual failures)
	rs.syncMu.Lock()
	rs.synced[userID] = true
	rs.syncing[userID] = false
	rs.syncMu.Unlock()
}

// EnsureSynced implements SandboxSyncer interface.
// If the runner hasn't been synced yet (or sync failed), triggers a sync.
// This is called from EnsureSynced(ctx) in skill_sync.go.
func (rs *RemoteSandbox) EnsureSynced(ctx context.Context, userID string) {
	rs.syncMu.Lock()
	if rs.synced[userID] {
		rs.syncMu.Unlock()
		return
	}
	// If sync is already in progress, wait for it
	if rs.syncing[userID] {
		rs.syncMu.Unlock()
		// Poll every 500ms, up to 30s
		for i := 0; i < 60; i++ {
			time.Sleep(500 * time.Millisecond)
			rs.syncMu.Lock()
			if rs.synced[userID] {
				rs.syncMu.Unlock()
				return
			}
			rs.syncMu.Unlock()
		}
		log.WithField("user_id", userID).Warn("EnsureSynced: timed out waiting for in-progress sync")
		return
	}
	rs.syncMu.Unlock()

	// Get runner workspace
	rc, err := rs.getRunner(userID)
	if err != nil {
		log.WithError(err).WithField("user_id", userID).Debug("EnsureSynced: no runner connected, skipping sync")
		return
	}

	log.WithField("user_id", userID).Info("EnsureSynced: triggering on-demand sync")
	go rs.syncToRunner(userID, rc.workspace)
}

// syncDirToRunner recursively syncs a skill directory tree from the server to the runner.
// Each skill is a subdirectory; only directories containing SKILL.md are synced.
func (rs *RemoteSandbox) syncDirToRunner(ctx context.Context, userID, workspace, srcDir, dstSubdir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.WithError(err).WithField("dir", srcDir).Warn("syncToRunner: failed to read source dir")
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(srcDir, e.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			continue // not a valid skill (no SKILL.md)
		}
		dstDir := filepath.Join(dstSubdir, e.Name())
		rs.syncTreeToRunner(ctx, userID, skillDir, dstDir)
	}
}

// syncAgentsToRunner syncs .md agent files from the server's agents dir to the runner.
func (rs *RemoteSandbox) syncAgentsToRunner(ctx context.Context, userID, workspace, srcDir, dstSubdir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.WithError(err).WithField("dir", srcDir).Warn("syncToRunner: failed to read agents dir")
		return
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(dstSubdir, e.Name())
		rs.syncFileToRunner(ctx, userID, srcPath, dstPath)
	}
}

// syncTreeToRunner recursively syncs a directory from the server to the runner.
func (rs *RemoteSandbox) syncTreeToRunner(ctx context.Context, userID, srcDir, dstDir string) {
	if err := rs.MkdirAll(ctx, dstDir, 0o755, userID); err != nil {
		log.WithError(err).WithFields(log.Fields{"src": srcDir, "dst": dstDir}).Warn("syncTree: mkdir failed")
		return
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		log.WithError(err).WithField("dir", srcDir).Warn("syncTree: read failed")
		return
	}

	for _, e := range entries {
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(dstDir, e.Name())
		if e.IsDir() {
			rs.syncTreeToRunner(ctx, userID, srcPath, dstPath)
		} else {
			rs.syncFileToRunner(ctx, userID, srcPath, dstPath)
		}
	}
}

// syncFileToRunner reads a local file and writes it to the runner.
func (rs *RemoteSandbox) syncFileToRunner(ctx context.Context, userID, srcPath, dstPath string) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		log.WithError(err).WithField("file", srcPath).Warn("syncFile: read failed")
		return
	}
	if err := rs.WriteFile(ctx, dstPath, data, 0o644, userID); err != nil {
		log.WithError(err).WithFields(log.Fields{"src": srcPath, "dst": dstPath}).Warn("syncFile: write failed")
	}
}

// syncEmbeddedSkillToRunner syncs a single embedded skill to the runner.
// Skips if the skill directory already exists on the runner.
func (rs *RemoteSandbox) syncEmbeddedSkillToRunner(ctx context.Context, userID, workspace, skillName, dstSkillsDir string) {
	dstDir := filepath.Join(dstSkillsDir, skillName)
	// Check if already exists on runner
	if _, err := rs.Stat(ctx, dstDir, userID); err == nil {
		return // already exists
	}
	entries, err := fs.ReadDir(EmbeddedSkills, path.Join("embed_skills", skillName))
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := ReadEmbeddedSkillFile(skillName, e.Name())
		if err != nil {
			continue
		}
		dstPath := filepath.Join(dstDir, e.Name())
		if err := rs.MkdirAll(ctx, dstDir, 0o755, userID); err != nil {
			log.WithError(err).Warn("syncEmbeddedSkill: mkdir failed")
			return
		}
		if err := rs.WriteFile(ctx, dstPath, data, 0o644, userID); err != nil {
			log.WithError(err).Warn("syncEmbeddedSkill: write failed")
		}
	}
}

// syncEmbeddedAgentToRunner syncs a single embedded agent to the runner.
// Skips if the agent file already exists on the runner.
func (rs *RemoteSandbox) syncEmbeddedAgentToRunner(ctx context.Context, userID, workspace, agentName, dstAgentsDir string) {
	dstPath := filepath.Join(dstAgentsDir, agentName+".md")
	// Check if already exists on runner
	if _, err := rs.Stat(ctx, dstPath, userID); err == nil {
		return // already exists
	}
	data, err := ReadEmbeddedAgentFile(agentName)
	if err != nil {
		return
	}
	if err := rs.MkdirAll(ctx, dstAgentsDir, 0o755, userID); err != nil {
		log.WithError(err).Warn("syncEmbeddedAgent: mkdir failed")
		return
	}
	if err := rs.WriteFile(ctx, dstPath, data, 0o644, userID); err != nil {
		log.WithError(err).Warn("syncEmbeddedAgent: write failed")
	}
}
