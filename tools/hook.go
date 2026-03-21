package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	log "xbot/logger"
)

// MaxHookChainLen is the maximum number of hooks allowed in a HookChain.
const MaxHookChainLen = 20

// ToolHook is the interface for tool execution lifecycle hooks.
// Implement PreToolUse and/or PostToolUse to intercept tool execution.
type ToolHook interface {
	// Name returns a unique identifier for this hook.
	Name() string

	// PreToolUse is called before a tool is executed.
	// Return an error to block the tool execution.
	PreToolUse(ctx context.Context, toolName string, args string) error

	// PostToolUse is called after a tool execution completes (always, even on error).
	PostToolUse(ctx context.Context, toolName string, args string, result *ToolResult, err error, elapsed time.Duration)
}

// HookChain manages an ordered list of ToolHook implementations.
// It is safe for concurrent use (sync.RWMutex).
type HookChain struct {
	mu    sync.RWMutex
	hooks []ToolHook
}

// NewHookChain creates a HookChain with the given hooks pre-registered.
func NewHookChain(hooks ...ToolHook) *HookChain {
	hc := &HookChain{
		hooks: make([]ToolHook, 0, len(hooks)),
	}
	for _, h := range hooks {
		if h != nil {
			if len(hc.hooks) >= MaxHookChainLen {
				log.Warnf("NewHookChain: hook chain exceeds maximum length of %d, truncating", MaxHookChainLen)
				break
			}
			hc.hooks = append(hc.hooks, h)
		}
	}
	return hc
}

// Use adds a hook to the end of the chain.
// If a hook with the same name already exists, it is replaced.
// Returns an error if the chain is at maximum capacity.
func (hc *HookChain) Use(hook ToolHook) error {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	name := hook.Name()
	for i, h := range hc.hooks {
		if h.Name() == name {
			hc.hooks[i] = hook
			return nil
		}
	}
	if len(hc.hooks) >= MaxHookChainLen {
		return fmt.Errorf("hook chain exceeds maximum length of %d", MaxHookChainLen)
	}
	hc.hooks = append(hc.hooks, hook)
	return nil
}

// Remove removes a hook by name.
func (hc *HookChain) Remove(name string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	for i, h := range hc.hooks {
		if h.Name() == name {
			hc.hooks = append(hc.hooks[:i], hc.hooks[i+1:]...)
			return
		}
	}
}

// RunPre executes all PreToolUse hooks in order.
// If any hook returns an error, execution is blocked and that error is returned.
// Panics in individual hooks are recovered; subsequent hooks still run.
func (hc *HookChain) RunPre(ctx context.Context, toolName string, args string) error {
	hc.mu.RLock()
	hooks := make([]ToolHook, len(hc.hooks))
	copy(hooks, hc.hooks)
	hc.mu.RUnlock()

	var firstErr error
	for _, h := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Warnf("ToolHook.PreToolUse panic in hook %q: %v", h.Name(), r)
				}
			}()
			if err := h.PreToolUse(ctx, toolName, args); err != nil && firstErr == nil {
				firstErr = err
			}
		}()
	}
	return firstErr
}

// RunPost executes all PostToolUse hooks in order.
// Panics in individual hooks are recovered; all hooks are guaranteed to run.
func (hc *HookChain) RunPost(ctx context.Context, toolName string, args string, result *ToolResult, err error, elapsed time.Duration) {
	hc.mu.RLock()
	hooks := make([]ToolHook, len(hc.hooks))
	copy(hooks, hc.hooks)
	hc.mu.RUnlock()

	for _, h := range hooks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Warnf("ToolHook.PostToolUse panic in hook %q: %v", h.Name(), r)
				}
			}()
			h.PostToolUse(ctx, toolName, args, result, err, elapsed)
		}()
	}
}
