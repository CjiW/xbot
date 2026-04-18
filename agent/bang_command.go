package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xbot/bus"
	log "xbot/logger"
	"xbot/tools"
)

const (
	// bangOutputMaxLen is the max character count before output is sent as a file.
	bangOutputMaxLen = 16000
	// bangDefaultTimeout is the default execution timeout for bang commands.
	bangDefaultTimeout = 120 * time.Second
)

// isBangCommand checks if the message is a `!` prefixed quick command.
func isBangCommand(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	if strings.HasPrefix(trimmed, "!") && len(trimmed) > 1 {
		cmd := strings.TrimSpace(trimmed[1:])
		// Avoid conflict with `!!` or `!` followed by whitespace only
		if cmd == "" {
			return "", false
		}
		return cmd, true
	}
	return "", false
}

// sandboxUserID resolves the effective sandbox user ID from an inbound message.
// If the message has a feishu_user_id metadata (web user with linked feishu identity),
// use that so the user gets the same runner/workspace as on the Feishu side.
func sandboxUserID(msg bus.InboundMessage) string {
	if fid := msg.Metadata["feishu_user_id"]; fid != "" {
		return fid
	}
	return msg.SenderID
}

// handleBangCommand executes a quick shell command (triggered by `!` prefix)
// and returns the result directly, bypassing LLM.
func (a *Agent) handleBangCommand(ctx context.Context, msg bus.InboundMessage, command string) (*bus.OutboundMessage, error) {
	sbUID := sandboxUserID(msg)
	log.WithFields(log.Fields{
		"channel":      msg.Channel,
		"sender":       msg.SenderID,
		"sandbox_user": sbUID,
		"command":      tools.Truncate(command, 80),
	}).Info("Bang command")

	workspaceRoot := a.sandboxWorkspace(sbUID)
	if err := a.ensureWorkspace(ctx, workspaceRoot, sbUID); err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}

	// Resolve session CWD so bang commands run in the same directory as the agent.
	sessionCWD := a.resolveBangCWD(msg.Channel, msg.ChatID, sbUID, workspaceRoot)

	output, exitErr := a.executeBangCommand(ctx, command, workspaceRoot, sbUID, sessionCWD)

	// Format result
	content := formatBangOutput(command, output, exitErr)

	// If output is too long, handle based on channel:
	// - Feishu: write to a .md file and send as file link (feishu renders markdown file links as downloadable cards)
	// - Other channels (CLI, Web, etc.): truncate inline (file links are meaningless)
	runes := []rune(content)
	if len(runes) > bangOutputMaxLen {
		if msg.Channel == "feishu" {
			filePath, err := a.writeBangOutputFile(ctx, workspaceRoot, command, output, exitErr, sbUID)
			if err != nil {
				log.WithError(err).Warn("Failed to write bang output file, sending truncated")
				content = string(runes[:bangOutputMaxLen-100]) + "\n...\n(output truncated, full output write failed)"
			} else {
				fileName := filepath.Base(filePath)
				content = fmt.Sprintf("[%s](%s)", fileName, filePath)
			}
		} else {
			// Non-feishu channels: truncate inline
			content = string(runes[:bangOutputMaxLen-100]) + "\n...\n(output truncated)"
		}
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
	}, nil
}

// resolveBangCWD looks up the session's current working directory for bang commands.
// Returns empty string if session is not available (falls back to workspaceRoot).
func (a *Agent) resolveBangCWD(channel, chatID, senderID, workspaceRoot string) string {
	if a.multiSession == nil {
		return ""
	}
	sess, err := a.multiSession.GetOrCreateSession(channel, chatID)
	if err != nil {
		return ""
	}
	cwd := sess.GetCurrentDir()
	if cwd == "" {
		return ""
	}
	// For docker sandbox, translate host CWD → container path.
	// Session CWD is always stored as a host path.
	if a.sandbox != nil {
		sb := a.sandbox
		if resolver, ok := sb.(tools.SandboxResolver); ok {
			sb = resolver.SandboxForUser(senderID)
		}
		if sb.Name() == "docker" {
			hostRoot := a.workspaceRoot(senderID)
			containerWS := sb.Workspace(senderID)
			if containerWS != "" && strings.HasPrefix(cwd, hostRoot) {
				return containerWS + cwd[len(hostRoot):]
			}
			return ""
		}
	}
	return cwd
}

// executeBangCommand runs the command in the user's sandbox (or locally if sandbox is disabled).
// Both paths use login shell (bash -l -c) via Sandbox.Exec for consistent behavior.
// workspaceRoot is the sandbox-internal path for file operations.
// cwd is the session's current working directory (may be empty if not set).
func (a *Agent) executeBangCommand(ctx context.Context, command, workspaceRoot, senderID string, cwd string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, bangDefaultTimeout)
	defer cancel()

	sandbox := tools.GetSandbox()
	// Resolve per-user sandbox for correct Name() routing
	if resolver, ok := sandbox.(tools.SandboxResolver); ok {
		sandbox = resolver.SandboxForUser(senderID)
	}

	// GetShell triggers Docker container management (create/start/verify mount),
	// which requires the host-side workspace path, not the container-internal path.
	hostWorkspace := workspaceRoot
	if sandbox.Name() == "docker" {
		hostWorkspace = a.workspaceRoot(senderID)
	}
	shell, err := sandbox.GetShell(senderID, hostWorkspace)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	// Determine working directory based on sandbox mode.
	// cwd is already translated by resolveBangCWD (docker → container path).
	// For remote sandbox, don't set dir (runner manages its own CWD).
	var dir string
	switch sandbox.Name() {
	case "remote":
		// Don't set dir -- runner defaults to its workspace
		// (remote runner manages its own CWD via the agent's Cd tool)
	default:
		if cwd != "" {
			dir = cwd
		} else {
			dir = workspaceRoot
		}
	}

	spec := tools.ExecSpec{
		Command: shell,
		Args:    []string{shell, "-l", "-c", command},
		Shell:   false,
		Dir:     dir,
		Timeout: bangDefaultTimeout,
		UserID:  senderID,
	}
	if sandbox.Name() == "docker" {
		spec.Workspace = hostWorkspace
	}

	result, err := sandbox.Exec(execCtx, spec)
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	if result.Stdout != "" {
		buf.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		if buf.Len() > 0 {
			buf.WriteString("\n")
		}
		buf.WriteString("[stderr] ")
		buf.WriteString(result.Stderr)
	}

	output := strings.TrimSpace(buf.String())
	if result.ExitCode != 0 && result.ExitCode != -1 {
		return output, fmt.Errorf("exit code %d", result.ExitCode)
	}
	return output, nil
}

// formatBangOutput formats the command output for inline display.
func formatBangOutput(command, output string, execErr error) string {
	var buf strings.Builder

	if execErr != nil {
		if output != "" {
			fmt.Fprintf(&buf, "```\n%s\n```\n`exit: %s`", output, execErr)
		} else {
			fmt.Fprintf(&buf, "`exit: %s`", execErr)
		}
	} else if output == "" {
		buf.WriteString("`OK (no output)`")
	} else {
		fmt.Fprintf(&buf, "```\n%s\n```", output)
	}

	return buf.String()
}

// writeBangOutputFile writes long output to a .md file and returns the file path.
func (a *Agent) writeBangOutputFile(ctx context.Context, workspaceRoot, command, output string, execErr error, senderID string) (string, error) {
	var buf strings.Builder
	fmt.Fprintf(&buf, "# Command: `%s`\n\n", command)

	if execErr != nil {
		fmt.Fprintf(&buf, "**Exit**: `%s`\n\n", execErr)
	}

	buf.WriteString("```\n")
	buf.WriteString(output)
	buf.WriteString("\n```\n")

	fileName := fmt.Sprintf("cmd-output-%d.md", time.Now().UnixMilli())
	filePath := filepath.Join(workspaceRoot, fileName)

	if a.sandbox != nil {
		if err := a.sandbox.MkdirAll(ctx, workspaceRoot, 0o755, senderID); err != nil {
			return "", err
		}
		if err := a.sandbox.WriteFile(ctx, filePath, []byte(buf.String()), 0o644, senderID); err != nil {
			return "", err
		}
	} else {
		if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(filePath, []byte(buf.String()), 0o644); err != nil {
			return "", err
		}
	}

	return filePath, nil
}
