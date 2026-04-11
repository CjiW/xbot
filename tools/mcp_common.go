package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	log "xbot/logger"

	"xbot/llm"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPServerConfig 单个 MCP Server 的配置
type MCPServerConfig struct {
	Command      string            `json:"command,omitempty"`      // 可执行文件路径（stdio 模式）
	Args         []string          `json:"args,omitempty"`         // 命令行参数（stdio 模式）
	Env          map[string]string `json:"env,omitempty"`          // 环境变量
	URL          string            `json:"url,omitempty"`          // HTTP MCP URL（http 模式）
	Headers      map[string]string `json:"headers,omitempty"`      // HTTP 请求头
	Enabled      *bool             `json:"enabled,omitempty"`      // 是否启用（默认 true）
	Instructions string            `json:"instructions,omitempty"` // MCP 服务器使用说明（fallback，当服务器不返回时使用）
}

// MCPConfig MCP 配置文件结构
type MCPConfig struct {
	MCPServers map[string]MCPServerConfig `json:"mcpServers"`
}

// mcpConnection MCP 连接封装
type mcpConnection struct {
	name         string
	session      *mcp.ClientSession
	tools        []*mcp.Tool
	instructions string // from server's InitializeResult
}

// MCPServerCatalogEntry 单个 MCP Server 的目录条目（用于系统提示词中的轻量展示）
type MCPServerCatalogEntry struct {
	Name         string   // Server 名称
	Instructions string   // Server 初始化返回的使用说明
	ToolNames    []string // 工具名称列表（不含参数信息）
}

// sharedMCPClient is a singleton MCP client shared across all connections.
// The official SDK separates Client (long-lived) from ClientSession (per-connection).
var sharedMCPClient = mcp.NewClient(&mcp.Implementation{
	Name:    "xbot",
	Version: "1.0.0",
}, nil)

// safeDefaultPATH is the standard system PATH used for MCP server processes
// instead of leaking the host's full PATH.
const safeDefaultPATH = "/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin"

// npmQuietEnv suppresses npm/npx diagnostic output (audit warnings, fund
// messages, update notices) that would otherwise contaminate the JSON-RPC
// stdio channel and cause parse errors.
var npmQuietEnv = []string{
	"NPM_CONFIG_UPDATE_NOTIFIER=false",
	"NPM_CONFIG_AUDIT=false",
	"NPM_CONFIG_FUND=false",
	"NPM_CONFIG_LOGLEVEL=error",
	"NO_UPDATE_NOTIFIER=1",
}

// BuildStdioEnv builds the env list from MCP config.
// PATH is built from .xbot/bin (if exists) + user-configured PATH (if any).
// buildMinimalExecEnv will merge this with the login shell's PATH.
func BuildStdioEnv(cfg MCPServerConfig, configPath string) []string {
	var envList []string
	for k, v := range cfg.Env {
		envList = append(envList, fmt.Sprintf("%s=%s", k, v))
	}

	// Suppress npm/npx diagnostic output to prevent stdout contamination.
	envList = append(envList, npmQuietEnv...)

	pathParts := []string{}
	if binDir := resolveXbotBinDir(configPath); binDir != "" {
		pathParts = append(pathParts, binDir)
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		log.WithField("bin_dir", binDir).Debug("Added .xbot/bin to MCP server PATH")
	}
	if len(pathParts) > 0 {
		envList = append(envList, fmt.Sprintf("PATH=%s", strings.Join(pathParts, ":")))
	}

	return envList
}

// secretKeyPrefixes are environment variable name prefixes that must never be forwarded
// to MCP server child processes (secrets, credentials, tokens).
var secretKeyPrefixes = []string{
	"LLM_", "OPENAI_", "ANTHROPIC_", "GEMINI_", "DEEPSEEK_",
	"FEISHU_", "QQ_", "DINGTALK_",
	"AWS_", "AZURE_", "GOOGLE_",
	"GITHUB_TOKEN", "GITLAB_TOKEN",
	"SLACK_", "DISCORD_", "TELEGRAM_",
	"DATABASE_URL", "REDIS_URL", "MONGO_",
}

func isSecretKey(key string) bool {
	for _, prefix := range secretKeyPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// buildMinimalExecEnv constructs the environment for MCP server processes.
// It uses a login shell (bash -l) to capture the full user environment (PATH, GOPATH,
// GOROOT, etc.), then filters out secret keys and merges MCP-configured env vars.
// For PATH specifically, MCP-configured PATH entries are prepended to the login shell PATH
// so that .xbot/bin and user overrides take priority without losing the full shell PATH.
func buildMinimalExecEnv(envList []string) []string {
	// 1. Capture full environment from login shell
	loginEnv := getLoginShellEnv()

	// 2. Build base env from login shell, filtering out secrets
	envMap := make(map[string]string, len(loginEnv)+len(envList))
	for _, e := range loginEnv {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			key := e[:idx]
			if !isSecretKey(key) {
				envMap[key] = e[idx+1:]
			}
		}
	}

	// 3. Merge MCP-configured env vars.
	// For PATH: prepend MCP PATH to login shell PATH (deduped).
	// For others: MCP value overrides login shell value.
	var mcpPATHParts []string
	for _, e := range envList {
		if idx := strings.IndexByte(e, '='); idx > 0 {
			key := e[:idx]
			val := e[idx+1:]
			if isSecretKey(key) {
				continue // never forward secrets
			}
			if key == "PATH" {
				// Collect MCP PATH parts; will be prepended later
				mcpPATHParts = append(mcpPATHParts, strings.Split(val, ":")...)
			} else {
				envMap[key] = val
			}
		}
	}

	// 4. Build final env list with merged PATH
	env := make([]string, 0, len(envMap)+1)

	// Collect login shell PATH parts
	var loginPATHParts []string
	if loginPATH, ok := envMap["PATH"]; ok {
		loginPATHParts = strings.Split(loginPATH, ":")
		delete(envMap, "PATH") // will be re-added with merged value
	}

	// Merge PATH: MCP parts first (highest priority), then login shell parts (deduped)
	seen := make(map[string]bool)
	var mergedParts []string
	for _, part := range mcpPATHParts {
		if part != "" && !seen[part] {
			mergedParts = append(mergedParts, part)
			seen[part] = true
		}
	}
	for _, part := range loginPATHParts {
		if part != "" && !seen[part] {
			mergedParts = append(mergedParts, part)
			seen[part] = true
		}
	}
	if len(mergedParts) == 0 {
		mergedParts = []string{safeDefaultPATH}
	}
	env = append(env, "PATH="+strings.Join(mergedParts, ":"))

	// Add remaining env vars (sorted for determinism)
	for k, v := range envMap {
		env = append(env, k+"="+v)
	}

	return env
}

// getLoginShellEnv runs a login shell command to capture the full environment.
// Unix: bash -l -c 'env -0'
// Windows: powershell.exe -Command "[Environment]::GetEnvironmentVariables() | ..."
// Returns empty slice on failure (caller will use fallback).
func getLoginShellEnv() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		// PowerShell: list env vars as KEY=VALUE lines
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command",
			"Get-ChildItem Env: | ForEach-Object { $_.Name + '=' + $_.Value }")
	} else {
		cmd = exec.CommandContext(ctx, "bash", "-l", "-c", "env -0")
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil // discard stderr (shell startup noise)

	if err := cmd.Run(); err != nil {
		log.WithError(err).Warn("Failed to capture login shell env for MCP, using safe defaults")
		return nil
	}

	// Parse output
	output := stdout.Bytes()
	if len(output) == 0 {
		return nil
	}

	var env []string
	if runtime.GOOS == "windows" {
		// PowerShell outputs newline-delimited KEY=VALUE lines
		scanner := bufio.NewScanner(bytes.NewReader(output))
		for scanner.Scan() {
			line := scanner.Text()
			if strings.IndexByte(line, '=') > 0 {
				env = append(env, line)
			}
		}
	} else {
		// Unix: null-delimited output
		for len(output) > 0 {
			idx := bytes.IndexByte(output, 0)
			if idx < 0 {
				idx = len(output)
			}
			line := string(output[:idx])
			if idx2 := strings.IndexByte(line, '='); idx2 > 0 {
				env = append(env, line)
			}
			if idx >= len(output) {
				break
			}
			output = output[idx+1:]
		}
	}

	sort.Strings(env)
	return env
}

// resolveXbotBinDir 从 configPath 推断 .xbot/bin 目录（存在才返回）

// resolveXbotBinDir 从 configPath 推断 .xbot/bin 目录（存在才返回）
func resolveXbotBinDir(configPath string) string {
	if configPath == "" {
		return ""
	}

	dir := filepath.Dir(configPath) // e.g. /workdir/.xbot 或 /workdir

	// 如果 configPath 在 .xbot/ 目录下，bin 目录就在同一级
	var binDir string
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	if strings.HasSuffix(dir, string(filepath.Separator)+".xbot") || filepath.Base(dir) == ".xbot" {
		binDir = filepath.Join(dir, "bin")
	} else {
		// configPath 在 workDir 根目录，如 /workdir/mcp.json
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		binDir = filepath.Join(dir, ".xbot", "bin")
	}

	// 仅在目录存在时返回
	if info, err := os.Stat(binDir); err == nil && info.IsDir() {
		return binDir
	}
	return ""
}

// shellQuoteCmd 将 command + args 转为 shell 安全的单行字符串（用单引号包裹）
func shellQuoteCmd(command string, args []string) string {
	quote := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
	}
	parts := []string{quote(command)}
	for _, a := range args {
		parts = append(parts, quote(a))
	}
	return strings.Join(parts, " ")
}

// ConnectStdioServer 连接 stdio 模式的 MCP Server（公共函数）
// Returns a ClientSession (auto-initialized) and the session itself for closing.
func ConnectStdioServer(ctx context.Context, cfg MCPServerConfig, configPath, workspaceRoot, userID, serverName string) (*mcp.ClientSession, error) {
	envList := BuildStdioEnv(cfg, configPath)

	sandbox := GetSandbox()

	// Resolve per-user sandbox if using a router.
	if resolver, ok := sandbox.(SandboxResolver); ok && userID != "" {
		sandbox = resolver.SandboxForUser(userID)
	}

	var execCmd *exec.Cmd
	switch sandbox.Name() {
	case "docker":
		shell, err := sandbox.GetShell(userID, workspaceRoot)
		if err != nil {
			return nil, fmt.Errorf("get shell for MCP: %w", err)
		}
		shellCmd := "exec " + shellQuoteCmd(cfg.Command, cfg.Args)
		if ds, ok := sandbox.(*DockerSandbox); ok {
			cmdName, cmdArgs, err := ds.Wrap(shell, []string{"-l", "-c", shellCmd}, envList, workspaceRoot, userID)
			if err != nil {
				return nil, err
			}
			execCmd = exec.Command(cmdName, cmdArgs...)
		} else {
			return nil, fmt.Errorf("MCP stdio not supported in %s mode", sandbox.Name())
		}
	case "remote":
		rs, ok := sandbox.(*RemoteSandbox)
		if !ok {
			return nil, fmt.Errorf("remote sandbox type assertion failed")
		}
		transport := &RemoteStdioTransport{
			Sandbox:    rs,
			UserID:     userID,
			StreamID:   generateID(),
			Command:    cfg.Command,
			Args:       cfg.Args,
			Env:        envList,
			Dir:        "",
			ServerName: serverName,
		}
		connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		session, err := sharedMCPClient.Connect(connectCtx, transport, nil)
		if err != nil {
			return nil, fmt.Errorf("connect remote stdio: %w", err)
		}
		return session, nil
	default:
		// None 模式：直接本地执行
		execCmd = exec.Command(cfg.Command, cfg.Args...)
	}

	// Build a minimal safe environment instead of passing os.Environ() which
	// would leak host secrets (LLM_API_KEY, FEISHU_APP_SECRET, etc.).
	execCmd.Env = buildMinimalExecEnv(envList)

	// StderrPipe returns a reader that is closed automatically when the process exits,
	// so the drainStderr goroutine will not leak.
	stderrPipe, err := execCmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}
	go drainStderr(serverName, stderrPipe)

	transport := &mcp.CommandTransport{
		Command:           execCmd,
		TerminateDuration: 5 * time.Second,
	}

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	session, err := sharedMCPClient.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect stdio: %w", err)
	}

	return session, nil
}

// drainStderr reads lines from an MCP server's stderr and logs them.
// The reader is expected to close when the process exits.
func drainStderr(serverName string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) != "" {
			log.WithField("mcp_server", serverName).Warn(line)
		}
	}
	if err := scanner.Err(); err != nil {
		log.WithError(err).WithField("mcp_server", serverName).Debug("stderr reader closed")
	}
}

// ConnectHTTPServer 连接 HTTP 模式的 MCP Server（公共函数）
func ConnectHTTPServer(ctx context.Context, cfg MCPServerConfig) (*mcp.ClientSession, error) {
	transport := &mcp.StreamableClientTransport{
		Endpoint: cfg.URL,
	}

	// Note: Headers are injected via custom HTTP client if needed.
	// The official SDK's StreamableClientTransport uses HTTPClient field.
	if len(cfg.Headers) > 0 {
		transport.HTTPClient = newHeaderInjectorClient(cfg.Headers)
	}

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	session, err := sharedMCPClient.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect HTTP: %w", err)
	}

	return session, nil
}

// IsProcessExitError 判断是否为子进程退出错误（如 "exit status 1"）
func IsProcessExitError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "exit status") || strings.Contains(errStr, "signal:")
}

// MCPInitResult holds the result of MCP client initialization.
type MCPInitResult struct {
	Tools        []*mcp.Tool
	Instructions string
}

// InitializeMCPClient lists tools and extracts server instructions from an already-connected session.
// With the official SDK, Connect() auto-initializes; this function collects the results.
func InitializeMCPClient(ctx context.Context, session *mcp.ClientSession) (*MCPInitResult, error) {
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var tools []*mcp.Tool
	for tool, err := range session.Tools(connectCtx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, tool)
	}

	var instructions string
	if initResult := session.InitializeResult(); initResult != nil {
		instructions = initResult.Instructions
	}

	return &MCPInitResult{
		Tools:        tools,
		Instructions: instructions,
	}, nil
}

// ConvertMCPParams 将 MCP 参数转换为 LLM ToolParam 格式
// The official SDK's Tool.InputSchema is `any` (client-side: map[string]any).
func ConvertMCPParams(tool *mcp.Tool) []llm.ToolParam {
	return convertMCPParams(tool)
}

// convertMCPParams 将 MCP Tool 的 JSON Schema 参数转为 xbot ToolParam 列表
func convertMCPParams(tool *mcp.Tool) []llm.ToolParam {
	schema, ok := tool.InputSchema.(map[string]any)
	if !ok {
		return nil
	}

	props, _ := schema["properties"].(map[string]any)
	if props == nil {
		return nil
	}

	// 构建 required 集合
	requiredSet := make(map[string]bool)
	if reqList, ok := schema["required"].([]any); ok {
		for _, r := range reqList {
			if s, ok := r.(string); ok {
				requiredSet[s] = true
			}
		}
	}

	var params []llm.ToolParam
	for name, propRaw := range props {
		propMap, ok := propRaw.(map[string]any)
		if !ok {
			params = append(params, llm.ToolParam{
				Name:     name,
				Type:     "string",
				Required: requiredSet[name],
			})
			continue
		}

		paramType := "string"
		if t, ok := propMap["type"].(string); ok {
			paramType = t
		}

		desc := ""
		if d, ok := propMap["description"].(string); ok {
			desc = d
		}

		// 如果有 enum，附加到描述
		if enumVals, ok := propMap["enum"].([]any); ok && len(enumVals) > 0 {
			enumStrs := make([]string, len(enumVals))
			for i, v := range enumVals {
				enumStrs[i] = fmt.Sprintf("%v", v)
			}
			desc += fmt.Sprintf(" (options: %s)", strings.Join(enumStrs, ", "))
		}

		params = append(params, llm.ToolParam{
			Name:        name,
			Type:        paramType,
			Description: desc,
			Required:    requiredSet[name],
		})
	}
	return params
}

// formatMCPResult 将 MCP CallToolResult 的 Content 转为文本
func formatMCPResult(result *mcp.CallToolResult) string {
	if result == nil || len(result.Content) == 0 {
		return "(no output)"
	}

	var parts []string
	for _, c := range result.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image: %s]", v.MIMEType))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio: %s]", v.MIMEType))
		case *mcp.EmbeddedResource:
			data, _ := json.Marshal(v)
			parts = append(parts, string(data))
		default:
			data, _ := json.Marshal(c)
			parts = append(parts, string(data))
		}
	}
	return strings.Join(parts, "\n")
}

// LoadMCPConfig 从文件加载 MCP 配置
func LoadMCPConfig(configPath string) (*MCPConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config MCPConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse mcp.json: %w", err)
	}
	return &config, nil
}

// headerInjectorTransport wraps http.RoundTripper to inject custom headers.
type headerInjectorTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerInjectorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}

// newHeaderInjectorClient creates an http.Client that injects custom headers into every request.
func newHeaderInjectorClient(headers map[string]string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &headerInjectorTransport{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}
