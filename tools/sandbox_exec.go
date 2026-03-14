package tools

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// RunInSandbox 在沙箱容器内执行命令并返回输出
// 当未启用沙箱时返回错误
func RunInSandbox(ctx *ToolContext, command string, args ...string) (string, error) {
	if ctx == nil || !ctx.SandboxEnabled {
		return "", fmt.Errorf("sandbox not enabled")
	}

	workspaceRoot := ctx.WorkspaceRoot
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root not set")
	}

	sandbox := GetSandbox()
	cmdName, cmdArgs, err := sandbox.Wrap(command, args, nil, workspaceRoot, ctx.SenderID)
	if err != nil {
		return "", fmt.Errorf("wrap command for sandbox: %w", err)
	}

	cmd := exec.CommandContext(ctx.Ctx, cmdName, cmdArgs...)
	cmd.Dir = workspaceRoot
	cmd.Stdin = nil

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		output := stdout.String()
		if stderr.Len() > 0 {
			output += "\n[stderr] " + stderr.String()
		}
		return output, fmt.Errorf("sandbox command failed: %w, output: %s", err, output)
	}

	return strings.TrimSpace(stdout.String()), nil
}

// RunInSandboxWithShell 在沙箱容器内执行 shell 命令并返回输出
// 使用 login shell 自动加载环境变量配置文件
func RunInSandboxWithShell(ctx *ToolContext, shellCmd string) (string, error) {
	if ctx == nil || !ctx.SandboxEnabled {
		return "", fmt.Errorf("sandbox not enabled")
	}

	workspaceRoot := ctx.WorkspaceRoot
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root not set")
	}

	sandbox := GetSandbox()

	// 获取容器默认 shell 并使用 login shell 执行命令
	shell, err := sandbox.GetShell(ctx.SenderID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	// 使用 login shell 自动加载环境配置
	return RunInSandbox(ctx, shell, "-l", "-c", shellCmd)
}
