package tools

import (
	"context"
	"testing"
)

// mockApprovalHandler is a test implementation of ApprovalHandler.
type mockApprovalHandler struct {
	result  ApprovalResult
	err     error
	called  bool
	lastReq ApprovalRequest
}

func (m *mockApprovalHandler) RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResult, error) {
	m.called = true
	m.lastReq = req
	return m.result, m.err
}

func TestApprovalHook_Name(t *testing.T) {
	h := NewApprovalHook(&mockApprovalHandler{}, "alice", "root")
	if h.Name() != "approval" {
		t.Errorf("expected 'approval', got %q", h.Name())
	}
}

func TestApprovalHook_NoRunAs(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler, "alice", "root")
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "ls"}`)
	if err != nil {
		t.Errorf("expected no error for empty run_as, got %v", err)
	}
	if handler.called {
		t.Error("handler should not be called for empty run_as")
	}
}

func TestApprovalHook_DefaultUser(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler, "alice", "root")
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "ls", "run_as": "alice"}`)
	if err != nil {
		t.Errorf("expected no error for default_user, got %v", err)
	}
	if handler.called {
		t.Error("handler should not be called for default_user")
	}
}

func TestApprovalHook_PrivilegedUser_Approved(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalApproved}
	h := NewApprovalHook(handler, "alice", "root")
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "apt install nginx", "run_as": "root"}`)
	if err != nil {
		t.Errorf("expected no error for approved privileged_user, got %v", err)
	}
	if !handler.called {
		t.Error("handler should be called for privileged_user")
	}
}

func TestApprovalHook_PrivilegedUser_Denied(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalDenied}
	h := NewApprovalHook(handler, "alice", "root")
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "apt install nginx", "run_as": "root"}`)
	if err == nil {
		t.Fatal("expected error for denied privileged_user")
	}
}

func TestApprovalHook_UnknownUser(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler, "alice", "root")
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "ls", "run_as": "hacker"}`)
	if err == nil {
		t.Fatal("expected error for unknown user")
	}
}

func TestApprovalHook_FeatureDisabled(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler, "", "") // feature disabled
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "ls", "run_as": "root"}`)
	if err == nil {
		t.Fatal("expected error when feature is disabled")
	}
}

func TestApprovalHook_OnlyDefaultUser(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler, "alice", "") // only default user, no privileged
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "ls", "run_as": "alice"}`)
	if err != nil {
		t.Errorf("expected no error for default_user, got %v", err)
	}
	// run_as "root" should fail because privileged_user is not configured
	err = h.PreToolUse(context.Background(), "Shell", `{"command": "ls", "run_as": "root"}`)
	if err == nil {
		t.Fatal("expected error for run_as=root when privileged_user is empty")
	}
}

func TestApprovalHook_OnlyPrivilegedUser(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalApproved}
	h := NewApprovalHook(handler, "", "root") // only privileged user
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "ls", "run_as": "root"}`)
	if err != nil {
		t.Errorf("expected no error for approved privileged_user, got %v", err)
	}
}

func TestApprovalHook_ExtractRunAs(t *testing.T) {
	tests := []struct {
		args     string
		expected string
	}{
		{`{"command": "ls"}`, ""},
		{`{"command": "ls", "run_as": "root"}`, "root"},
		{`{}`, ""},
		{"invalid json", ""},
	}

	for _, tt := range tests {
		got := extractRunAs(tt.args)
		if got != tt.expected {
			t.Errorf("extractRunAs(%q) = %q, want %q", tt.args, got, tt.expected)
		}
	}
}

func TestApprovalHook_PopulateDetails(t *testing.T) {
	req := ApprovalRequest{RunAs: "root"}
	populateApprovalDetails(&req, "Shell", `{"command": "apt install nginx"}`)
	if req.Command != "apt install nginx" {
		t.Errorf("expected command 'apt install nginx', got %q", req.Command)
	}
	if req.Reason == "" {
		t.Error("expected non-empty reason")
	}

	req2 := ApprovalRequest{RunAs: "root"}
	populateApprovalDetails(&req2, "FileCreate", `{"path": "/etc/test.conf"}`)
	if req2.FilePath != "/etc/test.conf" {
		t.Errorf("expected file path '/etc/test.conf', got %q", req2.FilePath)
	}
}

func TestApprovalHook_PostToolUse(t *testing.T) {
	h := NewApprovalHook(&mockApprovalHandler{}, "alice", "root")
	// PostToolUse should be a no-op — verify it doesn't panic
	h.PostToolUse(context.Background(), "Shell", "", nil, nil, 0)
}
