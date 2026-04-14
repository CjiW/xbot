package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/event"
	"xbot/session"
	"xbot/tools"

	"github.com/gorilla/websocket"
	log "xbot/logger"
)

// RemoteBackend connects to a remote xbot server via WebSocket.
// The agent loop and tool execution run server-side; the CLI is a
// thin display/input layer.
//
// Management methods (LLMFactory, SettingsService, etc.) return nil
// until the WS protocol is extended with RPC support. For now,
// RemoteBackend only handles message send/receive.
type RemoteBackend struct {
	serverURL string // ws://host:port or wss://host:port
	token     string // authentication token

	// WS connection
	conn      *websocket.Conn
	connMu    sync.Mutex
	done      chan struct{}
	closeOnce sync.Once

	// Outbound callback
	outboundMu sync.RWMutex
	outboundCb func(bus.OutboundMessage)

	// Reconnect
	reconnectCh chan struct{}
}

// RemoteBackendConfig holds the configuration for connecting to a remote server.
type RemoteBackendConfig struct {
	ServerURL string // e.g. "ws://localhost:8080" or "wss://example.com"
	Token     string // auth token (optional, for future token-based auth)
}

// NewRemoteBackend creates a RemoteBackend that connects to the given server URL.
func NewRemoteBackend(cfg RemoteBackendConfig) *RemoteBackend {
	return &RemoteBackend{
		serverURL:   cfg.ServerURL,
		token:       cfg.Token,
		done:        make(chan struct{}),
		reconnectCh: make(chan struct{}, 1),
	}
}

// wsIncomingMessage represents a message received from the server.
// Mirrors channel.wsMessage but avoids importing channel internals.
type wsIncomingMessage struct {
	Type            string `json:"type"`
	ID              string `json:"id,omitempty"`
	Content         string `json:"content,omitempty"`
	OriginalContent string `json:"original_content,omitempty"`
	TS              int64  `json:"ts,omitempty"`
	ProgressHistory string `json:"progress_history,omitempty"`
}

// wsOutgoingMessage represents a message sent to the server.
// Mirrors channel.wsClientMessage.
type wsOutgoingMessage struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Start connects to the remote server via WebSocket and starts
// the read pump for receiving messages.
func (b *RemoteBackend) Start(ctx context.Context) error {
	if err := b.connect(ctx); err != nil {
		return fmt.Errorf("connect to %s: %w", b.serverURL, err)
	}
	go b.readPump(ctx)
	go b.reconnectLoop(ctx)
	return nil
}

// Stop closes the WebSocket connection.
func (b *RemoteBackend) Stop() {
	b.closeOnce.Do(func() {
		close(b.done)
		b.connMu.Lock()
		if b.conn != nil {
			b.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "client shutdown"))
			b.conn.Close()
			b.conn = nil
		}
		b.connMu.Unlock()
	})
}

// SendInbound sends a user message to the remote server via WebSocket.
func (b *RemoteBackend) SendInbound(msg bus.InboundMessage) error {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	if b.conn == nil {
		return fmt.Errorf("not connected to server")
	}
	outMsg := wsOutgoingMessage{
		Type:    "message",
		Content: msg.Content,
	}
	return b.conn.WriteJSON(outMsg)
}

// OnOutbound registers a callback for messages received from the server.
func (b *RemoteBackend) OnOutbound(callback func(bus.OutboundMessage)) {
	b.outboundMu.Lock()
	defer b.outboundMu.Unlock()
	b.outboundCb = callback
}

// Bus returns nil for RemoteBackend (no local message bus).
func (b *RemoteBackend) Bus() *bus.MessageBus { return nil }

// connect establishes the WebSocket connection to the server.
func (b *RemoteBackend) connect(ctx context.Context) error {
	// Build WS URL
	u, err := url.Parse(b.serverURL)
	if err != nil {
		return fmt.Errorf("invalid server URL: %w", err)
	}
	// Ensure ws:// or wss:// scheme
	switch u.Scheme {
	case "", "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	wsURL := fmt.Sprintf("%s://%s/ws", u.Scheme, u.Host)

	header := http.Header{}
	if b.token != "" {
		header.Set("Authorization", "Bearer "+b.token)
	}

	log.WithField("url", wsURL).Info("Connecting to remote xbot server...")

	dialer := websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
	}
	conn, _, err := dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return fmt.Errorf("WS dial: %w", err)
	}

	b.connMu.Lock()
	b.conn = conn
	b.connMu.Unlock()

	log.Info("Connected to remote xbot server")
	return nil
}

// readPump reads messages from the WebSocket and dispatches them via the outbound callback.
func (b *RemoteBackend) readPump(ctx context.Context) {
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
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway, websocket.CloseNormalClosure) {
				log.WithError(err).Warn("WS read error")
			}
			// Trigger reconnect
			select {
			case b.reconnectCh <- struct{}{}:
			default:
			}
			return
		}

		var msg wsIncomingMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.WithError(err).Debug("Invalid WS message from server")
			continue
		}

		// Convert to OutboundMessage and dispatch
		outMsg := bus.OutboundMessage{
			Content:  msg.Content,
			Channel:  "remote",
			Metadata: make(map[string]string),
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
			cb(outMsg)
		}
	}
}

// reconnectLoop attempts to reconnect when the connection drops.
func (b *RemoteBackend) reconnectLoop(ctx context.Context) {
	for {
		select {
		case <-b.done:
			return
		case <-ctx.Done():
			return
		case <-b.reconnectCh:
			// Exponential backoff reconnect
			for delay := time.Second; delay <= 30*time.Second; delay *= 2 {
				select {
				case <-b.done:
					return
				case <-ctx.Done():
					return
				default:
				}

				log.WithField("delay", delay).Info("Reconnecting to server...")
				time.Sleep(delay)

				if err := b.connect(ctx); err != nil {
					log.WithError(err).Warn("Reconnect failed")
					continue
				}
				// Restart read pump
				go b.readPump(ctx)
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Management stubs — return nil/error until WS RPC protocol is implemented
// ---------------------------------------------------------------------------

func (b *RemoteBackend) LLMFactory() *LLMFactory                     { return nil }
func (b *RemoteBackend) SettingsService() *SettingsService           { return nil }
func (b *RemoteBackend) MultiSession() *session.MultiTenantSession   { return nil }
func (b *RemoteBackend) BgTaskManager() *tools.BackgroundTaskManager { return nil }
func (b *RemoteBackend) ToolHookChain() *tools.HookChain             { return nil }

func (b *RemoteBackend) SetDirectSend(func(bus.OutboundMessage) (string, error)) {}
func (b *RemoteBackend) SetChannelFinder(func(string) (channel.Channel, bool))   {}
func (b *RemoteBackend) SetChannelPromptProviders(...ChannelPromptProvider)      {}
func (b *RemoteBackend) RegisterCoreTool(tools.Tool)                             {}
func (b *RemoteBackend) IndexGlobalTools()                                       {}
func (b *RemoteBackend) SetEventRouter(*event.Router)                            {}

func (b *RemoteBackend) SetContextMode(string) error {
	return fmt.Errorf("not supported: remote backend does not support SetContextMode")
}
func (b *RemoteBackend) SetMaxIterations(int)               {}
func (b *RemoteBackend) SetMaxConcurrency(int)              {}
func (b *RemoteBackend) SetMaxContextTokens(int)            {}
func (b *RemoteBackend) SetSandbox(tools.Sandbox, string)   {}
func (b *RemoteBackend) GetCardBuilder() *tools.CardBuilder { return nil }

func (b *RemoteBackend) CountInteractiveSessions(string, string) int { return 0 }
func (b *RemoteBackend) ListInteractiveSessions(string, string) []InteractiveSessionInfo {
	return nil
}
func (b *RemoteBackend) InspectInteractiveSession(_ context.Context, _, _, _, _ string, _ int) (string, error) {
	return "", fmt.Errorf("not supported: remote backend does not support InspectInteractiveSession")
}
