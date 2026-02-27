package channel

import "xbot/bus"

// Channel 聊天渠道接口
type Channel interface {
	// Name 返回渠道名称
	Name() string
	// Start 启动渠道，阻塞运行直到 ctx 取消
	Start() error
	// Stop 停止渠道
	Stop()
	// Send 发送消息，返回平台消息 ID（用于后续更新）
	Send(msg bus.OutboundMessage) (string, error)
}
