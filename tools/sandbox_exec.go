package tools

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"time"
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

	// 使用 OriginUserID 作为沙箱用户标识（基于原始用户隔离）
	sandboxUserID := ctx.OriginUserID
	if sandboxUserID == "" {
		sandboxUserID = ctx.SenderID // fallback：兼容旧数据
	}
	sandbox := GetSandbox()
	cmdName, cmdArgs, err := sandbox.Wrap(command, args, nil, workspaceRoot, sandboxUserID)
	if err != nil {
		return "", fmt.Errorf("wrap command for sandbox: %w", err)
	}

	cmd := exec.CommandContext(ctx.Ctx, cmdName, cmdArgs...)
	cmd.Dir = workspaceRoot
	cmd.Stdin = nil

	// 使用平台特定的进程属性设置（Setpgid），超时时可以杀掉整棵进程树
	setProcessAttrs(cmd)
	// Cancel 回调：context 超时/取消时 kill 整个进程组
	cmd.Cancel = func() error {
		killProcess(cmd)
		return nil
	}
	// WaitDelay：Cancel 后最多等 5 秒让 I/O drain，然后强制关闭 pipe 使 Wait 返回
	cmd.WaitDelay = 5 * time.Second

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

	// 使用 OriginUserID 作为沙箱用户标识（基于原始用户隔离）
	sandboxUserID := ctx.OriginUserID
	if sandboxUserID == "" {
		sandboxUserID = ctx.SenderID // fallback：兼容旧数据
	}
	sandbox := GetSandbox()

	// 获取容器默认 shell 并使用 login shell 执行命令
	shell, err := sandbox.GetShell(sandboxUserID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	// 使用 login shell 自动加载环境配置
	return RunInSandbox(ctx, shell, "-l", "-c", shellCmd)
}

// RunInSandboxRaw 在沙箱容器内执行命令并返回原始输出（不做 TrimSpace）
// 适用于需要保留文件原始内容的场景（如 cat 读取文件）
func RunInSandboxRaw(ctx *ToolContext, command string, args ...string) (string, error) {
	if ctx == nil || !ctx.SandboxEnabled {
		return "", fmt.Errorf("sandbox not enabled")
	}

	workspaceRoot := ctx.WorkspaceRoot
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root not set")
	}

	sandboxUserID := ctx.OriginUserID
	if sandboxUserID == "" {
		sandboxUserID = ctx.SenderID
	}
	sandbox := GetSandbox()
	cmdName, cmdArgs, err := sandbox.Wrap(command, args, nil, workspaceRoot, sandboxUserID)
	if err != nil {
		return "", fmt.Errorf("wrap command for sandbox: %w", err)
	}

	cmd := exec.CommandContext(ctx.Ctx, cmdName, cmdArgs...)
	cmd.Dir = workspaceRoot
	cmd.Stdin = nil

	setProcessAttrs(cmd)
	cmd.Cancel = func() error {
		killProcess(cmd)
		return nil
	}
	cmd.WaitDelay = 5 * time.Second

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

	return stdout.String(), nil
}

// RunInSandboxRawWithShell 在沙箱容器内执行 shell 命令并返回原始输出（不做 TrimSpace）
func RunInSandboxRawWithShell(ctx *ToolContext, shellCmd string) (string, error) {
	if ctx == nil || !ctx.SandboxEnabled {
		return "", fmt.Errorf("sandbox not enabled")
	}

	workspaceRoot := ctx.WorkspaceRoot
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root not set")
	}

	sandboxUserID := ctx.OriginUserID
	if sandboxUserID == "" {
		sandboxUserID = ctx.SenderID
	}
	sandbox := GetSandbox()

	shell, err := sandbox.GetShell(sandboxUserID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	return RunInSandboxRaw(ctx, shell, "-l", "-c", shellCmd)
}
