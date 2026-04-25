package hooks

import (
	"time"
)

// Event is the core interface that all hook events must implement.
type Event interface {
	// EventName returns the canonical event name, e.g. "PreToolUse".
	EventName() string
	// Payload returns the full event payload as a map.
	Payload() map[string]any
	// ToolName returns the tool name for tool-related events; empty string otherwise.
	ToolName() string
	// ToolInput returns the tool input for tool-related events; nil otherwise.
	ToolInput() map[string]any
}

// BasePayload contains fields shared by all event types.
type BasePayload struct {
	SessionID string `json:"session_id"`
	Channel   string `json:"channel"`
	SenderID  string `json:"sender_id"`
	ChatID    string `json:"chat_id"`
	CWD       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
}

// baseToMap converts BasePayload fields into a map.
func baseToMap(b BasePayload) map[string]any {
	return map[string]any{
		"session_id": b.SessionID,
		"channel":    b.Channel,
		"sender_id":  b.SenderID,
		"chat_id":    b.ChatID,
		"cwd":        b.CWD,
		"timestamp":  b.Timestamp,
	}
}

// ---------------------------------------------------------------------------
// Event name constants
// ---------------------------------------------------------------------------

const (
	EventSessionStart       = "SessionStart"
	EventSessionEnd         = "SessionEnd"
	EventUserPromptSubmit   = "UserPromptSubmit"
	EventPreToolUse         = "PreToolUse"
	EventPostToolUse        = "PostToolUse"
	EventPostToolUseFailure = "PostToolUseFailure"
	EventPostToolBatch      = "PostToolBatch"
	EventPermissionRequest  = "PermissionRequest"
	EventPermissionDenied   = "PermissionDenied"
	EventSubAgentStart      = "SubAgentStart"
	EventSubAgentStop       = "SubAgentStop"
	EventAgentStop          = "AgentStop"
	EventAgentError         = "AgentError"
	EventPreCompact         = "PreCompact"
	EventPostCompact        = "PostCompact"
	EventCronFired          = "CronFired"
	EventWebhookReceived    = "WebhookReceived"
)

// ---------------------------------------------------------------------------
// Helper types
// ---------------------------------------------------------------------------

// ToolBatchResult holds the outcome of a single tool invocation within a batch.
type ToolBatchResult struct {
	ToolName string
	Success  bool
	Error    string
	Elapsed  time.Duration
}

// ---------------------------------------------------------------------------
// 1. SessionStartEvent
// ---------------------------------------------------------------------------

// SessionStartEvent is emitted when a new agent session begins.
type SessionStartEvent struct {
	BasePayload
	Source         string `json:"source"`
	Model          string `json:"model"`
	MemoryProvider string `json:"memory_provider"`
}

func (e *SessionStartEvent) EventName() string         { return EventSessionStart }
func (e *SessionStartEvent) ToolName() string          { return "" }
func (e *SessionStartEvent) ToolInput() map[string]any { return nil }
func (e *SessionStartEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["source"] = e.Source
	m["model"] = e.Model
	m["memory_provider"] = e.MemoryProvider
	return m
}

// ---------------------------------------------------------------------------
// 2. SessionEndEvent
// ---------------------------------------------------------------------------

// SessionEndEvent is emitted when a session ends.
type SessionEndEvent struct {
	BasePayload
	Source string `json:"source"`
}

func (e *SessionEndEvent) EventName() string         { return EventSessionEnd }
func (e *SessionEndEvent) ToolName() string          { return "" }
func (e *SessionEndEvent) ToolInput() map[string]any { return nil }
func (e *SessionEndEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["source"] = e.Source
	return m
}

// ---------------------------------------------------------------------------
// 3. UserPromptSubmitEvent
// ---------------------------------------------------------------------------

// UserPromptSubmitEvent is emitted when the user submits a prompt.
type UserPromptSubmitEvent struct {
	BasePayload
	Prompt string `json:"prompt"`
}

func (e *UserPromptSubmitEvent) EventName() string         { return EventUserPromptSubmit }
func (e *UserPromptSubmitEvent) ToolName() string          { return "" }
func (e *UserPromptSubmitEvent) ToolInput() map[string]any { return nil }
func (e *UserPromptSubmitEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["prompt"] = e.Prompt
	return m
}

// ---------------------------------------------------------------------------
// 4. PreToolUseEvent
// ---------------------------------------------------------------------------

// PreToolUseEvent is emitted before a tool is executed.
type PreToolUseEvent struct {
	BasePayload
	ToolName_  string         `json:"tool_name"`
	ToolInput_ map[string]any `json:"tool_input"`
	ToolUseID  string         `json:"tool_use_id"`
}

func (e *PreToolUseEvent) EventName() string         { return EventPreToolUse }
func (e *PreToolUseEvent) ToolName() string          { return e.ToolName_ }
func (e *PreToolUseEvent) ToolInput() map[string]any { return e.ToolInput_ }
func (e *PreToolUseEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["tool_name"] = e.ToolName_
	m["tool_input"] = e.ToolInput_
	m["tool_use_id"] = e.ToolUseID
	return m
}

// ---------------------------------------------------------------------------
// 5. PostToolUseEvent
// ---------------------------------------------------------------------------

// PostToolUseEvent is emitted after a tool executes successfully.
type PostToolUseEvent struct {
	BasePayload
	ToolName_     string         `json:"tool_name"`
	ToolInput_    map[string]any `json:"tool_input"`
	ToolUseID     string         `json:"tool_use_id"`
	ToolElapsedMs int64          `json:"tool_elapsed_ms"`
	ToolError     string         `json:"tool_error"`
}

func (e *PostToolUseEvent) EventName() string         { return EventPostToolUse }
func (e *PostToolUseEvent) ToolName() string          { return e.ToolName_ }
func (e *PostToolUseEvent) ToolInput() map[string]any { return e.ToolInput_ }
func (e *PostToolUseEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["tool_name"] = e.ToolName_
	m["tool_input"] = e.ToolInput_
	m["tool_use_id"] = e.ToolUseID
	m["tool_elapsed_ms"] = e.ToolElapsedMs
	m["tool_error"] = e.ToolError
	return m
}

// ---------------------------------------------------------------------------
// 6. PostToolUseFailureEvent
// ---------------------------------------------------------------------------

// PostToolUseFailureEvent is emitted when a tool execution fails.
type PostToolUseFailureEvent struct {
	BasePayload
	ToolName_  string         `json:"tool_name"`
	ToolInput_ map[string]any `json:"tool_input"`
	ToolUseID  string         `json:"tool_use_id"`
	ToolError  string         `json:"tool_error"`
}

func (e *PostToolUseFailureEvent) EventName() string         { return EventPostToolUseFailure }
func (e *PostToolUseFailureEvent) ToolName() string          { return e.ToolName_ }
func (e *PostToolUseFailureEvent) ToolInput() map[string]any { return e.ToolInput_ }
func (e *PostToolUseFailureEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["tool_name"] = e.ToolName_
	m["tool_input"] = e.ToolInput_
	m["tool_use_id"] = e.ToolUseID
	m["tool_error"] = e.ToolError
	return m
}

// ---------------------------------------------------------------------------
// 7. PostToolBatchEvent
// ---------------------------------------------------------------------------

// PostToolBatchEvent is emitted after a batch of tools finishes.
type PostToolBatchEvent struct {
	BasePayload
	ToolCount int               `json:"tool_count"`
	Results   []ToolBatchResult `json:"results"`
}

func (e *PostToolBatchEvent) EventName() string         { return EventPostToolBatch }
func (e *PostToolBatchEvent) ToolName() string          { return "" }
func (e *PostToolBatchEvent) ToolInput() map[string]any { return nil }
func (e *PostToolBatchEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["tool_count"] = e.ToolCount
	// Convert ToolBatchResult slice to []map[string]any for JSON friendliness
	results := make([]map[string]any, 0, len(e.Results))
	for _, r := range e.Results {
		results = append(results, map[string]any{
			"tool_name": r.ToolName,
			"success":   r.Success,
			"error":     r.Error,
			"elapsed":   r.Elapsed.String(),
		})
	}
	m["results"] = results
	return m
}

// ---------------------------------------------------------------------------
// 8. PermissionRequestEvent
// ---------------------------------------------------------------------------

// PermissionRequestEvent is emitted when a permission decision is needed.
type PermissionRequestEvent struct {
	BasePayload
	ToolName_  string         `json:"tool_name"`
	ToolInput_ map[string]any `json:"tool_input"`
	ToolUseID  string         `json:"tool_use_id"`
}

func (e *PermissionRequestEvent) EventName() string         { return EventPermissionRequest }
func (e *PermissionRequestEvent) ToolName() string          { return e.ToolName_ }
func (e *PermissionRequestEvent) ToolInput() map[string]any { return e.ToolInput_ }
func (e *PermissionRequestEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["tool_name"] = e.ToolName_
	m["tool_input"] = e.ToolInput_
	m["tool_use_id"] = e.ToolUseID
	return m
}

// ---------------------------------------------------------------------------
// 9. PermissionDeniedEvent
// ---------------------------------------------------------------------------

// PermissionDeniedEvent is emitted when a permission request is denied.
type PermissionDeniedEvent struct {
	BasePayload
	ToolName_  string         `json:"tool_name"`
	ToolInput_ map[string]any `json:"tool_input"`
	Reason     string         `json:"reason"`
}

func (e *PermissionDeniedEvent) EventName() string         { return EventPermissionDenied }
func (e *PermissionDeniedEvent) ToolName() string          { return e.ToolName_ }
func (e *PermissionDeniedEvent) ToolInput() map[string]any { return e.ToolInput_ }
func (e *PermissionDeniedEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["tool_name"] = e.ToolName_
	m["tool_input"] = e.ToolInput_
	m["reason"] = e.Reason
	return m
}

// ---------------------------------------------------------------------------
// 10. SubAgentStartEvent
// ---------------------------------------------------------------------------

// SubAgentStartEvent is emitted when a sub-agent is launched.
type SubAgentStartEvent struct {
	BasePayload
	AgentType string `json:"agent_type"`
	Task      string `json:"task"`
}

func (e *SubAgentStartEvent) EventName() string         { return EventSubAgentStart }
func (e *SubAgentStartEvent) ToolName() string          { return "" }
func (e *SubAgentStartEvent) ToolInput() map[string]any { return nil }
func (e *SubAgentStartEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["agent_type"] = e.AgentType
	m["task"] = e.Task
	return m
}

// ---------------------------------------------------------------------------
// 11. SubAgentStopEvent
// ---------------------------------------------------------------------------

// SubAgentStopEvent is emitted when a sub-agent finishes.
type SubAgentStopEvent struct {
	BasePayload
	AgentType string `json:"agent_type"`
	Instance  string `json:"instance"`
	Content   string `json:"content"`
}

func (e *SubAgentStopEvent) EventName() string         { return EventSubAgentStop }
func (e *SubAgentStopEvent) ToolName() string          { return "" }
func (e *SubAgentStopEvent) ToolInput() map[string]any { return nil }
func (e *SubAgentStopEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["agent_type"] = e.AgentType
	m["instance"] = e.Instance
	m["content"] = e.Content
	return m
}

// ---------------------------------------------------------------------------
// 12. AgentStopEvent
// ---------------------------------------------------------------------------

// AgentStopEvent is emitted when the main agent finishes a turn.
type AgentStopEvent struct {
	BasePayload
	Content string `json:"content"`
}

func (e *AgentStopEvent) EventName() string         { return EventAgentStop }
func (e *AgentStopEvent) ToolName() string          { return "" }
func (e *AgentStopEvent) ToolInput() map[string]any { return nil }
func (e *AgentStopEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["content"] = e.Content
	return m
}

// ---------------------------------------------------------------------------
// 13. AgentErrorEvent
// ---------------------------------------------------------------------------

// AgentErrorEvent is emitted when the agent encounters an error.
type AgentErrorEvent struct {
	BasePayload
	ErrorType    string `json:"error_type"`
	ErrorMessage string `json:"error_message"`
}

func (e *AgentErrorEvent) EventName() string         { return EventAgentError }
func (e *AgentErrorEvent) ToolName() string          { return "" }
func (e *AgentErrorEvent) ToolInput() map[string]any { return nil }
func (e *AgentErrorEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["error_type"] = e.ErrorType
	m["error_message"] = e.ErrorMessage
	return m
}

// ---------------------------------------------------------------------------
// 14. PreCompactEvent
// ---------------------------------------------------------------------------

// PreCompactEvent is emitted before context compaction.
type PreCompactEvent struct {
	BasePayload
	Trigger               string `json:"trigger"`
	MessageCount          int    `json:"message_count"`
	EstimatedTokensBefore int64  `json:"estimated_tokens_before"`
}

func (e *PreCompactEvent) EventName() string         { return EventPreCompact }
func (e *PreCompactEvent) ToolName() string          { return "" }
func (e *PreCompactEvent) ToolInput() map[string]any { return nil }
func (e *PreCompactEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["trigger"] = e.Trigger
	m["message_count"] = e.MessageCount
	m["estimated_tokens_before"] = e.EstimatedTokensBefore
	return m
}

// ---------------------------------------------------------------------------
// 15. PostCompactEvent
// ---------------------------------------------------------------------------

// PostCompactEvent is emitted after context compaction.
type PostCompactEvent struct {
	BasePayload
	Trigger              string `json:"trigger"`
	EstimatedTokensAfter int64  `json:"estimated_tokens_after"`
}

func (e *PostCompactEvent) EventName() string         { return EventPostCompact }
func (e *PostCompactEvent) ToolName() string          { return "" }
func (e *PostCompactEvent) ToolInput() map[string]any { return nil }
func (e *PostCompactEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["trigger"] = e.Trigger
	m["estimated_tokens_after"] = e.EstimatedTokensAfter
	return m
}

// ---------------------------------------------------------------------------
// 16. CronFiredEvent
// ---------------------------------------------------------------------------

// CronFiredEvent is emitted when a scheduled cron job fires.
type CronFiredEvent struct {
	BasePayload
	JobID   string `json:"job_id"`
	Message string `json:"message"`
}

func (e *CronFiredEvent) EventName() string         { return EventCronFired }
func (e *CronFiredEvent) ToolName() string          { return "" }
func (e *CronFiredEvent) ToolInput() map[string]any { return nil }
func (e *CronFiredEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["job_id"] = e.JobID
	m["message"] = e.Message
	return m
}

// ---------------------------------------------------------------------------
// 17. WebhookReceivedEvent
// ---------------------------------------------------------------------------

// WebhookReceivedEvent is emitted when an incoming webhook is received.
type WebhookReceivedEvent struct {
	BasePayload
	TriggerID string         `json:"trigger_id"`
	Payload_  map[string]any `json:"payload"`
}

func (e *WebhookReceivedEvent) EventName() string         { return EventWebhookReceived }
func (e *WebhookReceivedEvent) ToolName() string          { return "" }
func (e *WebhookReceivedEvent) ToolInput() map[string]any { return nil }
func (e *WebhookReceivedEvent) Payload() map[string]any {
	m := baseToMap(e.BasePayload)
	m["hook_event_name"] = e.EventName()
	m["trigger_id"] = e.TriggerID
	m["payload"] = e.Payload_
	return m
}
