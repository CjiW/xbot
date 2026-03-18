package tools

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestLoggingHook_Name(t *testing.T) {
	h := NewLoggingHook()
	if h.Name() != "logging" {
		t.Fatalf("expected 'logging', got %q", h.Name())
	}
}

func TestLoggingHook_PreToolUse_NoError(t *testing.T) {
	h := NewLoggingHook()
	err := h.PreToolUse(context.Background(), "Shell", `{"command":"ls"}`)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestLoggingHook_PostToolUse(t *testing.T) {
	h := NewLoggingHook()
	// Should not panic with nil result
	h.PostToolUse(context.Background(), "Shell", "", nil, nil, 10*time.Millisecond)
	// Should not panic with error
	h.PostToolUse(context.Background(), "Shell", "", nil, &timeoutError{}, 5*time.Millisecond)
	// Should not panic with valid result
	h.PostToolUse(context.Background(), "Shell", "", &ToolResult{Summary: "ok"}, nil, 1*time.Millisecond)
}

func TestTimingHook_Name(t *testing.T) {
	h := NewTimingHook()
	if h.Name() != "timing" {
		t.Fatalf("expected 'timing', got %q", h.Name())
	}
}

func TestTimingHook_PreToolUse_NoError(t *testing.T) {
	h := NewTimingHook()
	err := h.PreToolUse(context.Background(), "Shell", "")
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestTimingHook_PostToolUse_Single(t *testing.T) {
	h := NewTimingHook()

	h.PostToolUse(context.Background(), "Shell", "", nil, nil, 100*time.Millisecond)

	stats := h.Stats()
	if len(stats) != 1 {
		t.Fatalf("expected 1 tool stat, got %d", len(stats))
	}

	s, ok := stats["Shell"]
	if !ok {
		t.Fatal("expected Shell stat")
	}
	if s.Count != 1 {
		t.Fatalf("expected count 1, got %d", s.Count)
	}
	if s.Total != 100*time.Millisecond {
		t.Fatalf("expected total 100ms, got %v", s.Total)
	}
	if s.Min != 100*time.Millisecond {
		t.Fatalf("expected min 100ms, got %v", s.Min)
	}
	if s.Max != 100*time.Millisecond {
		t.Fatalf("expected max 100ms, got %v", s.Max)
	}
	if s.Average != 100*time.Millisecond {
		t.Fatalf("expected avg 100ms, got %v", s.Average)
	}
}

func TestTimingHook_PostToolUse_Multiple(t *testing.T) {
	h := NewTimingHook()

	durations := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond}
	for _, d := range durations {
		h.PostToolUse(context.Background(), "Shell", "", nil, nil, d)
	}

	stats := h.Stats()
	s := stats["Shell"]
	if s.Count != 3 {
		t.Fatalf("expected count 3, got %d", s.Count)
	}
	if s.Total != 600*time.Millisecond {
		t.Fatalf("expected total 600ms, got %v", s.Total)
	}
	if s.Average != 200*time.Millisecond {
		t.Fatalf("expected avg 200ms, got %v", s.Average)
	}
	if s.Min != 100*time.Millisecond {
		t.Fatalf("expected min 100ms, got %v", s.Min)
	}
	if s.Max != 300*time.Millisecond {
		t.Fatalf("expected max 300ms, got %v", s.Max)
	}
}

func TestTimingHook_PostToolUse_MultipleTools(t *testing.T) {
	h := NewTimingHook()

	h.PostToolUse(context.Background(), "Shell", "", nil, nil, 50*time.Millisecond)
	h.PostToolUse(context.Background(), "Read", "", nil, nil, 10*time.Millisecond)
	h.PostToolUse(context.Background(), "Shell", "", nil, nil, 150*time.Millisecond)

	stats := h.Stats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(stats))
	}

	shell := stats["Shell"]
	if shell.Count != 2 {
		t.Fatalf("expected Shell count 2, got %d", shell.Count)
	}
	if shell.Min != 50*time.Millisecond {
		t.Fatalf("expected Shell min 50ms, got %v", shell.Min)
	}
	if shell.Max != 150*time.Millisecond {
		t.Fatalf("expected Shell max 150ms, got %v", shell.Max)
	}

	read := stats["Read"]
	if read.Count != 1 {
		t.Fatalf("expected Read count 1, got %d", read.Count)
	}
}

func TestTimingHook_Reset(t *testing.T) {
	h := NewTimingHook()
	h.PostToolUse(context.Background(), "Shell", "", nil, nil, 100*time.Millisecond)

	if len(h.Stats()) != 1 {
		t.Fatal("expected stats before reset")
	}

	h.Reset()

	if len(h.Stats()) != 0 {
		t.Fatal("expected no stats after reset")
	}
}

func TestTimingHook_ConcurrentSafety(t *testing.T) {
	h := NewTimingHook()

	var wg sync.WaitGroup
	// Concurrently write stats from multiple goroutines
	for i := 0; i < 1000; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			toolName := "Shell"
			if i%2 == 0 {
				toolName = "Read"
			}
			h.PostToolUse(context.Background(), toolName, "", nil, nil, time.Duration(i)*time.Microsecond)
		}(i)
	}
	wg.Wait()

	stats := h.Stats()
	if len(stats) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(stats))
	}

	shell := stats["Shell"]
	if shell.Count != 500 {
		t.Fatalf("expected Shell count 500, got %d", shell.Count)
	}

	read := stats["Read"]
	if read.Count != 500 {
		t.Fatalf("expected Read count 500, got %d", read.Count)
	}

	// Concurrent reads while writing
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := h.Stats()
			_ = s // just ensure no race/panic
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.PostToolUse(context.Background(), "Shell", "", nil, nil, time.Millisecond)
		}()
	}
	wg.Wait()
}

// timeoutError is a simple error for testing
type timeoutError struct{}

func (e *timeoutError) Error() string { return "timeout" }
