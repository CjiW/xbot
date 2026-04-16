package llm

import (
	"context"
	"sync"

	log "xbot/logger"
)

// DefaultLLMConcurrency is the default max concurrent LLM calls per tenant
// for the global (shared) LLM.
const DefaultLLMConcurrency = 5

// DefaultLLMConcurrencyPersonal is the default max concurrent LLM calls per tenant
// for personal (user-provided) LLM.
const DefaultLLMConcurrencyPersonal = 3

// tenantSem is a counting semaphore that supports dynamic capacity changes
// without replacing the underlying channel, avoiding goroutine leaks.
type tenantSem struct {
	mu       sync.Mutex
	cond     *sync.Cond
	count    int
	capacity int
}

func newTenantSem(capacity int) *tenantSem {
	s := &tenantSem{capacity: capacity}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// acquire blocks until a slot is available or ctx is cancelled.
func (s *tenantSem) acquire(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for s.count >= s.capacity {
		// Wait with context cancellation support
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				s.cond.Broadcast()
			case <-done:
			}
		}()

		s.cond.Wait()
		close(done) // signal the goroutine to stop

		if ctx.Err() != nil {
			return false
		}
	}
	s.count++
	return true
}

func (s *tenantSem) release() {
	s.mu.Lock()
	if s.count <= 0 {
		s.mu.Unlock()
		log.Warn("semaphore underflow")
		return
	}
	s.count--
	s.mu.Unlock()
	s.cond.Signal()
}

func (s *tenantSem) setCapacity(cap int) {
	s.mu.Lock()
	s.capacity = cap
	s.mu.Unlock()
	s.cond.Broadcast()
}

// LLMSemaphoreManager manages per-tenant LLM call concurrency using semaphores.
// Each tenant (identified by OriginUserID) gets independent semaphores for
// global LLM and personal LLM calls, preventing a single user from exhausting
// shared resources.
type LLMSemaphoreManager struct {
	mu         sync.RWMutex
	semaphores map[string]*tenantSem // key: "senderID:llmKey" → semaphore
	// llmKey: "global" for shared LLM, "personal" for user-provided LLM
}

// NewLLMSemaphoreManager creates a new LLMSemaphoreManager.
func NewLLMSemaphoreManager() *LLMSemaphoreManager {
	return &LLMSemaphoreManager{
		semaphores: make(map[string]*tenantSem),
	}
}

// Acquire obtains a concurrency slot for the given tenant and LLM type.
// It blocks until a slot is available or ctx is cancelled.
// getCapacity is called to dynamically read the current max concurrency setting.
// Returns a release function that must be called when the LLM call completes.
//
// The semaphore is never replaced — capacity changes are applied in-place via
// setCapacity + Broadcast, so goroutines blocked in acquire are always woken.
func (m *LLMSemaphoreManager) Acquire(ctx context.Context, senderID, llmKey string, getCapacity func() int) func() {
	desired := getCapacity()
	if desired <= 0 {
		// 0 or negative means no limit
		return func() {}
	}

	key := senderID + ":" + llmKey

	m.mu.RLock()
	sem := m.semaphores[key]
	m.mu.RUnlock()

	if sem == nil {
		m.mu.Lock()
		sem = m.semaphores[key]
		if sem == nil {
			sem = newTenantSem(desired)
			m.semaphores[key] = sem
		}
		m.mu.Unlock()
	}

	// Update capacity in-place; Broadcast wakes any goroutines blocked in acquire
	sem.setCapacity(desired)

	if !sem.acquire(ctx) {
		return func() {}
	}
	return sem.release
}
