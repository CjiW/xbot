package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// contextKey is an unexported type for context keys defined in this package.
type contextKey string

const permUsersKey contextKey = "perm_users"
const workingDirKey contextKey = "working_dir"

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

// isPermControlActiveFromCtx checks if permission control is active from context.
// Returns false when no perm users are configured (both empty).
func isPermControlActiveFromCtx(ctx context.Context) bool {
	defaultUser, privilegedUser := PermUsersFromContext(ctx)
	return defaultUser != "" || privilegedUser != ""
}

// WithPermUsers injects the permission control user config into the context.
func WithPermUsers(ctx context.Context, defaultUser, privilegedUser string) context.Context {
	return context.WithValue(ctx, permUsersKey, &PermUsersPair{
		DefaultUser:    defaultUser,
		PrivilegedUser: privilegedUser,
	})
}

// WithWorkingDir injects the agent's working directory into context.
// Used by checkpoint hook to resolve relative file paths to absolute.
func WithWorkingDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, workingDirKey, dir)
}

// WorkingDirFromContext retrieves the working directory from context.
func WorkingDirFromContext(ctx context.Context) string {
	if dir, ok := ctx.Value(workingDirKey).(string); ok {
		return dir
	}
	return ""
}

// ApprovalRequest represents a pending user approval for a tool execution.
type ApprovalRequest struct {
	ToolName string `json:"tool_name"` // e.g., "Shell"
	ToolArgs string `json:"tool_args"` // JSON arguments (for display)
	RunAs    string `json:"run_as"`    // Target OS user
	Reason   string `json:"reason"`    // Human-readable description

	// Extracted details for display (populated by ApprovalHook)
	Command     string `json:"command,omitempty"`      // Parsed command (possibly truncated for display)
	FilePath    string `json:"file_path,omitempty"`    // Target file (possibly truncated for display)
	ArgsSummary string `json:"args_summary,omitempty"` // Extra argument summary for approval UI
}

// ApprovalResult is the user's decision.
type ApprovalResult struct {
	Approved   bool   `json:"approved"`
	DenyReason string `json:"deny_reason,omitempty"`
}

// ApprovalHandler is the channel-agnostic interface for user approval.
// Each channel (CLI, Web) provides its own implementation.
type ApprovalHandler interface {
	// RequestApproval sends an approval request and waits for the user's response.
	RequestApproval(ctx context.Context, req ApprovalRequest) (ApprovalResult, error)
}

// extractRunAsAndReason parses the "run_as" and "reason" fields from JSON tool arguments.
// Returns empty strings if not present or on parse error.
func extractRunAsAndReason(args string) (runAs, reason string) {
	var raw struct {
		RunAs  string `json:"run_as"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(args), &raw); err != nil {
		return "", ""
	}
	return raw.RunAs, raw.Reason
}

func truncateApprovalText(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// populateApprovalDetails extracts human-readable details for the approval dialog.
func populateApprovalDetails(req *ApprovalRequest, toolName, args string) {
	const maxDisplayLen = 160

	switch toolName {
	case "Shell":
		var p struct {
			Command string `json:"command"`
			Reason  string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.Command = truncateApprovalText(p.Command, maxDisplayLen)
			req.ArgsSummary = req.Command
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = truncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Execute command as %q", req.RunAs)
			}
		}
	case "FileCreate":
		var p struct {
			Path   string `json:"path"`
			RunAs  string `json:"run_as"`
			Reason string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.FilePath = truncateApprovalText(p.Path, maxDisplayLen)
			req.ArgsSummary = req.FilePath
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = truncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Create file as %q", req.RunAs)
			}
		}
	case "FileReplace":
		var p struct {
			Path      string `json:"path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
			Reason    string `json:"reason"`
		}
		if json.Unmarshal([]byte(args), &p) == nil {
			req.FilePath = truncateApprovalText(p.Path, maxDisplayLen)
			req.ArgsSummary = fmt.Sprintf("old=%q new=%q", truncateApprovalText(p.OldString, 40), truncateApprovalText(p.NewString, 40))
			if strings.TrimSpace(p.Reason) != "" {
				req.Reason = truncateApprovalText(p.Reason, maxDisplayLen)
			} else {
				req.Reason = fmt.Sprintf("Modify file as %q", req.RunAs)
			}
		}
	}
	if req.Reason == "" {
		req.Reason = fmt.Sprintf("Execute %s as %q", toolName, req.RunAs)
	}
}
