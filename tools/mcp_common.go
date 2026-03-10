package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	log "xbot/logger"

	"xbot/llm"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPServerConfig 单个 MCP Server 的配置
type MCPServerConfig struct {
	Command string            `json:"command,omitempty"` // 可执行文件路径（stdio 模式）
	Args    []string          `json:"args,omitempty"`    // 命令行参数（stdio 模式）
	Env     map[string]string `json:"env,omitempty"`     // 环境变量
	URL     string            `json:"url,omitempty"`     // HTTP MCP URL（http 模式）
	Headers map[string]string `json:"headers,omitempty"` // HTTP 请求头
	Enabled *bool             `json:"enabled,omitempty"` // 是否启用（默认 true）
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

// BuildStdioEnv 构建 stdio 模式的环境变量列表，将 .xbot/bin 加入 PATH
func BuildStdioEnv(cfg MCPServerConfig, configPath string) []string {
	var envList []string
	for k, v := range cfg.Env {
		envList = append(envList, fmt.Sprintf("%s=%s", k, v))
	}

	// 将 .xbot/bin 加入 PATH（方便 MCP 命令找到本地安装的工具）
	if binDir := resolveXbotBinDir(configPath); binDir != "" {
		currentPath := os.Getenv("PATH")
		if currentPath != "" {
			envList = append(envList, fmt.Sprintf("PATH=%s:%s", binDir, currentPath))
		} else {
			envList = append(envList, fmt.Sprintf("PATH=%s", binDir))
		}
		log.WithField("bin_dir", binDir).Debug("Added .xbot/bin to MCP server PATH")
	}

	return envList
}

// resolveXbotBinDir 从 configPath 推断 .xbot/bin 目录（存在才返回）
func resolveXbotBinDir(configPath string) string {
	if configPath == "" {
		return ""
	}

	dir := filepath.Dir(configPath) // e.g. /workdir/.xbot 或 /workdir

	// 如果 configPath 在 .xbot/ 目录下，bin 目录就在同一级
	var binDir string
	if strings.HasSuffix(dir, string(filepath.Separator)+".xbot") || filepath.Base(dir) == ".xbot" {
		binDir = filepath.Join(dir, "bin")
	} else {
		// configPath 在 workDir 根目录，如 /workdir/mcp.json
		binDir = filepath.Join(dir, ".xbot", "bin")
	}

	// 仅在目录存在时返回
	if info, err := os.Stat(binDir); err == nil && info.IsDir() {
		return binDir
	}
	return ""
}

// resolveWorkspaceRoot 推断工作区根路径：
// - 如果 configPath 位于 .xbot/ 下，返回 .xbot 的父目录（项目根）
// - 否则返回 configPath 所在目录
func resolveWorkspaceRoot(configPath string) string {
	if configPath == "" {
		return ""
	}

	dir := filepath.Dir(configPath)
	if strings.HasSuffix(dir, string(filepath.Separator)+".xbot") || filepath.Base(dir) == ".xbot" {
		return filepath.Dir(dir)
	}
	return dir
}

// ConnectStdioServer 连接 stdio 模式的 MCP Server（公共函数）
// MCP servers run outside the sandbox — they are infrastructure processes that may
// need full system access (e.g., Chromium for Playwright, Docker for container tools).
// Returns a ClientSession (auto-initialized) and the session itself for closing.
func ConnectStdioServer(ctx context.Context, cfg MCPServerConfig, configPath, workspaceRoot string) (*mcp.ClientSession, error) {
	envList := BuildStdioEnv(cfg, configPath)

	// Build exec.Cmd directly (no sandbox wrapping)
	execCmd := exec.Command(cfg.Command, cfg.Args...)
	if workspaceRoot != "" {
		execCmd.Dir = workspaceRoot
	}
	if len(envList) > 0 {
		// Inherit current env and append MCP-specific env
		execCmd.Env = append(os.Environ(), envList...)
	}

	transport := &mcp.CommandTransport{
		Command:           execCmd,
		TerminateDuration: 5 * time.Second,
	}

	// Connect auto-initializes the MCP session (initialize + initialized handshake)
	connectCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	session, err := sharedMCPClient.Connect(connectCtx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect stdio: %w", err)
	}

	return session, nil
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
	connectCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
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
		Transport: &headerInjectorTransport{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}
