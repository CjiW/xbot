package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "xbot/logger"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
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
	client       *mcpclient.Client
	transport    any
	tools        []mcp.Tool
	instructions string // from server's InitializeResult
}

// MCPServerCatalogEntry 单个 MCP Server 的目录条目（用于系统提示词中的轻量展示）
type MCPServerCatalogEntry struct {
	Name         string   // Server 名称
	Instructions string   // Server 初始化返回的使用说明
	ToolNames    []string // 工具名称列表（不含参数信息）
}

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
func ConnectStdioServer(ctx context.Context, cfg MCPServerConfig, configPath, workspaceRoot string) (*mcpclient.Client, any, error) {
	envList := BuildStdioEnv(cfg, configPath)
	cmd, args, err := WrapCommandForSandbox(cfg.Command, cfg.Args, workspaceRoot)
	if err != nil {
		return nil, nil, err
	}

	// 创建 stdio transport
	stdioTransport := transport.NewStdio(cmd, envList, args...)

	// 使用父级 context 启动 transport，因为子进程生命周期绑定到此 context
	if err := stdioTransport.Start(ctx); err != nil {
		return nil, nil, fmt.Errorf("start stdio transport: %w", err)
	}

	client := mcpclient.NewClient(stdioTransport)
	return client, stdioTransport, nil
}

// ConnectHTTPServer 连接 HTTP 模式的 MCP Server（公共函数）
func ConnectHTTPServer(ctx context.Context, cfg MCPServerConfig) (*mcpclient.Client, any, error) {
	opts := []transport.StreamableHTTPCOption{}

	// 添加 headers
	if len(cfg.Headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(cfg.Headers))
	}

	// 创建 HTTP transport
	httpTransport, err := transport.NewStreamableHTTP(cfg.URL, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("create HTTP transport: %w", err)
	}

	// 启动 transport
	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := httpTransport.Start(connectCtx); err != nil {
		return nil, nil, fmt.Errorf("start HTTP transport: %w", err)
	}

	client := mcpclient.NewClient(httpTransport)
	return client, httpTransport, nil
}

// CloseTransport 关闭指定类型的 transport（公共函数）
func CloseTransport(t any) {
	switch tr := t.(type) {
	case interface{ Close() error }:
		// 使用 goroutine 和超时避免卡死
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			resultCh := make(chan error, 1)
			go func() {
				resultCh <- tr.Close()
			}()

			select {
			case err := <-resultCh:
				// 忽略进程退出错误，只记录其他错误
				_ = err
			case <-ctx.Done():
				// 超时不等待
			}
		}()
	}
}

// IsProcessExitError 判断是否为子进程退出错误（如 "exit status 1"）
func IsProcessExitError(err error) bool {
	if err == nil {
		return false
	}
	// 检查错误字符串是否包含 "exit status" 或 "signal:"
	errStr := err.Error()
	return hasPrefixSuffix(errStr, "exit status") || hasPrefixSuffix(errStr, "signal:")
}

// hasPrefixSuffix 检查字符串是否以指定子串开头或结尾，或在中间包含
func hasPrefixSuffix(s, substr string) bool {
	if len(s) < len(substr) {
		return false
	}
	if s == substr {
		return true
	}
	if len(s) > len(substr) {
		if s[:len(substr)] == substr || s[len(s)-len(substr):] == substr {
			return true
		}
		// 检查中间
		for i := 0; i <= len(s)-len(substr); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
	}
	return false
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
