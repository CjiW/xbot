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

	// 解析 export 语句，提取 KEY=VALUE 对
	exports := parseExportCommand(command)
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

// parseExportCommand 解析 export 命令，提取 KEY=VALUE 对
// 支持：KEY=value, KEY="value with spaces", KEY='value', KEY=$VAR, KEY=$PATH:/new
func parseExportCommand(command string) []string {
	var exports []string

	// 按行处理
	for _, line := range strings.Split(command, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "export ") {
			continue
		}

		// 去掉 export 前缀
		rest := strings.TrimPrefix(line, "export ")
		rest = strings.TrimSpace(rest)

		// 解析 KEY=VALUE 对
		exports = append(exports, parseKeyValuePairs(rest)...)
	}

	return exports
}

// parseKeyValuePairs 解析 export 后面的 KEY=VALUE 对
// 支持：VAR=value VAR="value with spaces" VAR='value' VAR=$VAR
func parseKeyValuePairs(s string) []string {
	var result []string

	for len(s) > 0 {
		s = strings.TrimSpace(s)
		if s == "" {
			break
		}

		// 找 KEY=
		eqIdx := strings.Index(s, "=")
		if eqIdx == -1 {
			break
		}

		key := s[:eqIdx]
		// 验证 key 是合法的变量名
		if !isValidVarName(key) {
			break
		}

		s = s[eqIdx+1:]

		// 解析 value
		value, remaining := parseValue(s)

		result = append(result, key+"="+value)
		s = remaining
	}

	return result
}

// parseValue 解析值部分，返回 (value, remaining)
func parseValue(s string) (string, string) {
	if len(s) == 0 {
		return "", ""
	}

	// 检查引号开头
	if s[0] == '"' {
		// 双引号：找到结束引号（处理转义）
		for i := 1; i < len(s); i++ {
			if s[i] == '"' && s[i-1] != '\\' {
				return s[:i+1], s[i+1:]
			}
		}
		// 没有结束引号，返回到末尾
		return s, ""
	}

	if s[0] == '\'' {
		// 单引号：找到结束引号（不处理转义）
		end := strings.Index(s[1:], "'")
		if end == -1 {
			return s, ""
		}
		return s[:end+2], s[end+2:]
	}

	// 无引号：遇到空格或下一个变量赋值结束
	// 但要处理 $VAR:/path 这种情况
	end := 0
	for end < len(s) {
		c := s[end]
		if c == ' ' || c == '\t' {
			break
		}
		// 检查是否是下一个 KEY=VALUE 的开始
		// 例如：PATH=/a GOPATH=/b 中间有空格
		// 或者：A=1B=2（没有空格，但 B 是新变量）
		if c == '=' && end > 0 {
			// 检查前面是否是合法的变量名
			potentialKey := ""
			for j := end - 1; j >= 0; j-- {
				ch := s[j]
				if isValidVarNameChar(ch) {
					potentialKey = string(ch) + potentialKey
				} else {
					break
				}
			}
			if isValidVarName(potentialKey) {
				// 这是下一个 KEY=VALUE，截断
				break
			}
		}
		end++
	}

	return s[:end], s[end:]
}

func isValidVarName(s string) bool {
	if len(s) == 0 {
		return false
	}
	if !isAlpha(s[0]) && s[0] != '_' {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isValidVarNameChar(s[i]) {
			return false
		}
	}
	return true
}

func isValidVarNameChar(c byte) bool {
	return isAlpha(c) || isDigit(c) || c == '_'
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool {
	return c >= '0' && c <= '9'
}
