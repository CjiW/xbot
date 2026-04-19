package channel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"xbot/bus"
)

// RunConfigFn is a callback that runs a SubAgent and returns the result.
// This avoids channel→agent circular dependency.
type RunConfigFn func(ctx context.Context, task string) (string, error)

// AgentChannel wraps a SubAgent as a Channel.
// SubAgents register as Channels in Dispatcher, enabling:
//   - SendMessage(to="agent:reviewer") → direct agent-to-agent communication
//   - Unified routing through Dispatcher (no separate PostOffice needed)
//
// RPC mechanism: Each Send() creates a per-request reply channel.
// The processing goroutine writes the result to that specific channel.
// This prevents reply mixing under concurrent Send() calls.
type AgentChannel struct {
	name   string
	runFn  RunConfigFn
	ctx    context.Context
	cancel context.CancelFunc
	inbox  chan *rpcRequest
	closed atomic.Bool
	mu     sync.Mutex // guards closed check + inbox send
	wg     sync.WaitGroup
}

// rpcRequest pairs a task with its per-request reply channel.
type rpcRequest struct {
	task    string
	replyCh chan<- string
}

// NewAgentChannel creates a new AgentChannel for a SubAgent.
func NewAgentChannel(name string, runFn RunConfigFn) *AgentChannel {
	return &AgentChannel{
		name:  name,
		runFn: runFn,
		inbox: make(chan *rpcRequest, 16),
	}
}

// Name returns the channel name (e.g., "agent:reviewer-rt1").
func (ac *AgentChannel) Name() string { return ac.name }

// Start launches the SubAgent processing loop.
func (ac *AgentChannel) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	ac.ctx = ctx
	ac.cancel = cancel

	ac.wg.Add(1)
	go func() {
		defer ac.wg.Done()
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
	}()

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
// This is a blocking call — it waits until the SubAgent processes the task
// and returns the result, or until the channel is stopped.
func (ac *AgentChannel) Send(msg bus.OutboundMessage) (string, error) {
	replyCh := make(chan string, 1)
	req := &rpcRequest{task: msg.Content, replyCh: replyCh}

	// Guard closed check + inbox send with mutex to prevent panic on closed channel.
	// Use select with ctx.Done() to prevent deadlock when inbox is full and Stop() is called.
	ac.mu.Lock()
	if ac.closed.Load() {
		ac.mu.Unlock()
		return "", fmt.Errorf("agent channel %s is closed", ac.name)
	}
	select {
	case ac.inbox <- req:
		ac.mu.Unlock()
	case <-ac.ctx.Done():
		ac.mu.Unlock()
		return "", fmt.Errorf("agent channel %s is stopped", ac.name)
	}

	// Block until reply arrives (RPC semantics).
	// No mutex held here — safe for concurrent Send() calls.
	select {
	case reply := <-replyCh:
		return reply, nil
	case <-ac.ctx.Done():
		return "", fmt.Errorf("agent channel %s stopped", ac.name)
	}
}
