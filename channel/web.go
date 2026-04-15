// xbot Web Channel implementation

package channel

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	log "xbot/logger"
	"xbot/storage/sqlite"
	"xbot/tools"

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
	maxBodySize          = 1 << 20             // 1MB maximum request body size
)

// limitBodySize wraps a handler to limit request body size.
func limitBodySize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// WebConfig (channel-level)
// ---------------------------------------------------------------------------

// WebChannelConfig Web 渠道配置（channel 包内部使用）
type WebChannelConfig struct {
	Host             string
	Port             int
	DB               *sql.DB // SQLite DB handle for user management and history
	FeishuLinkSecret string  // admin token for /api/auth/feishu-link endpoint
	InviteOnly       bool    // 禁止自主注册，新账号只能由 admin 创建
	PublicURL        string  // 对外访问地址，用于生成 Runner 连接命令
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
	// RunnerList lists all runners for a user with online status.
	RunnerList func(senderID string) ([]tools.RunnerInfo, error)
	// RunnerCreate creates a new named runner and returns the connect command.
	RunnerCreate func(senderID, name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error)
	// RunnerDelete deletes a named runner.
	RunnerDelete func(senderID, name string) error
	// RunnerGetActive returns the active runner name for the user.
	RunnerGetActive func(senderID string) (string, error)
	// RunnerSetActive sets the active runner for the user.
	RunnerSetActive func(senderID, name string) error
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
	// IsProcessing returns true if the backend is actively processing a request for the user.
	IsProcessing func(senderID string) bool
	// LLMSetConfig sets user's personal LLM config.
	LLMSetConfig func(senderID, provider, baseURL, apiKey, model string, maxOutputTokens int, thinkingMode string) error
	// LLMDelete reverts user to global LLM config.
	LLMDelete func(senderID string) error
	// LLMGetMaxContext returns the user's max context tokens setting.
	LLMGetMaxContext func(senderID string) int
	// LLMSetMaxContext sets the user's max context tokens setting.
	LLMSetMaxContext func(senderID string, maxContext int) error

	// RegistryPublish publishes a user's agent/skill to the marketplace.
	RegistryPublish func(entryType, name, senderID string) error
	// SandboxWriteFile writes file data to the user's sandbox at the given path.
	// Returns (sandboxInternalPath, error). sandboxInternalPath is the path inside
	// the sandbox (e.g., /workspace/uploads/file.txt). Returns ("", nil) if no sandbox available.
	SandboxWriteFile func(senderID string, sandboxRelPath string, data []byte, perm os.FileMode) (sandboxPath string, err error)
	// RunnerStatusNotify is called when a runner connects/disconnects.
	// Used by main to wire up real-time status push to WebChannel.
	RunnerStatusNotify func(senderID, runnerName string, online bool)
	// SyncProgressNotify is called when runner sync progress is reported.
	SyncProgressNotify func(senderID, phase, message string)
	// RPCHandler handles RPC requests from CLI remote clients.
	// The method string identifies the operation; params is the JSON-encoded request body.
	// Returns JSON-encoded result or an error.
	RPCHandler func(method string, params json.RawMessage) (json.RawMessage, error)
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
	Type            string             `json:"type"`                       // "text", "progress", "card", "progress_structured", "user_echo", "ask_user", "stream_content", "rpc_response"
	ID              string             `json:"id,omitempty"`               // UUID or RPC request ID
	Content         string             `json:"content,omitempty"`          // message content
	OriginalContent string             `json:"original_content,omitempty"` // user's original text before file processing (for user_echo matching)
	TS              int64              `json:"ts,omitempty"`               // timestamp
	Progress        *WsProgressPayload `json:"progress,omitempty"`         // structured progress data
	ProgressHistory string             `json:"progress_history,omitempty"` // JSON-encoded iteration history for completed turns
	// RPC response fields (used when Type == "rpc_response")
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// WsProgressPayload — structured progress data (corresponds to agent.StructuredProgress).
type WsProgressPayload struct {
	Phase          string              `json:"phase,omitempty"`
	Iteration      int                 `json:"iteration,omitempty"`
	ActiveTools    []WsToolProgress    `json:"active_tools,omitempty"`
	CompletedTools []WsToolProgress    `json:"completed_tools,omitempty"`
	Thinking       string              `json:"thinking,omitempty"`
	SubAgents      []WsSubAgent        `json:"sub_agents,omitempty"`
	TokenUsage     *WsTokenUsage       `json:"token_usage,omitempty"`
	Todos          []WsTodoItem        `json:"todos,omitempty"`
	Questions      []WsAskUserQuestion `json:"questions,omitempty"`
	RequestID      string              `json:"request_id,omitempty"`
	// StreamContent carries accumulated LLM streaming text (for CLI RemoteBackend).
	StreamContent          string `json:"stream_content,omitempty"`
	ReasoningStreamContent string `json:"reasoning_stream_content,omitempty"`
}

// GetReasoningStreamContent returns the ReasoningStreamContent field.
// Used by RemoteBackend to extract reasoning from stream_content messages.
func (p *WsProgressPayload) GetReasoningStreamContent() string {
	if p == nil {
		return ""
	}
	return p.ReasoningStreamContent
}

// WsToolProgress 单个工具的执行进度（对应 agent.ToolProgress）。
type WsToolProgress struct {
	Name      string `json:"name,omitempty"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status,omitempty"`
	Elapsed   int64  `json:"elapsed_ms,omitempty"` // milliseconds
	Iteration int    `json:"iteration,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// WsSubAgent 子 Agent 的结构化进度状态。
type WsSubAgent struct {
	Role     string       `json:"role"`
	Status   string       `json:"status"` // "running" | "done" | "error"
	Desc     string       `json:"desc,omitempty"`
	Children []WsSubAgent `json:"children,omitempty"`
}

// WsTokenUsage Token 使用量快照（对应 agent.TokenUsageSnapshot）。
type WsTokenUsage struct {
	PromptTokens     int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `json:"completion_tokens,omitempty"`
	TotalTokens      int64 `json:"total_tokens,omitempty"`
	CacheHitTokens   int64 `json:"cache_hit_tokens,omitempty"`
}

// WsTodoItem represents a TODO item for web display.
type WsTodoItem struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
	Done bool   `json:"done"`
}

type wsClientMessage struct {
	Type       string   `json:"type"`
	Content    string   `json:"content"`
	FileIDs    []string `json:"file_ids,omitempty"`
	FileNames  []string `json:"file_names,omitempty"`
	FileSizes  []int64  `json:"file_sizes,omitempty"`
	UploadKeys []string `json:"upload_keys,omitempty"` // OSS upload keys (for qiniu mode)
	FileMimes  []string `json:"file_mimes,omitempty"`  // MIME types
}

// WsAskUserPayload is the payload for "ask_user" WS messages (agent needs user input).
type WsAskUserPayload struct {
	Questions []WsAskUserQuestion `json:"questions"`
	RequestID string              `json:"request_id,omitempty"`
}

// WsAskUserQuestion represents a single question in the AskUser flow.
type WsAskUserQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
}

// WsAskUserResponse is the client response to an ask_user prompt.
type WsAskUserResponse struct {
	Answers   map[string]string `json:"answers"`   // question index -> answer
	Cancelled bool              `json:"cancelled"` // true = user cancelled
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

	// Working directory (workspace) — used to copy uploaded files into sandbox-accessible path
	workDir string

	// OSS provider for file storage (local or qiniu)
	ossProvider OSSProvider
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

// SetWorkDir sets the working directory for sandbox file access.
func (wc *WebChannel) SetWorkDir(dir string) {
	if dir != "" {
		wc.workDir = filepath.Clean(dir)
	}
}

// SetOSSProvider sets the OSS provider for file storage.
func (wc *WebChannel) SetOSSProvider(p OSSProvider) {
	wc.ossProvider = p
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
	mux.HandleFunc("/api/auth/register", limitBodySize(wc.handleRegister))
	mux.HandleFunc("/api/auth/login", limitBodySize(wc.handleLogin))
	mux.HandleFunc("/api/auth/logout", wc.handleLogout)
	mux.HandleFunc("/api/auth/feishu-link", limitBodySize(wc.handleFeishuLink))
	mux.HandleFunc("/api/auth/feishu-login", limitBodySize(wc.handleFeishuLogin))
	mux.HandleFunc("/api/auth/config", wc.handleAuthConfig)

	// REST API
	mux.HandleFunc("/api/history", wc.authMiddleware(wc.handleHistory))
	mux.HandleFunc("/api/settings", wc.authMiddleware(wc.handleSettings))
	mux.HandleFunc("/api/runner/token", wc.authMiddleware(wc.handleRunnerToken))
	mux.HandleFunc("/api/search", wc.authMiddleware(wc.handleSearch))

	// Multi-runner API
	mux.HandleFunc("/api/runners", wc.authMiddleware(wc.handleRunners))
	mux.HandleFunc("/api/runners/active", wc.authMiddleware(wc.handleRunnerActive))
	mux.HandleFunc("/api/runners/", wc.authMiddleware(wc.handleRunnerByName))

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

	// Static files
	if wc.staticDir != "" {
		mux.HandleFunc("/", wc.handleStatic)
	}

	addr := fmt.Sprintf("%s:%d", wc.config.Host, wc.config.Port)
	wc.server = &http.Server{
		Addr:         addr,
		Handler:      wc.securityHeadersMiddleware(mux),
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
		content = ConvertFeishuCard(content)
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

	// AskUser: agent needs user input
	if msg.WaitingUser {
		askPayload := &WsProgressPayload{}
		if msg.Metadata != nil {
			askPayload.RequestID = msg.Metadata["request_id"]
			if qJSON := msg.Metadata["ask_questions"]; qJSON != "" {
				var qs []WsAskUserQuestion
				if json.Unmarshal([]byte(qJSON), &qs) == nil {
					askPayload.Questions = qs
				}
			}
		}
		askMsg := wsMessage{
			Type:     "ask_user",
			ID:       msgID,
			TS:       time.Now().Unix(),
			Progress: askPayload,
		}
		wc.hub.sendToClient(msg.ChatID, askMsg)
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

// SendStreamContent sends streaming LLM content to a specific client.
// Used by CLI RemoteBackend connections to push token-by-token streaming.
func (wc *WebChannel) SendStreamContent(chatID, content, reasoning string) {
	if content == "" && reasoning == "" {
		return
	}
	wsMsg := wsMessage{
		Type: "stream_content",
		TS:   time.Now().Unix(),
		Progress: &WsProgressPayload{
			StreamContent:          content,
			ReasoningStreamContent: reasoning,
		},
	}
	_ = wc.hub.sendToClient(chatID, wsMsg) // stream events are ephemeral, safe to drop
}

// PushRunnerStatus pushes a runner online/offline status change to the Web client.
func (wc *WebChannel) PushRunnerStatus(chatID, runnerName string, online bool) {
	wsMsg := wsMessage{
		Type: "runner_status",
		TS:   time.Now().Unix(),
		Content: func() string {
			b, _ := json.Marshal(map[string]interface{}{"runner_name": runnerName, "online": online})
			return string(b)
		}(),
	}
	if !wc.hub.sendToClient(chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, runner status buffered")
	}
}

// PushSyncProgress pushes a sync progress notification to the Web client.
func (wc *WebChannel) PushSyncProgress(chatID, phase, message string) {
	wsMsg := wsMessage{
		Type: "sync_progress",
		TS:   time.Now().Unix(),
		Content: func() string {
			b, _ := json.Marshal(map[string]interface{}{"phase": phase, "message": message})
			return string(b)
		}(),
	}
	if !wc.hub.sendToClient(chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, sync progress buffered")
	}
}

// ---------------------------------------------------------------------------
// WebSocket handler
// ---------------------------------------------------------------------------

// wsUpgrader returns a WebSocket upgrader with origin checking.
func (wc *WebChannel) wsUpgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			// Allow same-origin or configured public URL
			if wc.config.PublicURL != "" {
				if pu, err := url.Parse(wc.config.PublicURL); err == nil && u.Host == pu.Host {
					return true
				}
			}
			return u.Host == r.Host
		},
	}
}

func (wc *WebChannel) handleWS(w http.ResponseWriter, r *http.Request) {
	var senderID, username string
	var si *sessionInfo

	// Support token-based auth for CLI clients (RemoteBackend).
	// Query params: ?token=<runner_token>&client_type=cli
	if token := r.URL.Query().Get("token"); token != "" && r.URL.Query().Get("client_type") == "cli" {
		var err error
		senderID, err = wc.validateCLIToken(token)
		if err != nil {
			log.WithError(err).Warn("CLI token auth failed")
			jsonErrorResponse(w, http.StatusUnauthorized, "invalid token")
			return
		}
		username = "cli:" + senderID
	} else {
		// Authenticate via cookie (web browser clients)
		si = wc.validateSession(r)
		if si == nil {
			jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		senderID = "web-" + strconv.Itoa(si.userID)
		// If linked to Feishu account, use Feishu identity directly.
		// This makes the web user share the same session/persona/workspace/skills/agents
		// as their Feishu account — effectively the same user.
		if si.feishuUserID != "" {
			senderID = si.feishuUserID
		}
		username = si.username
	}

	// Upgrade to WebSocket
	conn, err := wc.wsUpgrader().Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Warn("WebSocket upgrade failed")
		return
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
		"username":  username,
	}).Info("Web client connected")

	// Write pump
	wc.wg.Add(1)
	go func() {
		defer wc.wg.Done()
		wc.writePump(client)
	}()

	// Read pump (blocks until disconnect)
	// si is nil for CLI token auth; readPump uses it only for username lookup
	wc.readPump(client, si)
}

// validateCLIToken validates a runner token and returns the associated senderID.
// CLI RemoteBackend uses runner tokens for authentication — each token maps to a user.
func (wc *WebChannel) validateCLIToken(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty token")
	}
	db := tools.GetRunnerTokenDB()
	if db == nil {
		return "", fmt.Errorf("runner token auth not available")
	}
	store := tools.NewRunnerTokenStore(db)
	userID := store.FindByTokenInRunnerTokens(token)
	if userID == "" {
		return "", fmt.Errorf("invalid token")
	}
	return userID, nil
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

	// Resolve username safely (si is nil for CLI token-authed clients)
	username := "cli-remote"
	var feishuUserID string
	if si != nil {
		username = si.username
		feishuUserID = si.feishuUserID
	}

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
				SenderName: username,
				ChatID:     chatID,
				ChatType:   "p2p",
				Content:    "/cancel",
				Time:       time.Now(),
				RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
				From:       bus.NewIMAddress("web", c.userID),
				To:         bus.NewIMAddress("web", chatID),
			}
			continue
		case "rpc":
			// CLI RemoteBackend RPC request — dispatch to server-side handler
			if wc.callbacks.RPCHandler == nil {
				continue
			}
			var rpcReq struct {
				ID     string          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(raw, &rpcReq); err != nil {
				log.WithError(err).Debug("Invalid RPC message from CLI client")
				continue
			}
			result, rpcErr := wc.callbacks.RPCHandler(rpcReq.Method, rpcReq.Params)
			rpcMsg := wsMessage{Type: "rpc_response", ID: rpcReq.ID}
			if rpcErr != nil {
				rpcMsg.Error = rpcErr.Error()
			} else if result != nil {
				rpcMsg.Result = result
			}
			select {
			case c.sendCh <- rpcMsg:
			default:
				log.Warn("RPC response channel full, dropping response to CLI client")
			}
			continue
		case "message":
			if msg.Content == "" && len(msg.UploadKeys) == 0 {
				continue
			}

			var mediaPaths []string
			originalContent := msg.Content
			content := msg.Content

			// Handle OSS upload_keys: files already uploaded to cloud by frontend
			// Web uploads MUST go through OSS — local file storage is never allowed for security
			if len(msg.UploadKeys) > 0 && wc.ossProvider != nil {
				for i, key := range msg.UploadKeys {
					displayName := key
					if i < len(msg.FileNames) && msg.FileNames[i] != "" {
						displayName = filepath.Base(msg.FileNames[i])
					}
					var fileSize int64
					if i < len(msg.FileSizes) {
						fileSize = msg.FileSizes[i]
					}

					// Get signed download URL (private OSS requires signed URLs with TTL)
					downloadURL, err := wc.ossProvider.GetDownloadURL(key)
					if err != nil {
						log.WithError(err).WithField("key", key).Warn("Failed to get download URL for OSS file")
						content += fmt.Sprintf("\n\n📎 [用户上传文件: %s] (获取下载链接失败)", displayName)
						continue
					}

					ext := strings.ToLower(filepath.Ext(displayName))
					if isImageExt(ext) {
						content += fmt.Sprintf("\n\n<image url=\"%s\" name=\"%s\" size=\"%d\" />\n![%s](%s)", downloadURL, displayName, fileSize, displayName, downloadURL)
					} else {
						content += fmt.Sprintf("\n\n<file name=\"%s\" url=\"%s\" size=\"%d\" />", displayName, downloadURL, fileSize)
					}
				}
			}

			metadata := map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional}

			if feishuUserID != "" {
				metadata["feishu_user_id"] = feishuUserID
			}

			// Echo back complete user message (with file info) so frontend can update optimistic message
			if content != originalContent && len(msg.UploadKeys) > 0 {
				echoMsg := wsMessage{
					Type:            "user_echo",
					Content:         content,
					OriginalContent: originalContent,
					TS:              time.Now().Unix(),
				}
				wc.hub.sendToClient(chatID, echoMsg)
			}

			// Eagerly save user message so history API can return it during processing.
			// Skip bang commands (! prefix) — they should never be persisted.
			trimmed := strings.TrimSpace(content)
			if len(trimmed) <= 1 || trimmed[0] != '!' {
				_ = eagerSaveUserMsg(wc.db, c.userID, content)
				metadata["user_msg_eager_saved"] = "true"
			}

			wc.msgBus.Inbound <- bus.InboundMessage{
				Channel:    "web",
				SenderID:   c.userID,
				SenderName: username,
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
		case "ask_user_response":
			var resp WsAskUserResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				log.WithError(err).Debug("WS invalid ask_user_response")
				continue
			}
			if resp.Cancelled {
				// User cancelled — send /cancel equivalent
				wc.msgBus.Inbound <- bus.InboundMessage{
					Channel:    "web",
					SenderID:   c.userID,
					SenderName: username,
					ChatID:     chatID,
					ChatType:   "p2p",
					Content:    "/cancel",
					Time:       time.Now(),
					RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
					From:       bus.NewIMAddress("web", c.userID),
					To:         bus.NewIMAddress("web", chatID),
				}
			} else {
				// Format answers as indexed Q/A pairs
				var parts []string
				for idx, ans := range resp.Answers {
					parts = append(parts, fmt.Sprintf("Q%s: %s", idx, ans))
				}
				content := strings.Join(parts, "\n\n")
				wc.msgBus.Inbound <- bus.InboundMessage{
					Channel:    "web",
					SenderID:   c.userID,
					SenderName: username,
					ChatID:     chatID,
					ChatType:   "p2p",
					Content:    content,
					Time:       time.Now(),
					RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
					From:       bus.NewIMAddress("web", c.userID),
					To:         bus.NewIMAddress("web", chatID),
					Metadata:   map[string]string{"ask_user_answered": "true"},
				}
			}
		default:
			log.WithField("type", msg.Type).Debug("WS unknown message type")
		}
	}

}

// ---------------------------------------------------------------------------
// Security headers middleware
// ---------------------------------------------------------------------------

// securityHeadersMiddleware wraps an http.Handler with security response headers.
func (wc *WebChannel) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Build img-src with OSS domain whitelist (if configured)
		imgSrc := "'self' data: blob:"
		if wc.ossProvider != nil {
			if d := wc.ossProvider.Domain(); d != "" {
				imgSrc += " " + d
			}
		}

		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline' 'unsafe-eval'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"img-src "+imgSrc+"; "+
				"connect-src 'self' ws: wss:; "+
				"frame-ancestors 'none'",
		)
		next.ServeHTTP(w, r)
	})
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

	// Clean path to prevent directory traversal
	cleanPath := filepath.Clean(path)
	absPath := filepath.Join(wc.staticDir, cleanPath)

	// Ensure the resolved path is within the static directory
	absStaticDir, err := filepath.Abs(wc.staticDir)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "internal error")
		return
	}
	absResolved, err := filepath.Abs(absPath)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid path")
		return
	}
	if !strings.HasPrefix(absResolved, absStaticDir+string(os.PathSeparator)) && absResolved != absStaticDir {
		http.NotFound(w, r)
		return
	}

	// Try exact path
	if _, err := os.Stat(absResolved); err == nil {
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

// eagerSaveUserMsg persists a user message to session_messages immediately
// so that a page-refresh can recover it while the backend is still processing.
func eagerSaveUserMsg(db *sql.DB, userID string, content string) error {
	var tenantID int64
	if err := db.QueryRow(
		"SELECT id FROM tenants WHERE channel = 'web' AND chat_id = ?", userID,
	).Scan(&tenantID); err != nil {
		return err
	}
	// Use INSERT ... WHERE NOT EXISTS to avoid duplicate in race condition
	_, err := db.Exec(`INSERT INTO session_messages (tenant_id, role, content)
		SELECT ?, 'user', ?
		WHERE NOT EXISTS (
			SELECT 1 FROM session_messages
			WHERE tenant_id = ? AND role = 'user' AND content = ?
			ORDER BY id DESC LIMIT 1
		)`, tenantID, content, tenantID, content)
	return err
}
