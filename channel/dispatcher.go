package channel

import (
	"fmt"
	"sync"

	"xbot/bus"
	log "xbot/logger"
)

// Dispatcher 出站消息分发器
type Dispatcher struct {
	channels  map[string]Channel
	observers map[string][]Channel // channel name → observer channels（接收 outbound 副本）
	bus       *bus.MessageBus
	done      chan struct{}
	mu        sync.RWMutex
}

// NewDispatcher 创建分发器
func NewDispatcher(msgBus *bus.MessageBus) *Dispatcher {
	return &Dispatcher{
		channels:  make(map[string]Channel),
		observers: make(map[string][]Channel),
		bus:       msgBus,
		done:      make(chan struct{}),
	}
}

// Register 注册渠道
func (d *Dispatcher) Register(ch Channel) {
	d.mu.Lock()
	d.channels[ch.Name()] = ch
	d.mu.Unlock()
	log.WithField("channel", ch.Name()).Info("Channel registered")
}

// AddObserver 注册观察者：当目标 channel 收到 outbound 时，observer 也会收到一份副本。
func (d *Dispatcher) AddObserver(targetChannel string, observer Channel) {
	d.mu.Lock()
	d.observers[targetChannel] = append(d.observers[targetChannel], observer)
	d.mu.Unlock()
}

// RemoveObserver 移除观察者。
func (d *Dispatcher) RemoveObserver(targetChannel string, observer Channel) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list := d.observers[targetChannel]
	for i, ch := range list {
		if ch == observer {
			d.observers[targetChannel] = append(list[:i], list[i+1:]...)
			return
		}
	}
}

// Run 启动出站消息分发循环
func (d *Dispatcher) Run() {
	log.Info("Outbound dispatcher started")
	for {
		select {
		case <-d.done:
			return
		case msg := <-d.bus.Outbound:
			d.mu.RLock()
			ch, ok := d.channels[msg.Channel]
			var observers []Channel
			if ok {
				observers = d.observers[msg.Channel]
			}
			d.mu.RUnlock()
			if !ok {
				log.WithField("channel", msg.Channel).Warn("Unknown channel, dropping message")
				continue
			}
			if _, err := ch.Send(msg); err != nil {
				log.WithError(err).WithField("channel", msg.Channel).Error("Failed to send message")
			}
			// 转发副本给所有观察者
			for _, obs := range observers {
				if _, err := obs.Send(msg); err != nil {
					log.WithError(err).WithField("observer", obs.Name()).Error("Failed to send observer message")
				}
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
