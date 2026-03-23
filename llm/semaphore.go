package llm

import (
	"context"
	"sync"
)

// DefaultLLMConcurrency is the default max concurrent LLM calls per tenant
// for the global (shared) LLM.
const DefaultLLMConcurrency = 5

// DefaultLLMConcurrencyPersonal is the default max concurrent LLM calls per tenant
// for personal (user-provided) LLM.
const DefaultLLMConcurrencyPersonal = 3

// LLMSemaphoreManager manages per-tenant LLM call concurrency using semaphores.
// Each tenant (identified by OriginUserID) gets independent semaphores for
// global LLM and personal LLM calls, preventing a single user from exhausting
// shared resources.
type LLMSemaphoreManager struct {
	mu         sync.RWMutex
	semaphores map[string]chan struct{} // key: "senderID:llmKey" → semaphore
	// llmKey: "global" for shared LLM, "personal" for user-provided LLM
}

// NewLLMSemaphoreManager creates a new LLMSemaphoreManager.
func NewLLMSemaphoreManager() *LLMSemaphoreManager {
	return &LLMSemaphoreManager{
		semaphores: make(map[string]chan struct{}),
	}
}

// Acquire obtains a concurrency slot for the given tenant and LLM type.
// It blocks until a slot is available or ctx is cancelled.
// getCapacity is called to dynamically read the current max concurrency setting.
// Returns a release function that must be called when the LLM call completes.
//
// If capacity changes between calls (user updated settings), the semaphore is
// recreated with the new capacity. Existing goroutines holding slots on the
// old semaphore continue unaffected (the old semaphore won't be GC'd until
// they release, but new requests use the new one).
func (m *LLMSemaphoreManager) Acquire(ctx context.Context, senderID, llmKey string, getCapacity func() int) func() {
	desired := getCapacity()
	if desired <= 0 {
		// 0 or negative means no limit
		return func() {}
	}

	key := senderID + ":" + llmKey

	// Double-check locking: check capacity, rebuild if mismatch
	m.mu.RLock()
	sem := m.semaphores[key]
	m.mu.RUnlock()

	if sem == nil || cap(sem) != desired {
		m.mu.Lock()
		sem = m.semaphores[key]
		if sem == nil || cap(sem) != desired {
			newSem := make(chan struct{}, desired)
			m.semaphores[key] = newSem
			sem = newSem
		}
		m.mu.Unlock()
	}

	select {
	case sem <- struct{}{}:
		return func() { <-sem }
	case <-ctx.Done():
		return func() {}
	}
}
