package tools

import (
	"context"
	"strings"
	"testing"
)

func TestShellTool_RunAsReasonPairValidation(t *testing.T) {
	tool := &ShellTool{}
	// Must have perm control enabled for pair validation to apply
	permCtx := WithPermUsers(context.Background(), "alice", "root")
	ctx := &ToolContext{Ctx: permCtx}

	_, err := tool.Execute(ctx, `{"command":"whoami","run_as":"root"}`)
	if err == nil || !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("expected pair-validation error for run_as only, got %v", err)
	}

	_, err = tool.Execute(ctx, `{"command":"whoami","reason":"need root"}`)
	if err == nil || !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("expected pair-validation error for reason only, got %v", err)
	}
}

func TestFileCreateTool_RunAsReasonPairValidation(t *testing.T) {
	tool := &FileCreateTool{}
	permCtx := WithPermUsers(context.Background(), "alice", "root")
	ctx := &ToolContext{Ctx: permCtx}

	_, err := tool.Execute(ctx, `{"path":"/tmp/a.txt","content":"x","run_as":"root"}`)
	if err == nil || !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("expected pair-validation error for run_as only, got %v", err)
	}

	_, err = tool.Execute(ctx, `{"path":"/tmp/a.txt","content":"x","reason":"need root"}`)
	if err == nil || !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("expected pair-validation error for reason only, got %v", err)
	}
}

func TestFileReplaceTool_RunAsReasonPairValidation(t *testing.T) {
	tool := &FileReplaceTool{}
	permCtx := WithPermUsers(context.Background(), "alice", "root")
	ctx := &ToolContext{Ctx: permCtx}

	_, err := tool.Execute(ctx, `{"path":"/tmp/a.txt","old_string":"a","new_string":"b","run_as":"root"}`)
	if err == nil || !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("expected pair-validation error for run_as only, got %v", err)
	}

	_, err = tool.Execute(ctx, `{"path":"/tmp/a.txt","old_string":"a","new_string":"b","reason":"need root"}`)
	if err == nil || !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("expected pair-validation error for reason only, got %v", err)
	}
}

// TestPermDisabledIgnoresStaleRunAs verifies that stale run_as/reason from
// cached LLM context are silently ignored when permission control is disabled.
func TestPermDisabledIgnoresStaleRunAs(t *testing.T) {
	shellTool := &ShellTool{}

	// No perm users → perm control disabled
	ctx := &ToolContext{Ctx: context.Background(), WorkingDir: "/tmp"}

	// Shell: stale run_as alone should be stripped, not cause pair-validation error
	_, err := shellTool.Execute(ctx, `{"command":"echo hi","run_as":"root"}`)
	// Execution may fail for other reasons (sandbox etc), but NOT pair-validation
	if err != nil && strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("stale run_as should be ignored when perm control is disabled, got %v", err)
	}

	// Shell: stale reason alone should be stripped too
	_, err = shellTool.Execute(ctx, `{"command":"echo hi","reason":"need root"}`)
	if err != nil && strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("stale reason should be ignored when perm control is disabled, got %v", err)
	}
}
