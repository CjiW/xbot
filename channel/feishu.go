package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	log "xbot/logger"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// FeishuConfig 飞书渠道配置
type FeishuConfig struct {
	AppID             string   // App ID
	AppSecret         string   // App Secret
	EncryptKey        string   // 事件订阅加密 Key（可选）
	VerificationToken string   // 事件订阅验证 Token（可选）
	AllowFrom         []string // 允许的用户 open_id 白名单（空则允许所有人）
}

// FeishuChannel 飞书渠道实现
type FeishuChannel struct {
	config   FeishuConfig
	msgBus   *bus.MessageBus
	client   *lark.Client
	wsClient *larkws.Client
	running  bool
	mu       sync.Mutex

	// 消息去重缓存
	processedIDs   map[string]struct{}
	processedOrder []string
	maxProcessed   int
}

// NewFeishuChannel 创建飞书渠道
func NewFeishuChannel(cfg FeishuConfig, msgBus *bus.MessageBus) *FeishuChannel {
	return &FeishuChannel{
		config:       cfg,
		msgBus:       msgBus,
		processedIDs: make(map[string]struct{}),
		maxProcessed: 1000,
	}
}

func (f *FeishuChannel) Name() string { return "feishu" }

// Start 启动飞书 WebSocket 长连接
func (f *FeishuChannel) Start() error {
	if f.config.AppID == "" || f.config.AppSecret == "" {
		return fmt.Errorf("feishu app_id and app_secret are required")
	}

	f.mu.Lock()
	f.running = true
	f.mu.Unlock()

	// 创建 Lark 客户端（用于发送消息）
	f.client = lark.NewClient(f.config.AppID, f.config.AppSecret,
		lark.WithLogLevel(larkcore.LogLevelInfo),
	)

	// 创建事件处理器
	eventHandler := dispatcher.NewEventDispatcher(
		f.config.VerificationToken,
		f.config.EncryptKey,
	).OnP2MessageReceiveV1(f.onMessage).
		OnP2CardActionTrigger(f.onCardAction)

	// 创建 WebSocket 客户端
	f.wsClient = larkws.NewClient(
		f.config.AppID,
		f.config.AppSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(larkcore.LogLevelInfo),
	)

	log.Info("Feishu bot starting with WebSocket long connection...")

	// wsClient.Start() 会阻塞
	err := f.wsClient.Start(context.Background())
	if err != nil {
		return fmt.Errorf("feishu WebSocket failed: %w", err)
	}
	return nil
}

// Stop 停止飞书渠道
func (f *FeishuChannel) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running = false
	log.Info("Feishu bot stopped")
}

// Send 发送消息到飞书
func (f *FeishuChannel) Send(msg bus.OutboundMessage) error {
	if f.client == nil {
		return fmt.Errorf("feishu client not initialized")
	}

	if msg.Content == "" {
		return nil
	}

	// 检测 card builder 生成的完整卡片 JSON
	if strings.HasPrefix(msg.Content, "__FEISHU_CARD__:") {
		cardJSON := strings.TrimPrefix(msg.Content, "__FEISHU_CARD__:")
		return f.sendNormalMessage(msg.ChatID, []byte(cardJSON))
	}

	originalLen := len(msg.Content)
	log.WithFields(log.Fields{
		"chat_id":     msg.ChatID,
		"content_len": originalLen,
	}).Debug("Feishu: sending message")

	// 1) 提取 markdown 中的本地文件链接 [name](path)，上传并单独发送，从内容中移除
	content := f.extractAndSendLocalFiles(msg.ChatID, msg.Content)
	// 2) 替换 markdown 中的本地图片引用 ![alt](path) 为飞书 image_key
	content = f.replaceLocalImages(content)

	if strings.TrimSpace(content) == "" {
		return nil
	}

	if len(content) != originalLen {
		log.WithFields(log.Fields{
			"original_len": originalLen,
			"final_len":    len(content),
		}).Debug("Feishu: content length changed after processing")
	}

	// 构建消息卡片
	card := f.buildCard(content)
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal card: %w", err)
	}

	// 检查是否需要回复消息（reply 模式）
	messageID := ""
	if msg.Metadata != nil {
		messageID = msg.Metadata["message_id"]
	}

	if messageID != "" {
		// 使用回复模式
		return f.sendReplyMessage(msg.ChatID, messageID, cardJSON)
	}

	// 普通发送模式
	return f.sendNormalMessage(msg.ChatID, cardJSON)
}

// sendReplyMessage 发送回复消息
func (f *FeishuChannel) sendReplyMessage(chatID, parentID string, cardJSON []byte) error {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(parentID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(string(cardJSON)).
			Build()).
		Build()

	log.WithFields(log.Fields{
		"chat_id":    chatID,
		"parent_id":  parentID,
		"card_len":   len(cardJSON),
	}).Debug("Feishu: sending reply message")

	resp, err := f.client.Im.Message.Reply(context.Background(), req)
	if err != nil {
		return fmt.Errorf("send feishu reply message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	log.WithFields(log.Fields{
		"chat_id":   chatID,
		"parent_id": parentID,
	}).Debug("Feishu reply message sent")
	return nil
}

// sendNormalMessage 发送普通消息
func (f *FeishuChannel) sendNormalMessage(chatID string, cardJSON []byte) error {
	// 判断 receive_id_type
	receiveIDType := "chat_id"
	if !strings.HasPrefix(chatID, "oc_") {
		receiveIDType = "open_id"
	}

	log.WithFields(log.Fields{
		"chat_id":   chatID,
		"card_len":  len(cardJSON),
	}).Debug("Feishu: sending normal message")

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("interactive").
			Content(string(cardJSON)).
			Build()).
		Build()

	resp, err := f.client.Im.Message.Create(context.Background(), req)
	if err != nil {
		return fmt.Errorf("send feishu message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	log.WithField("chat_id", chatID).Debug("Feishu message sent")
	return nil
}

// imageExtensions 图片文件扩展名集合
var imageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true,
	".gif": true, ".bmp": true, ".ico": true, ".tiff": true, ".heic": true,
}

// mdLinkRe 匹配 markdown 链接语法 [name](path)，但不匹配图片 ![alt](path)
var mdLinkRe = regexp.MustCompile(`(?:^|[^!])\[([^\]]+)\]\(([^)]+)\)`)

// extractAndSendLocalFiles 从 markdown 中提取本地文件链接（非图片），上传并发送文件消息，从内容中移除该链接
func (f *FeishuChannel) extractAndSendLocalFiles(chatID, content string) string {
	return mdLinkRe.ReplaceAllStringFunc(content, func(match string) string {
		subs := mdLinkRe.FindStringSubmatch(match)
		if len(subs) < 3 {
			return match
		}
		linkPath := subs[2]

		// 保留前缀字符（mdLinkRe 可能捕获了 [ 前的非 ! 字符）
		prefix := ""
		if len(match) > 0 && match[0] != '[' {
			prefix = string(match[0])
		}

		// 跳过 URL
		if strings.HasPrefix(linkPath, "http://") || strings.HasPrefix(linkPath, "https://") {
			return match
		}

		// 跳过图片扩展名（图片由 replaceLocalImages 处理）
		ext := strings.ToLower(filepath.Ext(linkPath))
		if imageExtensions[ext] {
			return match
		}

		// 检查文件是否存在
		if _, err := os.Stat(linkPath); err != nil {
			return match
		}

		// 上传并发送文件
		if err := f.sendFile(chatID, linkPath); err != nil {
			log.WithError(err).WithField("path", linkPath).Warn("Failed to send local file")
			return match
		}

		log.WithField("path", linkPath).Debug("Sent local file from markdown link")

		// 替换链接为纯文本提示
		return prefix + "📎 " + subs[1]
	})
}

// sendFile 上传并发送文件消息
func (f *FeishuChannel) sendFile(chatID, filePath string) error {
	fileKey, err := f.uploadFile(filePath)
	if err != nil {
		return fmt.Errorf("upload file: %w", err)
	}

	receiveIDType := "chat_id"
	if !strings.HasPrefix(chatID, "oc_") {
		receiveIDType = "open_id"
	}

	fileName := filepath.Base(filePath)
	content, _ := json.Marshal(map[string]string{"file_key": fileKey, "file_name": fileName})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("file").
			Content(string(content)).
			Build()).
		Build()

	resp, err := f.client.Im.Message.Create(context.Background(), req)
	if err != nil {
		return fmt.Errorf("send file message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	log.WithFields(log.Fields{
		"chat_id":  chatID,
		"file_key": fileKey,
	}).Debug("Feishu file sent")
	return nil
}

// uploadImage 上传图片到飞书，返回 image_key
func (f *FeishuChannel) uploadImage(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file %s: %w", filePath, err)
	}
	defer file.Close()

	imageType := "message"
	req := larkim.NewCreateImageReqBuilder().
		Body(&larkim.CreateImageReqBody{
			ImageType: &imageType,
			Image:     file,
		}).
		Build()

	resp, err := f.client.Im.Image.Create(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("upload image API: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload image error: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return *resp.Data.ImageKey, nil
}

// uploadFile 上传文件到飞书，返回 file_key
func (f *FeishuChannel) uploadFile(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file %s: %w", filePath, err)
	}
	defer file.Close()

	fileName := filepath.Base(filePath)
	fileType := f.detectFileType(filePath)

	req := larkim.NewCreateFileReqBuilder().
		Body(&larkim.CreateFileReqBody{
			FileType: &fileType,
			FileName: &fileName,
			File:     file,
		}).
		Build()

	resp, err := f.client.Im.File.Create(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("upload file API: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload file error: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	return *resp.Data.FileKey, nil
}

// detectFileType 根据扩展名检测飞书文件类型
func (f *FeishuChannel) detectFileType(filePath string) string {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".mp4":
		return "mp4"
	case ".pdf":
		return "pdf"
	case ".doc", ".docx":
		return "doc"
	case ".xls", ".xlsx":
		return "xls"
	case ".ppt", ".pptx":
		return "ppt"
	case ".opus":
		return "opus"
	default:
		return "stream"
	}
}

// onMessage 处理收到的消息
func (f *FeishuChannel) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	f.mu.Lock()
	if !f.running {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	msg := event.Event.Message
	sender := event.Event.Sender

	// 调试日志：确认收到消息事件（记录所有消息，包括未@的）
	log.WithFields(log.Fields{
		"message_id": *msg.MessageId,
		"sender_type": func() string {
			if sender.SenderType != nil {
				return *sender.SenderType
			}
			return ""
		}(),
		"sender_id": func() string {
			if sender.SenderId != nil && sender.SenderId.OpenId != nil {
				return *sender.SenderId.OpenId
			}
			return ""
		}(),
		"chat_id": func() string {
			if msg.ChatId != nil {
				return *msg.ChatId
			}
			return ""
		}(),
		"chat_type": func() string {
			if msg.ChatType != nil {
				return *msg.ChatType
			}
			return ""
		}(),
		"msg_type": func() string {
			if msg.MessageType != nil {
				return *msg.MessageType
			}
			return ""
		}(),
	}).Info("Feishu: message event received")

	// 消息去重
	messageID := *msg.MessageId
	if f.isDuplicate(messageID) {
		log.WithField("message_id", messageID).Debug("Feishu: duplicate message, skipping")
		return nil
	}

	// 跳过机器人自己的消息
	if sender.SenderType != nil && *sender.SenderType == "bot" {
		log.WithField("message_id", messageID).Debug("Feishu: bot message, skipping")
		return nil
	}

	// 权限检查
	senderID := ""
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
		senderID = *sender.SenderId.OpenId
	}
	if !f.isAllowed(senderID) {
		log.WithField("sender", senderID).Warn("Feishu: access denied")
		return nil
	}

	chatID := ""
	if msg.ChatId != nil {
		chatID = *msg.ChatId
	}
	chatType := ""
	if msg.ChatType != nil {
		chatType = *msg.ChatType
	}
	msgType := ""
	if msg.MessageType != nil {
		msgType = *msg.MessageType
	}

	// 解析消息内容
	content := f.parseContent(msgType, msg)
	if content == "" {
		return nil
	}

	// 确定回复目标
	replyTo := chatID
	if chatType != "group" {
		replyTo = senderID
	}

	// 发布到消息总线
	f.msgBus.Inbound <- bus.InboundMessage{
		Channel:  "feishu",
		SenderID: senderID,
		ChatID:   replyTo,
		Content:  content,
		Metadata: map[string]string{
			"message_id": messageID,
			"chat_type":  chatType,
			"msg_type":   msgType,
		},
	}

	return nil
}

// onCardAction 处理卡片交互事件（按钮点击、表单提交）
func (f *FeishuChannel) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	if event.Event == nil || event.Event.Action == nil {
		log.Warn("Card action event is missing data")
		return &callback.CardActionTriggerResponse{}, nil
	}

	action := event.Event.Action

	// 解析用户操作数据
	var actionData map[string]any
	if action.Value != nil {
		actionData = action.Value
	} else if action.FormValue != nil {
		actionData = action.FormValue
	}

	// 获取聊天信息
	chatID := ""
	if event.Event.Context != nil {
		chatID = event.Event.Context.OpenChatID
	}

	// 获取用户 ID
	senderID := ""
	if event.Event.Operator != nil && event.Event.Operator.UserID != nil {
		senderID = *event.Event.Operator.UserID
	}

	// Card Builder 路径：检测 card_id
	if cardID, ok := actionData["card_id"].(string); ok {
		return f.handleCardBuilderAction(cardID, actionData, action, chatID, senderID)
	}

	log.WithField("action_value", actionData).Warn("Missing card_id in card action")
	return &callback.CardActionTriggerResponse{}, nil
}

// handleCardBuilderAction handles card actions from Card Builder MCP cards.
func (f *FeishuChannel) handleCardBuilderAction(cardID string, actionData map[string]any, action *callback.CallBackAction, chatID, senderID string) (*callback.CardActionTriggerResponse, error) {
	responseData := make(map[string]string)
	actionName := action.Tag

	if actionName == "form_submit" || len(action.FormValue) > 0 {
		// Collect all form field values
		for key, value := range action.FormValue {
			if key != "card_id" {
				switch v := value.(type) {
				case string:
					responseData[key] = v
				default:
					data, _ := json.Marshal(v)
					responseData[key] = string(data)
				}
			}
		}
		actionName = "form_submit"
	} else {
		// Button click: collect element name and custom data
		if name, ok := actionData["element_name"].(string); ok {
			responseData["element_name"] = name
		}
		for k, v := range actionData {
			if k != "card_id" && k != "element_name" {
				switch val := v.(type) {
				case string:
					responseData[k] = val
				default:
					data, _ := json.Marshal(val)
					responseData[k] = string(data)
				}
			}
		}
	}

	log.WithFields(log.Fields{
		"card_id":     cardID,
		"action_name": actionName,
		"chat_id":     chatID,
		"sender_id":   senderID,
	}).Info("Card builder action triggered")

	// Build user-readable summary
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[Card Action: %s] %s", cardID, actionName))
	for k, v := range responseData {
		sb.WriteString(fmt.Sprintf("\n- %s: %s", k, v))
	}

	f.msgBus.Inbound <- bus.InboundMessage{
		Channel:  f.Name(),
		SenderID: senderID,
		ChatID:   chatID,
		Content:  sb.String(),
		Time:     time.Now(),
		Metadata: map[string]string{
			"card_response": "true",
			"card_id":       cardID,
			"action_name":   actionName,
			"response_data": formatMapString(responseData),
		},
	}

	return &callback.CardActionTriggerResponse{
		Toast: &callback.Toast{
			Type:    "success",
			Content: "已收到，正在处理...",
		},
	}, nil
}

// formatMapString 将 map 格式化为字符串（用于 metadata）
func formatMapString(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	var parts []string
	for k, v := range m {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ", ")
}

// parseContent 解析消息内容
func (f *FeishuChannel) parseContent(msgType string, msg *larkim.EventMessage) string {
	if msg.Content == nil || *msg.Content == "" {
		return ""
	}

	var contentJSON map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &contentJSON); err != nil {
		return ""
	}

	switch msgType {
	case "text":
		if text, ok := contentJSON["text"].(string); ok {
			return text
		}
	case "post":
		return f.extractPostText(contentJSON)
	default:
		return fmt.Sprintf("[%s]", msgType)
	}
	return ""
}

// extractPostText 提取富文本内容
func (f *FeishuChannel) extractPostText(contentJSON map[string]any) string {
	// 尝试直接格式
	if result := f.extractFromLang(contentJSON); result != "" {
		return result
	}
	// 尝试本地化格式
	for _, lang := range []string{"zh_cn", "en_us", "ja_jp"} {
		if langContent, ok := contentJSON[lang].(map[string]any); ok {
			if result := f.extractFromLang(langContent); result != "" {
				return result
			}
		}
	}
	return ""
}

func (f *FeishuChannel) extractFromLang(langContent map[string]any) string {
	var parts []string
	if title, ok := langContent["title"].(string); ok && title != "" {
		parts = append(parts, title)
	}
	if blocks, ok := langContent["content"].([]any); ok {
		for _, block := range blocks {
			elements, ok := block.([]any)
			if !ok {
				continue
			}
			for _, elem := range elements {
				elemMap, ok := elem.(map[string]any)
				if !ok {
					continue
				}
				tag, _ := elemMap["tag"].(string)
				switch tag {
				case "text", "a":
					if text, ok := elemMap["text"].(string); ok {
						parts = append(parts, text)
					}
				case "at":
					if name, ok := elemMap["user_name"].(string); ok {
						parts = append(parts, "@"+name)
					}
				}
			}
		}
	}
	return strings.Join(parts, " ")
}

// mdImageRe 匹配 markdown 图片语法 ![alt](path)
var mdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)

// replaceLocalImages 扫描 markdown 中的本地图片引用，上传后替换为飞书 image_key
func (f *FeishuChannel) replaceLocalImages(content string) string {
	return mdImageRe.ReplaceAllStringFunc(content, func(match string) string {
		subs := mdImageRe.FindStringSubmatch(match)
		if len(subs) < 3 {
			return match
		}
		imgPath := subs[2]

		// 跳过 URL（http/https）和已经是 image_key 的（img_ 前缀）
		if strings.HasPrefix(imgPath, "http://") || strings.HasPrefix(imgPath, "https://") || strings.HasPrefix(imgPath, "img_") {
			return match
		}

		// 检查文件是否是图片类型
		ext := strings.ToLower(filepath.Ext(imgPath))
		if !imageExtensions[ext] {
			return match
		}

		// 检查文件是否存在
		if _, err := os.Stat(imgPath); err != nil {
			log.WithField("path", imgPath).Debug("Local image not found, keeping original markdown")
			return match
		}

		// 上传图片
		imageKey, err := f.uploadImage(imgPath)
		if err != nil {
			log.WithError(err).WithField("path", imgPath).Warn("Failed to upload local image, keeping original markdown")
			return match
		}

		log.WithFields(log.Fields{
			"path":      imgPath,
			"image_key": imageKey,
		}).Debug("Replaced local image with image_key")

		// 替换为飞书 image_key 格式
		return fmt.Sprintf("![%s](%s)", subs[1], imageKey)
	})
}

// buildCard 构建飞书消息卡片（JSON 2.0 结构）
func (f *FeishuChannel) buildCard(content string) map[string]any {
	return map[string]any{
		"schema": "2.0",
		"body": map[string]any{
			"elements": []map[string]any{
				{
					"tag":        "markdown",
					"content":    content,
					"text_align": "left",
					"text_size":  "normal",
				},
			},
		},
	}
}

// isDuplicate 检查消息是否重复
func (f *FeishuChannel) isDuplicate(messageID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, exists := f.processedIDs[messageID]; exists {
		return true
	}

	f.processedIDs[messageID] = struct{}{}
	f.processedOrder = append(f.processedOrder, messageID)

	// 清理过期缓存
	for len(f.processedOrder) > f.maxProcessed {
		oldest := f.processedOrder[0]
		f.processedOrder = f.processedOrder[1:]
		delete(f.processedIDs, oldest)
	}
	return false
}

// isAllowed 检查用户是否有权限
func (f *FeishuChannel) isAllowed(senderID string) bool {
	if len(f.config.AllowFrom) == 0 {
		return true
	}
	for _, allowed := range f.config.AllowFrom {
		if allowed == senderID {
			return true
		}
	}
	return false
}
