package events

// Event type constants (extensible).
const (
	// System events
	EventTypeSystemStarted = "system.started"
	EventTypeSystemStopped = "system.stopped"
	EventTypeSystemError   = "system.error"

	// Message events
	EventTypeMessageReceived = "message.received"
	EventTypeMessageSent     = "message.sent"
	EventTypeMessageProgress = "message.progress"

	// Agent lifecycle events
	EventTypeAgentCreated   = "agent.created"
	EventTypeAgentStarted   = "agent.started"
	EventTypeAgentStopped   = "agent.stopped"
	EventTypeAgentDestroyed = "agent.destroyed"
	EventTypeAgentMessage   = "agent.message"

	// Tool events
	EventTypeToolCalled    = "tool.called"
	EventTypeToolCompleted = "tool.completed"
	EventTypeToolFailed    = "tool.failed"
	EventTypeToolProgress  = "tool.progress"
)
