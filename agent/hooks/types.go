package hooks

import "context"

// ---------------------------------------------------------------------------
// Action
// ---------------------------------------------------------------------------

// Action represents the outcome decision from a hook handler.
type Action int

const (
	Allow Action = iota
	Deny
	Ask
	Defer
)

// String returns the lowercase name of the action.
func (a Action) String() string {
	switch a {
	case Allow:
		return "allow"
	case Deny:
		return "deny"
	case Ask:
		return "ask"
	case Defer:
		return "defer"
	default:
		return "allow"
	}
}

// ParseAction parses an action string (case-insensitive) into an Action value.
// Returns Allow for unrecognized strings.
func ParseAction(s string) Action {
	switch s {
	case "allow", "ALLOW", "Allow":
		return Allow
	case "deny", "DENY", "Deny":
		return Deny
	case "ask", "ASK", "Ask":
		return Ask
	case "defer", "DEFER", "Defer":
		return Defer
	default:
		return Allow
	}
}

// ---------------------------------------------------------------------------
// Decision
// ---------------------------------------------------------------------------

// Decision is the structured result from a hook handler that the agent loop
// uses to decide how to proceed.
type Decision struct {
	// Action determines whether the operation is allowed, denied, needs user
	// confirmation, or should be deferred to the next handler.
	Action Action
	// Reason is an optional human-readable explanation for the decision.
	Reason string
	// UpdatedInput is only used with PreToolUse events. When set, the tool
	// input is replaced with this map before execution.
	UpdatedInput map[string]any
	// Context is injected into the agent's context so it can adjust its
	// behaviour based on the hook's feedback.
	Context string
}

// ---------------------------------------------------------------------------
// Result
// ---------------------------------------------------------------------------

// Result is the raw output from executing a hook (command, HTTP call, etc.).
type Result struct {
	// ExitCode is the process exit code for command-type hooks.
	ExitCode int
	// Stdout captures the standard output of the hook execution.
	Stdout string
	// Stderr captures the standard error of the hook execution.
	Stderr string
	// Decision is the normalized action string: "allow", "deny", "ask", or "defer".
	Decision string
	// Reason is an optional human-readable explanation.
	Reason string
	// UpdatedInput is the modified tool input for PreToolUse hooks.
	UpdatedInput map[string]any
	// Context is additional text to inject into the agent context.
	Context string
}

// ---------------------------------------------------------------------------
// Executor
// ---------------------------------------------------------------------------

// Executor is the interface that hook execution backends must implement.
// Each backend type (command, http, mcp_tool, callback) provides its own executor.
type Executor interface {
	// Type returns the backend type identifier, e.g. "command", "http".
	Type() string
	// Execute runs the hook handler for the given event and returns the result.
	Execute(ctx context.Context, def *HookDef, event Event) (*Result, error)
}

// ---------------------------------------------------------------------------
// CallbackHook
// ---------------------------------------------------------------------------

// CallbackHook is an in-process hook handler registered programmatically.
type CallbackHook struct {
	// Name is a human-readable identifier for the callback.
	Name string
	// Fn is the function invoked when the hook fires.
	Fn func(ctx context.Context, event Event) (*Result, error)
}

// ---------------------------------------------------------------------------
// HookDef
// ---------------------------------------------------------------------------

// HookDef defines a single hook handler from the configuration file.
type HookDef struct {
	// Type identifies the backend: "command", "http", "mcp_tool", or "callback".
	Type string `json:"type"`
	// Command is the shell command to execute (command type only).
	Command string `json:"command,omitempty"`
	// URL is the HTTP endpoint to call (http type only).
	URL string `json:"url,omitempty"`
	// Headers are additional HTTP headers (http type only).
	Headers map[string]string `json:"headers,omitempty"`
	// AllowedEnvVars lists environment variable names that are passed to the
	// hook execution environment (http type).
	AllowedEnvVars []string `json:"allowed_env_vars,omitempty"`
	// Timeout is the maximum execution time in seconds. 0 means use the
	// default timeout.
	Timeout int `json:"timeout,omitempty"`
	// Async indicates the hook should be executed without blocking the agent loop.
	Async bool `json:"async,omitempty"`
	// Server is the MCP server name (mcp_tool type only).
	Server string `json:"server,omitempty"`
	// Tool is the MCP tool name (mcp_tool type only).
	Tool string `json:"tool,omitempty"`
	// Input is the static input passed to the MCP tool (mcp_tool type only).
	Input map[string]any `json:"input,omitempty"`
	// If is an optional condition expression that must evaluate to true for the
	// hook to fire.
	If string `json:"if,omitempty"`
}

// ---------------------------------------------------------------------------
// EventGroup
// ---------------------------------------------------------------------------

// EventGroup groups hooks under the same event matcher in the configuration.
type EventGroup struct {
	// Matcher is an optional pattern to further filter which events trigger
	// this group (e.g. a tool name glob for PreToolUse).
	Matcher string `json:"matcher,omitempty"`
	// Hooks is the ordered list of hook handlers for this group.
	Hooks []HookDef `json:"hooks"`
}
