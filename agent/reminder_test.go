package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

func TestBuildSystemReminder_ContextEditHints(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi!"},
		{Role: "tool", Content: "Result"},
	}

	tests := []struct {
		name       string
		rc         *ReminderContext
		wantPhase1 bool // expect gentle hint
		wantPhase2 bool // expect strong warning
	}{
		{
			name:       "nil context - no hint",
			rc:         nil,
			wantPhase1: false,
			wantPhase2: false,
		},
		{
			name:       "zero max tokens - no hint",
			rc:         &ReminderContext{MaxContextTokens: 0},
			wantPhase1: false,
			wantPhase2: false,
		},
		{
			name:       "below 40% - no hint",
			rc:         &ReminderContext{MaxContextTokens: 100000, UsedTokens: 30000, ToolDefTokens: 5000},
			wantPhase1: false,
			wantPhase2: false,
		},
		{
			name:       "exactly 40% - phase 1 hint",
			rc:         &ReminderContext{MaxContextTokens: 100000, UsedTokens: 35000, ToolDefTokens: 5000},
			wantPhase1: true,
			wantPhase2: false,
		},
		{
			name:       "50% - phase 1 hint",
			rc:         &ReminderContext{MaxContextTokens: 100000, UsedTokens: 40000, ToolDefTokens: 10000},
			wantPhase1: true,
			wantPhase2: false,
		},
		{
			name:       "exactly 60% - phase 2 warning",
			rc:         &ReminderContext{MaxContextTokens: 100000, UsedTokens: 50000, ToolDefTokens: 10000},
			wantPhase1: false,
			wantPhase2: true,
		},
		{
			name:       "80% - phase 2 warning",
			rc:         &ReminderContext{MaxContextTokens: 100000, UsedTokens: 60000, ToolDefTokens: 20000},
			wantPhase1: false,
			wantPhase2: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildSystemReminder(messages, []string{"Shell"}, "", "main", tt.rc)
			hasPhase1 := strings.Contains(result, "context_edit") && !strings.Contains(result, "⚠️")
			hasPhase2 := strings.Contains(result, "⚠️")

			if tt.wantPhase1 && !hasPhase1 {
				t.Errorf("expected phase 1 hint, got:\n%s", result)
			}
			if !tt.wantPhase1 && !tt.wantPhase2 && strings.Contains(result, "context_edit") {
				t.Errorf("unexpected context_edit hint, got:\n%s", result)
			}
			if tt.wantPhase2 && !hasPhase2 {
				t.Errorf("expected phase 2 warning, got:\n%s", result)
			}
			if !tt.wantPhase2 && hasPhase2 {
				t.Errorf("unexpected phase 2 warning, got:\n%s", result)
			}
		})
	}
}

func TestBuildSystemReminder_Phase2ContainsTokenInfo(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}

	rc := &ReminderContext{
		MaxContextTokens: 100000,
		UsedTokens:       60000,
		ToolDefTokens:    20000,
	}

	result := BuildSystemReminder(messages, []string{"Read"}, "", "main", rc)

	if !strings.Contains(result, "80%") {
		t.Errorf("phase 2 should contain percentage, got:\n%s", result)
	}
	if !strings.Contains(result, "80000") {
		t.Errorf("phase 2 should contain token count, got:\n%s", result)
	}
	if !strings.Contains(result, "context_edit action=list") {
		t.Errorf("phase 2 should guide specific actions, got:\n%s", result)
	}
}

func TestBuildSystemReminder_Phase1Content(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}

	rc := &ReminderContext{
		MaxContextTokens: 100000,
		UsedTokens:       35000,
		ToolDefTokens:    10000,
	}

	result := BuildSystemReminder(messages, []string{"Read"}, "", "main", rc)

	// Phase 1 should mention context_edit but NOT have the warning emoji
	if !strings.Contains(result, "context_edit") {
		t.Error("phase 1 should mention context_edit")
	}
	if strings.Contains(result, "⚠️") {
		t.Error("phase 1 should NOT contain warning emoji")
	}
	if !strings.Contains(result, "45%") {
		t.Error("phase 1 should mention percentage")
	}
}
