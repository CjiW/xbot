package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"xbot/tools"
)

// CommandExecutor runs shell commands as hook handlers.
// It executes the command from HookDef.Command via "sh -c", passes event
// payload as JSON on stdin, and interprets the exit code to produce a Result.
type CommandExecutor struct {
	xbotHome   string // $XBOT_HOME value
	projectDir string // $XBOT_PROJECT_DIR value
}

// NewCommandExecutor creates a new CommandExecutor with the given home and
// project directory paths.
func NewCommandExecutor(xbotHome, projectDir string) *CommandExecutor {
	return &CommandExecutor{
		xbotHome:   xbotHome,
		projectDir: projectDir,
	}
}

// Type returns "command".
func (e *CommandExecutor) Type() string { return "command" }

// Execute runs the shell command defined in def.Command.
//
// Execution flow:
//  1. Determine timeout: def.Timeout > 0, otherwise default 30s.
//  2. Create a child process via "sh -c <command>" with XBOT_* env vars.
//  3. Pipe event.Payload() as JSON to stdin.
//  4. Capture stdout and stderr separately.
//  5. Interpret exit code:
//     - 0: success; parse stdout as JSON Result if possible, else treat as Context.
//     - 2: blocking deny; stderr used as Reason, Decision="deny".
//     - other: non-blocking error; Decision="allow", stderr recorded.
func (e *CommandExecutor) Execute(ctx context.Context, def *HookDef, event Event) (*Result, error) {
	// 1. Determine timeout.
	timeout := 30 * time.Second
	if def.Timeout > 0 {
		timeout = time.Duration(def.Timeout) * time.Second
	}

	// 2. Create context with timeout.
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 3. Build command.
	cmd := exec.Command("sh", "-c", def.Command)
	// Use process group so timeout can kill the entire tree (shell + children).
	tools.SetProcessAttrs(cmd)

	// 4. Set environment variables — inherit current + add XBOT_* vars.
	cmd.Env = append(cmd.Environ(),
		"XBOT_HOME="+e.xbotHome,
		"XBOT_PROJECT_DIR="+e.projectDir,
	)
	if sid, ok := event.Payload()["session_id"].(string); ok {
		cmd.Env = append(cmd.Env, "XBOT_SESSION_ID="+sid)
	}

	// 5. Pass event payload as JSON via stdin.
	stdinBytes, err := json.Marshal(event.Payload())
	if err != nil {
		return nil, fmt.Errorf("marshal event payload: %w", err)
	}
	cmd.Stdin = bytes.NewReader(stdinBytes)

	// 6. Capture stdout and stderr separately.
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// 7. Run the command.
	var exitErr error
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start command: %w", err)
	}

	// Wait for command or context cancellation.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case exitErr = <-waitCh:
		// Command finished normally.
	case <-cmdCtx.Done():
		// Timeout — kill the entire process group.
		tools.KillProcess(cmd)
		<-waitCh // drain Wait
		return nil, cmdCtx.Err()
	}

	stdoutStr := stdout.String()
	stderrStr := stderr.String()

	// Check for context timeout/cancellation first.
	if cmdCtx.Err() != nil {
		return nil, cmdCtx.Err()
	}

	// Determine exit code.
	exitCode := 0
	if exitErr != nil {
		if exitCodeErr, ok := exitErr.(interface{ ExitCode() int }); ok {
			exitCode = exitCodeErr.ExitCode()
		} else {
			return nil, exitErr
		}
	}

	// 8. Interpret result based on exit code.
	switch exitCode {
	case 0:
		return parseSuccessResult(stdoutStr, stderrStr), nil
	case 2:
		return &Result{
			ExitCode: exitCode,
			Stdout:   stdoutStr,
			Stderr:   stderrStr,
			Decision: "deny",
			Reason:   stderrStr,
		}, nil
	default:
		return &Result{
			ExitCode: exitCode,
			Stdout:   stdoutStr,
			Stderr:   stderrStr,
			Decision: "allow",
			Reason:   stderrStr,
		}, nil
	}
}

// parseSuccessResult tries to decode stdout as a JSON object with hook result
// fields (decision, reason, updatedInput, context). If stdout is not valid
// JSON, it is returned as the Context field of the Result.
func parseSuccessResult(stdout, stderr string) *Result {
	result := &Result{
		ExitCode: 0,
		Stdout:   stdout,
		Stderr:   stderr,
		Decision: "allow",
	}

	stdout = trimSpace(stdout)
	if stdout == "" {
		return result
	}

	// Try to parse as JSON.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		// Not valid JSON — treat stdout as context.
		result.Context = stdout
		return result
	}

	// Extract known fields.
	if v, ok := raw["decision"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			result.Decision = s
		}
	}
	if v, ok := raw["reason"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			result.Reason = s
		}
	}
	if v, ok := raw["context"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			result.Context = s
		}
	}
	if v, ok := raw["updatedInput"]; ok {
		var m map[string]any
		if json.Unmarshal(v, &m) == nil {
			result.UpdatedInput = m
		}
	}

	return result
}

// trimSpace trims whitespace from a string.
func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '\n' || s[0] == '\r') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
