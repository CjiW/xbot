package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/bus"
	"xbot/clipanic"
	"xbot/llm"
	log "xbot/logger"

	"xbot/tools"
)

// toolCallEntry tracks a single tool call within an iteration.
type toolCallEntry struct {
	iteration int // Agent loop iteration number (for debug tracing)
	index     int // Index within the LLM response's tool calls
	tc        llm.ToolCall
}

// toolExecResult holds the result of executing a single tool call.
type toolExecResult struct {
	content    string
	llmContent string
	result     *tools.ToolResult
	err        error
	elapsed    time.Duration
}

// runState holds the mutable state for a single Run() execution.
// It bundles all loop-local variables so extracted methods can share state
// without passing dozens of parameters.
type runState struct {
	// Configuration (read-only after init)
	cfg                      RunConfig
	maxIter                  int
	sessionKey               string
	offloadSessionKey        string
	toolExecutor             func(ctx context.Context, tc llm.ToolCall) (*tools.ToolResult, error)
	toolTimeout              time.Duration
	autoNotify               bool
	batchProgressByIteration bool
	dynamicInjector          *DynamicContextInjector

	// Messages
	messages           []llm.ChatMessage
	initialMsgCount    int
	lastPersistedCount int

	// Token tracking
	lastPromptTokens      int64
	lastCompletionTokens  int64
	lastMsgCountAtLLMCall int
	restoredFromDB        bool
	hadLLMCall            bool

	// Loop state
	toolsUsed            []string
	waitingUser          bool
	waitingQuestion      string
	waitingMetadata      map[string]string
	lastContent          string
	disableCompressRetry bool
	compressAttempts     int
	lastCompressIter     int

	// Metrics (local counters for this Run)
	localIterCount    int
	localToolCalls    int
	localLLMCalls     int
	localInputTokens  int
	localOutputTokens int
	localCachedTokens int

	// Progress
	progressLines      []string
	progressMu         sync.Mutex
	structuredProgress *StructuredProgress
	iterationSnapshots []IterationSnapshot
	progressFinalizer  func()
}

// newRunState creates and initializes a runState from the given RunConfig.
func newRunState(cfg RunConfig) *runState {
	maxIter := cfg.MaxIterations
	if maxIter == 0 {
		maxIter = DefaultMaxIterations
	}

	sessionKey := cfg.SessionKey
	if sessionKey == "" && cfg.Channel != "" {
		sessionKey = cfg.Channel + ":" + cfg.ChatID
	}

	offloadSessionKey := sessionKey
	if cfg.RootSessionKey != "" {
		offloadSessionKey = cfg.RootSessionKey
	}

	toolExecutor := cfg.ToolExecutor
	if toolExecutor == nil {
		toolExecutor = defaultToolExecutor(&cfg)
	}

	// toolTimeout is kept for API compat but no longer used to wrap tool contexts.
	// Individual tools manage their own timeouts; engine only passes through the
	// parent context (which carries user cancellation via Ctrl+C).
	toolTimeout := cfg.ToolTimeout

	messages := copyMessages(cfg.Messages)
	for i := range messages {
		if messages[i].Role != "system" && strings.Contains(messages[i].Content, "<system-reminder>") {
			messages[i].Content = stripSystemReminder(messages[i].Content)
		}
	}

	autoNotify := cfg.ProgressNotifier != nil
	batchProgressByIteration := cfg.Channel == "web"

	return &runState{
		cfg:                      cfg,
		maxIter:                  maxIter,
		sessionKey:               sessionKey,
		offloadSessionKey:        offloadSessionKey,
		toolExecutor:             toolExecutor,
		toolTimeout:              toolTimeout,
		autoNotify:               autoNotify,
		batchProgressByIteration: batchProgressByIteration,
		messages:                 messages,
		initialMsgCount:          len(messages),
		lastPersistedCount:       len(messages),
		lastPromptTokens:         cfg.LastPromptTokens,
		lastCompletionTokens:     cfg.LastCompletionTokens,
		restoredFromDB:           cfg.LastPromptTokens > 0,
	}
}

// initProgress sets up structured progress tracking and the progress finalizer.
func (s *runState) initProgress() {
	if s.cfg.ProgressEventHandler != nil || s.cfg.OnIterationSnapshot != nil {
		s.structuredProgress = &StructuredProgress{
			Phase:          PhaseThinking,
			Iteration:      0,
			ActiveTools:    nil,
			CompletedTools: nil,
		}
	}

	copyLines := func(lines []string) []string {
		cp := make([]string, len(lines))
		copy(cp, lines)
		return cp
	}

	if s.structuredProgress != nil {
		s.progressFinalizer = func() {
			if len(s.structuredProgress.ActiveTools) > 0 {
				for _, t := range s.structuredProgress.ActiveTools {
					if t.Status == ToolDone || t.Status == ToolError {
						s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, t)
					}
				}
				s.structuredProgress.ActiveTools = nil
			}
			s.structuredProgress.Phase = PhaseDone
			if s.autoNotify && s.cfg.ProgressEventHandler != nil {
				s.cfg.ProgressEventHandler(&ProgressEvent{
					Lines:      copyLines(s.progressLines),
					Structured: s.structuredProgress,
					Timestamp:  time.Now(),
				})
			}
		}
	}
}

// initDynamicInjector sets up the dynamic context injector for CWD change detection.
func (s *runState) initDynamicInjector() {
	s.dynamicInjector = NewDynamicContextInjector(func() string {
		if s.cfg.Session != nil {
			if dir := s.cfg.Session.GetCurrentDir(); dir != "" {
				return dir
			}
		}
		return s.cfg.InitialCWD
	})
}

// tickSession advances the round counter for tool activation cleanup.
func (s *runState) tickSession() {
	if s.sessionKey != "" {
		s.cfg.Tools.TickSession(s.sessionKey)
	}
}

// cleanupTodos clears completed TODOs. Called via defer from Run().
func (s *runState) cleanupTodos() {
	if s.cfg.TodoManager != nil && s.sessionKey != "" {
		items := s.cfg.TodoManager.GetTodoItems(s.sessionKey)
		if len(items) > 0 {
			allDone := true
			for _, item := range items {
				if !item.Done {
					allDone = false
					break
				}
			}
			if allDone {
				s.cfg.TodoManager.ClearTodos(s.sessionKey)
			}
		}
	}
}

// recordMetrics records conversation metrics. Called via defer from Run().
func (s *runState) recordMetrics() {
	GlobalMetrics.RecordConversation(s.localIterCount, s.localToolCalls, s.localLLMCalls, s.localInputTokens, s.localOutputTokens)
	if s.cfg.RecordUserTokenUsage != nil && s.cfg.SenderID != "" {
		s.cfg.RecordUserTokenUsage(s.cfg.SenderID, s.cfg.Model, s.localInputTokens, s.localOutputTokens, s.localCachedTokens, 1, s.localLLMCalls)
	}
	GlobalMetrics.ClearRecallTracking()
}

// accumulateCompressUsage adds compression LLM token usage to the local counters
// so they are included in recordMetrics (and thus /usage).
func (s *runState) accumulateCompressUsage(result *CompressResult) {
	if result == nil {
		return
	}
	s.localLLMCalls += result.LLMCalls
	s.localInputTokens += int(result.InputTokens)
	s.localOutputTokens += int(result.OutputTokens)
	s.localCachedTokens += int(result.CachedTokens)
}

// syncMessages syncs the ContextEditor reference when messages are reassigned.
func (s *runState) syncMessages(newMessages []llm.ChatMessage) []llm.ChatMessage {
	if s.cfg.ContextEditor != nil {
		s.cfg.ContextEditor.SetMessages(newMessages)
	}
	return newMessages
}

// notifyProgress sends progress notification to the configured callback.
func (s *runState) notifyProgress(extra string) {
	if !s.autoNotify {
		return
	}
	s.progressMu.Lock()
	lines := make([]string, len(s.progressLines))
	copy(lines, s.progressLines)
	s.progressMu.Unlock()
	if extra != "" {
		lines = append(lines, extra)
	}
	var flatLines []string
	for _, line := range lines {
		flatLines = append(flatLines, strings.Split(line, "\n")...)
	}
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
	s.cfg.ProgressNotifier([]string{buf.String()})
	if s.cfg.ProgressEventHandler != nil && s.structuredProgress != nil {
		s.progressMu.Lock()
		snapshot := make([]string, len(s.progressLines))
		copy(snapshot, s.progressLines)
		s.progressMu.Unlock()
		s.cfg.ProgressEventHandler(&ProgressEvent{
			Lines:      snapshot,
			Structured: s.structuredProgress,
			Timestamp:  time.Now(),
		})
	}
}

// setupRetryNotify returns a context wrapped with LLM retry notification.
func (s *runState) setupRetryNotify(ctx context.Context) context.Context {
	return llm.WithRetryNotify(ctx, func(attempt, max uint, err error) {
		if !s.autoNotify {
			return
		}
		reason := summarizeRetryError(err)
		s.progressLines = append(s.progressLines,
			fmt.Sprintf("> ⚠️ LLM 请求失败 (%s)，重试中 %d/%d ...", reason, attempt, max))
		s.notifyProgress("")
	})
}

// buildOutput creates a RunOutput from an OutboundMessage.
func (s *runState) buildOutput(ob *bus.OutboundMessage) *RunOutput {
	out := &RunOutput{OutboundMessage: ob}
	if s.cfg.Memory != nil {
		out.Messages = s.messages
	}
	if len(s.messages) > s.lastPersistedCount {
		engineMsgs := make([]llm.ChatMessage, len(s.messages)-s.lastPersistedCount)
		copy(engineMsgs, s.messages[s.lastPersistedCount:])
		out.EngineMessages = engineMsgs
	}
	if len(s.iterationSnapshots) > 0 {
		out.IterationHistory = s.iterationSnapshots
	}
	out.LastPromptTokens = s.lastPromptTokens
	out.LastCompletionTokens = s.lastCompletionTokens
	if s.cfg.SaveTokenState != nil && s.hadLLMCall && s.lastPromptTokens > 0 {
		s.cfg.SaveTokenState(s.lastPromptTokens, s.lastCompletionTokens)
	}
	return out
}

// beginIteration updates state at the start of each loop iteration.
func (s *runState) beginIteration(i int) {
	s.localIterCount++
	if s.structuredProgress != nil {
		s.structuredProgress.Iteration = i
		s.structuredProgress.Phase = PhaseThinking
		s.structuredProgress.ActiveTools = nil
		s.structuredProgress.CompletedTools = nil
		s.structuredProgress.ThinkingContent = ""
		s.structuredProgress.ReasoningContent = ""
	}
	if s.structuredProgress != nil && s.cfg.TodoManager != nil && s.sessionKey != "" {
		todos := s.cfg.TodoManager.GetTodoItems(s.sessionKey)
		if len(todos) > 0 {
			s.structuredProgress.Todos = make([]TodoProgressItem, len(todos))
			copy(s.structuredProgress.Todos, todos)
		} else {
			s.structuredProgress.Todos = nil
		}
	}
}

// notifyThinking sends the thinking progress notification.
func (s *runState) notifyThinking(iteration int) {
	if s.autoNotify {
		if iteration == 0 {
			s.notifyProgress("💭")
		} else {
			s.notifyProgress("> 💭 思考中...")
		}
	}
}

// assertSystemMessages checks that messages have exactly one system message.
// Returns a RunOutput error if the assertion fails, nil otherwise.
func (s *runState) assertSystemMessages(ctx context.Context) *RunOutput {
	var systemCount int
	for _, m := range s.messages {
		if m.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 1 {
		log.Ctx(ctx).WithField("system_count", systemCount).Error("assert: LLM messages must have exactly one system message")
		return s.buildOutput(&bus.OutboundMessage{
			Channel: s.cfg.Channel,
			ChatID:  s.cfg.ChatID,
			Content: "内部错误：system 消息数量异常",
			Error:   fmt.Errorf("assert: LLM messages must have exactly one system message; got %d", systemCount),
		})
	}
	return nil
}

// callLLM invokes the LLM with the current messages, handling per-tenant
// concurrency semaphore and input-too-long errors with forced compression.
func (s *runState) callLLM(ctx context.Context, retryNotifyCtx context.Context) (*llm.LLMResponse, error) {
	toolDefs := visibleToolDefs(s.cfg.Tools.AsDefinitionsForSession(s.sessionKey), s.cfg.SettingsSvc, s.cfg.Channel, s.cfg.OriginUserID)

	var releaseLLMSem func()
	if s.cfg.LLMSemAcquire != nil {
		releaseLLMSem = s.cfg.LLMSemAcquire(ctx)
	}

	response, err := generateResponse(retryNotifyCtx, s.cfg.LLMClient, s.cfg.Model, s.messages, toolDefs, s.cfg.ThinkingMode, s.cfg.Stream, s.cfg.StreamContentFunc, s.cfg.StreamReasoningFunc)

	s.localLLMCalls++
	if response != nil {
		s.lastPromptTokens = response.Usage.PromptTokens
		s.lastCompletionTokens = response.Usage.CompletionTokens
		s.lastMsgCountAtLLMCall = len(s.messages)
		s.hadLLMCall = true
		s.localInputTokens += int(response.Usage.PromptTokens)
		s.localOutputTokens += int(response.Usage.CompletionTokens)
		s.localCachedTokens += int(response.Usage.CacheHitTokens)
	}

	if err != nil && llm.IsInputTooLongError(err) && len(s.messages) > 3 {
		response, err = s.handleInputTooLong(ctx, retryNotifyCtx, toolDefs)
	}

	if releaseLLMSem != nil {
		releaseLLMSem()
	}

	return response, err
}

// handleInputTooLong forces context compression when input exceeds model limits,
// then retries the LLM call.
func (s *runState) handleInputTooLong(ctx context.Context, retryNotifyCtx context.Context, toolDefs []llm.ToolDefinition) (*llm.LLMResponse, error) {
	log.Ctx(ctx).WithError(fmt.Errorf("input too long")).Warn("Input too long for LLM, forcing context compression and retrying")
	if s.autoNotify {
		s.progressLines = append(s.progressLines, "> ⚠️ 输入超限，正在强制压缩上下文...")
		s.notifyProgress("")
	}

	cm := s.cfg.ContextManager
	if cm == nil {
		return nil, fmt.Errorf("input too long")
	}

	result, compressErr := cm.ManualCompress(ctx, s.messages, s.cfg.LLMClient, s.cfg.Model)
	if compressErr != nil {
		log.Ctx(ctx).WithError(compressErr).Warn("Forced context compression after input-too-long failed")
		return nil, compressErr
	}
	s.accumulateCompressUsage(result)

	s.messages = s.syncMessages(result.LLMView)
	newCount, _ := llm.CountMessagesTokens(result.LLMView, s.cfg.Model)
	s.lastPromptTokens = int64(newCount)
	s.lastCompletionTokens = 0
	s.lastMsgCountAtLLMCall = len(s.messages)
	if s.autoNotify {
		s.progressLines = append(s.progressLines, fmt.Sprintf("> ✅ 强制压缩完成 → %d tokens (estimated)", newCount))
		s.notifyProgress("")
	}
	if s.cfg.Session != nil {
		if clearErr := s.cfg.Session.Clear(); clearErr != nil {
			log.Ctx(ctx).WithError(clearErr).Warn("Failed to clear session for force compression, skipping persistence")
		} else {
			allOk := true
			for _, msg := range result.SessionView {
				if err := assertNoSystemPersist(msg); err != nil {
					continue
				}
				if addErr := s.cfg.Session.AddMessage(msg); addErr != nil {
					log.Ctx(ctx).WithError(addErr).Warn("Failed to persist force-compressed message")
					allOk = false
					break
				}
			}
			if allOk {
				s.lastPersistedCount = len(s.messages)
			}
		}
	}
	compressCutoff := time.Now()
	if s.cfg.OffloadStore != nil {
		s.cfg.OffloadStore.CleanOldEntries(s.offloadSessionKey, compressCutoff)
	}
	if s.cfg.MaskStore != nil {
		s.cfg.MaskStore.CleanOldEntries(compressCutoff)
	}

	response, err := generateResponse(retryNotifyCtx, s.cfg.LLMClient, s.cfg.Model, s.messages, toolDefs, s.cfg.ThinkingMode, s.cfg.Stream, s.cfg.StreamContentFunc, s.cfg.StreamReasoningFunc)
	s.localLLMCalls++
	if response != nil {
		s.lastPromptTokens = response.Usage.PromptTokens
		s.lastCompletionTokens = response.Usage.CompletionTokens
		s.lastMsgCountAtLLMCall = len(s.messages)
		s.hadLLMCall = true
		s.localInputTokens += int(response.Usage.PromptTokens)
		s.localOutputTokens += int(response.Usage.CompletionTokens)
		s.localCachedTokens += int(response.Usage.CacheHitTokens)
	}
	return response, err
}

// handleLLMError handles errors from LLM calls. Returns a RunOutput if the
// error should terminate the loop, nil if no error.
// partialResp may contain content accumulated before the stream error.
func (s *runState) handleLLMError(ctx context.Context, err error, partialResp *llm.LLMResponse, iteration int) *RunOutput {
	if err == nil {
		return nil
	}
	if ctx.Err() == nil && !llm.IsInputTooLongError(err) {
		GlobalMetrics.TotalLLMErrors.Add(1)
	}
	if ctx.Err() != nil {
		return s.buildOutput(&bus.OutboundMessage{
			Channel:   s.cfg.Channel,
			ChatID:    s.cfg.ChatID,
			Content:   "Agent was cancelled.",
			Error:     ctx.Err(),
			ToolsUsed: s.toolsUsed,
		})
	}
	// Use partial response content if available (stream error with partial output),
	// otherwise fall back to lastContent from previous successful iteration.
	partialContent := ""
	if partialResp != nil {
		partialContent = llm.StripThinkBlocks(partialResp.Content)
	}
	if partialContent == "" {
		partialContent = s.lastContent
	}
	if partialContent != "" {
		log.Ctx(ctx).WithFields(log.Fields{
			"agent_id":  s.cfg.AgentID,
			"iteration": iteration + 1,
		}).Warnf("LLM failed, returning partial result: %v", err)
		return s.buildOutput(&bus.OutboundMessage{
			Channel:   s.cfg.Channel,
			ChatID:    s.cfg.ChatID,
			Content:   partialContent + "\n\n> ⚠️ LLM 调用失败 (" + summarizeRetryError(err) + ")，以上为部分结果。",
			ToolsUsed: s.toolsUsed,
		})
	}
	userErrMsg := fmt.Sprintf("❌ LLM 服务调用失败 (%s)，请稍后重试。", summarizeRetryError(err))
	return s.buildOutput(&bus.OutboundMessage{
		Channel:   s.cfg.Channel,
		ChatID:    s.cfg.ChatID,
		Content:   userErrMsg,
		Error:     fmt.Errorf("%w: %w", ErrLLMGenerate, err),
		ToolsUsed: s.toolsUsed,
	})
}

// handleFinalResponse processes LLM responses.
// Returns (output, retry): output is non-nil when Run should return it;
// retry is true when context was compressed and the loop should continue.
//
// Finish reason handling:
//   - "stop" + no tool_calls → final text response, return
//   - "tool_calls" (or HasToolCalls) → execute tools, continue loop
//   - "length" → model hit max_tokens, output is truncated. Auto-retry once
//     by appending partial content and asking model to continue. If model
//     still can't finish, return with truncation warning.
//   - "content_filter" → filtered by safety, return with warning
//   - "" (empty/unknown) → abnormal stream termination, warn and return
//   - "model_context_window_exceeded" → compress and retry
func (s *runState) handleFinalResponse(ctx context.Context, response *llm.LLMResponse) (output *RunOutput, retry bool) {
	cleanContent := llm.StripThinkBlocks(response.Content)

	if !response.HasToolCalls() {
		// context_window_exceeded: force compress and retry
		if response.FinishReason == llm.FinishReasonContextWindowExceeded {
			log.Ctx(ctx).WithFields(log.Fields{
				"msg_count":          len(s.messages),
				"last_prompt_tokens": s.lastPromptTokens,
				"finish_reason":      response.FinishReason,
			}).Warn("Model context window exceeded, forcing compression and retry")
			cm := s.cfg.ContextManager
			if cm != nil && !s.disableCompressRetry {
				s.disableCompressRetry = true
				if s.cfg.MemoryToolDefs != nil && s.cfg.MemoryToolExec != nil {
					cm.SetMemoryTools(s.cfg.MemoryToolDefs, s.cfg.MemoryToolExec)
				}
				result, compressErr := cm.Compress(ctx, s.messages, s.cfg.LLMClient, s.cfg.Model)
				if compressErr != nil {
					log.Ctx(ctx).WithError(compressErr).Warn("Forced compression failed after context_window_exceeded")
				} else {
					s.accumulateCompressUsage(result)
					s.messages = s.syncMessages(result.LLMView)
					newCount, _ := llm.CountMessagesTokens(result.LLMView, s.cfg.Model)
					s.lastPromptTokens = int64(newCount)
					s.lastCompletionTokens = 0
					s.lastMsgCountAtLLMCall = len(s.messages)
					if s.cfg.Session != nil {
						if clearErr := s.cfg.Session.Clear(); clearErr != nil {
							log.Ctx(ctx).WithError(clearErr).Warn("Failed to clear session for context_window_exceeded compression")
						} else {
							allOk := true
							for _, msg := range result.SessionView {
								if err := assertNoSystemPersist(msg); err != nil {
									continue
								}
								if err := s.cfg.Session.AddMessage(msg); err != nil {
									log.Ctx(ctx).WithError(err).Error("Partial write during context_window_exceeded compression")
									allOk = false
									break
								}
							}
							if allOk {
								s.lastPersistedCount = len(s.messages)
							} else {
								log.Ctx(ctx).Warn("Context window exceeded compression persistence failed, session may be inconsistent")
							}
						}
					}
					log.Ctx(ctx).Info("Forced compression completed after context_window_exceeded, retrying")
					return nil, true // retry loop iteration
				}
			}
			out := s.buildOutput(&bus.OutboundMessage{
				Channel:   s.cfg.Channel,
				ChatID:    s.cfg.ChatID,
				Content:   "⚠️ Context window exceeded. Use /new to start a new conversation.",
				ToolsUsed: s.toolsUsed,
			})
			out.ReasoningContent = response.ReasoningContent
			return out, false
		}

		// length: output truncated due to max_tokens limit
		output := cleanContent
		if response.FinishReason == llm.FinishReasonLength {
			output += "\n\n⚠️ Output was truncated (reached max output token limit). Use /set-llm max_output_tokens=<n> to increase."
		}
		// content_filter: model output was filtered by safety system
		if response.FinishReason == llm.FinishReasonContentFilter {
			log.Ctx(ctx).WithFields(log.Fields{
				"finish_reason": response.FinishReason,
				"content_len":   len(cleanContent),
			}).Warn("Model response filtered by content filter")
			if output == "" {
				output = "⚠️ Response was filtered by content safety system."
			} else {
				output += "\n\n⚠️ Response was partially filtered by content safety system."
			}
		}

		// Update ThinkingContent so PhaseDone progress carries the final reply.
		// recordAssistantMsg is not called for final text responses (handleFinalResponse
		// returns directly), so ThinkingContent must be set here for SubAgent
		// session viewers that synthesize assistant messages from PhaseDone payload.
		if s.structuredProgress != nil && cleanContent != "" {
			s.structuredProgress.ThinkingContent = cleanContent
		}

		out := s.buildOutput(&bus.OutboundMessage{
			Channel:     s.cfg.Channel,
			ChatID:      s.cfg.ChatID,
			Content:     output,
			ToolsUsed:   s.toolsUsed,
			WaitingUser: s.waitingUser,
		})
		out.ReasoningContent = response.ReasoningContent
		return out, false
	}
	return nil, false
}

// recordAssistantMsg records intermediate content and the assistant message.
func (s *runState) recordAssistantMsg(ctx context.Context, response *llm.LLMResponse) {
	cleanContent := llm.StripThinkBlocks(response.Content)

	if cleanContent != "" {
		s.lastContent = cleanContent
	}

	if s.autoNotify && cleanContent != "" {
		s.progressLines = append(s.progressLines, cleanContent)
	}
	if s.structuredProgress != nil && cleanContent != "" {
		s.structuredProgress.ThinkingContent = cleanContent
	}
	// Wire the model's reasoning chain (reasoning_content) to progress
	// so the CLI can display the thinking process to the user.
	if s.structuredProgress != nil && response.ReasoningContent != "" {
		s.structuredProgress.ReasoningContent = response.ReasoningContent
	}

	// Push progress so CLI can display reasoning immediately after LLM completes,
	// rather than waiting for the next notifyProgress call (e.g. executeToolCalls).
	if s.autoNotify {
		s.notifyProgress("")
	}

	assistantMsg := llm.ChatMessage{
		Role:             "assistant",
		Content:          strings.TrimRight(response.Content, " \t"),
		ReasoningContent: response.ReasoningContent,
		ToolCalls:        response.ToolCalls,
	}
	s.messages = s.syncMessages(append(s.messages, assistantMsg))
}

// maybeCompress checks if context compression or observation masking is needed.
func (s *runState) maybeCompress(ctx context.Context) {
	s.compressAttempts++
	cm := s.cfg.ContextManager
	if cm == nil || len(s.messages) <= 3 {
		return
	}

	maxTokens := 0
	if s.cfg.ContextManagerConfig != nil {
		maxTokens = s.cfg.ContextManagerConfig.MaxContextTokens
	}
	if maxTokens <= 0 {
		log.Ctx(ctx).WithFields(log.Fields{
			"last_prompt_tokens": s.lastPromptTokens,
			"msg_count":          len(s.messages),
		}).Info("maybeCompress skipped: maxTokens=0")
		return
	}

	// Reserve headroom for max_output_tokens: the API budget is shared
	// between prompt (input) and completion (output). If we don't subtract
	// maxOutputTokens, we risk exceeding the context window when the model
	// generates a long response.
	maxOutputTokens := s.cfg.MaxOutputTokens
	if maxOutputTokens <= 0 {
		maxOutputTokens = 8192 // defaultMaxOutputTokens
	}
	promptBudget := maxTokens - maxOutputTokens
	if promptBudget <= 0 {
		promptBudget = maxTokens / 2 // fallback: reserve half for output
	}

	// Token estimation strategy:
	// - API prompt_tokens (exact) covers messages[0..lastMsgCount] + tool defs
	// - API completion_tokens (exact) covers the assistant message content/reasoning/tool_calls
	// - Local estimation only for tool result messages appended after the last LLM call
	//
	// restoredFromDB path: lastPromptTokens is from the PREVIOUS Run's last API call.
	// We use it as-is — no local estimation fallback.
	totalTokens := int64(0)
	var tokenSource string
	if s.lastPromptTokens > 0 && s.lastMsgCountAtLLMCall > 0 {
		// In-Run path: we've had at least one LLM call in this Run.
		totalTokens = s.lastPromptTokens + s.lastCompletionTokens
		tokenSource = "api+completion"
		if len(s.messages) > s.lastMsgCountAtLLMCall+1 {
			toolMsgs := s.messages[s.lastMsgCountAtLLMCall+1:]
			deltaTokens, deltaErr := llm.CountMessagesTokens(toolMsgs, s.cfg.Model)
			if deltaErr != nil {
				log.Ctx(ctx).WithError(deltaErr).Warn("maybeCompress: failed to count tool msg tokens")
			} else {
				totalTokens += int64(deltaTokens)
			}
			tokenSource = "api+completion+tool_delta"
		}
	} else if s.lastPromptTokens > 0 {
		// Restored from previous Run (DB or in-memory) — use API prompt_tokens as baseline.
		// Do NOT add lastCompletionTokens: those are output tokens from the
		// previous Run, not part of the current context size.
		totalTokens = s.lastPromptTokens
		tokenSource = "restored"
	} else {
		// No API token data available. This should never happen in production:
		// - First Run: no need to compress (few messages)
		// - Subsequent Runs: API call always sets lastPromptTokens
		// - After restart: DB restoration sets lastPromptTokens via SaveTokenState
		if len(s.messages) > 3 {
			log.Ctx(ctx).Error("maybeCompress: no API token data available, skipping compress check")
		}
		totalTokens = 0
		tokenSource = "no_data"
	}

	needCompress := len(s.messages) > 3 && shouldCompact(int(totalTokens), promptBudget) && (s.lastCompressIter == 0 || s.compressAttempts-s.lastCompressIter >= 5)

	// Free snip layer (Claude Code style): before expensive API-based compression,
	// trim old tool result contents that are no longer needed. This is free — no
	// API call required, just replaces large tool result content with placeholders.
	// Triggered when context exceeds 65% of prompt budget but before the 75%
	// compression threshold. Only activates when maxOutputTokens is reasonable
	// (>100 tokens) to avoid interfering with test scenarios using extreme values.
	snipped := false
	if !needCompress && maxOutputTokens > 100 && totalTokens > int64(float64(promptBudget)*0.65) && len(s.messages) > 6 {
		snipped = s.snipOldToolResults(ctx)
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"total_tokens":       totalTokens,
		"max_context":        maxTokens,
		"max_output_tokens":  maxOutputTokens,
		"prompt_budget":      promptBudget,
		"threshold":          int(float64(promptBudget) * 0.75),
		"msg_count":          len(s.messages),
		"need":               needCompress,
		"snipped":            snipped,
		"base_prompt_tokens": s.lastPromptTokens,
		"completion_tokens":  s.lastCompletionTokens,
		"source":             tokenSource,
	}).Info("maybeCompress check")

	if needCompress {
		s.runCompression(ctx, cm, int(totalTokens), maxTokens)
		return
	}

	// Layer 2: Observation masking (lightweight, no LLM call)
	if s.cfg.MaskStore != nil {
		maskingThreshold := float64(maxTokens) * 0.6
		if float64(totalTokens) > maskingThreshold {
			keepGroups := calculateKeepGroups(int(totalTokens), maxTokens)
			masked, count, maskedEntries := MaskOldToolResults(s.messages, s.cfg.MaskStore, keepGroups)
			if count > 0 {
				s.messages = s.syncMessages(masked)
				GlobalMetrics.MaskingEvents.Add(1)
				GlobalMetrics.MaskedItems.Add(int64(count))

				if s.cfg.Session != nil {
					persistedMasked := 0
					for _, entry := range maskedEntries {
						if entry.MessageIndex < s.lastPersistedCount {
							if err := s.cfg.Session.UpdateMessageContent(entry.MessageIndex, entry.Content); err != nil {
								log.Ctx(ctx).WithError(err).WithField("idx", entry.MessageIndex).Warn("Failed to persist masked message to session")
							} else {
								persistedMasked++
							}
						}
					}
					if persistedMasked > 0 {
						log.Ctx(ctx).WithField("persisted_masked", persistedMasked).Info("Persisted masked messages to session")
					}
				}

				if s.autoNotify {
					s.progressLines = append(s.progressLines, fmt.Sprintf("> 🎭 上下文较大 (%d tokens)，已遮蔽 %d 条旧工具结果", totalTokens, count))
					s.notifyProgress("")
				}
				log.Ctx(ctx).WithField("masked_count", count).Info("Observation masking triggered")
			}
		}
	}
}

// runCompression performs the actual context compression.
// snipOldToolResults replaces large tool result contents from earlier iterations
// with compact placeholders. This is a FREE context reduction layer — no API call
// needed. Inspired by Claude Code's "Snip" layer.
//
// Strategy: tool results from iterations before the last 3 are replaced with
// "[Tool result cleared to save context]" if they exceed 500 chars. This preserves
// the message structure (tool_use/tool_result pairing) while freeing tokens.
func (s *runState) snipOldToolResults(ctx context.Context) bool {
	const (
		minIterationsBeforeSnip = 3
		maxContentLen           = 500
		placeholder             = "[Old tool result cleared to save context]"
	)

	snipped := false
	for i := range s.messages {
		msg := &s.messages[i]
		if msg.Role != "tool" || len(msg.Content) <= maxContentLen {
			continue
		}
		// Check if this tool result is from an old iteration by finding the
		// corresponding tool_use (assistant message with matching ToolCallID).
		// Simple heuristic: if message index is far from the end, it's old.
		distanceFromEnd := len(s.messages) - i
		if distanceFromEnd < minIterationsBeforeSnip*3 { // ~3 messages per iteration
			continue
		}
		oldLen := len(msg.Content)
		msg.Content = placeholder
		snipped = true
		log.Ctx(ctx).WithFields(log.Fields{
			"msg_index":    i,
			"old_content":  oldLen,
			"distance_end": distanceFromEnd,
		}).Debug("Snipped old tool result")
	}
	if snipped {
		log.Ctx(ctx).Debug("Snipped old tool results to reduce context")
	}
	return snipped
}

func (s *runState) runCompression(ctx context.Context, cm ContextManager, totalTokens, maxTokens int) {
	if s.structuredProgress != nil {
		s.structuredProgress.Phase = PhaseCompressing
	}
	if s.autoNotify {
		s.progressLines = append(s.progressLines, fmt.Sprintf("> 📦 上下文过大 (%d tokens)，正在压缩 + 记忆整理...", totalTokens))
		s.notifyProgress("")
	}

	log.Ctx(ctx).Info("Auto context compaction triggered")

	if s.cfg.MemoryToolDefs != nil && s.cfg.MemoryToolExec != nil {
		cm.SetMemoryTools(s.cfg.MemoryToolDefs, s.cfg.MemoryToolExec)
	}

	result, compressErr := cm.Compress(ctx, s.messages, s.cfg.LLMClient, s.cfg.Model)
	if compressErr != nil {
		log.Ctx(ctx).WithError(compressErr).Warn("Auto context compaction failed")
		if s.structuredProgress != nil {
			s.structuredProgress.Phase = PhaseThinking
		}
		return
	}
	s.accumulateCompressUsage(result)

	oldTokenCount := totalTokens
	s.messages = s.syncMessages(result.LLMView)
	// Use the estimated token count of the compressed messages so that
	// subsequent maybeCompress calls (in this Run and the next) don't fall
	// back to the even-more-overestimated local counting path.
	newTokenCount, _ := llm.CountMessagesTokens(result.LLMView, s.cfg.Model)
	s.lastPromptTokens = int64(newTokenCount)
	s.lastCompletionTokens = 0
	s.lastMsgCountAtLLMCall = len(s.messages)
	s.lastCompressIter = s.compressAttempts
	if s.structuredProgress != nil {
		s.structuredProgress.Phase = PhaseThinking
		s.structuredProgress.HistoryCompacted = true
	}
	if s.autoNotify {
		for i := len(s.progressLines) - 1; i >= 0; i-- {
			if strings.Contains(s.progressLines[i], "正在压缩") {
				s.progressLines[i] = fmt.Sprintf("> ✅ 压缩完成: %d → %d tokens", oldTokenCount, newTokenCount)
				break
			}
		}
		s.notifyProgress("")
	}

	log.Ctx(ctx).WithFields(log.Fields{
		"new_tokens": newTokenCount,
	}).Info("Auto context compaction completed")

	GlobalMetrics.CompressEvents.Add(1)
	GlobalMetrics.CompressTokensIn.Add(int64(oldTokenCount))
	GlobalMetrics.CompressTokensOut.Add(int64(newTokenCount))

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
	if s.cfg.Session != nil {
		if err := s.cfg.Session.Clear(); err != nil {
			log.Ctx(ctx).WithError(err).Warn("Failed to clear session for auto compaction, skipping persistence")
		} else {
			allOk := true
			for _, msg := range result.SessionView {
				if err := assertNoSystemPersist(msg); err != nil {
					continue
				}
				if err := s.cfg.Session.AddMessage(msg); err != nil {
					log.Ctx(ctx).WithError(err).Error("Partial write during auto compaction, session may be corrupted")
					allOk = false
					break
				}
			}
			if allOk {
				log.Ctx(ctx).Info("Auto compaction persisted to session")
				if hook := cm.SessionHook(); hook != nil {
					hook.AfterPersist(ctx, s.cfg.Session, result)
				}
				// Update the persistence watermark to match the new (compressed) message
				// slice length. Without this, postToolProcessing's incremental persist
				// check (len(s.messages) > s.lastPersistedCount) will never be true again
				// because s.messages shrank but lastPersistedCount still points to the old
				// (larger) index, causing all subsequent messages to be lost on restart.
				s.lastPersistedCount = len(s.messages)
			} else {
				log.Ctx(ctx).Warn("Auto compaction persistence failed, using in-memory result only")
			}
		}
	}

	// Clean offload and mask entries that were compressed away.
	compressCutoff := time.Now()
	if s.cfg.OffloadStore != nil {
		s.cfg.OffloadStore.CleanOldEntries(s.offloadSessionKey, compressCutoff)
	}
	if s.cfg.MaskStore != nil {
		s.cfg.MaskStore.CleanOldEntries(compressCutoff)
	}
}

// executeToolCalls runs all tool calls from the LLM response.
func (s *runState) executeToolCalls(ctx context.Context, response *llm.LLMResponse, iteration int) []toolExecResult {
	// Add progress placeholders for all tool calls
	progressStartIdx := len(s.progressLines)
	for _, tc := range response.ToolCalls {
		s.toolsUsed = append(s.toolsUsed, tc.Name)
		s.localToolCalls++
		toolLabel := formatToolProgress(tc.Name, tc.Arguments)
		if s.autoNotify {
			s.progressLines = append(s.progressLines, fmt.Sprintf("> ⏳ %s ...", toolLabel))
		}
	}
	execResults := make([]toolExecResult, len(response.ToolCalls))
	if s.structuredProgress != nil {
		s.structuredProgress.Phase = PhaseToolExec
		s.structuredProgress.ActiveTools = make([]ToolProgress, len(response.ToolCalls))
		// NOTE: do NOT clear CompletedTools here — they represent tools that finished
		// earlier in this iteration (e.g. first batch of WebSearch done, second batch started).
		// Clearing them causes completed tools to flicker/disappear in the CLI progress panel.
		for j, tc := range response.ToolCalls {
			s.structuredProgress.ActiveTools[j] = ToolProgress{
				Name:      tc.Name,
				Label:     formatToolProgress(tc.Name, tc.Arguments),
				Status:    ToolPending,
				Iteration: iteration,
			}
		}
	}
	if s.autoNotify {
		s.notifyProgress("")
	}

	// execOne executes a single tool call and records the result.
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

		var execCtx context.Context
		var cancel context.CancelFunc
		if tc.Name == "SubAgent" {
			execCtx, cancel = ctx, func() {}
			if s.autoNotify {
				pi := progressStartIdx + entry.index
				if pi < len(s.progressLines) {
					execCtx = WithSubAgentProgress(execCtx, func(detail SubAgentProgressDetail) {
						s.progressMu.Lock()
						s.progressLines[pi] = formatSubAgentProgress(detail)
						s.progressMu.Unlock()
						s.notifyProgress("")
					})
				}
			}
		} else {
			execCtx, cancel = ctx, func() {}
		}

		start := time.Now()
		if s.structuredProgress != nil && entry.index < len(s.structuredProgress.ActiveTools) {
			s.structuredProgress.ActiveTools[entry.index].Status = ToolRunning
		}
		// Notify CLI immediately so the running animation is visible
		// before the tool blocks on execution.
		if s.autoNotify {
			s.notifyProgress("")
		}
		result, execErr := s.toolExecutor(execCtx, tc)
		elapsed := time.Since(start)
		cancel()

		execResults[entry.index] = toolExecResult{err: execErr, result: result, elapsed: elapsed}
		if s.structuredProgress != nil && entry.index < len(s.structuredProgress.ActiveTools) {
			status := ToolDone
			if execErr != nil || (result != nil && result.IsError) {
				status = ToolError
			}
			s.structuredProgress.ActiveTools[entry.index].Status = status
			s.structuredProgress.ActiveTools[entry.index].Elapsed = elapsed
			if result != nil && result.Summary != "" {
				su := strings.TrimSpace(result.Summary)
				if idx := strings.Index(su, "\n"); idx >= 0 {
					su = su[:idx]
				}
				if r := []rune(su); len(r) > 100 {
					su = string(r[:100]) + "..."
				}
				s.structuredProgress.ActiveTools[entry.index].Summary = su
			} else if execErr != nil {
				su := execErr.Error()
				if r := []rune(su); len(r) > 100 {
					su = string(r[:100]) + "..."
				}
				s.structuredProgress.ActiveTools[entry.index].Summary = su
			}
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

			if s.autoNotify {
				if tc.Name == "SubAgent" {
					line := s.progressLines[progressStartIdx+entry.index]
					line = strings.ReplaceAll(line, "⏳", "❌")
					line = strings.ReplaceAll(line, "🔄", "❌")
					s.progressLines[progressStartIdx+entry.index] = line
				} else {
					s.progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> ❌ %s (%s)", toolLabel, elapsed.Round(time.Millisecond))
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

			if s.autoNotify {
				if tc.Name == "SubAgent" {
					line := s.progressLines[progressStartIdx+entry.index]
					// Replace both possible prefixes: ⏳ (initial placeholder) and 🔄 (progress-updated)
					line = strings.ReplaceAll(line, "⏳", "✅")
					line = strings.ReplaceAll(line, "🔄", "✅")
					s.progressLines[progressStartIdx+entry.index] = line
				} else {
					icon := "✅"
					if result.IsError {
						icon = "❌"
					}
					s.progressLines[progressStartIdx+entry.index] = fmt.Sprintf("> %s %s (%s)", icon, toolLabel, elapsed.Round(time.Millisecond))
				}
			}
		}
	}

	// executeSubAgentOps runs SubAgent tool calls concurrently.
	executeSubAgentOps := func(ops []toolCallEntry, execFn func(toolCallEntry), subAgentSem func(context.Context) func(), doAutoNotify bool) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, entry := range ops {
			wg.Add(1)
			go func(e toolCallEntry) {
				defer clipanic.Recover("agent.executeToolCalls.subAgentOp", e.tc, false)
				defer wg.Done()
				var release func()
				if subAgentSem != nil {
					release = subAgentSem(ctx)
					defer release()
				}
				execFn(e)
				if doAutoNotify && !s.batchProgressByIteration {
					mu.Lock()
					s.notifyProgress("")
					mu.Unlock()
				}
			}(entry)
		}
		wg.Wait()
	}

	// Dispatch tool calls based on execution mode
	if s.cfg.EnableReadWriteSplit {
		var readOps, writeOps, subAgentOps []toolCallEntry
		for idx, tc := range response.ToolCalls {
			entry := toolCallEntry{iteration: iteration, index: idx, tc: tc}
			if tc.Name == "SubAgent" && s.cfg.EnableConcurrentSubAgents {
				subAgentOps = append(subAgentOps, entry)
			} else if readOnlyTools[tc.Name] {
				readOps = append(readOps, entry)
			} else {
				writeOps = append(writeOps, entry)
			}
		}

		if len(subAgentOps) > 0 {
			executeSubAgentOps(subAgentOps, execOne, s.cfg.SubAgentSem, s.autoNotify)
		}
		if len(readOps) > 0 {
			const maxParallel = 8
			sem := make(chan struct{}, maxParallel)
			var wg sync.WaitGroup
			for _, entry := range readOps {
				wg.Add(1)
				sem <- struct{}{}
				go func(e toolCallEntry) {
					defer clipanic.Recover("agent.executeToolCalls.readOp", e.tc, false)
					defer wg.Done()
					defer func() { <-sem }()
					execOne(e)
				}(entry)
			}
			wg.Wait()
			if s.autoNotify && !s.batchProgressByIteration {
				s.notifyProgress("")
			}
		}
		for _, entry := range writeOps {
			execOne(entry)
			if s.autoNotify && !s.batchProgressByIteration {
				s.notifyProgress("")
			}
		}
	} else if s.cfg.EnableConcurrentSubAgents {
		var subAgentOps, otherOps []toolCallEntry
		for idx, tc := range response.ToolCalls {
			entry := toolCallEntry{iteration: iteration, index: idx, tc: tc}
			if tc.Name == "SubAgent" {
				subAgentOps = append(subAgentOps, entry)
			} else {
				otherOps = append(otherOps, entry)
			}
		}

		if len(subAgentOps) > 0 {
			executeSubAgentOps(subAgentOps, execOne, s.cfg.SubAgentSem, s.autoNotify)
		}
		for _, entry := range otherOps {
			execOne(entry)
			if s.autoNotify && !s.batchProgressByIteration {
				s.notifyProgress("")
			}
		}
	} else {
		for idx, tc := range response.ToolCalls {
			execOne(toolCallEntry{iteration: iteration, index: idx, tc: tc})
			if s.autoNotify && !s.batchProgressByIteration {
				s.notifyProgress("")
			}
		}
	}

	// Update structured progress: all tools completed
	if s.structuredProgress != nil {
		s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, s.structuredProgress.ActiveTools...)
		s.structuredProgress.ActiveTools = nil
	}
	if s.autoNotify && !s.batchProgressByIteration && s.structuredProgress != nil {
		s.notifyProgress("")
	}
	// Snapshot completed iteration
	if s.structuredProgress != nil && len(s.structuredProgress.CompletedTools) > 0 {
		snap := IterationSnapshot{
			Iteration: iteration,
			Thinking:  s.structuredProgress.ThinkingContent,
			Reasoning: s.structuredProgress.ReasoningContent,
			Tools:     make([]IterationToolSnapshot, len(s.structuredProgress.CompletedTools)),
		}
		for j, t := range s.structuredProgress.CompletedTools {
			snap.Tools[j] = IterationToolSnapshot{
				Name:      t.Name,
				Label:     t.Label,
				Status:    string(t.Status),
				ElapsedMS: t.Elapsed.Milliseconds(),
				Summary:   t.Summary,
			}
		}
		s.iterationSnapshots = append(s.iterationSnapshots, snap)
		if s.cfg.OnIterationSnapshot != nil {
			s.cfg.OnIterationSnapshot(snap)
		}
	}
	if s.autoNotify && s.batchProgressByIteration {
		s.notifyProgress("")
	}

	return execResults
}

// processToolResults handles offload, OAuth, waiting user, and stale invalidation
// for tool execution results.
func (s *runState) processToolResults(ctx context.Context, response *llm.LLMResponse, execResults []toolExecResult) {
	// Count recall tool calls for metrics
	for idx2, tc := range response.ToolCalls {
		r := execResults[idx2]
		if r.err != nil {
			continue
		}
		switch tc.Name {
		case "offload_recall":
			var args struct {
				ID string `json:"id"`
			}
			if json.Unmarshal([]byte(tc.Arguments), &args) == nil && args.ID != "" {
				GlobalMetrics.RecordOffloadRecall(args.ID)
			} else {
				GlobalMetrics.OffloadedRecalls.Add(1)
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

	// Process results in original order
	for idx, tc := range response.ToolCalls {
		r := execResults[idx]
		content := r.llmContent

		// Layer 1 Offload
		skipOffload := tc.Name == "offload_recall"
		if tc.Name == "Read" && readArgsHasOffsetOrLimit(tc.Arguments) {
			skipOffload = true
		}
		if s.cfg.OffloadStore != nil && r.err == nil && !skipOffload {
			offloadContent := content
			if r.result != nil && r.result.Summary != "" {
				offloadContent = r.result.Summary
			}
			offloaded, wasOffloaded := s.cfg.OffloadStore.MaybeOffload(ctx, s.offloadSessionKey, tc.Name, tc.Arguments, offloadContent, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID)
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

		// OAuth auto-trigger
		if r.err != nil && s.cfg.OAuthHandler != nil {
			if oauthContent, handled := s.cfg.OAuthHandler(ctx, tc, r.err); handled {
				content = oauthContent
				s.autoNotify = false
				if r.result != nil && r.result.WaitingUser {
					s.waitingUser = true
					if s.waitingQuestion == "" && r.result.Summary != "" {
						s.waitingQuestion = r.result.Summary
					}
					if len(r.result.Metadata) > 0 && s.waitingMetadata == nil {
						s.waitingMetadata = make(map[string]string)
						for k, v := range r.result.Metadata {
							s.waitingMetadata[k] = v
						}
					}
				}
			}
		}

		// Check sessionFinalSent
		if s.cfg.SessionFinalSentCallback != nil && s.cfg.SessionFinalSentCallback() {
			s.autoNotify = false
			s.progressLines = nil
		}

		if r.result != nil && r.result.WaitingUser {
			s.waitingUser = true
			if s.waitingQuestion == "" && r.result.Summary != "" {
				s.waitingQuestion = r.result.Summary
			}
			if len(r.result.Metadata) > 0 && s.waitingMetadata == nil {
				s.waitingMetadata = make(map[string]string)
				for k, v := range r.result.Metadata {
					s.waitingMetadata[k] = v
				}
			}
		}

		toolMsg := llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments, content)
		if r.result != nil && r.result.Detail != "" {
			toolMsg.Detail = r.result.Detail
		}
		s.messages = s.syncMessages(append(s.messages, toolMsg))
	}

	// Invalidate stale Read offloads after any tool execution
	if s.cfg.OffloadStore != nil {
		staleIDs := s.cfg.OffloadStore.InvalidateStaleReads(ctx, s.offloadSessionKey, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID)
		if len(staleIDs) > 0 {
			log.Ctx(ctx).WithFields(log.Fields{
				"stale_count": len(staleIDs),
				"stale_ids":   staleIDs,
			}).Info("Stale offloads detected and invalidated")
			s.messages = s.syncMessages(s.cfg.OffloadStore.PurgeStaleMessages(s.offloadSessionKey, s.messages))
		}
	}
}

// postToolProcessing handles dynamic context injection, system reminder,
// session persistence, background task draining, and waiting user check.
// Returns a RunOutput if the loop should terminate, nil otherwise.
func (s *runState) postToolProcessing(ctx context.Context, response *llm.LLMResponse, iteration int) *RunOutput {
	// --- Dynamic Context injection (CWD change detection) ---
	s.dynamicInjector.InjectIfNeeded(s.messages)

	// --- System Reminder injection ---
	if len(response.ToolCalls) > 0 {
		// Strip previous reminder from earlier messages to avoid accumulation
		for idx := len(s.messages) - 2; idx >= 0; idx-- {
			if strings.Contains(s.messages[idx].Content, "<system-reminder>") {
				s.messages[idx].Content = stripSystemReminder(s.messages[idx].Content)
			} else {
				break
			}
		}

		var todoSummary string
		if s.cfg.TodoManager != nil && s.sessionKey != "" {
			todoSummary = s.cfg.TodoManager.GetTodoSummary(s.sessionKey)
		}

		// Get current CWD for system reminder
		var cwd string
		if s.cfg.Session != nil {
			cwd = s.cfg.Session.GetCurrentDir()
		}

		reminder := BuildSystemReminder(s.messages, response.ToolCalls, todoSummary, s.cfg.AgentID, cwd)
		if reminder != "" && len(s.messages) > 0 {
			lastIdx := len(s.messages) - 1
			s.messages[lastIdx].Content += "\n\n" + reminder
		}
	}

	// --- Incremental session persistence ---
	if s.cfg.Session != nil && len(s.messages) > s.lastPersistedCount {
		persistOk := true
		for _, msg := range s.messages[s.lastPersistedCount:] {
			if msg.Role == "system" {
				continue
			}
			persistMsg := msg
			if strings.Contains(persistMsg.Content, "<system-reminder>") {
				persistMsg.Content = stripSystemReminder(persistMsg.Content)
			}
			if err := s.cfg.Session.AddMessage(persistMsg); err != nil {
				log.Ctx(ctx).WithError(err).Error("Failed to persist message to session")
				persistOk = false
				break
			}
		}
		if persistOk {
			s.lastPersistedCount = len(s.messages)
		}
	}

	// --- Background notification draining (bg tasks + bg subagents) ---
	if s.cfg.DrainBgNotifications != nil {
		pending := s.cfg.DrainBgNotifications()
		for _, notif := range pending {
			switch n := notif.(type) {
			case *tools.BackgroundTask:
				s.injectBgTaskNotification(ctx, iteration, n)
			case *tools.SubAgentBgNotify:
				s.injectSubAgentBgNotification(ctx, iteration, n)
			}
		}
	}

	// Check if any tool marked as waiting for user response
	if s.waitingUser {
		log.Ctx(ctx).Info("Tool is waiting for user response, ending loop without additional reply")
		outMsg := &bus.OutboundMessage{
			Channel:     s.cfg.Channel,
			ChatID:      s.cfg.ChatID,
			ToolsUsed:   s.toolsUsed,
			WaitingUser: true,
		}
		if s.waitingQuestion != "" || len(s.waitingMetadata) > 0 || s.cfg.SenderID != "" {
			outMsg.Metadata = make(map[string]string)
			if s.cfg.SenderID != "" {
				outMsg.Metadata["sender_id"] = s.cfg.SenderID
			}
			if s.waitingQuestion != "" {
				outMsg.Metadata["ask_question"] = s.waitingQuestion
			}
			for k, v := range s.waitingMetadata {
				outMsg.Metadata[k] = v
			}
		}
		return s.buildOutput(outMsg)
	}

	return nil
}

// injectBgTaskNotification injects a bg task completion as a synthetic tool call/result pair.
func (s *runState) injectBgTaskNotification(ctx context.Context, iteration int, bgTask *tools.BackgroundTask) {
	bgContent := tools.FormatBgTaskCompletion(bgTask)
	bgAssistantMsg := llm.ChatMessage{
		Role:    "assistant",
		Content: "A background task has completed. Let me check the result.",
		ToolCalls: []llm.ToolCall{{
			ID:   "bg_" + bgTask.ID,
			Name: "background_task_result",
		}},
	}
	if s.cfg.OffloadStore != nil {
		if offloaded, ok := s.cfg.OffloadStore.MaybeOffload(ctx, s.offloadSessionKey, "background_task_result", "", bgContent, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID); ok {
			bgContent = offloaded.Summary
			GlobalMetrics.OffloadEvents.Add(1)
			GlobalMetrics.OffloadedItems.Add(1)
		}
	}
	bgToolMsg := llm.NewToolMessage("background_task_result", "bg_"+bgTask.ID, "", bgContent)
	s.messages = s.syncMessages(append(s.messages, bgAssistantMsg, bgToolMsg))
	log.Ctx(ctx).WithField("task_id", bgTask.ID).Info("Injected bg task completion into Run loop")

	if s.cfg.Session != nil {
		_ = s.cfg.Session.AddMessage(bgAssistantMsg)
		_ = s.cfg.Session.AddMessage(bgToolMsg)
		s.lastPersistedCount = len(s.messages)
	}

	if s.structuredProgress != nil {
		var elapsed time.Duration
		if bgTask.FinishedAt != nil {
			elapsed = bgTask.FinishedAt.Sub(bgTask.StartedAt)
		}
		s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, ToolProgress{
			Name:      "background_task_result",
			Label:     fmt.Sprintf("bg:%s", bgTask.ID),
			Status:    ToolDone,
			Elapsed:   elapsed,
			Iteration: iteration,
		})
		if s.autoNotify {
			s.notifyProgress("")
		}
	}
}

// injectSubAgentBgNotification injects a bg subagent notification as a synthetic tool call/result pair.
// Progress notifications are dropped entirely — they would pollute the parent's TUI and waste LLM tokens.
// Only completed notifications are injected (as tool messages) and shown in the TUI progress block.
func (s *runState) injectSubAgentBgNotification(ctx context.Context, iteration int, n *tools.SubAgentBgNotify) {
	// Drop progress notifications — only completion matters for the parent agent
	if n.Type == tools.SubAgentBgNotifyProgress {
		log.Ctx(ctx).WithFields(log.Fields{
			"role":     n.Role,
			"instance": n.Instance,
		}).Debug("Dropping bg subagent progress notification in Run loop")
		return
	}
	bgContent := tools.FormatSubAgentBgNotify(n)
	toolName := "bg_subagent_" + string(n.Type)
	toolID := fmt.Sprintf("bgsub_%s_%s", n.Role, n.Instance)
	assistantMsg := llm.ChatMessage{
		Role:    "assistant",
		Content: fmt.Sprintf("Background subagent %s has a %s update.", n.Role, n.Type),
		ToolCalls: []llm.ToolCall{{
			ID:   toolID,
			Name: toolName,
		}},
	}
	if s.cfg.OffloadStore != nil {
		if offloaded, ok := s.cfg.OffloadStore.MaybeOffload(ctx, s.offloadSessionKey, toolName, "", bgContent, s.cfg.WorkspaceRoot, "", s.cfg.OriginUserID); ok {
			bgContent = offloaded.Summary
			GlobalMetrics.OffloadEvents.Add(1)
			GlobalMetrics.OffloadedItems.Add(1)
		}
	}
	toolMsg := llm.NewToolMessage(toolName, toolID, "", bgContent)
	s.messages = s.syncMessages(append(s.messages, assistantMsg, toolMsg))
	log.Ctx(ctx).WithFields(log.Fields{
		"role":     n.Role,
		"instance": n.Instance,
		"type":     n.Type,
	}).Info("Injected bg subagent notification into Run loop")

	if s.cfg.Session != nil {
		_ = s.cfg.Session.AddMessage(assistantMsg)
		_ = s.cfg.Session.AddMessage(toolMsg)
		s.lastPersistedCount = len(s.messages)
	}

	// Show completion in TUI progress block
	if s.structuredProgress != nil {
		s.structuredProgress.CompletedTools = append(s.structuredProgress.CompletedTools, ToolProgress{
			Name:      toolName,
			Label:     fmt.Sprintf("bgsub:%s/%s", n.Role, n.Instance),
			Status:    ToolDone,
			Iteration: iteration,
		})
		if s.autoNotify {
			s.notifyProgress("")
		}
	}
}

// buildMaxIterOutput creates the output for when max iterations is reached.
func (s *runState) buildMaxIterOutput() *RunOutput {
	return s.buildOutput(&bus.OutboundMessage{
		Channel:   s.cfg.Channel,
		ChatID:    s.cfg.ChatID,
		Content:   "已达到最大迭代次数，请重新描述你的需求。",
		ToolsUsed: s.toolsUsed,
	})
}
