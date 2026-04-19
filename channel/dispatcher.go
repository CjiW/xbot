package channel

import (
	"fmt"
	"sync"

	"xbot/bus"
	log "xbot/logger"
)

// Dispatcher 出站消息分发器
type Dispatcher struct {
	channels map[string]Channel
	bus      *bus.MessageBus
	done     chan struct{}
	mu       sync.RWMutex
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
	d.mu.Lock()
	d.channels[ch.Name()] = ch
	d.mu.Unlock()
	log.WithField("channel", ch.Name()).Info("Channel registered")
}

// Run 启动出站消息分发循环
func (d *Dispatcher) Run() {
	log.Info("Outbound dispatcher started")
	for {
		select {
		case <-d.done:
			return
		case msg, ok := <-d.bus.Outbound:
			if !ok {
				log.Info("Outbound channel closed, dispatcher exiting")
				return
			}
			d.mu.RLock()
			ch, ok := d.channels[msg.Channel]
			d.mu.RUnlock()
			if !ok {
				log.WithField("channel", msg.Channel).Warn("Unknown channel, dropping message")
				continue
			}
			if _, err := func() (ret string, err error) {
				defer func() {
					if r := recover(); r != nil {
						log.WithField("channel", msg.Channel).Errorf("Channel.Send panic: %v", r)
						err = fmt.Errorf("channel %s panic: %v", msg.Channel, r)
					}
				}()
				return ch.Send(msg)
			}(); err != nil {
				log.WithError(err).WithField("channel", msg.Channel).Error("Failed to send message")
			}
		}
	}
}

// Stop 停止分发器
func (d *Dispatcher) Stop() {
	close(d.done)
	d.mu.RLock()
	for _, ch := range d.channels {
		ch.Stop()
	}
	d.mu.RUnlock()
}

// Unregister removes a channel from the dispatcher.
func (d *Dispatcher) Unregister(name string) {
	d.mu.Lock()
	delete(d.channels, name)
	d.mu.Unlock()
	log.WithField("channel", name).Info("Channel unregistered")
}

// SendMessage implements bus.MessageSender.
func (d *Dispatcher) SendMessage(channelName, chatID, content string) (string, error) {
	return d.SendDirect(bus.OutboundMessage{
		Channel: channelName,
		ChatID:  chatID,
		Content: content,
	})
}

// Compile-time interface check
var _ bus.MessageSender = (*Dispatcher)(nil)

// SendDirect 同步发送消息到指定渠道，返回平台消息 ID
func (d *Dispatcher) SendDirect(msg bus.OutboundMessage) (string, error) {
	d.mu.RLock()
	ch, ok := d.channels[msg.Channel]
	d.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("unknown channel: %s", msg.Channel)
	}
	return ch.Send(msg)
}

// GetChannel 获取渠道
func (d *Dispatcher) GetChannel(name string) (Channel, bool) {
	d.mu.RLock()
	ch, ok := d.channels[name]
	d.mu.RUnlock()
	return ch, ok
}

// EnabledChannels 返回已注册的渠道列表
func (d *Dispatcher) EnabledChannels() []string {
	d.mu.RLock()
	names := make([]string, 0, len(d.channels))
	for name := range d.channels {
		names = append(names, name)
	}
	d.mu.RUnlock()
	return names
}
