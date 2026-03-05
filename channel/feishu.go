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
	"xbot/tools"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkcontact "github.com/larksuite/oapi-sdk-go/v3/service/contact/v3"
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

	// OpenID -> 用户姓名缓存
	userNameCache map[string]string
	userNameMu    sync.RWMutex

	// 卡片 message_id -> card_id 映射（用于回调路由）
	cardMsgIDs sync.Map

	// CardBuilder for card callback handling
	cardBuilder *tools.CardBuilder
}

// NewFeishuChannel 创建飞书渠道
func NewFeishuChannel(cfg FeishuConfig, msgBus *bus.MessageBus) *FeishuChannel {
	return &FeishuChannel{
		config:        cfg,
		msgBus:        msgBus,
		processedIDs:  make(map[string]struct{}),
		maxProcessed:  1000,
		userNameCache: make(map[string]string),
	}
}

func (f *FeishuChannel) Name() string { return "feishu" }

// SetCardBuilder sets the CardBuilder for card callback handling.
func (f *FeishuChannel) SetCardBuilder(builder *tools.CardBuilder) {
	f.cardBuilder = builder
}

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

// getUserName 通过 Contact API 获取用户姓名，带内存缓存
func (f *FeishuChannel) getUserName(openID string) string {
	if openID == "" {
		return ""
	}

	f.userNameMu.RLock()
	name, ok := f.userNameCache[openID]
	f.userNameMu.RUnlock()
	if ok {
		return name
	}

	req := larkcontact.NewGetUserReqBuilder().
		UserId(openID).
		UserIdType("open_id").
		Build()
	resp, err := f.client.Contact.User.Get(context.Background(), req)
	if err != nil {
		log.WithError(err).WithField("open_id", openID).Warn("Feishu: failed to get user info")
		return ""
	}
	if !resp.Success() {
		log.WithFields(log.Fields{
			"open_id": openID,
			"code":    resp.Code,
			"msg":     resp.Msg,
		}).Warn("Feishu: get user info API error")
		return ""
	}

	resolved := ""
	if resp.Data != nil && resp.Data.User != nil && resp.Data.User.Name != nil {
		resolved = *resp.Data.User.Name
	}

	f.userNameMu.Lock()
	f.userNameCache[openID] = resolved
	f.userNameMu.Unlock()

	return resolved
}

// Send 发送消息到飞书，返回平台消息 ID
func (f *FeishuChannel) Send(msg bus.OutboundMessage) (string, error) {
	if f.client == nil {
		return "", fmt.Errorf("feishu client not initialized")
	}

	// 表情回复：metadata 中带 add_reaction 时，对指定消息添加表情后返回
	if msg.Metadata != nil && msg.Metadata["add_reaction"] != "" {
		targetMsgID := msg.Metadata["reaction_message_id"]
		emojiType := msg.Metadata["add_reaction"]
		if targetMsgID != "" {
			if err := f.addReaction(targetMsgID, emojiType); err != nil {
				log.WithError(err).WithField("message_id", targetMsgID).Warn("Feishu: failed to add reaction")
			}
		}
		return "", nil
	}

	if msg.Content == "" {
		return "", nil
	}

	// card builder 生成的完整卡片 JSON，走正常 patch/reply/send 流程
	// 格式: __FEISHU_CARD__:card_id:{"schema":...}
	if strings.HasPrefix(msg.Content, "__FEISHU_CARD__:") {
		payload := strings.TrimPrefix(msg.Content, "__FEISHU_CARD__:")

		cardID := ""
		cardJSON := payload
		if idx := strings.Index(payload, ":{"); idx >= 0 {
			cardID = payload[:idx]
			cardJSON = payload[idx+1:]
		}

		updateMsgID := ""
		replyTo := ""
		if msg.Metadata != nil {
			updateMsgID = msg.Metadata["update_message_id"]
			replyTo = msg.Metadata["message_id"]
		}

		var msgID string
		if updateMsgID != "" {
			if err := f.patchMessage(updateMsgID, []byte(cardJSON)); err != nil {
				log.WithError(err).WithField("message_id", updateMsgID).Warn("Feishu: card patch failed, falling back to create")
			} else {
				msgID = updateMsgID
			}
		}
		if msgID == "" {
			var err error
			if replyTo != "" {
				msgID, err = f.sendReplyMessage(msg.ChatID, replyTo, []byte(cardJSON))
			} else {
				msgID, err = f.sendNormalMessage(msg.ChatID, []byte(cardJSON))
			}
			if err != nil {
				return "", err
			}
		}

		if cardID != "" && msgID != "" {
			f.cardMsgIDs.Store(msgID, cardID)
		}
		return msgID, nil
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
		return "", nil
	}

	if len(content) != originalLen {
		log.WithFields(log.Fields{
			"original_len": originalLen,
			"final_len":    len(content),
		}).Debug("Feishu: content length changed after processing")
	}

	// 飞书卡片对 markdown 表格数量有上限（约 3 个），超出会被 API 拒绝
	content = limitMarkdownTables(content, 3)

	// 构建消息卡片
	card := f.buildCard(content)
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("marshal card: %w", err)
	}

	// 检查是否需要更新已有消息（Patch 模式）
	updateMsgID := ""
	if msg.Metadata != nil {
		updateMsgID = msg.Metadata["update_message_id"]
	}
	if updateMsgID != "" {
		if err := f.patchMessage(updateMsgID, cardJSON); err != nil {
			log.WithError(err).WithField("message_id", updateMsgID).Warn("Feishu: patch failed, falling back to create")
		} else {
			return updateMsgID, nil
		}
	}

	// 检查是否需要回复消息（reply 模式）
	messageID := ""
	if msg.Metadata != nil {
		messageID = msg.Metadata["message_id"]
	}

	if messageID != "" {
		return f.sendReplyMessage(msg.ChatID, messageID, cardJSON)
	}

	return f.sendNormalMessage(msg.ChatID, cardJSON)
}

// sendReplyMessage 发送回复消息，返回新消息的 message_id
func (f *FeishuChannel) sendReplyMessage(chatID, parentID string, cardJSON []byte) (string, error) {
	req := larkim.NewReplyMessageReqBuilder().
		MessageId(parentID).
		Body(larkim.NewReplyMessageReqBodyBuilder().
			MsgType("interactive").
			Content(string(cardJSON)).
			Build()).
		Build()

	log.WithFields(log.Fields{
		"chat_id":   chatID,
		"parent_id": parentID,
		"card_len":  len(cardJSON),
	}).Debug("Feishu: sending reply message")

	resp, err := f.client.Im.Message.Reply(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("send feishu reply message: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	msgID := ""
	if resp.Data != nil && resp.Data.MessageId != nil {
		msgID = *resp.Data.MessageId
	}

	log.WithFields(log.Fields{
		"chat_id":    chatID,
		"parent_id":  parentID,
		"message_id": msgID,
	}).Debug("Feishu reply message sent")
	return msgID, nil
}

// sendNormalMessage 发送普通消息，返回新消息的 message_id
func (f *FeishuChannel) sendNormalMessage(chatID string, cardJSON []byte) (string, error) {
	receiveIDType := "chat_id"
	if !strings.HasPrefix(chatID, "oc_") {
		receiveIDType = "open_id"
	}

	log.WithFields(log.Fields{
		"chat_id":  chatID,
		"card_len": len(cardJSON),
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
		return "", fmt.Errorf("send feishu message: %w", err)
	}
	if !resp.Success() {
		return "", fmt.Errorf("feishu API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	msgID := ""
	if resp.Data != nil && resp.Data.MessageId != nil {
		msgID = *resp.Data.MessageId
	}

	log.WithFields(log.Fields{
		"chat_id":    chatID,
		"message_id": msgID,
	}).Debug("Feishu message sent")
	return msgID, nil
}

// patchMessage 更新已有的卡片消息（原地替换内容，避免刷屏）
func (f *FeishuChannel) patchMessage(messageID string, cardJSON []byte) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(larkim.NewPatchMessageReqBodyBuilder().
			Content(string(cardJSON)).
			Build()).
		Build()

	resp, err := f.client.Im.Message.Patch(context.Background(), req)
	if err != nil {
		return fmt.Errorf("patch feishu message: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("feishu patch API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	log.WithField("message_id", messageID).Debug("Feishu message patched")
	return nil
}

// addReaction 对指定消息添加表情回复
func (f *FeishuChannel) addReaction(messageID, emojiType string) error {
	req := larkim.NewCreateMessageReactionReqBuilder().
		MessageId(messageID).
		Body(larkim.NewCreateMessageReactionReqBodyBuilder().
			ReactionType(larkim.NewEmojiBuilder().EmojiType(emojiType).Build()).
			Build()).
		Build()

	resp, err := f.client.Im.MessageReaction.Create(context.Background(), req)
	if err != nil {
		return fmt.Errorf("add reaction: %w", err)
	}
	if !resp.Success() {
		return fmt.Errorf("add reaction API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}

	log.WithFields(log.Fields{
		"message_id": messageID,
		"emoji":      emojiType,
	}).Debug("Feishu reaction added")
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
	content := f.parseContent(eventMessageAdapter{msg})
	if content == "" {
		return nil
	}

	// 剥离 @mention 占位符（群聊中 @bot 后内容带 @_user_N 前缀）
	if msg.Mentions != nil {
		for _, m := range msg.Mentions {
			if m.Key != nil {
				content = strings.ReplaceAll(content, *m.Key, "")
			}
		}
		content = strings.TrimSpace(content)
		if content == "" {
			return nil
		}
	}

	// 确定回复目标
	replyTo := chatID
	if chatType != "group" {
		replyTo = senderID
	}

	// 检查是否有活跃的卡片会话，用户发送文本消息时跳过卡片
	if msgType == "text" && f.cardBuilder != nil {
		if activeCardID, ok := f.cardBuilder.GetActiveCardID(replyTo); ok {
			// 查找卡片消息 ID 并 patch 为"已跳过"状态
			f.cardMsgIDs.Range(func(key, value any) bool {
				if value.(string) == activeCardID {
					messageID := key.(string)
					skippedCard := f.buildCard("⚠️ 用户选择直接回复，卡片已关闭")
					if cardJSON, err := json.Marshal(skippedCard); err == nil {
						if err := f.patchMessage(messageID, cardJSON); err != nil {
							log.WithError(err).WithField("message_id", messageID).Warn("Feishu: failed to patch skipped card")
						}
					}
					return false // stop iteration
				}
				return true
			})
			// 清除活跃卡片映射
			f.cardBuilder.ClearActiveCard(replyTo)
			log.WithFields(log.Fields{
				"chat_id": replyTo,
				"card_id": activeCardID,
			}).Info("Card skipped due to text message")
		}
	}

	// 解析发送者姓名
	senderName := f.getUserName(senderID)

	var refMsg = ""
	refMsgEv := f.getHistoryMsgById(event.Event)
	if refMsgEv != nil {
		log.WithFields(log.Fields{
			"message_id":     messageID,
			"ref_message_id": *refMsgEv.MessageId,
		}).Info("Found reference message for incoming message")
		refMsg = f.parseContent(messageAdapter{refMsgEv})
		refSenderID := ""
		if refMsgEv.Sender != nil && refMsgEv.Sender.Id != nil {
			refSenderID = *refMsgEv.Sender.Id
		}
		refSenderName := f.getUserName(refSenderID)
		refMsg = fmt.Sprintf("> 引用自 %s (%s) 的消息：%s", refSenderName, refSenderID, refMsg)
	} else if msg.RootId != nil {
		refMsg = "[存在引用的消息但是无法找到内容，可能是因为消息过旧不在缓存中]"
	}

	// 发布到消息总线
	f.msgBus.Inbound <- bus.InboundMessage{
		Channel:    "feishu",
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     replyTo,
		Content:    fmt.Sprintf("%s\n%s", refMsg, content),
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

	// 查找 card_id：优先从 actionData（按钮 value），否则通过 message_id 反查
	messageID := ""
	if event.Event.Context != nil {
		messageID = event.Event.Context.OpenMessageID
	}
	cardID := ""
	if id, ok := actionData["card_id"].(string); ok {
		cardID = id
	} else if messageID != "" {
		if id, ok := f.cardMsgIDs.Load(messageID); ok {
			cardID = id.(string)
		}
	}
	if cardID == "" {
		log.WithField("action_value", actionData).Warn("Missing card_id in card action")
		return &callback.CardActionTriggerResponse{}, nil
	}

	return f.handleCardBuilderAction(cardID, actionData, action, chatID, senderID, messageID)
}

// handleCardBuilderAction handles card actions from Card Builder MCP cards.
// Button clicks, form submissions, and standalone select interactions are forwarded to the agent.
func (f *FeishuChannel) handleCardBuilderAction(cardID string, actionData map[string]any, action *callback.CallBackAction, chatID, senderID, messageID string) (*callback.CardActionTriggerResponse, error) {
	responseData := make(map[string]string)
	actionName := action.Tag

	// Check if this interaction type is expected for this card
	expectedInteractions := f.getExpectedInteractions(cardID)

	switch {
	case actionName == "form_submit" || len(action.FormValue) > 0:
		for key, value := range action.FormValue {
			if key == "card_id" {
				continue
			}
			switch v := value.(type) {
			case string:
				responseData[key] = v
			default:
				data, _ := json.Marshal(v)
				responseData[key] = string(data)
			}
		}
		actionName = "form_submit"

	case actionName == "button":
		if action.Name != "" {
			responseData["name"] = action.Name
		}
		for k, v := range actionData {
			if k == "card_id" {
				continue
			}
			switch val := v.(type) {
			case string:
				responseData[k] = val
			default:
				data, _ := json.Marshal(val)
				responseData[k] = string(data)
			}
		}

	case actionName == "select_static", actionName == "multi_select_static":
		// Only handle if this card expects standalone select interactions
		if !f.isExpectedInteraction(expectedInteractions, actionName) {
			log.WithFields(log.Fields{
				"card_id": cardID,
				"tag":     actionName,
				"name":    action.Name,
			}).Debug("Ignoring select interaction (not expected for this card)")
			return &callback.CardActionTriggerResponse{}, nil
		}

		// Extract selection from action.Value
		elementName := action.Name
		if elementName != "" {
			responseData["element_name"] = elementName
		}

		// Get selected value(s)
		if action.Value != nil {
			if selected, ok := action.Value["selected_option"]; ok {
				responseData["selected"] = fmt.Sprintf("%v", selected)
			} else if selected, ok := action.Value["selected_options"]; ok {
				responseData["selected"] = fmt.Sprintf("%v", selected)
			}
		}

		// Get available options from card metadata for context
		if f.cardBuilder != nil && elementName != "" {
			if opts := f.cardBuilder.GetElementOptions(cardID, elementName); opts != "" {
				responseData["available_options"] = opts
			}
		}

		// Clear active card since user interacted with it
		if f.cardBuilder != nil && chatID != "" {
			f.cardBuilder.ClearActiveCard(chatID)
		}

	case actionName == "overflow", actionName == "checker", actionName == "select_img":
		// Handle other interactive elements if expected
		if !f.isExpectedInteraction(expectedInteractions, actionName) {
			log.WithFields(log.Fields{
				"card_id": cardID,
				"tag":     actionName,
				"name":    action.Name,
			}).Debug("Ignoring interactive element (not expected for this card)")
			return &callback.CardActionTriggerResponse{}, nil
		}

		elementName := action.Name
		if elementName != "" {
			responseData["element_name"] = elementName
		}
		for k, v := range actionData {
			if k == "card_id" {
				continue
			}
			switch val := v.(type) {
			case string:
				responseData[k] = val
			default:
				data, _ := json.Marshal(val)
				responseData[k] = string(data)
			}
		}

		// Clear active card since user interacted with it
		if f.cardBuilder != nil && chatID != "" {
			f.cardBuilder.ClearActiveCard(chatID)
		}

	default:
		// Unknown interaction type
		log.WithFields(log.Fields{
			"card_id": cardID,
			"tag":     actionName,
			"name":    action.Name,
		}).Debug("Ignoring unknown card interaction type")
		return &callback.CardActionTriggerResponse{}, nil
	}

	log.WithFields(log.Fields{
		"card_id":     cardID,
		"action_name": actionName,
		"chat_id":     chatID,
		"sender_id":   senderID,
		"data":        responseData,
	}).Info("Card builder action triggered")

	// 表单提交后，patch 卡片为"已提交"状态（防止重复提交）
	if actionName == "form_submit" && messageID != "" {
		submittedCard := f.buildCard("✅ 已提交，正在处理...")
		if cardJSON, err := json.Marshal(submittedCard); err == nil {
			if err := f.patchMessage(messageID, cardJSON); err != nil {
				log.WithError(err).WithField("message_id", messageID).Warn("Feishu: failed to disable form after submit")
			}
		}
		// Clear active card since user interacted with it
		if f.cardBuilder != nil && chatID != "" {
			f.cardBuilder.ClearActiveCard(chatID)
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "[Card Action: %s] %s", cardID, actionName)
	for k, v := range responseData {
		fmt.Fprintf(&sb, "\n- %s: %s", k, v)
	}

	f.msgBus.Inbound <- bus.InboundMessage{
		Channel:    f.Name(),
		SenderID:   senderID,
		SenderName: f.getUserName(senderID),
		ChatID:     chatID,
		Content:    sb.String(),
		Time:       time.Now(),
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

// getExpectedInteractions returns the expected interaction types for a card.
func (f *FeishuChannel) getExpectedInteractions(cardID string) []string {
	if f.cardBuilder == nil {
		return nil
	}
	return f.cardBuilder.GetExpectedInteractions(cardID)
}

// isExpectedInteraction checks if an interaction type is expected for a card.
func (f *FeishuChannel) isExpectedInteraction(expected []string, actionName string) bool {
	if len(expected) == 0 {
		// Default behavior: only handle button and form_submit
		return actionName == "button" || actionName == "form_submit"
	}
	for _, e := range expected {
		if e == actionName {
			return true
		}
	}
	return false
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

// feishuMsg is a common interface for extracting message fields from both
// larkim.EventMessage (WebSocket event) and larkim.Message (API response),
// which have different field names for the same logical data.
type feishuMsg interface {
	GetMessageId() *string
	GetMsgType() string
	GetContent() *string
}

// eventMessageAdapter wraps *larkim.EventMessage to implement feishuMsg.
type eventMessageAdapter struct{ m *larkim.EventMessage }

func (a eventMessageAdapter) GetMessageId() *string { return a.m.MessageId }
func (a eventMessageAdapter) GetMsgType() string {
	if a.m.MessageType != nil {
		return *a.m.MessageType
	}
	return ""
}
func (a eventMessageAdapter) GetContent() *string { return a.m.Content }

// messageAdapter wraps *larkim.Message to implement feishuMsg.
type messageAdapter struct{ m *larkim.Message }

func (a messageAdapter) GetMessageId() *string { return a.m.MessageId }
func (a messageAdapter) GetMsgType() string {
	if a.m.MsgType != nil {
		return *a.m.MsgType
	}
	return ""
}
func (a messageAdapter) GetContent() *string {
	if a.m.Body != nil {
		return a.m.Body.Content
	}
	return nil
}

// parseContent 解析消息内容 (接受 feishuMsg 接口，兼容 EventMessage 和 Message)
func (f *FeishuChannel) parseContent(msg feishuMsg) string {
	content := msg.GetContent()
	if content == nil || *content == "" {
		return ""
	}
	msgType := msg.GetMsgType()

	var contentJSON map[string]any
	if err := json.Unmarshal([]byte(*content), &contentJSON); err != nil {
		return ""
	}

	switch msgType {
	case "text":
		if text, ok := contentJSON["text"].(string); ok {
			return text
		}
	case "post":
		return f.extractPostText(contentJSON)
	case "file":
		fileKey, _ := contentJSON["file_key"].(string)
		fileName, _ := contentJSON["file_name"].(string)
		messageID := ""
		if mid := msg.GetMessageId(); mid != nil {
			messageID = *mid
		}
		return fmt.Sprintf(`<file name="%s" file_key="%s" message_id="%s" />`, fileName, fileKey, messageID)
	case "image":
		imageKey, _ := contentJSON["image_key"].(string)
		messageID := ""
		if mid := msg.GetMessageId(); mid != nil {
			messageID = *mid
		}
		return fmt.Sprintf(`<image image_key="%s" message_id="%s" />`, imageKey, messageID)
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

// buildCard 构建飞书消息卡片（JSON 2.0 结构，启用 update_multi 以支持 Patch 更新）
func (f *FeishuChannel) buildCard(content string) map[string]any {
	return map[string]any{
		"schema": "2.0",
		"config": map[string]any{
			"update_multi": true,
		},
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

func (f *FeishuChannel) getHistoryMsgById(currentMsgEV *larkim.P2MessageReceiveV1Data) *larkim.Message {
	currentMsg := currentMsgEV.Message
	if currentMsg == nil {
		return nil
	}

	// 当前消息没有 ParentId，无需查找历史引用
	if currentMsg.ParentId == nil || *currentMsg.ParentId == "" {
		return nil
	}
	req := larkim.NewGetMessageReqBuilder().
		MessageId(*currentMsg.ParentId).
		UserIdType(`open_id`).
		Build()

	resp, err := f.client.Im.Message.Get(context.Background(), req)
	if err != nil {
		log.WithError(err).WithField("parent_id", *currentMsg.ParentId).Warn("Failed to get parent message")
		return nil
	}
	if !resp.Success() {
		log.WithFields(log.Fields{
			"parent_id": *currentMsg.ParentId,
			"code":      resp.Code,
			"msg":       resp.Msg,
		}).Warn("Failed to get parent message from API")
		return nil
	}
	if resp.Data == nil || len(resp.Data.Items) == 0 {
		log.WithField("parent_id", *currentMsg.ParentId).Warn("Parent message not found in response")
		return nil
	}
	return resp.Data.Items[0]
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

// mdTableSepRe 匹配 markdown 表格的分隔行（如 |---|---|）
var mdTableSepRe = regexp.MustCompile(`^\|[\s:]*-+[\s:]*(\|[\s:]*-+[\s:]*)+\|?\s*$`)

// limitMarkdownTables 限制 markdown 内容中的表格数量。
// 超出 maxTables 的表格会被转成代码块（保留可读性但不触发飞书 table 渲染）。
func limitMarkdownTables(content string, maxTables int) string {
	lines := strings.Split(content, "\n")
	tableCount := 0
	inTable := false
	inExcessTable := false
	var result []string

	for _, line := range lines {
		isTableLine := strings.HasPrefix(strings.TrimSpace(line), "|") && strings.Contains(line, "|")
		isSepLine := isTableLine && mdTableSepRe.MatchString(strings.TrimSpace(line))

		if !inTable && isSepLine {
			// 进入新表格：分隔行之前的 header 行也属于这个表格
			tableCount++
			inTable = true
			inExcessTable = tableCount > maxTables

			if inExcessTable {
				// 把已写入的 header 行（上一行）也转成代码块内容
				if len(result) > 0 {
					prev := result[len(result)-1]
					if strings.HasPrefix(strings.TrimSpace(prev), "|") {
						result[len(result)-1] = "```"
						result = append(result, prev)
					} else {
						result = append(result, "```")
					}
				} else {
					result = append(result, "```")
				}
				result = append(result, line)
				continue
			}
		}

		if inTable && !isTableLine {
			// 离开表格
			if inExcessTable {
				result = append(result, "```")
			}
			inTable = false
			inExcessTable = false
		}

		result = append(result, line)
	}

	// 文件末尾仍在超限表格中，关闭代码块
	if inExcessTable {
		result = append(result, "```")
	}

	return strings.Join(result, "\n")
}
