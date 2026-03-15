package agent

import (
	"testing"
)

func TestCommandRegistry_Match(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	tests := []struct {
		input   string
		wantCmd string // expected Name(), or "" for no match
	}{
		// Exact matches
		{"/new", "/new"},
		{"/version", "/version"},
		{"/help", "/help"},
		{"/llm", "/llm"},

		// Case insensitive
		{"/NEW", "/new"},
		{"/Version", "/version"},
		{"/HELP", "/help"},
		{"/LLM", "/llm"},

		// With leading/trailing whitespace
		{"  /new  ", "/new"},
		{" /version ", "/version"},

		// Prefix commands
		{"/prompt", "/prompt"},
		{"/prompt show me the system prompt", "/prompt"},
		{"/set-llm provider=openai", "/set-llm"},
		{"/set-llm", "/set-llm"},

		// Bang commands
		{"!ls", "!"},
		{"!echo hello world", "!"},
		{"! ls -la", "!"},

		// Not commands
		{"hello", ""},
		{"", ""},
		{"new", ""},
		{"version", ""},
		{"!", ""},           // bare ! is not a bang command
		{"!! echo hi", "!"}, // !! is valid (command is "! echo hi")
	}

	for _, tt := range tests {
		cmd := r.Match(tt.input)
		if tt.wantCmd == "" {
			if cmd != nil {
				t.Errorf("Match(%q) = %q, want nil", tt.input, cmd.Name())
			}
		} else {
			if cmd == nil {
				t.Errorf("Match(%q) = nil, want %q", tt.input, tt.wantCmd)
			} else if cmd.Name() != tt.wantCmd {
				t.Errorf("Match(%q) = %q, want %q", tt.input, cmd.Name(), tt.wantCmd)
			}
		}
	}
}

func TestCommandRegistry_IsCommand(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	if !r.IsCommand("/new") {
		t.Error("IsCommand(/new) = false, want true")
	}
	if !r.IsCommand("!ls") {
		t.Error("IsCommand(!ls) = false, want true")
	}
	if r.IsCommand("hello world") {
		t.Error("IsCommand(hello world) = true, want false")
	}
}

func TestCommandRegistry_Commands(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	cmds := r.Commands()
	if len(cmds) != 9 {
		t.Errorf("Commands() returned %d commands, want 9", len(cmds))
	}

	// Verify all expected commands are registered
	names := make(map[string]bool)
	for _, cmd := range cmds {
		names[cmd.Name()] = true
	}
	expected := []string{"/new", "/version", "/help", "/prompt", "/set-llm", "/llm", "/compress", "/context", "!"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("Command %q not found in registry", name)
		}
	}
}

func TestCommandConcurrency(t *testing.T) {
	r := NewCommandRegistry()
	registerBuiltinCommands(r)

	// Commands that mutate session state must NOT be concurrent
	nonConcurrent := map[string]bool{
		"/new":      true,
		"/compress": true,
		"/set-llm":  true,
	}

	// Commands that are stateless/read-only should be concurrent
	concurrent := map[string]bool{
		"/version": true,
		"/help":    true,
		"/llm":     true,
		"/prompt":  true,
		"!":        true,
	}

	for _, cmd := range r.Commands() {
		name := cmd.Name()
		if nonConcurrent[name] {
			if cmd.Concurrent() {
				t.Errorf("Command %q: Concurrent() = true, want false (mutates session state)", name)
			}
		}
		if concurrent[name] {
			if !cmd.Concurrent() {
				t.Errorf("Command %q: Concurrent() = false, want true (stateless)", name)
			}
		}
	}
}
