package tools

import (
	"context"
	"strings"
	"testing"
)

// withPerm is a test helper for injecting perm users into context.
func withPerm(ctx context.Context, defaultUser, privilegedUser string) context.Context {
	return WithPermUsers(ctx, defaultUser, privilegedUser)
}

func TestExtractRunAsAndReason(t *testing.T) {
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

func TestPopulateApprovalDetails(t *testing.T) {
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

func TestTruncateApprovalText(t *testing.T) {
	tests := []struct {
		input string
		max   int
		want  string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello..."},
		{"  hello  ", 10, "hello"},
		{"ab", 0, "ab"},
		{"abc", 3, "abc"},
	}

	for _, tt := range tests {
		got := truncateApprovalText(tt.input, tt.max)
		if got != tt.want {
			t.Errorf("truncateApprovalText(%q, %d) = %q, want %q", tt.input, tt.max, got, tt.want)
		}
	}
}

func TestIsPermControlActiveFromCtx(t *testing.T) {
	// Empty context — not active
	if isPermControlActiveFromCtx(context.Background()) {
		t.Error("expected inactive for empty context")
	}

	// Both empty — not active
	if isPermControlActiveFromCtx(withPerm(context.Background(), "", "")) {
		t.Error("expected inactive for both empty users")
	}

	// Default user only — active
	if !isPermControlActiveFromCtx(withPerm(context.Background(), "alice", "")) {
		t.Error("expected active for default user only")
	}

	// Privileged user only — active
	if !isPermControlActiveFromCtx(withPerm(context.Background(), "", "root")) {
		t.Error("expected active for privileged user only")
	}

	// Both — active
	if !isPermControlActiveFromCtx(withPerm(context.Background(), "alice", "root")) {
		t.Error("expected active for both users")
	}
}

func TestWorkingDirFromContext(t *testing.T) {
	// Empty context
	if dir := WorkingDirFromContext(context.Background()); dir != "" {
		t.Errorf("expected empty dir from empty context, got %q", dir)
	}

	// With working dir
	ctx := WithWorkingDir(context.Background(), "/home/user/project")
	if dir := WorkingDirFromContext(ctx); dir != "/home/user/project" {
		t.Errorf("expected /home/user/project, got %q", dir)
	}
}

