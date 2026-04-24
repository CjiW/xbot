package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xbot/bus"
	"xbot/llm"
	"xbot/memory"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
	"xbot/tools"
)

// SubAgentProgressCallback is the type for SubAgent progress callback.
// It carries depth information for recursive SubAgent progress penetration.
type SubAgentProgressCallback func(detail SubAgentProgressDetail)

type subAgentProgressKey struct{}

// SubAgentProgressFromContext extracts the SubAgent progress callback from context.
func SubAgentProgressFromContext(ctx context.Context) (SubAgentProgressCallback, bool) {
	cb, ok := ctx.Value(subAgentProgressKey{}).(SubAgentProgressCallback)
	return cb, ok
}

// WithSubAgentProgress returns a new context with the SubAgent progress callback.
func WithSubAgentProgress(ctx context.Context, cb SubAgentProgressCallback) context.Context {
	return context.WithValue(ctx, subAgentProgressKey{}, cb)
}

// RunConfig 统一的 Agent 运行配置。
// 主 Agent 和 SubAgent 使用同一个 Run() 方法，差异通过配置注入。
type RunConfig struct {
	// === 必需 ===
	LLMClient    llm.LLM
	Model        string
	ThinkingMode string // 思考模式（如 "enabled", "auto"）
	Stream       bool   // 使用流式 API 调用 LLM（兼容 Copilot 等代理）
	Tools        *tools.Registry
	Messages     []llm.ChatMessage

	// === 身份（从 InboundMessage 提取） ===
	AgentID      string // "main", "main/code-reviewer"
	Channel      string // 原始 IM 渠道（用于 ToolContext）
	ChatID       string // 原始 IM 会话
	SenderID     string // 直接调用者 ID（SubAgent 场景下为父 Agent ID）
	OriginUserID string // 原始用户 ID（始终为终端用户，用于 LLM 配置、工作区路径等）
	SenderName   string
	FeishuUserID string // 非空表示通过飞书身份登录 web（用于 runner 路由）

	// === 工作区 & 沙箱 ===
	WorkingDir       string   // Agent 工作目录（宿主机）
	WorkspaceRoot    string   // 用户可读写工作区根目录（宿主机路径）
	ReadOnlyRoots    []string // 额外只读目录
	SkillsDirs       []string // 全局 skill 目录列表
	AgentsDir        string
	MCPConfigPath    string        // 用户 MCP 配置路径
	GlobalMCPConfig  string        // 全局 MCP 配置路径（只读）
	DataDir          string        // 数据持久化目录
	SandboxEnabled   bool          // 是否启用命令沙箱
	PreferredSandbox string        // 沙箱类型（docker 优先）
	Sandbox          tools.Sandbox // Sandbox 实例引用（V4 新增）
	SandboxMode      string        // 实际沙箱模式："none", "docker", "remote"
	InitialCWD       string        // 初始当前工作目录（宿主机路径，用于 SubAgent 继承父 Agent 的 CWD）

	// === 循环控制 ===
	MaxIterations   int // 0 = 使用默认值 100
	MaxOutputTokens int // 0 = 使用 LLM client 默认值（8192）

	// === 可选能力（nil = 不启用） ===

	// Session 持久化（nil = 纯内存，不持久化）
	Session *session.TenantSession

	// SessionKey 工具激活的 session key（为空时从 Channel+ChatID 生成）
	SessionKey string

	// RootSessionKey 顶层 Agent 的 session key。
	// SubAgent 场景下指向主 Agent 的 session key，用于 offload_recall 等需要访问父 session 数据的场景。
	// 主 Agent 场景下为空（与 SessionKey 相同）。
	RootSessionKey string

	// ProgressNotifier 进度通知回调（nil = 不通知）
	ProgressNotifier func(lines []string)

	// ProgressEventHandler 结构化进度事件回调（nil = 不发送）
	ProgressEventHandler func(event *ProgressEvent)

	// ContextManager 上下文管理器（nil = 不压缩）
	ContextManager ContextManager

	// ContextManagerConfig 上下文管理器配置（Phase 2 智能触发需要访问 MaxContextTokens 等）
	ContextManagerConfig *ContextManagerConfig

	// SendFunc 向 IM 渠道发送消息（nil = 不能发消息）
	SendFunc func(channel, chatID, content string, metadata ...map[string]string) error

	// InjectInbound 注入入站消息，触发 Agent 完整处理循环（nil = 不支持）
	InjectInbound func(channel, chatID, senderID, content string)

	// Memory 记忆提供者（nil = 无记忆）
	Memory memory.MemoryProvider

	// ToolContextExtras Letta 记忆相关的 ToolContext 扩展字段
	ToolContextExtras *ToolContextExtras

	// SpawnAgent SubAgent 创建能力（nil = 不能创建子 Agent）
	// 输入输出都是统一消息：InboundMessage → OutboundMessage
	SpawnAgent func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)

	// OAuthHandler OAuth 自动触发处理器（nil = 不处理 OAuth）
	// 返回 (content, handled)：handled=true 时用 content 替换工具错误
	OAuthHandler func(ctx context.Context, tc llm.ToolCall, execErr error) (content string, handled bool)

	// ToolExecutor 工具执行函数。
	// 主 Agent 注入带 session MCP、激活检查、Letta memory 的完整版本；
	// SubAgent 使用 nil（defaultToolExecutor 从 cfg.Tools 查找并执行）。
	ToolExecutor func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error)

	// ToolTimeout is deprecated and no longer used for wrapping tool contexts.
	// Individual tools (e.g. Shell) manage their own timeouts.
	// Engine only passes through the parent context (user Ctrl+C cancels it).
	ToolTimeout time.Duration

	// EnableReadWriteSplit 启用读写分离并行执行（默认 false = 全部串行）
	EnableReadWriteSplit bool

	// SessionFinalSentCallback 工具发送最终回复时的回调（如飞书卡片）。
	// 返回 true 表示已发送最终回复，后续进度通知应停止。
	SessionFinalSentCallback func() bool

	// InteractiveCallbacks Interactive SubAgent 回调（nil = 不支持 interactive）。
	// 主 Agent 注入，SubAgent 不注入。
	InteractiveCallbacks *InteractiveCallbacks

	// HookChain tool execution hook chain (nil = no hooks).
	HookChain *tools.HookChain

	// SettingsSvc provides access to user settings (nil = settings not available).
	SettingsSvc *SettingsService

	// OffloadStore Layer 1 offload store（nil = 不启用）
	OffloadStore *OffloadStore

	// MaskStore Observation Masking 存储（nil = 不启用）
	MaskStore *ObservationMaskStore

	// ContextEditor Context Editing 编辑器（nil = 不启用）
	ContextEditor *ContextEditor

	// MemoryToolDefs 记忆工具定义列表（nil = 压缩时不使用记忆工具）
	MemoryToolDefs []llm.ToolDefinition

	// MemoryToolExec 记忆工具执行函数（nil = 压缩时不使用记忆工具）
	MemoryToolExec func(ctx context.Context, tc llm.ToolCall) (content string, err error)

	// TodoManager TODO 管理器（可选）
	TodoManager TodoManagerProvider

	// DrainBgNotifications is called between iterations to check for completed bg tasks
	// and bg subagent notifications. Returns notifications that should be injected
	// as tool results into the current Run loop.
	// Returns nil when no notifications are pending. Called on each iteration.
	DrainBgNotifications func() []tools.BgNotification

	// LLMSemAcquire is called before each LLM call to acquire a per-tenant
	// concurrency slot. Returns a release function that must be called after
	// the LLM call completes. If nil, no concurrency limiting is applied.
	LLMSemAcquire func(context.Context) func()

	// RecordUserTokenUsage is called at the end of Run() to persist per-user
	// token usage (inputTokens, outputTokens, conversationCount, llmCallCount).
	// If nil, per-user tracking is skipped.
	RecordUserTokenUsage func(senderID, model string, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount int)

	// EnableConcurrentSubAgents enables parallel execution of SubAgent tool calls.
	// When true, multiple SubAgent calls in the same iteration run concurrently,
	// bounded by SubAgentSem. Default false (backward compatible: sequential).
	EnableConcurrentSubAgents bool

	// SubAgentSem acquires a per-tenant semaphore slot for SubAgent execution.
	// It blocks until a slot is available and returns a release function.
	// If nil and EnableConcurrentSubAgents is true, no limit is applied.
	SubAgentSem func(context.Context) func()

	// LastPromptTokens is the prompt_tokens from the previous Run()'s last LLM call.
	// Restored from agent state or DB to avoid starting from 0 after restart.
	LastPromptTokens int64
	// LastCompletionTokens is the completion_tokens from the previous Run()'s last LLM call.
	LastCompletionTokens int64
	// SaveTokenState persists token counts after Run() completes.
	// Called with the final promptTokens and completionTokens values.
	// If nil, token counts are only kept in memory (lost on restart).
	SaveTokenState func(promptTokens, completionTokens int64)

	// BgTaskManager 后台任务管理器（nil = 不支持后台任务）
	BgTaskManager *tools.BackgroundTaskManager
	// MessageSender 允许 Agent 向任何 Channel 发消息（IM、Agent、Group）。
	// nil = 不启用（SubAgent 继承主 Agent 的 MessageSender）。
	MessageSender bus.MessageSender
	// RegisterAgentChannel registers an AgentChannel in the Dispatcher.
	RegisterAgentChannel func(name string, runFn bus.RunFn) error
	// UnregisterAgentChannel removes an AgentChannel from the Dispatcher.
	UnregisterAgentChannel func(name string)

	// OnIterationSnapshot is called after each iteration snapshot is created.
	// Used by background interactive sessions to incrementally expose iteration
	// history for real-time inspect, instead of waiting for Run() to finish.
	OnIterationSnapshot func(snap IterationSnapshot)

	// StreamContentFunc is called with accumulated text content on each content delta
	// during LLM streaming. When set (and Stream=true), generateResponse uses
	// CollectStreamWithCallback instead of CollectStream. Nil by default (no streaming).
	StreamContentFunc func(content string)

	// StreamReasoningFunc is called with accumulated reasoning content on each
	// reasoning delta during LLM streaming. Nil by default (no reasoning streaming).
	StreamReasoningFunc func(content string)
}

// TodoManagerProvider 提供 TODO 状态查询和清理
type TodoManagerProvider interface {
	GetTodoSummary(sessionKey string) string
	GetTodoItems(sessionKey string) []TodoProgressItem
	ClearTodos(sessionKey string)
}

// InteractiveCallbacks 主 Agent 提供给 buildToolContext 的 interactive 回调。
type InteractiveCallbacks struct {
	SpawnFn     func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	SendFn      func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	UnloadFn    func(ctx context.Context, roleName, instance string) error
	InterruptFn func(ctx context.Context, roleName, instance string) error
	InspectFn   func(ctx context.Context, roleName, instance string, tail int) (string, error)
}

// ToolContextExtras Letta 记忆相关的 ToolContext 扩展字段。
// 仅包含 Letta memory 特有的字段，通用字段（InjectInbound、Registry 等）
// 已迁移到 RunConfig 中。
type ToolContextExtras struct {
	TenantID                int64
	CoreMemory              *sqlite.CoreMemoryService
	ArchivalMemory          *vectordb.ArchivalService
	MemorySvc               *sqlite.MemoryService
	RecallTimeRange         vectordb.RecallTimeRangeFunc
	ToolIndexer             memory.ToolIndexer
	InvalidateAllSessionMCP func()
}

// DefaultMaxIterations 默认最大迭代次数。
const DefaultMaxIterations = 2000

// readOnlyTools 只读工具集合，用于读写分离并行执行。
var readOnlyTools = map[string]bool{
	"Read": true, "Grep": true, "Glob": true,
	"WebSearch": true, "ChatHistory": true,
}

// RunOutput is the result of a Run() call.
// It extends OutboundMessage with internal messages needed for post-run processing
// (e.g., SubAgent memory consolidation).
type RunOutput struct {
	*bus.OutboundMessage
	// Messages contains the full conversation messages from the Run loop.
	// Only populated when Memory is set in RunConfig (used for memorize after exit).
	Messages []llm.ChatMessage
	// EngineMessages contains assistant+tool messages produced during the Run loop.
	// These are the messages appended to the original cfg.Messages during execution.
	// Used by processMessage to persist context when WaitingUser is true.
	EngineMessages []llm.ChatMessage
	// IterationHistory contains snapshots of completed iterations for UI display.
	IterationHistory []IterationSnapshot
	// LastPromptTokens is the prompt_tokens from the last LLM API call.
	// This is the authoritative token count for the full input (messages + tool defs).
	LastPromptTokens int64
	// LastCompletionTokens is the completion_tokens from the last LLM API call.
	LastCompletionTokens int64
	// ReasoningContent is the final response's reasoning_content (thinking).
	// Required for DeepSeek thinking mode — must be persisted so it can be
	// passed back to the API in subsequent turns.
	ReasoningContent string
}

// IterationSnapshot captures the tool summary of a completed iteration.
type IterationSnapshot struct {
	Iteration int                     `json:"iteration"`
	Thinking  string                  `json:"thinking,omitempty"`
	Reasoning string                  `json:"reasoning,omitempty"`
	Tools     []IterationToolSnapshot `json:"tools"`
}

// IterationToolSnapshot captures a single tool's execution result within an iteration.
type IterationToolSnapshot struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"` // done | error
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
	Summary   string `json:"summary,omitempty"`
}

// readArgsHasOffsetOrLimit checks whether a Read tool call's JSON arguments contain
// offset > 0 or max_lines > 0. Used to skip offloading when the LLM intentionally
// narrowed the read range — offloading would replace actual content with a summary.
func readArgsHasOffsetOrLimit(argsJSON string) bool {
	var args struct {
		Offset   int `json:"offset"`
		MaxLines int `json:"max_lines"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return false
	}
	return args.Offset > 0 || args.MaxLines > 0
}

// Run 统一的 Agent 循环。
//
// 输入：RunConfig（从 InboundMessage 构建）
// 输出：*RunOutput（可直接发送到 IM 或返回给父 Agent）
//
// 主 Agent 和 SubAgent 使用同一个 Run()，差异通过 RunConfig 注入：
//   - 主 Agent: ToolExecutor=buildToolExecutor, ProgressNotifier=sendMessage, ContextManager=enabled, ...

// generateResponse calls the LLM using non-streaming mode.
func generateResponse(ctx context.Context, client llm.LLM, model string, messages []llm.ChatMessage, tools []llm.ToolDefinition, thinkingMode string, stream bool, streamContentFn func(string), streamReasoningFn func(string)) (*llm.LLMResponse, error) {
	if stream {
		if sc, ok := client.(llm.StreamingLLM); ok {
			eventCh, err := sc.GenerateStream(ctx, model, messages, tools, thinkingMode)
			if err != nil {
				return nil, err
			}
			if streamContentFn != nil || streamReasoningFn != nil {
				return llm.CollectStreamWithCallback(ctx, eventCh, streamContentFn, streamReasoningFn)
			}
			return llm.CollectStream(ctx, eventCh)
		}
		// Fallback: client doesn't support streaming, use non-stream
	}
	return client.Generate(ctx, model, messages, tools, thinkingMode)
}

// Run 统一的 Agent 循环。
//
// 输入：RunConfig（从 InboundMessage 构建）
// 输出：*RunOutput（可直接发送到 IM 或返回给父 Agent）
//
// 主 Agent 和 SubAgent 使用同一个 Run()，差异通过 RunConfig 注入：
//   - 主 Agent: ToolExecutor=buildToolExecutor, ProgressNotifier=sendMessage, ContextManager=enabled, ...
//   - SubAgent: ToolExecutor=simpleExecutor, ProgressNotifier=nil, ContextManager=independent_phase1, ...
func Run(ctx context.Context, cfg RunConfig) *RunOutput {
	s := newRunState(cfg)

	// Cleanup completed TODOs on exit
	defer s.cleanupTodos()

	// Sync ContextEditor reference
	s.messages = s.syncMessages(s.messages)

	// Record conversation metrics on exit
	defer s.recordMetrics()

	// Setup structured progress tracking
	s.initProgress()

	// Ensure PhaseDone event is sent on exit
	if s.progressFinalizer != nil {
		defer s.progressFinalizer()
	}

	// Setup dynamic context injector for CWD change detection
	s.initDynamicInjector()

	// Advance round counter for tool activation cleanup
	s.tickSession()

	// Wrap context with LLM retry notification
	retryNotifyCtx := s.setupRetryNotify(ctx)

	// --- Main loop ---
	for i := 0; i < s.maxIter; i++ {
		// Check for cancellation before starting each iteration
		select {
		case <-ctx.Done():
			out := s.buildOutput(&bus.OutboundMessage{
				Channel: s.cfg.Channel,
				ChatID:  s.cfg.ChatID,
				Content: "Agent was cancelled.",
			})
			out.Error = ctx.Err()
			return out
		default:
		}

		s.beginIteration(i)
		s.maybeCompress(ctx)
		s.notifyThinking(i)

		if out := s.assertSystemMessages(ctx); out != nil {
			return out
		}

		response, err := s.callLLM(ctx, retryNotifyCtx)

		// If ctx was cancelled during LLM call, exit immediately
		if ctx.Err() != nil {
			out := s.buildOutput(&bus.OutboundMessage{
				Channel: s.cfg.Channel,
				ChatID:  s.cfg.ChatID,
				Content: "Agent was cancelled.",
			})
			out.Error = ctx.Err()
			return out
		}

		if out := s.handleLLMError(ctx, err, response, i); out != nil {
			return out
		}

		out, retry := s.handleFinalResponse(ctx, response)
		if retry {
			continue
		}
		if out != nil {
			return out
		}

		s.recordAssistantMsg(ctx, response)

		results := s.executeToolCalls(ctx, response, i)

		// Always process tool results (preserves engine messages for session continuity)
		s.processToolResults(ctx, response, results)

		// If ctx was cancelled during tool execution, exit after preserving results
		if ctx.Err() != nil {
			// Strip trailing unpaired tool_calls so they don't get persisted
			// to DB and cause API errors on the next Run.
			s.messages = llm.FixupTrailingToolCalls(s.messages)
			out := s.buildOutput(&bus.OutboundMessage{
				Channel: s.cfg.Channel,
				ChatID:  s.cfg.ChatID,
				Content: "Agent was cancelled.",
			})
			out.Error = ctx.Err()
			return out
		}

		if out := s.postToolProcessing(ctx, response, i); out != nil {
			return out
		}
	}

	return s.buildMaxIterOutput()
}

// executeWithHooks wraps tool execution with pre/post hook calls.
// Both defaultToolExecutor (SubAgents) and buildToolExecutor (main Agent)
// MUST use this function to ensure hooks are called identically.
//
// The function:
//  1. Runs pre-tool hooks with WorkingDir injected into context
//  2. Executes the tool
//  3. Runs post-tool hooks (always, even on error)
//
// toolExecCtx is the base context (with perm users etc. injected).
// toolCtx is the ToolContext (with WorkingDir resolved).
// workingDir is toolCtx.WorkingDir, extracted for convenience.
func executeWithHooks(
	hookChain *tools.HookChain,
	toolExecCtx context.Context,
	toolCtx *tools.ToolContext,
	toolName, toolArgs string,
	tool tools.Tool,
) (*tools.ToolResult, error) {
	// Pre-tool hooks: inject WorkingDir so hooks can resolve paths
	if hookChain != nil {
		hookCtx := tools.WithWorkingDir(toolExecCtx, toolCtx.WorkingDir)
		if err := hookChain.RunPre(hookCtx, toolName, toolArgs); err != nil {
			return nil, fmt.Errorf("pre-tool hook blocked %q: %w", toolName, err)
		}
	}

	start := time.Now()
	result, err := tool.Execute(toolCtx, toolArgs)
	elapsed := time.Since(start)

	// Post-tool hooks (always, even on error)
	if hookChain != nil {
		hookChain.RunPost(toolExecCtx, toolName, toolArgs, result, err, elapsed)
	}

	return result, err
}

// defaultToolExecutor creates the default tool executor (looks up from Registry and executes).
// Used for SubAgent and other scenarios that don't need session MCP / activation checks.
func defaultToolExecutor(cfg *RunConfig) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		tool, ok := cfg.Tools.Get(tc.Name)
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tc.Name)
		}

		toolExecCtx := withApprovalTarget(ctx, cfg.ChatID, cfg.OriginUserID)
		if cfg.SettingsSvc != nil {
			permUsers := cfg.SettingsSvc.GetPermUsers(cfg.Channel, cfg.OriginUserID)
			if permUsers != nil {
				toolExecCtx = tools.WithPermUsers(toolExecCtx, permUsers.DefaultUser, permUsers.PrivilegedUser)
			}
		}
		toolCtx := buildToolContext(toolExecCtx, cfg)

		return executeWithHooks(cfg.HookChain, toolExecCtx, toolCtx, tc.Name, tc.Arguments, tool)
	}
}

// spawnAgentAdapter 将 SpawnAgent 函数适配为 SubAgentManager 接口。
// 核心职责：将 (task, prompt, tools) 函数签名转换为统一的 InboundMessage。
//
// 这使得 SubAgentTool 零改动：它仍然调用 SubAgentManager.RunSubAgent()，
// 而 adapter 内部完成 string ↔ InboundMessage/OutboundMessage 转换。
type spawnAgentAdapter struct {
	spawnFn  func(ctx context.Context, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	parentID string
	channel  string
	chatID   string
	senderID string

	// Interactive mode callbacks (nil = interactive not supported)
	interactiveSpawnFn     func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	interactiveSendFn      func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	interactiveUnloadFn    func(ctx context.Context, roleName, instance string) error
	interactiveInterruptFn func(ctx context.Context, roleName, instance string) error
	interactiveInspectFn   func(ctx context.Context, roleName, instance string, tail int) (string, error)
}

// RunSubAgent 实现 tools.SubAgentManager 接口。
func (a *spawnAgentAdapter) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, roleName, model string) (string, error) {
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, false, "", model)
	out, err := a.spawnFn(parentCtx.Ctx, msg)
	if err != nil {
		return "", err
	}
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// SpawnInteractive 实现 InteractiveSubAgentManager.SpawnInteractive。
func (a *spawnAgentAdapter) SpawnInteractive(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, instance, model string) (string, error) {
	if a.interactiveSpawnFn == nil {
		return "", fmt.Errorf("interactive mode not supported")
	}
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, true, instance, model)
	out, err := a.interactiveSpawnFn(parentCtx.Ctx, roleName, msg)
	if err != nil {
		return "", err
	}
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// SendInteractive 实现 InteractiveSubAgentManager.SendInteractive。
func (a *spawnAgentAdapter) SendInteractive(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, instance, model string) (string, error) {
	if a.interactiveSendFn == nil {
		return "", fmt.Errorf("interactive mode not supported")
	}
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, true, instance, model)
	out, err := a.interactiveSendFn(parentCtx.Ctx, roleName, msg)
	if err != nil {
		return "", err
	}
	if out.Error != nil {
		return out.Content, out.Error
	}
	return out.Content, nil
}

// UnloadInteractive 实现 InteractiveSubAgentManager.UnloadInteractive。
func (a *spawnAgentAdapter) UnloadInteractive(parentCtx *tools.ToolContext, roleName, instance string) error {
	if a.interactiveUnloadFn == nil {
		return fmt.Errorf("interactive mode not supported")
	}
	return a.interactiveUnloadFn(parentCtx.Ctx, roleName, instance)
}

// InspectInteractive 实现 InteractiveSubAgentManager.InspectInteractive。
func (a *spawnAgentAdapter) InspectInteractive(parentCtx *tools.ToolContext, roleName, instance string, tailCount int) (string, error) {
	if a.interactiveInspectFn == nil {
		return "", fmt.Errorf("interactive inspect not supported")
	}
	return a.interactiveInspectFn(parentCtx.Ctx, roleName, instance, tailCount)
}

// InterruptInteractive 实现 InteractiveSubAgentManager.InterruptInteractive。
func (a *spawnAgentAdapter) InterruptInteractive(parentCtx *tools.ToolContext, roleName, instance string) error {
	if a.interactiveInterruptFn == nil {
		return fmt.Errorf("interactive interrupt not supported")
	}
	return a.interactiveInterruptFn(parentCtx.Ctx, roleName, instance)
}

// buildMsg 构造 SubAgent InboundMessage。
func (a *spawnAgentAdapter) buildMsg(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, interactive bool, instance, model string) bus.InboundMessage {
	metadata := map[string]string{
		"origin_channel": a.channel,
		"origin_chat_id": a.chatID,
		"origin_sender":  a.senderID,
	}
	if interactive {
		metadata["interactive"] = "true"
	}
	if instance != "" {
		metadata["instance_id"] = instance
	}
	// Propagate background flag from ToolContext metadata
	if parentCtx.Metadata != nil {
		if bg, ok := parentCtx.Metadata["background"]; ok {
			metadata["background"] = bg
		}
	}
	// Propagate model override from SubAgent role definition
	if model != "" {
		metadata["model"] = model
	}
	// Propagate parent's CWD so SubAgent inherits working directory.
	// If CurrentDir is empty (parent never Cd'd), fall back to WorkingDir.
	parentCWD := parentCtx.CurrentDir
	if parentCWD == "" {
		parentCWD = parentCtx.WorkingDir
	}
	if parentCWD != "" {
		metadata["parent_cwd"] = parentCWD
	}

	return bus.InboundMessage{
		From: bus.NewIMAddress(a.channel, a.senderID),
		To:   bus.NewAgentAddress(a.parentID),

		Channel:    bus.SchemeAgent,
		Content:    task,
		SenderID:   parentCtx.SenderID,
		SenderName: parentCtx.SenderName,
		ChatID:     a.chatID,
		ChatType:   "agent",
		Time:       time.Now(),

		ParentAgentID: a.parentID,
		RoleName:      roleName,
		SystemPrompt:  systemPrompt,
		AllowedTools:  allowedTools,
		Capabilities:  caps.ToMap(),
		Metadata:      metadata,
	}
}

// sandboxReadOnlyRoots 将 host 路径的 ReadOnlyRoots 转换为 sandbox 路径。
// 仅在 sandboxWorkDir 非空且与 WorkspaceRoot 不同时进行转换。
func sandboxReadOnlyRoots(hostRoots []string, sandboxWorkDir, workspaceRoot string) []string {
	if sandboxWorkDir == "" || sandboxWorkDir == workspaceRoot {
		return hostRoots
	}
	result := make([]string, 0, len(hostRoots))
	for _, ro := range hostRoots {
		if strings.HasPrefix(ro, workspaceRoot) {
			result = append(result, sandboxWorkDir+strings.TrimPrefix(ro, workspaceRoot))
		} else {
			result = append(result, ro)
		}
	}
	return result
}

// buildToolContext 统一构建 ToolContext。
// 从 RunConfig 中提取所有字段，主 Agent 和 SubAgent 使用同一个构建路径。
// resolveSandbox resolves the per-user sandbox instance if the global sandbox
// implements SandboxResolver (e.g., SandboxRouter). Falls back to the global instance.
func resolveSandbox(sandbox tools.Sandbox, userID string) tools.Sandbox {
	if sandbox == nil {
		return nil
	}
	if resolver, ok := sandbox.(tools.SandboxResolver); ok {
		return resolver.SandboxForUser(userID)
	}
	return sandbox
}

func buildToolContext(ctx context.Context, cfg *RunConfig) *tools.ToolContext {
	// Resolve per-user sandbox BEFORE building ToolContext.
	// For remote users, the resolved sandbox is RemoteSandbox (Name() == "remote").
	// If FeishuUserID is set (web login via Feishu identity), use it for routing
	// so the user gets the same runner as on the Feishu side.
	sandboxUserID := cfg.SenderID
	if cfg.FeishuUserID != "" {
		sandboxUserID = cfg.FeishuUserID
	}
	resolvedSandbox := resolveSandbox(cfg.Sandbox, sandboxUserID)
	isRemote := resolvedSandbox != nil && resolvedSandbox.Name() == "remote"

	// For remote users, leave WorkspaceRoot/WorkingDir empty — the runner
	// manages its own filesystem. Host paths must not leak into ToolContext
	// for remote users (they cause server-side directory creation and
	// confuse path resolution).
	var workspaceRoot, workingDir string
	if !isRemote {
		workspaceRoot = cfg.WorkspaceRoot
		workingDir = cfg.WorkingDir
	}

	tc := &tools.ToolContext{
		Ctx:            ctx,
		AgentID:        cfg.AgentID,
		Channel:        cfg.Channel,
		ChatID:         cfg.ChatID,
		SenderID:       cfg.SenderID,
		OriginUserID:   cfg.OriginUserID,
		SenderName:     cfg.SenderName,
		SendFunc:       cfg.SendFunc,
		RootSessionKey: cfg.RootSessionKey,

		// 工作区 & 沙箱
		WorkingDir:           workingDir,
		WorkspaceRoot:        workspaceRoot,
		ReadOnlyRoots:        cfg.ReadOnlyRoots,
		SandboxReadOnlyRoots: sandboxReadOnlyRoots(cfg.ReadOnlyRoots, "", workspaceRoot),
		SkillsDirs:           cfg.SkillsDirs,
		AgentsDir:            cfg.AgentsDir,
		MCPConfigPath:        cfg.MCPConfigPath,
		GlobalMCPConfigPath:  cfg.GlobalMCPConfig,
		SandboxEnabled:       cfg.SandboxEnabled,
		PreferredSandbox:     cfg.PreferredSandbox,
		Sandbox:              resolvedSandbox,
		DataDir:              cfg.DataDir,

		// 注入入站消息
		InjectInbound: cfg.InjectInbound,

		// 工具注册表
		Registry: cfg.Tools,

		// 流式设置继承
		Stream: cfg.Stream,
	}

	// 注入 SpawnAgent（包装为 SubAgentManager 接口）
	if cfg.SpawnAgent != nil {
		// 使用 OriginUserID 构建 adapter（用于消息溯源）
		originUserID := cfg.OriginUserID
		if originUserID == "" {
			originUserID = cfg.SenderID // fallback：兼容旧数据
		}
		adapter := &spawnAgentAdapter{
			spawnFn:  cfg.SpawnAgent,
			parentID: cfg.AgentID,
			channel:  cfg.Channel,
			chatID:   cfg.ChatID,
			senderID: originUserID, // 使用原始用户 ID（用于消息溯源）
		}
		// 注入 Interactive callbacks（主 Agent 专有）
		if cb := cfg.InteractiveCallbacks; cb != nil {
			adapter.interactiveSpawnFn = cb.SpawnFn
			adapter.interactiveSendFn = cb.SendFn
			adapter.interactiveUnloadFn = cb.UnloadFn
			adapter.interactiveInterruptFn = cb.InterruptFn
			adapter.interactiveInspectFn = cb.InspectFn
		}
		tc.Manager = adapter
	}

	// 注入 Letta 记忆字段（覆盖上面的默认值）
	if ext := cfg.ToolContextExtras; ext != nil {
		tc.TenantID = ext.TenantID
		tc.CoreMemory = ext.CoreMemory
		tc.ArchivalMemory = ext.ArchivalMemory
		tc.MemorySvc = ext.MemorySvc
		tc.RecallTimeRange = ext.RecallTimeRange
		tc.ToolIndexer = ext.ToolIndexer
		if ext.InvalidateAllSessionMCP != nil {
			tc.InvalidateAllSessionMCP = ext.InvalidateAllSessionMCP
		}
	}

	// 注入 BgTaskManager 后台任务管理器
	if cfg.BgTaskManager != nil {
		tc.BgTaskManager = cfg.BgTaskManager
		sessionKey := cfg.SessionKey
		if sessionKey == "" {
			sessionKey = cfg.Channel + ":" + cfg.ChatID
		}
		tc.BgSessionKey = sessionKey
		// NOTE: OnComplete callback registration moved to Agent.bgNotifyLoop.
		// Engine no longer registers callbacks per-buildToolContext call.
	}

	// 注入 MessageSender（Dispatcher 引用，允许 Agent 向任何 Channel 发消息）
	tc.MessageSender = cfg.MessageSender
	// 注入 AgentChannel 注册/注销回调
	tc.RegisterAgentChannel = cfg.RegisterAgentChannel
	tc.UnregisterAgentChannel = cfg.UnregisterAgentChannel
	// 注入 ToolContext extras (memory, MCP, etc.)

	// 注入 session cwd（PWD 工具优化）
	if cfg.Session != nil {
		tc.CurrentDir = cfg.Session.GetCurrentDir()
		tc.SetCurrentDir = func(dir string) {
			cfg.Session.SetCurrentDir(dir)
		}
	} else {
		// No session — use InitialCWD for CWD persistence (SubAgent or sessionless mode).
		// SetCurrentDir must ALWAYS be set so Cd can persist CWD even when InitialCWD
		// starts empty (e.g., parent Agent never Cd'd before spawning SubAgent).
		cwd := cfg.InitialCWD
		if cwd != "" && cfg.Sandbox != nil && cfg.Sandbox.Name() != "none" && cfg.WorkspaceRoot != "" {
			sandboxWS := cfg.Sandbox.Workspace(cfg.OriginUserID)
			if sandboxWS != "" && strings.HasPrefix(cwd, cfg.WorkspaceRoot) {
				cwd = sandboxWS + cwd[len(cfg.WorkspaceRoot):]
			}
		}
		if cwd != "" {
			tc.CurrentDir = cwd
		}
		tc.SetCurrentDir = func(dir string) {
			cfg.InitialCWD = dir
		}
	}

	return tc
}

// CallChain 调用链上下文，用于追踪 Agent 间调用关系和防止递归。
type CallChain struct {
	Chain []string // 调用链: ["main", "main/code-reviewer"]
}

// DefaultMaxSubAgentDepth 默认 SubAgent 嵌套深度。
const DefaultMaxSubAgentDepth = 6

type callChainKey struct{}

// CallChainFromContext 从 context 中提取调用链。
func CallChainFromContext(ctx context.Context) *CallChain {
	if cc, ok := ctx.Value(callChainKey{}).(*CallChain); ok {
		return cc
	}
	return &CallChain{Chain: []string{"main"}}
}

// WithCallChain 将调用链注入 context。
func WithCallChain(ctx context.Context, cc *CallChain) context.Context {
	return context.WithValue(ctx, callChainKey{}, cc)
}

// CanSpawn 检查是否可以创建指定角色的 SubAgent。
// 返回 nil 表示可以，返回 error 表示不可以（深度超限或循环调用）。
// maxDepth 为最大允许深度，如果 <= 0 则使用默认值 DefaultMaxSubAgentDepth。
func (cc *CallChain) CanSpawn(targetRole string, maxDepth int) error {
	if maxDepth <= 0 {
		maxDepth = DefaultMaxSubAgentDepth
	}
	if len(cc.Chain) >= maxDepth {
		return fmt.Errorf("max SubAgent depth %d reached (chain: %v)", maxDepth, cc.Chain)
	}
	for _, id := range cc.Chain {
		role := id
		if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
			role = id[idx+1:]
		}
		if role == targetRole {
			return fmt.Errorf("circular SubAgent call: role %q already in chain %v", targetRole, cc.Chain)
		}
	}
	return nil
}

// Spawn 创建新的调用链（追加目标角色）。
func (cc *CallChain) Spawn(targetRole string) *CallChain {
	currentID := cc.Chain[len(cc.Chain)-1]
	newChain := make([]string, len(cc.Chain)+1)
	copy(newChain, cc.Chain)
	newChain[len(cc.Chain)] = currentID + "/" + targetRole
	return &CallChain{Chain: newChain}
}

// Depth 返回当前调用深度。
func (cc *CallChain) Depth() int {
	return len(cc.Chain)
}

// Current 返回当前 Agent ID。
func (cc *CallChain) Current() string {
	if len(cc.Chain) == 0 {
		return "main"
	}
	return cc.Chain[len(cc.Chain)-1]
}
