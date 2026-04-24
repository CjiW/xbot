package hooks

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Manager — central hook lifecycle coordinator
// ---------------------------------------------------------------------------

// Manager loads hook configurations, matches events to handlers, and executes
// them to produce a single aggregated Decision.
type Manager struct {
	mu         sync.RWMutex
	config     *HookConfig
	layers     []*ConfigLayer
	executors  map[string]Executor
	builtins   []*CallbackHook
	xbotHome   string
	projectDir string
}

// NewManager creates a Manager by loading hook configurations from the
// standard layer locations. Default executors for "command" and "http" types
// are registered automatically. The "mcp_tool" executor must be injected via
// RegisterExecutor after creation.
func NewManager(xbotHome, projectDir string) (*Manager, error) {
	layers, config, err := LoadHooksConfig(xbotHome, projectDir)
	if err != nil {
		return nil, fmt.Errorf("load hooks config: %w", err)
	}

	m := &Manager{
		config:     config,
		layers:     layers,
		executors:  make(map[string]Executor),
		builtins:   make([]*CallbackHook, 0),
		xbotHome:   xbotHome,
		projectDir: projectDir,
	}

	// Register default executors.
	m.executors["command"] = NewCommandExecutor(xbotHome, projectDir)
	m.executors["http"] = NewHTTPExecutor()

	return m, nil
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// RegisterBuiltin adds an in-process callback hook. All builtins fire for
// every event; use the callback function to filter internally.
func (m *Manager) RegisterBuiltin(hook *CallbackHook) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.builtins = append(m.builtins, hook)
}

// RegisterExecutor adds or replaces an executor backend.
func (m *Manager) RegisterExecutor(executor Executor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors[executor.Type()] = executor
}

// GetBuiltin returns the builtin callback hook with the given name, or nil.
func (m *Manager) GetBuiltin(name string) *CallbackHook {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, b := range m.builtins {
		if b.Name == name {
			return b
		}
	}
	return nil
}

// ReloadConfig re-reads hook configuration files and replaces the active
// configuration. Executors and builtins are preserved.
func (m *Manager) ReloadConfig() error {
	layers, config, err := LoadHooksConfig(m.xbotHome, m.projectDir)
	if err != nil {
		return fmt.Errorf("reload hooks config: %w", err)
	}

	m.mu.Lock()
	m.layers = layers
	m.config = config
	m.mu.Unlock()

	return nil
}

// Emit dispatches the given event to all matching handlers and returns an
// aggregated Decision.
//
// Handler collection order:
//  1. Builtins (Go callbacks) — always match the current event.
//  2. Configured hooks — matched by event name → EventGroup matcher → per-hook
//     if-condition.
//
// Execution rules:
//   - At most 10 handlers per event (excess is truncated with a warning).
//   - Total timeout: 60 s.
//   - async handlers run in the background; their results do not participate
//     in the aggregation.
//   - command-type handlers are skipped when EnableCommandHooks is false.
//
// Decision aggregation priority: deny > defer > ask > allow.
func (m *Manager) Emit(ctx context.Context, event Event) (*Decision, error) {
	eventName := event.EventName()

	// Collect all matching handlers.
	type handler struct {
		builtin *CallbackHook
		def     *HookDef
	}

	m.mu.RLock()
	handlers := make([]handler, 0, len(m.builtins)+16)

	// 1. Builtins — always match.
	for _, b := range m.builtins {
		handlers = append(handlers, handler{builtin: b})
	}

	// 2. Config hooks — match by event name, matcher, and if-condition.
	groups := m.config.Hooks[eventName]
	for gi := range groups {
		group := &groups[gi]
		matcher := NewMatcher(group.Matcher)
		if !matcher.Match(event) {
			continue
		}
		for hi := range group.Hooks {
			def := &group.Hooks[hi]
			if !matcher.SetIf(def.If).MatchIf(event) {
				continue
			}
			handlers = append(handlers, handler{def: def})
		}
	}

	// Capture config flag for command check while holding the read lock.
	commandEnabled := m.config.EnableCommandHooks
	m.mu.RUnlock()

	// No handlers → allow immediately.
	if len(handlers) == 0 {
		return &Decision{Action: Allow}, nil
	}

	// Cap at 10 handlers.
	if len(handlers) > 10 {
		log.Warnf("hooks: event %s matched %d handlers, truncating to 10", eventName, len(handlers))
		handlers = handlers[:10]
	}

	// Apply total timeout.
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Execute handlers and collect decisions.
	var decisions []*Decision
	for _, h := range handlers {
		// Callback handler.
		if h.builtin != nil {
			result, err := h.builtin.Fn(ctx, event)
			if err != nil {
				log.Errorf("hooks: builtin %q error: %v", h.builtin.Name, err)
				continue
			}
			decisions = append(decisions, resultToDecision(result))
			continue
		}

		def := h.def

		// Command handler — check enabled flag.
		if def.Type == "command" && !commandEnabled {
			log.Debug("hooks: skipping command handler (EnableCommandHooks=false)")
			continue
		}

		// Async handler — fire and forget.
		if def.Async {
			go func(d *HookDef) {
				exec := m.getExecutor(d.Type)
				if exec == nil {
					log.Errorf("hooks: no executor for type %q", d.Type)
					return
				}
				_, err := exec.Execute(ctx, d, event)
				if err != nil {
					log.Errorf("hooks: async %s handler error: %v", d.Type, err)
				}
			}(def)
			continue
		}

		// Sync handler.
		exec := m.getExecutor(def.Type)
		if exec == nil {
			log.Errorf("hooks: no executor for type %q", def.Type)
			continue
		}
		result, err := exec.Execute(ctx, def, event)
		if err != nil {
			log.Errorf("hooks: %s handler error: %v", def.Type, err)
			continue
		}
		decisions = append(decisions, resultToDecision(result))
	}

	if len(decisions) == 0 {
		return &Decision{Action: Allow}, nil
	}

	return aggregateDecisions(decisions), nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// getExecutor returns the executor for the given type (read-locked).
func (m *Manager) getExecutor(handlerType string) Executor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executors[handlerType]
}

// resultToDecision converts a raw executor Result into a Decision.
func resultToDecision(r *Result) *Decision {
	if r == nil {
		return &Decision{Action: Allow}
	}
	return &Decision{
		Action:       ParseAction(r.Decision),
		Reason:       r.Reason,
		UpdatedInput: r.UpdatedInput,
		Context:      r.Context,
	}
}

// aggregateDecisions merges multiple decisions into one.
// Priority: deny > defer > ask > allow.
// Reason values are concatenated with "; ".
// UpdatedInput takes the first non-nil value.
// Context values are concatenated with newlines.
func aggregateDecisions(decisions []*Decision) *Decision {
	if len(decisions) == 0 {
		return &Decision{Action: Allow}
	}

	result := &Decision{Action: Allow}
	var reasons []string
	var contexts []string

	for _, d := range decisions {
		// Priority ordering: deny(1) > defer(3) > ask(2) > allow(0).
		switch {
		case d.Action == Deny:
			result.Action = Deny
		case d.Action == Defer && result.Action != Deny:
			result.Action = Defer
		case d.Action == Ask && result.Action != Deny && result.Action != Defer:
			result.Action = Ask
		case d.Action == Allow && result.Action == Allow:
			// keep allow
		}

		if d.Reason != "" {
			reasons = append(reasons, d.Reason)
		}
		if result.UpdatedInput == nil && d.UpdatedInput != nil {
			result.UpdatedInput = d.UpdatedInput
		}
		if d.Context != "" {
			contexts = append(contexts, d.Context)
		}
	}

	if len(reasons) > 0 {
		result.Reason = strings.Join(reasons, "; ")
	}
	if len(contexts) > 0 {
		result.Context = strings.Join(contexts, "\n")
	}

	return result
}
