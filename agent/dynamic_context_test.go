package agent

import (
	"testing"

	"xbot/llm"
)

func TestDynamicContextInjector_FirstCall_NoInject(t *testing.T) {
	var currentDir = "/workspace"
	injector := NewDynamicContextInjector(func() string { return currentDir })

	messages := []llm.ChatMessage{
		{Role: "tool", Content: "some tool result"},
	}

	injected := injector.InjectIfNeeded(messages)
	if injected {
		t.Error("first call should not inject")
	}
	if messages[0].Content != "some tool result" {
		t.Errorf("first call should not modify message, got: %s", messages[0].Content)
	}
}

func TestDynamicContextInjector_NoChange_NoInject(t *testing.T) {
	currentDir := "/workspace"
	injector := NewDynamicContextInjector(func() string { return currentDir })

	// First call: record baseline
	injector.InjectIfNeeded(nil)

	// Second call: no change
	messages := []llm.ChatMessage{
		{Role: "tool", Content: "tool result"},
	}
	injected := injector.InjectIfNeeded(messages)
	if injected {
		t.Error("no change should not inject")
	}
}

func TestDynamicContextInjector_CWDChanged_Injects(t *testing.T) {
	currentDir := "/workspace"
	injector := NewDynamicContextInjector(func() string { return currentDir })

	// First call: record baseline
	injector.InjectIfNeeded(nil)

	// Simulate Cd tool changing the directory
	currentDir = "/workspace/subdir"

	messages := []llm.ChatMessage{
		{Role: "tool", Content: "changed directory"},
	}
	injected := injector.InjectIfNeeded(messages)
	if !injected {
		t.Error("CWD change should trigger injection")
	}
	if !contains(messages[0].Content, "<dynamic-context>") {
		t.Errorf("message should contain <dynamic-context>, got: %s", messages[0].Content)
	}
	if !contains(messages[0].Content, "/workspace/subdir") {
		t.Errorf("message should contain new directory, got: %s", messages[0].Content)
	}
}

func TestDynamicContextInjector_MultipleChanges(t *testing.T) {
	currentDir := "/workspace"
	injector := NewDynamicContextInjector(func() string { return currentDir })

	// Round 1: baseline
	injector.InjectIfNeeded(nil)

	// Round 2: change to /workspace/a
	currentDir = "/workspace/a"
	msgs1 := []llm.ChatMessage{{Role: "tool", Content: "result1"}}
	if !injector.InjectIfNeeded(msgs1) {
		t.Error("should inject on first change")
	}

	// Round 3: same dir, no change
	msgs2 := []llm.ChatMessage{{Role: "tool", Content: "result2"}}
	if injector.InjectIfNeeded(msgs2) {
		t.Error("should not inject when CWD unchanged")
	}

	// Round 4: change to /workspace/a/b
	currentDir = "/workspace/a/b"
	msgs3 := []llm.ChatMessage{{Role: "tool", Content: "result3"}}
	if !injector.InjectIfNeeded(msgs3) {
		t.Error("should inject on second change")
	}
	if !contains(msgs3[0].Content, "/workspace/a/b") {
		t.Errorf("should contain latest directory, got: %s", msgs3[0].Content)
	}
}

func TestDynamicContextInjector_EmptyMessages(t *testing.T) {
	currentDir := "/workspace"
	injector := NewDynamicContextInjector(func() string { return currentDir })
	injector.InjectIfNeeded(nil) // baseline

	currentDir = "/workspace/changed"
	injected := injector.InjectIfNeeded(nil) // empty messages
	// Should not panic, injected is true but no message to modify
	if !injected {
		t.Error("should report injected=true even with empty messages")
	}
}

func TestDynamicContextInjector_SubAgentScenario(t *testing.T) {
	// SubAgent: getCWD returns fixed InitialCWD (never changes)
	fixedDir := "/workspace/project"
	injector := NewDynamicContextInjector(func() string { return fixedDir })

	messages := []llm.ChatMessage{{Role: "tool", Content: "result"}}
	injector.InjectIfNeeded(messages) // baseline

	injected := injector.InjectIfNeeded(messages) // no change
	if injected {
		t.Error("SubAgent with fixed CWD should never inject")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}
