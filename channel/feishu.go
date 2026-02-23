package channel

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"xbot/bus"
	log "xbot/logger"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
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
	).OnP2MessageReceiveV1(f.onMessage)

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

	// 判断 receive_id_type
	receiveIDType := "chat_id"
	if !strings.HasPrefix(msg.ChatID, "oc_") {
		receiveIDType = "open_id"
	}

	// 构建消息卡片
	card := f.buildCard(msg.Content)
	cardJSON, err := json.Marshal(card)
	if err != nil {
		return fmt.Errorf("marshal card: %w", err)
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(receiveIDType).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(msg.ChatID).
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

	log.WithField("chat_id", msg.ChatID).Debug("Feishu message sent")
	return nil
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

	// 消息去重
	messageID := *msg.MessageId
	if f.isDuplicate(messageID) {
		return nil
	}

	// 跳过机器人自己的消息
	if sender.SenderType != nil && *sender.SenderType == "bot" {
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
