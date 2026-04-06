package channel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/bus"
	log "xbot/logger"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Cache entry types with expiry tracking
// ---------------------------------------------------------------------------

// msgSeqEntry tracks msg_seq state per conversation with last-access time.
type msgSeqEntry struct {
	seq      int
	lastUsed time.Time
}

// chatTypeEntry tracks chat type with last-access time for expiry.
type chatTypeEntry struct {
	chatType string
	lastUsed time.Time
}

const (
	// cacheExpiry is the duration after which cache entries are eligible for eviction.
	cacheExpiry = 30 * time.Minute
	// cacheMaxEntries is the safety threshold; when exceeded, cleanup is triggered.
	cacheMaxEntries = 10000
	// cacheMaxScanPerCleanup limits iterations per cleanup call to avoid holding the lock too long.
	cacheMaxScanPerCleanup = 500
)

// ---------------------------------------------------------------------------
// QQ Bot intent bit flags
// ---------------------------------------------------------------------------

const (
	qqIntentGuildMembers       = 1 << 1
	qqIntentDirectMessage      = 1 << 12
	qqIntentGroupAndC2C        = 1 << 25
	qqIntentPublicGuildMessage = 1 << 30
)

// intentLevels 从高到低尝试的 intent 组合
var intentLevels = []struct {
	name  string
	value int
}{
	{"full (guild+dm+group+c2c)", qqIntentPublicGuildMessage | qqIntentDirectMessage | qqIntentGroupAndC2C},
	{"group+channel", qqIntentPublicGuildMessage | qqIntentGroupAndC2C},
	{"channel-only", qqIntentPublicGuildMessage | qqIntentGuildMembers},
}

// ---------------------------------------------------------------------------
// WebSocket op codes
// ---------------------------------------------------------------------------

const (
	qqOpDispatch       = 0
	qqOpHeartbeat      = 1
	qqOpIdentify       = 2
	qqOpResume         = 6
	qqOpReconnect      = 7
	qqOpInvalidSession = 9
	qqOpHello          = 10
	qqOpHeartbeatACK   = 11
)

// ---------------------------------------------------------------------------
// Reconnect strategy
// ---------------------------------------------------------------------------

var reconnectDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

const maxReconnectAttempts = 100

// quickDisconnectThreshold: if 3 disconnects happen within this window each,
// we wait 60s before reconnecting.
const quickDisconnectWindow = 5 * time.Second
const quickDisconnectCount = 3

// ---------------------------------------------------------------------------
// QQ API endpoints
// ---------------------------------------------------------------------------

const (
	qqTokenURL   = "https://bots.qq.com/app/getAppAccessToken"
	qqGatewayURL = "https://api.sgroup.qq.com/gateway"
	qqAPIBase    = "https://api.sgroup.qq.com"
)

// ---------------------------------------------------------------------------
// QQConfig 配置
// ---------------------------------------------------------------------------

// QQConfig QQ 机器人渠道配置
type QQConfig struct {
	AppID        string   // QQ Bot App ID
	ClientSecret string   // QQ Bot Client Secret
	AllowFrom    []string // 允许的用户 openid 白名单（空则允许所有人）
}

// ---------------------------------------------------------------------------
// QQChannel 实现
// ---------------------------------------------------------------------------

// QQChannel QQ 机器人渠道实现
type QQChannel struct {
	WSChannelBase

	config QQConfig
	msgBus *bus.MessageBus

	running atomic.Bool

	// Session state
	sessionID   string
	lastSeq     atomic.Int64
	intentLevel int // index into intentLevels

	// Token management
	accessToken   string
	tokenExpireAt time.Time
	tokenMu       sync.Mutex

	// Heartbeat
	heartbeatInterval time.Duration
	heartbeatStop     chan struct{}
	heartbeatACK      atomic.Bool

	// msg_seq tracking per conversation for replies
	msgSeqMap map[string]msgSeqEntry
	msgSeqMu  sync.Mutex

	// chat type cache: chatID -> "c2c" | "group" | "guild"
	chatTypeCache map[string]chatTypeEntry
	chatTypeMu    sync.RWMutex

	// Markdown support: disabled after first failure (auto-fallback to text)
	markdownDisabled atomic.Bool
}

// NewQQChannel 创建 QQ 渠道
func NewQQChannel(cfg QQConfig, msgBus *bus.MessageBus) *QQChannel {
	return &QQChannel{
		WSChannelBase: NewWSChannelBase(1000, quickDisconnectWindow, quickDisconnectCount),
		config:        cfg,
		msgBus:        msgBus,
		msgSeqMap:     make(map[string]msgSeqEntry),
		chatTypeCache: make(map[string]chatTypeEntry),
	}
}

func (q *QQChannel) Name() string { return "qq" }

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

// Start 启动 QQ 渠道，阻塞运行直到 Stop 被调用
func (q *QQChannel) Start() error {
	if q.config.AppID == "" || q.config.ClientSecret == "" {
		return fmt.Errorf("qq app_id and client_secret are required")
	}

	q.running.Store(true)
	log.Info("QQ bot starting...")

	attempt := 0
	for q.running.Load() {
		if attempt >= maxReconnectAttempts {
			return fmt.Errorf("qq: exceeded max reconnect attempts (%d)", maxReconnectAttempts)
		}

		err := q.connectAndRun()
		if !q.running.Load() {
			return nil // graceful shutdown
		}

		if err != nil {
			log.WithError(err).Warn("QQ: WebSocket session ended")
		}

		// Quick disconnect detection
		if q.isQuickDisconnectLoop() {
			log.Warn("QQ: rapid disconnect loop detected, waiting 60s")
			if !q.sleepOrStop(60 * time.Second) {
				return nil
			}
			attempt++
			continue
		}

		delay := reconnectDelays[attempt%len(reconnectDelays)]
		if attempt >= len(reconnectDelays) {
			delay = reconnectDelays[len(reconnectDelays)-1]
		}

		log.WithFields(log.Fields{
			"attempt": attempt + 1,
			"delay":   delay,
		}).Info("QQ: reconnecting...")

		if !q.sleepOrStop(delay) {
			return nil
		}
		attempt++
	}
	return nil
}

// Stop 停止 QQ 渠道
func (q *QQChannel) Stop() {
	if !q.running.Load() {
		return
	}
	q.running.Store(false)
	close(q.stopCh)
	q.closeConn()
	log.Info("QQ bot stopped")
}

// ---------------------------------------------------------------------------
// Token management
// ---------------------------------------------------------------------------

// getToken 获取有效的 access_token，必要时刷新
func (q *QQChannel) getToken() (string, error) {
	q.tokenMu.Lock()
	defer q.tokenMu.Unlock()

	// 提前 5 分钟刷新
	if q.accessToken != "" && time.Now().Before(q.tokenExpireAt.Add(-5*time.Minute)) {
		return q.accessToken, nil
	}

	body, _ := json.Marshal(map[string]string{
		"appId":        q.config.AppID,
		"clientSecret": q.config.ClientSecret,
	})

	resp, err := http.Post(qqTokenURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("qq: token request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("qq: read token response: %w", err)
	}

	var result struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   string `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("qq: parse token response: %w (body: %s)", err, string(data))
	}
	if result.AccessToken == "" {
		return "", fmt.Errorf("qq: empty access_token in response: %s", string(data))
	}

	// expires_in is a string of seconds
	expSec := 7200 // default 2h
	if result.ExpiresIn != "" {
		fmt.Sscanf(result.ExpiresIn, "%d", &expSec)
	}

	q.accessToken = result.AccessToken
	q.tokenExpireAt = time.Now().Add(time.Duration(expSec) * time.Second)

	log.WithField("expires_in", expSec).Info("QQ: access token refreshed")
	return q.accessToken, nil
}

// authHeader 返回 Authorization header 值
func (q *QQChannel) authHeader() (string, error) {
	token, err := q.getToken()
	if err != nil {
		return "", err
	}
	return "QQBot " + token, nil
}

// ---------------------------------------------------------------------------
// WebSocket gateway
// ---------------------------------------------------------------------------

// getGatewayURL 获取 WebSocket 网关地址
func (q *QQChannel) getGatewayURL() (string, error) {
	auth, err := q.authHeader()
	if err != nil {
		return "", err
	}

	req, _ := http.NewRequest("GET", qqGatewayURL, nil)
	req.Header.Set("Authorization", auth)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("qq: gateway request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("qq: read gateway response: %w", err)
	}

	var result struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", fmt.Errorf("qq: parse gateway response: %w (body: %s)", err, string(data))
	}
	if result.URL == "" {
		return "", fmt.Errorf("qq: empty gateway URL in response: %s", string(data))
	}

	log.WithField("url", result.URL).Debug("QQ: gateway URL obtained")
	return result.URL, nil
}

// ---------------------------------------------------------------------------
// Connect and run main loop
// ---------------------------------------------------------------------------

// connectAndRun 建立 WebSocket 连接并运行消息循环，返回时表示连接断开
func (q *QQChannel) connectAndRun() error {
	gwURL, err := q.getGatewayURL()
	if err != nil {
		return fmt.Errorf("get gateway: %w", err)
	}

	conn, _, err := websocket.DefaultDialer.Dial(gwURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}

	q.connMu.Lock()
	q.conn = conn
	q.connMu.Unlock()

	defer q.closeConn()

	connectTime := time.Now()

	// Step 1: Wait for Hello (op:10)
	if err := q.waitForHello(); err != nil {
		return fmt.Errorf("hello: %w", err)
	}

	// Step 2: Identify or Resume
	if q.sessionID != "" && q.lastSeq.Load() > 0 {
		if err := q.sendResume(); err != nil {
			log.WithError(err).Warn("QQ: resume failed, will re-identify")
			q.sessionID = ""
			q.lastSeq.Store(0)
			if err := q.sendIdentify(); err != nil {
				return fmt.Errorf("identify: %w", err)
			}
		}
	} else {
		if err := q.sendIdentify(); err != nil {
			return fmt.Errorf("identify: %w", err)
		}
	}

	// Step 3: Start heartbeat
	q.startHeartbeat()
	defer q.stopHeartbeat()

	// Step 4: Read messages
	for q.running.Load() {
		_, data, err := conn.ReadMessage()
		if err != nil {
			q.recordDisconnect(connectTime)
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return fmt.Errorf("ws closed: %w", err)
			}
			return fmt.Errorf("ws read: %w", err)
		}

		if err := q.handleMessage(data); err != nil {
			// Some errors are fatal (need reconnect)
			if isFatalWSError(err) {
				return err
			}
			log.WithError(err).Warn("QQ: message handling error")
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// WebSocket message types
// ---------------------------------------------------------------------------

// qqWSMessage represents a QQ WebSocket message
type qqWSMessage struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
	S  *int64          `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
}

// qqHelloData op:10 payload
type qqHelloData struct {
	HeartbeatInterval int `json:"heartbeat_interval"`
}

// qqReadyData op:0 READY payload
type qqReadyData struct {
	SessionID string `json:"session_id"`
}

// qqAttachment 富媒体文件附件
type qqAttachment struct {
	ContentType string `json:"content_type"` // "image/jpeg", "image/png", "image/gif", "file", "video/mp4", "voice"
	Filename    string `json:"filename"`
	Height      int    `json:"height,omitempty"`
	Width       int    `json:"width,omitempty"`
	Size        int    `json:"size,omitempty"`
	URL         string `json:"url"`
	VoiceWavURL string `json:"voice_wav_url,omitempty"`  // 语音 wav 格式链接
	ASRText     string `json:"asr_refer_text,omitempty"` // 语音 ASR 参考结果
}

// qqC2CMessage C2C_MESSAGE_CREATE payload
type qqC2CMessage struct {
	Author struct {
		UserOpenID string `json:"user_openid"`
	} `json:"author"`
	Content     string         `json:"content"`
	ID          string         `json:"id"`
	Timestamp   string         `json:"timestamp"`
	Attachments []qqAttachment `json:"attachments,omitempty"`
}

// qqGroupMessage GROUP_AT_MESSAGE_CREATE payload
type qqGroupMessage struct {
	Author struct {
		MemberOpenID string `json:"member_openid"`
	} `json:"author"`
	Content     string         `json:"content"`
	ID          string         `json:"id"`
	Timestamp   string         `json:"timestamp"`
	GroupOpenID string         `json:"group_openid"`
	Attachments []qqAttachment `json:"attachments,omitempty"`
}

// qqGuildMessage AT_MESSAGE_CREATE payload
type qqGuildMessage struct {
	Author struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"author"`
	Content     string         `json:"content"`
	ID          string         `json:"id"`
	Timestamp   string         `json:"timestamp"`
	ChannelID   string         `json:"channel_id"`
	GuildID     string         `json:"guild_id"`
	Attachments []qqAttachment `json:"attachments,omitempty"`
}

// ---------------------------------------------------------------------------
// WebSocket protocol handlers
// ---------------------------------------------------------------------------

// waitForHello 等待 op:10 Hello 消息
func (q *QQChannel) waitForHello() error {
	q.connMu.Lock()
	conn := q.conn
	q.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("no connection")
	}

	// Set a read deadline for hello
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	_, data, err := conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}

	var msg qqWSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("parse hello: %w", err)
	}

	if msg.Op != qqOpHello {
		return fmt.Errorf("expected op:10 Hello, got op:%d", msg.Op)
	}

	var hello qqHelloData
	if err := json.Unmarshal(msg.D, &hello); err != nil {
		return fmt.Errorf("parse hello data: %w", err)
	}

	q.heartbeatInterval = time.Duration(hello.HeartbeatInterval) * time.Millisecond
	log.WithField("heartbeat_interval_ms", hello.HeartbeatInterval).Info("QQ: received Hello")
	return nil
}

// sendIdentify 发送 op:2 Identify，支持 intent 降级
func (q *QQChannel) sendIdentify() error {
	auth, err := q.authHeader()
	if err != nil {
		return err
	}

	// Only try the current intent level; caller (connectAndRun) handles retry
	il := intentLevels[q.intentLevel]
	payload := map[string]any{
		"op": qqOpIdentify,
		"d": map[string]any{
			"token":   auth,
			"intents": il.value,
			"shard":   []int{0, 1},
		},
	}

	log.WithFields(log.Fields{
		"intents":      il.name,
		"intents_bits": il.value,
	}).Info("QQ: sending Identify")

	if err := q.wsSend(payload); err != nil {
		return fmt.Errorf("send identify: %w", err)
	}

	// Wait for READY or Invalid Session
	q.connMu.Lock()
	conn := q.conn
	q.connMu.Unlock()
	if conn == nil {
		return fmt.Errorf("no connection")
	}

	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("read identify response: %w", err)
	}

	var msg qqWSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("parse identify response: %w", err)
	}

	if msg.Op == qqOpDispatch && msg.T == "READY" {
		var ready qqReadyData
		if err := json.Unmarshal(msg.D, &ready); err != nil {
			return fmt.Errorf("parse READY: %w", err)
		}
		q.sessionID = ready.SessionID
		if msg.S != nil {
			q.lastSeq.Store(*msg.S)
		}
		log.WithFields(log.Fields{
			"session_id": q.sessionID,
			"intents":    il.name,
		}).Info("QQ: session established")
		return nil
	}

	if msg.Op == qqOpInvalidSession {
		log.WithField("intents", il.name).Warn("QQ: invalid session for intent level, trying lower")
		if q.intentLevel+1 < len(intentLevels) {
			q.intentLevel++
		}
		return fmt.Errorf("intent degradation needed, will retry")
	}

	// Unexpected response
	log.WithFields(log.Fields{
		"op": msg.Op,
		"t":  msg.T,
	}).Warn("QQ: unexpected response to Identify")
	return fmt.Errorf("unexpected identify response op:%d t:%s", msg.Op, msg.T)
}

// sendResume 发送 op:6 Resume
func (q *QQChannel) sendResume() error {
	auth, err := q.authHeader()
	if err != nil {
		return err
	}

	payload := map[string]any{
		"op": qqOpResume,
		"d": map[string]any{
			"token":      auth,
			"session_id": q.sessionID,
			"seq":        q.lastSeq.Load(),
		},
	}

	log.WithFields(log.Fields{
		"session_id": q.sessionID,
		"seq":        q.lastSeq.Load(),
	}).Info("QQ: sending Resume")

	return q.wsSend(payload)
}

// handleMessage 处理收到的 WebSocket 消息
func (q *QQChannel) handleMessage(data []byte) error {
	var msg qqWSMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("parse ws message: %w", err)
	}

	// Update sequence number
	if msg.S != nil {
		q.lastSeq.Store(*msg.S)
	}

	switch msg.Op {
	case qqOpDispatch:
		return q.handleDispatch(msg.T, msg.D)

	case qqOpHeartbeatACK:
		q.heartbeatACK.Store(true)
		log.Debug("QQ: heartbeat ACK received")

	case qqOpReconnect:
		log.Warn("QQ: server requested reconnect")
		return &fatalWSError{msg: "server requested reconnect (op:7)"}

	case qqOpInvalidSession:
		log.Warn("QQ: invalid session")
		q.sessionID = ""
		q.lastSeq.Store(0)
		return &fatalWSError{msg: "invalid session (op:9)"}

	case qqOpHello:
		// Unexpected second hello, ignore
		log.Debug("QQ: unexpected Hello, ignoring")

	default:
		log.WithField("op", msg.Op).Debug("QQ: unknown op code")
	}

	return nil
}

// handleDispatch 处理 op:0 Dispatch 事件
func (q *QQChannel) handleDispatch(eventType string, data json.RawMessage) error {
	switch eventType {
	case "READY":
		// Already handled during identify, but may come during resume
		var ready qqReadyData
		if err := json.Unmarshal(data, &ready); err == nil && ready.SessionID != "" {
			q.sessionID = ready.SessionID
			log.WithField("session_id", q.sessionID).Info("QQ: session ready (via dispatch)")
		}

	case "RESUMED":
		log.Info("QQ: session resumed successfully")

	case "C2C_MESSAGE_CREATE":
		return q.handleC2CMessage(data)

	case "GROUP_AT_MESSAGE_CREATE":
		return q.handleGroupMessage(data)

	case "AT_MESSAGE_CREATE":
		return q.handleGuildMessage(data)

	default:
		log.WithField("event", eventType).Debug("QQ: unhandled event type")
	}

	return nil
}

// ---------------------------------------------------------------------------
// Message handlers
// ---------------------------------------------------------------------------

// handleC2CMessage 处理私聊消息
func (q *QQChannel) handleC2CMessage(data json.RawMessage) error {
	var msg qqC2CMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("parse C2C message: %w", err)
	}

	senderID := msg.Author.UserOpenID
	messageID := msg.ID
	content := strings.TrimSpace(msg.Content)

	log.WithFields(log.Fields{
		"message_id":       messageID,
		"sender_id":        senderID,
		"content_len":      len(content),
		"attachment_count": len(msg.Attachments),
	}).Info("QQ: C2C message received")

	if q.isDuplicate(messageID) {
		log.WithField("message_id", messageID).Debug("QQ: duplicate message, skipping")
		return nil
	}

	if !q.isAllowed(q.config.AllowFrom, senderID) {
		log.WithField("sender", senderID).Warn("QQ: access denied")
		return nil
	}

	// Append attachment tags to content
	if attTags := formatAttachments(msg.Attachments); attTags != "" {
		if content != "" {
			content = content + "\n" + attTags
		} else {
			content = attTags
		}
	}

	if content == "" {
		return nil
	}

	msgTime := q.parseTimestamp(msg.Timestamp)

	// Cache chat type for outbound routing
	q.cacheChatType(senderID, "c2c")

	// For C2C, chatID is the user's openid (reply target)
	q.msgBus.Inbound <- bus.InboundMessage{
		Channel:    "qq",
		SenderID:   senderID,
		SenderName: "", // QQ C2C doesn't provide username
		ChatID:     senderID,
		Content:    content,
		Time:       msgTime,
		Metadata: map[string]string{
			"message_id": messageID,
			"chat_type":  "c2c",
		},
	}

	return nil
}

// handleGroupMessage 处理群消息
func (q *QQChannel) handleGroupMessage(data json.RawMessage) error {
	var msg qqGroupMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("parse group message: %w", err)
	}

	senderID := msg.Author.MemberOpenID
	messageID := msg.ID
	groupID := msg.GroupOpenID
	content := strings.TrimSpace(msg.Content)

	log.WithFields(log.Fields{
		"message_id":       messageID,
		"sender_id":        senderID,
		"group_id":         groupID,
		"content_len":      len(content),
		"attachment_count": len(msg.Attachments),
	}).Info("QQ: group message received")

	if q.isDuplicate(messageID) {
		log.WithField("message_id", messageID).Debug("QQ: duplicate message, skipping")
		return nil
	}

	if !q.isAllowed(q.config.AllowFrom, senderID) {
		log.WithField("sender", senderID).Warn("QQ: access denied")
		return nil
	}

	// Strip leading/trailing whitespace and @mention artifacts
	content = stripQQMention(content)

	// Append attachment tags to content
	if attTags := formatAttachments(msg.Attachments); attTags != "" {
		if content != "" {
			content = content + "\n" + attTags
		} else {
			content = attTags
		}
	}

	if content == "" {
		return nil
	}

	msgTime := q.parseTimestamp(msg.Timestamp)

	// Cache chat type for outbound routing
	q.cacheChatType(groupID, "group")

	q.msgBus.Inbound <- bus.InboundMessage{
		Channel:    "qq",
		SenderID:   senderID,
		SenderName: "", // QQ group doesn't provide username in this event
		ChatID:     groupID,
		Content:    content,
		Time:       msgTime,
		Metadata: map[string]string{
			"message_id": messageID,
			"chat_type":  "group",
			"group_id":   groupID,
		},
	}

	return nil
}

// handleGuildMessage 处理频道消息
func (q *QQChannel) handleGuildMessage(data json.RawMessage) error {
	var msg qqGuildMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("parse guild message: %w", err)
	}

	senderID := msg.Author.ID
	senderName := msg.Author.Username
	messageID := msg.ID
	channelID := msg.ChannelID
	guildID := msg.GuildID
	content := strings.TrimSpace(msg.Content)

	log.WithFields(log.Fields{
		"message_id":       messageID,
		"sender_id":        senderID,
		"sender_name":      senderName,
		"channel_id":       channelID,
		"guild_id":         guildID,
		"content_len":      len(content),
		"attachment_count": len(msg.Attachments),
	}).Info("QQ: guild message received")

	if q.isDuplicate(messageID) {
		log.WithField("message_id", messageID).Debug("QQ: duplicate message, skipping")
		return nil
	}

	if !q.isAllowed(q.config.AllowFrom, senderID) {
		log.WithField("sender", senderID).Warn("QQ: access denied")
		return nil
	}

	// Strip @mention artifacts
	content = stripQQMention(content)

	// Append attachment tags to content
	if attTags := formatAttachments(msg.Attachments); attTags != "" {
		if content != "" {
			content = content + "\n" + attTags
		} else {
			content = attTags
		}
	}

	if content == "" {
		return nil
	}

	msgTime := q.parseTimestamp(msg.Timestamp)

	// Cache chat type for outbound routing
	q.cacheChatType(channelID, "guild")

	q.msgBus.Inbound <- bus.InboundMessage{
		Channel:    "qq",
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     channelID,
		Content:    content,
		Time:       msgTime,
		Metadata: map[string]string{
			"message_id": messageID,
			"chat_type":  "guild",
			"channel_id": channelID,
			"guild_id":   guildID,
		},
	}

	return nil
}

// ---------------------------------------------------------------------------
// QQ rich media file types
// ---------------------------------------------------------------------------

const (
	qqFileTypeImage = 1 // 图片 (png/jpg)
	qqFileTypeVideo = 2 // 视频 (mp4)
	qqFileTypeVoice = 3 // 语音 (silk/wav/mp3/flac)
	qqFileTypeFile  = 4 // 文件（群场景暂不开放）
)

// qqFileUploadResponse 富媒体上传 API 返回
type qqFileUploadResponse struct {
	FileUUID string `json:"file_uuid"`
	FileInfo string `json:"file_info"`
	TTL      int    `json:"ttl"`
	ID       string `json:"id"`
}

// qqImageExtensions 图片文件扩展名集合
var qqImageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	".gif": true, ".bmp": true, ".ico": true, ".tiff": true, ".heic": true,
}

// qqMdImageRe 匹配 markdown 图片语法 ![alt](path)
var qqMdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// qqMdLinkRe 匹配 markdown 链接语法 [name](path)，但不匹配图片 ![alt](path)
var qqMdLinkRe = regexp.MustCompile(`(?:^|[^!])\[([^\]]+)\]\(([^)]+)\)`)

// qqMentionRegex matches QQ @mention artifacts like <@!123456> or <@123456>
var qqMentionRegex = regexp.MustCompile(`<@!?\d+>`)

// ---------------------------------------------------------------------------
// Send (outbound)
// ---------------------------------------------------------------------------

// Send 发送消息到 QQ，返回平台消息 ID
func (q *QQChannel) Send(msg bus.OutboundMessage) (string, error) {
	if msg.Content == "" {
		return "", nil
	}

	chatType := ""
	if msg.Metadata != nil {
		chatType = msg.Metadata["chat_type"]
	}

	// Determine chat type from metadata or infer from chatID pattern
	if chatType == "" {
		chatType = q.inferChatType(msg.ChatID)
	}

	// Resolve target ID for group messages
	targetID := msg.ChatID
	if chatType == "group" && msg.Metadata != nil && msg.Metadata["group_id"] != "" {
		targetID = msg.Metadata["group_id"]
	}

	// Process content: extract and send local images/files (c2c and group only)
	content := msg.Content
	if chatType == "c2c" || chatType == "group" {
		content = q.extractAndSendLocalFiles(targetID, chatType, content, msg.Metadata)
		content = q.extractAndSendLocalImages(targetID, chatType, content, msg.Metadata)
	}

	// If all content was media (nothing left after extraction), skip text send
	if strings.TrimSpace(content) == "" {
		return "", nil
	}

	switch chatType {
	case "c2c":
		return q.sendC2CMessage(targetID, content, msg.Metadata)
	case "group":
		return q.sendGroupMessage(targetID, content, msg.Metadata)
	case "guild":
		return q.sendGuildMessage(msg.ChatID, content, msg.Metadata)
	default:
		return q.sendAutoDetect(msg.ChatID, content, msg.Metadata)
	}
}

// sendC2CMessage 发送私聊消息
func (q *QQChannel) sendC2CMessage(openID, content string, metadata map[string]string) (string, error) {
	url := fmt.Sprintf("%s/v2/users/%s/messages", qqAPIBase, openID)

	msgID := ""
	if metadata != nil {
		msgID = metadata["message_id"]
	}

	seq := q.nextMsgSeq(msgID)

	// Try markdown first, fallback to plain text if markdown is not enabled
	body := q.buildMarkdownBody(content, msgID, seq)
	id, err := q.doSendRequest(url, body, "c2c", openID)
	if err != nil && q.isMarkdownUnsupported(err) {
		log.Debug("QQ: markdown not supported for c2c, falling back to plain text")
		q.markdownDisabled.Store(true)
		body = q.buildTextBody(content, msgID, q.nextMsgSeq(msgID))
		return q.doSendRequest(url, body, "c2c", openID)
	}
	return id, err
}

// sendGroupMessage 发送群消息
func (q *QQChannel) sendGroupMessage(groupOpenID, content string, metadata map[string]string) (string, error) {
	url := fmt.Sprintf("%s/v2/groups/%s/messages", qqAPIBase, groupOpenID)

	msgID := ""
	if metadata != nil {
		msgID = metadata["message_id"]
	}

	seq := q.nextMsgSeq(msgID)

	// Try markdown first, fallback to plain text if markdown is not enabled
	body := q.buildMarkdownBody(content, msgID, seq)
	id, err := q.doSendRequest(url, body, "group", groupOpenID)
	if err != nil && q.isMarkdownUnsupported(err) {
		log.Debug("QQ: markdown not supported for group, falling back to plain text")
		q.markdownDisabled.Store(true)
		body = q.buildTextBody(content, msgID, q.nextMsgSeq(msgID))
		return q.doSendRequest(url, body, "group", groupOpenID)
	}
	return id, err
}

// sendGuildMessage 发送频道消息
func (q *QQChannel) sendGuildMessage(channelID, content string, metadata map[string]string) (string, error) {
	url := fmt.Sprintf("%s/channels/%s/messages", qqAPIBase, channelID)

	msgID := ""
	if metadata != nil {
		msgID = metadata["message_id"]
	}

	body := map[string]any{
		"content": content,
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}

	return q.doSendRequest(url, body, "guild", channelID)
}

// sendAutoDetect 自动检测消息类型并发送
func (q *QQChannel) sendAutoDetect(chatID, content string, metadata map[string]string) (string, error) {
	log.WithField("chat_id", chatID).Warn("QQ: unknown chat type, attempting auto-detect")

	const maxAttempts = 3

	// Try cached type first
	if cached := q.inferChatType(chatID); cached != "" {
		var id string
		var err error
		switch cached {
		case "group":
			id, err = q.sendGroupMessage(chatID, content, metadata)
			if err == nil {
				return id, nil
			}
			log.WithError(err).Debug("QQ: cached group send failed, trying other types")
		case "guild":
			id, err = q.sendGuildMessage(chatID, content, metadata)
			if err == nil {
				return id, nil
			}
			log.WithError(err).Debug("QQ: cached guild send failed, trying other types")
		case "c2c":
			id, err = q.sendC2CMessage(chatID, content, metadata)
			if err == nil {
				return id, nil
			}
			log.WithError(err).Debug("QQ: cached c2c send failed, trying other types")
		}
		_ = id
	}

	// Try all types with attempt limit
	attempts := 0
	for _, tryFn := range []func() (string, error){
		func() (string, error) { return q.sendGroupMessage(chatID, content, metadata) },
		func() (string, error) { return q.sendGuildMessage(chatID, content, metadata) },
		func() (string, error) { return q.sendC2CMessage(chatID, content, metadata) },
	} {
		id, err := tryFn()
		if err == nil {
			return id, nil
		}
		attempts++
		if attempts >= maxAttempts {
			break
		}
		log.WithError(err).Debug("QQ: auto-detect send attempt failed")
		_ = id
	}

	return "", fmt.Errorf("qq: all auto-detect attempts failed for chat_id=%s", chatID)
}

// ---------------------------------------------------------------------------
// Rich media upload & send (images / files)
// ---------------------------------------------------------------------------

// uploadFileToQQ 上传富媒体文件到 QQ，返回 file_info 用于发送消息
// targetID: c2c 场景为 user openid，group 场景为 group_openid
// chatType: "c2c" 或 "group"
// fileType: qqFileTypeImage / qqFileTypeVideo / qqFileTypeVoice / qqFileTypeFile
// fileData: base64 编码的文件内容
func (q *QQChannel) uploadFileToQQ(targetID, chatType string, fileType int, fileData string) (*qqFileUploadResponse, error) {
	var apiURL string
	switch chatType {
	case "c2c":
		apiURL = fmt.Sprintf("%s/v2/users/%s/files", qqAPIBase, targetID)
	case "group":
		apiURL = fmt.Sprintf("%s/v2/groups/%s/files", qqAPIBase, targetID)
	default:
		return nil, fmt.Errorf("qq: unsupported chat type for file upload: %s", chatType)
	}

	body := map[string]any{
		"file_type":    fileType,
		"file_data":    fileData,
		"srv_send_msg": false, // 不直接发送，获取 file_info 后再发
	}

	auth, err := q.authHeader()
	if err != nil {
		return nil, fmt.Errorf("qq: auth for upload: %w", err)
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("qq: marshal upload body: %w", err)
	}

	req, err := http.NewRequest("POST", apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("qq: create upload request: %w", err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")

	log.WithFields(log.Fields{
		"url":       apiURL,
		"chat_type": chatType,
		"file_type": fileType,
		"body_len":  len(jsonBody),
	}).Debug("QQ: uploading file")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qq: upload request failed: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("qq: read upload response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("qq: upload API error: status=%d body=%s", resp.StatusCode, string(respData))
	}

	var result qqFileUploadResponse
	if err := json.Unmarshal(respData, &result); err != nil {
		return nil, fmt.Errorf("qq: parse upload response: %w (body: %s)", err, string(respData))
	}

	log.WithFields(log.Fields{
		"file_uuid": result.FileUUID,
		"ttl":       result.TTL,
	}).Debug("QQ: file uploaded")

	return &result, nil
}

// sendMediaMessage 发送富媒体消息 (msg_type: 7)
func (q *QQChannel) sendMediaMessage(targetID, chatType, fileInfo string, metadata map[string]string) (string, error) {
	var apiURL string
	switch chatType {
	case "c2c":
		apiURL = fmt.Sprintf("%s/v2/users/%s/messages", qqAPIBase, targetID)
	case "group":
		apiURL = fmt.Sprintf("%s/v2/groups/%s/messages", qqAPIBase, targetID)
	default:
		return "", fmt.Errorf("qq: unsupported chat type for media send: %s", chatType)
	}

	msgID := ""
	if metadata != nil {
		msgID = metadata["message_id"]
	}

	body := map[string]any{
		"content":  " ", // QQ requires non-empty content even for media
		"msg_type": 7,   // rich media
		"media": map[string]string{
			"file_info": fileInfo,
		},
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}
	body["msg_seq"] = q.nextMsgSeq(msgID)

	return q.doSendRequest(apiURL, body, chatType, targetID)
}

// extractAndSendLocalImages 从 markdown 中提取本地图片 ![alt](path)，上传并发送，从内容中移除
func (q *QQChannel) extractAndSendLocalImages(targetID, chatType, content string, metadata map[string]string) string {
	return qqMdImageRe.ReplaceAllStringFunc(content, func(match string) string {
		subs := qqMdImageRe.FindStringSubmatch(match)
		if len(subs) < 3 {
			return match
		}
		imgPath := subs[2]
		altText := subs[1]

		// Skip URLs and image_key references
		if strings.HasPrefix(imgPath, "http://") || strings.HasPrefix(imgPath, "https://") || strings.HasPrefix(imgPath, "img_") {
			return match
		}

		// Check if it's an image extension
		ext := strings.ToLower(filepath.Ext(imgPath))
		if !qqImageExtensions[ext] {
			return match
		}

		// Check file exists
		if _, err := os.Stat(imgPath); err != nil {
			log.WithField("path", imgPath).Debug("QQ: local image not found, keeping original markdown")
			return match
		}

		// Read and base64 encode
		fileData, err := os.ReadFile(imgPath)
		if err != nil {
			log.WithError(err).WithField("path", imgPath).Warn("QQ: failed to read local image")
			return match
		}
		b64Data := base64.StdEncoding.EncodeToString(fileData)

		// Upload to QQ
		uploadResp, err := q.uploadFileToQQ(targetID, chatType, qqFileTypeImage, b64Data)
		if err != nil {
			log.WithError(err).WithField("path", imgPath).Warn("QQ: failed to upload image")
			return match
		}

		// Send as media message
		if _, err := q.sendMediaMessage(targetID, chatType, uploadResp.FileInfo, metadata); err != nil {
			log.WithError(err).WithField("path", imgPath).Warn("QQ: failed to send image message")
			return match
		}

		log.WithField("path", imgPath).Debug("QQ: sent local image")

		// Replace with text indicator
		if altText != "" {
			return "📷 " + altText
		}
		return "📷 " + filepath.Base(imgPath)
	})
}

// extractAndSendLocalFiles 从 markdown 中提取本地文件链接 [name](path)（非图片），上传并发送
func (q *QQChannel) extractAndSendLocalFiles(targetID, chatType, content string, metadata map[string]string) string {
	return qqMdLinkRe.ReplaceAllStringFunc(content, func(match string) string {
		subs := qqMdLinkRe.FindStringSubmatch(match)
		if len(subs) < 3 {
			return match
		}
		linkPath := subs[2]

		// Preserve prefix character (regex may capture non-! char before [)
		prefix := ""
		if len(match) > 0 && match[0] != '[' {
			prefix = string(match[0])
		}

		// Skip URLs
		if strings.HasPrefix(linkPath, "http://") || strings.HasPrefix(linkPath, "https://") {
			return match
		}

		// Skip image extensions (handled by extractAndSendLocalImages)
		ext := strings.ToLower(filepath.Ext(linkPath))
		if qqImageExtensions[ext] {
			return match
		}

		// Check file exists
		if _, err := os.Stat(linkPath); err != nil {
			return match
		}

		// Read and base64 encode
		fileData, err := os.ReadFile(linkPath)
		if err != nil {
			log.WithError(err).WithField("path", linkPath).Warn("QQ: failed to read local file")
			return match
		}
		b64Data := base64.StdEncoding.EncodeToString(fileData)

		// Determine file type
		fileType := qqFileTypeFile
		switch ext {
		case ".mp4":
			fileType = qqFileTypeVideo
		case ".silk", ".wav", ".mp3", ".flac", ".amr":
			fileType = qqFileTypeVoice
		}

		// Upload to QQ
		uploadResp, err := q.uploadFileToQQ(targetID, chatType, fileType, b64Data)
		if err != nil {
			log.WithError(err).WithField("path", linkPath).Warn("QQ: failed to upload file")
			return match
		}

		// Send as media message
		if _, err := q.sendMediaMessage(targetID, chatType, uploadResp.FileInfo, metadata); err != nil {
			log.WithError(err).WithField("path", linkPath).Warn("QQ: failed to send file message")
			return match
		}

		log.WithField("path", linkPath).Debug("QQ: sent local file")

		return prefix + "📎 " + subs[1]
	})
}

// doSendRequest 执行发送消息的 HTTP 请求
func (q *QQChannel) doSendRequest(url string, body map[string]any, chatType, target string) (string, error) {
	auth, err := q.authHeader()
	if err != nil {
		return "", fmt.Errorf("qq: auth for send: %w", err)
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("qq: marshal send body: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return "", fmt.Errorf("qq: create send request: %w", err)
	}
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")

	log.WithFields(log.Fields{
		"url":       url,
		"chat_type": chatType,
		"target":    target,
		"body_len":  len(jsonBody),
	}).Debug("QQ: sending message")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("qq: send request failed: %w", err)
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("qq: read send response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("qq: send API error: status=%d body=%s", resp.StatusCode, string(respData))
	}

	// Parse response to get message ID
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respData, &result); err != nil {
		// Non-fatal: message was sent but we can't parse the ID
		log.WithError(err).Debug("QQ: could not parse send response for message ID")
		return "", nil
	}

	log.WithFields(log.Fields{
		"chat_type":  chatType,
		"target":     target,
		"message_id": result.ID,
	}).Debug("QQ: message sent")

	return result.ID, nil
}

// ---------------------------------------------------------------------------
// Heartbeat
// ---------------------------------------------------------------------------

// startHeartbeat 启动心跳协程
func (q *QQChannel) startHeartbeat() {
	q.heartbeatStop = make(chan struct{})
	q.heartbeatACK.Store(true) // assume first ACK is OK

	go func() {
		ticker := time.NewTicker(q.heartbeatInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if !q.heartbeatACK.Load() {
					log.Warn("QQ: missed heartbeat ACK, connection may be dead")
					// Close connection to trigger reconnect
					q.closeConn()
					return
				}

				q.heartbeatACK.Store(false)
				seq := q.lastSeq.Load()
				var d any
				if seq > 0 {
					d = seq
				}
				payload := map[string]any{
					"op": qqOpHeartbeat,
					"d":  d,
				}
				if err := q.wsSend(payload); err != nil {
					log.WithError(err).Warn("QQ: failed to send heartbeat")
					return
				}
				log.Debug("QQ: heartbeat sent")

			case <-q.heartbeatStop:
				return
			case <-q.stopCh:
				return
			}
		}
	}()
}

// stopHeartbeat 停止心跳
func (q *QQChannel) stopHeartbeat() {
	if q.heartbeatStop != nil {
		select {
		case <-q.heartbeatStop:
			// already closed
		default:
			close(q.heartbeatStop)
		}
	}
}

// ---------------------------------------------------------------------------
// WebSocket helpers
// ---------------------------------------------------------------------------


// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------


// ---------------------------------------------------------------------------
// Access control
// ---------------------------------------------------------------------------


// ---------------------------------------------------------------------------
// msg_seq tracking
// ---------------------------------------------------------------------------

// nextMsgSeq 获取下一个 msg_seq（QQ 要求同一 msg_id 的回复递增 seq）
func (q *QQChannel) nextMsgSeq(msgID string) int {
	if msgID == "" {
		return 1
	}

	q.msgSeqMu.Lock()
	defer q.msgSeqMu.Unlock()

	entry := q.msgSeqMap[msgID]
	entry.seq++
	entry.lastUsed = time.Now()
	q.msgSeqMap[msgID] = entry
	seq := entry.seq

	// Prevent unbounded growth: evict entries not used recently
	if len(q.msgSeqMap) > cacheMaxEntries {
		cutoff := time.Now().Add(-cacheExpiry)
		scanned := 0
		for k, v := range q.msgSeqMap {
			if v.lastUsed.Before(cutoff) {
				delete(q.msgSeqMap, k)
			}
			scanned++
			if scanned >= cacheMaxScanPerCleanup {
				break
			}
		}
	}

	return seq
}

// ---------------------------------------------------------------------------
// Chat type inference
// ---------------------------------------------------------------------------

// cacheChatType 缓存 chatID 对应的聊天类型
func (q *QQChannel) cacheChatType(chatID, chatType string) {
	q.chatTypeMu.Lock()
	defer q.chatTypeMu.Unlock()
	q.chatTypeCache[chatID] = chatTypeEntry{chatType: chatType, lastUsed: time.Now()}

	// 防止无限增长：淘汰过期条目
	if len(q.chatTypeCache) > cacheMaxEntries {
		cutoff := time.Now().Add(-cacheExpiry)
		scanned := 0
		for k, v := range q.chatTypeCache {
			if v.lastUsed.Before(cutoff) {
				delete(q.chatTypeCache, k)
			}
			scanned++
			if scanned >= cacheMaxScanPerCleanup {
				break
			}
		}
	}
}

// inferChatType 根据 chatID 查找缓存的聊天类型
func (q *QQChannel) inferChatType(chatID string) string {
	q.chatTypeMu.RLock()
	defer q.chatTypeMu.RUnlock()
	if entry, ok := q.chatTypeCache[chatID]; ok {
		return entry.chatType
	}
	return ""
}

// ---------------------------------------------------------------------------
// Attachment formatting
// ---------------------------------------------------------------------------

// formatAttachments 将 QQ attachments 转为与飞书一致的 XML 标签格式
// 图片: <image url="..." filename="..." width="..." height="..." />
// 文件: <file url="..." filename="..." size="..." />
// 视频: <video url="..." filename="..." />
// 语音: <audio url="..." filename="..." asr_text="..." />
func formatAttachments(attachments []qqAttachment) string {
	if len(attachments) == 0 {
		return ""
	}

	var parts []string
	for _, att := range attachments {
		url := att.URL
		if url == "" {
			continue
		}
		// QQ 返回的 URL 可能不带 scheme
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			url = "https://" + url
		}

		ct := strings.ToLower(att.ContentType)
		switch {
		case strings.HasPrefix(ct, "image/"):
			tag := fmt.Sprintf(`<image url="%s" filename="%s"`, url, att.Filename)
			if att.Width > 0 {
				tag += fmt.Sprintf(` width="%d"`, att.Width)
			}
			if att.Height > 0 {
				tag += fmt.Sprintf(` height="%d"`, att.Height)
			}
			tag += " />"
			parts = append(parts, tag)

		case ct == "video/mp4" || strings.HasPrefix(ct, "video/"):
			parts = append(parts, fmt.Sprintf(`<video url="%s" filename="%s" />`, url, att.Filename))

		case ct == "voice" || strings.HasPrefix(ct, "audio/"):
			tag := fmt.Sprintf(`<audio url="%s" filename="%s"`, url, att.Filename)
			if att.VoiceWavURL != "" {
				wavURL := att.VoiceWavURL
				if !strings.HasPrefix(wavURL, "http://") && !strings.HasPrefix(wavURL, "https://") {
					wavURL = "https://" + wavURL
				}
				tag += fmt.Sprintf(` wav_url="%s"`, wavURL)
			}
			if att.ASRText != "" {
				tag += fmt.Sprintf(` asr_text="%s"`, att.ASRText)
			}
			tag += " />"
			parts = append(parts, tag)

		default:
			// 通用文件
			tag := fmt.Sprintf(`<file url="%s" filename="%s"`, url, att.Filename)
			if att.Size > 0 {
				tag += fmt.Sprintf(` size="%d"`, att.Size)
			}
			tag += " />"
			parts = append(parts, tag)
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n")
}

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

// stripQQMention 去除 QQ @mention 标记
// QQ messages may contain <@!botid> or similar mention artifacts
func stripQQMention(content string) string {
	return strings.TrimSpace(qqMentionRegex.ReplaceAllString(content, ""))
}

// parseTimestamp 解析 QQ 消息时间戳
func (q *QQChannel) parseTimestamp(ts string) time.Time {
	if ts == "" {
		return time.Now()
	}

	// QQ timestamps can be ISO 8601 format
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t
		}
	}

	log.WithField("timestamp", ts).Debug("QQ: could not parse timestamp, using now")
	return time.Now()
}


// ---------------------------------------------------------------------------
// Quick disconnect detection
// ---------------------------------------------------------------------------


// ---------------------------------------------------------------------------
// Markdown message helpers
// ---------------------------------------------------------------------------

// buildMarkdownBody 构建 markdown 格式的消息体 (msg_type: 2)
// 如果 markdown 已被禁用（之前发送失败），直接返回纯文本格式
func (q *QQChannel) buildMarkdownBody(content, msgID string, seq int) map[string]any {
	if q.markdownDisabled.Load() {
		return q.buildTextBody(content, msgID, seq)
	}
	body := map[string]any{
		"content":  content,
		"msg_type": 2, // markdown
		"markdown": map[string]string{
			"content": content,
		},
		"msg_seq": seq,
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}
	return body
}

// buildTextBody 构建纯文本格式的消息体 (msg_type: 0)
func (q *QQChannel) buildTextBody(content, msgID string, seq int) map[string]any {
	body := map[string]any{
		"content":  content,
		"msg_type": 0, // text
		"msg_seq":  seq,
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}
	return body
}

// isMarkdownUnsupported 判断错误是否表示 markdown 消息类型不被支持
// QQ API 在未开通 markdown 权限时会返回特定错误码
func (q *QQChannel) isMarkdownUnsupported(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	// QQ API 返回的错误中包含 "not support" 或权限相关错误码
	return strings.Contains(errMsg, "not support") ||
		strings.Contains(errMsg, "msg_type") ||
		strings.Contains(errMsg, "304003") || // 无权限
		strings.Contains(errMsg, "304004") || // 消息类型不支持
		strings.Contains(errMsg, "50006") // 不支持的消息类型
}

// ---------------------------------------------------------------------------
// Fatal WS error type
// ---------------------------------------------------------------------------

type fatalWSError struct {
	msg string
}

func (e *fatalWSError) Error() string {
	return e.msg
}

func isFatalWSError(err error) bool {
	_, ok := err.(*fatalWSError)
	return ok
}
