package channel

import (
	"fmt"
	"xbot/bus"
	log "xbot/logger"
)

// Dispatcher 出站消息分发器
type Dispatcher struct {
	channels map[string]Channel
	bus      *bus.MessageBus
	done     chan struct{}
}

// NewDispatcher 创建分发器
func NewDispatcher(msgBus *bus.MessageBus) *Dispatcher {
	return &Dispatcher{
		channels: make(map[string]Channel),
		bus:      msgBus,
		done:     make(chan struct{}),
	}
}

// Register 注册渠道
func (d *Dispatcher) Register(ch Channel) {
	d.channels[ch.Name()] = ch
	log.WithField("channel", ch.Name()).Info("Channel registered")
}

// Run 启动出站消息分发循环
func (d *Dispatcher) Run() {
	log.Info("Outbound dispatcher started")
	for {
		select {
		case <-d.done:
			return
		case msg := <-d.bus.Outbound:
			ch, ok := d.channels[msg.Channel]
			if !ok {
				log.WithField("channel", msg.Channel).Warn("Unknown channel, dropping message")
				continue
			}
			if _, err := ch.Send(msg); err != nil {
				log.WithError(err).WithField("channel", msg.Channel).Error("Failed to send message")
			}
		}
	}
}

// Stop 停止分发器
func (d *Dispatcher) Stop() {
	close(d.done)
	for _, ch := range d.channels {
		ch.Stop()
	}
}

// SendDirect 同步发送消息到指定渠道，返回平台消息 ID
func (d *Dispatcher) SendDirect(msg bus.OutboundMessage) (string, error) {
	ch, ok := d.channels[msg.Channel]
	if !ok {
		return "", fmt.Errorf("unknown channel: %s", msg.Channel)
	}
	return ch.Send(msg)
}

// GetChannel 获取渠道
func (d *Dispatcher) GetChannel(name string) (Channel, bool) {
	ch, ok := d.channels[name]
	return ch, ok
}

// EnabledChannels 返回已注册的渠道列表
func (d *Dispatcher) EnabledChannels() []string {
	names := make([]string, 0, len(d.channels))
	for name := range d.channels {
		names = append(names, name)
	}
	return names
}
