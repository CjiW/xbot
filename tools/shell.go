package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

BACKGROUND MODE: Set "background": true to run long-running commands (dev servers, build processes) without blocking. Returns a task ID immediately. The agent continues working while the command runs in the background. When the command finishes, its output is automatically injected into the conversation. Use task_status to check progress, task_kill to terminate.

AUTO-BACKGROUND: If a command times out, it is automatically converted to a background task so no work is lost. The agent receives the task ID and can continue.

Parameters (JSON):
  - command: string, the command to execute
  - timeout: number (optional), timeout in seconds (default: 120, max: 600)
  - background: boolean (optional), run in background mode

Environment Variables:
- Commands run in a login shell (detected from container's /etc/passwd), which automatically sources /etc/profile, ~/.bash_profile, ~/.bashrc, etc.
- Use "export VAR=value" to set environment variables (auto-persisted to ~/.xbot_env)
- Or write directly: echo 'export PATH=$PATH:/new/path' >> ~/.xbot_env`
}

func (t *ShellTool) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "command", Type: "string", Description: "The command to execute", Required: true},
		{Name: "timeout", Type: "number", Description: "Timeout in seconds (default: 120, max: 600)", Required: false},
		{Name: "background", Type: "boolean", Description: "Run command in background (for long-running tasks like dev servers). Returns task ID immediately.", Required: false},
	}
}

func (t *ShellTool) Execute(toolCtx *ToolContext, input string) (*ToolResult, error) {
	var params struct {
		Command    string  `json:"command"`
		Timeout    float64 `json:"timeout"`
		Background bool    `json:"background"`
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

	userID := ""
	workspaceRoot := ""
	execDir := ""
	if toolCtx != nil {
		workspaceRoot = toolCtx.WorkspaceRoot
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

	// 沙箱模式：workspace 必须用宿主机路径（用于 bind mount / 容器查找），
	// 不能用容器内路径（CurrentDir），否则会导致容器 mount 校验失败并重建。
	sandboxWorkspace := workspaceRoot
	if sandboxWorkspace == "" {
		sandboxWorkspace = execDir
	}

	// 使用 ToolContext 中的沙箱实例（由 SandboxRouter 按用户路由注入）
	sandbox := toolCtx.Sandbox
	if sandbox == nil {
		sandbox = GetSandbox()
	}

	// 获取容器默认 shell 并使用 login shell 执行命令
	shell, err := sandbox.GetShell(userID, sandboxWorkspace)
	if err != nil {
		return nil, fmt.Errorf("failed to get shell: %w", err)
	}

	// 构建登录 shell 命令
	shellCmd := params.Command

	// 审计日志：记录每次 shell 执行
	log.WithFields(log.Fields{
		"command":    params.Command,
		"timeout":    timeout,
		"background": params.Background,
	}).Debug("Shell command executing")

	// Build ExecSpec based on sandbox mode
	buildSpec := func() ExecSpec {
		switch sandbox.Name() {
		case "docker":
			dir := ""
			if toolCtx != nil && toolCtx.CurrentDir != "" {
				dir = toolCtx.CurrentDir
			} else if toolCtx != nil && toolCtx.Sandbox != nil && toolCtx.Sandbox.Name() != "none" {
				dir = toolCtx.Sandbox.Workspace(toolCtx.OriginUserID)
			}
			return ExecSpec{
				Command:   shell,
				Args:      []string{shell, "-l", "-c", shellCmd},
				Shell:     false,
				Dir:       dir,
				Timeout:   timeout,
				Workspace: sandboxWorkspace,
				UserID:    userID,
			}
		case "remote":
			remoteDir := ""
			if toolCtx != nil && toolCtx.CurrentDir != "" {
				remoteDir = toolCtx.CurrentDir
			} else if rs, ok := sandbox.(*RemoteSandbox); ok {
				remoteDir = rs.Workspace(userID)
			}
			return ExecSpec{
				Command: shell,
				Args:    []string{shell, "-l", "-c", shellCmd},
				Shell:   false,
				Dir:     remoteDir,
				Timeout: timeout,
				UserID:  userID,
			}
		default:
			return ExecSpec{
				Command: shell,
				Args:    []string{shell, "-l", "-c", shellCmd},
				Shell:   false,
				Dir:     execDir,
				Timeout: timeout,
				UserID:  userID,
			}
		}
	}

	// Background mode: launch task in goroutine, return task ID immediately
	if params.Background {
		return t.executeBackground(toolCtx, shellCmd, sandbox, buildSpec)
	}

	// Foreground mode: synchronous execution with auto-promote on timeout
	return t.executeForeground(toolCtx, shellCmd, sandbox, parentCtx, timeout, buildSpec)
}

// executeBackground launches a command as a background task.
func (t *ShellTool) executeBackground(
	toolCtx *ToolContext,
	command string,
	sandbox Sandbox,
	buildSpec func() ExecSpec,
) (*ToolResult, error) {
	if toolCtx == nil || toolCtx.BgTaskManager == nil {
		return nil, fmt.Errorf("background tasks not supported (BgTaskManager not configured)")
	}

	sessionKey := toolCtx.BgSessionKey
	if sessionKey == "" {
		sessionKey = toolCtx.Channel + ":" + toolCtx.ChatID
	}

	task := toolCtx.BgTaskManager.Start(sessionKey, command,
		func(ctx context.Context, outputBuf func(string)) (int, error) {
			spec := buildSpec()
			spec.Timeout = 0 // no timeout for background
			return sandboxExecAsync(ctx, sandbox, spec, outputBuf)
		},
	)

	result := fmt.Sprintf(
		"Background task started [id: %s]\nCommand: %s\n\n"+
			"The task is running in the background. You can continue working.\n"+
			"When it completes, the output will be automatically injected into the conversation.\n"+
			"- Use task_status to check current progress\n"+
			"- Use task_kill to terminate the task",
		task.ID, task.Command,
	)

	return NewResultWithTips(result, fmt.Sprintf("Background task running: bg:%s", task.ID)), nil
}

// executeForeground runs a command synchronously. If it times out, auto-promotes
// to a background task (like Claude Code) so no work is lost.
func (t *ShellTool) executeForeground(
	toolCtx *ToolContext,
	command string,
	sandbox Sandbox,
	parentCtx context.Context,
	timeout time.Duration,
	buildSpec func() ExecSpec,
) (*ToolResult, error) {
	// Build spec with KeepAlive for none-sandbox so timeout doesn't kill the process.
	// The process can then be adopted by BgTaskManager on timeout.
	spec := buildSpec()
	if sandbox.Name() == "none" && toolCtx != nil && toolCtx.BgTaskManager != nil {
		spec.KeepAlive = true
	}
	result, err := sandbox.Exec(parentCtx, spec)

	if err != nil {
		return nil, fmt.Errorf("sandbox exec: %w", err)
	}

	// 检测 export 命令并持久化环境变量（docker + remote）
	var envPersisted bool
	sandboxName := sandbox.Name()
	if toolCtx != nil && (toolCtx.SandboxEnabled || sandboxName == "remote") {
		envPersisted = t.persistEnvFromCommand(toolCtx, command)
	}

	// 合并输出
	var resultBuilder strings.Builder
	if result.Stdout != "" {
		resultBuilder.WriteString(result.Stdout)
	}
	if result.Stderr != "" {
		if resultBuilder.Len() > 0 {
			resultBuilder.WriteString("\n")
		}
		resultBuilder.WriteString("[stderr] ")
		resultBuilder.WriteString(result.Stderr)
	}
	output := strings.TrimSpace(resultBuilder.String())

	if result.TimedOut {
		// AUTO-PROMOTE: convert timed-out command to background task
		if toolCtx != nil && toolCtx.BgTaskManager != nil {
			sessionKey := toolCtx.BgSessionKey
			if sessionKey == "" {
				sessionKey = toolCtx.Channel + ":" + toolCtx.ChatID
			}

			var task *BackgroundTask

			// If the sandbox supports KeepAlive and returned a live process,
			// adopt it (no re-execution) — the original process continues running.
			if result.Process != nil {
				partialOutput := output
				task = toolCtx.BgTaskManager.Adopt(sessionKey, command, result.Process, partialOutput, result.ExitCodeCh)
				log.WithFields(log.Fields{
					"command": command,
					"timeout": timeout,
					"task_id": task.ID,
				}).Info("Timed-out command adopted as background task (no re-exec)")
			} else {
				// Sandbox doesn't support KeepAlive (docker/remote) — fall back to re-execution.
				partialOutput := output
				task = toolCtx.BgTaskManager.Start(sessionKey, command,
					func(ctx context.Context, outputBuf func(string)) (int, error) {
						spec := buildSpec()
						spec.Timeout = 0
						if partialOutput != "" {
							outputBuf(partialOutput + "\n\n--- [restarted after timeout] ---\n")
						}
						return sandboxExecAsync(ctx, sandbox, spec, outputBuf)
					},
				)
				log.WithFields(log.Fields{
					"command": command,
					"timeout": timeout,
					"task_id": task.ID,
				}).Info("Timed-out command auto-promoted to background task (re-exec)")
			}

			timeoutMsg := fmt.Sprintf(
				"[TIMEOUT after %s] Command timed out. Auto-promoted to background task [id: %s]\n"+
					"Partial output before timeout:\n%s\n\n"+
					"The command continues running in the background. Its output will be injected when done.\n"+
					"- Use task_status to check progress\n"+
					"- Use task_kill to terminate",
				timeout, task.ID, output,
			)
			return NewResultWithTips(timeoutMsg, fmt.Sprintf("Auto-promoted to background: bg:%s", task.ID)), nil
		}

		// No BgTaskManager — fall back to old behavior
		timeoutErr := fmt.Sprintf("[TIMEOUT after %s] Command timed out", timeout)
		if output != "" {
			timeoutErr = fmt.Sprintf("[TIMEOUT after %s] Partial output:\n%s", timeout, output)
		}
		log.WithFields(log.Fields{
			"command": command,
			"timeout": timeout,
			"output":  output,
		}).Warn("Shell command timed out")
		return NewErrorResult(timeoutErr), nil
	}

	if result.ExitCode != 0 {
		var errMsg string
		if output != "" {
			errMsg = fmt.Sprintf("[EXIT %d] %s\n%s", result.ExitCode, command, output)
		} else if result.Stderr != "" {
			errMsg = fmt.Sprintf("[EXIT %d] %s\n[stderr] %s", result.ExitCode, command, result.Stderr)
		} else {
			errMsg = fmt.Sprintf("[EXIT %d] %s (no output)", result.ExitCode, command)
		}

		log.WithFields(log.Fields{
			"command":  command,
			"exitCode": result.ExitCode,
			"stderr":   result.Stderr,
		}).Warn("Shell command failed")

		return NewErrorResult(errMsg), nil
	}

	if output == "" {
		if envPersisted {
			return NewResult("Command executed successfully. Environment variables persisted to ~/.xbot_env"), nil
		}
		return NewResult("Command executed successfully (no output)"), nil
	}

	if envPersisted {
		output += "\n[Environment variables persisted to ~/.xbot_env]"
	}

	res := NewResult(output)
	if tip := detectCdTip(command); tip != "" {
		res = res.WithTips(tip)
	}
	return res, nil
}

// sandboxExecAsync runs a sandbox command asynchronously, streaming output via outputBuf.
func sandboxExecAsync(
	ctx context.Context,
	sandbox Sandbox,
	spec ExecSpec,
	outputBuf func(string),
) (int, error) {
	switch sandbox.Name() {
	case "none":
		return noneSandboxExecAsync(ctx, spec, outputBuf)
	default:
		// Docker/Remote: synchronous fallback (timeout=0 means no timeout)
		result, err := sandbox.Exec(ctx, spec)
		if outputBuf != nil && result != nil {
			if result.Stdout != "" {
				outputBuf(result.Stdout)
			}
			if result.Stderr != "" {
				outputBuf("[stderr] " + result.Stderr)
			}
		}
		if err != nil {
			if result != nil {
				return result.ExitCode, err
			}
			return -1, err
		}
		return result.ExitCode, nil
	}
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
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
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
		// Escape values for safe shell sourcing — prevent command injection via $(...), backticks, etc.
		sanitized := shellEscapeValue(v)
		lines = append(lines, fmt.Sprintf("export %s=%s", k, sanitized))
	}
	newContent := strings.Join(lines, "\n")

	// 写入文件（使用随机 heredoc 标记防止注入）
	randBytes := make([]byte, 16)
	if _, err := rand.Read(randBytes); err != nil {
		return false
	}
	heredocTag := "XBOT_ENV_" + hex.EncodeToString(randBytes)
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	writeCmd := fmt.Sprintf("cat > ~/.xbot_env << '%s'\n%s\n%s", heredocTag, newContent, heredocTag)
	if _, err := RunInSandboxWithShell(toolCtx, writeCmd); err != nil {
		return false
	}

	// 确保 ~/.bashrc 在 non-interactive guard 之前 source ~/.xbot_env
	// bash -l 通过 /etc/profile → ~/.profile → . ~/.bashrc 链条加载 .bashrc，
	// 但 [ -z "$PS1" ] && return 会阻止非交互模式执行后续内容，
	// 所以 source 语句必须插在 early return 之前。
	ensureBashrcCmd := `# Remove existing source block (including adjacent blank lines)
// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
if grep -q 'source ~/.xbot_env' ~/.bashrc 2>/dev/null; then
    // NOTE: .xbot is the server-side config directory; not accessible in user sandbox
    sed -i '/# Source xbot environment variables/,/source ~\/\.xbot_env/d' ~/.bashrc
    # Clean up consecutive blank lines left by deletion
    sed -i '/^$/{ N; /^\n$/d; }' ~/.bashrc
fi

# Insert before PS1 guard if present, otherwise append to end (fallback for Alpine etc.)
if grep -q '\[ -z "\$PS1" \]' ~/.bashrc 2>/dev/null; then
    // NOTE: .xbot is the server-side config directory; not accessible in user sandbox
    sed -i '/^\s*\[ -z "\$PS1" \]/i # Source xbot environment variables\n[ -f ~/.xbot_env ] && source ~/.xbot_env\n' ~/.bashrc
// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
elif ! grep -q 'source ~/.xbot_env' ~/.bashrc 2>/dev/null; then
    // NOTE: .xbot is the server-side config directory; not accessible in user sandbox
    echo -e '\n# Source xbot environment variables\n[ -f ~/.xbot_env ] && source ~/.xbot_env' >> ~/.bashrc
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

	// 检查裸 sudo（无 -S / -n / NOPASSWD）：在 none sandbox 下会打开 /dev/tty 导致终端卡死
	// 允许的写法: echo pass | sudo -S ..., sudo -n ..., sudo --non-interactive, NOPASSWD 配置
	// NOTE: Go regexp (RE2) 不支持 lookbehind/lookahead，用单词边界 + 代码逻辑代替
	sudoRe := regexp.MustCompile(`\bsudo\b`)
	if sudoRe.MatchString(cmd) {
		hasSafeFlag := regexp.MustCompile(`\bsudo\s+(-[Sn]\b|--non-interactive\b)`).MatchString(cmd)
		hasPipeOrNopasswd := strings.Contains(cmd, "|") || strings.Contains(cmd, "NOPASSWD")
		if !hasSafeFlag && !hasPipeOrNopasswd {
			return true, "bare sudo without -S/-n flag or password pipe will block the terminal (use \"echo password | sudo -S\" or configure NOPASSWD in /etc/sudoers)"
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

// shellEscapeValue escapes a value for safe inclusion in a shell variable assignment.
// Prevents command injection via $(...), backticks, \n, etc. when the value is
// later sourced by bash (e.g., from ~/.xbot_env).
func shellEscapeValue(v string) string {
	var b strings.Builder
	b.WriteByte('\'')
	for _, ch := range v {
		switch ch {
		case '\'':
			b.WriteString("'\\''")
		case '\n':
			// Newlines in single-quoted strings break the export statement;
			// replace with literal \n (two chars: backslash + n).
			b.WriteString("'\\n'")
		default:
			b.WriteRune(ch)
		}
	}
	b.WriteByte('\'')
	return b.String()
}
