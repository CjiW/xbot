package tools

import (
	"context"
	"strings"
	"testing"
)

func TestApprovalHook_RejectsMissingReasonBeforeRequestingApproval(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalResult{Approved: true}}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", "root"), "Shell", `{"command": "ls", "run_as": "root"}`)
	if err == nil {
		t.Fatal("expected error for missing reason")
	}
	if !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("unexpected error: %v", err)
	}
	if handler.called {
		t.Fatal("approval handler should not be called when reason validation fails")
	}
}

func TestApprovalHook_RejectsReasonWithoutRunAsBeforeRequestingApproval(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalResult{Approved: true}}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", "root"), "Shell", `{"command": "ls", "reason": "just because"}`)
	if err == nil {
		t.Fatal("expected error for reason without run_as")
	}
	if !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("unexpected error: %v", err)
	}
	if handler.called {
		t.Fatal("approval handler should not be called when pair validation fails")
	}
}
