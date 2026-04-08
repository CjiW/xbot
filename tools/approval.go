package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

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
// It only activates when the tool call includes a run_as parameter matching the
// configured privileged user.
type ApprovalHook struct {
	handler        ApprovalHandler
	defaultUser    string // from user settings (empty = feature disabled)
	privilegedUser string // from user settings (empty = no privileged user)
	timeout        time.Duration
}

// NewApprovalHook creates an ApprovalHook with the given handler and user configuration.
func NewApprovalHook(handler ApprovalHandler, defaultUser, privilegedUser string) *ApprovalHook {
	return &ApprovalHook{
		handler:        handler,
		defaultUser:    defaultUser,
		privilegedUser: privilegedUser,
		timeout:        60 * time.Second,
	}
}

func (h *ApprovalHook) Name() string { return "approval" }

func (h *ApprovalHook) PreToolUse(ctx context.Context, toolName string, args string) error {
	runAs := extractRunAs(args)

	// No run_as specified — execute as current process user
	if runAs == "" {
		return nil
	}

	// Feature not configured — reject any run_as value
	if h.defaultUser == "" && h.privilegedUser == "" {
		return fmt.Errorf("permission control is not enabled: cannot use run_as %q (configure default_user or privileged_user in settings)", runAs)
	}

	// Validate run_as against configured users
	if runAs == h.defaultUser {
		// Default user — no approval needed
		return nil
	}

	if runAs != h.privilegedUser {
		// Unknown user
		users := h.defaultUser
		if h.privilegedUser != "" {
			if users != "" {
				users += " or " + h.privilegedUser
			} else {
				users = h.privilegedUser
			}
		}
		return fmt.Errorf("unknown run_as user %q: must be %q", runAs, users)
	}

	// Privileged user — request approval with timeout
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
