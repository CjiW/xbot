package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
	"xbot/llm"

	log "xbot/logger"
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
Example: {"command": "ls -la"}

Environment Variables:
- Commands run in a login shell (detected from container's /etc/passwd), which automatically sources /etc/profile, ~/.bash_profile, ~/.bashrc, etc.
- Use "export VAR=value" to set environment variables (auto-persisted to ~/.xbot_env)
- Or write directly: echo 'export PATH=$PATH:/new/path' >> ~/.xbot_env`
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

	// 检测命令中的控制字符和 null bytes
	if strings.ContainsAny(params.Command, "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f") {
		return nil, fmt.Errorf("command contains control characters (null bytes or other non-printable characters)")
	}

	// 安全预检：拦截危险命令
	if blocked, reason := checkDangerousCommand(params.Command); blocked {
		return nil, fmt.Errorf("command blocked by safety check: %s", reason)
	}

	const maxShellTimeout = 600 * time.Second

	timeout := defaultShellTimeout
	if params.Timeout > 0 {
		timeout = time.Duration(params.Timeout) * time.Second
		if timeout > maxShellTimeout {
			log.WithFields(log.Fields{
				"requested": timeout,
				"max":       maxShellTimeout,
			}).Warn("Shell timeout exceeds maximum, capping")
			timeout = maxShellTimeout
		}
	}

	// 使用传入的 context 作为父 context，支持外部取消（如用户 stop）
	parentCtx := context.Background()
	if toolCtx != nil && toolCtx.Ctx != nil {
		parentCtx = toolCtx.Ctx
	}
	ctx, cancel := context.WithTimeout(parentCtx, timeout)
	defer cancel()

	execDir := ""
	userID := ""
	if toolCtx != nil {
		// 优先使用 CurrentDir（PWD 工具优化）
		if toolCtx.CurrentDir != "" {
			execDir = toolCtx.CurrentDir
		} else if toolCtx.WorkspaceRoot != "" {
			execDir = toolCtx.WorkspaceRoot
		} else {
			execDir = toolCtx.WorkingDir
		}
		userID = toolCtx.OriginUserID
		if userID == "" {
			userID = toolCtx.SenderID // fallback
		}
	}

	// 使用全局沙箱实例
	sandbox := GetSandbox()

	// 获取容器默认 shell 并使用 login shell 执行命令
	// 这样可以自动加载 /etc/profile, ~/.bashrc 等配置文件
	shell, err := sandbox.GetShell(userID, execDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get shell: %w", err)
	}

	// 使用 login shell 自动加载环境配置
	cmdName, cmdArgs, err := sandbox.Wrap(shell, []string{"-l", "-c", params.Command}, nil, execDir, userID)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)

	// 审计日志：记录每次 shell 执行
	log.WithFields(log.Fields{
		"command": params.Command,
		"timeout": timeout,
	}).Debug("Shell command executing")

	if execDir != "" {
		cmd.Dir = execDir
	}

	// 关闭 stdin 防止交互式命令阻塞
	cmd.Stdin = nil

	// 使用平台特定的进程属性设置
	setProcessAttrs(cmd)

	// 设置 Cancel 回调：context 超时/取消时 kill 整个进程组（而非仅主进程）
	// 默认 exec.CommandContext 只 kill 主进程，在 docker exec / Setpgid 场景下子进程会残留导致 Wait 卡住
	cmd.Cancel = func() error {
		killProcess(cmd)
		return nil
	}
	// WaitDelay：Cancel 后最多等 5 秒让 I/O drain，然后强制关闭 pipe 使 Wait 返回
	cmd.WaitDelay = 5 * time.Second

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	// 命令执行完毕后，异步触发 export+import 持久化文件系统变更
	if toolCtx != nil && toolCtx.SandboxEnabled {
		sandbox.PostExec(userID)
	}

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
		if ctx.Err() == context.DeadlineExceeded || ctx.Err() == context.Canceled {
			timeoutErr := fmt.Sprintf("[TIMEOUT after %s] Command timed out", timeout)
			if result != "" {
				timeoutErr = fmt.Sprintf("[TIMEOUT after %s] Partial output:\n%s", timeout, result)
			}
			log.Ctx(ctx).WithFields(log.Fields{
				"command": params.Command,
				"timeout": timeout,
				"output":  result,
			}).Warn("Shell command timed out")
			return NewErrorResult(timeoutErr), nil
		}

		// 构建详细的错误信息，包含 exit code 和 stderr
		exitCode := cmd.ProcessState.ExitCode()
		stderrStr := strings.TrimSpace(stderr.String())

		var errMsg string
		if result != "" {
			// 有输出时保持原格式
			errMsg = fmt.Sprintf("[EXIT %d] %s\n%s", exitCode, params.Command, result)
		} else if stderrStr != "" {
			// 无标准输出但有 stderr 时，显示 stderr
			errMsg = fmt.Sprintf("[EXIT %d] %s\n[stderr] %s", exitCode, params.Command, stderrStr)
		} else {
			// 无任何输出时，显示命令和 exit code
			errMsg = fmt.Sprintf("[EXIT %d] %s (no output)", exitCode, params.Command)
		}

		// 打印错误日志，方便排查问题
		log.Ctx(ctx).WithFields(log.Fields{
			"command":  params.Command,
			"exitCode": exitCode,
			"stderr":   stderrStr,
		}).Warn("Shell command failed")

		return NewErrorResult(errMsg), nil
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

	res := NewResult(result)
	if tip := detectCdTip(params.Command); tip != "" {
		res = res.WithTips(tip)
	}
	return res, nil
}

// persistEnvFromCommand 从命令中提取 export 语句并持久化到 ~/.xbot_env
func (t *ShellTool) persistEnvFromCommand(toolCtx *ToolContext, command string) bool {
	// 检测是否包含 export 命令（快速检查）
	if !strings.Contains(command, "export") {
		return false
	}

	// 提取 export 后面的所有 KEY=VALUE 对
	// 先匹配整个 export 语句，再解析其中的 KEY=VALUE
	exports := parseExportStatements(command)
	if len(exports) == 0 {
		return false
	}

	// 读取现有的 ~/.xbot_env
	existing := ""
	readCmd := "cat ~/.xbot_env 2>/dev/null || true"
	if output, err := RunInSandboxWithShell(toolCtx, readCmd); err == nil {
		existing = output
	}

	// 合并环境变量（去重）
	envMap := parseEnvFileLines(existing)

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
	lines = append(lines, "# This file is sourced by ~/.bashrc")
	for k, v := range envMap {
		lines = append(lines, fmt.Sprintf("export %s=%s", k, v))
	}
	newContent := strings.Join(lines, "\n")

	// 写入文件
	writeCmd := fmt.Sprintf("cat > ~/.xbot_env << 'XBOT_ENV_EOF'\n%s\nXBOT_ENV_EOF", newContent)
	if _, err := RunInSandboxWithShell(toolCtx, writeCmd); err != nil {
		return false
	}

	// 确保 ~/.bashrc 在 non-interactive guard 之前 source ~/.xbot_env
	// bash -l 通过 /etc/profile → ~/.profile → . ~/.bashrc 链条加载 .bashrc，
	// 但 [ -z "$PS1" ] && return 会阻止非交互模式执行后续内容，
	// 所以 source 语句必须插在 early return 之前。
	ensureBashrcCmd := `# Remove existing source block (including adjacent blank lines)
if grep -q 'source ~/.xbot_env' ~/.bashrc 2>/dev/null; then
    sed -i '/# Source xbot environment variables/,/source ~\/\.xbot_env/d' ~/.bashrc
    # Clean up consecutive blank lines left by deletion
    sed -i '/^$/{ N; /^\n$/d; }' ~/.bashrc
fi

# Insert before PS1 guard if present, otherwise append to end (fallback for Alpine etc.)
if grep -q '\[ -z "\$PS1" \]' ~/.bashrc 2>/dev/null; then
    sed -i '/^\s*\[ -z "\$PS1" \]/i # Source xbot environment variables\n[ -f ~/.xbot_env ] \&\& source ~/.xbot_env\n' ~/.bashrc
elif ! grep -q 'source ~/.xbot_env' ~/.bashrc 2>/dev/null; then
    echo -e '\n# Source xbot environment variables\n[ -f ~/.xbot_env ] \&\& source ~/.xbot_env' >> ~/.bashrc
fi`
	RunInSandboxWithShell(toolCtx, ensureBashrcCmd)

	return true
}

// cdPattern detects standalone cd commands (not inside subshells, comments, or strings).
// Matches: "cd foo", "cd /path", "cd ..", "cd ~", as well as "cd foo && ls" etc.
var cdPattern = regexp.MustCompile(`(?:^|&&|\|\||;)\s*cd\s+`)

// detectCdTip returns a tip string if the command contains a cd that won't persist.
func detectCdTip(command string) string {
	if !cdPattern.MatchString(command) {
		return ""
	}
	return `NOTE: "cd" inside Shell only affects this single command — the working directory resets on the next tool call. Use the Cd tool to persistently change directory.`
}

// dangerPatterns 定义绝对禁止执行的命令模式（黑名单拦截，直接拒绝）
var dangerPatterns = []struct {
	pattern *regexp.Regexp
	reason  string
}{
	{regexp.MustCompile(`rm\s+-[^\s]*rf\s+/\s*`), "rm -rf / is destructive and will wipe the entire filesystem"},
	{regexp.MustCompile(`mkfs\b`), "mkfs will destroy filesystem data"},
	{regexp.MustCompile(`dd\s+.*(/dev/zero|/dev/random|/dev/null)\s+.*of=/dev/`), "dd writing to device is destructive"},
	{regexp.MustCompile(`:\(\)\s*\{.*\}\s*;`), "fork bomb detected"},
	{regexp.MustCompile(`chmod\s+777\s+/\s*`), "chmod 777 / is a security risk"},
	{regexp.MustCompile(`mv\s+/\s+/dev/null`), "mv / /dev/null is destructive"},
}

// warningPatterns 定义高危命令（告警但允许执行）
var warningPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\brm\s+(-[^\s]*rf|-rf)\b`),
	regexp.MustCompile(`\bdd\b`),
	regexp.MustCompile(`\bmkfs\b`),
	regexp.MustCompile(`\bchmod\s+777\b`),
	regexp.MustCompile(`\b(format| FORMAT)\b`),
}

// checkDangerousCommand 检查命令是否包含危险模式
// 返回 (blocked, reason)，如果 blocked=true 则应拒绝执行
func checkDangerousCommand(cmd string) (bool, string) {
	// 检查绝对禁止模式
	for _, dp := range dangerPatterns {
		if dp.pattern.MatchString(cmd) {
			return true, dp.reason
		}
	}

	// 检查高危告警模式（仅日志记录，不拦截）
	for _, wp := range warningPatterns {
		if wp.MatchString(cmd) {
			log.WithField("command", cmd).Warn("Dangerous command detected (allowed with warning)")
			break
		}
	}

	return false, ""
}
