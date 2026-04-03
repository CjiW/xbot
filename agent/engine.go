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
	"xbot/memory"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
	"xbot/tools"

	log "xbot/logger"
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
	MaxIterations int // 0 = 使用默认值 100

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

	// ToolTimeout 单个工具调用超时（0 = 使用默认 120s）
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

	// DrainBgNotifications is called between iterations to check for completed bg tasks.
	// Returns tasks that should be injected as tool results into the current Run loop.
	// Returns nil when no notifications are pending. Called on each iteration.
	DrainBgNotifications func() []*tools.BackgroundTask

	// LLMSemAcquire is called before each LLM call to acquire a per-tenant
	// concurrency slot. Returns a release function that must be called after
	// the LLM call completes. If nil, no concurrency limiting is applied.
	LLMSemAcquire func() func()

	// RecordUserTokenUsage is called at the end of Run() to persist per-user
	// token usage (inputTokens, outputTokens, conversationCount, llmCallCount).
	// If nil, per-user tracking is skipped.
	RecordUserTokenUsage func(senderID string, inputTokens, outputTokens, conversationCount, llmCallCount int)

	// EnableConcurrentSubAgents enables parallel execution of SubAgent tool calls.
	// When true, multiple SubAgent calls in the same iteration run concurrently,
	// bounded by SubAgentSem. Default false (backward compatible: sequential).
	EnableConcurrentSubAgents bool

	// SubAgentSem acquires a per-tenant semaphore slot for SubAgent execution.
	// It blocks until a slot is available and returns a release function.
	// If nil and EnableConcurrentSubAgents is true, no limit is applied.
	SubAgentSem func() func()

	// BgTaskManager 后台任务管理器（nil = 不支持后台任务）
	BgTaskManager *tools.BackgroundTaskManager
}

// TodoManagerProvider 提供 TODO 状态查询
type TodoManagerProvider interface {
	GetTodoSummary(sessionKey string) string
	GetTodoItems(sessionKey string) []TodoProgressItem
}

// InteractiveCallbacks 主 Agent 提供给 buildToolContext 的 interactive 回调。
type InteractiveCallbacks struct {
	SpawnFn  func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	SendFn   func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	UnloadFn func(ctx context.Context, roleName, instance string) error
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
const DefaultMaxIterations = 100

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
}

// IterationSnapshot captures the tool summary of a completed iteration.
type IterationSnapshot struct {
	Iteration int                     `json:"iteration"`
	Thinking  string                  `json:"thinking,omitempty"`
	Tools     []IterationToolSnapshot `json:"tools"`
}

// IterationToolSnapshot captures a single tool's execution result within an iteration.
type IterationToolSnapshot struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"` // done | error
	ElapsedMS int64  `json:"elapsed_ms,omitempty"`
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

// generateResponse calls the LLM using streaming if available, falling back to Generate().
// This avoids blocking on the full response — streaming allows incremental data processing.
func generateResponse(ctx context.Context, client llm.LLM, model string, messages []llm.ChatMessage, tools []llm.ToolDefinition, thinkingMode string) (*llm.LLMResponse, error) {
	if streaming, ok := client.(llm.StreamingLLM); ok {
		eventCh, streamErr := streaming.GenerateStream(ctx, model, messages, tools, thinkingMode)
		if streamErr != nil {
			return nil, streamErr
		}
		return llm.CollectStream(ctx, eventCh)
	}
	return client.Generate(ctx, model, messages, tools, thinkingMode)
}

// - SubAgent: ToolExecutor=simpleExecutor, ProgressNotifier=nil, ContextManager=independent_phase1, ...
func Run(ctx context.Context, cfg RunConfig) *RunOutput {
	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = DefaultMaxIterations
	}

	sessionKey := cfg.SessionKey
	if sessionKey == "" && cfg.Channel != "" {
		sessionKey = cfg.Channel + ":" + cfg.ChatID
	}

	// offloadSessionKey: SubAgent 的 offload 数据存放在顶层 Agent 的 session 目录下，
	// 与 offload_recall 的 RootSessionKey 保持一致，避免 SubAgent 存了找不到。
	offloadSessionKey := sessionKey
	if cfg.RootSessionKey != "" {
		offloadSessionKey = cfg.RootSessionKey
	}

	toolExecutor := cfg.ToolExecutor
	if toolExecutor == nil {
		toolExecutor = defaultToolExecutor(&cfg)
	}

	toolTimeout := cfg.ToolTimeout
	if toolTimeout == 0 {
		toolTimeout = 120 * time.Second
	}

	messages := cfg.Messages
	initialMsgCount := len(messages)
	lastPersistedCount := initialMsgCount
	var lastPromptTokens int64        // 上一次 LLM 返回的精确 prompt token 数
	var lastMsgCountAtLLMCall int     // 上一次 LLM 调用时 messages 的长度（用于增量追踪）
	var disableCompressRetry bool

	// 初始化 ContextEditor 的消息引用（允许 context_edit 工具直接修改 messages）
	// syncMessages 闭包：每次 messages 被重赋值后调用，保持 ContextEditor 引用同步
	syncMessages := func(newMessages []llm.ChatMessage) []llm.ChatMessage {
		if cfg.ContextEditor != nil {
			cfg.ContextEditor.SetMessages(newMessages)
		}
		return newMessages
	}
	messages = syncMessages(messages)

	var toolsUsed []string
	var waitingUser bool
	var waitingQuestion string            // captured from tool result Summary for CLI panel
	var waitingMetadata map[string]string // captured from tool result Metadata
	var progressLines []string
	var progressMu sync.Mutex // 保护 progressLines 的并发读写 + notifyProgress 的串行化
	var lastContent string    // 用于 LLM 错误时的降级返回

	// 本轮对话的本地计数器（循环结束后一次性提交到 GlobalMetrics）
	localIterCount, localToolCalls, localLLMCalls, localInputTokens, localOutputTokens := 0, 0, 0, 0, 0

	// 确保 Run() 退出时总是记录对话指标
	// 使用闭包 defer 延迟求值，避免 defer 声明时计数器仍为 0
	defer func() {
		GlobalMetrics.RecordConversation(localIterCount, localToolCalls, localLLMCalls, localInputTokens, localOutputTokens)
		if cfg.RecordUserTokenUsage != nil && cfg.OriginUserID != "" {
			cfg.RecordUserTokenUsage(cfg.OriginUserID, localInputTokens, localOutputTokens, 1, localLLMCalls)
		}
		GlobalMetrics.ClearRecallTracking()
	}()

	// --- 结构化进度状态 ---
	var structuredProgress *StructuredProgress
	var iterationSnapshots []IterationSnapshot
	var progressFinalizer func() // set up after autoNotify/copyLines are available
	if cfg.ProgressEventHandler != nil {
		structuredProgress = &StructuredProgress{
			Phase:          PhaseThinking,
			Iteration:      0,
			ActiveTools:    nil,
			CompletedTools: nil,
		}
	}

	// copyLines 返回 progressLines 的浅拷贝（避免闭包捕获后修改）
	copyLines := func(lines []string) []string {
		cp := make([]string, len(lines))
		copy(cp, lines)
		return cp
	}

	autoNotify := cfg.ProgressNotifier != nil
	batchProgressByIteration := cfg.Channel == "web"

	// 确保 Run() 退出时总是发送 PhaseDone 事件，防止 CLI 进度卡在 spinner
	if structuredProgress != nil {
		progressFinalizer = func() {
			// Migrate any completed/errored tools from ActiveTools to CompletedTools
			// so they are preserved even on abnormal exit.
			if len(structuredProgress.ActiveTools) > 0 {
				for _, t := range structuredProgress.ActiveTools {
					if t.Status == ToolDone || t.Status == ToolError {
						structuredProgress.CompletedTools = append(structuredProgress.CompletedTools, t)
					}
				}
				structuredProgress.ActiveTools = nil
			}
			structuredProgress.Phase = PhaseDone
			if autoNotify && cfg.ProgressEventHandler != nil {
				cfg.ProgressEventHandler(&ProgressEvent{
					Lines:      copyLines(progressLines),
					Structured: structuredProgress,
					Timestamp:  time.Now(),
				})
			}
		}
		defer progressFinalizer()
	}

	// --- 进度通知 ---
	notifyProgress := func(extra string) {
		if !autoNotify {
			return
		}
		lines := progressLines
		if extra != "" {
			lines = append(append([]string{}, progressLines...), extra)
		}
		// 展平多行 entry（树状进度格式会在单个 progressLines 槽中包含 \n）
		var flatLines []string
		for _, line := range lines {
			flatLines = append(flatLines, strings.Split(line, "\n")...)
		}
		// 在非引用行和引用行之间插入空行，避免飞书 markdown 渲染粘连
		var buf strings.Builder
		for i, line := range flatLines {
			if i > 0 {
				prev := flatLines[i-1]
				prevIsQuote := strings.HasPrefix(prev, "> ")
				currIsQuote := strings.HasPrefix(line, "> ")
				if prevIsQuote != currIsQuote {
					buf.WriteByte('\n')
				}
			}
			buf.WriteString(line)
			if i < len(flatLines)-1 {
				buf.WriteByte('\n')
			}
		}
		cfg.ProgressNotifier([]string{buf.String()})
		// 结构化进度事件回调
		if cfg.ProgressEventHandler != nil && structuredProgress != nil {
			cfg.ProgressEventHandler(&ProgressEvent{
				Lines:      copyLines(progressLines),
				Structured: structuredProgress,
				Timestamp:  time.Now(),
			})
		}
	}

	// --- 自动压缩 ---
	maybeCompress := func() {
		cm := cfg.ContextManager
		if cm == nil || len(messages) <= 3 {
			return
		}

		maxTokens := 0
		if cfg.ContextManagerConfig != nil {
			maxTokens = cfg.ContextManagerConfig.MaxContextTokens
		}
		if maxTokens <= 0 {
			log.Ctx(ctx).WithFields(log.Fields{
				"last_prompt_tokens": lastPromptTokens,
				"msg_count":          len(messages),
			}).Info("maybeCompress skipped: maxTokens=0")
			return
		}

		// Base: API-returned prompt_tokens (exact, includes messages + tool defs).
		// Delta: messages appended after last LLM call (assistant tool_calls + tool results).
		// This avoids full local re-estimation while accounting for inter-iteration growth.
		totalTokens := lastPromptTokens
		if lastMsgCountAtLLMCall > 0 && lastMsgCountAtLLMCall < len(messages) {
			deltaMsgs := messages[lastMsgCountAtLLMCall:]
			deltaTokens, deltaErr := llm.CountMessagesTokens(deltaMsgs, cfg.Model)
			if deltaErr != nil {
				log.Ctx(ctx).WithError(deltaErr).Warn("maybeCompress: failed to count delta tokens")
			} else {
				totalTokens += int64(deltaTokens)
			}
		} else if lastPromptTokens == 0 {
			// First iteration: no API value yet, fall back to full local estimation
			toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)
			toolTokens, _ := llm.CountToolsTokens(toolDefs, cfg.Model)
			cachedMsgTokens, _ := llm.CountMessagesTokens(messages, cfg.Model)
			totalTokens = int64(cachedMsgTokens) + int64(toolTokens)
		}

		needCompress := len(messages) > 3 && shouldCompact(int(totalTokens), maxTokens)
		log.Ctx(ctx).WithFields(log.Fields{
			"total_tokens":      totalTokens,
			"max_tokens":        maxTokens,
			"threshold":         int(float64(maxTokens) * 0.75),
			"msg_count":         len(messages),
			"need":              needCompress,
			"base_prompt_tokens": lastPromptTokens,
			"source":            func() string {
				if lastPromptTokens == 0 {
				return "local"
				}
				if lastMsgCountAtLLMCall < len(messages) {
				return "api+delta"
				}
				return "api"
			}(),
		}).Info("maybeCompress check")

		if needCompress {
			// Set phase to compressing for structured progress (CLI status bar + progress block)
			if structuredProgress != nil {
				structuredProgress.Phase = PhaseCompressing
			}
			if autoNotify {
				progressLines = append(progressLines, fmt.Sprintf("> 📦 上下文过大 (%d tokens)，正在压缩 + 记忆整理...", totalTokens))
				notifyProgress("")
			}

			log.Ctx(ctx).Info("Auto context compaction triggered")

			// Inject memory tools for compaction (allows the LLM to archive
			// important context before it gets compacted away).
			if cfg.MemoryToolDefs != nil && cfg.MemoryToolExec != nil {
				cm.SetMemoryTools(cfg.MemoryToolDefs, cfg.MemoryToolExec)
			}

			result, compressErr := cm.Compress(ctx, messages, cfg.LLMClient, cfg.Model)
			if compressErr != nil {
				log.Ctx(ctx).WithError(compressErr).Warn("Auto context compaction failed")
				// Restore phase even on failure
				if structuredProgress != nil {
					structuredProgress.Phase = PhaseThinking
				}
				return
			}

			oldTokenCount := int(totalTokens)
			messages = syncMessages(result.LLMView)
			// Reset token tracking: compressed messages are completely different
			lastPromptTokens = 0
			lastMsgCountAtLLMCall = len(messages)

			newTokenCount, _ := llm.CountMessagesTokens(result.LLMView, cfg.Model)
			// Restore phase after compression completes
			if structuredProgress != nil {
				structuredProgress.Phase = PhaseThinking
			}
			if autoNotify {
				// Replace the "正在压缩" line with the completion line
				for i := len(progressLines) - 1; i >= 0; i-- {
					if strings.Contains(progressLines[i], "正在压缩") {
						progressLines[i] = fmt.Sprintf("> ✅ 压缩完成: %d → %d tokens", oldTokenCount, newTokenCount)
						break
					}
				}
				notifyProgress("")
			}

			log.Ctx(ctx).WithFields(log.Fields{
				"new_tokens": newTokenCount,
			}).Info("Auto context compaction completed")

			GlobalMetrics.CompressEvents.Add(1)
			GlobalMetrics.CompressTokensIn.Add(int64(oldTokenCount))
			GlobalMetrics.CompressTokensOut.Add(int64(newTokenCount))

			// Record effectiveness for logging
			if oldTokenCount > 0 {
				reductionRate := 1.0 - float64(newTokenCount)/float64(oldTokenCount)
				if reductionRate < 0.10 {
					log.Ctx(ctx).WithFields(log.Fields{
						"old_tokens": oldTokenCount,
						"new_tokens": newTokenCount,
						"reduction":  fmt.Sprintf("%.1f%%", reductionRate*100),
					}).Warn("Compaction ineffective (reduction < 10%)")
				}
			}

			// Persist compaction result to session
			if cfg.Session != nil {
				if err := cfg.Session.Clear(); err != nil {
					log.Ctx(ctx).WithError(err).Warn("Failed to clear session for auto compaction, skipping persistence")
				} else {
					allOk := true
					for _, msg := range result.SessionView {
						assertNoSystemPersist(msg)
						if err := cfg.Session.AddMessage(msg); err != nil {
							log.Ctx(ctx).WithError(err).Error("Partial write during auto compaction, session may be corrupted")
							allOk = false
							break
						}
					}
					if allOk {
						log.Ctx(ctx).Info("Auto compaction persisted to session")
						if hook := cm.SessionHook(); hook != nil {
							hook.AfterPersist(ctx, cfg.Session, result)
						}
					} else {
						log.Ctx(ctx).Warn("Auto compaction persistence failed, using in-memory result only")
					}
				}
			}
			return
		}

		// Layer 2: Observation masking (lightweight, no LLM call)
		if cfg.MaskStore != nil {
			maskingThreshold := float64(maxTokens) * 0.6
			if float64(totalTokens) > maskingThreshold {
				keepGroups := calculateKeepGroups(int(totalTokens), maxTokens)
				masked, count := MaskOldToolResults(messages, cfg.MaskStore, keepGroups)
				if count > 0 {
				messages = syncMessages(masked)
				GlobalMetrics.MaskingEvents.Add(1)
				GlobalMetrics.MaskedItems.Add(int64(count))

				// Sync masked messages back to session so that subsequent
				// buildPrompt calls (e.g. /context, next Run) see the same
				// reduced content instead of the original large tool results.
				if cfg.Session != nil {
					if err := cfg.Session.Clear(); err != nil {
						log.Ctx(ctx).WithError(err).Warn("Failed to clear session for masking sync")
					} else {
						allOk := true
						for _, msg := range messages {
						if msg.Role == "system" {
							continue
						}
						assertNoSystemPersist(msg)
						if err := cfg.Session.AddMessage(msg); err != nil {
							log.Ctx(ctx).WithError(err).Error("Partial write during masking sync, session may be corrupted")
							allOk = false
							break
						}
						}
						if allOk {
						lastPersistedCount = len(messages)
						log.Ctx(ctx).WithFields(log.Fields{
							"masked_count":     count,
							"persisted_msgs":   len(messages),
							"tokens_before":    totalTokens,
							"tokens_after_est": "see_next_iteration",
						}).Info("Masking synced to session")
						}
					}
				}

				if autoNotify {
					progressLines = append(progressLines, fmt.Sprintf("> 🎭 上下文较大 (%d tokens)，已遮蔽 %d 条旧工具结果", totalTokens, count))
					notifyProgress("")
				}
				log.Ctx(ctx).WithField("masked_count", count).Info("Observation masking triggered")
				}
			}
		}
	}

	// 推进 round 计数，自动清理长期未使用的工具激活
	if sessionKey != "" {
		cfg.Tools.TickSession(sessionKey)
	}

	// --- 注入 LLM 重试通知回调 ---
	retryNotifyCtx := llm.WithRetryNotify(ctx, func(attempt, max uint, err error) {
		if !autoNotify {
			return
		}
		reason := summarizeRetryError(err)
		progressLines = append(progressLines,
			fmt.Sprintf("> ⚠️ LLM 请求失败 (%s)，重试中 %d/%d ...", reason, attempt, max))
		notifyProgress("")
	})

	// buildOutput creates a RunOutput with messages populated when Memory is set.
	// This is used by SubAgent memory consolidation after Run() returns.
	buildOutput := func(ob *bus.OutboundMessage) *RunOutput {
		out := &RunOutput{OutboundMessage: ob}
		if cfg.Memory != nil {
			out.Messages = messages
		}
		// Always capture engine-produced messages (assistant + tool).
		// Only includes messages NOT yet incrementally persisted, so the cancel
		// path in processMessage can save them without creating duplicates.
		if len(messages) > lastPersistedCount {
			engineMsgs := make([]llm.ChatMessage, len(messages)-lastPersistedCount)
			copy(engineMsgs, messages[lastPersistedCount:])
			out.EngineMessages = engineMsgs
		}
		// Capture iteration history snapshots for UI display.
		if len(iterationSnapshots) > 0 {
			out.IterationHistory = iterationSnapshots
		}
		// Clean offload data after conversation turn completes.
		// Only for the top-level agent (RootSessionKey == "") — SubAgents share
		// the parent's offload namespace and must not delete it while parent runs.
		if cfg.OffloadStore != nil && cfg.RootSessionKey == "" {
			cfg.OffloadStore.CleanSession(offloadSessionKey)
		}
		out.LastPromptTokens = lastPromptTokens
		return out
	}

	// --- 主循环 ---

	// 工具执行相关类型（提取到循环外，避免每轮重新定义）
	type toolCallEntry struct {
		iteration int // Agent 循环迭代号（用于调试追踪）
		index     int // 本次 LLM 响应中 tool call 的序号
		tc        llm.ToolCall
	}
	type toolExecResult struct {
		content    string
		llmContent string
		result     *tools.ToolResult
		err        error
		elapsed    time.Duration
	}

	// --- 动态上下文注入器（CWD 变化检测）---
	// 主 Agent 通过 session.GetCurrentDir() 获取实时 CWD，
	// SubAgent 使用 cfg.InitialCWD（SubAgent 不支持 Cd，CWD 不变）。
	dynamicInjector := NewDynamicContextInjector(func() string {
		if cfg.Session != nil {
			if dir := cfg.Session.GetCurrentDir(); dir != "" {
				return dir
			}
		}
		return cfg.InitialCWD
	})

	for i := 0; i < maxIter; i++ {
		localIterCount++
		// 更新结构化进度
		if structuredProgress != nil {
			structuredProgress.Iteration = i
			structuredProgress.Phase = PhaseThinking
			structuredProgress.ActiveTools = nil
			structuredProgress.CompletedTools = nil
			structuredProgress.ThinkingContent = ""
		}
		// Refresh TODO state for progress display
		if structuredProgress != nil && cfg.TodoManager != nil && sessionKey != "" {
			todos := cfg.TodoManager.GetTodoItems(sessionKey)
			if len(todos) > 0 {
				structuredProgress.Todos = make([]TodoProgressItem, len(todos))
				copy(structuredProgress.Todos, todos)
			} else {
				structuredProgress.Todos = nil
			}
		}
		maybeCompress()

		if autoNotify {
			if i == 0 {
				notifyProgress("💭")
			} else {
				notifyProgress("> 💭 思考中...")
			}
		}

		// assert: 发给 LLM 的消息必须恰好一条 system
		// NOTE: 旧代码用 panic 暴露问题，新代码改为 log.Error + 返回错误消息。
		// 这是生产环境的改进（不应 panic），需确保日志监控能捕获此 Error 级别日志。
		var systemCount int
		for _, m := range messages {
			if m.Role == "system" {
				systemCount++
			}
		}
		if systemCount != 1 {
			log.Ctx(ctx).WithField("system_count", systemCount).Error("assert: LLM messages must have exactly one system message")
			return buildOutput(&bus.OutboundMessage{
				Channel: cfg.Channel,
				ChatID:  cfg.ChatID,
				Content: "内部错误：system 消息数量异常",
				Error:   fmt.Errorf("assert: LLM messages must have exactly one system message; got %d", systemCount),
			})
		}

		// 使用会话特定的工具定义
		toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)

		// LLM 调用（per-tenant 并发限流）
		// 注意：不在 engine 层设置 per-request 超时，由 RetryLLM.perAttemptCtx 管理。
		// engine 层的 context.WithTimeout 会导致 parent deadline 与 retry 冲突：
		// 第一次请求耗尽超时后，parent deadline 已过期，后续重试被立即取消。

		// Acquire per-tenant LLM concurrency slot (if configured).
		// Release after Generate (and potential retry) completes — NOT via defer,
		// because defer binds to Run() and would leak slots across loop iterations,
		// causing deadlock after <capacity> iterations.
		var releaseLLMSem func()
		if cfg.LLMSemAcquire != nil {
			releaseLLMSem = cfg.LLMSemAcquire()
		}

		response, err := generateResponse(retryNotifyCtx, cfg.LLMClient, cfg.Model, messages, toolDefs, cfg.ThinkingMode)

		// 记录 LLM 调用指标（通过 local 变量，最终由 RecordConversation 统一入库）
		localLLMCalls++
		if response != nil {
			lastPromptTokens = response.Usage.PromptTokens
			lastMsgCountAtLLMCall = len(messages)
			localInputTokens += int(response.Usage.PromptTokens)
			localOutputTokens += int(response.Usage.CompletionTokens)
		}

		if err != nil && llm.IsInputTooLongError(err) && len(messages) > 3 {
			// 输入超限时强制压缩上下文后重试
			log.Ctx(ctx).WithError(err).Warn("Input too long for LLM, forcing context compression and retrying")
			if autoNotify {
				progressLines = append(progressLines, "> ⚠️ 输入超限，正在强制压缩上下文...")
				notifyProgress("")
			}

			if cm := cfg.ContextManager; cm != nil {
				// 强制压缩：输入超限时，不检查阈值，直接压缩
				result, compressErr := cm.ManualCompress(ctx, messages, cfg.LLMClient, cfg.Model)
				if compressErr != nil {
					log.Ctx(ctx).WithError(compressErr).Warn("Forced context compression after input-too-long failed")
				} else {
					messages = syncMessages(result.LLMView)
					// Reset token tracking: compressed messages are completely different
					lastPromptTokens = 0
					lastMsgCountAtLLMCall = len(messages)
					if autoNotify {
						newTokenCount, _ := llm.CountMessagesTokens(result.LLMView, cfg.Model)
						progressLines = append(progressLines, fmt.Sprintf("> ✅ 强制压缩完成 → %d tokens (estimated)", newTokenCount))
						notifyProgress("")
					}
					// 持久化压缩结果到 session（使用 SessionView，不含 tool 消息）
					if cfg.Session != nil {
						if clearErr := cfg.Session.Clear(); clearErr != nil {
							log.Ctx(ctx).WithError(clearErr).Warn("Failed to clear session for force compression, skipping persistence")
						} else {
							for _, msg := range result.SessionView {
								assertNoSystemPersist(msg)
								if addErr := cfg.Session.AddMessage(msg); addErr != nil {
									log.Ctx(ctx).WithError(addErr).Warn("Failed to persist force-compressed message")
									break
								}
							}
						}
					}
					response, err = generateResponse(retryNotifyCtx, cfg.LLMClient, cfg.Model, messages, toolDefs, cfg.ThinkingMode)
					// 重试也要记录 LLM 调用指标（通过 local 变量）
					localLLMCalls++
					if response != nil {
						lastPromptTokens = response.Usage.PromptTokens
						lastMsgCountAtLLMCall = len(messages)
						localInputTokens += int(response.Usage.PromptTokens)
						localOutputTokens += int(response.Usage.CompletionTokens)
					}
				}
			}

		}

		// Release per-tenant LLM semaphore after Generate (+ optional retry) completes.
		if releaseLLMSem != nil {
			releaseLLMSem()
		}

		if err != nil {
			// 记录 LLM 错误（排除 context cancel 和 input-too-long 重试路径）
			if ctx.Err() == nil && !llm.IsInputTooLongError(err) {
				GlobalMetrics.TotalLLMErrors.Add(1)
			}
			if ctx.Err() != nil {
				return buildOutput(&bus.OutboundMessage{
					Channel:   cfg.Channel,
					ChatID:    cfg.ChatID,
					Content:   "Agent was cancelled.",
					Error:     ctx.Err(),
					ToolsUsed: toolsUsed,
				})
			}
			// LLM 错误时优雅降级：如果有之前的中间内容，返回它（附加错误提示）
			if lastContent != "" {
				log.Ctx(ctx).WithFields(log.Fields{
					"agent_id":  cfg.AgentID,
					"iteration": i + 1,
				}).Warnf("LLM failed, returning partial result: %v", err)
				return buildOutput(
					&bus.OutboundMessage{
						Channel:   cfg.Channel,
						ChatID:    cfg.ChatID,
						Content:   lastContent + "\n\n> ⚠️ LLM 调用失败 (" + summarizeRetryError(err) + ")，以上为部分结果。",
						ToolsUsed: toolsUsed,
					})
			}
			// 所有重试失败且无中间内容，返回用户友好的错误信息
			userErrMsg := fmt.Sprintf("❌ LLM 服务调用失败 (%s)，请稍后重试。", summarizeRetryError(err))
			return buildOutput(
				&bus.OutboundMessage{
					Channel:   cfg.Channel,
					ChatID:    cfg.ChatID,
					Content:   userErrMsg,
					Error:     fmt.Errorf("%w: %w", ErrLLMGenerate, err),
					ToolsUsed: toolsUsed,
				})
		}

		// 过滤 think 块
		cleanContent := llm.StripThinkBlocks(response.Content)

		if !response.HasToolCalls() {
			// 模型返回 context_window_exceeded：实际 token 超过模型上下文窗口，
			// 强制压缩后重试（我们的 token 计数可能低于实际值）。
			if response.FinishReason == llm.FinishReasonContextWindowExceeded {
				log.Ctx(ctx).WithFields(log.Fields{
					"msg_count":        len(messages),
					"last_prompt_tokens": lastPromptTokens,
					"finish_reason":    response.FinishReason,
				}).Warn("Model context window exceeded, forcing compression and retry")
				cm := cfg.ContextManager
				if cm != nil && !disableCompressRetry {
					disableCompressRetry = true // 只重试一次，防止死循环
					if cfg.MemoryToolDefs != nil && cfg.MemoryToolExec != nil {
						cm.SetMemoryTools(cfg.MemoryToolDefs, cfg.MemoryToolExec)
					}
					result, compressErr := cm.Compress(ctx, messages, cfg.LLMClient, cfg.Model)
					if compressErr != nil {
						log.Ctx(ctx).WithError(compressErr).Warn("Forced compression failed after context_window_exceeded")
					} else {
						messages = syncMessages(result.LLMView)
						// Reset token tracking: compressed messages are completely different
						lastPromptTokens = 0
						lastMsgCountAtLLMCall = len(messages)
						if cfg.Session != nil {
							_ = cfg.Session.Clear()
							for _, msg := range result.SessionView {
								assertNoSystemPersist(msg)
								_ = cfg.Session.AddMessage(msg)
							}
						}
						log.Ctx(ctx).Info("Forced compression completed after context_window_exceeded, retrying")
						continue
					}
				}
				// 压缩不可用或失败，返回错误提示
				return buildOutput(&bus.OutboundMessage{
					Channel:   cfg.Channel,
					ChatID:    cfg.ChatID,
					Content:   "⚠️ 模型上下文窗口已满，无法继续处理。请使用 /new 开始新对话。",
					ToolsUsed: toolsUsed,
				})
			}
			return buildOutput(&bus.OutboundMessage{
				Channel:     cfg.Channel,
				ChatID:      cfg.ChatID,
				Content:     cleanContent,
				ToolsUsed:   toolsUsed,
				WaitingUser: waitingUser,
			})
		}

		// 记录最新的中间内容，用于 LLM 错误时降级
		if cleanContent != "" {
			lastContent = cleanContent
		}

		// 模型的中间思考内容加入进度
		if autoNotify && cleanContent != "" {
			progressLines = append(progressLines, cleanContent)
		}
		// 更新结构化进度：记录思考内容
		if structuredProgress != nil && cleanContent != "" {
			structuredProgress.ThinkingContent = cleanContent
		}

		// 记录 assistant 消息（含 tool_calls），保留原始 content（包括 think 块）
		assistantMsg := llm.ChatMessage{
			Role:             "assistant",
			Content:          response.Content,
			ReasoningContent: response.ReasoningContent, // DeepSeek/OpenAI reasoning 模型的思维链
			ToolCalls:        response.ToolCalls,
		}
		messages = syncMessages(append(messages, assistantMsg))

		// --- 工具执行 ---

		// 为所有工具调用添加进度行占位符
		progressStartIdx := len(progressLines)
		for _, tc := range response.ToolCalls {
			toolsUsed = append(toolsUsed, tc.Name)
			localToolCalls++
			toolLabel := formatToolProgress(tc.Name, tc.Arguments)
			if autoNotify {
				progressLines = append(progressLines, fmt.Sprintf("> ⏳ %s ...", toolLabel))
			}
		}
		if autoNotify && !batchProgressByIteration {
			notifyProgress("")
		}

		execResults := make([]toolExecResult, len(response.ToolCalls))
		// 更新结构化进度：进入工具执行阶段
		if structuredProgress != nil {
			structuredProgress.Phase = PhaseToolExec
			structuredProgress.ActiveTools = make([]ToolProgress, len(response.ToolCalls))
			// Reset completed tools per iteration to avoid unbounded accumulation.
			structuredProgress.CompletedTools = nil
			// Frontend tracks max completed_tools via ref for turn persistence.
			for j, tc := range response.ToolCalls {
				structuredProgress.ActiveTools[j] = ToolProgress{
					Name:      tc.Name,
					Label:     formatToolProgress(tc.Name, tc.Arguments),
					Status:    ToolPending,
					Iteration: i,
				}
			}
		}
		if autoNotify && batchProgressByIteration {
			notifyProgress("")
		}

		// execOne 执行单个工具并记录结果。
		// 并发安全说明：execResults、progressLines、structuredProgress.ActiveTools
		// 的写入均按 entry.index 隔离到不同的 slice 元素，Go 中对不同 index 的并发写是安全的。

		// executeSubAgentOps 并发执行 SubAgent tool calls，受可选的 SubAgentSem 约束。
		// 每个子 Agent 完成后立即更新进度 patch，而非等全部完成。
		executeSubAgentOps := func(ops []toolCallEntry, execFn func(toolCallEntry), subAgentSem func() func(), doAutoNotify bool, np func(string)) {
			var wg sync.WaitGroup
			var mu sync.Mutex // 保护 np 调用的串行化（patch 同一条消息）
			for _, entry := range ops {
				wg.Add(1)
				go func(e toolCallEntry) {
					defer wg.Done()
					var release func()
					if subAgentSem != nil {
						release = subAgentSem()
						defer release() // defer 保证 panic 时也能释放信号量
					}
					execFn(e)
					// 每个 SubAgent 完成后立即 patch 进度
					if doAutoNotify && !batchProgressByIteration {
						mu.Lock()
						np("")
						mu.Unlock()
					}
				}(entry)
			}
			wg.Wait()
		}

		execOne := func(entry toolCallEntry) {
			tc := entry.tc
			argPreview := tc.Arguments
			if r := []rune(argPreview); len(r) > 200 {
				argPreview = string(r[:200]) + "..."
			}
			log.Ctx(ctx).WithFields(log.Fields{
				"tool":      tc.Name,
				"id":        tc.ID,
				"iteration": entry.iteration,
				"call_idx":  entry.index,
			}).Debugf("Tool call: %s(%s)", tc.Name, argPreview)

			// 工具执行加超时（SubAgent 工具使用独立的长超时）
			var execCtx context.Context
			var cancel context.CancelFunc
			if tc.Name == "SubAgent" {
				// SubAgent 不设独立超时，直接使用父 Agent 的 context（ctx 已携带主 session 的 deadline）
				execCtx, cancel = ctx, func() {}
				// 为并行 SubAgent 注入进度回调：子 Agent 通过此回调更新父 Agent 的占位行，
				// 避免多个子 Agent 直接 patch 同一条消息导致互相覆盖。
				if autoNotify {
					pi := progressStartIdx + entry.index
					if pi < len(progressLines) {
						execCtx = WithSubAgentProgress(execCtx, func(detail SubAgentProgressDetail) {
							progressMu.Lock()
							progressLines[pi] = formatSubAgentProgress(detail)
							progressMu.Unlock()
							notifyProgress("") // read progressLines outside lock (notifyProgress has its own snapshot)
						})
					}
				}
			} else {
				execCtx, cancel = context.WithTimeout(ctx, toolTimeout)
			}

			start := time.Now()
			// 更新结构化进度：工具开始执行
			if structuredProgress != nil && entry.index < len(structuredProgress.ActiveTools) {
				structuredProgress.ActiveTools[entry.index].Status = ToolRunning
			}
			// 临时替换 ToolExecutor 的 ctx
			result, execErr := toolExecutor(execCtx, tc)
			elapsed := time.Since(start)
			cancel()

			execResults[entry.index] = toolExecResult{err: execErr, result: result, elapsed: elapsed}
			// 更新结构化进度：工具执行完成
			if structuredProgress != nil && entry.index < len(structuredProgress.ActiveTools) {
				status := ToolDone
				if execErr != nil || (result != nil && result.IsError) {
					status = ToolError
				}
				structuredProgress.ActiveTools[entry.index].Status = status
				structuredProgress.ActiveTools[entry.index].Elapsed = elapsed
			}

			toolLabel := formatToolProgress(tc.Name, tc.Arguments)
			if execErr != nil {
				GlobalMetrics.TotalToolErrors.Add(1)
				log.Ctx(ctx).WithFields(log.Fields{
					"tool":    tc.Name,
					"elapsed": elapsed.Round(time.Millisecond),
				}).WithError(execErr).Debug("Tool failed (hook also logged)")
				execResults[entry.index].content = fmt.Sprintf("Error: %v\n\nPlease fix the issue and try again with corrected parameters.", execErr)
				execResults[entry.index].llmContent = execResults[entry.index].content

				if autoNotify {
					if tc.Name == "SubAgent" {
						line := progressLines[progressStartIdx+entry.index]
						if strings.Contains(line, "🔄") {
							// Progress callback fired — mark tree as failed while preserving structure
							progressLines[progressStartIdx+entry.index] = strings.ReplaceAll(line, "🔄", "❌")
						} else {
							// Progress callback never fired (e.g. immediate validation error like missing instance)
							// Replace the ⏳ placeholder with a clear ❌ completion line
							progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> ❌ %s (%s)", toolLabel, elapsed.Round(time.Millisecond))
						}
					} else {
						progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> ❌ %s (%s)", toolLabel, elapsed.Round(time.Millisecond))
					}
				}
			} else {
				execResults[entry.index].content = result.Summary
				execResults[entry.index].llmContent = buildToolMessageContent(result)

				if result.IsError {
					GlobalMetrics.TotalToolErrors.Add(1)
					execResults[entry.index].llmContent = fmt.Sprintf("Error: %s\n\nDo NOT retry the same command. Analyze the error, fix the root cause, then try a different approach.", execResults[entry.index].llmContent)
				}

				resultPreview := result.Summary
				if r := []rune(resultPreview); len(r) > 200 {
					resultPreview = string(r[:200]) + "..."
				}
				log.Ctx(ctx).WithFields(log.Fields{
					"tool":    tc.Name,
					"elapsed": elapsed.Round(time.Millisecond),
				}).Debugf("Tool done: %s", resultPreview)

				if autoNotify {
					// SubAgent progress callback already wrote a meaningful progress tree.
					// Mark ALL SubAgent progress lines as done (🔄 → ✅) without overwriting the tree.
					if tc.Name == "SubAgent" {
						progressLines[progressStartIdx+entry.index] = strings.ReplaceAll(
							progressLines[progressStartIdx+entry.index], "🔄", "✅")
					} else {
						icon := "✅"
						if result.IsError {
							icon = "❌"
						}
						progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> %s %s (%s)", icon, toolLabel, elapsed.Round(time.Millisecond))
					}
				}
			}
		}

		// 读写分离并行执行
		if cfg.EnableReadWriteSplit {
			var readOps, writeOps, subAgentOps []toolCallEntry
			for idx, tc := range response.ToolCalls {
				entry := toolCallEntry{iteration: i, index: idx, tc: tc}
				if tc.Name == "SubAgent" && cfg.EnableConcurrentSubAgents {
					subAgentOps = append(subAgentOps, entry)
				} else if readOnlyTools[tc.Name] {
					readOps = append(readOps, entry)
				} else {
					writeOps = append(writeOps, entry)
				}
			}

			// Phase 0: SubAgent 并发执行（受 SubAgentSem 约束）
			if len(subAgentOps) > 0 {
				executeSubAgentOps(subAgentOps, execOne, cfg.SubAgentSem, autoNotify, notifyProgress)
			}

			// Phase 1: 只读操作并行执行
			if len(readOps) > 0 {
				const maxParallel = 8
				sem := make(chan struct{}, maxParallel)
				var wg sync.WaitGroup
				for _, entry := range readOps {
					wg.Add(1)
					sem <- struct{}{}
					go func(e toolCallEntry) {
						defer wg.Done()
						defer func() { <-sem }()
						execOne(e)
					}(entry)
				}
				wg.Wait()
				if autoNotify && !batchProgressByIteration {
					notifyProgress("")
				}
			}

			// Phase 2: 写操作串行执行
			for _, entry := range writeOps {
				execOne(entry)
				if autoNotify && !batchProgressByIteration {
					notifyProgress("")
				}
			}
		} else if cfg.EnableConcurrentSubAgents {
			// SubAgent 并发执行（无读写分离时）
			var subAgentOps, otherOps []toolCallEntry
			for idx, tc := range response.ToolCalls {
				entry := toolCallEntry{iteration: i, index: idx, tc: tc}
				if tc.Name == "SubAgent" {
					subAgentOps = append(subAgentOps, entry)
				} else {
					otherOps = append(otherOps, entry)
				}
			}

			// SubAgent 并发执行
			if len(subAgentOps) > 0 {
				executeSubAgentOps(subAgentOps, execOne, cfg.SubAgentSem, autoNotify, notifyProgress)
			}

			// 其他操作串行执行
			for _, entry := range otherOps {
				execOne(entry)
				if autoNotify && !batchProgressByIteration {
					notifyProgress("")
				}
			}
		} else {
			// 全部串行执行（默认，向后兼容）
			for idx, tc := range response.ToolCalls {
				execOne(toolCallEntry{iteration: i, index: idx, tc: tc})
				if autoNotify && !batchProgressByIteration {
					notifyProgress("")
				}
			}
		}

		// 更新结构化进度：所有工具执行完毕
		if structuredProgress != nil {
			structuredProgress.CompletedTools = append(structuredProgress.CompletedTools, structuredProgress.ActiveTools...)
			structuredProgress.ActiveTools = nil
		}
		// 非 web 渠道（CLI）需要在 CompletedTools 填充后发一次事件，
		// 否则 CLI 无法在迭代切换时快照已完成工具到迭代历史。
		if autoNotify && !batchProgressByIteration && structuredProgress != nil {
			notifyProgress("")
		}
		// Snapshot completed iteration for IterationHistory
		if structuredProgress != nil && len(structuredProgress.CompletedTools) > 0 {
			snap := IterationSnapshot{
				Iteration: i,
				Thinking:  structuredProgress.ThinkingContent,
				Tools:     make([]IterationToolSnapshot, len(structuredProgress.CompletedTools)),
			}
			for j, t := range structuredProgress.CompletedTools {
				snap.Tools[j] = IterationToolSnapshot{
					Name:      t.Name,
					Label:     t.Label,
					Status:    string(t.Status),
					ElapsedMS: t.Elapsed.Milliseconds(),
				}
			}
			iterationSnapshots = append(iterationSnapshots, snap)
		}
		if autoNotify && batchProgressByIteration {
			notifyProgress("")
		}

		// Count recall tool calls (offload_recall / recall_masked) for metrics
		for idx2, tc := range response.ToolCalls {
			r := execResults[idx2]
			if r.err != nil {
				continue
			}
			switch tc.Name {
			case "offload_recall":
				// 从参数中提取 offload ID 用于去重计数
				var args struct {
					ID string `json:"id"`
				}
				if json.Unmarshal([]byte(tc.Arguments), &args) == nil && args.ID != "" {
					GlobalMetrics.RecordOffloadRecall(args.ID)
				} else {
					GlobalMetrics.OffloadedRecalls.Add(1) // fallback: 无法解析时仍然计数
				}
			case "recall_masked":
				var args struct {
					ID string `json:"id"`
				}
				if json.Unmarshal([]byte(tc.Arguments), &args) == nil && args.ID != "" {
					GlobalMetrics.RecordMaskedRecall(args.ID)
				} else {
					GlobalMetrics.MaskedRecalls.Add(1)
				}
			case "context_edit":
				GlobalMetrics.ContextEditEvents.Add(1)
			}
		}

		// 按原始顺序处理结果
		for idx, tc := range response.ToolCalls {
			r := execResults[idx]
			content := r.llmContent

			// Phase 2: Layer 1 Offload
			// Use raw Summary (with real newlines) instead of llmContent (JSON-serialized),
			// so summarizeRead can correctly split multi-line file content.
			// Skip offload_recall to prevent recursive offloading: its result would exceed
			// the threshold and get offloaded again, creating an infinite loop.
			// Skip Read with offset > 0: the LLM intentionally narrowed the result,
			// offloading would replace actual content with a summary, defeating the purpose.
			skipOffload := tc.Name == "offload_recall"
			if tc.Name == "Read" && readArgsHasOffsetOrLimit(tc.Arguments) {
				skipOffload = true
			}
			if cfg.OffloadStore != nil && r.err == nil && !skipOffload {
				offloadContent := content // default fallback
				if r.result != nil && r.result.Summary != "" {
					offloadContent = r.result.Summary
				}
				offloaded, wasOffloaded := cfg.OffloadStore.MaybeOffload(ctx, offloadSessionKey, tc.Name, tc.Arguments, offloadContent, cfg.WorkspaceRoot, "", cfg.OriginUserID)
				if wasOffloaded {
					content = offloaded.Summary
					GlobalMetrics.OffloadEvents.Add(1)
					GlobalMetrics.OffloadedItems.Add(1)
					log.Ctx(ctx).WithFields(log.Fields{
						"tool":         tc.Name,
						"offload_id":   offloaded.ID,
						"tokens_saved": offloaded.TokenSize,
					}).Info("Tool result offloaded")
				}
			}

			// OAuth 自动触发
			if r.err != nil && cfg.OAuthHandler != nil {
				if oauthContent, handled := cfg.OAuthHandler(ctx, tc, r.err); handled {
					content = oauthContent
					autoNotify = false
					if r.result != nil && r.result.WaitingUser {
						waitingUser = true
						if waitingQuestion == "" && r.result.Summary != "" {
							waitingQuestion = r.result.Summary
						}
						if len(r.result.Metadata) > 0 && waitingMetadata == nil {
							waitingMetadata = make(map[string]string)
							for k, v := range r.result.Metadata {
								waitingMetadata[k] = v
							}
						}
					}
				}
			}

			// 检查 sessionFinalSent
			if cfg.SessionFinalSentCallback != nil && cfg.SessionFinalSentCallback() {
				autoNotify = false
				progressLines = nil
			}

			if r.result != nil && r.result.WaitingUser {
				waitingUser = true
				// Capture question for CLI interactive panel (e.g., AskUser tool)
				if waitingQuestion == "" && r.result.Summary != "" {
					waitingQuestion = r.result.Summary
				}
				if len(r.result.Metadata) > 0 && waitingMetadata == nil {
					waitingMetadata = make(map[string]string)
					for k, v := range r.result.Metadata {
						waitingMetadata[k] = v
					}
				}
			}

			toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
			if r.result != nil && r.result.Detail != "" {
				toolMsg.Detail = r.result.Detail
			}
			messages = syncMessages(append(messages, toolMsg))
		}

		// Layer 1 Offload: invalidate stale Read offloads after any tool execution
		if cfg.OffloadStore != nil {
			// ReadPath from LLM is in sandbox format (e.g. /workspace/src/main.go).
			// os.ReadFile runs in xbot host process, so we pass both workspaceRoot and
			// sandboxWorkDir to convert sandbox→host paths before reading.
			staleIDs := cfg.OffloadStore.InvalidateStaleReads(ctx, offloadSessionKey, cfg.WorkspaceRoot, "", cfg.OriginUserID)
			if len(staleIDs) > 0 {
				log.Ctx(ctx).WithFields(log.Fields{
					"stale_count": len(staleIDs),
					"stale_ids":   staleIDs,
				}).Info("Stale offloads detected and invalidated")
				messages = syncMessages(cfg.OffloadStore.PurgeStaleMessages(offloadSessionKey, messages))
			}
		}

		// --- Dynamic Context 注入（CWD 变化检测）---
		// 在 sys_reminder 之前注入：dynamic-context 描述事实性环境变化，sys_reminder 描述行为引导
		dynamicInjector.InjectIfNeeded(messages)

		// --- System Reminder 注入（含双阶段 context_edit 提示）---
		// 只在最后一个 tool message 上追加，每轮迭代前先 strip 旧 reminder 避免重复。
		if len(response.ToolCalls) > 0 {
			// Strip previous reminder from earlier messages to avoid accumulation
			for idx := len(messages) - 2; idx >= 0; idx-- {
				if strings.Contains(messages[idx].Content, "<system-reminder>") {
					messages[idx].Content = stripSystemReminder(messages[idx].Content)
					break
				}
			}

			var roundToolNames []string
			for _, tc2 := range response.ToolCalls {
				roundToolNames = append(roundToolNames, tc2.Name)
			}
			var todoSummary string
			if cfg.TodoManager != nil && sessionKey != "" {
				todoSummary = cfg.TodoManager.GetTodoSummary(sessionKey)
			}

			reminder := BuildSystemReminder(messages, roundToolNames, todoSummary, cfg.AgentID)
			if reminder != "" && len(messages) > 0 {
				lastIdx := len(messages) - 1
				messages[lastIdx].Content += "\n\n" + reminder
			}
		}

		// --- 增量持久化：每轮迭代结束后立即将新消息写入 Session ---
		// 确保中途终止（kill/SIGTERM）不会丢失已完成的迭代进度。
		if cfg.Session != nil && len(messages) > lastPersistedCount {
			for _, msg := range messages[lastPersistedCount:] {
				if msg.Role == "system" {
					continue // system 消息不持久化
				}
				if err := cfg.Session.AddMessage(msg); err != nil {
					log.Ctx(ctx).WithError(err).Warn("Failed to incrementally persist engine message")
				}
			}
			lastPersistedCount = len(messages)
		}

		// --- 注入后台任务完成通知（迭代中：tool result） ---
		// bgNotifyLoop buffers into Agent.bgRunPending, which Run loop drains here.
		// Injected as assistant+tool message pair for LLM to process inline.
		// Also persisted immediately and shown in CLI iteration via CompletedTools.
		if cfg.DrainBgNotifications != nil {
			pending := cfg.DrainBgNotifications()
			for _, bgTask := range pending {
				bgContent := tools.FormatBgTaskCompletion(bgTask)
				bgAssistantMsg := llm.ChatMessage{
					Role:    "assistant",
					Content: "A background task has completed. Let me check the result.",
					ToolCalls: []llm.ToolCall{{
						ID:   "bg_" + bgTask.ID,
						Name: "background_task_result",
					}},
				}
				// Offload large bg task output (same as normal tool results)
				if cfg.OffloadStore != nil {
					if offloaded, ok := cfg.OffloadStore.MaybeOffload(ctx, offloadSessionKey, "background_task_result", "", bgContent, cfg.WorkspaceRoot, "", cfg.OriginUserID); ok {
						bgContent = offloaded.Summary
						GlobalMetrics.OffloadEvents.Add(1)
						GlobalMetrics.OffloadedItems.Add(1)
					}
				}
				bgToolMsg := llm.NewToolMessage("background_task_result", "bg_"+bgTask.ID, "", bgContent)
				messages = syncMessages(append(messages, bgAssistantMsg, bgToolMsg))
				log.Ctx(ctx).WithField("task_id", bgTask.ID).Info("Injected bg task completion into Run loop")

				// Persist immediately (don't wait for next iteration's incremental persist)
				if cfg.Session != nil {
					_ = cfg.Session.AddMessage(bgAssistantMsg)
					_ = cfg.Session.AddMessage(bgToolMsg)
					lastPersistedCount = len(messages)
				}

				// Show as completed tool in current iteration so CLI renders it inline
				if structuredProgress != nil {
					var elapsed time.Duration
					if bgTask.FinishedAt != nil {
						elapsed = bgTask.FinishedAt.Sub(bgTask.StartedAt)
					}
					structuredProgress.CompletedTools = append(structuredProgress.CompletedTools, ToolProgress{
						Name:      "background_task_result",
						Label:     fmt.Sprintf("bg:%s", bgTask.ID),
						Status:    ToolDone,
						Elapsed:   elapsed,
						Iteration: i, // belongs to current iteration
					})
					if autoNotify {
						notifyProgress("")
					}
				}
			}
		}

		// 如果有任何工具标记为等待用户响应，则停止循环
		if waitingUser {
			log.Ctx(ctx).Info("Tool is waiting for user response, ending loop without additional reply")
			outMsg := &bus.OutboundMessage{
				Channel:     cfg.Channel,
				ChatID:      cfg.ChatID,
				ToolsUsed:   toolsUsed,
				WaitingUser: true,
			}
			if waitingQuestion != "" || len(waitingMetadata) > 0 {
				outMsg.Metadata = make(map[string]string)
				if waitingQuestion != "" {
					outMsg.Metadata["ask_question"] = waitingQuestion
				}
				for k, v := range waitingMetadata {
					outMsg.Metadata[k] = v
				}
			}
			return buildOutput(outMsg)
		}
	}

	return buildOutput(&bus.OutboundMessage{
		Channel:   cfg.Channel,
		ChatID:    cfg.ChatID,
		Content:   "已达到最大迭代次数，请重新描述你的需求。",
		ToolsUsed: toolsUsed,
	})
}

// defaultToolExecutor creates the default tool executor (looks up from Registry and executes).
// Used for SubAgent and other scenarios that don't need session MCP / activation checks.
func defaultToolExecutor(cfg *RunConfig) func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
	return func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error) {
		tool, ok := cfg.Tools.Get(tc.Name)
		if !ok {
			return nil, fmt.Errorf("unknown tool: %s", tc.Name)
		}

		toolCtx := buildToolContext(ctx, cfg)

		// Run pre-tool hooks
		if cfg.HookChain != nil {
			if err := cfg.HookChain.RunPre(ctx, tc.Name, tc.Arguments); err != nil {
				return nil, fmt.Errorf("pre-tool hook blocked %q: %w", tc.Name, err)
			}
		}

		start := time.Now()
		result, err := tool.Execute(toolCtx, tc.Arguments)
		elapsed := time.Since(start)

		// Run post-tool hooks (always, even on error)
		if cfg.HookChain != nil {
			cfg.HookChain.RunPost(ctx, tc.Name, tc.Arguments, result, err, elapsed)
		}

		return result, err
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
	interactiveSpawnFn  func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	interactiveSendFn   func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	interactiveUnloadFn func(ctx context.Context, roleName, instance string) error
}

// RunSubAgent 实现 tools.SubAgentManager 接口。
func (a *spawnAgentAdapter) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, roleName string) (string, error) {
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, false, "")
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
func (a *spawnAgentAdapter) SpawnInteractive(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, instance string) (string, error) {
	if a.interactiveSpawnFn == nil {
		return "", fmt.Errorf("interactive mode not supported")
	}
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, true, instance)
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
func (a *spawnAgentAdapter) SendInteractive(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, instance string) (string, error) {
	if a.interactiveSendFn == nil {
		return "", fmt.Errorf("interactive mode not supported")
	}
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, true, instance)
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

// buildMsg 构造 SubAgent InboundMessage。
func (a *spawnAgentAdapter) buildMsg(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, interactive bool, instance string) bus.InboundMessage {
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
