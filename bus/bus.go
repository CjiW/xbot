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

// InboundMessage 统一的入站消息。
// 来源可以是 IM 渠道（feishu/qq/cli）或其他 Agent（agent 内部调用）。
type InboundMessage struct {
	// === 路由（统一寻址，Phase 1 新增） ===
	From Address // 发送者地址: im://feishu/ou_xxx 或 agent://main
	To   Address // 目标地址:   im://feishu/oc_xxx 或 agent://main/code-reviewer

	// === 路由（旧字段，迁移期间保留，渠道层双写） ===
	Channel    string // 渠道名称: "feishu", "cli", "agent" 等
	SenderID   string // 发送者标识
	SenderName string // 发送者姓名（由渠道解析）
	ChatID     string // 会话/群组标识
	ChatType   string // 会话类型: "p2p" / "group" / "agent"

	// === 内容 ===
	Content string   // 消息文本
	Media   []string // 媒体文件路径

	// === 元数据 ===
	Metadata  map[string]string // 渠道/调用方特定元数据
	Time      time.Time
	IsCron    bool   // 是否由 cron 定时任务触发
	RequestID string // 请求追踪 ID（UUID 无横线），在渠道收到消息时生成

	// === Agent 间通信扩展（仅 Channel="agent" 时有值） ===
	ParentAgentID string   // 父 Agent ID（如 "main"）
	SystemPrompt  string   // 覆盖 system prompt
	AllowedTools  []string // 工具白名单（空=全部可用工具，除 SubAgent）
	RoleName      string   // SubAgent 角色名（如 "code-reviewer"）
	// Capabilities 声明 SubAgent 可获得的能力。
	// 使用 map[string]bool 避免 bus→tools 循环依赖。
	// 已知 key: "memory", "send_message", "spawn_agent"
	Capabilities map[string]bool
}

// IsFromAgent 判断消息是否来自其他 Agent（而非 IM 渠道）。
func (m *InboundMessage) IsFromAgent() bool {
	return m.Channel == SchemeAgent || m.From.IsAgent()
}

// OriginChannel 获取原始 IM 渠道名称。
// Agent 间调用时从 Metadata["origin_channel"] 继承，否则返回当前 Channel。
func (m *InboundMessage) OriginChannel() string {
	if m.IsFromAgent() {
		if ch, ok := m.Metadata["origin_channel"]; ok {
			return ch
		}
	}
	return m.Channel
}

// OriginChatID 获取原始 IM 会话 ID。
func (m *InboundMessage) OriginChatID() string {
	if m.IsFromAgent() {
		if id, ok := m.Metadata["origin_chat_id"]; ok {
			return id
		}
	}
	return m.ChatID
}

// OriginSenderID 获取原始 IM 发送者 ID。
func (m *InboundMessage) OriginSenderID() string {
	if m.IsFromAgent() {
		if id, ok := m.Metadata["origin_sender"]; ok {
			return id
		}
	}
	return m.SenderID
}

// OutboundMessage 统一的出站消息。
// 目标可以是 IM 渠道或调用方 Agent。
type OutboundMessage struct {
	// === 路由（统一寻址，Phase 1 新增） ===
	From Address // 发送者地址: agent://main
	To   Address // 目标地址:   im://feishu/oc_xxx 或 agent://main（返回给父 Agent）

	// === 路由（旧字段，迁移期间保留） ===
	Channel string // 目标渠道
	ChatID  string // 目标会话

	// === 内容 ===
	Content string   // 消息文本
	Media   []string // 附件文件路径

	// === 元数据 ===
	Metadata map[string]string // 附加元数据

	// === Agent 间通信扩展 ===
	ToolsUsed   []string // 使用过的工具列表（SubAgent 返回时携带）
	WaitingUser bool     // 是否等待用户响应
	Error       error    // 执行错误（SubAgent 返回时携带）
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
