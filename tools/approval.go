package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// contextKey is an unexported type for context keys defined in this package.
type contextKey string

const permUsersKey contextKey = "perm_users"

// PermUsersFromContext retrieves the permission control user config from context.
func PermUsersFromContext(ctx context.Context) (defaultUser, privilegedUser string) {
	config, ok := ctx.Value(permUsersKey).(*PermUsersPair)
	if !ok || config == nil {
		return "", ""
	}
	return config.DefaultUser, config.PrivilegedUser
}

// PermUsersPair holds the permission control user pair for context injection.
type PermUsersPair struct {
	DefaultUser    string
	PrivilegedUser string
}

// WithPermUsers injects the permission control user config into the context.
func WithPermUsers(ctx context.Context, defaultUser, privilegedUser string) context.Context {
	return context.WithValue(ctx, permUsersKey, &PermUsersPair{
		DefaultUser:    defaultUser,
		PrivilegedUser: privilegedUser,
	})
}

// ApprovalRequest represents a pending user approval for a tool execution.
type ApprovalRequest struct {
	ToolName string `json:"tool_name"` // e.g., "Shell"
	ToolArgs string `json:"tool_args"` // JSON arguments (for display)
	RunAs    string `json:"run_as"`    // Target OS user
	Reason   string `json:"reason"`    // Human-readable description

	// Extracted details for display (populated by ApprovalHook)
	Command  string `json:"command,omitempty"`   // Parsed command (for Shell)
	FilePath string `json:"file_path,omitempty"` // Target file (for FileReplace/FileCreate)
}

// ApprovalResult is the user's decision.
type ApprovalResult int

const (
	ApprovalDenied   ApprovalResult = 0
	ApprovalApproved ApprovalResult = 1
)

// ApprovalHandler is the channel-agnostic interface for user approval.
// Each channel (CLI, Web) provides its own implementation.
type ApprovalHandler interface {
	// RequestApproval sends an approval request and waits for the user's response.
	RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResult, error)
}

// ApprovalHook is a ToolHook that intercepts tool calls targeting privileged users.
// It reads the user configuration from context (injected per-request by the engine),
// so settings changes take effect immediately without restart.
type ApprovalHook struct {
	handler ApprovalHandler
	timeout time.Duration
}

// NewApprovalHook creates an ApprovalHook with the given handler.
// User configuration (defaultUser, privilegedUser) is read from context per-request.
func NewApprovalHook(handler ApprovalHandler) *ApprovalHook {
	return &ApprovalHook{
		handler: handler,
		timeout: 60 * time.Second,
	}
}

func (h *ApprovalHook) Name() string { return "approval" }

func (h *ApprovalHook) PreToolUse(ctx context.Context, toolName string, args string) error {
	runAs := extractRunAs(args)

	// No run_as specified — execute as current process user
	if runAs == "" {
		return nil
	}

	// Read user configuration from context (per-request, from user_settings)
	defaultUser, privilegedUser := PermUsersFromContext(ctx)

	// Feature not configured — reject any run_as value
	if defaultUser == "" && privilegedUser == "" {
		return fmt.Errorf("permission control is not enabled: cannot use run_as %q (configure default_user or privileged_user in settings)", runAs)
	}

	// Validate run_as against configured users
	if runAs == defaultUser {
		// Default user — no approval needed
		return nil
	}

	if runAs != privilegedUser {
		// Unknown user
		users := defaultUser
		if privilegedUser != "" {
			if users != "" {
				users += " or " + privilegedUser
			} else {
				users = privilegedUser
			}
		}
		return fmt.Errorf("unknown run_as user %q: must be %q", runAs, users)
	}

	// Privileged user — request approval with timeout
	if h.handler == nil {
		// No approval handler registered — block execution
		return fmt.Errorf("execution as %q requires approval but no approval handler is available (running in non-interactive channel?)", runAs)
	}

	approvalCtx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()

	req := ApprovalRequest{
		ToolName: toolName,
		ToolArgs: args,
		RunAs:    runAs,
	}

	// Extract display details from args
	populateApprovalDetails(&req, toolName, args)

	result, err := h.handler.RequestApproval(approvalCtx, req)
	if err != nil {
		return fmt.Errorf("approval request failed: %w", err)
	}
	if result != ApprovalApproved {
		return fmt.Errorf("user denied execution as %q", runAs)
	}

	return nil
}

func (h *ApprovalHook) PostToolUse(ctx context.Context, toolName string, args string, result *ToolResult, err error, elapsed time.Duration) {
	// No post-action needed
}

// extractRunAs parses the "run_as" field from JSON tool arguments.
// Returns empty string if not present or on parse error.
func extractRunAs(args string) string {
	var raw struct {
		RunAs string `json:"run_as"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return ""
	}
	return raw.RunAs
}

// populateApprovalDetails extracts human-readable details for the approval dialog.
func populateApprovalDetails(req *ApprovalRequest, toolName, args string) {
	switch toolName {
	case "Shell":
		var p struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.Command = p.Command
			req.Reason = fmt.Sprintf("Execute command as %q: %s", req.RunAs, p.Command)
		}
	case "FileCreate":
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.FilePath = p.Path
			req.Reason = fmt.Sprintf("Create file as %q: %s", req.RunAs, p.Path)
		}
	case "FileReplace":
		var p struct {
			Path string `json:"path"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.FilePath = p.Path
			req.Reason = fmt.Sprintf("Modify file as %q: %s", req.RunAs, p.Path)
		}
	}
	if req.Reason == "" {
		req.Reason = fmt.Sprintf("Execute %s as %q", toolName, req.RunAs)
	}
}
