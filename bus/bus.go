package bus

import "time"

// InboundMessage 从 IM 渠道收到的入站消息
type InboundMessage struct {
	Channel    string            // 渠道名称: "feishu", "cli" 等
	SenderID   string            // 发送者标识
	SenderName string            // 发送者姓名（由渠道解析）
	ChatID     string            // 会话/群组标识
	Content    string            // 消息文本
	Media      []string          // 媒体文件路径
	Metadata   map[string]string // 渠道特定元数据
	Time       time.Time
}

// OutboundMessage 发送到 IM 渠道的出站消息
type OutboundMessage struct {
	Channel  string            // 目标渠道
	ChatID   string            // 目标会话
	Content  string            // 消息文本
	Media    []string          // 附件文件路径
	Metadata map[string]string // 附加元数据
}

// MessageBus 异步消息总线，解耦渠道和 Agent
type MessageBus struct {
	Inbound  chan InboundMessage
	Outbound chan OutboundMessage
}

// NewMessageBus 创建消息总线
func NewMessageBus() *MessageBus {
	return &MessageBus{
		Inbound:  make(chan InboundMessage, 64),
		Outbound: make(chan OutboundMessage, 64),
	}
}
