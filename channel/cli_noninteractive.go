package channel

import (
	"fmt"
	"strings"
	"xbot/bus"
)

// ---------------------------------------------------------------------------
// NonInteractiveChannel (非交互模式，单次执行)
// ---------------------------------------------------------------------------

// NonInteractiveChannel 非交互模式渠道，用于管道/参数模式。
// 收到完整消息后打印到 stdout 并设置退出标志。
type NonInteractiveChannel struct {
	msgBus *bus.MessageBus
	msgCh  chan bus.OutboundMessage
	done   chan struct{}
}

// NewNonInteractiveChannel 创建非交互模式渠道
func NewNonInteractiveChannel(msgBus *bus.MessageBus) *NonInteractiveChannel {
	ch := &NonInteractiveChannel{
		msgBus: msgBus,
		msgCh:  make(chan bus.OutboundMessage, 64),
		done:   make(chan struct{}),
	}
	// 启动消息接收 goroutine
	go ch.run()
	return ch
}

func (c *NonInteractiveChannel) run() {
	var prevContent string
	for msg := range c.msgCh {
		content := msg.Content
		if strings.HasPrefix(content, "__FEISHU_CARD__") {
			content = ConvertFeishuCard(content)
		}
		if msg.IsPartial {
			// 流式部分消息：只输出增量部分
			if len(content) > len(prevContent) {
				diff := content[len(prevContent):]
				fmt.Print(diff)
			}
			prevContent = content
		} else {
			// 完整消息：输出剩余差异部分，然后换行
			if len(content) > len(prevContent) {
				diff := content[len(prevContent):]
				fmt.Print(diff)
			}
			fmt.Println()
			close(c.done)
			return
		}
	}
}

func (c *NonInteractiveChannel) Name() string { return "cli" }
func (c *NonInteractiveChannel) Start() error { return nil }
func (c *NonInteractiveChannel) Stop()        {}
func (c *NonInteractiveChannel) Send(msg bus.OutboundMessage) (string, error) {
	select {
	case c.msgCh <- msg:
	default:
	}
	return "", nil
}
func (c *NonInteractiveChannel) WaitDone() { <-c.done }
