package tools

import (
	"context"
	"strings"
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

func withPerm(ctx context.Context, defaultUser, privilegedUser string) context.Context {
	return WithPermUsers(ctx, defaultUser, privilegedUser)
}

func TestApprovalHook_Name(t *testing.T) {
	h := NewApprovalHook(&mockApprovalHandler{})
	if h.Name() != "approval" {
		t.Errorf("expected 'approval', got %q", h.Name())
	}
}

func TestApprovalHook_NoRunAs(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", "root"), "Shell", `{"command": "ls"}`)
	if err != nil {
		t.Errorf("expected no error for empty run_as, got %v", err)
	}
	if handler.called {
		t.Error("handler should not be called for empty run_as")
	}
}

func TestApprovalHook_DefaultUser(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", "root"), "Shell", `{"command": "ls", "run_as": "alice", "reason": "list directory"}`)
	if err != nil {
		t.Errorf("expected no error for default_user, got %v", err)
	}
	if handler.called {
		t.Error("handler should not be called for default_user")
	}
}

func TestApprovalHook_PrivilegedUser_Approved(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalResult{Approved: true}}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", "root"), "Shell", `{"command": "apt install nginx", "run_as": "root", "reason": "install package"}`)
	if err != nil {
		t.Errorf("expected no error for approved privileged_user, got %v", err)
	}
	if !handler.called {
		t.Error("handler should be called for privileged_user")
	}
}

func TestApprovalHook_PrivilegedUser_Denied(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalResult{Approved: false}}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", "root"), "Shell", `{"command": "apt install nginx", "run_as": "root", "reason": "install package"}`)
	if err == nil {
		t.Fatal("expected error for denied privileged_user")
	}
}

func TestApprovalHook_PrivilegedUser_DeniedWithReason(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalResult{Approved: false, DenyReason: "unsafe package source"}}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", "root"), "Shell", `{"command": "apt install nginx", "run_as": "root", "reason": "install package"}`)
	if err == nil {
		t.Fatal("expected error for denied privileged_user")
	}
	if !strings.Contains(err.Error(), "unsafe package source") {
		t.Fatalf("expected deny reason in error, got %v", err)
	}
}

func TestApprovalHook_UnknownUser(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", "root"), "Shell", `{"command": "ls", "run_as": "hacker"}`)
	if err == nil {
		t.Fatal("expected error for unknown user")
	}
}

func TestApprovalHook_FeatureDisabled(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler)
	// No perm users in context — feature disabled, stale run_as should be silently ignored
	err := h.PreToolUse(context.Background(), "Shell", `{"command": "ls", "run_as": "root"}`)
	if err != nil {
		t.Fatalf("expected nil when perm control is disabled (stale run_as should be ignored), got %v", err)
	}
}

func TestApprovalHook_EmptyPermUsers(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler)
	// Perm users in context but both empty — feature disabled, stale run_as should be ignored
	err := h.PreToolUse(withPerm(context.Background(), "", ""), "Shell", `{"command": "ls", "run_as": "root"}`)
	if err != nil {
		t.Fatalf("expected nil when perm users are both empty (stale run_as should be ignored), got %v", err)
	}
}

func TestApprovalHook_OnlyDefaultUser(t *testing.T) {
	handler := &mockApprovalHandler{}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "alice", ""), "Shell", `{"command": "ls", "run_as": "alice", "reason": "list directory"}`)
	if err != nil {
		t.Errorf("expected no error for default_user, got %v", err)
	}
	// run_as "root" should fail because privileged_user is not configured
	err = h.PreToolUse(withPerm(context.Background(), "alice", ""), "Shell", `{"command": "ls", "run_as": "root", "reason": "list root"}`)
	if err == nil {
		t.Fatal("expected error for run_as=root when privileged_user is empty")
	}
}

func TestApprovalHook_OnlyPrivilegedUser(t *testing.T) {
	handler := &mockApprovalHandler{result: ApprovalResult{Approved: true}}
	h := NewApprovalHook(handler)
	err := h.PreToolUse(withPerm(context.Background(), "", "root"), "Shell", `{"command": "ls", "run_as": "root", "reason": "list root directory"}`)
	if err != nil {
		t.Errorf("expected no error for approved privileged_user, got %v", err)
	}
}

func TestApprovalHook_ExtractRunAsAndReason(t *testing.T) {
	tests := []struct {
		args           string
		expectedRunAs  string
		expectedReason string
	}{
		{`{"command": "ls"}`, "", ""},
		{`{"command": "ls", "run_as": "root", "reason": "list root"}`, "root", "list root"},
		{`{}`, "", ""},
		{"invalid json", "", ""},
	}

	for _, tt := range tests {
		runAs, reason := extractRunAsAndReason(tt.args)
		if runAs != tt.expectedRunAs || reason != tt.expectedReason {
			t.Errorf("extractRunAsAndReason(%q) = (%q, %q), want (%q, %q)", tt.args, runAs, reason, tt.expectedRunAs, tt.expectedReason)
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
	if req.ArgsSummary != "apt install nginx" {
		t.Errorf("expected args summary to match command, got %q", req.ArgsSummary)
	}

	req2 := ApprovalRequest{RunAs: "root"}
	populateApprovalDetails(&req2, "FileCreate", `{"path": "/etc/test.conf"}`)
	if req2.FilePath != "/etc/test.conf" {
		t.Errorf("expected file path '/etc/test.conf', got %q", req2.FilePath)
	}

	req3 := ApprovalRequest{RunAs: "root"}
	populateApprovalDetails(&req3, "Shell", `{"command": "apt install nginx", "reason": "Install nginx for reverse proxy"}`)
	if req3.Reason != "Install nginx for reverse proxy" {
		t.Errorf("expected explicit reason, got %q", req3.Reason)
	}

	longCmd := "python -c '" + strings.Repeat("x", 300) + "'"
	req4 := ApprovalRequest{RunAs: "root"}
	populateApprovalDetails(&req4, "Shell", `{"command": "`+longCmd+`"}`)
	if len(req4.Command) > 160 {
		t.Fatalf("expected truncated command <= 160 chars, got %d", len(req4.Command))
	}
	if !strings.HasSuffix(req4.Command, "...") {
		t.Fatalf("expected truncated command to end with ellipsis, got %q", req4.Command)
	}
}

func TestApprovalHook_PostToolUse(t *testing.T) {
	h := NewApprovalHook(&mockApprovalHandler{})
	// PostToolUse should be a no-op — verify it doesn't panic
	h.PostToolUse(context.Background(), "Shell", "", nil, nil, 0)
}

func TestApprovalHook_NilHandler(t *testing.T) {
	h := NewApprovalHook(nil)
	ctx := withPerm(context.Background(), "", "root")
	err := h.PreToolUse(ctx, "Shell", `{"command": "ls", "run_as": "root"}`)
	if err == nil {
		t.Fatal("expected error when handler is nil and privileged_user requested")
	}
}

func TestPermUsersFromContext(t *testing.T) {
	// Empty context
	du, pu := PermUsersFromContext(context.Background())
	if du != "" || pu != "" {
		t.Errorf("expected empty users from empty context, got %q/%q", du, pu)
	}

	// With perm users
	ctx := WithPermUsers(context.Background(), "alice", "root")
	du, pu = PermUsersFromContext(ctx)
	if du != "alice" || pu != "root" {
		t.Errorf("expected alice/root, got %q/%q", du, pu)
	}
}
