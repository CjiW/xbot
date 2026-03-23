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
// It is injected via context to allow parallel SubAgents to update parent
// progress lines without conflicting with each other.
type SubAgentProgressCallback func(roleName, line string)

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

	// === 工作区 & 沙箱 ===
	WorkingDir       string   // Agent 工作目录（宿主机）
	WorkspaceRoot    string   // 用户可读写工作区根目录（宿主机路径）
	SandboxWorkDir   string   // 沙箱内工作目录（如 /workspace）
	ReadOnlyRoots    []string // 额外只读目录
	SkillsDirs       []string // 全局 skill 目录列表
	AgentsDir        string
	MCPConfigPath    string // 用户 MCP 配置路径
	GlobalMCPConfig  string // 全局 MCP 配置路径（只读）
	DataDir          string // 数据持久化目录
	SandboxEnabled   bool   // 是否启用命令沙箱
	PreferredSandbox string // 沙箱类型（docker 优先）
	InitialCWD       string // 初始当前工作目录（宿主机路径，用于 SubAgent 继承父 Agent 的 CWD）

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
	SendFunc func(channel, chatID, content string) error

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

	// LLMTimeout 单次 LLM 调用超时（0 = 不设超时）
	LLMTimeout time.Duration

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

	// TodoManager TODO 管理器（可选）
	TodoManager TodoManagerProvider

	// RecallTracker 摘要精化追踪器（nil = 不启用，仅主 Agent）
	RecallTracker *RecallTracker

	// LLMSemAcquire is called before each LLM call to acquire a per-tenant
	// concurrency slot. Returns a release function that must be called after
	// the LLM call completes. If nil, no concurrency limiting is applied.
	LLMSemAcquire func() func()

	// EnableConcurrentSubAgents enables parallel execution of SubAgent tool calls.
	// When true, multiple SubAgent calls in the same iteration run concurrently,
	// bounded by SubAgentSem. Default false (backward compatible: sequential).
	EnableConcurrentSubAgents bool

	// SubAgentSem acquires a per-tenant semaphore slot for SubAgent execution.
	// It blocks until a slot is available and returns a release function.
	// If nil and EnableConcurrentSubAgents is true, no limit is applied.
	SubAgentSem func() func()
}

// TodoManagerProvider 提供 TODO 状态查询
type TodoManagerProvider interface {
	GetTodoSummary(sessionKey string) string
}

// InteractiveCallbacks 主 Agent 提供给 buildToolContext 的 interactive 回调。
type InteractiveCallbacks struct {
	SpawnFn  func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	SendFn   func(ctx context.Context, roleName string, msg bus.InboundMessage) (*bus.OutboundMessage, error)
	UnloadFn func(ctx context.Context, roleName string) error
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
//   - SubAgent: ToolExecutor=simpleExecutor, ProgressNotifier=nil, ContextManager=independent_phase1, ...
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
	var progressLines []string
	var progressMu sync.Mutex // 保护 progressLines 的并发读写 + notifyProgress 的串行化
	var lastContent string    // 用于 LLM 错误时的降级返回
	var iteration int         // 当前迭代次数（maybeCompress 闭包内需要访问）

	// 本轮对话的本地计数器（循环结束后一次性提交到 GlobalMetrics）
	localIterCount, localToolCalls, localLLMCalls, localInputTokens, localOutputTokens := 0, 0, 0, 0, 0

	// 确保 Run() 退出时总是记录对话指标
	// 使用闭包 defer 延迟求值，避免 defer 声明时计数器仍为 0
	defer func() {
		GlobalMetrics.RecordConversation(localIterCount, localToolCalls, localLLMCalls, localInputTokens, localOutputTokens)
		GlobalMetrics.ClearRecallTracking()
	}()

	// --- 结构化进度状态 ---
	var structuredProgress *StructuredProgress
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

	// --- 进度通知 ---
	notifyProgress := func(extra string) {
		if !autoNotify {
			return
		}
		lines := progressLines
		if extra != "" {
			lines = append(append([]string{}, progressLines...), extra)
		}
		// 在非引用行和引用行之间插入空行，避免飞书 markdown 渲染粘连
		var buf strings.Builder
		for i, line := range lines {
			if i > 0 {
				prev := lines[i-1]
				prevIsQuote := strings.HasPrefix(prev, "> ")
				currIsQuote := strings.HasPrefix(line, "> ")
				if prevIsQuote != currIsQuote {
					buf.WriteByte('\n')
				}
			}
			buf.WriteString(line)
			if i < len(lines)-1 {
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

		toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)
		toolTokens, _ := llm.CountToolsTokens(toolDefs, cfg.Model)

		// cachedMsgTokens 缓存 messages 的 token 数，避免在同一轮 maybeCompress 内重复计算。
		// 值为 0 表示缓存失效（messages 已变更），需要重新计算。
		var cachedMsgTokens int

		// --- Layer 0: Observation Masking ---
		// 在压缩之前先遮蔽旧的 tool result，减少上下文体积。
		if cfg.MaskStore != nil {
			cachedMsgTokens, _ = llm.CountMessagesTokens(messages, cfg.Model)
			totalTokens := cachedMsgTokens + toolTokens

			maxTokens := 100000
			if cfg.ContextManagerConfig != nil && cfg.ContextManagerConfig.MaxContextTokens > 0 {
				maxTokens = cfg.ContextManagerConfig.MaxContextTokens
			}

			maskingThreshold := float64(maxTokens) * 0.4
			if float64(totalTokens) > maskingThreshold {
				masked, count := MaskOldToolResults(messages, cfg.MaskStore, 3)
				if count > 0 {
					messages = syncMessages(masked)
					cachedMsgTokens = 0 // messages 已变更，缓存失效
					GlobalMetrics.MaskingEvents.Add(1)
					GlobalMetrics.MaskedItems.Add(int64(count))
					if autoNotify {
						progressLines = append(progressLines, fmt.Sprintf("> 🎭 上下文较大 (%d tokens)，已遮蔽 %d 条旧工具结果", totalTokens, count))
						notifyProgress("")
					}
					log.Ctx(ctx).WithField("masked_count", count).Info("Observation masking triggered")
				}
			}
		}

		// Phase 2: SmartCompressor 智能触发（动态阈值+冷却）
		if smart, ok := cm.(SmartCompressor); ok && smart.TriggerProvider() != nil && cfg.ContextManagerConfig != nil {
			provider := smart.TriggerProvider()
			triggerInfo := BuildTriggerInfo(iteration, messages, toolsUsed, provider, cfg.ContextManagerConfig, cfg.Model)
			if !smart.ShouldCompressDynamic(triggerInfo) {
				return
			}
			// 智能触发命中，继续执行压缩...
		} else if !cm.ShouldCompress(messages, cfg.Model, toolTokens) {
			return
		}

		if autoNotify {
			if cachedMsgTokens == 0 {
				cachedMsgTokens, _ = llm.CountMessagesTokens(messages, cfg.Model)
			}
			progressLines = append(progressLines, fmt.Sprintf("> 📦 上下文过大 (%d tokens)，正在压缩...", cachedMsgTokens+toolTokens))
			notifyProgress("")
		}

		log.Ctx(ctx).Info("Auto context compression triggered via ContextManager")

		result, compressErr := cm.Compress(ctx, messages, cfg.LLMClient, cfg.Model)
		if compressErr != nil {
			log.Ctx(ctx).WithError(compressErr).Warn("Auto context compression failed")
			return
		}

		oldTokenCount := cachedMsgTokens
		if oldTokenCount == 0 {
			oldTokenCount, _ = llm.CountMessagesTokens(messages, cfg.Model)
		}
		messages = syncMessages(result.LLMView)

		newTokenCount, _ := llm.CountMessagesTokens(result.LLMView, cfg.Model)
		if autoNotify {
			progressLines = append(progressLines, fmt.Sprintf("> ✅ 上下文压缩完成: %d → %d tokens", oldTokenCount, newTokenCount))
			notifyProgress("")
		}
		log.Ctx(ctx).WithFields(log.Fields{
			"new_tokens": newTokenCount,
		}).Info("Auto context compression completed")

		// 记录压缩指标
		GlobalMetrics.CompressEvents.Add(1)
		GlobalMetrics.CompressTokensIn.Add(int64(oldTokenCount))
		GlobalMetrics.CompressTokensOut.Add(int64(newTokenCount))

		// 摘要精化检查：压缩后评估是否需要补充高频召回信息
		if cfg.RecallTracker != nil && cfg.RecallTracker.ShouldRefine(iteration) {
			refinePrompt := cfg.RecallTracker.GenerateRefinePrompt()
			if refinePrompt != "" {
				// 将精化提示注入到压缩后的摘要中（追加到最后一条 assistant 消息）
				for j := len(messages) - 1; j >= 0; j-- {
					if messages[j].Role == "assistant" {
						messages[j].Content += "\n\n" + refinePrompt
						break
					}
				}
				cfg.RecallTracker.MarkRefine(iteration)
				GlobalMetrics.SummaryRefines.Add(1)
				log.Ctx(ctx).Info("Summary refine prompt injected after compression")
			}
		}

		// BUG FIX: 记录压缩迭代号 + 有效性检测。
		// 之前从未调用 RecordCompress，导致 Cooldown.ShouldTrigger 永远返回 true，
		// 每次迭代都触发压缩，压缩无效时形成死循环。
		// 新增：缩减率 <10% 视为低效，连续 2 次低效后加大冷却期到 10 次迭代。
		if smart, ok := cm.(SmartCompressor); ok && smart.TriggerProvider() != nil {
			smart.TriggerProvider().Cooldown.RecordCompress(iteration)
			if oldTokenCount > 0 {
				reductionRate := 1.0 - float64(newTokenCount)/float64(oldTokenCount)
				if reductionRate < 0.10 {
					log.Ctx(ctx).WithFields(log.Fields{
						"old_tokens": oldTokenCount,
						"new_tokens": newTokenCount,
						"reduction":  fmt.Sprintf("%.1f%%", reductionRate*100),
					}).Warn("Phase 2 compress: ineffective (reduction < 10%), increasing cooldown")
					smart.TriggerProvider().Cooldown.RecordIneffective()
				} else {
					smart.TriggerProvider().Cooldown.RecordEffective()
				}
			}
		}

		// 持久化压缩结果到 session
		if cfg.Session != nil {
			if err := cfg.Session.Clear(); err != nil {
				log.Ctx(ctx).WithError(err).Warn("Failed to clear session for auto compression, skipping persistence")
			} else {
				allOk := true
				for _, msg := range result.SessionView {
					assertNoSystemPersist(msg)
					if err := cfg.Session.AddMessage(msg); err != nil {
						log.Ctx(ctx).WithError(err).Error("Partial write during auto compression, session may be corrupted")
						allOk = false
						break
					}
				}
				if allOk {
					log.Ctx(ctx).Info("Auto compression persisted to session")
					// 调用 SessionHook（Phase 2 可能需要在此做额外操作，如更新话题分区索引）
					if hook := cm.SessionHook(); hook != nil {
						hook.AfterPersist(ctx, cfg.Session, result)
					}
				} else {
					log.Ctx(ctx).Warn("Auto compression persistence failed, using in-memory result only")
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
		// Clean offload data after conversation turn completes.
		// Only for the top-level agent (RootSessionKey == "") — SubAgents share
		// the parent's offload namespace and must not delete it while parent runs.
		if cfg.OffloadStore != nil && cfg.RootSessionKey == "" {
			cfg.OffloadStore.CleanSession(offloadSessionKey)
		}
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
		iteration = i
		localIterCount++
		// 更新结构化进度
		if structuredProgress != nil {
			structuredProgress.Iteration = i
			structuredProgress.Phase = PhaseThinking
			structuredProgress.ActiveTools = nil
		}
		maybeCompress()

		if autoNotify && i > 0 {
			notifyProgress("> 💭 思考中...")
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

		// LLM 调用（可选超时 + per-tenant 并发限流）
		var llmCtx context.Context
		var llmCancel context.CancelFunc
		if cfg.LLMTimeout > 0 {
			llmCtx, llmCancel = context.WithTimeout(retryNotifyCtx, cfg.LLMTimeout)
		} else {
			llmCtx, llmCancel = retryNotifyCtx, func() {}
		}

		// Acquire per-tenant LLM concurrency slot (if configured)
		var releaseLLMSem func()
		if cfg.LLMSemAcquire != nil {
			releaseLLMSem = cfg.LLMSemAcquire()
		}

		response, err := cfg.LLMClient.Generate(llmCtx, cfg.Model, messages, toolDefs, cfg.ThinkingMode)

		// Release LLM concurrency slot (unconditionally, even on error)
		if releaseLLMSem != nil {
			releaseLLMSem()
		}

		llmCancel()

		// 记录 LLM 调用指标（通过 local 变量，最终由 RecordConversation 统一入库）
		localLLMCalls++
		if response != nil {
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
					if autoNotify {
						newTokenCount, _ := llm.CountMessagesTokens(result.LLMView, cfg.Model)
						progressLines = append(progressLines, fmt.Sprintf("> ✅ 强制压缩完成 → %d tokens (estimated)", newTokenCount))
						notifyProgress("")
					}
					// 持久化压缩结果到 session（使用 SessionView，不含 tool 消息）
					if cfg.Session != nil {
						if clearErr := cfg.Session.Clear(); clearErr == nil {
							for _, msg := range result.SessionView {
								assertNoSystemPersist(msg)
								if addErr := cfg.Session.AddMessage(msg); addErr != nil {
									log.Ctx(ctx).WithError(addErr).Warn("Failed to persist force-compressed message")
									break
								}
							}
						}
					}
					// 重试 LLM 调用（使用 retryNotifyCtx 以保留重试通知回调）
					var retryCtx context.Context
					var retryCancel context.CancelFunc
					if cfg.LLMTimeout > 0 {
						retryCtx, retryCancel = context.WithTimeout(retryNotifyCtx, cfg.LLMTimeout)
					} else {
						retryCtx, retryCancel = retryNotifyCtx, func() {}
					}
					response, err = cfg.LLMClient.Generate(retryCtx, cfg.Model, messages, toolDefs, cfg.ThinkingMode)
					retryCancel()
					// 重试也要记录 LLM 调用指标（通过 local 变量）
					localLLMCalls++
					if response != nil {
						localInputTokens += int(response.Usage.PromptTokens)
						localOutputTokens += int(response.Usage.CompletionTokens)
					}
				}
			}

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
		if autoNotify {
			notifyProgress("")
		}

		execResults := make([]toolExecResult, len(response.ToolCalls))
		// 更新结构化进度：进入工具执行阶段
		if structuredProgress != nil {
			structuredProgress.Phase = PhaseToolExec
			structuredProgress.ActiveTools = make([]ToolProgress, len(response.ToolCalls))
			for j, tc := range response.ToolCalls {
				structuredProgress.ActiveTools[j] = ToolProgress{
					Name:   tc.Name,
					Label:  formatToolProgress(tc.Name, tc.Arguments),
					Status: ToolPending,
				}
			}
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
					}
					execFn(e)
					if release != nil {
						release()
					}
					// 每个 SubAgent 完成后立即 patch 进度
					if doAutoNotify {
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

			// 工具执行加超时（SubAgent 工具不加超时）
			var execCtx context.Context
			var cancel context.CancelFunc
			if tc.Name == "SubAgent" {
				execCtx = ctx
				// 为并行 SubAgent 注入进度回调：子 Agent 通过此回调更新父 Agent 的占位行，
				// 避免多个子 Agent 直接 patch 同一条消息导致互相覆盖。
				if autoNotify {
					pi := progressStartIdx + entry.index
					if pi < len(progressLines) {
						execCtx = WithSubAgentProgress(ctx, func(roleName, line string) {
							// 只取最后一行，避免多行内容破坏飞书 markdown blockquote
							if idx := strings.LastIndex(line, "\n"); idx >= 0 {
								line = line[idx+1:]
							}
							progressMu.Lock()
							progressLines[pi] = fmt.Sprintf("> 🔄 SubAgent [%s]: %s", roleName, line)
							notifyProgress("")
							progressMu.Unlock()
						})
					}
				}
				cancel = func() {}
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
					progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> ❌ %s (%s)", toolLabel, elapsed.Round(time.Millisecond))
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
					icon := "✅"
					if result.IsError {
						icon = "❌"
					}
					progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> %s %s (%s)", icon, toolLabel, elapsed.Round(time.Millisecond))
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
				if autoNotify {
					notifyProgress("")
				}
			}

			// Phase 2: 写操作串行执行
			for _, entry := range writeOps {
				execOne(entry)
				if autoNotify {
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
				if autoNotify {
					notifyProgress("")
				}
			}
		} else {
			// 全部串行执行（默认，向后兼容）
			for idx, tc := range response.ToolCalls {
				execOne(toolCallEntry{iteration: i, index: idx, tc: tc})
				if autoNotify {
					notifyProgress("")
				}
			}
		}

		// 更新结构化进度：所有工具执行完毕
		if structuredProgress != nil {
			structuredProgress.CompletedTools = append(structuredProgress.CompletedTools, structuredProgress.ActiveTools...)
			structuredProgress.ActiveTools = nil
		}

		// 计数 recall 工具调用（offload_recall / recall_masked）并通知 RecallTracker
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
				if cfg.RecallTracker != nil && r.result != nil {
					cfg.RecallTracker.RecordRecall(tc.ID, "offload", r.result.Summary)
				}
			case "recall_masked":
				// 从参数中提取 mask ID 用于去重计数
				var args struct {
					ID string `json:"id"`
				}
				if json.Unmarshal([]byte(tc.Arguments), &args) == nil && args.ID != "" {
					GlobalMetrics.RecordMaskedRecall(args.ID)
				} else {
					GlobalMetrics.MaskedRecalls.Add(1) // fallback
				}
				if cfg.RecallTracker != nil && r.result != nil {
					cfg.RecallTracker.RecordRecall(tc.ID, "masked", r.result.Summary)
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
				offloaded, wasOffloaded := cfg.OffloadStore.MaybeOffload(offloadSessionKey, tc.Name, tc.Arguments, offloadContent, cfg.WorkspaceRoot, cfg.SandboxWorkDir)
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
			staleIDs := cfg.OffloadStore.InvalidateStaleReads(offloadSessionKey, cfg.WorkspaceRoot, cfg.SandboxWorkDir)
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
		if len(response.ToolCalls) > 0 {
			var roundToolNames []string
			for _, tc2 := range response.ToolCalls {
				roundToolNames = append(roundToolNames, tc2.Name)
			}
			var todoSummary string
			if cfg.TodoManager != nil && sessionKey != "" {
				todoSummary = cfg.TodoManager.GetTodoSummary(sessionKey)
			}

			// 计算当前 token 使用量，用于 context_edit 双阶段提示
			var reminderCtx *ReminderContext
			if cfg.ContextManagerConfig != nil && cfg.ContextManagerConfig.MaxContextTokens > 0 {
				toolDefs := cfg.Tools.AsDefinitionsForSession(sessionKey)
				toolTokens, _ := llm.CountToolsTokens(toolDefs, cfg.Model)
				msgTokens, _ := llm.CountMessagesTokens(messages, cfg.Model)
				reminderCtx = &ReminderContext{
					MaxContextTokens: cfg.ContextManagerConfig.MaxContextTokens,
					UsedTokens:       msgTokens,
					ToolDefTokens:    toolTokens,
				}
			}

			reminder := BuildSystemReminder(messages, roundToolNames, todoSummary, cfg.AgentID, reminderCtx)
			if reminder != "" && len(messages) > 0 {
				lastIdx := len(messages) - 1
				messages[lastIdx].Content += "\n\n" + reminder
			}
		}

		// 如果有任何工具标记为等待用户响应，则停止循环
		if waitingUser {
			log.Ctx(ctx).Info("Tool is waiting for user response, ending loop without additional reply")
			return buildOutput(&bus.OutboundMessage{
				Channel:     cfg.Channel,
				ChatID:      cfg.ChatID,
				ToolsUsed:   toolsUsed,
				WaitingUser: true,
			})
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
	interactiveUnloadFn func(ctx context.Context, roleName string) error
}

// RunSubAgent 实现 tools.SubAgentManager 接口。
func (a *spawnAgentAdapter) RunSubAgent(parentCtx *tools.ToolContext, task string, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, roleName string) (string, error) {
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, false)
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
func (a *spawnAgentAdapter) SpawnInteractive(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities) (string, error) {
	if a.interactiveSpawnFn == nil {
		return "", fmt.Errorf("interactive mode not supported")
	}
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, true)
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
func (a *spawnAgentAdapter) SendInteractive(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities) (string, error) {
	if a.interactiveSendFn == nil {
		return "", fmt.Errorf("interactive mode not supported")
	}
	msg := a.buildMsg(parentCtx, task, roleName, systemPrompt, allowedTools, caps, true)
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
func (a *spawnAgentAdapter) UnloadInteractive(parentCtx *tools.ToolContext, roleName string) error {
	if a.interactiveUnloadFn == nil {
		return fmt.Errorf("interactive mode not supported")
	}
	return a.interactiveUnloadFn(parentCtx.Ctx, roleName)
}

// buildMsg 构造 SubAgent InboundMessage。
func (a *spawnAgentAdapter) buildMsg(parentCtx *tools.ToolContext, task, roleName, systemPrompt string, allowedTools []string, caps tools.SubAgentCapabilities, interactive bool) bus.InboundMessage {
	metadata := map[string]string{
		"origin_channel": a.channel,
		"origin_chat_id": a.chatID,
		"origin_sender":  a.senderID,
	}
	if interactive {
		metadata["interactive"] = "true"
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

// buildToolContext 统一构建 ToolContext。
// 从 RunConfig 中提取所有字段，主 Agent 和 SubAgent 使用同一个构建路径。
func buildToolContext(ctx context.Context, cfg *RunConfig) *tools.ToolContext {
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
		WorkingDir:          cfg.WorkingDir,
		WorkspaceRoot:       cfg.WorkspaceRoot,
		SandboxWorkDir:      cfg.SandboxWorkDir,
		ReadOnlyRoots:       cfg.ReadOnlyRoots,
		SkillsDirs:          cfg.SkillsDirs,
		AgentsDir:           cfg.AgentsDir,
		MCPConfigPath:       cfg.MCPConfigPath,
		GlobalMCPConfigPath: cfg.GlobalMCPConfig,
		SandboxEnabled:      cfg.SandboxEnabled,
		PreferredSandbox:    cfg.PreferredSandbox,
		DataDir:             cfg.DataDir,

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
		if cwd != "" && cfg.SandboxEnabled && cfg.WorkspaceRoot != "" && cfg.SandboxWorkDir != "" {
			if strings.HasPrefix(cwd, cfg.WorkspaceRoot) {
				cwd = cfg.SandboxWorkDir + cwd[len(cfg.WorkspaceRoot):]
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
