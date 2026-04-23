package channel

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xbot/bus"
)

func TestAgentChannelRPC(t *testing.T) {
	var calls atomic.Int32
	runFn := func(ctx context.Context, task string) (string, error) {
		calls.Add(1)
		return "response to: " + task, nil
	}

	ac := NewAgentChannel("agent:test/inst1", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	msg := bus.OutboundMessage{Content: "hello"}
	result, err := ac.Send(msg)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result != "response to: hello" {
		t.Errorf("expected 'response to: hello', got %q", result)
	}
	if calls.Load() != 1 {
		t.Errorf("expected 1 call, got %d", calls.Load())
	}
}

func TestAgentChannelMultipleRPC(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		time.Sleep(10 * time.Millisecond) // simulate work
		return "done: " + task, nil
	}

	ac := NewAgentChannel("agent:test/multi", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	// Sequential sends — each should get its own reply
	for i := 0; i < 5; i++ {
		msg := bus.OutboundMessage{Content: "task" + time.Now().String()}
		result, err := ac.Send(msg)
		if err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
		if result[:5] != "done:" {
			t.Errorf("Send %d: unexpected result %q", i, result)
		}
	}
}

func TestAgentChannelClosed(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		return "ok", nil
	}

	ac := NewAgentChannel("agent:test/closed", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ac.Stop()

	// Send after close should fail
	_, err := ac.Send(bus.OutboundMessage{Content: "hello"})
	if err == nil {
		t.Error("expected error after Stop")
	}
}

func TestAgentChannelDoubleStop(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		return "ok", nil
	}

	ac := NewAgentChannel("agent:test/double", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Double stop should not panic
	ac.Stop()
	ac.Stop()
}

func TestAgentChannelRunFnError(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		return "Error: something went wrong", nil
	}

	ac := NewAgentChannel("agent:test/err", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	result, err := ac.Send(bus.OutboundMessage{Content: "fail"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if result != "Error: something went wrong" {
		t.Errorf("expected error message in result, got %q", result)
	}
}

func TestAgentChannelContextCancellation(t *testing.T) {
	started := make(chan struct{})
	runFn := func(ctx context.Context, task string) (string, error) {
		close(started)
		<-ctx.Done()
		return "cancelled", nil
	}

	ac := NewAgentChannel("agent:test/cancel", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	// Start a Send that will block on context cancellation
	done := make(chan struct{})
	go func() {
		defer close(done)
		ac.Send(bus.OutboundMessage{Content: "block"})
	}()

	// Wait for runFn to start
	select {
	case <-started:
		// good
	case <-time.After(time.Second):
		t.Fatal("runFn never started")
	}

	// Stop should cancel the context
	go func() {
		time.Sleep(50 * time.Millisecond)
		ac.Stop()
	}()

	// The blocked Send should eventually return
	select {
	case <-done:
		// good
	case <-time.After(2 * time.Second):
		t.Fatal("Send never returned after Stop")
	}
}

func TestAgentChannelInDispatcher(t *testing.T) {
	runFn := func(ctx context.Context, task string) (string, error) {
		return "dispatched: " + task, nil
	}

	// Create a minimal Dispatcher with nil MessageBus (sufficient for SendDirect)
	disp := NewDispatcher(nil)

	ac := NewAgentChannel("agent:test/dispatch", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	disp.Register(ac)

	// Send via Dispatcher
	msg := bus.OutboundMessage{
		Channel: "agent:test/dispatch",
		Content: "hello dispatcher",
	}
	result, err := disp.SendDirect(msg)
	if err != nil {
		t.Fatalf("SendDirect: %v", err)
	}
	if result != "dispatched: hello dispatcher" {
		t.Errorf("expected 'dispatched: hello dispatcher', got %q", result)
	}

	// Cleanup
	disp.Unregister("agent:test/dispatch")
	ac.Stop()
}

func TestAgentChannelConcurrentSends(t *testing.T) {
	var completed atomic.Int32
	runFn := func(ctx context.Context, task string) (string, error) {
		time.Sleep(20 * time.Millisecond)
		return "ok: " + task, nil
	}

	ac := NewAgentChannel("agent:test/concurrent", runFn)
	if err := ac.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer ac.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			result, err := ac.Send(bus.OutboundMessage{Content: "task"})
			if err != nil {
				t.Errorf("Send %d: %v", idx, err)
				return
			}
			if result[:3] != "ok:" {
				t.Errorf("Send %d: unexpected result %q", idx, result)
			}
			completed.Add(1)
		}(i)
	}
	wg.Wait()

	if completed.Load() != 10 {
		t.Errorf("expected 10 completions, got %d", completed.Load())
	}
}
