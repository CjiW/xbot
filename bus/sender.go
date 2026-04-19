package bus

import "context"

// MessageSender sends messages to any addressable Channel.
// Implemented by channel.Dispatcher. Defined in bus package to avoid
// circular dependencies between channel and tools packages.
type MessageSender interface {
	// SendMessage sends a message to the specified channel/chatID.
	// Returns the response content (for agent channels, RPC) or empty string (for IM).
	SendMessage(channelName, chatID, content string) (string, error)
}

// RunFn runs a SubAgent task and returns the result.
// Defined in bus to avoid channel→agent circular dependency.
type RunFn func(ctx context.Context, task string) (string, error)
