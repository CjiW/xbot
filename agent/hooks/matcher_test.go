package hooks

import (
	"testing"
)

// ---------------------------------------------------------------------------
// 1. TestMatcher_MatchAll_Wildcard
// ---------------------------------------------------------------------------

func TestMatcher_MatchAll_Wildcard(t *testing.T) {
	m := NewMatcher("*")
	ev := &PreToolUseEvent{ToolName_: "Shell", ToolInput_: map[string]any{"command": "ls"}}
	if !m.Match(ev) {
		t.Error("expected '*' to match all events")
	}

	// Also matches non-tool events
	ev2 := &SessionStartEvent{}
	if !m.Match(ev2) {
		t.Error("expected '*' to match non-tool events too")
	}
}

// ---------------------------------------------------------------------------
// 2. TestMatcher_MatchAll_Empty
// ---------------------------------------------------------------------------

func TestMatcher_MatchAll_Empty(t *testing.T) {
	m := NewMatcher("")
	ev := &PreToolUseEvent{ToolName_: "Shell", ToolInput_: map[string]any{"command": "ls"}}
	if !m.Match(ev) {
		t.Error("expected empty pattern to match all events")
	}
}

// ---------------------------------------------------------------------------
// 3. TestMatcher_ExactMatch
// ---------------------------------------------------------------------------

func TestMatcher_ExactMatch(t *testing.T) {
	m := NewMatcher("Shell")

	// Matches
	ev := &PreToolUseEvent{ToolName_: "Shell", ToolInput_: map[string]any{"command": "ls"}}
	if !m.Match(ev) {
		t.Error("expected exact match for Shell")
	}

	// Does not match
	ev2 := &PreToolUseEvent{ToolName_: "FileCreate", ToolInput_: map[string]any{"path": "a.go"}}
	if m.Match(ev2) {
		t.Error("expected no match for FileCreate")
	}
}

// ---------------------------------------------------------------------------
// 4. TestMatcher_MultiMatch
// ---------------------------------------------------------------------------

func TestMatcher_MultiMatch(t *testing.T) {
	m := NewMatcher("Shell|FileCreate")

	ev1 := &PreToolUseEvent{ToolName_: "Shell", ToolInput_: map[string]any{"command": "ls"}}
	if !m.Match(ev1) {
		t.Error("expected match for Shell in Shell|FileCreate")
	}

	ev2 := &PreToolUseEvent{ToolName_: "FileCreate", ToolInput_: map[string]any{"path": "a.go"}}
	if !m.Match(ev2) {
		t.Error("expected match for FileCreate in Shell|FileCreate")
	}

	ev3 := &PreToolUseEvent{ToolName_: "Grep", ToolInput_: map[string]any{"pattern": "foo"}}
	if m.Match(ev3) {
		t.Error("expected no match for Grep in Shell|FileCreate")
	}
}

// ---------------------------------------------------------------------------
// 5. TestMatcher_RegexMatch
// ---------------------------------------------------------------------------

func TestMatcher_RegexMatch(t *testing.T) {
	// Test "^mcp__"
	m1 := NewMatcher("^mcp__")
	ev1 := &PreToolUseEvent{ToolName_: "mcp__github", ToolInput_: map[string]any{}}
	if !m1.Match(ev1) {
		t.Error("expected ^mcp__ to match mcp__github")
	}
	ev1b := &PreToolUseEvent{ToolName_: "Shell", ToolInput_: map[string]any{}}
	if m1.Match(ev1b) {
		t.Error("expected ^mcp__ not to match Shell")
	}

	// Test "File.*"
	m2 := NewMatcher("File.*")
	ev2 := &PreToolUseEvent{ToolName_: "FileCreate", ToolInput_: map[string]any{}}
	if !m2.Match(ev2) {
		t.Error("expected File.* to match FileCreate")
	}
	ev2b := &PreToolUseEvent{ToolName_: "FileReplace", ToolInput_: map[string]any{}}
	if !m2.Match(ev2b) {
		t.Error("expected File.* to match FileReplace")
	}
}

// ---------------------------------------------------------------------------
// 6. TestMatcher_RegexPrecompiled
// ---------------------------------------------------------------------------

func TestMatcher_RegexPrecompiled(t *testing.T) {
	// Regex patterns are compiled at NewMatcher time
	m := NewMatcher("^mcp__")
	if m.regex == nil {
		t.Error("expected regex to be pre-compiled")
	}
	if m.exact != nil {
		t.Error("expected no exact list for regex pattern")
	}
	if m.matchAll {
		t.Error("expected matchAll=false for regex pattern")
	}
}

// ---------------------------------------------------------------------------
// 7. TestMatcher_IfCondition_Shell
// ---------------------------------------------------------------------------

func TestMatcher_IfCondition_Shell(t *testing.T) {
	m := NewMatcher("Shell")
	m.SetIf("Shell(rm *)")

	// Match: Shell with command containing "rm -rf /"
	ev := &PreToolUseEvent{
		ToolName_: "Shell",
		ToolInput_: map[string]any{
			"command": "rm -rf /",
		},
	}
	if !m.Match(ev) {
		t.Error("expected Shell to match pattern")
	}
	if !m.MatchIf(ev) {
		t.Error("expected Shell(rm *) to match event with command 'rm -rf /'")
	}

	// No match: Shell but command doesn't match "rm *"
	ev2 := &PreToolUseEvent{
		ToolName_: "Shell",
		ToolInput_: map[string]any{
			"command": "ls -la",
		},
	}
	if !m.Match(ev2) {
		t.Error("expected Shell to match pattern")
	}
	if m.MatchIf(ev2) {
		t.Error("expected Shell(rm *) not to match event with command 'ls -la'")
	}
}

// ---------------------------------------------------------------------------
// 8. TestMatcher_IfCondition_FileReplace
// ---------------------------------------------------------------------------

func TestMatcher_IfCondition_FileReplace(t *testing.T) {
	m := NewMatcher("FileReplace")
	m.SetIf("FileReplace(*.go)")

	ev := &PreToolUseEvent{
		ToolName_: "FileReplace",
		ToolInput_: map[string]any{
			"path":       "src/main.go",
			"old_string": "foo",
			"new_string": "bar",
		},
	}
	if !m.MatchIf(ev) {
		t.Error("expected FileReplace(*.go) to match path 'src/main.go'")
	}

	// Non-.go file should not match
	ev2 := &PreToolUseEvent{
		ToolName_: "FileReplace",
		ToolInput_: map[string]any{
			"path":       "src/main.ts",
			"old_string": "foo",
			"new_string": "bar",
		},
	}
	if m.MatchIf(ev2) {
		t.Error("expected FileReplace(*.go) not to match path 'src/main.ts'")
	}
}

// ---------------------------------------------------------------------------
// 9. TestMatcher_IfCondition_NoMatch
// ---------------------------------------------------------------------------

func TestMatcher_IfCondition_NoMatch(t *testing.T) {
	m := NewMatcher("Shell")
	m.SetIf("Shell(rm *)")

	// Wrong tool name → if condition should fail
	ev := &PreToolUseEvent{
		ToolName_: "FileCreate",
		ToolInput_: map[string]any{
			"path": "rm something",
		},
	}
	if m.MatchIf(ev) {
		t.Error("expected if condition to fail for wrong tool name")
	}
}

// ---------------------------------------------------------------------------
// 10. TestMatcher_IfCondition_Empty
// ---------------------------------------------------------------------------

func TestMatcher_IfCondition_Empty(t *testing.T) {
	m := NewMatcher("Shell")
	// No SetIf call → ifCond is empty

	ev := &PreToolUseEvent{
		ToolName_: "Shell",
		ToolInput_: map[string]any{"command": "ls"},
	}
	if !m.MatchIf(ev) {
		t.Error("expected empty if condition to always return true")
	}
}

// ---------------------------------------------------------------------------
// 11. TestMatcher_NonToolEvent
// ---------------------------------------------------------------------------

func TestMatcher_NonToolEvent(t *testing.T) {
	// Exact pattern should not match non-tool events (ToolName() == "")
	m := NewMatcher("Shell")
	ev := &SessionStartEvent{}
	if m.Match(ev) {
		t.Error("expected exact matcher not to match non-tool event")
	}

	// Regex pattern should not match empty tool name
	m2 := NewMatcher("^Shell")
	if m2.Match(ev) {
		t.Error("expected regex ^Shell not to match empty tool name")
	}

	// Match-all should still match
	m3 := NewMatcher("*")
	if !m3.Match(ev) {
		t.Error("expected match-all to match non-tool event")
	}
}
