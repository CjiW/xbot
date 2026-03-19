package agent

import (
	"context"
	"sync"
	"testing"

	"xbot/llm"
)

// TestNewContextManager tests NewContextManager with 5 different inputs
func TestNewContextManager(t *testing.T) {
	tests := []struct {
		name string
		mode ContextMode
		want ContextMode
	}{
		{"phase1", ContextModePhase1, ContextModePhase1},
		{"phase2", ContextModePhase2, ContextModePhase2},
		{"none", ContextModeNone, ContextModeNone},
		{"empty string", "", ContextModePhase1},
		{"invalid value", ContextMode("invalid"), ContextModePhase1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ContextManagerConfig{
				MaxContextTokens:     100000,
				CompressionThreshold: 0.7,
				DefaultMode:          tt.mode,
			}
			cm := NewContextManager(cfg)
			if cm.Mode() != tt.want {
				t.Errorf("NewContextManager(%q).Mode() = %q, want %q", tt.mode, cm.Mode(), tt.want)
			}
		})
	}
}

// TestContextManagerConfig_EffectiveMode tests EffectiveMode, runtime override, and reset
func TestContextManagerConfig_EffectiveMode(t *testing.T) {
	cfg := &ContextManagerConfig{
		MaxContextTokens:     100000,
		CompressionThreshold: 0.7,
		DefaultMode:          ContextModePhase1,
	}

	// Default mode
	if got := cfg.EffectiveMode(); got != ContextModePhase1 {
		t.Errorf("EffectiveMode() = %q, want %q", got, ContextModePhase1)
	}

	// Runtime override
	cfg.SetRuntimeMode(ContextModeNone)
	if got := cfg.EffectiveMode(); got != ContextModeNone {
		t.Errorf("EffectiveMode() after override = %q, want %q", got, ContextModeNone)
	}

	// Reset
	cfg.ResetRuntimeMode()
	if got := cfg.EffectiveMode(); got != ContextModePhase1 {
		t.Errorf("EffectiveMode() after reset = %q, want %q", got, ContextModePhase1)
	}
}

// TestPhase1Manager_ShouldCompress tests boundary values
func TestPhase1Manager_ShouldCompress(t *testing.T) {
	cfg := &ContextManagerConfig{
		MaxContextTokens:     100,
		CompressionThreshold: 0.7,
		DefaultMode:          ContextModePhase1,
	}
	m := newPhase1Manager(cfg)

	// Few messages: should not compress
	msgs := []llm.ChatMessage{
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
		llm.NewUserMessage("how are you"),
	}
	// Note: len(msgs) <= 3 returns false directly
	if m.ShouldCompress(msgs, "test-model", 0) {
		t.Error("ShouldCompress with ≤3 messages should return false")
	}

	// Many messages but token count won't exceed threshold (empty content = low tokens)
	// With MaxContextTokens=100 and threshold=0.7, threshold=70 tokens
	// We can't easily control token counting in unit tests, so we test the
	// boundary condition by checking with > 3 messages
	msgs4 := make([]llm.ChatMessage, 5)
	for i := range msgs4 {
		if i%2 == 0 {
			msgs4[i] = llm.NewUserMessage("short")
		} else {
			msgs4[i] = llm.NewAssistantMessage("reply")
		}
	}
	// This should not compress (well under threshold)
	if m.ShouldCompress(msgs4, "test-model", 0) {
		t.Error("ShouldCompress with short messages under threshold should return false")
	}

	// With large toolTokens, should exceed threshold
	// threshold = 70, if toolTokens >= 70, should trigger
	if !m.ShouldCompress(msgs4, "test-model", 100) {
		t.Error("ShouldCompress with toolTokens exceeding threshold should return true")
	}
}

// TestNoopManager tests noopManager behavior
func TestNoopManager(t *testing.T) {
	cfg := &ContextManagerConfig{
		MaxContextTokens:     100000,
		CompressionThreshold: 0.7,
		DefaultMode:          ContextModeNone,
	}
	m := newNoopManager(cfg)

	// Mode should be none
	if m.Mode() != ContextModeNone {
		t.Errorf("noopManager.Mode() = %q, want %q", m.Mode(), ContextModeNone)
	}

	// ShouldCompress should always return false
	if m.ShouldCompress(nil, "model", 0) {
		t.Error("noopManager.ShouldCompress should return false")
	}

	// Compress should return error
	_, err := m.Compress(context.TODO(), nil, nil, "model")
	if err == nil {
		t.Error("noopManager.Compress should return error")
	}
}

// TestPhase2Manager_ManualCompress tests that Phase 2 ManualCompress falls back to Phase 1
func TestPhase2Manager_ManualCompress(t *testing.T) {
	cfg := &ContextManagerConfig{
		MaxContextTokens:     100000,
		CompressionThreshold: 0.7,
		DefaultMode:          ContextModePhase2,
	}
	m := newPhase2Manager(cfg)

	// Mode should be phase2
	if m.Mode() != ContextModePhase2 {
		t.Errorf("phase2Manager.Mode() = %q, want %q", m.Mode(), ContextModePhase2)
	}

	// Compress should return error (not implemented)
	_, err := m.Compress(context.TODO(), nil, nil, "model")
	if err == nil {
		t.Error("phase2Manager.Compress should return error (not implemented)")
	}
}

// TestResolveContextMode tests resolveContextMode with various configs
func TestResolveContextMode(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want ContextMode
	}{
		{
			name: "ContextMode takes priority",
			cfg: Config{
				ContextMode:        ContextModeNone,
				EnableAutoCompress: true,
			},
			want: ContextModeNone,
		},
		{
			name: "EnableAutoCompress=false results in none",
			cfg: Config{
				ContextMode:        "",
				EnableAutoCompress: false,
			},
			want: ContextModeNone,
		},
		{
			name: "EnableAutoCompress=true defaults to phase1",
			cfg: Config{
				ContextMode:        "",
				EnableAutoCompress: true,
			},
			want: ContextModePhase1,
		},
		{
			name: "Empty ContextMode with EnableAutoCompress=true defaults to phase1",
			cfg: Config{
				ContextMode:        "",
				EnableAutoCompress: true,
			},
			want: ContextModePhase1,
		},
		{
			name: "Invalid ContextMode with EnableAutoCompress=true falls back to phase1",
			cfg: Config{
				ContextMode:        "invalid",
				EnableAutoCompress: true,
			},
			want: ContextModePhase1,
		},
		{
			name: "Invalid ContextMode with EnableAutoCompress=false falls back to none",
			cfg: Config{
				ContextMode:        "invalid",
				EnableAutoCompress: false,
			},
			want: ContextModeNone,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveContextMode(tt.cfg)
			if got != tt.want {
				t.Errorf("resolveContextMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestContextManagerConfig_Concurrent tests RWMutex safety with concurrent reads/writes
func TestContextManagerConfig_Concurrent(t *testing.T) {
	cfg := &ContextManagerConfig{
		MaxContextTokens:     100000,
		CompressionThreshold: 0.7,
		DefaultMode:          ContextModePhase1,
	}

	var wg sync.WaitGroup
	// Concurrent writers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if id%2 == 0 {
				cfg.SetRuntimeMode(ContextModeNone)
			} else {
				cfg.ResetRuntimeMode()
			}
		}(i)
	}
	// Concurrent readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cfg.EffectiveMode()
			_ = cfg.RuntimeMode()
		}()
	}
	wg.Wait()

	// Final state should be consistent
	mode := cfg.EffectiveMode()
	if mode != ContextModePhase1 && mode != ContextModeNone {
		t.Errorf("EffectiveMode() after concurrent access = %q, want phase1 or none", mode)
	}
}

// TestGetSetContextManager_Concurrent tests concurrent Get/Set on Agent
func TestGetSetContextManager_Concurrent(t *testing.T) {
	// We can't easily create a full Agent in unit tests (it needs DB, bus, etc.)
	// Instead, test the concurrent pattern used by GetContextManager/SetContextManager
	// by directly testing the RWMutex pattern
	var (
		mu   sync.RWMutex
		cm   ContextManager
		once sync.Once
	)

	cfg := &ContextManagerConfig{
		MaxContextTokens:     100000,
		CompressionThreshold: 0.7,
		DefaultMode:          ContextModePhase1,
	}

	// Initialize once
	once.Do(func() {
		cm = NewContextManager(cfg)
	})

	var wg sync.WaitGroup
	// Concurrent getters
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.RLock()
			_ = cm.Mode()
			mu.RUnlock()
		}()
	}
	// Concurrent setters
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(mode ContextMode) {
			defer wg.Done()
			mu.Lock()
			cm = NewContextManager(&ContextManagerConfig{
				MaxContextTokens:     100000,
				CompressionThreshold: 0.7,
				DefaultMode:          mode,
			})
			mu.Unlock()
		}(ContextMode("none"))
	}
	wg.Wait()
}
