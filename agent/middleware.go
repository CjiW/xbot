package agent

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"xbot/llm"
	log "xbot/logger"
)

// MessageContext 中间件处理的上下文，携带消息构建所需的全部信息。
// 中间件通过读取/修改此结构来参与消息构建流程。
type MessageContext struct {
	// Ctx 标准 context，用于超时控制和取消
	Ctx context.Context

	// SystemParts 系统提示词的各个部分，按 key 存储。
	// 最终会按 key 排序后拼接为完整的 system message。
	// 使用 map 而非 string 拼接，方便中间件独立修改自己负责的部分。
	SystemParts map[string]string

	// UserContent 原始用户消息内容
	UserContent string

	// UserMessage 最终发送给 LLM 的用户消息（中间件可修改）
	UserMessage string

	// History 对话历史
	History []llm.ChatMessage

	// Messages 最终构建的消息列表。
	// 通常由 pipeline 最后组装，中间件一般不直接操作。
	Messages []llm.ChatMessage

	// --- 以下为中间件可能需要的元数据 ---

	// Channel 消息渠道（如 feishu）
	Channel string

	// WorkDir 工作目录（展示给 LLM 的路径）
	WorkDir string

	// SenderName 发送者名称
	SenderName string

	// SenderID 发送者 ID
	SenderID string

	// ChatID 会话 ID
	ChatID string

	// Extra 扩展字段，中间件可以通过此 map 传递自定义数据
	Extra map[string]any
}

// Well-known Extra keys used by built-in middlewares.
// Use these constants instead of raw strings to avoid typos.
const (
	ExtraKeySkillsCatalog  = "skills_catalog"
	ExtraKeyAgentsCatalog  = "agents_catalog"
	ExtraKeyMemoryProvider = "memory_provider"
)

// GetExtra 从 Extra 中获取指定类型的值
func (mc *MessageContext) GetExtra(key string) (any, bool) {
	if mc.Extra == nil {
		return nil, false
	}
	v, ok := mc.Extra[key]
	return v, ok
}

// SetExtra 设置 Extra 中的值
func (mc *MessageContext) SetExtra(key string, value any) {
	if mc.Extra == nil {
		mc.Extra = make(map[string]any)
	}
	mc.Extra[key] = value
}

// GetExtraString 从 Extra 中获取 string 类型的值
func (mc *MessageContext) GetExtraString(key string) (string, bool) {
	v, ok := mc.GetExtra(key)
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// GetExtraTyped 从 Extra 中获取指定类型 T 的值（泛型 helper）。
// 避免手动 GetExtra + 类型断言的样板代码。
//
//	mem, ok := GetExtraTyped[memory.MemoryProvider](mc, ExtraKeyMemoryProvider)
func GetExtraTyped[T any](mc *MessageContext, key string) (T, bool) {
	v, ok := mc.GetExtra(key)
	if !ok {
		var zero T
		return zero, false
	}
	typed, ok := v.(T)
	return typed, ok
}

// BuildSystemPrompt 将 SystemParts 按 key 排序后拼接为完整的系统提示词。
// key 的命名约定决定了拼接顺序（字典序），建议使用数字前缀：
//
//	"00_base"    - 基础提示词模板
//	"10_skills"  - Skills 目录
//	"15_agents"  - Agents 目录
//	"20_memory"  - 记忆内容
//	"30_sender"  - 发送者信息
//	"90_time"    - 时间戳（变化最频繁，放最后以优化 KV-cache）
func (mc *MessageContext) BuildSystemPrompt() string {
	if len(mc.SystemParts) == 0 {
		return ""
	}

	keys := make([]string, 0, len(mc.SystemParts))
	for k := range mc.SystemParts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var total int
	for _, k := range keys {
		total += len(mc.SystemParts[k])
	}
	// pre-allocate: total content + newlines between parts
	buf := make([]byte, 0, total+len(keys))
	for i, k := range keys {
		if mc.SystemParts[k] == "" {
			continue
		}
		if i > 0 && len(buf) > 0 {
			buf = append(buf, '\n')
		}
		buf = append(buf, mc.SystemParts[k]...)
	}
	return string(buf)
}

// Assemble 组装最终的消息列表。
// 将 system prompt + history + user message 组装为 []ChatMessage。
func (mc *MessageContext) Assemble() []llm.ChatMessage {
	systemPrompt := mc.BuildSystemPrompt()

	messages := make([]llm.ChatMessage, 0, len(mc.History)+2)
	messages = append(messages, llm.NewSystemMessage(systemPrompt))
	messages = append(messages, mc.History...)
	messages = append(messages, llm.NewUserMessage(mc.UserMessage))

	// assert: 最终只能有一条 system（本 pipeline 生成），history 不得含 system（session 不持久化 system）
	var systemCount int
	for _, m := range messages {
		if m.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 1 {
		panic("assert: Assemble must produce exactly one system message; got " + fmt.Sprint(systemCount) + " (history may contain system)")
	}
	mc.Messages = messages
	return messages
}

// MessageMiddleware 消息构建中间件接口。
// 每个中间件负责消息构建流程中的一个独立步骤。
type MessageMiddleware interface {
	// Name 返回中间件名称，用于日志和调试
	Name() string

	// Priority 返回中间件优先级。数值越小越先执行。
	// 建议范围：
	//   0-99:   基础设施（提示词模板、环境信息）
	//   100-199: 上下文注入（skills、agents、memory）
	//   200-299: 用户消息处理（时间戳、发送者标识）
	//   300-399: 后处理（token 裁剪、格式化）
	Priority() int

	// Process 处理消息上下文。
	// 中间件应该只修改自己负责的部分，不要覆盖其他中间件的输出。
	// 返回 error 时，pipeline 会记录日志但继续执行（不中断流程）。
	Process(mc *MessageContext) error
}

// MessagePipeline 消息构建管道，按优先级执行中间件链。
// 并发安全：Agent 持有单个 pipeline 实例，多个 goroutine 可同时调用 Run()。
// Use/Remove 是写操作（通常在初始化阶段调用），Run/Middlewares 是读操作。
type MessagePipeline struct {
	mu          sync.RWMutex
	middlewares []MessageMiddleware
	sorted      bool
}

// NewMessagePipeline 创建消息构建管道
func NewMessagePipeline(middlewares ...MessageMiddleware) *MessagePipeline {
	p := &MessagePipeline{
		middlewares: middlewares,
	}
	p.sortLocked()
	return p
}

// Use 添加中间件到管道
func (p *MessagePipeline) Use(mw ...MessageMiddleware) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.middlewares = append(p.middlewares, mw...)
	p.sortLocked()
}

// Remove 按名称移除所有同名中间件。返回实际移除的数量。
func (p *MessagePipeline) Remove(name string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	filtered := p.middlewares[:0]
	for _, mw := range p.middlewares {
		if mw.Name() == name {
			n++
		} else {
			filtered = append(filtered, mw)
		}
	}
	p.middlewares = filtered
	return n
}

// sortLocked 按优先级排序中间件（稳定排序，相同优先级保持添加顺序）。
// 调用方必须持有锁（mu.Lock）。
func (p *MessagePipeline) sortLocked() {
	sort.SliceStable(p.middlewares, func(i, j int) bool {
		return p.middlewares[i].Priority() < p.middlewares[j].Priority()
	})
	p.sorted = true
}

// snapshot 返回当前中间件列表的快照（浅拷贝）。
// 调用方持有读锁即可。
func (p *MessagePipeline) snapshot() []MessageMiddleware {
	result := make([]MessageMiddleware, len(p.middlewares))
	copy(result, p.middlewares)
	return result
}

// Run 执行管道，返回构建好的消息列表。
// 并发安全：先获取中间件快照，再在快照上执行，不持有锁。
func (p *MessagePipeline) Run(mc *MessageContext) []llm.ChatMessage {
	p.mu.RLock()
	mws := p.snapshot()
	p.mu.RUnlock()

	for _, mw := range mws {
		if err := mw.Process(mc); err != nil {
			log.WithError(err).WithField("middleware", mw.Name()).Warn("Message middleware failed, skipping")
		}
	}

	return mc.Assemble()
}

// Middlewares 返回当前管道中的中间件列表（按优先级排序的快照）
func (p *MessagePipeline) Middlewares() []MessageMiddleware {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.snapshot()
}
