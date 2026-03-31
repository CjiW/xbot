package channel

import (
	"strings"
	"testing"
	"time"

	"xbot/bus"
)

func sendProgress(model *cliModel, payload *CLIProgressPayload) {
	model.Update(cliProgressMsg{payload: payload})
}

func sendDone(model *cliModel, content string) {
	model.typing = false
	model.Update(cliOutboundMsg{
		msg: bus.OutboundMessage{
			Content:   content,
			IsPartial: false,
		},
	})
}

func assertCount(t *testing.T, label, haystack, needle string, expected int) {
	count := strings.Count(haystack, needle)
	if count != expected {
		t.Errorf("%s: expected '%s' x%d, got x%d", label, needle, expected, count)
	}
}

func countToolsInSummary(model *cliModel) int {
	for _, msg := range model.messages {
		if msg.role == "tool_summary" {
			if len(msg.iterations) > 0 {
				count := 0
				for _, it := range msg.iterations {
					count += len(it.Tools)
				}
				return count
			}
			return len(msg.tools)
		}
	}
	return 0
}

// Basic: 2 iterations, no final empty iteration
func TestProgressNoDuplication(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()

	sendProgress(model, &CLIProgressPayload{Phase: "thinking", Iteration: 0, Thinking: "A"})
	sendProgress(model, &CLIProgressPayload{
		Phase: "tool_exec", Iteration: 0, Thinking: "A",
		CompletedTools: []CLIToolProgress{
			{Name: "read", Label: "Read file", Status: "done", Elapsed: 1000},
		},
	})
	sendProgress(model, &CLIProgressPayload{Phase: "thinking", Iteration: 1, Thinking: "B"})
	sendProgress(model, &CLIProgressPayload{
		Phase: "tool_exec", Iteration: 1, Thinking: "B",
		CompletedTools: []CLIToolProgress{
			{Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 500},
		},
	})

	block := model.renderProgressBlock()
	assertCount(t, "Read file", block, "Read file", 1)
	assertCount(t, "Search pattern", block, "Search pattern", 1)
	assertCount(t, "Thinking A", block, "A", 1)
	assertCount(t, "Thinking B", block, "B", 1)

	sendDone(model, "Final answer")

	if model.renderProgressBlock() != "" {
		t.Error("Progress block should be empty after done")
	}
	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools in summary, got %d", tools)
	}
}

// Realistic: 2 iterations with 2+1 tools, then empty thinking iteration before done
func TestProgressRealisticSequence(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()

	// Iter 0
	sendProgress(model, &CLIProgressPayload{Phase: "thinking", Iteration: 0, Thinking: "Let me look"})
	sendProgress(model, &CLIProgressPayload{
		Phase: "tool_exec", Iteration: 0, Thinking: "Let me look",
		CompletedTools: []CLIToolProgress{
			{Name: "read", Label: "Read config", Status: "done", Elapsed: 500},
			{Name: "grep", Label: "Search pattern", Status: "done", Elapsed: 300},
		},
	})
	// Iter 1
	sendProgress(model, &CLIProgressPayload{Phase: "thinking", Iteration: 1, Thinking: "Based on results"})
	sendProgress(model, &CLIProgressPayload{
		Phase: "tool_exec", Iteration: 1, Thinking: "Based on results",
		CompletedTools: []CLIToolProgress{
			{Name: "edit", Label: "Fix bug", Status: "done", Elapsed: 200},
		},
	})
	// Iter 2: empty thinking (no tools) - this is the bug trigger
	sendProgress(model, &CLIProgressPayload{Phase: "thinking", Iteration: 2, Thinking: ""})

	block := model.renderProgressBlock()
	assertCount(t, "Read config total", block, "Read config", 1)
	assertCount(t, "Search pattern total", block, "Search pattern", 1)
	assertCount(t, "Fix bug total", block, "Fix bug", 1)

	sendDone(model, "Here is the fix.")

	if model.renderProgressBlock() != "" {
		t.Error("Progress block should be empty after done")
	}
	if tools := countToolsInSummary(model); tools != 3 {
		t.Errorf("Expected 3 tools in summary, got %d", tools)
	}
}

// Bug scenario: lastCompletedTools leaking across iterations
func TestLastCompletedToolsLeak(t *testing.T) {
	model := newCLIModel()
	model.handleResize(80, 24)
	model.typing = true
	model.typingStartTime = time.Now()

	// Iter 0: 1 tool
	sendProgress(model, &CLIProgressPayload{
		Phase: "tool_exec", Iteration: 0, Thinking: "A",
		CompletedTools: []CLIToolProgress{
			{Name: "read", Label: "Read", Status: "done", Elapsed: 100},
		},
	})
	// Iter 1: 1 tool
	sendProgress(model, &CLIProgressPayload{
		Phase: "tool_exec", Iteration: 1, Thinking: "B",
		CompletedTools: []CLIToolProgress{
			{Name: "edit", Label: "Edit", Status: "done", Elapsed: 200},
		},
	})
	// Iter 2: empty thinking (triggers iter 1 snapshot, should clear lastCompletedTools)
	sendProgress(model, &CLIProgressPayload{Phase: "thinking", Iteration: 2, Thinking: ""})

	// Verify lastCompletedTools was cleared after iter 1 snapshot
	if len(model.lastCompletedTools) != 0 {
		t.Errorf("lastCompletedTools should be empty after iter switch, got %d entries", len(model.lastCompletedTools))
	}

	sendDone(model, "Done")

	// Should have exactly 2 tools (Read + Edit), not 3 (no duplicate Edit)
	if tools := countToolsInSummary(model); tools != 2 {
		t.Errorf("Expected 2 tools (no leak), got %d", tools)
	}
}
