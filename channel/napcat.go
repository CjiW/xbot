package channel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xbot/bus"
	log "xbot/logger"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Reconnect strategy (shared constants with qq.go)
// ---------------------------------------------------------------------------

var napcatReconnectDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

const napcatMaxReconnectAttempts = 100
const napcatQuickDisconnectWindow = 5 * time.Second
const napcatQuickDisconnectCount = 3

// ---------------------------------------------------------------------------
// NapCatConfig 配置
// ---------------------------------------------------------------------------

// NapCatConfig NapCat (OneBot 11) 渠道配置
type NapCatConfig struct {
	Enabled   bool
	WSUrl     string   // NapCat WebSocket URL, e.g. "ws://localhost:3001"
	Token     string   // 鉴权 token（可选）
	AllowFrom []string // 允许的 QQ 号白名单（空则允许所有）
}

// ---------------------------------------------------------------------------
// NapCatChannel 实现
// ---------------------------------------------------------------------------

// NapCatChannel NapCat (OneBot 11) 渠道实现
type NapCatChannel struct {
	WSChannelBase

	config NapCatConfig
	msgBus *bus.MessageBus

	running  atomic.Bool
	stopOnce sync.Once

	// API 请求-响应匹配
	pending   map[string]chan json.RawMessage // echo -> response channel
	pendingMu sync.Mutex

	// Bot 自身 QQ 号（从事件中获取）
	selfID atomic.Int64

	// 聊天类型缓存（chatID → "group"/"private"）
	chatTypeCache sync.Map
}

// NewNapCatChannel 创建 NapCat 渠道
func NewNapCatChannel(cfg NapCatConfig, msgBus *bus.MessageBus) *NapCatChannel {
	return &NapCatChannel{
		WSChannelBase: NewWSChannelBase(1000, napcatQuickDisconnectWindow, napcatQuickDisconnectCount),
		config:        cfg,
		msgBus:        msgBus,
		pending:       make(map[string]chan json.RawMessage),
	}
}

func (n *NapCatChannel) Name() string { return "napcat" }

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

// Start 启动 NapCat 渠道，阻塞运行直到 Stop 被调用
func (n *NapCatChannel) Start() error {
	if n.config.WSUrl == "" {
		return fmt.Errorf("napcat: ws_url is required")
	}

	n.running.Store(true)
	log.WithField("ws_url", n.config.WSUrl).Info("NapCat bot starting...")

	attempt := 0
	for n.running.Load() {
		if attempt >= napcatMaxReconnectAttempts {
			return fmt.Errorf("napcat: exceeded max reconnect attempts (%d)", napcatMaxReconnectAttempts)
		}

		connectStart := time.Now()
		err := n.connectAndRun()
		if !n.running.Load() {
			return nil // graceful shutdown
		}
		// 连接持续超过 30s 说明不是立即断开，重置计数
		if time.Since(connectStart) > 30*time.Second {
			attempt = 0
		}

		if err != nil {
			log.WithError(err).Warn("NapCat: WebSocket session ended")
		}

		// Quick disconnect detection
		if n.isQuickDisconnectLoop() {
			log.Warn("NapCat: rapid disconnect loop detected, waiting 60s")
			if !n.sleepOrStop(60 * time.Second) {
				return nil
			}
			attempt++
			continue
		}

		delay := napcatReconnectDelays[attempt%len(napcatReconnectDelays)]
		if attempt >= len(napcatReconnectDelays) {
			delay = napcatReconnectDelays[len(napcatReconnectDelays)-1]
		}

		log.WithFields(log.Fields{
			"attempt": attempt + 1,
			"delay":   delay,
		}).Info("NapCat: reconnecting...")

		if !n.sleepOrStop(delay) {
			return nil
		}
		attempt++
	}
	return nil
}

// Stop 停止 NapCat 渠道
func (n *NapCatChannel) Stop() {
	n.stopOnce.Do(func() {
		n.running.Store(false)
		close(n.stopCh)
		n.closeConn()
		n.clearPending()
		log.Info("NapCat bot stopped")
	})
}

// ---------------------------------------------------------------------------
// Connect and run main loop
// ---------------------------------------------------------------------------

// connectAndRun 建立 WebSocket 连接并运行消息循环，返回时表示连接断开
func (n *NapCatChannel) connectAndRun() error {
	header := http.Header{}
	if n.config.Token != "" {
		header.Set("Authorization", "Bearer "+n.config.Token)
	}

	conn, _, err := websocket.DefaultDialer.Dial(n.config.WSUrl, header)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}

	n.connMu.Lock()
	n.conn = conn
	n.connMu.Unlock()

	defer n.closeConn()

	connectTime := time.Now()
	log.WithField("ws_url", n.config.WSUrl).Info("NapCat: WebSocket connected")

	// Read messages
	for n.running.Load() {
		_, data, err := conn.ReadMessage()
		if err != nil {
			n.recordDisconnect(connectTime)
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return fmt.Errorf("ws closed: %w", err)
			}
			return fmt.Errorf("ws read: %w", err)
		}

		if err := n.handleEvent(data); err != nil {
			log.WithError(err).Warn("NapCat: event handling error")
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// OneBot 11 event types
// ---------------------------------------------------------------------------

// obEvent OneBot 11 通用事件结构
type obEvent struct {
	PostType      string          `json:"post_type"`
	MessageType   string          `json:"message_type"`
	SubType       string          `json:"sub_type"`
	MetaEventType string          `json:"meta_event_type"`
	SelfID        int64           `json:"self_id"`
	Time          int64           `json:"time"`
	MessageID     int64           `json:"message_id"`
	UserID        int64           `json:"user_id"`
	GroupID       int64           `json:"group_id"`
	RawMessage    string          `json:"raw_message"`
	Message       json.RawMessage `json:"message"`
	Sender        obSender        `json:"sender"`

	// API 响应字段
	Status  json.RawMessage `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    string          `json:"echo"`
}

// obSender 发送者信息
type obSender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
	Card     string `json:"card"` // 群名片
}

// obMessageSegment OneBot 11 消息段
type obMessageSegment struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// obTextData 文本消息段数据
type obTextData struct {
	Text string `json:"text"`
}

// obImageData 图片消息段数据
type obImageData struct {
	File string `json:"file"`
	URL  string `json:"url"`
}

// obAtData @消息段数据
type obAtData struct {
	QQ any `json:"qq"`
}

// formatQQ 将 obAtData.QQ(any) 格式化为字符串
// NapCat 可能发送 string 或 float64 类型的 QQ 号
func formatQQ(qq any) string {
	switch v := qq.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatInt(int64(v), 10)
	default:
		return fmt.Sprintf("%v", v)
	}
}

// obMediaData 通用媒体消息段数据（record/video/file）
type obMediaData struct {
	File string `json:"file"`
	URL  string `json:"url"`
}

// obAPIRequest OneBot 11 API 请求
type obAPIRequest struct {
	Action string `json:"action"`
	Params any    `json:"params"`
	Echo   string `json:"echo"`
}

// obAPIResponse OneBot 11 API 响应
type obAPIResponse struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Echo    string          `json:"echo"`
}

// obSendMsgResponse send_msg 响应数据
type obSendMsgResponse struct {
	MessageID int64 `json:"message_id"`
}

// ---------------------------------------------------------------------------
// Event dispatcher
// ---------------------------------------------------------------------------

// handleEvent 处理从 WebSocket 收到的事件
func (n *NapCatChannel) handleEvent(data []byte) error {
	var event obEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("parse event: %w", err)
	}

	// 检查是否是 API 响应（有 echo 字段）
	if event.Echo != "" {
		n.handleAPIResponse(event.Echo, data)
		return nil
	}

	// 记录 self_id
	if event.SelfID != 0 {
		n.selfID.Store(event.SelfID)
	}

	switch event.PostType {
	case "message":
		return n.handleMessage(&event)
	case "meta_event":
		return n.handleMetaEvent(&event)
	case "notice":
		log.WithField("sub_type", event.SubType).Debug("NapCat: notice event (ignored)")
	case "request":
		log.WithField("sub_type", event.SubType).Debug("NapCat: request event (ignored)")
	default:
		// 可能是纯 API 响应（status 字段存在但无 post_type）
		if len(event.Status) > 0 {
			// 无 echo 的 API 响应，忽略
			return nil
		}
		log.WithField("post_type", event.PostType).Debug("NapCat: unknown event type")
	}

	return nil
}

// handleAPIResponse 处理 API 响应，匹配 pending 请求
func (n *NapCatChannel) handleAPIResponse(echo string, data []byte) {
	n.pendingMu.Lock()
	ch, ok := n.pending[echo]
	if ok {
		delete(n.pending, echo)
	}
	n.pendingMu.Unlock()

	if ok {
		select {
		case ch <- json.RawMessage(data):
		default:
			// channel 可能已满或已关闭，丢弃响应
		}
	}
}

// handleMetaEvent 处理元事件
func (n *NapCatChannel) handleMetaEvent(event *obEvent) error {
	switch event.MetaEventType {
	case "heartbeat":
		log.Debug("NapCat: heartbeat received")
	case "lifecycle":
		log.WithField("sub_type", event.SubType).Info("NapCat: lifecycle event")
	default:
		log.WithField("meta_event_type", event.MetaEventType).Debug("NapCat: unknown meta event")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Message handler
// ---------------------------------------------------------------------------

// handleMessage 处理消息事件
func (n *NapCatChannel) handleMessage(event *obEvent) error {
	messageID := fmt.Sprintf("%d", event.MessageID)

	log.WithFields(log.Fields{
		"message_id":   messageID,
		"message_type": event.MessageType,
		"user_id":      event.UserID,
		"group_id":     event.GroupID,
		"raw_message":  truncate(event.RawMessage, 100),
	}).Info("NapCat: message received")

	// 去重
	if n.isDuplicate(messageID) {
		log.WithField("message_id", messageID).Debug("NapCat: duplicate message, skipping")
		return nil
	}

	// 白名单检查
	senderID := fmt.Sprintf("%d", event.UserID)
	if !n.isAllowed(n.config.AllowFrom, senderID) {
		log.WithField("sender", senderID).Info("NapCat: access denied")
		return nil
	}

	// 解析消息段
	content, media, mentionedBot := n.parseMessageSegments(event.Message, event.SelfID)

	// 群消息必须 @bot 才处理，私聊消息直接处理
	if event.MessageType == "group" && !mentionedBot {
		log.WithField("group_id", event.GroupID).Debug("NapCat: group message without @bot, skipping")
		return nil
	}

	// 如果消息为空（可能全是表情或 @bot），跳过
	if content == "" && len(media) == 0 {
		return nil
	}

	// 构建入站消息
	senderName := event.Sender.Nickname
	if event.Sender.Card != "" {
		senderName = event.Sender.Card // 群名片优先
	}

	var chatID string
	var chatType string
	var xbotChatType string

	switch event.MessageType {
	case "private":
		chatID = senderID
		chatType = "private"
		xbotChatType = "p2p"
	case "group":
		chatID = fmt.Sprintf("%d", event.GroupID)
		chatType = "group"
		xbotChatType = "group"
	default:
		chatID = senderID
		chatType = event.MessageType
		xbotChatType = "p2p"
	}

	requestID := log.NewRequestID()

	inbound := bus.InboundMessage{
		From:       bus.NewIMAddress("napcat", senderID),
		To:         bus.NewIMAddress("napcat", chatID),
		Channel:    "napcat",
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     chatID,
		ChatType:   xbotChatType,
		Content:    content,
		Media:      media,
		Time: func() time.Time {
			if event.Time == 0 {
				return time.Now()
			}
			return time.Unix(event.Time, 0)
		}(),
		RequestID: requestID,
		Metadata: map[string]string{
			"message_id":   messageID,
			"chat_type":    chatType,
			"self_id":      fmt.Sprintf("%d", event.SelfID),
			"reply_policy": "optional", // QQ 不支持 patch，禁用 ACK 和进度通知
		},
	}

	// 缓存 chatID 对应的聊天类型，供 Send 方法使用
	n.chatTypeCache.Store(chatID, chatType)

	n.msgBus.Inbound <- inbound
	return nil
}

// ---------------------------------------------------------------------------
// Message segment parsing
// ---------------------------------------------------------------------------

// parseMessageSegments 解析 OneBot 11 消息段数组，返回文本内容、媒体 URL 列表和是否 @bot
// selfID 用于过滤群消息中 @bot 的消息段
func (n *NapCatChannel) parseMessageSegments(raw json.RawMessage, selfID int64) (string, []string, bool) {
	if len(raw) == 0 {
		return "", nil, false
	}

	var segments []obMessageSegment
	if err := json.Unmarshal(raw, &segments); err != nil {
		// 可能是字符串格式的消息（messagePostFormat=string），直接返回
		var s string
		if err2 := json.Unmarshal(raw, &s); err2 == nil {
			return s, nil, false
		}
		log.WithError(err).Debug("NapCat: failed to parse message segments")
		return "", nil, false
	}

	var textParts []string
	var media []string
	selfIDStr := fmt.Sprintf("%d", selfID)
	mentionedBot := false

	for _, seg := range segments {
		switch seg.Type {
		case "text":
			var data obTextData
			if err := json.Unmarshal(seg.Data, &data); err == nil && data.Text != "" {
				textParts = append(textParts, data.Text)
			}

		case "image":
			var data obImageData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				url := data.URL
				if url == "" {
					url = data.File
				}
				if url != "" {
					media = append(media, url)
				}
			}

		case "at":
			var data obAtData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				// 检测 @bot 自己或 @all
				qqStr := formatQQ(data.QQ)
				if qqStr == selfIDStr || qqStr == "all" {
					mentionedBot = true
					continue
				}
				textParts = append(textParts, fmt.Sprintf("@%s", qqStr))
			}

		case "reply":
			// 回复消息段，不添加到文本中，但可以记录
			// metadata 中已有 message_id，reply 的 id 可以忽略

		case "face":
			// QQ 表情，忽略

		case "record":
			var data obMediaData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				url := data.URL
				if url == "" {
					url = data.File
				}
				if url != "" {
					media = append(media, url)
				}
			}

		case "video":
			var data obMediaData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				url := data.URL
				if url == "" {
					url = data.File
				}
				if url != "" {
					media = append(media, url)
				}
			}

		case "file":
			var data obMediaData
			if err := json.Unmarshal(seg.Data, &data); err == nil {
				url := data.URL
				if url == "" {
					url = data.File
				}
				if url != "" {
					media = append(media, url)
				}
			}

		default:
			log.WithField("type", seg.Type).Debug("NapCat: unknown message segment type")
		}
	}

	text := strings.TrimSpace(strings.Join(textParts, ""))
	return text, media, mentionedBot
}

// ---------------------------------------------------------------------------
// Send (outbound)
// ---------------------------------------------------------------------------

// Send 发送消息到 NapCat
func (n *NapCatChannel) Send(msg bus.OutboundMessage) (string, error) {
	if msg.Content == "" && len(msg.Media) == 0 {
		return "", nil
	}

	// QQ 不支持 patch（原地更新消息），直接发送新消息。
	// reply_policy=optional 已禁用 ACK 和进度通知，此处只会收到最终回复。

	chatType := ""
	if msg.Metadata != nil {
		chatType = msg.Metadata["chat_type"]
	}
	// 从缓存推断聊天类型
	if chatType == "" {
		if cached, ok := n.chatTypeCache.Load(msg.ChatID); ok {
			chatType = cached.(string)
		}
	}

	// 构建消息内容（消息段数组）
	message := n.buildOutboundMessage(msg.Content, msg.Media)

	// 根据 chat_type 选择 API
	switch chatType {
	case "group":
		groupID, err := strconv.ParseInt(msg.ChatID, 10, 64)
		if err != nil {
			return "", fmt.Errorf("napcat: invalid group_id %q: %w", msg.ChatID, err)
		}
		return n.sendGroupMsg(groupID, message)

	case "private":
		userID, err := strconv.ParseInt(msg.ChatID, 10, 64)
		if err != nil {
			return "", fmt.Errorf("napcat: invalid user_id %q: %w", msg.ChatID, err)
		}
		return n.sendPrivateMsg(userID, message)

	default:
		// 无法确定聊天类型，默认尝试私聊
		log.WithField("chat_id", msg.ChatID).Warn("NapCat: unknown chat type, defaulting to private")
		id, err := strconv.ParseInt(msg.ChatID, 10, 64)
		if err != nil {
			return "", fmt.Errorf("napcat: invalid chat_id %q: %w", msg.ChatID, err)
		}
		return n.sendPrivateMsg(id, message)
	}
}

// mediaTypeFromURL 从 URL/路径扩展名推断 OneBot 媒体消息段类型
func mediaTypeFromURL(url string) string {
	switch {
	case strings.HasSuffix(strings.ToLower(url), ".mp3"),
		strings.HasSuffix(strings.ToLower(url), ".wav"),
		strings.HasSuffix(strings.ToLower(url), ".silk"),
		strings.HasSuffix(strings.ToLower(url), ".amr"):
		return "record"
	case strings.HasSuffix(strings.ToLower(url), ".mp4"),
		strings.HasSuffix(strings.ToLower(url), ".avi"):
		return "video"
	default:
		return "image"
	}
}

// buildOutboundMessage 构建出站消息内容
// 如果只有文本，返回纯文本字符串；如果有媒体，返回消息段数组
func (n *NapCatChannel) buildOutboundMessage(content string, media []string) any {
	if len(media) == 0 {
		return content
	}

	// 构建消息段数组
	var segments []map[string]any

	// 添加文本段
	if content != "" {
		segments = append(segments, map[string]any{
			"type": "text",
			"data": map[string]string{
				"text": content,
			},
		})
	}

	// 添加媒体段
	for _, url := range media {
		segments = append(segments, map[string]any{
			"type": mediaTypeFromURL(url),
			"data": map[string]string{
				"file": url,
			},
		})
	}

	return segments
}

// sendPrivateMsg 发送私聊消息
func (n *NapCatChannel) sendPrivateMsg(userID int64, message any) (string, error) {
	resp, err := n.callAPI("send_private_msg", map[string]any{
		"user_id": userID,
		"message": message,
	})
	if err != nil {
		return "", fmt.Errorf("napcat: send_private_msg failed: %w", err)
	}

	var result obSendMsgResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", nil // 发送成功但解析响应失败，不影响
	}
	return fmt.Sprintf("%d", result.MessageID), nil
}

// sendGroupMsg 发送群消息
func (n *NapCatChannel) sendGroupMsg(groupID int64, message any) (string, error) {
	resp, err := n.callAPI("send_group_msg", map[string]any{
		"group_id": groupID,
		"message":  message,
	})
	if err != nil {
		return "", fmt.Errorf("napcat: send_group_msg failed: %w", err)
	}

	var result obSendMsgResponse
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		return "", nil
	}
	return fmt.Sprintf("%d", result.MessageID), nil
}

// ---------------------------------------------------------------------------
// API call with echo matching
// ---------------------------------------------------------------------------

// callAPI 调用 OneBot 11 API，通过 echo 匹配响应
func (n *NapCatChannel) callAPI(action string, params any) (*obAPIResponse, error) {
	echo := uuid.New().String()

	// 注册 pending 响应通道
	ch := make(chan json.RawMessage, 1)
	n.pendingMu.Lock()
	n.pending[echo] = ch
	n.pendingMu.Unlock()

	// 发送请求
	req := obAPIRequest{
		Action: action,
		Params: params,
		Echo:   echo,
	}

	if err := n.wsSend(req); err != nil {
		n.pendingMu.Lock()
		delete(n.pending, echo)
		n.pendingMu.Unlock()
		return nil, fmt.Errorf("ws send: %w", err)
	}

	// 等待响应（超时 30s）
	select {
	case data := <-ch:
		if data == nil {
			return nil, fmt.Errorf("napcat: connection closed while waiting for %s response", action)
		}
		var resp obAPIResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, fmt.Errorf("parse api response: %w", err)
		}
		if resp.RetCode != 0 {
			return &resp, fmt.Errorf("api error: status=%s retcode=%d", resp.Status, resp.RetCode)
		}
		return &resp, nil
	case <-time.After(30 * time.Second):
		n.pendingMu.Lock()
		if ch, ok := n.pending[echo]; ok {
			close(ch)
			delete(n.pending, echo)
		}
		n.pendingMu.Unlock()
		return nil, fmt.Errorf("api call %s timed out", action)

	case <-n.stopCh:
		n.pendingMu.Lock()
		if ch, ok := n.pending[echo]; ok {
			close(ch)
			delete(n.pending, echo)
		}
		n.pendingMu.Unlock()
		return nil, fmt.Errorf("channel stopped")
	}
}

// ---------------------------------------------------------------------------
// WebSocket helpers
// ---------------------------------------------------------------------------

// clearPending 清理所有 pending 请求
func (n *NapCatChannel) clearPending() {
	n.pendingMu.Lock()
	defer n.pendingMu.Unlock()

	for echo, ch := range n.pending {
		close(ch)
		delete(n.pending, echo)
	}
}

// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Access control
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Quick disconnect detection
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// truncate 截断字符串用于日志
func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
}
