package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

func TestBuildSystemReminder_Basic(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi!"},
		{Role: "tool", Content: "Result"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Shell"}}, "", "main")

	if !strings.Contains(result, "<system-reminder>") {
		t.Error("expected system-reminder tag")
	}
	if !strings.Contains(result, "用户需求: Hello") {
		t.Errorf("expected user goal, got:\n%s", result)
	}
	if !strings.Contains(result, "已完成 1 次工具调用") {
		t.Errorf("expected tool count, got:\n%s", result)
	}
	if !strings.Contains(result, "Shell") {
		t.Errorf("expected tool name in reminder, got:\n%s", result)
	}
}

func TestBuildSystemReminder_SubAgent(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "Do task X"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Read"}}, "", "main/worker")

	if !strings.Contains(result, "执行任务: Do task X") {
		t.Errorf("SubAgent should show task prefix, got:\n%s", result)
	}
}

func TestBuildSystemReminder_WithTodo(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Read"}}, "2/5 完成", "main")

	if !strings.Contains(result, "TODO: 2/5 完成") {
		t.Errorf("expected TODO summary, got:\n%s", result)
	}
}

func TestBuildSystemReminder_NoContextEditHints(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "result"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Shell"}}, "", "main")

	if strings.Contains(result, "context_edit") {
		t.Errorf("should not contain context_edit hints, got:\n%s", result)
	}
}

func TestBuildSystemReminder_Empty(t *testing.T) {
	result := BuildSystemReminder(nil, nil, "", "main")
	if result != "" {
		t.Errorf("expected empty result for nil messages, got: %q", result)
	}
}

func TestBuildSystemReminder_GitCommitTriggersPostDev(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fix bug"},
	}

	// Shell with git commit should trigger post-dev reminder
	result := BuildSystemReminder(messages, []llm.ToolCall{{
		Name:      "Shell",
		Arguments: `{"command":"git commit -m \"fix: bug\" -a"}`,
	}}, "", "main")

	if !strings.Contains(result, "post-dev") {
		t.Errorf("expected post-dev reminder on git commit, got:\n%s", result)
	}
	if !strings.Contains(result, "git commit") {
		t.Errorf("expected git commit mention, got:\n%s", result)
	}
}

func TestBuildSystemReminder_NoPostDevWithoutGitCommit(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fix bug"},
	}

	// Shell without git commit should NOT trigger post-dev reminder
	result := BuildSystemReminder(messages, []llm.ToolCall{{
		Name:      "Shell",
		Arguments: `{"command":"go build ./..."}`,
	}}, "", "main")

	if strings.Contains(result, "post-dev") {
		t.Errorf("should not contain post-dev reminder without git commit, got:\n%s", result)
	}
	if strings.Contains(result, "knowledge-management") {
		t.Errorf("should not contain old knowledge-management reminder, got:\n%s", result)
	}
}
