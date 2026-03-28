// xbot Web Channel implementation

package channel

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	log "xbot/logger"
	"xbot/storage/sqlite"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Frontend static files are served from an external directory (set via SetStaticDir).
// No go:embed — frontend is deployed independently.

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
	Host             string
	Port             int
	DB               *sql.DB // SQLite DB handle for user management and history
	MemoryWindow     int
	FeishuLinkSecret string // admin token for /api/auth/feishu-link endpoint
}

// WebCallbacks holds callback functions for Web channel API endpoints.
// Injected from main to decouple channel from agent/tools packages.
type WebCallbacks struct {
	// RunnerTokenGet returns the runner connect command for the user ("" if none).
	RunnerTokenGet func(senderID string) string
	// RunnerTokenGenerate generates a new per-user token and returns the connect command.
	RunnerTokenGenerate func(senderID, mode, dockerImage, workspace string) (string, error)
	// RunnerTokenRevoke revokes the user's current token.
	RunnerTokenRevoke func(senderID string) error
	// RegistryBrowse lists available agents/skills in the marketplace.
	RegistryBrowse func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error)
	// RegistryInstall installs a shared entry for the user.
	RegistryInstall func(entryType string, id int64, senderID string) error
	// RegistryListMy lists the user's installed entries.
	RegistryListMy func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error)
	// RegistryUnpublish removes a user's published entry.
	RegistryUnpublish func(entryType, name, senderID string) error
	// RegistryUninstall removes a user-installed entry.
	RegistryUninstall func(entryType, name, senderID string) error
	// LLMList returns available models and current model.
	LLMList func(senderID string) ([]string, string)
	// LLMSet switches the user's model.
	LLMSet func(senderID, model string) error
	// LLMGetConfig returns user's LLM config (provider, baseURL, model, ok).
	LLMGetConfig func(senderID string) (provider, baseURL, model string, ok bool)
	// LLMSetConfig sets user's personal LLM config.
	LLMSetConfig func(senderID, provider, baseURL, apiKey, model string) error
	// LLMDelete reverts user to global LLM config.
	LLMDelete func(senderID string) error
	// LLMGetMaxContext returns the user's max context tokens setting.
	LLMGetMaxContext func(senderID string) int
	// LLMSetMaxContext sets the user's max context tokens setting.
	LLMSetMaxContext func(senderID string, maxContext int) error

	// NormalizeSenderID normalizes sender ID for single-user mode.
	NormalizeSenderID func(senderID string) string
	// RegistryPublish publishes a user's agent/skill to the marketplace.
	RegistryPublish func(entryType, name, senderID string) error
	// SandboxWriteFile writes file data to the user's sandbox at the given path.
	// Returns (sandboxInternalPath, error). sandboxInternalPath is the path inside
	// the sandbox (e.g., /workspace/uploads/file.txt). Returns ("", nil) if no sandbox available.
	SandboxWriteFile func(senderID string, sandboxRelPath string, data []byte, perm os.FileMode) (sandboxPath string, err error)
}

// ---------------------------------------------------------------------------
// Hub: manages all WebSocket clients
// ---------------------------------------------------------------------------

// Hub 管理所有 WebSocket 连接
type Hub struct {
	mu      sync.RWMutex
	clients map[string]*Client     // senderID → Client
	offline map[string]*ringBuffer // senderID → offline message buffer
	offMu   sync.Mutex
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
		old.closeDone()

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

func (c *Client) closeDone() {
	c.closeOnce.Do(func() { close(c.done) })
}

func (h *Hub) stopAll() {
	h.mu.Lock()
	for id, c := range h.clients {
		c.closeDone()
		delete(h.clients, id)
	}
	h.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Client: a single WebSocket connection
// ---------------------------------------------------------------------------

// Client represents a single WebSocket client
type Client struct {
	conn      *websocket.Conn
	sendCh    chan wsMessage
	done      chan struct{}
	closeOnce sync.Once
	hub       *Hub
	userID    string
}

// ---------------------------------------------------------------------------
// ring buffer for offline messages
// ---------------------------------------------------------------------------

type ringBuffer struct {
	buf   []wsMessage
	size  int
	head  int
	tail  int
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
	Type             string             `json:"type"`                          // "text", "progress", "card", "progress_structured"
	ID               string             `json:"id,omitempty"`                  // UUID
	Content          string             `json:"content,omitempty"`             // message content
	TS               int64              `json:"ts,omitempty"`                  // timestamp
	Progress         *WsProgressPayload `json:"progress,omitempty"`            // structured progress data
	ProgressHistory  string             `json:"progress_history,omitempty"`    // JSON-encoded iteration history for completed turns
}

// WsProgressPayload 结构化进度消息负载（对应 agent.StructuredProgress）。
type WsProgressPayload struct {
	Phase          string           `json:"phase,omitempty"`
	Iteration      int              `json:"iteration,omitempty"`
	ActiveTools    []WsToolProgress `json:"active_tools,omitempty"`
	CompletedTools []WsToolProgress `json:"completed_tools,omitempty"`
	Thinking       string           `json:"thinking,omitempty"`
}

// WsToolProgress 单个工具的执行进度（对应 agent.ToolProgress）。
type WsToolProgress struct {
	Name    string `json:"name,omitempty"`
	Label   string `json:"label,omitempty"`
	Status  string `json:"status,omitempty"`
	Elapsed int64  `json:"elapsed_ms,omitempty"` // milliseconds
}

type wsClientMessage struct {
	Type      string   `json:"type"`
	Content   string   `json:"content"`
	FileIDs   []string `json:"file_ids,omitempty"`
	FileNames []string `json:"file_names,omitempty"`
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

	// Callbacks from main
	callbacks WebCallbacks

	// Auth
	sessions   map[string]sessionInfo // token → sessionInfo
	sessionsMu sync.RWMutex

	// DB
	db *sql.DB

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup

	// Static files (external directory)
	staticDir string

	// File uploads directory
	uploadDir string

	// Working directory (workspace) — used to copy uploaded files into sandbox-accessible path
	workDir string
}

type sessionInfo struct {
	userID       int
	username     string
	feishuUserID string // non-empty when logged in via Feishu identity
	expires      time.Time
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

// SetStaticDir sets the directory for serving frontend static files.
func (wc *WebChannel) SetStaticDir(dir string) {
	if dir != "" {
		wc.staticDir = filepath.Clean(dir)
	}
}

// SetUploadDir sets the directory for file uploads.
func (wc *WebChannel) SetUploadDir(dir string) {
	if dir != "" {
		wc.uploadDir = filepath.Clean(dir)
	}
}

// SetWorkDir sets the working directory for sandbox file access.
func (wc *WebChannel) SetWorkDir(dir string) {
	if dir != "" {
		wc.workDir = filepath.Clean(dir)
	}
}

// SetCallbacks injects callback functions from main for API endpoints.
func (wc *WebChannel) SetCallbacks(cb WebCallbacks) {
	wc.callbacks = cb
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
	mux.HandleFunc("/api/auth/feishu-link", wc.handleFeishuLink)
	mux.HandleFunc("/api/auth/feishu-login", wc.handleFeishuLogin)

	// REST API
	mux.HandleFunc("/api/history", wc.authMiddleware(wc.handleHistory))
	mux.HandleFunc("/api/settings", wc.authMiddleware(wc.handleSettings))
	mux.HandleFunc("/api/runner/token", wc.authMiddleware(wc.handleRunnerToken))

	// Market API
	mux.HandleFunc("/api/market", wc.authMiddleware(wc.handleMarket))
	mux.HandleFunc("/api/market/install", wc.authMiddleware(wc.handleMarketInstall))
	mux.HandleFunc("/api/market/uninstall", wc.authMiddleware(wc.handleMarketUninstall))
	mux.HandleFunc("/api/market/my", wc.authMiddleware(wc.handleMarketMy))
	mux.HandleFunc("/api/market/publish", wc.authMiddleware(wc.handleMarketPublish))
	mux.HandleFunc("/api/market/unpublish", wc.authMiddleware(wc.handleMarketUnpublish))

	// LLM Config API
	mux.HandleFunc("/api/llm-config", wc.authMiddleware(wc.handleLLMConfig))
	mux.HandleFunc("/api/llm-config/model", wc.authMiddleware(wc.handleLLMModelSet))
	mux.HandleFunc("/api/llm-max-context", wc.authMiddleware(wc.handleLLMMaxContext))

	// File API
	mux.HandleFunc("/api/files/upload", wc.authMiddleware(wc.handleFileUpload))
	mux.HandleFunc("/api/files/", wc.authMiddleware(wc.handleFileDownload))

	// Static files
	if wc.staticDir != "" {
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
		Type:            msgType,
		ID:              msgID,
		Content:         content,
		TS:              time.Now().Unix(),
		ProgressHistory: msg.Metadata["progress_history"],
	}

	// Send via hub (non-blocking: writes to buffered channel)
	if !wc.hub.sendToClient(msg.ChatID, wsMsg) {
		// Client offline, message buffered in ring buffer
		log.WithField("chat_id", msg.ChatID).Debug("Web client offline, message buffered")
	}

	return msgID, nil
}

// SendProgress 发送结构化进度事件到 Web 客户端（非阻塞）。
// 内部通过 hub 的缓冲通道发送，保持调用路径轻量。
func (wc *WebChannel) SendProgress(chatID string, payload *WsProgressPayload) {
	if payload == nil {
		return
	}

	wsMsg := wsMessage{
		Type:     "progress_structured",
		TS:       time.Now().Unix(),
		Progress: payload,
	}

	if !wc.hub.sendToClient(chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, progress event buffered")
	}
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

	senderID := "web-" + strconv.Itoa(si.userID)
	if wc.callbacks.NormalizeSenderID != nil {
		senderID = wc.callbacks.NormalizeSenderID(senderID)
	}

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
			// Server shutdown — send close frame with GoingAway status
			c.conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"))
			return
		}
	}
}

func (wc *WebChannel) readPump(c *Client, si *sessionInfo) {
	defer func() {
		c.conn.Close()
		c.closeDone()
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

		// Handle message type (default to "message" for backward compatibility)
		if msg.Type == "" {
			msg.Type = "message"
		}

		switch msg.Type {
		case "cancel":
			// Reuse existing /cancel mechanism: push "/cancel" text into msgBus
			wc.msgBus.Inbound <- bus.InboundMessage{
				Channel:    "web",
				SenderID:   c.userID,
				SenderName: si.username,
				ChatID:     chatID,
				ChatType:   "p2p",
				Content:    "/cancel",
				Time:       time.Now(),
				RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
				From:       bus.NewIMAddress("web", c.userID),
				To:         bus.NewIMAddress("web", chatID),
			}
			continue

		case "message":
			if msg.Content == "" && len(msg.FileIDs) == 0 {
				continue
			}

		default:
			continue
		}

		// Send to message bus
		var mediaPaths []string
		content := msg.Content
		if len(msg.FileIDs) > 0 && wc.uploadDir != "" {
			for i, fid := range msg.FileIDs {
				uploadPath := filepath.Join(wc.uploadDir, "web", fid)
				mediaPaths = append(mediaPaths, uploadPath)

				// Use original file name if provided, fallback to fid
				displayName := fid
				if i < len(msg.FileNames) && msg.FileNames[i] != "" {
					displayName = filepath.Base(msg.FileNames[i]) // sanitize: prevent path traversal
				}

				ext := strings.ToLower(filepath.Ext(fid))
				if isImageExt(ext) {
					// For image files, embed as base64 in content so LLM can see them
					if data, err := os.ReadFile(uploadPath); err == nil {
						mimeType := mime.TypeByExtension(ext)
						if mimeType == "" {
							mimeType = http.DetectContentType(data)
						}
						b64 := base64.StdEncoding.EncodeToString(data)
						content += fmt.Sprintf("\n\n![uploaded image](data:%s;base64,%s)", mimeType, b64)
					}
				} else {
					// For non-image files, write to user's sandbox so tools can access them.
					// Try remote sandbox first (via callback), then fall back to local workDir copy.
					fileData, readErr := os.ReadFile(uploadPath)
					if readErr != nil {
						content += fmt.Sprintf("\n\n📎 [附件: %s] (读取文件失败)", displayName)
						continue
					}
					sandboxRelPath := "uploads/" + displayName
					written := false
					// Try remote/server sandbox via callback
					if wc.callbacks.SandboxWriteFile != nil {
						if sandboxPath, err := wc.callbacks.SandboxWriteFile(c.userID, sandboxRelPath, fileData, 0644); err == nil && sandboxPath != "" {
							content += fmt.Sprintf("\n\n📎 [附件: %s] 已下载到沙箱工作目录，路径: %s/%s", displayName, sandboxPath, displayName)
							written = true
						} else if err != nil {
							log.WithError(err).WithField("file", displayName).Warn("Failed to write file to sandbox via callback")
						}
					}
					// Fallback: local workDir copy (for docker sandbox)
					if !written && wc.workDir != "" {
						userWsDir := filepath.Join(wc.workDir, ".xbot", "users", c.userID, "workspace")
						sandboxFileDir := filepath.Join(userWsDir, "uploads")
						sandboxFilePath := filepath.Join(sandboxFileDir, displayName)
						if err := os.MkdirAll(sandboxFileDir, 0755); err == nil {
							if err := copyFile(uploadPath, sandboxFilePath); err == nil {
								content += fmt.Sprintf("\n\n📎 [附件: %s] 已下载到沙箱工作目录，路径: /workspace/uploads/%s", displayName, displayName)
								written = true
							} else {
								content += fmt.Sprintf("\n\n📎 [附件: %s] 路径: %s (复制到工作目录失败)", displayName, uploadPath)
							}
						} else {
							content += fmt.Sprintf("\n\n📎 [附件: %s] 路径: %s (创建目录失败)", displayName, uploadPath)
						}
					}
					if !written {
						content += fmt.Sprintf("\n\n📎 [附件: %s] 路径: %s", displayName, uploadPath)
					}

				}
			}
		} else if len(msg.FileIDs) > 0 && wc.uploadDir == "" {
			log.Warn("Web channel received file_ids but uploadDir is not configured")
			for _, fid := range msg.FileIDs {
				content += fmt.Sprintf("\n\n📎 [附件: %s]", fid)
			}
		}

		metadata := map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional}
		if si.feishuUserID != "" {
			metadata["feishu_user_id"] = si.feishuUserID
		}

		wc.msgBus.Inbound <- bus.InboundMessage{
			Channel:    "web",
			SenderID:   c.userID,
			SenderName: si.username,
			ChatID:     chatID,
			ChatType:   "p2p",
			Content:    content,
			Media:      mediaPaths,
			Time:       time.Now(),
			RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
			From:       bus.NewIMAddress("web", c.userID),
			To:         bus.NewIMAddress("web", chatID),
			Metadata:   metadata,
		}
	}
}

// ---------------------------------------------------------------------------
// Static file handler
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleStatic(w http.ResponseWriter, r *http.Request) {
	if wc.staticDir == "" {
		http.NotFound(w, r)
		return
	}

	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	// Try exact path
	fullPath := filepath.Join(wc.staticDir, path)
	if _, err := os.Stat(fullPath); err == nil {
		http.FileServer(http.Dir(wc.staticDir)).ServeHTTP(w, r)
		return
	}

	// SPA fallback: serve index.html for non-file paths
	if !strings.Contains(path, ".") {
		r.URL.Path = "/"
		http.FileServer(http.Dir(wc.staticDir)).ServeHTTP(w, r)
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

// isImageExt returns true if the file extension is a common image format.
func isImageExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg", ".tiff", ".tif":
		return true
	}
	return false
}
