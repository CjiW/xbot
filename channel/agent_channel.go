package channel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"xbot/bus"
	"xbot/clipanic"
)

// AgentChannel wraps a SubAgent as a Channel in Dispatcher.
// Enables unified routing: SendMessage(to="agent:reviewer/r1") → Dispatcher → AgentChannel.
//
// RPC mechanism: Each Send() creates a per-request reply channel.
// The processing goroutine writes the result to that specific channel.
// This prevents reply mixing under concurrent Send() calls.
type AgentChannel struct {
	name   string
	runFn  bus.RunFn
	ctx    context.Context
	cancel context.CancelFunc
	inbox  chan *rpcRequest
	closed atomic.Bool
	mu     sync.Mutex // guards closed check + inbox send
	wg     sync.WaitGroup
}

type rpcRequest struct {
	task    string
	replyCh chan<- string
}

// NewAgentChannel creates a new AgentChannel for a SubAgent.
func NewAgentChannel(name string, runFn bus.RunFn) *AgentChannel {
	return &AgentChannel{
		name:  name,
		runFn: runFn,
		inbox: make(chan *rpcRequest, 16),
	}
}

func (ac *AgentChannel) Name() string { return ac.name }

// Start launches the SubAgent processing loop.
func (ac *AgentChannel) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	ac.ctx = ctx
	ac.cancel = cancel

	ac.wg.Go(func() {
		defer clipanic.Recover("channel.AgentChannel.Start", nil, false)
		for {
			select {
			case <-ctx.Done():
				return
			case req, ok := <-ac.inbox:
				if !ok {
					return
				}
				result, err := ac.runFn(ctx, req.task)
				if err != nil {
					result = "Error: " + err.Error()
				}
				select {
				case req.replyCh <- result:
				case <-ctx.Done():
				}
			}
		}
	})

	return nil
}

// Stop cancels the SubAgent and waits for it to finish.
func (ac *AgentChannel) Stop() {
	ac.mu.Lock()
	if ac.closed.Swap(true) {
		ac.mu.Unlock()
		return
	}
	close(ac.inbox)
	ac.mu.Unlock()

	if ac.cancel != nil {
		ac.cancel()
	}
	ac.wg.Wait()
}

// Send delivers a message to the SubAgent and waits for the reply (RPC).
func (ac *AgentChannel) Send(msg bus.OutboundMessage) (string, error) {
	replyCh := make(chan string, 1)
	req := &rpcRequest{task: msg.Content, replyCh: replyCh}

	ac.mu.Lock()
	closed := ac.closed.Load()
	ac.mu.Unlock()
	if closed {
		return "", fmt.Errorf("agent channel %s is closed", ac.name)
	}
	select {
	case ac.inbox <- req:
	case <-ac.ctx.Done():
		return "", fmt.Errorf("agent channel %s is stopped", ac.name)
	}

	select {
	case reply := <-replyCh:
		return reply, nil
	case <-ac.ctx.Done():
		return "", fmt.Errorf("agent channel %s stopped", ac.name)
	}
}

// IsClosed reports whether the channel is closed.
func (ac *AgentChannel) IsClosed() bool { return ac.closed.Load() }
