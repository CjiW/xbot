package tools

import (
	"fmt"
	"strings"
	"time"
)

// RunInSandbox 在沙箱内执行命令并返回输出。
// 当沙箱为 none 模式时返回错误。
func RunInSandbox(ctx *ToolContext, command string, args ...string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}
	if sandbox.Name() == "none" {
		return "", fmt.Errorf("sandbox not enabled")
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	spec := ExecSpec{
		Command: command,
		Args:    append([]string{command}, args...),
		Shell:   false,
		Timeout: 30 * time.Second,
		UserID:  userID,
	}
	setSandboxDir(ctx, sandbox, &spec)

	result, err := sandbox.Exec(ctx.Ctx, spec)
	if err != nil {
		return "", err
	}
	return formatExecResult(result), nil
}

// RunInSandboxWithShell 在沙箱内执行 shell 命令并返回输出。
// 使用 login shell 自动加载环境变量配置文件。
func RunInSandboxWithShell(ctx *ToolContext, shellCmd string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}
	if sandbox.Name() == "none" {
		return "", fmt.Errorf("sandbox not enabled")
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	// 获取默认 shell
	workspaceRoot := ctx.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = ctx.WorkingDir
	}
	shell, err := sandbox.GetShell(userID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	spec := ExecSpec{
		Command: shell,
		Args:    []string{shell, "-l", "-c", shellCmd},
		Shell:   false,
		Timeout: 30 * time.Second,
		UserID:  userID,
	}
	setSandboxDir(ctx, sandbox, &spec)

	result, err := sandbox.Exec(ctx.Ctx, spec)
	if err != nil {
		return "", err
	}
	return formatExecResult(result), nil
}

// RunInSandboxRaw 在沙箱内执行命令并返回原始输出（不做 TrimSpace）。
// 适用于需要保留文件原始内容的场景（如 cat 读取文件）。
func RunInSandboxRaw(ctx *ToolContext, command string, args ...string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}
	if sandbox.Name() == "none" {
		return "", fmt.Errorf("sandbox not enabled")
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	spec := ExecSpec{
		Command: command,
		Args:    append([]string{command}, args...),
		Shell:   false,
		Timeout: 30 * time.Second,
		UserID:  userID,
	}
	setSandboxDir(ctx, sandbox, &spec)

	result, err := sandbox.Exec(ctx.Ctx, spec)
	if err != nil {
		return "", err
	}
	return formatExecResultRaw(result), nil
}

// RunInSandboxRawWithShell 在沙箱内执行 shell 命令并返回原始输出（不做 TrimSpace）。
func RunInSandboxRawWithShell(ctx *ToolContext, shellCmd string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("sandbox not enabled")
	}
	sandbox := ctx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}
	if sandbox.Name() == "none" {
		return "", fmt.Errorf("sandbox not enabled")
	}

	userID := ctx.OriginUserID
	if userID == "" {
		userID = ctx.SenderID
	}

	workspaceRoot := ctx.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = ctx.WorkingDir
	}
	shell, err := sandbox.GetShell(userID, workspaceRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get shell: %w", err)
	}

	spec := ExecSpec{
		Command: shell,
		Args:    []string{shell, "-l", "-c", shellCmd},
		Shell:   false,
		Timeout: 30 * time.Second,
		UserID:  userID,
	}
	setSandboxDir(ctx, sandbox, &spec)

	result, err := sandbox.Exec(ctx.Ctx, spec)
	if err != nil {
		return "", err
	}
	return formatExecResultRaw(result), nil
}

// setSandboxDir 根据 sandbox 模式设置 ExecSpec 的 Dir 和 Workspace 字段。
func setSandboxDir(ctx *ToolContext, sandbox Sandbox, spec *ExecSpec) {
	switch sandbox.Name() {
	case "docker":
		spec.Workspace = ctx.WorkspaceRoot
		spec.Dir = ctx.Sandbox.Workspace(ctx.OriginUserID)
	case "remote":
		// 不设 Dir — runner 默认使用其 workspace
	case "none":
		spec.Dir = ctx.WorkspaceRoot
	}
}

// formatExecResult 格式化 ExecResult 为 TrimSpace 后的字符串。
// 非零退出码时返回 error。
func formatExecResult(result *ExecResult) string {
	output := strings.TrimSpace(result.Stdout)
	if result.Stderr != "" {
		if output != "" {
			output += "\n[stderr] " + result.Stderr
		} else {
			output = "[stderr] " + result.Stderr
		}
	}
	return output
}

// formatExecResultRaw 格式化 ExecResult 为原始字符串（不做 TrimSpace）。
func formatExecResultRaw(result *ExecResult) string {
	output := result.Stdout
	if result.Stderr != "" {
		if output != "" {
			output += "\n[stderr] " + result.Stderr
		} else {
			output = "[stderr] " + result.Stderr
		}
	}
	return output
}
