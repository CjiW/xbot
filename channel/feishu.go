package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	config    FeishuConfig
	msgBus    *bus.MessageBus
	client    *lark.Client
	wsClient  *larkws.Client
	running   bool
	mu        sync.Mutex
	botOpenID string
	botName   string // 机器人名称，用于引用消息中标识自己

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

	// 初始化机器人自身 open_id（用于群聊 @ 识别）
	if err := f.refreshBotOpenID(context.Background()); err != nil {
		log.WithError(err).Warn("Feishu: failed to initialize bot open_id from bot/v3/info")
	}

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
// 对于 bot 类型的 sender（以 "cli_" 开头），返回机器人名称
func (f *FeishuChannel) getUserName(openID string) string {
	if openID == "" {
		return ""
	}

	// Bot open_id 通常以 "cli_" 开头，返回机器人名称
	if strings.HasPrefix(openID, "cli_") {
		f.mu.Lock()
		botName := f.botName
		f.mu.Unlock()
		if botName != "" {
			return botName
		}
		return "Bot"
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
		return fmt.Errorf("feishu patch API error: code=%d, msg=%s detail: %s", resp.Code, resp.Msg, resp.ErrorResp())
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
	// 在渠道收到消息的第一时间生成 requestID
	requestID := log.NewRequestID()
	l := log.WithField("request_id", requestID)

	f.mu.Lock()
	if !f.running {
		f.mu.Unlock()
		return nil
	}
	f.mu.Unlock()

	msg := event.Event.Message
	sender := event.Event.Sender

	// 调试日志：确认收到消息事件（记录所有消息，包括未@的）
	l.WithFields(log.Fields{
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
		l.WithField("message_id", messageID).Debug("Feishu: duplicate message, skipping")
		return nil
	}

	// 跳过机器人自己的消息
	if sender.SenderType != nil && *sender.SenderType == "bot" {
		l.WithField("message_id", messageID).Debug("Feishu: bot message, skipping")
		return nil
	}

	// 权限检查
	senderID := ""
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
		senderID = *sender.SenderId.OpenId
	}
	if !f.isAllowed(senderID) {
		l.WithField("sender", senderID).Warn("Feishu: access denied")
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

	// 群聊前置拦截：
	// 1) 未 @ 机器人 且非 @所有人 -> 直接拦截
	// 2) 仅 @所有人 -> 放行给 Agent 决定是否回复（并标记 optional）
	mentionScope := "direct"
	if chatType == "group" {
		shouldHandle, atAllOnly, reason := f.shouldHandleGroupMessage(msg)
		if !shouldHandle {
			l.WithFields(log.Fields{
				"message_id": messageID,
				"chat_id":    chatID,
				"reason":     reason,
			}).Debug("Feishu: group message intercepted before agent")
			return nil
		}
		if atAllOnly {
			mentionScope = "at_all_optional"
		}
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

	if mentionScope == "at_all_optional" {
		content = "[群聊 @所有人 消息：按相关性决定是否需要回复；不相关可不回复]\n" + content
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
							l.WithError(err).WithField("message_id", messageID).Warn("Feishu: failed to patch skipped card")
						}
					}
					return false // stop iteration
				}
				return true
			})
			// 清除活跃卡片映射
			f.cardBuilder.ClearActiveCard(replyTo)
			l.WithFields(log.Fields{
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
		l.WithFields(log.Fields{
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

	// 构建消息内容：refMsg 非空时添加引用前缀
	var finalContent string
	if refMsg != "" {
		finalContent = fmt.Sprintf("%s\n%s", refMsg, content)
	} else {
		finalContent = content
	}

	// 发布到消息总线
	msgTime := time.Now()
	if msg.CreateTime != nil {
		if ms, err := strconv.ParseInt(*msg.CreateTime, 10, 64); err == nil {
			msgTime = time.UnixMilli(ms)
		} else {
			l.WithError(err).WithField("create_time", *msg.CreateTime).Warn("Feishu: failed to parse message CreateTime, using current time")
		}
	}
	metadata := map[string]string{
		"message_id": messageID,
		"chat_type":  chatType,
		"msg_type":   msgType,
	}
	if mentionScope == "at_all_optional" {
		metadata[bus.MetadataReplyPolicy] = bus.ReplyPolicyOptional
	}

	f.msgBus.Inbound <- bus.InboundMessage{
		Channel:    "feishu",
		SenderID:   senderID,
		SenderName: senderName,
		ChatID:     replyTo,
		ChatType:   chatType,
		Content:    finalContent,
		Time:       msgTime,
		Metadata:   metadata,
		RequestID:  requestID,
	}

	return nil
}

type feishuBotInfoResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Bot  struct {
		OpenID string `json:"open_id"`
		Name   string `json:"app_name"` // 机器人名称
	} `json:"bot"`
}

// refreshBotOpenID calls Bot API (/open-apis/bot/v3/info) to get bot open_id.
func (f *FeishuChannel) refreshBotOpenID(ctx context.Context) error {
	if f.client == nil {
		return fmt.Errorf("feishu client not initialized")
	}

	rawResp, err := f.client.Get(ctx, "/open-apis/bot/v3/info", nil, larkcore.AccessTokenTypeTenant)
	if err != nil {
		return fmt.Errorf("request bot info: %w", err)
	}

	var resp feishuBotInfoResp
	if err := json.Unmarshal(rawResp.RawBody, &resp); err != nil {
		return fmt.Errorf("unmarshal bot info response: %w", err)
	}
	if resp.Code != 0 {
		return fmt.Errorf("bot info API error: code=%d, msg=%s", resp.Code, resp.Msg)
	}
	if strings.TrimSpace(resp.Bot.OpenID) == "" {
		return fmt.Errorf("bot info API returned empty bot.open_id")
	}

	f.mu.Lock()
	f.botOpenID = strings.TrimSpace(resp.Bot.OpenID)
	f.botName = strings.TrimSpace(resp.Bot.Name)
	f.mu.Unlock()

	log.WithFields(log.Fields{
		"bot_open_id": resp.Bot.OpenID,
		"bot_name":    resp.Bot.Name,
	}).Info("Feishu: bot info initialized from bot/v3/info")
	return nil
}

// shouldHandleGroupMessage determines whether a group message should be forwarded to Agent.
// Rules:
// - direct @bot: always forward
// - only @all (without direct @bot): forward as optional
// - otherwise: intercept
func (f *FeishuChannel) shouldHandleGroupMessage(msg *larkim.EventMessage) (shouldHandle bool, atAllOnly bool, reason string) {
	if msg == nil || len(msg.Mentions) == 0 {
		return false, false, "no_mentions"
	}

	f.mu.Lock()
	botOpenID := f.botOpenID
	f.mu.Unlock()

	hasAtAll := false
	hasDirectBot := false

	for _, mention := range msg.Mentions {
		if mention == nil {
			continue
		}
		if isAtAllMention(mention) {
			hasAtAll = true
			continue
		}
		if botOpenID != "" && isBotMention(mention, botOpenID) {
			hasDirectBot = true
		}
	}

	if hasDirectBot {
		return true, false, "direct_bot_mention"
	}
	if hasAtAll {
		return true, true, "at_all_optional"
	}

	if botOpenID == "" {
		return false, false, "bot_open_id_unknown"
	}
	return false, false, "mentioned_others"
}

func isBotMention(mention *larkim.MentionEvent, botOpenID string) bool {
	if mention == nil || mention.Id == nil || botOpenID == "" {
		return false
	}
	if mention.Id.OpenId != nil && *mention.Id.OpenId == botOpenID {
		return true
	}
	if mention.Id.UserId != nil && *mention.Id.UserId == botOpenID {
		return true
	}
	if mention.Id.UnionId != nil && *mention.Id.UnionId == botOpenID {
		return true
	}
	return false
}

func isAtAllMention(mention *larkim.MentionEvent) bool {
	if mention == nil {
		return false
	}
	if mention.Key != nil {
		key := strings.TrimSpace(strings.ToLower(*mention.Key))
		if key == "@all" || key == "@_all" {
			return true
		}
	}
	if mention.Name != nil {
		name := strings.TrimSpace(strings.ToLower(*mention.Name))
		switch name {
		case "all", "everyone", "所有人", "全体成员":
			return true
		}
	}
	if mention.Id != nil {
		if mention.Id.OpenId != nil && strings.EqualFold(strings.TrimSpace(*mention.Id.OpenId), "all") {
			return true
		}
		if mention.Id.UserId != nil && strings.EqualFold(strings.TrimSpace(*mention.Id.UserId), "all") {
			return true
		}
		if mention.Id.UnionId != nil && strings.EqualFold(strings.TrimSpace(*mention.Id.UnionId), "all") {
			return true
		}
	}
	return false
}

// onCardAction 处理卡片交互事件（按钮点击、表单提交）
func (f *FeishuChannel) onCardAction(ctx context.Context, event *callback.CardActionTriggerEvent) (*callback.CardActionTriggerResponse, error) {
	// 在渠道收到卡片交互的第一时间生成 requestID
	requestID := log.NewRequestID()

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

	return f.handleCardBuilderAction(cardID, actionData, action, chatID, senderID, messageID, requestID)
}

// handleCardBuilderAction handles card actions from Card Builder MCP cards.
// Button clicks, form submissions, and standalone select interactions are forwarded to the agent.
func (f *FeishuChannel) handleCardBuilderAction(cardID string, actionData map[string]any, action *callback.CallBackAction, chatID, senderID, messageID, requestID string) (*callback.CardActionTriggerResponse, error) {
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
		RequestID:  requestID,
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
	messageID := ""
	if mid := msg.GetMessageId(); mid != nil {
		messageID = *mid
	}

	switch msgType {
	case "text":
		if text, ok := contentJSON["text"].(string); ok {
			return text
		}
	case "post":
		return f.extractPostText(contentJSON, messageID)
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
	case "folder":
		// 文件夹
		fileKey, _ := contentJSON["file_key"].(string)
		fileName, _ := contentJSON["file_name"].(string)
		return fmt.Sprintf(`<folder name="%s" file_key="%s" />`, fileName, fileKey)
	case "audio":
		// 音频
		fileKey, _ := contentJSON["file_key"].(string)
		duration, _ := contentJSON["duration"].(float64)
		messageID := ""
		if mid := msg.GetMessageId(); mid != nil {
			messageID = *mid
		}
		return fmt.Sprintf(`<audio file_key="%s" duration="%.0f" message_id="%s" />`, fileKey, duration, messageID)
	case "media":
		// 视频（带封面）
		fileKey, _ := contentJSON["file_key"].(string)
		imageKey, _ := contentJSON["image_key"].(string)
		fileName, _ := contentJSON["file_name"].(string)
		duration, _ := contentJSON["duration"].(float64)
		messageID := ""
		if mid := msg.GetMessageId(); mid != nil {
			messageID = *mid
		}
		return fmt.Sprintf(`<video name="%s" file_key="%s" image_key="%s" duration="%.0f" message_id="%s" />`, fileName, fileKey, imageKey, duration, messageID)
	case "sticker":
		// 表情包
		fileKey, _ := contentJSON["file_key"].(string)
		return fmt.Sprintf(`<sticker file_key="%s" />`, fileKey)
	case "interactive":
		// 卡片消息 - 解析卡片元素
		return f.extractInteractiveContent(contentJSON)
	case "share_chat":
		// 群名片
		chatID, _ := contentJSON["chat_id"].(string)
		return fmt.Sprintf(`[分享群聊: %s]`, chatID)
	case "share_user":
		// 个人名片
		userID, _ := contentJSON["user_id"].(string)
		return fmt.Sprintf(`[分享用户: %s]`, userID)
	// TODO: 其他不常用类型
	// case "hongbao": return "[红包]"
	// case "system": return "[系统消息]"
	// case "location": return "[位置]"
	// case "vote": return "[投票]"
	// case "task": return "[任务]"
	// case "share_calendar_event", "calendar", "general_calendar": return "[日程]"
	// case "video_chat": return "[视频通话]"
	// case "merge_forward": return "[合并转发]"
	default:
		return fmt.Sprintf("[%s]", msgType)
	}
	return ""
}

// extractPostText 提取富文本内容
func (f *FeishuChannel) extractPostText(contentJSON map[string]any, messageId string) string {
	// 尝试直接格式
	if result := f.extractFromLang(contentJSON, messageId); result != "" {
		return result
	}
	// 尝试本地化格式
	for _, lang := range []string{"zh_cn", "en_us", "ja_jp"} {
		if langContent, ok := contentJSON[lang].(map[string]any); ok {
			if result := f.extractFromLang(langContent, messageId); result != "" {
				return result
			}
		}
	}
	return ""
}

func (f *FeishuChannel) extractFromLang(langContent map[string]any, messageId string) string {
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
				case "img":
					if imageKey, ok := elemMap["image_key"].(string); ok {
						parts = append(parts, fmt.Sprintf("<image image_key=\"%s\" message_id=\"%s\" />", imageKey, messageId))
					}
				case "code_block":
					// 代码块 - 重点支持
					language, _ := elemMap["language"].(string)
					code, _ := elemMap["text"].(string)
					if code != "" {
						parts = append(parts, fmt.Sprintf("```%s\n%s\n```", language, code))
					}
				case "emotion":
					// 表情
					if emojiType, ok := elemMap["emoji_type"].(string); ok {
						parts = append(parts, fmt.Sprintf("[表情: %s]", emojiType))
					}
				case "hr":
					// 分割线
					parts = append(parts, "---")
				case "media":
					// 视频
					fileKey, _ := elemMap["file_key"].(string)
					imageKey, _ := elemMap["image_key"].(string)
					if fileKey != "" {
						parts = append(parts, fmt.Sprintf("<video file_key=\"%s\" image_key=\"%s\" message_id=\"%s\" />", fileKey, imageKey, messageId))
					}
				case "folder":
					// 文件夹
					fileKey, _ := elemMap["file_key"].(string)
					fileName, _ := elemMap["file_name"].(string)
					if fileKey != "" {
						parts = append(parts, fmt.Sprintf("<folder name=\"%s\" file_key=\"%s\" />", fileName, fileKey))
					}
				// TODO: 其他不常用类型
				// case "button": parts = append(parts, "[按钮]")
				// case "note": parts = append(parts, "[备注]")
				// case "select_static": parts = append(parts, "[下拉选择]")
				// case "date_picker": parts = append(parts, "[日期选择]")
				// case "overflow": parts = append(parts, "[更多选项]")
				// case "video_chat": parts = append(parts, "[视频通话]")
				// case "location": parts = append(parts, "[位置]")
				default:
					// 其他未处理的元素类型，记录但不阻塞
					if tag != "" {
						parts = append(parts, fmt.Sprintf("[%s]", tag))
					}
				}
			}
		}
	}
	return strings.Join(parts, " ")
}

// extractInteractiveContent 解析卡片消息内容（接收到的卡片结构与发送时不一致，仅支持部分元素）
func (f *FeishuChannel) extractInteractiveContent(contentJSON map[string]any) string {
	var parts []string

	// 解析标题
	if title, ok := contentJSON["title"].(string); ok && title != "" {
		parts = append(parts, "[卡片: "+title+"]")
	}

	// 解析元素
	if elements, ok := contentJSON["elements"].([]any); ok {
		for _, block := range elements {
			blockElems, ok := block.([]any)
			if !ok {
				continue
			}
			for _, elem := range blockElems {
				elemMap, ok := elem.(map[string]any)
				if !ok {
					continue
				}
				tag, _ := elemMap["tag"].(string)
				switch tag {
				case "text":
					if text, ok := elemMap["text"].(string); ok {
						parts = append(parts, text)
					}
				case "a":
					if text, ok := elemMap["text"].(string); ok {
						href, _ := elemMap["href"].(string)
						parts = append(parts, fmt.Sprintf("%s (%s)", text, href))
					}
				case "at":
					if userID, ok := elemMap["user_id"].(string); ok {
						parts = append(parts, fmt.Sprintf("@%s", userID))
					}
				case "img":
					if imageKey, ok := elemMap["image_key"].(string); ok {
						parts = append(parts, fmt.Sprintf("<image image_key=\"%s\" />", imageKey))
					}
				case "button":
					if text, ok := elemMap["text"].(string); ok {
						btnType, _ := elemMap["type"].(string)
						parts = append(parts, fmt.Sprintf("[按钮: %s (%s)]", text, btnType))
					}
				case "hr":
					parts = append(parts, "---")
				case "note":
					// 备注元素
					if noteElems, ok := elemMap["elements"].([]any); ok {
						var noteParts []string
						for _, ne := range noteElems {
							if neMap, ok := ne.(map[string]any); ok {
								if neTag, _ := neMap["tag"].(string); neTag == "img" {
									if ik, ok := neMap["image_key"].(string); ok {
										noteParts = append(noteParts, fmt.Sprintf("<image image_key=\"%s\" />", ik))
									}
								} else if neText, ok := neMap["text"].(string); ok {
									noteParts = append(noteParts, neText)
								}
							}
						}
						if len(noteParts) > 0 {
							parts = append(parts, "[备注: "+strings.Join(noteParts, " ")+"]")
						}
					}
				case "select_static", "multi_select_static":
					if placeholder, ok := elemMap["placeholder"].(string); ok {
						parts = append(parts, fmt.Sprintf("[下拉选择: %s]", placeholder))
					}
				case "date_picker":
					if placeholder, ok := elemMap["placeholder"].(string); ok {
						parts = append(parts, fmt.Sprintf("[日期选择: %s]", placeholder))
					}
				case "overflow":
					parts = append(parts, "[更多选项]")
				default:
					// 未知元素类型，记录但不阻塞
					if tag != "" {
						parts = append(parts, fmt.Sprintf("[%s]", tag))
					}
				}
			}
		}
	}

	if len(parts) == 0 {
		return "[卡片消息]"
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
