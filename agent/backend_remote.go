package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/agent/hooks"
	"xbot/bus"
	"xbot/channel"
	"xbot/clipanic"
	"xbot/config"
	"xbot/event"
	llm "xbot/llm"
	"xbot/session"
	"xbot/tools"

	"github.com/gorilla/websocket"
	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// RPC protocol types (shared between RemoteBackend client and server handler)
// ---------------------------------------------------------------------------

// rpcResponse is sent by the server back to the CLI client.
type rpcResponse struct {
	Type   string          `json:"type"`             // "rpc_response"
	ID     string          `json:"id"`               // matches request ID
	Result json.RawMessage `json:"result,omitempty"` // JSON result (nil for void methods)
	Error  string          `json:"error,omitempty"`  // error message (empty = success)
}

// ---------------------------------------------------------------------------
// RemoteBackend — full-featured RPC client over WebSocket
// ---------------------------------------------------------------------------

// RemoteBackend connects to a remote xbot server via WebSocket.
// All AgentBackend methods are forwarded as RPC requests; the server
// dispatches them to its LocalBackend. Streaming progress events are
// pushed from server to client via dedicated WS message types.
type RemoteBackend struct {
	serverURL string
	token     string

	// WS connection
	conn      *websocket.Conn
	connMu    sync.Mutex
	done      chan struct{}
	closeOnce sync.Once

	// readPump lifecycle — WaitGroup ensures old readPump exits
	// before reconnect spawns a new one, preventing goroutine leaks.
	readPumpWg sync.WaitGroup

	// Event seq tracking — tracks the highest seq from server events
	// so that on reconnect we send last_seq and only replay missed events.
	lastSeq atomic.Uint64

	// Outbound message callback (for final agent replies)
	outboundMu sync.RWMutex
	outboundCb func(bus.OutboundMessage)

	// Progress callback (for streaming + structured progress)
	progressMu sync.RWMutex
	progressCb func(*channel.CLIProgressPayload)

	// Reconnect
	reconnectCh   chan struct{}
	onReconnectCb func() // called after successful reconnect (for history reload)

	// Connection state — tracks WS liveness for CLI header bar indicator
	connState     string // "connected" | "disconnected" | "reconnecting"
	onConnStateCb func(state string)

	// RPC pending calls: requestID → response channel
	rpcMu      sync.Mutex
	pending    map[string]chan *rpcResponse
	rpcCounter atomic.Int64
}

// RemoteBackendConfig holds the configuration for connecting to a remote server.
type RemoteBackendConfig struct {
	ServerURL string // e.g. "ws://localhost:8080" or "wss://example.com"
	Token     string // runner token for authentication
}

// NewRemoteBackend creates a RemoteBackend that connects to the given server URL.
func NewRemoteBackend(cfg RemoteBackendConfig) *RemoteBackend {
	return &RemoteBackend{
		serverURL:   cfg.ServerURL,
		token:       cfg.Token,
		done:        make(chan struct{}),
		reconnectCh: make(chan struct{}, 1),
		pending:     make(map[string]chan *rpcResponse),
	}
}

// ---------------------------------------------------------------------------
// WS incoming message types (server → client)
// ---------------------------------------------------------------------------

// wsIncomingMessage represents a message received from the server.
// Supports all message types: text, progress_structured, stream_content, rpc_response, ask_user.
type wsIncomingMessage struct {
	Type            string                     `json:"type"`
	ID              string                     `json:"id,omitempty"`
	Content         string                     `json:"content,omitempty"`
	OriginalContent string                     `json:"original_content,omitempty"`
	TS              int64                      `json:"ts,omitempty"`
	Seq             uint64                     `json:"seq,omitempty"`
	Progress        *channel.WsProgressPayload `json:"progress,omitempty"`
	ProgressHistory string                     `json:"progress_history,omitempty"`
	Result          json.RawMessage            `json:"result,omitempty"`
	Error           string                     `json:"error,omitempty"`
	Channel         string                     `json:"channel,omitempty"`
	ChatID          string                     `json:"chat_id,omitempty"`
}

// wsOutgoingMessage represents a message sent to the server.
type wsOutgoingMessage struct {
	Type       string          `json:"type"`
	Content    string          `json:"content,omitempty"`
	ID         string          `json:"id,omitempty"`
	Method     string          `json:"method,omitempty"`
	Params     json.RawMessage `json:"params,omitempty"`
	Channel    string          `json:"channel,omitempty"`
	ChatID     string          `json:"chat_id,omitempty"`
	SenderID   string          `json:"sender_id,omitempty"`
	SenderName string          `json:"sender_name,omitempty"`
	ChatType   string          `json:"chat_type,omitempty"`
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start connects to the remote server via WebSocket and starts the read pump.
func (b *RemoteBackend) Start(ctx context.Context) error {
	if err := b.connect(ctx); err != nil {
		return fmt.Errorf("connect to %s: %w", b.serverURL, err)
	}
	b.readPumpWg.Add(1)
	go b.readPump(ctx)
	go b.reconnectLoop(ctx)
	go b.pingLoop(ctx)
	return nil
}

// Stop closes the WebSocket connection.
func (b *RemoteBackend) Stop() {
	b.closeOnce.Do(func() {
		close(b.done)
		b.connMu.Lock()
		if b.conn != nil {
			b.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			b.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "client shutdown"))
			b.conn.Close()
			b.conn = nil
		}
		b.connMu.Unlock()
		// Unblock all pending RPC calls (non-blocking write, consistent with readPump)
		b.rpcMu.Lock()
		for id, ch := range b.pending {
			select {
			case ch <- &rpcResponse{Error: "connection closed"}:
			default:
			}
			delete(b.pending, id)
		}
		b.rpcMu.Unlock()
	})
}

// ---------------------------------------------------------------------------
// Message I/O
// ---------------------------------------------------------------------------

// SendInbound sends a user message to the remote server via WebSocket.
func (b *RemoteBackend) SendInbound(msg bus.InboundMessage) error {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	if b.conn == nil {
		return fmt.Errorf("not connected to server")
	}
	// Set write deadline to avoid blocking indefinitely on dead connections.
	b.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer b.conn.SetWriteDeadline(time.Time{}) // reset

	// Detect /cancel and send as "cancel" type so the server's cancel handler
	// routes it correctly (web.go readPump switch on msg.Type).
	msgType := "message"
	isCancel := strings.TrimSpace(strings.ToLower(msg.Content)) == "/cancel"
	if isCancel {
		msgType = "cancel"
	}

	outMsg := wsOutgoingMessage{
		Type:       msgType,
		Content:    msg.Content,
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
		SenderID:   msg.SenderID,
		SenderName: msg.SenderName,
		ChatType:   msg.ChatType,
	}
	return b.conn.WriteJSON(outMsg)
}

// OnOutbound registers a callback for agent reply messages received from the server.
func (b *RemoteBackend) OnOutbound(callback func(bus.OutboundMessage)) {
	b.outboundMu.Lock()
	defer b.outboundMu.Unlock()
	b.outboundCb = callback
}

// Bus returns nil for RemoteBackend (no local message bus).
func (b *RemoteBackend) Bus() *bus.MessageBus { return nil }

// IsRemote returns true — the agent loop runs on the server.
func (b *RemoteBackend) IsRemote() bool { return true }

// ServerURL returns the configured server URL for display purposes.
func (b *RemoteBackend) ServerURL() string { return b.serverURL }

// IsProcessing returns true if there is an active agent turn for the given chat.
func (b *RemoteBackend) IsProcessing(ch, chatID string) bool {
	raw, err := b.callRPC("is_processing", map[string]string{
		"channel": ch, "chat_id": chatID,
	})
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(raw)) == "true"
}

// GetActiveProgress returns the latest progress snapshot for an active turn via RPC.
func (b *RemoteBackend) GetActiveProgress(ch, chatID string) *channel.CLIProgressPayload {
	raw, err := b.callRPC("get_active_progress", map[string]string{
		"channel": ch, "chat_id": chatID,
	})
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var payload channel.CLIProgressPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return &payload
}

// OnProgress registers a callback for streaming progress events.
func (b *RemoteBackend) OnProgress(callback func(*channel.CLIProgressPayload)) {
	b.progressMu.Lock()
	defer b.progressMu.Unlock()
	b.progressCb = callback
}

// OnReconnect registers a callback invoked after a successful WS reconnection.
// Used to reload history and re-sync state that may have changed during disconnect.
func (b *RemoteBackend) OnReconnect(callback func()) {
	b.onReconnectCb = callback
}

// OnConnStateChange registers a callback invoked when the WS connection state changes.
// States: "connected", "disconnected", "reconnecting".
// Used by CLI to update the header bar connection indicator in real-time.
func (b *RemoteBackend) OnConnStateChange(callback func(state string)) {
	b.onConnStateCb = callback
}

// ConnState returns the current connection state string.
func (b *RemoteBackend) ConnState() string {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	return b.connState
}

// setConnState updates connState and fires the callback if state changed.
// Must be called with connMu held OR from a single-threaded context.
func (b *RemoteBackend) setConnState(state string) {
	b.connMu.Lock()
	prev := b.connState
	b.connState = state
	cb := b.onConnStateCb
	b.connMu.Unlock()
	if prev != state && cb != nil {
		cb(state)
	}
}

// ---------------------------------------------------------------------------
// WebSocket connection
// ---------------------------------------------------------------------------

func (b *RemoteBackend) connect(ctx context.Context) error {
	u, err := url.Parse(b.serverURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	switch u.Scheme {
	case "", "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	wsPath := u.Path
	if wsPath == "" || wsPath == "/" {
		wsPath = "/ws"
	}
	u.Path = wsPath
	q := u.Query()
	q.Set("client_type", "cli")
	if b.token != "" {
		q.Set("token", b.token)
	}
	u.RawQuery = q.Encode()
	wsURL := u.String()
	log.WithField("url", wsURL).Info("Connecting to remote xbot server...")
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("WS dial: %w", err)
	}

	// Set up pong handler to detect server liveness.
	// Server sends pings every 30s; pong handler resets read deadline.
	conn.SetPongHandler(func(_ string) error {
		conn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})
	// Initial read deadline — if no data (including pongs) in 120s, connection is dead.
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))

	// Atomically replace connection to avoid race with Stop().
	b.connMu.Lock()
	old := b.conn
	b.conn = conn
	b.connMu.Unlock()
	if old != nil {
		old.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "reconnecting"))
		old.Close()
	}
	log.Info("Connected to remote xbot server")
	b.setConnState("connected")

	// Send sync message so server replays missed events from eventStream buffer.
	// This enables mid-turn reconnect: a new CLI terminal sees recent progress/stream
	// events without waiting for the 2s timeout fallback.
	syncMsg := struct {
		Type    string `json:"type"`
		LastSeq uint64 `json:"last_seq"`
	}{
		Type:    "sync",
		LastSeq: b.lastSeq.Load(),
	}
	if err := conn.WriteJSON(syncMsg); err != nil {
		log.WithError(err).Warn("Failed to send sync message")
	}

	return nil
}

// SubscribeChat sends a subscribe message to the server so the Hub routes
// server-pushed events (progress, stream, outbound) to this WS client.
// Must be called after connect() with the business chatID (e.g. "/home/user").
func (b *RemoteBackend) SubscribeChat(chatID string) {
	b.connMu.Lock()
	conn := b.conn
	b.connMu.Unlock()
	if conn == nil {
		return
	}
	subMsg := wsOutgoingMessage{Type: "subscribe", ChatID: chatID}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(subMsg); err != nil {
		log.WithError(err).Warn("Failed to send subscribe message")
	}
	conn.SetWriteDeadline(time.Time{})
}

// ---------------------------------------------------------------------------
// Read pump — dispatches server messages
// ---------------------------------------------------------------------------

func (b *RemoteBackend) readPump(ctx context.Context) {
	defer b.readPumpWg.Done()
	for {
		select {
		case <-b.done:
			return
		case <-ctx.Done():
			return
		default:
		}
		b.connMu.Lock()
		conn := b.conn
		b.connMu.Unlock()
		if conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.WithError(err).Warn("WS connection lost (read error)")
			} else {
				log.WithError(err).Info("WS connection closed")
			}
			// Unblock all pending RPC callers so they don't hang until timeout.
			// Use non-blocking write instead of close(ch) to avoid double-close
			// panic if Stop() runs concurrently (both hold rpcMu but close on
			// buffered-chan can still race if channel is already drained).
			b.rpcMu.Lock()
			for id, ch := range b.pending {
				select {
				case ch <- &rpcResponse{Error: "connection lost"}:
				default:
				}
				delete(b.pending, id)
			}
			b.rpcMu.Unlock()
			select {
			case b.reconnectCh <- struct{}{}:
			default:
			}
			b.setConnState("disconnected")
			return
		}
		var msg wsIncomingMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.WithError(err).Debug("Invalid WS message from server")
			continue
		}
		// Track highest seq for reconnect sync.
		if msg.Seq > 0 {
			for {
				old := b.lastSeq.Load()
				if msg.Seq <= old || b.lastSeq.CompareAndSwap(old, msg.Seq) {
					break
				}
			}
		}
		switch msg.Type {
		case "rpc_response":
			b.handleRPCResponse(&msg)
		case "text":
			outMsg := bus.OutboundMessage{
				Content:  msg.Content,
				Channel:  msg.Channel,
				ChatID:   msg.ChatID,
				Metadata: make(map[string]string),
			}
			if outMsg.Channel == "" {
				outMsg.Channel = "remote"
			}
			if msg.ID != "" {
				outMsg.Metadata["message_id"] = msg.ID
			}
			if msg.ProgressHistory != "" {
				outMsg.Metadata["progress_history"] = msg.ProgressHistory
			}
			b.outboundMu.RLock()
			cb := b.outboundCb
			b.outboundMu.RUnlock()
			if cb != nil {
				log.WithField("msg_type", msg.Type).WithField("content_len", len(msg.Content)).Info("RemoteBackend: dispatching outbound message")
				func() {
					defer func() {
						if r := recover(); r != nil {
							clipanic.Report("agent.RemoteBackend.OnOutbound", outMsg, r)
							log.WithField("panic", r).Warn("RemoteBackend outbound callback panicked")
						}
					}()
					cb(outMsg)
				}()
				log.Debug("RemoteBackend: outbound callback returned")
			} else {
				log.Warn("Received server reply but no outbound callback registered")
			}
		case "progress_structured":
			b.dispatchProgress(convertWsProgressToCLI(msg.Progress))
		case "stream_content":
			b.dispatchProgress(&channel.CLIProgressPayload{
				ChatID:                 msg.Progress.ChatID,
				StreamContent:          msg.Progress.GetStreamContent(),
				ReasoningStreamContent: msg.Progress.GetReasoningStreamContent(),
			})
		case "ask_user":
			if msg.Progress != nil {
				if len(msg.Progress.Questions) > 0 {
					qJSON, _ := json.Marshal(msg.Progress.Questions)
					outMsg := bus.OutboundMessage{
						Channel:     "cli",
						WaitingUser: true,
						Metadata: map[string]string{
							"ask_questions": string(qJSON),
						},
					}
					if msg.Progress.RequestID != "" {
						outMsg.Metadata["request_id"] = msg.Progress.RequestID
					}
					b.outboundMu.RLock()
					cb := b.outboundCb
					b.outboundMu.RUnlock()
					if cb != nil {
						func() {
							defer func() {
								if r := recover(); r != nil {
									clipanic.Report("agent.RemoteBackend.OnAskUser", outMsg, r)
									log.WithField("panic", r).Warn("RemoteBackend ask_user callback panicked")
								}
							}()
							cb(outMsg)
						}()
					} else {
						log.Warn("Received ask_user but no outbound callback registered")
					}
				}
			}
		}
	}
}

func (b *RemoteBackend) handleRPCResponse(msg *wsIncomingMessage) {
	if msg.ID == "" {
		return
	}
	b.rpcMu.Lock()
	ch, ok := b.pending[msg.ID]
	if ok {
		delete(b.pending, msg.ID)
	}
	b.rpcMu.Unlock()
	if ok {
		ch <- &rpcResponse{
			ID:     msg.ID,
			Result: msg.Result,
			Error:  msg.Error,
		}
	}
}

func (b *RemoteBackend) dispatchProgress(payload *channel.CLIProgressPayload) {
	if payload == nil {
		return
	}
	b.progressMu.RLock()
	cb := b.progressCb
	b.progressMu.RUnlock()
	if cb != nil {
		func() {
			defer func() {
				if r := recover(); r != nil {
					clipanic.Report("agent.RemoteBackend.OnProgress", payload, r)
					log.WithField("panic", r).Warn("RemoteBackend progress callback panicked")
				}
			}()
			cb(payload)
		}()
	}
}

func convertWsProgressToCLI(wp *channel.WsProgressPayload) *channel.CLIProgressPayload {
	if wp == nil {
		return nil
	}
	payload := &channel.CLIProgressPayload{
		ChatID:                 wp.ChatID,
		Phase:                  wp.Phase,
		Iteration:              wp.Iteration,
		Thinking:               wp.Thinking,
		Reasoning:              wp.Reasoning,
		StreamContent:          wp.StreamContent,
		ReasoningStreamContent: wp.ReasoningStreamContent,
		HistoryCompacted:       wp.HistoryCompacted,
	}
	for _, t := range wp.ActiveTools {
		payload.ActiveTools = append(payload.ActiveTools, channel.CLIToolProgress{
			Name: t.Name, Label: t.Label, Status: t.Status,
			Elapsed: t.Elapsed, Summary: t.Summary,
		})
	}
	for _, t := range wp.CompletedTools {
		payload.CompletedTools = append(payload.CompletedTools, channel.CLIToolProgress{
			Name: t.Name, Label: t.Label, Status: t.Status,
			Elapsed: t.Elapsed, Summary: t.Summary,
		})
	}
	for _, sa := range wp.SubAgents {
		payload.SubAgents = append(payload.SubAgents, convertWsSubAgent(sa))
	}
	for _, td := range wp.Todos {
		payload.Todos = append(payload.Todos, channel.CLITodoItem(td))
	}
	if wp.TokenUsage != nil {
		payload.TokenUsage = &channel.CLITokenUsage{
			PromptTokens:     wp.TokenUsage.PromptTokens,
			CompletionTokens: wp.TokenUsage.CompletionTokens,
			TotalTokens:      wp.TokenUsage.TotalTokens,
			CacheHitTokens:   wp.TokenUsage.CacheHitTokens,
		}
	}
	return payload
}

func convertWsSubAgent(sa channel.WsSubAgent) channel.CLISubAgent {
	r := channel.CLISubAgent{Role: sa.Role, Status: sa.Status, Desc: sa.Desc}
	for _, c := range sa.Children {
		r.Children = append(r.Children, convertWsSubAgent(c))
	}
	return r
}

// ---------------------------------------------------------------------------
// Ping loop — sends WebSocket pings to keep connection alive
// ---------------------------------------------------------------------------

// pingLoop sends WebSocket pings every 25 seconds.
// The server sends pings every 30s and expects pongs within 60s.
// Client pings prevent the server's read deadline from expiring.
func (b *RemoteBackend) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-b.done:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.sendPing()
		}
	}
}

// sendPing sends a WebSocket ping frame to the server.
func (b *RemoteBackend) sendPing() {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	if b.conn == nil {
		return
	}
	if err := b.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
		log.WithError(err).Warn("WS ping failed")
	}
}

// ---------------------------------------------------------------------------
// Reconnect
// ---------------------------------------------------------------------------

func (b *RemoteBackend) reconnectLoop(ctx context.Context) {
	for {
		select {
		case <-b.done:
			return
		case <-ctx.Done():
			return
		case <-b.reconnectCh:
			b.setConnState("reconnecting")
			consecutiveFailures := 0
			for delay := time.Second; delay <= 30*time.Second; delay *= 2 {
				select {
				case <-b.done:
					return
				case <-ctx.Done():
					return
				default:
				}
				log.WithField("delay", delay).Info("Reconnecting to server...")
				timer := time.NewTimer(delay)
				select {
				case <-b.done:
					timer.Stop()
					return
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				if err := b.connect(ctx); err != nil {
					consecutiveFailures++
					log.WithError(err).Warn("Reconnect failed")
					// Notify user after 3 consecutive failures via outbound callback.
					if consecutiveFailures == 3 {
						b.outboundMu.RLock()
						cb := b.outboundCb
						b.outboundMu.RUnlock()
						if cb != nil {
							cb(bus.OutboundMessage{
								Channel: "remote",
								Content: fmt.Sprintf("Connection lost, reconnecting (attempt %d)...", consecutiveFailures),
							})
						}
					}
					continue
				}
				log.Info("Reconnected to server")
				consecutiveFailures = 0
				// Notify CLI to reload history and re-sync state.
				// Run in goroutine — callback may make slow RPC calls that
				// should not block the reconnectLoop.
				if b.onReconnectCb != nil {
					go b.onReconnectCb()
				}
				b.readPumpWg.Add(1)
				go b.readPump(ctx)
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Generic RPC call
// ---------------------------------------------------------------------------

func (b *RemoteBackend) callRPC(method string, params any) (json.RawMessage, error) {
	// Lock order: connMu → rpcMu (never reverse, to prevent deadlock).
	b.connMu.Lock()
	if b.conn == nil {
		b.connMu.Unlock()
		return nil, fmt.Errorf("not connected to server")
	}
	var rawParams json.RawMessage
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			b.connMu.Unlock()
			return nil, fmt.Errorf("marshal RPC params: %w", err)
		}
		rawParams = data
	}
	id := fmt.Sprintf("rpc-%d", b.rpcCounter.Add(1))
	ch := make(chan *rpcResponse, 1)
	b.rpcMu.Lock()
	b.pending[id] = ch
	b.rpcMu.Unlock()
	req := wsOutgoingMessage{Type: "rpc", ID: id, Method: method, Params: rawParams}
	// Set write deadline to avoid blocking indefinitely on dead connections.
	b.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := b.conn.WriteJSON(req); err != nil {
		b.conn.SetWriteDeadline(time.Time{})
		b.connMu.Unlock()
		b.rpcMu.Lock()
		delete(b.pending, id)
		b.rpcMu.Unlock()
		return nil, fmt.Errorf("send RPC %s: %w", method, err)
	}
	b.conn.SetWriteDeadline(time.Time{})
	b.connMu.Unlock()
	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("RPC %s: connection closed", method)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("RPC %s: %s", method, resp.Error)
		}
		return resp.Result, nil
	case <-time.After(30 * time.Second):
		b.rpcMu.Lock()
		delete(b.pending, id)
		b.rpcMu.Unlock()
		return nil, fmt.Errorf("RPC %s: timeout", method)
	case <-b.done:
		b.rpcMu.Lock()
		delete(b.pending, id)
		b.rpcMu.Unlock()
		return nil, fmt.Errorf("RPC %s: backend stopped", method)
	}
}

func (b *RemoteBackend) callRPCVoid(method string, params any) error {
	_, err := b.callRPC(method, params)
	return err
}

func (b *RemoteBackend) callRPCString(method string, params any) (string, error) {
	raw, err := b.callRPC(method, params)
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("unmarshal RPC %s: %w", method, err)
	}
	return s, nil
}

func (b *RemoteBackend) GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool) {
	raw, err := b.callRPC("get_session_messages", map[string]any{
		"channel": channelName, "chat_id": chatID,
		"role": roleName, "instance": instance,
	})
	if err != nil {
		return nil, false
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var msgs []SessionMessage
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, false
	}
	return msgs, true
}

func (b *RemoteBackend) GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool) {
	raw, err := b.callRPC("get_agent_session_dump", map[string]any{
		"channel": channelName, "chat_id": chatID,
		"role": roleName, "instance": instance,
	})
	if err != nil {
		return nil, false
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var dump AgentSessionDump
	if err := json.Unmarshal(raw, &dump); err != nil {
		return nil, false
	}
	return &dump, true
}

func (b *RemoteBackend) GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool) {
	raw, err := b.callRPC("get_agent_session_dump_by_full_key", map[string]any{
		"full_key": fullKey,
	})
	if err != nil {
		return nil, false
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var dump AgentSessionDump
	if err := json.Unmarshal(raw, &dump); err != nil {
		return nil, false
	}
	return &dump, true
}

func (b *RemoteBackend) callRPCInt(method string, params any) (int, error) {
	raw, err := b.callRPC(method, params)
	if err != nil {
		return 0, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var n int
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("unmarshal RPC %s: %w", method, err)
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// AgentBackend — nil-returning object methods
// ---------------------------------------------------------------------------

func (b *RemoteBackend) LLMFactory() *LLMFactory                     { return nil }
func (b *RemoteBackend) SettingsService() *SettingsService           { return nil }
func (b *RemoteBackend) MultiSession() *session.MultiTenantSession   { return nil }
func (b *RemoteBackend) BgTaskManager() *tools.BackgroundTaskManager { return nil }
func (b *RemoteBackend) HookManager() *hooks.Manager             { return nil }
func (b *RemoteBackend) ApprovalState() *hooks.ApprovalState     { return nil }

// ---------------------------------------------------------------------------
// AgentBackend — init-only no-ops (server handles these)
// ---------------------------------------------------------------------------

func (b *RemoteBackend) SetDirectSend(func(bus.OutboundMessage) (string, error)) {}
func (b *RemoteBackend) SetChannelFinder(func(string) (channel.Channel, bool))   {}
func (b *RemoteBackend) SetChannelPromptProviders(...ChannelPromptProvider)      {}
func (b *RemoteBackend) RegisterCoreTool(tools.Tool)                             {}
func (b *RemoteBackend) IndexGlobalTools()                                       {}
func (b *RemoteBackend) SetEventRouter(*event.Router)                            {}
func (b *RemoteBackend) SetSandbox(tools.Sandbox, string)                        {}
func (b *RemoteBackend) GetCardBuilder() *tools.CardBuilder                      { return nil }
func (b *RemoteBackend) RegisterTool(tools.Tool)                                 {}
func (b *RemoteBackend) RegistryManager() *RegistryManager                       { return nil }

// ---------------------------------------------------------------------------
// AgentBackend — RPC-backed setters
// ---------------------------------------------------------------------------

func (b *RemoteBackend) SetContextMode(mode string) error {
	return b.callRPCVoid("set_context_mode", map[string]string{"mode": mode})
}

func (b *RemoteBackend) SetCWD(ch, chatID, dir string) error {
	return b.callRPCVoid("set_cwd", map[string]string{
		"channel": ch, "chat_id": chatID, "dir": dir,
	})
}

func (b *RemoteBackend) SetMaxIterations(n int) {
	if err := b.callRPCVoid("set_max_iterations", map[string]int{"n": n}); err != nil {
		log.WithError(err).Warn("RemoteBackend: SetMaxIterations RPC failed")
	}
}

func (b *RemoteBackend) SetMaxConcurrency(n int) {
	if err := b.callRPCVoid("set_max_concurrency", map[string]int{"n": n}); err != nil {
		log.WithError(err).Warn("RemoteBackend: SetMaxConcurrency RPC failed")
	}
}

func (b *RemoteBackend) SetMaxContextTokens(n int) {
	if err := b.callRPCVoid("set_max_context_tokens", map[string]int{"n": n}); err != nil {
		log.WithError(err).Warn("RemoteBackend: SetMaxContextTokens RPC failed")
	}
}

func (b *RemoteBackend) SetProxyLLM(senderID string, proxy *llm.ProxyLLM, model string) {
	if err := b.callRPCVoid("set_proxy_llm", map[string]string{"model": model}); err != nil {
		log.WithError(err).Warn("RemoteBackend: SetProxyLLM RPC failed")
	}
}

func (b *RemoteBackend) ClearProxyLLM(senderID string) {
	if err := b.callRPCVoid("clear_proxy_llm", nil); err != nil {
		log.WithError(err).Warn("RemoteBackend: ClearProxyLLM RPC failed")
	}
}

func (b *RemoteBackend) SetUserModel(senderID, model string) error {
	return b.callRPCVoid("set_user_model", map[string]string{"model": model, "sender_id": senderID})
}

// SwitchModel switches the active model for a user (memory-only, like LLMFactory.SwitchModel).
// Unlike SetUserModel, this does not require an existing LLMConfig.
func (b *RemoteBackend) SwitchModel(senderID, model string) error {
	return b.callRPCVoid("switch_model", map[string]string{"model": model, "sender_id": senderID})
}

func (b *RemoteBackend) SetUserMaxContext(senderID string, maxContext int) error {
	return b.callRPCVoid("set_user_max_context", map[string]any{"max_context": maxContext, "sender_id": senderID})
}

func (b *RemoteBackend) SetUserMaxOutputTokens(senderID string, maxTokens int) error {
	return b.callRPCVoid("set_user_max_output_tokens", map[string]any{"max_tokens": maxTokens, "sender_id": senderID})
}

func (b *RemoteBackend) SetUserThinkingMode(senderID string, mode string) error {
	return b.callRPCVoid("set_user_thinking_mode", map[string]string{"mode": mode, "sender_id": senderID})
}

func (b *RemoteBackend) SetLLMConcurrency(senderID string, personal int) error {
	return b.callRPCVoid("set_llm_concurrency", map[string]any{"personal": personal, "sender_id": senderID})
}

// ---------------------------------------------------------------------------
// AgentBackend — RPC-backed getters
// ---------------------------------------------------------------------------

func (b *RemoteBackend) GetDefaultModel() string {
	s, _ := b.callRPCString("get_default_model", nil)
	return s
}

func (b *RemoteBackend) GetUserMaxContext(senderID string) int {
	n, _ := b.callRPCInt("get_user_max_context", map[string]string{"sender_id": senderID})
	return n
}

func (b *RemoteBackend) GetUserMaxOutputTokens(senderID string) int {
	n, _ := b.callRPCInt("get_user_max_output_tokens", map[string]string{"sender_id": senderID})
	return n
}

func (b *RemoteBackend) GetUserThinkingMode(senderID string) string {
	s, _ := b.callRPCString("get_user_thinking_mode", map[string]string{"sender_id": senderID})
	return s
}

func (b *RemoteBackend) GetLLMConcurrency(senderID string) int {
	n, _ := b.callRPCInt("get_llm_concurrency", map[string]string{"sender_id": senderID})
	return n
}

func (b *RemoteBackend) GetContextMode() string {
	s, _ := b.callRPCString("get_context_mode", nil)
	return s
}

func (b *RemoteBackend) CountInteractiveSessions(channelName, chatID string) int {
	n, _ := b.callRPCInt("count_interactive_sessions", map[string]string{"channel": channelName, "chat_id": chatID})
	return n
}

func (b *RemoteBackend) ListInteractiveSessions(channelName, chatID string) []InteractiveSessionInfo {
	raw, err := b.callRPC("list_interactive_sessions", map[string]string{"channel": channelName, "chat_id": chatID})
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var result []InteractiveSessionInfo
	if err := json.Unmarshal(raw, &result); err != nil {
		log.WithError(err).Warn("Failed to unmarshal ListInteractiveSessions")
		return nil
	}
	return result
}

func (b *RemoteBackend) InspectInteractiveSession(ctx context.Context, roleName, channelName, chatID, instance string, tailCount int) (string, error) {
	raw, err := b.callRPC("inspect_interactive_session", map[string]any{
		"role": roleName, "channel": channelName, "chat_id": chatID,
		"instance": instance, "tail_count": tailCount,
	})
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("unmarshal inspect result: %w", err)
	}
	return s, nil
}

// ---------------------------------------------------------------------------
// Extended RPC methods for Settings, LLM, Memory, Subscriptions
// ---------------------------------------------------------------------------

func (b *RemoteBackend) GetSettings(namespace, senderID string) (map[string]string, error) {
	raw, err := b.callRPC("get_settings", map[string]string{"namespace": namespace, "sender_id": senderID})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal settings: %w", err)
	}
	return result, nil
}

func (b *RemoteBackend) SetSetting(namespace, senderID, key, value string) error {
	return b.callRPCVoid("set_setting", map[string]string{
		"namespace": namespace, "sender_id": senderID, "key": key, "value": value,
	})
}

func (b *RemoteBackend) ListModels() []string {
	raw, err := b.callRPC("list_models", nil)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var result []string
	if err := json.Unmarshal(raw, &result); err != nil {
		log.WithError(err).Warn("Failed to unmarshal ListModels")
		return nil
	}
	return result
}

func (b *RemoteBackend) ListAllModels() []string {
	raw, err := b.callRPC("list_all_models", nil)
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var result []string
	if err := json.Unmarshal(raw, &result); err != nil {
		log.WithError(err).Warn("Failed to unmarshal ListAllModels")
		return nil
	}
	return result
}

func (b *RemoteBackend) SetModelTiers(cfg config.LLMConfig) error {
	return b.callRPCVoid("set_model_tiers", cfg)
}

func (b *RemoteBackend) SetDefaultThinkingMode(mode string) error {
	return b.callRPCVoid("set_default_thinking_mode", map[string]string{"mode": mode})
}

func (b *RemoteBackend) ClearMemory(ctx context.Context, ch, chatID, targetType, senderID string) error {
	return b.callRPCVoid("clear_memory", map[string]string{
		"channel": ch, "chat_id": chatID, "target_type": targetType,
	})
}

// RewindCheckpoints rolls back file checkpoints on the server side.
// Returns JSON-encoded RewindResult.
func (b *RemoteBackend) RewindCheckpoints(turnIdx int) (string, error) {
	return b.callRPCString("rewind_checkpoints", map[string]any{"turn_idx": turnIdx})
}

func (b *RemoteBackend) GetMemoryStats(ctx context.Context, ch, chatID, senderID string) map[string]string {
	raw, err := b.callRPC("get_memory_stats", map[string]string{
		"channel": ch, "chat_id": chatID,
	})
	if err != nil || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var result map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		log.WithError(err).Warn("Failed to unmarshal GetMemoryStats")
		return nil
	}
	return result
}

func (b *RemoteBackend) GetUserTokenUsage(senderID string) (map[string]any, error) {
	raw, err := b.callRPC("get_user_token_usage", map[string]string{"sender_id": senderID})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal token usage: %w", err)
	}
	return result, nil
}

func (b *RemoteBackend) GetDailyTokenUsage(senderID string, days int) ([]map[string]any, error) {
	raw, err := b.callRPC("get_daily_token_usage", map[string]any{"days": days, "sender_id": senderID})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result []map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal daily usage: %w", err)
	}
	return result, nil
}

func (b *RemoteBackend) GetBgTaskCount(sessionKey string) int {
	n, _ := b.callRPCInt("get_bg_task_count", map[string]string{"session_key": sessionKey})
	return n
}

// BgTaskJSON is a transport-only struct for serializing background tasks over RPC.
type BgTaskJSON struct {
	ID         string `json:"id"`
	Command    string `json:"command"`
	Status     string `json:"status"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at,omitempty"`
	Output     string `json:"output"`
	ExitCode   int    `json:"exit_code"`
	Error      string `json:"error,omitempty"`
}

func (b *RemoteBackend) ListBgTasks(sessionKey string) ([]BgTaskJSON, error) {
	raw, err := b.callRPC("list_bg_tasks", map[string]string{"session_key": sessionKey})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result []BgTaskJSON
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal bg tasks: %w", err)
	}
	return result, nil
}

func (b *RemoteBackend) KillBgTask(taskID string) error {
	return b.callRPCVoid("kill_bg_task", map[string]string{"task_id": taskID})
}

func (b *RemoteBackend) CleanupCompletedBgTasks(sessionKey string) {
	_ = b.callRPCVoid("cleanup_completed_bg_tasks", map[string]string{"session_key": sessionKey})
}

// TenantInfo is a transport-only struct for serializing tenant info over RPC.
type TenantInfo struct {
	ID           int64  `json:"id"`
	Channel      string `json:"channel"`
	ChatID       string `json:"chat_id"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
}

func (b *RemoteBackend) ListTenants() ([]TenantInfo, error) {
	raw, err := b.callRPC("list_tenants", nil)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result []TenantInfo
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tenants: %w", err)
	}
	return result, nil
}

func (b *RemoteBackend) ListSubscriptions(senderID string) ([]channel.Subscription, error) {
	raw, err := b.callRPC("list_subscriptions", map[string]string{"sender_id": senderID})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result []channel.Subscription
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal subscriptions: %w", err)
	}
	return result, nil
}

func (b *RemoteBackend) GetDefaultSubscription(senderID string) (*channel.Subscription, error) {
	raw, err := b.callRPC("get_default_subscription", map[string]string{"sender_id": senderID})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result channel.Subscription
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal subscription: %w", err)
	}
	return &result, nil
}

func (b *RemoteBackend) AddSubscription(senderID string, sub channel.Subscription) error {
	return b.callRPCVoid("add_subscription", map[string]any{"sub": sub})
}

func (b *RemoteBackend) RemoveSubscription(id string) error {
	return b.callRPCVoid("remove_subscription", map[string]string{"id": id})
}

func (b *RemoteBackend) SetDefaultSubscription(id string, chatID string) error {
	return b.callRPCVoid("set_default_subscription", map[string]string{"id": id, "chat_id": chatID})
}

func (b *RemoteBackend) RenameSubscription(id, name string) error {
	return b.callRPCVoid("rename_subscription", map[string]string{"id": id, "name": name})
}

func (b *RemoteBackend) UpdateSubscription(id string, sub channel.Subscription) error {
	return b.callRPCVoid("update_subscription", map[string]any{"id": id, "sub": sub})
}

func (b *RemoteBackend) SetSubscriptionModel(id, model string) error {
	return b.callRPCVoid("set_subscription_model", map[string]string{"id": id, "model": model})
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

func (b *RemoteBackend) GetHistory(ch, chatID string) ([]channel.HistoryMessage, error) {
	raw, err := b.callRPC("get_history", map[string]string{
		"channel": ch, "chat_id": chatID,
	})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result []channel.HistoryMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (b *RemoteBackend) TrimHistory(ch, chatID string, cutoff time.Time) error {
	if cutoff.IsZero() {
		return nil
	}
	return b.callRPCVoid("trim_history", map[string]any{
		"channel": ch, "chat_id": chatID,
		"cutoff": cutoff.Format(time.RFC3339),
	})
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (b *RemoteBackend) Close() error {
	b.Stop()
	return nil
}

func (b *RemoteBackend) ResetTokenState() {
	if err := b.callRPCVoid("reset_token_state", nil); err != nil {
		log.WithError(err).Warn("RemoteBackend: ResetTokenState RPC failed")
	}
}

func (b *RemoteBackend) GetChannelConfigs() (map[string]map[string]string, error) {
	raw, err := b.callRPC("get_channel_config", nil)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var result map[string]map[string]string
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("unmarshal channel configs: %w", err)
	}
	return result, nil
}

func (b *RemoteBackend) SetChannelConfig(channel string, values map[string]string) error {
	return b.callRPCVoid("set_channel_config", map[string]any{
		"channel": channel,
		"values":  values,
	})
}

func (b *RemoteBackend) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// ---------------------------------------------------------------------------
// RPC method registry — used by server-side handler
// ---------------------------------------------------------------------------

// RPCMethodList returns all RPC method names for documentation/validation.
func RPCMethodList() []string {
	return []string{
		"get_context_mode", "set_context_mode",
		"get_settings", "set_setting",
		"set_max_iterations", "set_max_concurrency", "set_max_context_tokens",
		"get_default_model", "set_user_model", "switch_model",
		"get_user_max_context", "set_user_max_context",
		"get_user_max_output_tokens", "set_user_max_output_tokens",
		"get_user_thinking_mode", "set_user_thinking_mode",
		"get_llm_concurrency", "set_llm_concurrency",
		"set_default_thinking_mode",
		"list_models", "list_all_models", "set_model_tiers",
		"set_proxy_llm", "clear_proxy_llm",
		"clear_memory", "get_memory_stats",
		"get_user_token_usage", "get_daily_token_usage",
		"count_interactive_sessions", "list_interactive_sessions",
		"inspect_interactive_session",
		"get_bg_task_count",
		"list_subscriptions", "get_default_subscription",
		"add_subscription", "remove_subscription",
		"set_default_subscription", "rename_subscription",
		"update_subscription", "set_subscription_model",
		"get_history", "trim_history",
		"reset_token_state",
		"get_channel_config", "set_channel_config",
		"is_processing", "get_active_progress",
		"rewind_checkpoints",
	}
}
