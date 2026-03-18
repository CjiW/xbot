package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
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
		// 优先使用 CurrentDir（PWD 工具优化）
		if toolCtx.CurrentDir != "" {
			workspaceRoot = toolCtx.CurrentDir
		} else if toolCtx.WorkspaceRoot != "" {
			workspaceRoot = toolCtx.WorkspaceRoot
		} else {
			workspaceRoot = toolCtx.WorkingDir
		}
		userID = toolCtx.SenderID
	}

	// 使用全局沙箱实例
	sandbox := GetSandbox()

	// 获取容器默认 shell 并使用 login shell 执行命令
	// 这样可以自动加载 /etc/profile, ~/.bashrc 等配置文件
	shell, err := sandbox.GetShell(userID, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to get shell: %w", err)
	}

	// 使用 login shell 自动加载环境配置
	cmdName, cmdArgs, err := sandbox.Wrap(shell, []string{"-l", "-c", params.Command}, nil, workspaceRoot, userID)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, cmdName, cmdArgs...)

	// 审计日志：记录每次 shell 执行
	log.WithFields(log.Fields{
		"command": params.Command,
		"timeout": timeout,
	}).Debug("Shell command executing")

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
			killProcess(cmd)
			if result != "" {
				return NewErrorResult(fmt.Sprintf("[TIMEOUT after %s] Partial output:\n%s", timeout, result)), nil
			}
			return NewErrorResult(fmt.Sprintf("[TIMEOUT after %s] Command timed out with no output. The command may be waiting for input or running too long.", timeout)), nil
		}
		if result != "" {
			return NewErrorResult(fmt.Sprintf("[EXIT %s]\n%s", err, result)), nil
		}
		return nil, fmt.Errorf("command failed: %w", err)
	}

	if result == "" {
		// 解析 cd 命令并更新 cwd（PWD 工具优化）
		t.maybeUpdateCwd(toolCtx, params.Command, workspaceRoot)
		if envPersisted {
			return NewResult("Command executed successfully. Environment variables persisted to ~/.xbot_env"), nil
		}
		// 检测 cd 命令，添加提醒
		if containsCdCommand(params.Command) {
			return NewResult("Command executed successfully (no output).\n\n💡 提示：Shell 中的 cd 命令在下一条命令时会失效。请使用 PWD 工具切换目录。"), nil
		}
		return NewResult("Command executed successfully (no output)"), nil
	}

	// 解析 cd 命令并更新 cwd（PWD 工具优化）
	t.maybeUpdateCwd(toolCtx, params.Command, workspaceRoot)

	if envPersisted {
		result += "\n[Environment variables persisted to ~/.xbot_env]"
	}

	// 检测 cd 命令，添加提醒
	if containsCdCommand(params.Command) {
		result += "\n\n💡 提示：Shell 中的 cd 命令在下一条命令时会失效。请使用 PWD 工具切换目录。"
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

	// 确保 ~/.bashrc 包含 source ~/.xbot_env
	// 这样 bash -l 会自动加载环境变量
	ensureBashrcCmd := `grep -q 'source ~/.xbot_env' ~/.bashrc 2>/dev/null || echo -e '\n# Source xbot environment variables\n[ -f ~/.xbot_env ] && source ~/.xbot_env' >> ~/.bashrc`
	RunInSandboxWithShell(toolCtx, ensureBashrcCmd)

	return true
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

// maybeUpdateCwd 解析命令中的 cd 操作，更新 session 中的 cwd（PWD 工具优化）
// 支持：
//   - cd /path
//   - cd subdir
//   - cd ..
//   - cd dir && other_command
//   - cd dir; other_command
func (t *ShellTool) maybeUpdateCwd(toolCtx *ToolContext, command, currentDir string) {
	if toolCtx == nil || toolCtx.SetCurrentDir == nil || currentDir == "" {
		return
	}

	// 提取 cd 命令的目标路径
	targetDir := extractCdTarget(command)
	if targetDir == "" {
		return
	}

	// 计算绝对路径
	var absDir string
	if filepath.IsAbs(targetDir) {
		absDir = filepath.Clean(targetDir)
	} else {
		absDir = filepath.Clean(filepath.Join(currentDir, targetDir))
	}

	// 验证路径在工作区内（防止目录穿越）
	// 获取工作区根目录用于验证
	workspaceRoot := toolCtx.WorkspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = toolCtx.WorkingDir
	}
	if workspaceRoot == "" {
		return
	}

	// 确保最终路径在工作区内
	rel, err := filepath.Rel(workspaceRoot, absDir)
	if err != nil || strings.HasPrefix(rel, "..") {
		log.WithFields(log.Fields{
			"target_dir":     targetDir,
			"resolved_dir":   absDir,
			"workspace_root": workspaceRoot,
		}).Debug("CD command targets directory outside workspace, ignoring")
		return
	}

	// 更新 session 中的 cwd
	log.WithFields(log.Fields{
		"old_dir": currentDir,
		"new_dir": absDir,
	}).Debug("Updating cwd from cd command")
	toolCtx.SetCurrentDir(absDir)
}

// cdPattern 匹配 cd 命令的正则表达式
// 支持: cd /path, cd path, cd .., cd ~, cd $HOME 等
var cdPattern = regexp.MustCompile(`(?:^|&&|;)\s*cd\s+([^\s;&|]+|"[^"]+"|'[^']+')`)

// extractCdTarget 从命令中提取 cd 的目标路径
// 返回空字符串表示没有找到有效的 cd 命令
func extractCdTarget(command string) string {
	matches := cdPattern.FindAllStringSubmatch(command, -1)
	if len(matches) == 0 {
		return ""
	}

	// 取最后一个 cd 命令（如果有多个）
	lastMatch := matches[len(matches)-1]
	target := lastMatch[1]

	// 去除引号
	if len(target) >= 2 {
		if (target[0] == '"' && target[len(target)-1] == '"') ||
			(target[0] == '\'' && target[len(target)-1] == '\'') {
			target = target[1 : len(target)-1]
		}
	}

	// 处理 ~ 和 $HOME
	if target == "~" || target == "$HOME" {
		// 返回空，让调用方处理（保持在当前目录或工作区根目录）
		return ""
	}
	if strings.HasPrefix(target, "~/") {
		// ~/path -> 暂不处理，返回空
		return ""
	}

	return target
}

// containsCdCommand 检测命令中是否包含 cd（不解析，只提醒）
func containsCdCommand(command string) bool {
	// 简单匹配：cd 后跟空格
	return strings.Contains(command, "cd ")
}
