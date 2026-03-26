package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
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

	workspaceRoot := a.workspaceRoot(msg.SenderID)
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
// Both paths use login shell (bash -l -c) via the sandbox infrastructure for consistent behavior.
func (a *Agent) executeBangCommand(ctx context.Context, command, workspaceRoot, senderID string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, bangDefaultTimeout)
	defer cancel()

	sandbox := tools.GetSandbox()

	// Get the container/system default shell
	shell, err := sandbox.GetShell(senderID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	// Use login shell (-l) to auto-source /etc/profile, ~/.bashrc, ~/.xbot_env, etc.
	cmdName, cmdArgs, err := sandbox.Wrap(shell, []string{"-l", "-c", command}, nil, workspaceRoot, senderID)
	if err != nil {
		return "", fmt.Errorf("wrap command: %w", err)
	}

	cmd := exec.CommandContext(execCtx, cmdName, cmdArgs...)
	cmd.Dir = workspaceRoot
	cmd.Stdin = nil

	// 使用平台特定的进程属性设置（Setpgid），超时时可以杀掉整棵进程树
	tools.SetProcessAttrs(cmd)
	// Cancel 回调：context 超时/取消时 kill 整个进程组
	cmd.Cancel = func() error {
		tools.KillProcess(cmd)
		return nil
	}
	// WaitDelay：Cancel 后最多等 5 秒让 I/O drain，然后强制关闭 pipe 使 Wait 返回
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	var result strings.Builder
	if stdout.Len() > 0 {
		result.Write(stdout.Bytes())
	}
	if stderr.Len() > 0 {
		if result.Len() > 0 {
			result.WriteString("\n")
		}
		result.WriteString("[stderr] ")
		result.Write(stderr.Bytes())
	}

	return strings.TrimSpace(result.String()), runErr
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
	} else {
		if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
			return "", err
		}
	}
	if a.sandbox != nil {
		if err := a.sandbox.WriteFile(ctx, filePath, []byte(buf.String()), 0o644, senderID); err != nil {
			return "", err
		}
	} else {
		if err := os.WriteFile(filePath, []byte(buf.String()), 0o644); err != nil {
			return "", err
		}
	}

	return filePath, nil
}
