package memory

import (
	"context"

	"xbot/llm"
)

// MemoryProvider 可插拔记忆系统的核心接口。
// 所有记忆实现（flat/tiered/agentic）必须满足此接口。
type MemoryProvider interface {
	// Recall 为当前对话检索相关记忆，返回注入 system prompt 的文本。
	// query 为用户当前消息，用于按需检索（flat 实现忽略此参数）。
	Recall(ctx context.Context, query string) (string, error)

	// Memorize 对话结束后处理记忆（压缩、存储、进化等）。
	Memorize(ctx context.Context, input MemorizeInput) (MemorizeResult, error)

	// Close 释放资源。
	Close() error
}

// ToolIndexer 提供工具语义搜索能力
type ToolIndexer interface {
	// IndexTools 将工具索引到向量存储（启动时调用）
	IndexTools(ctx context.Context, tools []ToolIndexEntry) error

	// SearchTools 语义搜索工具
	SearchTools(ctx context.Context, query string, topK int) ([]ToolIndexEntry, error)
}

// ToolIndexEntry 工具索引条目
type ToolIndexEntry struct {
	Name        string   // 工具名称 (如 mcp_server_tool)
	ServerName  string   // MCP服务器名 (如 feishu, global)
	Source      string   // 来源: "global" 或 "personal"
	Description string   // 工具描述
	Channels    []string // 支持的渠道列表（空=所有渠道）
}

// MemorizeInput 记忆写入的输入参数。
type MemorizeInput struct {
	Messages         []llm.ChatMessage // 需要处理的对话消息
	LastConsolidated int               // 上次合并的偏移量
	LLMClient        llm.LLM           // 用于压缩/分析的 LLM
	Model            string            // 模型名称
	ArchiveAll       bool              // true=归档所有消息（/new 命令）
	MemoryWindow     int               // 上下文窗口大小
}

// MemorizeResult 记忆写入的结果。
type MemorizeResult struct {
	NewLastConsolidated int  // 新的合并偏移量
	OK                  bool // 是否成功
}

// --- 可选能力接口（Phase 2+ 使用，此处预定义） ---

// Manageable 支持手动记忆管理（pin/unpin/delete）。
type Manageable interface {
	Pin(ctx context.Context, noteID string) error
	Unpin(ctx context.Context, noteID string) error
	Delete(ctx context.Context, noteID string) error
}

// Evolvable 支持记忆进化（A-Mem 风格）。
type Evolvable interface {
	Evolve(ctx context.Context, content string) ([]Evolution, error)
}

// Evolution 记忆进化操作记录。
type Evolution struct {
	Action string // "created" | "merged" | "updated" | "strengthened" | "discarded"
	NoteID string
	Detail string
}
