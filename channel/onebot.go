package channel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	log "xbot/logger"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// OneBotConfig 配置
// ---------------------------------------------------------------------------

// OneBotConfig OneBot v11 渠道配置
type OneBotConfig struct {
	WSUrl     string
	HTTPUrl   string
	Token     string
	AllowFrom []string
}

// ---------------------------------------------------------------------------
// Reconnect strategy
// ---------------------------------------------------------------------------

var onebotReconnectDelays = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

const onebotMaxReconnectAttempts = 100

// ---------------------------------------------------------------------------
// CQ 码正则
// ---------------------------------------------------------------------------

const onebotMaxImageSize = 50 << 20 // 50 MB

var (
	cqImageRe     = regexp.MustCompile(`\[CQ:image,[^\]]*url=([^\],]+)[^\]]*\]`)
	cqCodeRe      = regexp.MustCompile(`\[CQ:[^\]]+\]`)
	onebotMdImgRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

	// Markdown 格式正则（预编译）
	onebotMdBoldRe      = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	onebotMdUnderBoldRe = regexp.MustCompile(`__([^_]+)__`)
	onebotMdItalicRe    = regexp.MustCompile(`\*([^*]+)\*`)
	onebotMdUnderItalRe = regexp.MustCompile(`_([^_]+)_`)
	onebotMdCodeRe      = regexp.MustCompile("`([^`]+)`")
	onebotMdHeadingRe   = regexp.MustCompile(`(?m)^#{1,6}\s+`)
	onebotMdLinkRe      = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
)

// ---------------------------------------------------------------------------
// OneBot 事件结构
// ---------------------------------------------------------------------------

type onebotEvent struct {
	PostType    string       `json:"post_type"`
	MessageType string       `json:"message_type"`
	SubType     string       `json:"sub_type"`
	MessageID   int64        `json:"message_id"`
	UserID      int64        `json:"user_id"`
	GroupID     int64        `json:"group_id"`
	Message     string       `json:"message"`
	RawMessage  string       `json:"raw_message"`
	SelfID      int64        `json:"self_id"`
	Sender      onebotSender `json:"sender"`
}

type onebotSender struct {
	UserID   int64  `json:"user_id"`
	Nickname string `json:"nickname"`
}

// ---------------------------------------------------------------------------
// OneBotChannel 实现
// ---------------------------------------------------------------------------

// OneBotChannel OneBot v11 渠道实现
type OneBotChannel struct {
	cfg      OneBotConfig
	bus      *bus.MessageBus
	wsConn   *websocket.Conn
	httpCli  *http.Client
	done     chan struct{}
	mu       sync.Mutex
	selfID   string         // bot 自身 QQ 号，从 lifecycle 事件获取
	selfAtRe *regexp.Regexp // 预编译的 at 自己 CQ 码正则
	chatMeta sync.Map       // chatID -> chatType ("p2p"/"group") 缓存
}

// NewOneBotChannel 创建 OneBot 渠道
func NewOneBotChannel(cfg OneBotConfig, msgBus *bus.MessageBus) *OneBotChannel {
	return &OneBotChannel{
		cfg:     cfg,
		bus:     msgBus,
		httpCli: &http.Client{Timeout: 30 * time.Second},
		done:    make(chan struct{}),
	}
}

// Name 返回渠道名称
func (c *OneBotChannel) Name() string { return "onebot" }

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

// Start 启动 OneBot 渠道（非阻塞）
func (c *OneBotChannel) Start() error {
	go c.connectAndListen()
	return nil
}

// Stop 停止 OneBot 渠道
func (c *OneBotChannel) Stop() {
	select {
	case <-c.done:
		// already closed
	default:
		close(c.done)
	}

	c.mu.Lock()
	if c.wsConn != nil {
		c.wsConn.Close()
		c.wsConn = nil
	}
	c.mu.Unlock()

	log.Info("OneBot: channel stopped")
}

// ---------------------------------------------------------------------------
// WebSocket 连接与监听
// ---------------------------------------------------------------------------

func (c *OneBotChannel) connectAndListen() {
	attempt := 0
	for {
		select {
		case <-c.done:
			return
		default:
		}

		if attempt >= onebotMaxReconnectAttempts {
			log.Error("OneBot: exceeded max reconnect attempts, giving up")
			return
		}

		connected, err := c.runOnce()

		select {
		case <-c.done:
			return
		default:
		}

		if err != nil {
			log.WithError(err).Warn("OneBot: WebSocket session ended")
		}

		// 如果曾成功建立连接并收到过消息，重置重连计数
		if connected {
			attempt = 0
		}

		delay := onebotReconnectDelays[attempt%len(onebotReconnectDelays)]
		if attempt >= len(onebotReconnectDelays) {
			delay = onebotReconnectDelays[len(onebotReconnectDelays)-1]
		}

		log.WithFields(log.Fields{
			"attempt": attempt + 1,
			"delay":   delay,
		}).Info("OneBot: reconnecting...")

		select {
		case <-time.After(delay):
		case <-c.done:
			return
		}
		attempt++
	}
}

// runOnce 建立一次 WS 连接并读取消息，返回时表示连接断开。
// connected 为 true 表示曾成功建立连接并收到过消息。
func (c *OneBotChannel) runOnce() (connected bool, err error) {
	wsURL := c.cfg.WSUrl
	if c.cfg.Token != "" {
		if strings.Contains(wsURL, "?") {
			wsURL += "&access_token=" + c.cfg.Token
		} else {
			wsURL += "?access_token=" + c.cfg.Token
		}
	}

	log.WithField("url", wsURL).Info("OneBot: connecting to WebSocket...")

	conn, _, dialErr := websocket.DefaultDialer.Dial(wsURL, nil)
	if dialErr != nil {
		return false, fmt.Errorf("onebot: ws dial: %w", dialErr)
	}

	c.mu.Lock()
	c.wsConn = conn
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.wsConn == conn {
			conn.Close()
			c.wsConn = nil
		}
		c.mu.Unlock()
	}()

	// 设置读超时和 Pong 处理
	const (
		wsReadTimeout = 3 * time.Minute
		wsPingPeriod  = 30 * time.Second
	)

	conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
		return nil
	})

	// 启动 Ping goroutine
	pingDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(wsPingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.mu.Lock()
				writeErr := conn.WriteControl(
					websocket.PingMessage, nil,
					time.Now().Add(10*time.Second),
				)
				c.mu.Unlock()
				if writeErr != nil {
					return
				}
			case <-pingDone:
				return
			case <-c.done:
				return
			}
		}
	}()
	defer close(pingDone)

	log.Info("OneBot: WebSocket connected")

	for {
		select {
		case <-c.done:
			return connected, nil
		default:
		}

		_, data, readErr := conn.ReadMessage()
		if readErr != nil {
			return connected, fmt.Errorf("onebot: ws read: %w", readErr)
		}

		// 成功读到消息，标记已连接
		connected = true
		// 重置读超时
		conn.SetReadDeadline(time.Now().Add(wsReadTimeout))

		// 解析基础 JSON 判断 post_type
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			log.WithError(err).Debug("OneBot: failed to parse ws message")
			continue
		}

		var postType string
		if pt, ok := raw["post_type"]; ok {
			json.Unmarshal(pt, &postType)
		}

		switch postType {
		case "meta_event":
			// 提取 self_id
			var selfID int64
			if sid, ok := raw["self_id"]; ok {
				json.Unmarshal(sid, &selfID)
			}
			if selfID != 0 {
				c.selfID = strconv.FormatInt(selfID, 10)
				c.selfAtRe = regexp.MustCompile(`\[CQ:at,qq=` + regexp.QuoteMeta(c.selfID) + `[^\]]*\]`)
				log.WithField("self_id", c.selfID).Info("OneBot: bot self_id obtained")
			}
		case "message":
			c.handleOneBotMessage(data)
		default:
			// 其他事件（notice, request 等）忽略
			log.WithField("post_type", postType).Debug("OneBot: unhandled event type")
		}
	}
}

// ---------------------------------------------------------------------------
// 消息处理
// ---------------------------------------------------------------------------

func (c *OneBotChannel) handleOneBotMessage(data []byte) {
	var evt onebotEvent
	if err := json.Unmarshal(data, &evt); err != nil {
		log.WithError(err).Warn("OneBot: failed to parse message event")
		return
	}

	userIDStr := strconv.FormatInt(evt.UserID, 10)

	// AllowFrom 检查
	if len(c.cfg.AllowFrom) > 0 {
		allowed := false
		for _, a := range c.cfg.AllowFrom {
			if a == userIDStr {
				allowed = true
				break
			}
		}
		if !allowed {
			log.WithField("user_id", userIDStr).Debug("OneBot: access denied")
			return
		}
	}

	// 确定 chatType 和 chatID
	var chatType, chatID string
	if evt.MessageType == "private" {
		chatType = "p2p"
		chatID = "private_" + userIDStr
	} else if evt.MessageType == "group" {
		chatType = "group"
		chatID = "group_" + strconv.FormatInt(evt.GroupID, 10)
	} else {
		chatType = evt.MessageType
		chatID = "other_" + userIDStr
	}

	// 缓存 chatMeta
	c.chatMeta.Store(chatID, chatType)

	// 解析消息内容
	message := evt.Message
	if message == "" {
		message = evt.RawMessage
	}

	// 去除 at 自己的 CQ 码
	if c.selfAtRe != nil {
		message = c.selfAtRe.ReplaceAllString(message, "")
	}

	// 提取图片 URL 并下载
	var mediaFiles []string
	imgURLs := parseCQImages(message)
	for _, imgURL := range imgURLs {
		localPath, err := c.downloadOneBotImage(imgURL)
		if err != nil {
			log.WithError(err).WithField("url", imgURL).Warn("OneBot: failed to download image")
			continue
		}
		mediaFiles = append(mediaFiles, localPath)
	}

	// 去除所有 CQ 码，得到纯文本
	cleanText := stripCQCodes(message)
	cleanText = strings.TrimSpace(cleanText)

	if cleanText == "" && len(mediaFiles) == 0 {
		return
	}

	senderName := evt.Sender.Nickname

	log.WithFields(log.Fields{
		"message_id":  evt.MessageID,
		"user_id":     userIDStr,
		"sender_name": senderName,
		"chat_type":   chatType,
		"chat_id":     chatID,
		"content_len": len(cleanText),
		"media_count": len(mediaFiles),
	}).Info("OneBot: message received")

	c.bus.Inbound <- bus.InboundMessage{
		Channel:    "onebot",
		SenderID:   userIDStr,
		SenderName: senderName,
		ChatID:     chatID,
		ChatType:   chatType,
		Content:    cleanText,
		Media:      mediaFiles,
		Time:       time.Now(),
		Metadata: map[string]string{
			"message_id":   strconv.FormatInt(evt.MessageID, 10),
			"chat_type":    chatType,
			"message_type": evt.MessageType,
		},
	}
}

// ---------------------------------------------------------------------------
// Send (outbound)
// ---------------------------------------------------------------------------

// Send 发送消息到 OneBot，返回平台消息 ID
func (c *OneBotChannel) Send(msg bus.OutboundMessage) (string, error) {
	if msg.Content == "" && len(msg.Media) == 0 {
		return "", nil
	}

	// 从 chatMeta 或 chatID 前缀推断 chatType
	chatType := ""
	if v, ok := c.chatMeta.Load(msg.ChatID); ok {
		chatType, _ = v.(string)
	}
	if chatType == "" {
		if strings.HasPrefix(msg.ChatID, "group_") {
			chatType = "group"
		} else if strings.HasPrefix(msg.ChatID, "private_") {
			chatType = "p2p"
		}
	}

	// 提取 chatID 中的数字部分
	numericID := extractNumericID(msg.ChatID)
	if numericID == "" {
		return "", fmt.Errorf("onebot: cannot extract numeric ID from chatID: %s", msg.ChatID)
	}

	// 处理消息内容
	content := msg.Content
	if content != "" {
		content = markdownToOneBotMessage(content)
	}

	// 处理 Media 附件
	for _, mediaPath := range msg.Media {
		absPath, err := filepath.Abs(mediaPath)
		if err != nil {
			absPath = mediaPath
		}
		ext := strings.ToLower(filepath.Ext(absPath))
		switch ext {
		case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
			content += fmt.Sprintf("[CQ:image,file=file:///%s]", absPath)
		default:
			// 非图片文件，尝试通过 upload_file API 发送
			if err := c.uploadFile(chatType, numericID, absPath); err != nil {
				log.WithError(err).WithField("path", absPath).Warn("OneBot: failed to upload file")
				// 回退：在消息中附加文件路径提示
				content += fmt.Sprintf("\n📎 文件: %s", filepath.Base(absPath))
			}
		}
	}

	if content == "" {
		return "", nil
	}

	// 发送消息
	id, err := strconv.ParseInt(numericID, 10, 64)
	if err != nil {
		return "", fmt.Errorf("onebot: invalid numeric ID %q: %w", numericID, err)
	}

	var endpoint string
	var payload map[string]any

	if chatType == "group" {
		endpoint = "/send_group_msg"
		payload = map[string]any{
			"group_id": id,
			"message":  content,
		}
	} else {
		// 默认私聊
		endpoint = "/send_private_msg"
		payload = map[string]any{
			"user_id": id,
			"message": content,
		}
	}

	respData, err := c.callAPI(endpoint, payload)
	if err != nil {
		return "", fmt.Errorf("onebot: send message: %w", err)
	}

	// 解析 message_id
	var result struct {
		MessageID int64 `json:"message_id"`
	}
	if err := json.Unmarshal(respData, &result); err != nil {
		log.WithError(err).Debug("OneBot: could not parse send response for message_id")
		return "", nil
	}

	msgIDStr := strconv.FormatInt(result.MessageID, 10)
	log.WithFields(log.Fields{
		"chat_type":  chatType,
		"target":     numericID,
		"message_id": msgIDStr,
	}).Debug("OneBot: message sent")

	return msgIDStr, nil
}

// ---------------------------------------------------------------------------
// HTTP API 调用
// ---------------------------------------------------------------------------

// callAPI 调用 OneBot HTTP API
func (c *OneBotChannel) callAPI(endpoint string, payload any) (json.RawMessage, error) {
	url := strings.TrimRight(c.cfg.HTTPUrl, "/") + endpoint

	jsonBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}

	log.WithFields(log.Fields{
		"url":      url,
		"body_len": len(jsonBody),
	}).Debug("OneBot: calling API")

	resp, err := c.httpCli.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	// OneBot v11 响应格式: {"status":"ok","retcode":0,"data":{...}}
	var apiResp struct {
		Status  string          `json:"status"`
		RetCode int             `json:"retcode"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &apiResp); err != nil {
		return nil, fmt.Errorf("parse response: %w (body: %s)", err, string(respBody))
	}

	if apiResp.Status != "ok" && apiResp.Status != "async" {
		return nil, fmt.Errorf("API returned status=%s retcode=%d body=%s", apiResp.Status, apiResp.RetCode, string(respBody))
	}

	return apiResp.Data, nil
}

// uploadFile 通过 upload_file API 上传文件（部分 OneBot 实现支持）
func (c *OneBotChannel) uploadFile(chatType, targetID, filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		absPath = filePath
	}

	// 尝试使用 upload_group_file 或 upload_private_file
	var endpoint string
	payload := map[string]any{
		"file": "file:///" + absPath,
		"name": filepath.Base(absPath),
	}

	id, _ := strconv.ParseInt(targetID, 10, 64)

	if chatType == "group" {
		endpoint = "/upload_group_file"
		payload["group_id"] = id
	} else {
		endpoint = "/upload_private_file"
		payload["user_id"] = id
	}

	_, err = c.callAPI(endpoint, payload)
	return err
}

// ---------------------------------------------------------------------------
// CQ 码处理辅助函数
// ---------------------------------------------------------------------------

// parseCQImages 从消息中提取图片 URL
func parseCQImages(message string) []string {
	matches := cqImageRe.FindAllStringSubmatch(message, -1)
	var urls []string
	for _, m := range matches {
		if len(m) >= 2 {
			urls = append(urls, m[1])
		}
	}
	return urls
}

// stripCQCodes 去除所有 CQ 码，返回纯文本
func stripCQCodes(message string) string {
	return cqCodeRe.ReplaceAllString(message, "")
}

// markdownToOneBotMessage 将 Markdown 内容转为 OneBot 消息
// 提取本地图片转 CQ 码，其余去除 Markdown 格式保留文本
func markdownToOneBotMessage(content string) string {
	// 将 Markdown 图片 ![alt](path) 转为 CQ 码（仅本地文件）
	content = onebotMdImgRe.ReplaceAllStringFunc(content, func(match string) string {
		subs := onebotMdImgRe.FindStringSubmatch(match)
		if len(subs) < 2 {
			return match
		}
		imgPath := subs[1]

		// 跳过 URL
		if strings.HasPrefix(imgPath, "http://") || strings.HasPrefix(imgPath, "https://") {
			return match
		}

		// 检查文件是否存在
		absPath, err := filepath.Abs(imgPath)
		if err != nil {
			return match
		}
		if _, err := os.Stat(absPath); err != nil {
			return match
		}

		return fmt.Sprintf("[CQ:image,file=file:///%s]", absPath)
	})

	// 去除 Markdown 格式标记，保留文本
	// 去除粗体 **text** 或 __text__
	content = onebotMdBoldRe.ReplaceAllString(content, "$1")
	content = onebotMdUnderBoldRe.ReplaceAllString(content, "$1")
	// 去除斜体 *text* 或 _text_
	content = onebotMdItalicRe.ReplaceAllString(content, "$1")
	content = onebotMdUnderItalRe.ReplaceAllString(content, "$1")
	// 去除行内代码 `code`
	content = onebotMdCodeRe.ReplaceAllString(content, "$1")
	// 去除标题标记 # ## ### 等
	content = onebotMdHeadingRe.ReplaceAllString(content, "")
	// 去除链接 [text](url) → text (url)
	content = onebotMdLinkRe.ReplaceAllString(content, "$1 ($2)")

	return content
}

// downloadOneBotImage 下载图片到本地临时目录
func (c *OneBotChannel) downloadOneBotImage(imgURL string) (string, error) {
	dir := "/tmp/onebot_images"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create image dir: %w", err)
	}

	// 用 UUID 命名避免冲突
	ext := filepath.Ext(imgURL)
	// 清理 ext 中可能的查询参数
	if idx := strings.Index(ext, "?"); idx != -1 {
		ext = ext[:idx]
	}
	if ext == "" || len(ext) > 5 {
		ext = ".jpg"
	}
	localPath := filepath.Join(dir, uuid.NewString()+ext)

	resp, err := c.httpCli.Get(imgURL)
	if err != nil {
		return "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download image: status %d", resp.StatusCode)
	}

	f, err := os.Create(localPath)
	if err != nil {
		return "", fmt.Errorf("create image file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, io.LimitReader(resp.Body, onebotMaxImageSize)); err != nil {
		os.Remove(localPath)
		return "", fmt.Errorf("write image file: %w", err)
	}

	return localPath, nil
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// extractNumericID 从 chatID 中提取数字部分
// 例如 "group_123456" → "123456", "private_789" → "789"
func extractNumericID(chatID string) string {
	// 尝试从前缀分割
	parts := strings.SplitN(chatID, "_", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	// 如果没有前缀，直接返回（可能本身就是数字）
	return chatID
}
