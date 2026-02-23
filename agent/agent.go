package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	"xbot/llm"
	"xbot/session"
	"xbot/tools"

	log "github.com/sirupsen/logrus"
)

// Agent 核心 Agent 引擎
type Agent struct {
	bus           *bus.MessageBus
	llmClient     llm.LLM
	model         string
	session       *session.Session
	tools         *tools.Registry
	maxIterations int
	memoryWindow  int
	memory        *MemoryStore

	consolidatingMu sync.Mutex
	consolidating   bool // 是否正在进行记忆合并
}

// Config Agent 配置
type Config struct {
	Bus           *bus.MessageBus
	LLM           llm.LLM
	Model         string
	MaxIterations int    // 单次对话最大工具调用迭代次数
	MemoryWindow  int    // 上下文窗口大小（保留的历史消息数）
	SessionPath   string // 会话持久化文件路径（空则不持久化）
	MemoryDir     string // 记忆文件目录（MEMORY.md / HISTORY.md）
}

// New 创建 Agent
func New(cfg Config) *Agent {
	if cfg.MaxIterations == 0 {
		cfg.MaxIterations = 20
	}
	if cfg.MemoryWindow == 0 {
		cfg.MemoryWindow = 50
	}
	if cfg.MemoryDir == "" {
		cfg.MemoryDir = "data/memory"
	}
	return &Agent{
		bus:           cfg.Bus,
		llmClient:     cfg.LLM,
		model:         cfg.Model,
		session:       session.New(cfg.SessionPath),
		tools:         tools.DefaultRegistry(),
		maxIterations: cfg.MaxIterations,
		memoryWindow:  cfg.MemoryWindow,
		memory:        NewMemoryStore(cfg.MemoryDir),
	}
}

// Run 启动 Agent 循环，持续消费入站消息
func (a *Agent) Run(ctx context.Context) error {
	log.Info("Agent loop started")
	for {
		select {
		case <-ctx.Done():
			log.Info("Agent loop stopped")
			return ctx.Err()
		case msg := <-a.bus.Inbound:
			response, err := a.processMessage(ctx, msg)
			if err != nil {
				log.WithError(err).Error("Error processing message")
				a.bus.Outbound <- bus.OutboundMessage{
					Channel: msg.Channel,
					ChatID:  msg.ChatID,
					Content: fmt.Sprintf("处理消息时发生错误: %v", err),
				}
				continue
			}
			if response != nil {
				a.bus.Outbound <- *response
			}
		}
	}
}

// processMessage 处理单条入站消息
func (a *Agent) processMessage(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	preview := msg.Content
	if len(preview) > 80 {
		preview = preview[:80] + "..."
	}
	log.WithFields(log.Fields{
		"channel": msg.Channel,
		"sender":  msg.SenderID,
	}).Infof("Processing: %s", preview)

	// 斜杠命令
	cmd := strings.TrimSpace(strings.ToLower(msg.Content))
	if cmd == "/new" {
		return a.handleNewSession(ctx, msg)
	}
	if cmd == "/help" {
		return &bus.OutboundMessage{
			Channel: msg.Channel,
			ChatID:  msg.ChatID,
			Content: "xbot 命令:\n/new — 开始新对话（归档记忆后重置）\n/help — 显示帮助",
		}, nil
	}

	// 检查是否需要触发自动记忆合并
	a.maybeConsolidate(ctx)

	// 构建 LLM 消息（注入长期记忆）
	history := a.session.GetHistory(a.memoryWindow)
	messages := BuildMessages(history, msg.Content, msg.Channel, a.memory)

	// 运行 Agent 循环
	finalContent, toolsUsed, err := a.runLoop(ctx, messages)
	if err != nil {
		return nil, err
	}

	if finalContent == "" {
		finalContent = "处理完成，但没有需要回复的内容。"
	}

	// 保存会话
	a.session.AddMessage(llm.NewUserMessage(msg.Content))
	assistantMsg := llm.NewAssistantMessage(finalContent)
	if len(toolsUsed) > 0 {
		_ = toolsUsed
	}
	a.session.AddMessage(assistantMsg)

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: finalContent,
	}, nil
}

// handleNewSession 处理 /new 命令：先归档记忆，再清空会话
func (a *Agent) handleNewSession(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error) {
	messages := a.session.GetMessages()
	lastConsolidated := a.session.LastConsolidated()

	// 取尚未合并的消息进行归档
	snapshot := messages
	if lastConsolidated < len(messages) {
		snapshot = messages[lastConsolidated:]
	}

	if len(snapshot) > 0 {
		log.Infof("/new: archiving %d unconsolidated messages", len(snapshot))
		_, ok := a.memory.Consolidate(ctx, snapshot, 0, a.llmClient, a.model, true, a.memoryWindow)
		if !ok {
			return &bus.OutboundMessage{
				Channel: msg.Channel,
				ChatID:  msg.ChatID,
				Content: "记忆归档失败，会话未重置，请重试。",
			}, nil
		}
	}

	a.session.Clear()
	a.session.SetLastConsolidated(0)

	return &bus.OutboundMessage{
		Channel: msg.Channel,
		ChatID:  msg.ChatID,
		Content: "会话已重置，记忆已归档。",
	}, nil
}

// maybeConsolidate 检查并异步触发记忆合并
func (a *Agent) maybeConsolidate(ctx context.Context) {
	if a.session.Len() <= a.memoryWindow {
		return
	}

	a.consolidatingMu.Lock()
	if a.consolidating {
		a.consolidatingMu.Unlock()
		return
	}
	a.consolidating = true
	a.consolidatingMu.Unlock()

	// 异步执行合并，不阻塞当前消息处理
	go func() {
		defer func() {
			a.consolidatingMu.Lock()
			a.consolidating = false
			a.consolidatingMu.Unlock()
		}()

		messages := a.session.GetMessages()
		lastConsolidated := a.session.LastConsolidated()

		newLC, ok := a.memory.Consolidate(ctx, messages, lastConsolidated, a.llmClient, a.model, false, a.memoryWindow)
		if ok {
			a.session.SetLastConsolidated(newLC)
			log.Infof("Auto memory consolidation completed, lastConsolidated=%d", newLC)
		}
	}()
}

// runLoop 执行 Agent 迭代循环（LLM -> 工具调用 -> LLM ...）
func (a *Agent) runLoop(ctx context.Context, messages []llm.ChatMessage) (string, []string, error) {
	var toolsUsed []string

	for i := 0; i < a.maxIterations; i++ {
		response, err := a.llmClient.Generate(ctx, a.model, messages, a.tools.AsDefinitions())
		if err != nil {
			return "", toolsUsed, fmt.Errorf("LLM generate failed: %w", err)
		}

		if !response.HasToolCalls() {
			content := strings.TrimSpace(response.Content)
			return content, toolsUsed, nil
		}

		// 记录 assistant 消息（含 tool_calls）
		assistantMsg := llm.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		// 执行每个工具调用
		for _, tc := range response.ToolCalls {
			toolsUsed = append(toolsUsed, tc.Name)

			argPreview := tc.Arguments
			if len(argPreview) > 200 {
				argPreview = argPreview[:200] + "..."
			}
			log.WithFields(log.Fields{
				"tool": tc.Name,
				"id":   tc.ID,
			}).Infof("Tool call: %s(%s)", tc.Name, argPreview)

			result, execErr := a.executeTool(ctx, tc)
			content := ""
			if execErr != nil {
				content = fmt.Sprintf("Error: %v", execErr)
				log.WithError(execErr).Warnf("Tool %s failed", tc.Name)
			} else {
				content = result.Summary
			}

			toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
			if result != nil && result.Detail != "" {
				toolMsg.Detail = result.Detail
			}
			messages = append(messages, toolMsg)
		}
	}

	return "已达到最大迭代次数，请重新描述你的需求。", toolsUsed, nil
}

// executeTool 执行单个工具调用
func (a *Agent) executeTool(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	tool, ok := a.tools.Get(tc.Name)
	if !ok {
		return nil, fmt.Errorf("unknown tool: %s", tc.Name)
	}

	timeout := 120 * time.Second
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	toolCtx := &tools.ToolContext{
		Ctx:        execCtx,
		WorkingDir: ".",
		AgentID:    "main",
		Manager:    a, // Agent 实现了 SubAgentManager 接口
	}

	return tool.Execute(toolCtx, tc.Arguments)
}

// RunSubAgent 实现 tools.SubAgentManager 接口
// 创建一个独立的子 Agent 循环来执行任务，子 Agent 拥有自己的工具集但不能再创建子 Agent
func (a *Agent) RunSubAgent(ctx context.Context, parentAgentID string, task string, systemPrompt string) (string, error) {
	if systemPrompt == "" {
		systemPrompt = "You are a helpful assistant. Complete the given task using the available tools."
	}

	// 子 Agent 工具集：除 SubAgent 外的所有标准工具（防止递归创建）
	subTools := a.tools.Clone()
	subTools.Unregister("SubAgent")

	// 构建子 Agent 的消息
	messages := []llm.ChatMessage{
		llm.NewSystemMessage(systemPrompt),
		llm.NewUserMessage(task),
	}

	log.WithFields(log.Fields{
		"parent": parentAgentID,
		"task":   truncate(task, 80),
	}).Info("SubAgent started")

	// 子 Agent 迭代循环（与主 Agent 的 runLoop 类似，但使用独立工具集）
	maxIter := a.maxIterations
	var toolsUsed []string

	for i := 0; i < maxIter; i++ {
		response, err := a.llmClient.Generate(ctx, a.model, messages, subTools.AsDefinitions())
		if err != nil {
			return "", fmt.Errorf("sub-agent LLM failed: %w", err)
		}

		if !response.HasToolCalls() {
			content := strings.TrimSpace(response.Content)
			log.WithFields(log.Fields{
				"parent":    parentAgentID,
				"tools":     toolsUsed,
				"iteration": i + 1,
			}).Info("SubAgent completed")
			return content, nil
		}

		assistantMsg := llm.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		for _, tc := range response.ToolCalls {
			toolsUsed = append(toolsUsed, tc.Name)

			tool, ok := subTools.Get(tc.Name)
			if !ok {
				toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, fmt.Sprintf("Error: unknown tool: %s", tc.Name))
				messages = append(messages, toolMsg)
				continue
			}

			timeout := 120 * time.Second
			execCtx, cancel := context.WithTimeout(ctx, timeout)

			toolCtx := &tools.ToolContext{
				Ctx:        execCtx,
				WorkingDir: ".",
				AgentID:    parentAgentID + "/sub",
			}

			result, execErr := tool.Execute(toolCtx, tc.Arguments)
			cancel()

			content := ""
			if execErr != nil {
				content = fmt.Sprintf("Error: %v", execErr)
			} else {
				content = result.Summary
			}

			toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
			messages = append(messages, toolMsg)
		}
	}

	return "Sub-agent reached maximum iterations.", nil
}

// truncate 截断字符串
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ProcessDirect 直接处理一条消息（用于 CLI 模式）
func (a *Agent) ProcessDirect(ctx context.Context, content string) (string, error) {
	msg := bus.InboundMessage{
		Channel:  "cli",
		SenderID: "user",
		ChatID:   "direct",
		Content:  content,
		Time:     time.Now(),
	}
	resp, err := a.processMessage(ctx, msg)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}
	return resp.Content, nil
}

// parseToolArgFirst 从 JSON 参数中取第一个值作为预览
func parseToolArgFirst(argsJSON string) string {
	var m map[string]any
	if json.Unmarshal([]byte(argsJSON), &m) != nil {
		return argsJSON
	}
	for _, v := range m {
		s, ok := v.(string)
		if ok {
			if len(s) > 40 {
				return s[:40] + "…"
			}
			return s
		}
	}
	return argsJSON
}
