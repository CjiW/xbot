package agent

import (
	"context"
	"strings"

	"xbot/bus"
)

// Command defines the interface for slash commands and other quick commands
// that bypass the LLM pipeline and execute directly.
type Command interface {
	// Name returns the primary command name (e.g., "/new", "/help").
	// For bang commands, this returns "!".
	Name() string

	// Aliases returns alternative names that also trigger this command.
	// For example, a command might respond to both "/version" and "/v".
	Aliases() []string

	// Match checks if the given message content should be handled by this command.
	// This allows commands with prefix matching (e.g., "/prompt <query>", "/set-llm <args>")
	// or special patterns (e.g., "!" prefix for bang commands).
	// Returns true if this command should handle the message.
	Match(content string) bool

	// Execute runs the command and returns an outbound message (or nil to suppress reply).
	// The command receives the full Agent context to access sessions, tools, etc.
	Execute(ctx context.Context, a *Agent, msg bus.InboundMessage) (*bus.OutboundMessage, error)
}

// CommandRegistry holds registered commands and provides lookup.
type CommandRegistry struct {
	commands []Command
}

// NewCommandRegistry creates an empty command registry.
func NewCommandRegistry() *CommandRegistry {
	return &CommandRegistry{}
}

// Register adds a command to the registry.
func (r *CommandRegistry) Register(cmd Command) {
	r.commands = append(r.commands, cmd)
}

// Match finds the first command that matches the given message content.
// Returns nil if no command matches.
func (r *CommandRegistry) Match(content string) Command {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}
	for _, cmd := range r.commands {
		if cmd.Match(trimmed) {
			return cmd
		}
	}
	return nil
}

// IsCommand returns true if the message content matches any registered command.
func (r *CommandRegistry) IsCommand(content string) bool {
	return r.Match(content) != nil
}

// Commands returns all registered commands (for /help generation, etc.).
func (r *CommandRegistry) Commands() []Command {
	return r.commands
}
