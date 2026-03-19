package events

// AgentCreatedPayload is the payload for AgentCreatedEvent.
type AgentCreatedPayload struct {
	AgentID     string `json:"agent_id"`
	AgentRole   string `json:"agent_role"`
	DisplayName string `json:"display_name"`
	Avatar      string `json:"avatar"`
	GroupID     string `json:"group_id"`
	Task        string `json:"task"`
	ParentAgent string `json:"parent_agent"`
}

// AgentDestroyedPayload is the payload for AgentDestroyedEvent.
type AgentDestroyedPayload struct {
	AgentID   string `json:"agent_id"`
	AgentRole string `json:"agent_role"`
	GroupID   string `json:"group_id"`
}

// ToolCalledPayload is the payload for ToolCalledEvent.
type ToolCalledPayload struct {
	ToolName   string                 `json:"tool_name"`
	AgentID    string                 `json:"agent_id"`
	Parameters map[string]interface{} `json:"parameters"`
	Context    map[string]string      `json:"context"`
}

// MessageReceivedPayload is the payload for MessageReceivedEvent.
type MessageReceivedPayload struct {
	MessageID   string `json:"message_id"`
	Channel     string `json:"channel"`
	ChatID      string `json:"chat_id"`
	SenderID    string `json:"sender_id"`
	ContentType string `json:"content_type"`
	Content     string `json:"content"`
}

// MessageSentPayload is the payload for MessageSentEvent.
type MessageSentPayload struct {
	MessageID string `json:"message_id"`
	Channel   string `json:"channel"`
	Content   string `json:"content"`
}
