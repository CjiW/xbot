package bus

import "time"

const (
	// MetadataReplyPolicy controls how Agent should behave before final reply.
	// Supported values:
	// - "auto" (default): normal flow, send ack/progress
	// - "optional": no ack/progress; agent may decide to not reply
	MetadataReplyPolicy = "reply_policy"

	ReplyPolicyAuto     = "auto"
	ReplyPolicyOptional = "optional"
)

// InboundReplyPolicy returns normalized reply policy for inbound metadata.
func InboundReplyPolicy(metadata map[string]string) string {
	if metadata == nil {
		return ReplyPolicyAuto
	}
	policy := metadata[MetadataReplyPolicy]
	if policy == "" {
		return ReplyPolicyAuto
	}
	return policy
}

// ShouldPreReplyNotify indicates whether ack/progress UI should be sent before final reply.
func ShouldPreReplyNotify(metadata map[string]string) bool {
	return InboundReplyPolicy(metadata) != ReplyPolicyOptional
}

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
	IsCron     bool // 是否由 cron 定时任务触发
}

// OutboundMessage 发送到 IM 渠道的出站消息
type OutboundMessage struct {
	Channel       string            // 目标渠道
	ChatID        string            // 目标会话
	Content       string            // 消息文本
	Media         []string          // 附件文件路径
	Metadata      map[string]string // 附加元数据
	WorkspaceRoot string            // 发送者的工作目录（用于解析文件路径）
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
