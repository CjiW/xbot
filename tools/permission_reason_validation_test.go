package tools

import (
	"strings"
	"testing"
)

func TestShellTool_RunAsReasonPairValidation(t *testing.T) {
	tool := &ShellTool{}
	ctx := &ToolContext{}

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
	ctx := &ToolContext{}

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
	ctx := &ToolContext{}

	_, err := tool.Execute(ctx, `{"path":"/tmp/a.txt","old_string":"a","new_string":"b","run_as":"root"}`)
	if err == nil || !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("expected pair-validation error for run_as only, got %v", err)
	}

	_, err = tool.Execute(ctx, `{"path":"/tmp/a.txt","old_string":"a","new_string":"b","reason":"need root"}`)
	if err == nil || !strings.Contains(err.Error(), "run_as and reason must be provided together") {
		t.Fatalf("expected pair-validation error for reason only, got %v", err)
	}
}
