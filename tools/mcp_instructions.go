package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	log "xbot/logger"

	"xbot/llm"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPInstructionsManager 管理 MCP 服务器的 instructions 检查和生成
type MCPInstructionsManager struct {
	globalConfigPath string
	llmClient        llm.LLM
	model            string
}

// NewMCPInstructionsManager 创建 MCP instructions 管理器
func NewMCPInstructionsManager(globalConfigPath string, llmClient llm.LLM, model string) *MCPInstructionsManager {
	return &MCPInstructionsManager{
		globalConfigPath: globalConfigPath,
		llmClient:        llmClient,
		model:            model,
	}
}

// CheckAndGenerateInstructionsAsync 异步检查并生成所有 MCP 服务器的 instructions
func (m *MCPInstructionsManager) CheckAndGenerateInstructionsAsync(ctx context.Context) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("recover", r).Error("Panic in MCP instructions check")
			}
		}()

		// 稍微延迟启动，避免影响主启动流程
		time.Sleep(2 * time.Second)

		if err := m.checkAndGenerateAll(ctx); err != nil {
			log.WithError(err).Warn("MCP instructions check completed with errors")
		}
	}()
}

// checkAndGenerateAll 检查所有 MCP 服务器并生成缺失的 instructions
func (m *MCPInstructionsManager) checkAndGenerateAll(ctx context.Context) error {
	if m.globalConfigPath == "" {
		return nil
	}

	config, err := LoadMCPConfig(m.globalConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 没有 mcp.json 不是错误
		}
		return fmt.Errorf("load mcp config: %w", err)
	}

	if config == nil || len(config.MCPServers) == 0 {
		return nil
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		updated  bool
		hasError bool
	)

	for name, serverCfg := range config.MCPServers {
		// 跳过已禁用的服务器
		if serverCfg.Enabled != nil && !*serverCfg.Enabled {
			continue
		}

		wg.Add(1)
		go func(serverName string, cfg MCPServerConfig) {
			defer wg.Done()

			needGenerate, serverInstructions, err := m.checkServer(ctx, serverName, cfg)
			if err != nil {
				log.WithError(err).WithField("server", serverName).Warn("Failed to check MCP server")
				mu.Lock()
				hasError = true
				mu.Unlock()
				return
			}

			if needGenerate {
				generatedInstructions, err := m.generateInstructions(ctx, serverName, cfg, serverInstructions)
				if err != nil {
					log.WithError(err).WithField("server", serverName).Warn("Failed to generate instructions")
					mu.Lock()
					hasError = true
					mu.Unlock()
					return
				}

				mu.Lock()
				cfg.Instructions = generatedInstructions
				config.MCPServers[serverName] = cfg
				updated = true
				mu.Unlock()

				log.WithFields(log.Fields{
					"server": serverName,
				}).Info("Generated and saved MCP instructions")
			}
		}(name, serverCfg)
	}

	wg.Wait()

	// 如果有更新，保存配置
	if updated {
		if err := m.saveConfig(config); err != nil {
			return fmt.Errorf("save mcp config: %w", err)
		}
		log.WithField("path", m.globalConfigPath).Info("MCP config updated with generated instructions")
	}

	if hasError {
		return fmt.Errorf("some servers failed")
	}
	return nil
}

// checkServer 检查单个服务器，返回是否需要生成 instructions 以及服务器返回的工具信息
func (m *MCPInstructionsManager) checkServer(ctx context.Context, name string, cfg MCPServerConfig) (needGenerate bool, serverInfo *serverInfoResult, err error) {
	// 如果 config 中已经有 instructions，不需要生成（优先使用 config 的 instructions）
	if cfg.Instructions != "" {
		return false, nil, nil
	}

	// config 中没有 instructions，需要连接服务器并生成
	serverInfo, err = m.connectAndGetInfo(ctx, name, cfg)
	if err != nil {
		return false, nil, err
	}

	// 需要生成 instructions
	return true, serverInfo, nil
}

type serverInfoResult struct {
	instructions string
	tools        []*mcp.Tool
}

// connectAndGetInfo 连接服务器并获取 instructions 和工具列表
func (m *MCPInstructionsManager) connectAndGetInfo(ctx context.Context, name string, cfg MCPServerConfig) (*serverInfoResult, error) {
	var (
		session *mcp.ClientSession
		err     error
	)

	// 优先使用 HTTP transport
	if cfg.URL != "" {
		session, err = ConnectHTTPServer(ctx, cfg)
	} else if cfg.Command != "" {
		session, err = ConnectStdioServer(ctx, cfg, m.globalConfigPath, resolveWorkspaceRoot(m.globalConfigPath), name)
	} else {
		return nil, fmt.Errorf("mcp server config must have either 'url' or 'command'")
	}

	if err != nil {
		return nil, err
	}
	defer session.Close()

	// 获取工具列表和 instructions
	initResult, err := InitializeMCPClient(ctx, session)
	if err != nil {
		return nil, err
	}

	return &serverInfoResult{
		instructions: initResult.Instructions,
		tools:        initResult.Tools,
	}, nil
}

// generateInstructions 使用 LLM 生成 instructions
func (m *MCPInstructionsManager) generateInstructions(ctx context.Context, serverName string, cfg MCPServerConfig, serverInfo *serverInfoResult) (string, error) {
	if m.llmClient == nil {
		return "", fmt.Errorf("LLM client not available")
	}

	// 构建工具信息描述
	var toolsDesc strings.Builder
	fmt.Fprintf(&toolsDesc, "MCP Server: %s\n\n", serverName)
	toolsDesc.WriteString("Available Tools:\n")

	for _, tool := range serverInfo.tools {
		fmt.Fprintf(&toolsDesc, "\n## %s\n", tool.Name)
		if tool.Description != "" {
			fmt.Fprintf(&toolsDesc, "Description: %s\n", tool.Description)
		}

		// 添加参数信息
		params := ConvertMCPParams(tool)
		if len(params) > 0 {
			toolsDesc.WriteString("Parameters:\n")
			for _, p := range params {
				req := ""
				if p.Required {
					req = " (required)"
				}
				fmt.Fprintf(&toolsDesc, "  - %s (%s)%s: %s\n", p.Name, p.Type, req, p.Description)
			}
		}
	}

	// 构建 prompt
	prompt := fmt.Sprintf(`You are analyzing an MCP (Model Context Protocol) server to generate a concise usage instruction.

Server Information:
%s

Please generate a brief instruction (2-4 sentences) that describes:
1. What this MCP server does
2. When to use its tools
3. Any important usage notes

Write the instruction in a clear, concise manner that helps an AI assistant understand when and how to use this server's tools.
Do not include the server name or tool names in the instruction - just describe the functionality and use cases. Do not generate longer instructions than necessary.
Respond with only the instruction text in one sentence, no additional formatting or explanation.`, toolsDesc.String())

	// 调用 LLM
	messages := []llm.ChatMessage{
		llm.NewUserMessage(prompt),
	}

	response, err := m.llmClient.Generate(ctx, m.model, messages, nil)
	if err != nil {
		return "", fmt.Errorf("LLM generate: %w", err)
	}

	instruction := strings.TrimSpace(response.Content)
	if instruction == "" {
		return "", fmt.Errorf("LLM returned empty instruction")
	}

	return instruction, nil
}

// saveConfig 保存配置到文件
func (m *MCPInstructionsManager) saveConfig(config *MCPConfig) error {
	data, err := json.MarshalIndent(config, "", "    ")
	if err != nil {
		return err
	}

	return os.WriteFile(m.globalConfigPath, data, 0o644)
}
