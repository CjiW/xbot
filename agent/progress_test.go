package agent

import (
	"testing"
	"time"
)

func TestProgressEventCreation(t *testing.T) {
	now := time.Now()
	event := &ProgressEvent{
		Lines: []string{"> thinking...", "> ⏳ Read(file.go) ..."},
		Structured: &StructuredProgress{
			Phase:     PhaseThinking,
			Iteration: 0,
			ActiveTools: []ToolProgress{
				{Name: "Read", Label: "Read(file.go)", Status: ToolRunning, Iteration: 0},
			},
			CompletedTools: nil,
			TokenUsage: &TokenUsageSnapshot{
				PromptTokens:     1000,
				CompletionTokens: 500,
				TotalTokens:      1500,
				CacheHitTokens:   200,
			},
		},
		Timestamp: now,
	}

	if event.Structured.Phase != PhaseThinking {
		t.Errorf("expected phase %q, got %q", PhaseThinking, event.Structured.Phase)
	}
	if len(event.Structured.ActiveTools) != 1 {
		t.Fatalf("expected 1 active tool, got %d", len(event.Structured.ActiveTools))
	}
	if event.Structured.ActiveTools[0].Name != "Read" {
		t.Errorf("expected tool name 'Read', got %q", event.Structured.ActiveTools[0].Name)
	}
	if event.Structured.TokenUsage.TotalTokens != 1500 {
		t.Errorf("expected total tokens 1500, got %d", event.Structured.TokenUsage.TotalTokens)
	}
	if !event.Timestamp.Equal(now) {
		t.Error("timestamp mismatch")
	}
}

func TestStructuredProgress_Phases(t *testing.T) {
	sp := &StructuredProgress{
		Phase:     PhaseThinking,
		Iteration: 1,
	}

	// Transition through phases
	sp.Phase = PhaseToolExec
	if sp.Phase != "tool_exec" {
		t.Errorf("expected 'tool_exec', got %q", sp.Phase)
	}

	sp.Phase = PhaseCompressing
	if sp.Phase != "compressing" {
		t.Errorf("expected 'compressing', got %q", sp.Phase)
	}

	sp.Phase = PhaseRetrying
	if sp.Phase != "retrying" {
		t.Errorf("expected 'retrying', got %q", sp.Phase)
	}

	sp.Phase = PhaseDone
	if sp.Phase != "done" {
		t.Errorf("expected 'done', got %q", sp.Phase)
	}
}

func TestToolProgress_StatusTransitions(t *testing.T) {
	tp := ToolProgress{
		Name:   "Shell",
		Label:  "Shell(ls -la)",
		Status: ToolPending,
	}

	if tp.Status != "pending" {
		t.Errorf("expected 'pending', got %q", tp.Status)
	}

	tp.Status = ToolRunning
	if tp.Status != "running" {
		t.Errorf("expected 'running', got %q", tp.Status)
	}

	tp.Status = ToolDone
	tp.Elapsed = 150 * time.Millisecond
	if tp.Status != "done" {
		t.Errorf("expected 'done', got %q", tp.Status)
	}
	if tp.Elapsed != 150*time.Millisecond {
		t.Errorf("expected 150ms, got %v", tp.Elapsed)
	}

	tp.Status = ToolError
	if tp.Status != "error" {
		t.Errorf("expected 'error', got %q", tp.Status)
	}
}

func TestProgressEvent_NilStructured(t *testing.T) {
	event := &ProgressEvent{
		Lines:     []string{"> done"},
		Timestamp: time.Now(),
	}
	if event.Structured != nil {
		t.Error("Structured should be nil")
	}
	if len(event.Lines) != 1 {
		t.Errorf("expected 1 line, got %d", len(event.Lines))
	}
}
