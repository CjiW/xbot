package agent

import (
	"context"
	"fmt"
	"sync"

	"xbot/llm"
	log "xbot/logger"
	"xbot/session"
)

// ContextMode selects the context management strategy.
type ContextMode string

const (
	ContextModePhase1 ContextMode = "phase1"
	ContextModeNone   ContextMode = "none"
)

// ValidContextModes lists all recognized modes.
var ValidContextModes = []ContextMode{ContextModePhase1, ContextModeNone}

// IsValidContextMode checks if a mode string is recognized.
func IsValidContextMode(mode ContextMode) bool {
	for _, m := range ValidContextModes {
		if m == mode {
			return true
		}
	}
	return false
}

// ContextManager is the unified interface for context compaction strategies.
type ContextManager interface {
	Mode() ContextMode
	ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool
	Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)
	ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error)
	ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats
	SessionHook() SessionCompressHook
}

// ContextStats holds token usage statistics for /context info.
type ContextStats struct {
	SystemTokens      int
	UserTokens        int
	AssistantTokens   int
	ToolMsgTokens     int
	ToolDefTokens     int
	TotalTokens       int
	MaxTokens         int
	Threshold         int
	Mode              ContextMode
	IsRuntimeOverride bool
	DefaultMode       ContextMode
}

// SessionCompressHook is called after persisting compaction results to session.
type SessionCompressHook interface {
	AfterPersist(ctx context.Context, tenantSession *session.TenantSession, result *CompressResult)
}

// ContextManagerConfig holds compaction configuration with concurrent-safe runtime override.
type ContextManagerConfig struct {
	mu sync.RWMutex

	MaxContextTokens     int
	CompressionThreshold float64

	DefaultMode ContextMode
	runtimeMode ContextMode
}

// EffectiveMode returns the currently active mode (runtime override takes priority).
func (c *ContextManagerConfig) EffectiveMode() ContextMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.runtimeMode != "" {
		return c.runtimeMode
	}
	return c.DefaultMode
}

// RuntimeMode returns the runtime override mode (empty string if none).
func (c *ContextManagerConfig) RuntimeMode() ContextMode {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.runtimeMode
}

// SetRuntimeMode sets the runtime mode override.
func (c *ContextManagerConfig) SetRuntimeMode(mode ContextMode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runtimeMode = mode
}

// ResetRuntimeMode clears the runtime override, reverting to DefaultMode.
func (c *ContextManagerConfig) ResetRuntimeMode() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runtimeMode = ""
}

// noopManager disables automatic compaction but keeps /compress available.
type noopManager struct {
	config *ContextManagerConfig
	phase1 *phase1Manager
}

func newNoopManager(cfg *ContextManagerConfig) *noopManager {
	return &noopManager{
		config: cfg,
		phase1: newPhase1Manager(cfg),
	}
}

func (m *noopManager) Mode() ContextMode                                  { return ContextModeNone }
func (m *noopManager) ShouldCompress([]llm.ChatMessage, string, int) bool { return false }
func (m *noopManager) SessionHook() SessionCompressHook                   { return nil }

func (m *noopManager) Compress(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error) {
	return nil, fmt.Errorf("auto compression is disabled (mode=none)")
}

func (m *noopManager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	return m.phase1.ManualCompress(ctx, messages, client, model)
}

func (m *noopManager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	stats := m.phase1.ContextInfo(messages, model, toolTokens)
	stats.Mode = ContextModeNone
	return stats
}

// NewContextManager creates a ContextManager based on the effective mode.
func NewContextManager(cfg *ContextManagerConfig) ContextManager {
	mode := cfg.EffectiveMode()
	switch mode {
	case ContextModeNone:
		return newNoopManager(cfg)
	case ContextModePhase1, "":
		return newPhase1Manager(cfg)
	default:
		log.WithField("mode", mode).Warnf("Unknown context mode %q, falling back to Phase 1", mode)
		return newPhase1Manager(cfg)
	}
}

// resolveContextMode determines the context mode from Config.
func resolveContextMode(cfg Config) ContextMode {
	if cfg.ContextMode != "" {
		if IsValidContextMode(cfg.ContextMode) {
			return cfg.ContextMode
		}
		log.WithField("mode", cfg.ContextMode).Warn("Invalid AGENT_CONTEXT_MODE, ignoring")
	}
	if !cfg.EnableAutoCompress {
		return ContextModeNone
	}
	return ContextModePhase1
}
