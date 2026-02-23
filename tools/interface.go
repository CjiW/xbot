package tools

import (
	"context"
	"xbot/llm"
)

// ToolContext 工具执行上下文
type ToolContext struct {
	Ctx        context.Context // 可取消的上下文，用于响应 stop 信号
	WorkingDir string          // Agent 的工作目录
	AgentID    string          // 当前 Agent 的 ID
	Manager    SubAgentManager // Agent 管理器引用（用于创建 SubAgent）
}

// SubAgentManager SubAgent 管理接口，避免循环依赖
type SubAgentManager interface {
	// RunSubAgent 创建并运行一个 SubAgent，返回最终响应文本
	RunSubAgent(ctx context.Context, parentAgentID string, task string, systemPrompt string) (string, error)
}

// ToolResult 工具执行结果
type ToolResult struct {
	Summary string // 精简结果，进入 LLM 上下文
	Detail  string // 详细内容（如 diff），仅推前端展示，不进入上下文
}

// NewResult 创建 Summary == Detail 的简单结果
func NewResult(content string) *ToolResult {
	return &ToolResult{Summary: content}
}

// Tool 工具接口
type Tool interface {
	llm.ToolDefinition
	Execute(ctx *ToolContext, input string) (*ToolResult, error)
}

// Registry 工具注册表
type Registry struct {
	tools map[string]Tool
}

// NewRegistry 创建工具注册表
func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Tool),
	}
}

// Register 注册工具
func (r *Registry) Register(tool Tool) {
	r.tools[tool.Name()] = tool
}

// Unregister 注销工具
func (r *Registry) Unregister(name string) {
	delete(r.tools, name)
}

// Get 获取工具
func (r *Registry) Get(name string) (Tool, bool) {
	tool, ok := r.tools[name]
	return tool, ok
}

// List 列出所有工具
func (r *Registry) List() []Tool {
	tools := make([]Tool, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	return tools
}

// AsDefinitions 转换为 LLM 工具定义列表
func (r *Registry) AsDefinitions() []llm.ToolDefinition {
	defs := make([]llm.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool)
	}
	return defs
}

// Clone 复制工具注册表
func (r *Registry) Clone() *Registry {
	clone := NewRegistry()
	for name, tool := range r.tools {
		clone.tools[name] = tool
	}
	return clone
}

// DefaultRegistry 创建包含默认工具的注册表
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(&BashTool{})
	r.Register(&GlobTool{})
	r.Register(&GrepTool{})
	r.Register(&ReadTool{})
	r.Register(&EditTool{})
	r.Register(NewWebSearchTool())
	r.Register(&SubAgentTool{})
	return r
}
