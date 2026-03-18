package tools

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"
)

// LoggingHook logs tool execution start/completion/failure.
// It replaces the inline logging in engine.go's execOne closure.
type LoggingHook struct{}

// NewLoggingHook creates a new LoggingHook.
func NewLoggingHook() *LoggingHook {
	return &LoggingHook{}
}

func (h *LoggingHook) Name() string { return "logging" }

func (h *LoggingHook) PreToolUse(ctx context.Context, toolName string, args string) error {
	preview := args
	if r := []rune(preview); len(r) > 200 {
		preview = string(r[:200]) + "..."
	}
	log.Ctx(ctx).WithField("tool", toolName).Infof("Tool call: %s(%s)", toolName, preview)
	return nil
}

func (h *LoggingHook) PostToolUse(ctx context.Context, toolName string, args string, result *ToolResult, err error, elapsed time.Duration) {
	fields := log.Fields{
		"tool":    toolName,
		"elapsed": elapsed.Round(time.Millisecond),
	}
	if err != nil {
		log.Ctx(ctx).WithFields(fields).WithError(err).Warn("Tool execution failed")
	} else {
		preview := ""
		if result != nil {
			preview = result.Summary
			if r := []rune(preview); len(r) > 200 {
				preview = string(r[:200]) + "..."
			}
		}
		log.Ctx(ctx).WithFields(fields).Infof("Tool done: %s", preview)
	}
}

// toolTimingStats holds per-tool timing statistics.
// All counter fields use int64 and are updated via sync/atomic.
type toolTimingStats struct {
	Count int64
	Total int64 // total duration in nanoseconds
	Min   int64 // min duration in nanoseconds
	Max   int64 // max duration in nanoseconds
}

// TimingHook collects per-tool execution timing statistics.
// Map access is protected by sync.RWMutex; counter fields use atomic operations.
type TimingHook struct {
	mu    sync.RWMutex
	stats map[string]*toolTimingStats
}

// NewTimingHook creates a new TimingHook.
func NewTimingHook() *TimingHook {
	return &TimingHook{
		stats: make(map[string]*toolTimingStats),
	}
}

func (h *TimingHook) Name() string { return "timing" }

func (h *TimingHook) PreToolUse(_ context.Context, _ string, _ string) error {
	return nil
}

func (h *TimingHook) PostToolUse(_ context.Context, toolName string, _ string, _ *ToolResult, _ error, elapsed time.Duration) {
	ns := elapsed.Nanoseconds()

	// Get or create stats entry (map access needs mutex)
	h.mu.RLock()
	s, ok := h.stats[toolName]
	h.mu.RUnlock()

	if !ok {
		h.mu.Lock()
		// Double-check after acquiring write lock
		s, ok = h.stats[toolName]
		if !ok {
			s = &toolTimingStats{Min: ns, Max: ns}
			h.stats[toolName] = s
		}
		h.mu.Unlock()
	}

	// Atomic counter updates (no mutex needed for the struct fields)
	atomic.AddInt64(&s.Count, 1)
	atomic.AddInt64(&s.Total, ns)

	// Atomic min/max update via CAS loop
	for {
		old := atomic.LoadInt64(&s.Min)
		if ns >= old || atomic.CompareAndSwapInt64(&s.Min, old, ns) {
			break
		}
	}
	for {
		old := atomic.LoadInt64(&s.Max)
		if ns <= old || atomic.CompareAndSwapInt64(&s.Max, old, ns) {
			break
		}
	}
}

// TimingSnapshot is a snapshot of timing statistics for a single tool.
type TimingSnapshot struct {
	Count   int64
	Total   time.Duration
	Average time.Duration
	Min     time.Duration
	Max     time.Duration
}

// Stats returns a snapshot of all timing statistics.
// The returned map is a copy and safe to read without synchronization.
func (h *TimingHook) Stats() map[string]TimingSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make(map[string]TimingSnapshot, len(h.stats))
	for name, s := range h.stats {
		count := atomic.LoadInt64(&s.Count)
		total := atomic.LoadInt64(&s.Total)
		min := atomic.LoadInt64(&s.Min)
		max := atomic.LoadInt64(&s.Max)

		snap := TimingSnapshot{
			Count: count,
			Total: time.Duration(total),
			Min:   time.Duration(min),
			Max:   time.Duration(max),
		}
		if count > 0 {
			snap.Average = time.Duration(total / count)
		}
		result[name] = snap
	}
	return result
}

// Reset clears all timing statistics.
func (h *TimingHook) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stats = make(map[string]*toolTimingStats)
}
