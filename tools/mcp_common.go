package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"xbot/llm"

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
	name      string
	client    *mcpclient.Client
	transport any
	tools     []mcp.Tool
}

// ConnectStdioServer 连接 stdio 模式的 MCP Server（公共函数）
func ConnectStdioServer(ctx context.Context, cfg MCPServerConfig) (*mcpclient.Client, any, error) {
	// 构建环境变量列表
	var envList []string
	for k, v := range cfg.Env {
		envList = append(envList, fmt.Sprintf("%s=%s", k, v))
	}

	// 创建 stdio transport
	stdioTransport := transport.NewStdio(cfg.Command, envList, cfg.Args...)

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
				if err != nil && !IsProcessExitError(err) {
					// 忽略进程退出错误，只记录其他错误
				}
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

// InitializeMCPClient 初始化 MCP 客户端并获取工具列表（公共函数）
func InitializeMCPClient(ctx context.Context, client *mcpclient.Client) ([]mcp.Tool, error) {
	// 初始化 MCP 协议
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "xbot",
		Version: "1.0.0",
	}

	connectCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	_, err := client.Initialize(connectCtx, initReq)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}

	// 获取可用工具列表
	toolsResult, err := client.ListTools(connectCtx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	return toolsResult.Tools, nil
}

// ConvertMCPParams 将 MCP 参数转换为 LLM ToolParam 格式
func ConvertMCPParams(tool mcp.Tool) []llm.ToolParam {
	schema := tool.InputSchema
	props := schema.Properties
	if props == nil {
		return nil
	}

	// 构建 required 集合
	requiredSet := make(map[string]bool)
	for _, r := range schema.Required {
		requiredSet[r] = true
	}

	var params []llm.ToolParam
	for name, propRaw := range props {
		// propRaw 是 interface{}，通常是 map[string]interface{}
		propMap, ok := propRaw.(map[string]interface{})
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

		params = append(params, llm.ToolParam{
			Name:        name,
			Type:        paramType,
			Description: desc,
			Required:    requiredSet[name],
		})
	}
	return params
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
