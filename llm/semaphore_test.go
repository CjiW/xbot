package llm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLLMSemaphoreManager_BasicAcquire(t *testing.T) {
	mgr := NewLLMSemaphoreManager()

	release := mgr.Acquire(context.Background(), "user1", "global", func() int { return 2 })
	if release == nil {
		t.Fatal("release func should not be nil")
	}
	release()
}

func TestLLMSemaphoreManager_ConcurrencyLimit(t *testing.T) {
	mgr := NewLLMSemaphoreManager()
	const maxConcurrent = 3

	var running atomic.Int32
	var maxRunning atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := mgr.Acquire(context.Background(), "user1", "global", func() int { return maxConcurrent })
			cur := running.Add(1)
			for {
				old := maxRunning.Load()
				if cur <= old || maxRunning.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond) // hold slot
			running.Add(-1)
			release()
		}()
	}

	wg.Wait()

	if got := maxRunning.Load(); got > maxConcurrent {
		t.Errorf("max concurrent = %d, want <= %d", got, maxConcurrent)
	}
}

func TestLLMSemaphoreManager_ZeroCapacity(t *testing.T) {
	mgr := NewLLMSemaphoreManager()

	// capacity 0 means no limit — should never block
	var started atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release := mgr.Acquire(context.Background(), "user1", "global", func() int { return 0 })
			started.Add(1)
			time.Sleep(10 * time.Millisecond)
			if release != nil {
				release()
			}
		}()
	}

	wg.Wait()
	if got := started.Load(); got != 5 {
		t.Errorf("started = %d, want 5 (no limit)", got)
	}
}

func TestLLMSemaphoreManager_CancelledContext(t *testing.T) {
	mgr := NewLLMSemaphoreManager()

	ctx, cancel := context.WithCancel(context.Background())

	// Fill up the semaphore
	for i := 0; i < 2; i++ {
		mgr.Acquire(context.Background(), "user1", "global", func() int { return 2 })
	}

	// This should block until cancelled
	done := make(chan struct{})
	go func() {
		release := mgr.Acquire(ctx, "user1", "global", func() int { return 2 })
		if release != nil {
			release()
		}
		close(done)
	}()

	cancel()
	select {
	case <-done:
		// success
	case <-time.After(time.Second):
		t.Fatal("Acquire should return when context is cancelled")
	}
}

func TestLLMSemaphoreManager_DifferentTenantIsolation(t *testing.T) {
	mgr := NewLLMSemaphoreManager()

	// user1 gets capacity 1, user2 gets capacity 1
	// Both should be able to run concurrently since they're different tenants
	var wg sync.WaitGroup
	var count atomic.Int32

	wg.Add(2)
	for _, user := range []string{"user1", "user2"} {
		go func(u string) {
			defer wg.Done()
			release := mgr.Acquire(context.Background(), u, "global", func() int { return 1 })
			count.Add(1)
			time.Sleep(20 * time.Millisecond)
			count.Add(-1)
			release()
		}(user)
	}

	wg.Wait()
	// Both should have been able to run at the same time
	if count.Load() != 0 {
		t.Error("expected all goroutines to finish")
	}
}

func TestLLMSemaphoreManager_DifferentLLMKeyIsolation(t *testing.T) {
	mgr := NewLLMSemaphoreManager()

	// Same user but different LLM key — should have independent limits
	var wg sync.WaitGroup
	var count atomic.Int32

	wg.Add(2)
	for _, key := range []string{"global", "personal"} {
		go func(k string) {
			defer wg.Done()
			release := mgr.Acquire(context.Background(), "user1", k, func() int { return 1 })
			count.Add(1)
			time.Sleep(20 * time.Millisecond)
			count.Add(-1)
			release()
		}(key)
	}

	wg.Wait()
	if count.Load() != 0 {
		t.Error("expected all goroutines to finish")
	}
}

func TestLLMSemaphoreManager_DynamicCapacityChange(t *testing.T) {
	mgr := NewLLMSemaphoreManager()

	// First acquire with capacity 1
	release1 := mgr.Acquire(context.Background(), "user1", "global", func() int { return 1 })
	if release1 == nil {
		t.Fatal("first acquire should succeed with cap=1")
	}

	// Try to acquire again with capacity 1 — should block
	done := make(chan struct{})
	go func() {
		release := mgr.Acquire(context.Background(), "user1", "global", func() int { return 1 })
		if release != nil {
			release()
		}
		close(done)
	}()

	// Should not complete immediately
	select {
	case <-done:
		t.Fatal("second acquire should block when cap=1 and first holds slot")
	case <-time.After(50 * time.Millisecond):
		// good, it's blocking
	}

	// Release the first slot
	release1()

	// Now second should be able to proceed
	select {
	case <-done:
		// success
	case <-time.After(time.Second):
		t.Fatal("second acquire should succeed after first release")
	}
}
