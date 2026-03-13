package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"regexp"
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
	userID := ""
	if toolCtx != nil {
		if toolCtx.WorkspaceRoot != "" {
			workspaceRoot = toolCtx.WorkspaceRoot
		} else {
			workspaceRoot = toolCtx.WorkingDir
		}
		userID = toolCtx.SenderID
	}

	// 使用全局沙箱实例
	sandbox := GetSandbox()

	// 沙箱模式：在命令前 source 环境变量文件
	var wrappedCmd string
	if toolCtx != nil && toolCtx.SandboxEnabled {
		// ~/.xbot_env 存在则 source，不存在则忽略
		wrappedCmd = fmt.Sprintf("[ -f ~/.xbot_env ] && . ~/.xbot_env; %s", params.Command)
	} else {
		wrappedCmd = params.Command
	}

	cmdName, cmdArgs, err := sandbox.Wrap("sh", []string{"-c", wrappedCmd}, nil, workspaceRoot, userID)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)

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

	err = cmd.Run()

	// 沙箱模式：检测 export 命令并持久化环境变量
	var envPersisted bool
	if toolCtx != nil && toolCtx.SandboxEnabled {
		envPersisted = t.persistEnvFromCommand(toolCtx, params.Command)
	}

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

	if err != nil {
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
			return NewResult(fmt.Sprintf("[EXIT %s]\n%s", err, result)), nil
		}
		return nil, fmt.Errorf("command failed: %w", err)
	}

	if result == "" {
		if envPersisted {
			return NewResult("Command executed successfully. Environment variables persisted to ~/.xbot_env"), nil
		}
		return NewResult("Command executed successfully (no output)"), nil
	}

	if envPersisted {
		result += "\n[Environment variables persisted to ~/.xbot_env]"
	}

	return NewResult(result), nil
}

// persistEnvFromCommand 从命令中提取 export 语句并持久化到 ~/.xbot_env
func (t *ShellTool) persistEnvFromCommand(toolCtx *ToolContext, command string) bool {
	// 检测是否包含 export 命令（快速检查）
	if !strings.Contains(command, "export") {
		return false
	}

	// 提取 export 后面的所有 KEY=VALUE 对
	// 先匹配整个 export 语句，再解析其中的 KEY=VALUE
	exportPattern := regexp.MustCompile(`export\s+((?:[A-Za-z_][A-Za-z0-9_]*=\S+\s*)+)`)
	matches := exportPattern.FindAllStringSubmatch(command, -1)
	if len(matches) == 0 {
		return false
	}

	// 解析所有的 KEY=VALUE 对
	var exports []string
	kvPattern := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*=\S+)`)
	for _, match := range matches {
		if len(match) > 1 {
			kvMatches := kvPattern.FindAllString(match[1], -1)
			exports = append(exports, kvMatches...)
		}
	}

	if len(exports) == 0 {
		return false
	}

	// 读取现有的 ~/.xbot_env
	existing := ""
	readCmd := "[ -f ~/.xbot_env ] && cat ~/.xbot_env"
	if output, err := RunInSandboxWithShell(toolCtx, readCmd); err == nil {
		existing = output
	}

	// 合并环境变量（去重）
	envMap := make(map[string]string)
	for _, line := range strings.Split(existing, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// 添加新的环境变量
	for _, exp := range exports {
		parts := strings.SplitN(exp, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// 构建新的文件内容
	var lines []string
	lines = append(lines, "# Auto-generated by xbot - DO NOT EDIT MANUALLY")
	lines = append(lines, "# This file is sourced before each shell command")
	for k, v := range envMap {
		lines = append(lines, fmt.Sprintf("%s=%s", k, v))
	}
	newContent := strings.Join(lines, "\n")

	// 写入文件
	writeCmd := fmt.Sprintf("cat > ~/.xbot_env << 'XBOT_ENV_EOF'\n%s\nXBOT_ENV_EOF", newContent)
	if _, err := RunInSandboxWithShell(toolCtx, writeCmd); err != nil {
		return false
	}

	return true
}
