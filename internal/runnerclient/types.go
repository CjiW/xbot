package runnerclient

import "time"

const (
	// PingPeriod 心跳发送间隔
	PingPeriod = 30 * time.Second
	// PongWait 等待 pong 响应超时
	PongWait = 60 * time.Second
	// WriteWait 写操作超时
	WriteWait = 10 * time.Second
)

// WriteMsg 是通过单一写协程发送的消息。
type WriteMsg struct {
	Data []byte
	Err  chan error // 非 nil 表示控制消息（如 ping），需要错误回报
}

// LogFunc 是日志回调函数类型。
type LogFunc func(format string, args ...any)

// callLogf 安全调用日志函数（nil 保护）。
func callLogf(logf LogFunc, format string, args ...any) {
	if logf != nil {
		logf(format, args...)
	}
}
