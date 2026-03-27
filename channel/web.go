// xbot Web Channel implementation

package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	log "xbot/logger"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Frontend static files are embedded via web_embed.go (variable webDistFS)

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	webSendChBufSize     = 64
	webOfflineMsgBufSize = 50
	webSessionCookieName = "xbot_session"
	webSessionMaxAge     = 30 * 24 * time.Hour // 30 days
)

// ---------------------------------------------------------------------------
// WebConfig (channel-level)
// ---------------------------------------------------------------------------

// WebChannelConfig Web 渠道配置（channel 包内部使用）
type WebChannelConfig struct {
	Host string
	Port int
	DB   *sql.DB // SQLite DB handle for user management and history
}

// ---------------------------------------------------------------------------
// Hub: manages all WebSocket clients
// ---------------------------------------------------------------------------

// Hub 管理所有 WebSocket 连接
type Hub struct {
	mu         sync.RWMutex
	clients    map[string]*Client // senderID → Client
	offline    map[string]*ringBuffer // senderID → offline message buffer
	offMu      sync.Mutex
}

func newHub() *Hub {
	return &Hub{
		clients: make(map[string]*Client),
		offline: make(map[string]*ringBuffer),
	}
}

func (h *Hub) addClient(senderID string, c *Client) {
	h.mu.Lock()
	// Close existing connection for same user
	if old, ok := h.clients[senderID]; ok {
		close(old.done)
	}
	h.clients[senderID] = c
	h.mu.Unlock()

	// Flush offline messages
	h.offMu.Lock()
	if buf, ok := h.offline[senderID]; ok {
		msgs := buf.flush()
		for _, msg := range msgs {
			select {
			case c.sendCh <- msg:
			default:
				// sendCh full, drop message
			}
		}
		delete(h.offline, senderID)
	}
	h.offMu.Unlock()
}

func (h *Hub) removeClient(senderID string, c *Client) {
	h.mu.Lock()
	if existing, ok := h.clients[senderID]; ok && existing == c {
		delete(h.clients, senderID)
	}
	h.mu.Unlock()
}

func (h *Hub) getClient(senderID string) *Client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.clients[senderID]
}

func (h *Hub) sendToClient(senderID string, msg wsMessage) bool {
	c := h.getClient(senderID)
	if c != nil {
		select {
		case c.sendCh <- msg:
			return true
		default:
			// Channel full, buffer offline
		}
	}
	// Buffer as offline message
	h.offMu.Lock()
	buf, ok := h.offline[senderID]
	if !ok {
		buf = newRingBuffer(webOfflineMsgBufSize)
		h.offline[senderID] = buf
	}
	buf.push(msg)
	h.offMu.Unlock()
	return false
}

func (h *Hub) stopAll() {
	h.mu.Lock()
	for id, c := range h.clients {
		close(c.done)
		delete(h.clients, id)
	}
	h.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Client: a single WebSocket connection
// ---------------------------------------------------------------------------

// Client represents a single WebSocket client
type Client struct {
	conn   *websocket.Conn
	sendCh chan wsMessage
	done   chan struct{}
	hub    *Hub
	userID string
}

// ---------------------------------------------------------------------------
// ring buffer for offline messages
// ---------------------------------------------------------------------------

type ringBuffer struct {
	buf  []wsMessage
	size int
	head int
 tail int
	count int
}

func newRingBuffer(size int) *ringBuffer {
	return &ringBuffer{
		buf:  make([]wsMessage, size),
		size: size,
	}
}

func (rb *ringBuffer) push(msg wsMessage) {
	if rb.count == rb.size {
		rb.head = (rb.head + 1) % rb.size
		rb.count--
	}
	rb.buf[rb.tail] = msg
	rb.tail = (rb.tail + 1) % rb.size
	rb.count++
}

func (rb *ringBuffer) flush() []wsMessage {
	result := make([]wsMessage, 0, rb.count)
	for rb.count > 0 {
		result = append(result, rb.buf[rb.head])
		rb.head = (rb.head + 1) % rb.size
		rb.count--
	}
	rb.head = 0
	rb.tail = 0
	return result
}

// ---------------------------------------------------------------------------
// WS protocol messages
// ---------------------------------------------------------------------------

type wsMessage struct {
	Type    string `json:"type"`              // "text", "progress", "card"
	ID      string `json:"id,omitempty"`      // UUID
	Content string `json:"content,omitempty"` // message content
	TS      int64  `json:"ts,omitempty"`      // timestamp
}

type wsClientMessage struct {
	Type    string `json:"type"`
	Content string `json:"content"`
}

// ---------------------------------------------------------------------------
// WebChannel: implements Channel interface
// ---------------------------------------------------------------------------

// WebChannel Web 渠道实现
type WebChannel struct {
	config WebChannelConfig
	msgBus *bus.MessageBus
	hub    *Hub
	server *http.Server

	// Auth
	sessions   map[string]sessionInfo // token → sessionInfo
	sessionsMu sync.RWMutex

	// DB
	db *sql.DB

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup

	// Static files (embedded)
	staticFS fs.FS
}

type sessionInfo struct {
	userID   int
	username string
	expires  time.Time
}

// NewWebChannel 创建 Web 渠道
func NewWebChannel(cfg WebChannelConfig, msgBus *bus.MessageBus) *WebChannel {
	return &WebChannel{
		config:   cfg,
		msgBus:   msgBus,
		hub:      newHub(),
		sessions: make(map[string]sessionInfo),
		db:       cfg.DB,
		stopCh:   make(chan struct{}),
	}
}

// SetStaticFS sets the embedded filesystem for serving the frontend
func (wc *WebChannel) SetStaticFS(f fs.FS) {
	wc.staticFS = f
}

func (wc *WebChannel) Name() string { return "web" }

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

// Start 启动 Web 渠道 HTTP server
func (wc *WebChannel) Start() error {
	mux := http.NewServeMux()

	// WebSocket endpoint
	mux.HandleFunc("/ws", wc.handleWS)

	// Auth API
	mux.HandleFunc("/api/auth/register", wc.handleRegister)
	mux.HandleFunc("/api/auth/login", wc.handleLogin)
	mux.HandleFunc("/api/auth/logout", wc.handleLogout)

	// REST API
	mux.HandleFunc("/api/history", wc.authMiddleware(wc.handleHistory))

	// Static files
	if wc.staticFS != nil {
		mux.HandleFunc("/", wc.handleStatic)
	}

	addr := fmt.Sprintf("%s:%d", wc.config.Host, wc.config.Port)
	wc.server = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.WithFields(log.Fields{
		"host": wc.config.Host,
		"port": wc.config.Port,
	}).Info("Web channel starting...")

	// Start cleanup goroutine for expired sessions
	wc.wg.Add(1)
	go wc.sessionCleanup()

	err := wc.server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Stop 停止 Web 渠道
func (wc *WebChannel) Stop() {
	log.Info("Web channel stopping...")
	close(wc.stopCh)

	wc.hub.stopAll()

	if wc.server != nil {
		ctx, cancel := func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 5*time.Second)
		}()
		_ = wc.server.Shutdown(ctx)
		cancel()
	}

	wc.wg.Wait()
	log.Info("Web channel stopped")
}

// ---------------------------------------------------------------------------
// Send: non-blocking write to WebSocket client
// ---------------------------------------------------------------------------

// Send 发送消息到 Web 客户端（非阻塞）
func (wc *WebChannel) Send(msg bus.OutboundMessage) (string, error) {
	msgID := strings.ReplaceAll(uuid.New().String(), "-", "")

	content := msg.Content
	msgType := "text"

	// __FEISHU_CARD__ protocol adaptation
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		msgType = "card"
		content = convertFeishuCard(content)
	}

	wsMsg := wsMessage{
		Type:    msgType,
		ID:      msgID,
		Content: content,
		TS:      time.Now().Unix(),
	}

	// Send via hub (non-blocking: writes to buffered channel)
	if !wc.hub.sendToClient(msg.ChatID, wsMsg) {
		// Client offline, message buffered in ring buffer
		log.WithField("chat_id", msg.ChatID).Debug("Web client offline, message buffered")
	}

	return msgID, nil
}

// ---------------------------------------------------------------------------
// WebSocket handler
// ---------------------------------------------------------------------------

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func (wc *WebChannel) handleWS(w http.ResponseWriter, r *http.Request) {
	// Authenticate via cookie
	si := wc.validateSession(r)
	if si == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Warn("WebSocket upgrade failed")
		return
	}

	senderID := "web:" + strconv.Itoa(si.userID)

	client := &Client{
		conn:   conn,
		sendCh: make(chan wsMessage, webSendChBufSize),
		done:   make(chan struct{}),
		hub:    wc.hub,
		userID: senderID,
	}

	wc.hub.addClient(senderID, client)
	log.WithFields(log.Fields{
		"sender_id": senderID,
		"username":  si.username,
	}).Info("Web client connected")

	// Write pump
	wc.wg.Add(1)
	go func() {
		defer wc.wg.Done()
		wc.writePump(client)
	}()

	// Read pump (blocks until disconnect)
	wc.readPump(client, si)
}

func (wc *WebChannel) writePump(c *Client) {
	defer c.conn.Close()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case msg, ok := <-c.sendCh:
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteJSON(msg); err != nil {
				log.WithError(err).Debug("WS write error")
				return
			}
		case <-ticker.C:
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			return
		}
	}
}

func (wc *WebChannel) readPump(c *Client, si *sessionInfo) {
	defer func() {
		c.conn.Close()
		close(c.done)
		wc.hub.removeClient(c.userID, c)
		log.WithField("sender_id", c.userID).Info("Web client disconnected")
	}()

	c.conn.SetReadLimit(65536) // 64KB max message
	c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	chatID := c.userID // p2p mode

	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure) {
				log.WithError(err).Debug("WS read error")
			}
			return
		}

		var msg wsClientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.WithError(err).Debug("WS invalid message")
			continue
		}

		if msg.Type != "message" || msg.Content == "" {
			continue
		}

		// Send to message bus
		wc.msgBus.Inbound <- bus.InboundMessage{
			Channel:    "web",
			SenderID:   c.userID,
			SenderName: si.username,
			ChatID:     chatID,
			ChatType:   "p2p",
			Content:    msg.Content,
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
			From:       bus.NewIMAddress("web", c.userID),
			To:         bus.NewIMAddress("web", chatID),
		}
	}
}

// ---------------------------------------------------------------------------
// Static file handler
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleStatic(w http.ResponseWriter, r *http.Request) {
	if wc.staticFS == nil {
		http.NotFound(w, r)
		return
	}

	// Try to serve the file
	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	// Try exact path
	f, err := wc.staticFS.Open(strings.TrimPrefix(path, "/"))
	if err == nil {
		f.Close()
		http.FileServer(http.FS(wc.staticFS)).ServeHTTP(w, r)
		return
	}

	// SPA fallback: serve index.html for non-file paths
	if !strings.Contains(path, ".") {
		r.URL.Path = "/"
		http.FileServer(http.FS(wc.staticFS)).ServeHTTP(w, r)
		return
	}

	http.NotFound(w, r)
}

// ---------------------------------------------------------------------------
// __FEISHU_CARD__ protocol adaptation
// ---------------------------------------------------------------------------

// convertFeishuCard extracts human-readable content from __FEISHU_CARD__ prefixed JSON.
// Best-effort: if extraction fails, returns raw JSON stripped of prefix.
func convertFeishuCard(content string) string {
	// Strip prefix
	jsonStr := strings.TrimPrefix(content, "__FEISHU_CARD__")
	jsonStr = strings.TrimSpace(jsonStr)

	var card map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &card); err != nil {
		return jsonStr // fallback: return raw JSON
	}

	// Try to extract header.title.content
	var result strings.Builder
	if header, ok := card["header"].(map[string]interface{}); ok {
		if title, ok := header["title"].(map[string]interface{}); ok {
			if tc, ok := title["content"].(string); ok && tc != "" {
				result.WriteString("# ")
				result.WriteString(tc)
				result.WriteString("\n\n")
			}
		}
	}

	// Try to extract elements (simplified)
	if elements, ok := card["elements"].([]interface{}); ok {
		for _, elem := range elements {
			if obj, ok := elem.(map[string]interface{}); ok {
				tag, _ := obj["tag"].(string)
				switch tag {
				case "div":
					if text, ok := obj["text"].(string); ok {
						// text might be JSON with content field
						var textObj map[string]string
						if json.Unmarshal([]byte(text), &textObj) == nil {
							if c, ok := textObj["content"]; ok {
								result.WriteString(c)
								result.WriteString("\n")
							}
						} else {
							result.WriteString(text)
							result.WriteString("\n")
						}
					}
				case "markdown":
					if content, ok := obj["content"].(string); ok {
						result.WriteString(content)
						result.WriteString("\n")
					}
				}
			}
		}
	}

	if result.Len() == 0 {
		return jsonStr
	}
	return strings.TrimSpace(result.String())
}

// ---------------------------------------------------------------------------
// Session cleanup
// ---------------------------------------------------------------------------

func (wc *WebChannel) sessionCleanup() {
	defer wc.wg.Done()
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-wc.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			wc.sessionsMu.Lock()
			for token, si := range wc.sessions {
				if now.After(si.expires) {
					delete(wc.sessions, token)
				}
			}
			wc.sessionsMu.Unlock()
		}
	}
}
