package bus

// MessageSender sends messages to any addressable Channel.
// Implemented by channel.Dispatcher. Defined in bus package to avoid
// circular dependencies between channel and tools packages.
type MessageSender interface {
	// SendMessage sends a message to the specified channel/chatID.
	// Returns the response content (for agent channels, RPC) or empty string (for IM).
	SendMessage(channelName, chatID, content string) (string, error)
	// RegisterDynamic registers a dynamic channel (agent/group).
	// Returns error if the channel doesn't implement the full Channel interface.
	RegisterDynamic(name string, ch ChannelLike) error
	// UnregisterDynamic removes a dynamically registered channel.
	UnregisterDynamic(name string)
}

// ChannelLike is a minimal channel interface for dynamic registration.
// Both channel.Channel and channel-specific types (AgentChannel, GroupChannel) implement this.
type ChannelLike interface {
	Name() string
	Start() error
	Stop()
}

// ResolveChannelName extracts the Dispatcher channel name from an address.
// For IM addresses ("feishu:ou_xxx") it returns the protocol prefix ("feishu").
// For agent/group addresses ("agent:reviewer-cr1", "group:rt1") it returns the full address.
// Plain names pass through unchanged.
func ResolveChannelName(addr string) string {
	imPrefixes := []string{"feishu", "web", "qq", "cli"}
	for _, prefix := range imPrefixes {
		if len(addr) > len(prefix)+1 && addr[:len(prefix)+1] == prefix+":" {
			return prefix
		}
	}
	return addr
}
