package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
	"xbot/llm"
)

const defaultShellTimeout = 120 * time.Second

// ShellTool 执行命令工具
type ShellTool struct{}

func (t *ShellTool) Name() string {
	return "Shell"
}

func (t *ShellTool) Description() string {
	return `Execute a command and return its output.
The command will be executed in the agent's working directory.
IMPORTANT: Commands are executed non-interactively with a timeout. Do NOT run interactive commands (e.g. vim, top, htop) or commands that require manual input. For commands that might prompt for input, use non-interactive flags (e.g. "apt-get -y", "yes |", "ssh -o BatchMode=yes"). For sudo, use NOPASSWD or "echo password | sudo -S".
Parameters (JSON):
  - command: string, the command to execute
  - timeout: number (optional), timeout in seconds (default: 120)
Example: {"command": "ls -la"}`
}

func (t *ShellTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "command", Type: "string", Description: "The command to execute", Required: true},
		{Name: "timeout", Type: "number", Description: "Timeout in seconds (default: 120)", Required: false},
	}
}

func (t *ShellTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Command string  `json:"command"`
		Timeout float64 `json:"timeout"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	if params.Command == "" {
		return nil, fmt.Errorf("command is required")
	}

	timeout := defaultShellTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
	}

	// 使用传入的 context 作为父 context，支持外部取消（如用户 stop）
	parentCtx := context.Background()
	if toolCtx != nil && toolCtx.Ctx != nil {
		parentCtx = toolCtx.Ctx
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	workspaceRoot := ""
	sandboxEnabled := true
	if toolCtx != nil {
		if toolCtx.WorkspaceRoot != "" {
			workspaceRoot = toolCtx.WorkspaceRoot
		} else {
			workspaceRoot = toolCtx.WorkingDir
		}
		sandboxEnabled = toolCtx.SandboxEnabled
	}

	var cmd *exec.Cmd
	if sandboxEnabled {
		cmdName, cmdArgs, err := shellWrapForSandbox(params.Command, workspaceRoot)
		if err != nil {
			return nil, err
		}
		cmd = exec.CommandContext(ctx, cmdName, cmdArgs...)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", params.Command)
	}

	if workspaceRoot != "" {
		cmd.Dir = workspaceRoot
	}

	// 关闭 stdin 防止交互式命令阻塞
	cmd.Stdin = nil

	// 使用平台特定的进程属性设置
	setProcessAttrs(cmd)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// 合并输出
	var resultBuilder strings.Builder
	if stdout.Len() > 0 {
		resultBuilder.Write(stdout.Bytes())
	}
	if stderr.Len() > 0 {
		if resultBuilder.Len() > 0 {
			resultBuilder.WriteString("\n")
		}
		resultBuilder.WriteString("[stderr] ")
		resultBuilder.Write(stderr.Bytes())
	}
	result := strings.TrimSpace(resultBuilder.String())

	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			// 超时：杀掉进程
			killProcess(cmd)
			if result != "" {
				return NewResult(fmt.Sprintf("[TIMEOUT after %s] Partial output:\n%s", timeout, result)), nil
			}
			return NewResult(fmt.Sprintf("[TIMEOUT after %s] Command timed out with no output. The command may be waiting for input or running too long.", timeout)), nil
		}
		// 命令执行失败但有输出（如 exit code != 0）
		if result != "" {
			return NewResult(fmt.Sprintf("[EXIT %s]\n%s", runErr, result)), nil
		}
		return nil, fmt.Errorf("command failed: %w", runErr)
	}

	if result == "" {
		return NewResult("Command executed successfully (no output)"), nil
	}

	return NewResult(result), nil
}
