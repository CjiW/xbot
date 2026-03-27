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
	bangOutputMaxLen = 4000
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

// handleBangCommand executes a quick shell command (triggered by `!` prefix)
// and returns the result directly, bypassing LLM.
func (a *Agent) handleBangCommand(ctx context.Context, msg bus.InboundMessage, command string) (*bus.OutboundMessage, error) {
	log.WithFields(log.Fields{
		"channel": msg.Channel,
		"sender":  msg.SenderID,
		"command": tools.Truncate(command, 80),
	}).Info("Bang command")

	workspaceRoot := a.sandboxWorkspace(msg.SenderID)
	if err := a.ensureWorkspace(ctx, workspaceRoot, msg.SenderID); err != nil {
		return nil, fmt.Errorf("create user workspace: %w", err)
	}

	output, exitErr := a.executeBangCommand(ctx, command, workspaceRoot, msg.SenderID)

	// Format result
	content := formatBangOutput(command, output, exitErr)

	// If output is too long, write to a .md file and send as file link
	if len([]rune(content)) > bangOutputMaxLen {
		filePath, err := a.writeBangOutputFile(ctx, workspaceRoot, command, output, exitErr, msg.SenderID)
		if err != nil {
			log.WithError(err).Warn("Failed to write bang output file, sending truncated")
			// Truncate and send inline
			runes := []rune(content)
			content = string(runes[:bangOutputMaxLen-100]) + "\n...\n(output truncated, full output write failed)"
		} else {
			fileName := filepath.Base(filePath)
			content = fmt.Sprintf("[%s](%s)", fileName, filePath)
		}
	}

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: content,
	}, nil
}

// executeBangCommand runs the command in the user's sandbox (or locally if sandbox is disabled).
// Both paths use login shell (bash -l -c) via Sandbox.Exec for consistent behavior.
// workspaceRoot is the sandbox-internal path for file operations.
func (a *Agent) executeBangCommand(ctx context.Context, command, workspaceRoot, senderID string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, bangDefaultTimeout)
	defer cancel()

	sandbox := tools.GetSandbox()
	// Resolve per-user sandbox for correct Name() routing
	if resolver, ok := sandbox.(tools.SandboxResolver); ok {
		sandbox = resolver.SandboxForUser(senderID)
	}

	// Get the container/system default shell
	shell, err := sandbox.GetShell(senderID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	// Determine working directory based on sandbox mode
	var dir string
	switch sandbox.Name() {
	case "docker":
		dir = "/workspace"
	case "remote":
		// Don't set dir -- runner defaults to its workspace
	default:
		dir = workspaceRoot
	}

	spec := tools.ExecSpec{
		Command: shell,
		Args:    []string{shell, "-l", "-c", command},
		Shell:   false,
		Dir:     dir,
		Timeout: bangDefaultTimeout,
		UserID:  senderID,
	}
	// For docker mount validation, Workspace must be the host path.
	if sandbox.Name() == "docker" {
		spec.Workspace = a.workspaceRoot(senderID)
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
