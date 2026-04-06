package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	retry "github.com/avast/retry-go/v5"
	log "xbot/logger"
)

// RetryNotifyFunc 重试通知回调。
// attempt: 当前重试次数（从 1 开始），maxAttempts: 最大尝试次数，err: 触发重试的错误。
type RetryNotifyFunc func(attempt, maxAttempts uint, err error)

type retryNotifyKey struct{}

// WithRetryNotify 将重试通知回调注入 context。
// RetryLLM 在每次重试时会调用该回调，调用方可借此向用户推送进度。
func WithRetryNotify(ctx context.Context, fn RetryNotifyFunc) context.Context {
	return context.WithValue(ctx, retryNotifyKey{}, fn)
}

// getRetryNotify 从 context 获取通知回调（可能为 nil）。
func getRetryNotify(ctx context.Context) RetryNotifyFunc {
	fn, _ := ctx.Value(retryNotifyKey{}).(RetryNotifyFunc)
	return fn
}

// RetryConfig 重试配置
type RetryConfig struct {
	Attempts      uint          // 最大尝试次数（含首次），默认 5
	Delay         time.Duration // 初始延迟，默认 1s
	MaxDelay      time.Duration // 最大延迟，默认 30s
	MaxConcurrent int           // 最大并发数（0 表示不限制）
	Timeout       time.Duration // 单次 LLM 调用超时（0 = 不设超时）
}

// DefaultRetryConfig 返回默认重试配置
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		Attempts: 5,
		Delay:    1 * time.Second,
		MaxDelay: 30 * time.Second,
		Timeout:  120 * time.Second, // 单次 LLM 调用超时，确保每次重试有独立窗口
	}
}

// RetryLLM 为任意 LLM 实现提供重试能力的装饰器
type RetryLLM struct {
	inner  LLM
	config RetryConfig
	sem    chan struct{} // 并发信号量，nil 表示不限制
}

// NewRetryLLM 创建重试包装器；inner 可选实现 StreamingLLM
func NewRetryLLM(inner LLM, cfg RetryConfig) *RetryLLM {
	r := &RetryLLM{inner: inner, config: cfg}
	if cfg.MaxConcurrent > 0 {
		r.sem = make(chan struct{}, cfg.MaxConcurrent)
	}
	return r
}

// acquire 获取并发信号量，返回释放函数。
// 如果 sem 为 nil（不限制并发），返回空操作函数。
// 注意：返回的 release 函数仅在成功获取信号量后才执行释放，
// 如果 ctx 已取消，返回空操作函数避免死锁。
func (r *RetryLLM) acquire(ctx context.Context) func() {
	if r.sem == nil {
		return func() {}
	}
	select {
	case r.sem <- struct{}{}:
		return func() { <-r.sem }
	case <-ctx.Done():
		return func() {} // ctx 已取消，返回空操作避免死锁
	}
}

// IsInputTooLongError detects 400-class errors caused by the input exceeding the
// model's context window. Different providers return this in different formats:
//   - Dashscope: "Range of input length should be [1, 202752]"
//   - OpenAI:    "maximum context length" / "max_tokens"
//   - Anthropic: "prompt is too long"
func IsInputTooLongError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// 快速路径：检查是否包含 HTTP 400 状态码模式（排除纯数字误匹配）
	has400 := strings.Contains(msg, ": 400 ") ||
		strings.Contains(msg, "status=400") ||
		strings.Contains(msg, `status_code":400`) ||
		strings.Contains(msg, `"status":400`) ||
		strings.Contains(msg, "status code 400")
	// Input-too-long 指示关键词（足够精确，无需 400 前置）
	indicators := []string{
		"range of input length",
		"maximum context length",
		"max_tokens",
		"context_length_exceeded",
		"prompt is too long",
		"input too long",
		"token limit",
		"reduce the length",
		"too many tokens",
	}
	for _, ind := range indicators {
		if strings.Contains(msg, ind) {
			return true
		}
	}
	// 有 400 但无精确指示关键词，可能是其他 400 错误，不误判
	_ = has400
	return false
}

// isRetryableError 判断错误是否可重试。
// 可重试：429、5xx、网络错误、context 超时
// 不可重试：context 取消（用户主动 /cancel）、其他 4xx
//
// 注意：由于 retryOptions 不再传 retry.Context(ctx)，超时重试现在可以正常工作。
// 每次重试通过 perAttemptCtx 创建全新的超时上下文。
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// context.Canceled：用户主动取消（/cancel 等），不重试
	if errors.Is(err, context.Canceled) {
		return false
	}
	// context.DeadlineExceeded：超时是瞬态错误，允许重试
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	// 网络层错误可重试
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// OpenAI SDK 错误格式: `POST "URL": NNN StatusText ...`
	for _, code := range []string{"429", "500", "502", "503", "504"} {
		if strings.Contains(msg, ": "+code+" ") { // OpenAI
			return true
		}
	}
	// B-05 修复：Anthropic SDK 错误格式: `anthropic API error: status=NNN, body=...`
	// 原有 OpenAI 格式匹配无法匹配此格式，需单独处理
	if strings.Contains(msg, "anthropic API error: status=") {
		if idx := strings.Index(msg, "status="); idx != -1 {
			codeStr := msg[idx+7:]
			// 找到 status 值的结束位置（逗号、空格或字符串结尾）
			for i, c := range codeStr {
				if c == ',' || c == ' ' || c == ')' {
					codeStr = codeStr[:i]
					break
				}
			}
			// 429 和 5xx 可重试
			if codeStr == "429" || strings.HasPrefix(codeStr, "5") {
				return true
			}
		}
	}
	return false
}

// isRateLimitError 判断错误是否为 429 Rate Limit 错误
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// OpenAI SDK: `POST "URL": 429 Too Many Requests`
	if strings.Contains(msg, ": 429 ") {
		return true
	}
	// Anthropic SDK: `anthropic API error: status=429, body=...`
	if strings.Contains(msg, "status=429") {
		return true
	}
	return false
}

// retryOptions 构建通用重试选项
func (r *RetryLLM) retryOptions(ctx context.Context, label string) []retry.Option {
	return []retry.Option{
		retry.Attempts(r.config.Attempts),
		retry.Delay(r.config.Delay),
		retry.MaxDelay(r.config.MaxDelay),
		retry.DelayType(retry.CombineDelay(retry.BackOffDelay, retry.RandomDelay)),
		// 不传 retry.Context(ctx) —— 超时后 ctx 已取消会导致 retry 框架跳过重试
		// context.Canceled 由 isRetryableError 处理（返回 false → 不重试）
		retry.RetryIf(isRetryableError),
		retry.OnRetry(func(n uint, err error) {
			log.Ctx(ctx).WithFields(log.Fields{
				"attempt": n + 1,
				"max":     r.config.Attempts,
				"error":   err.Error(),
			}).Warn("[LLM] " + label)

			// 通知调用方（如 agent runLoop）以便向用户推送进度
			if notify := getRetryNotify(ctx); notify != nil {
				notify(n+1, r.config.Attempts, err)
			}

			// 429 额外指数退避：避免短时间内重复触发速率限制
			if isRateLimitError(err) {
				extraDelay := time.Duration(2<<min(n, 4)) * time.Second // 2s, 4s, 8s, 16s, 32s
				log.Ctx(ctx).WithField("delay", extraDelay).Warn("[LLM] Rate limited, backing off")
				select {
				case <-time.After(extraDelay):
				case <-ctx.Done():
				}
			}
		}),
	}
}

// perAttemptCtx 为每次重试创建全新的超时上下文。
// 如果调用方 ctx 携带 deadline（如 engine.go 的 context.WithTimeout），
// 提取超时 duration 创建新 ctx，而非复用同一个 deadline。
// 这样每次重试都有完整的超时窗口。
// 父 ctx 的取消信号仍然会被传播。
func (r *RetryLLM) perAttemptCtx(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := r.config.Timeout
	if timeout <= 0 {
		if deadline, ok := parent.Deadline(); ok {
			timeout = time.Until(deadline)
		}
	}
	if timeout <= 0 {
		return parent, func() {}
	}
	// 父 ctx 已取消则不启动
	select {
	case <-parent.Done():
		return parent, func() {}
	default:
	}
	// 创建全新超时上下文（不继承父 ctx 的 deadline）
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	// 传播父 ctx 的取消信号（但不是 deadline）
	go func() {
		select {
		case <-parent.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// Generate 生成 LLM 响应，失败时按配置重试
func (r *RetryLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	release := r.acquire(ctx)
	defer release()
	return retry.NewWithData[*LLMResponse](
		r.retryOptions(ctx, "Retrying request")...,
	).Do(func() (*LLMResponse, error) {
		attemptCtx, cancel := r.perAttemptCtx(ctx)
		defer cancel()
		return r.inner.Generate(attemptCtx, model, messages, tools, thinkingMode)
	})
}

// ListModels 获取可用模型列表（直接转发，不重试）
func (r *RetryLLM) ListModels() []string {
	return r.inner.ListModels()
}

// GenerateStream 仅在获取 channel 时重试，流开始后不重试。
// 注意：不使用 perAttemptCtx，因为 GenerateStream 是异步的（启动 goroutine 后立即返回），
// perAttemptCtx 的 defer cancel() 会在 goroutine 仍在运行时过早取消上下文，
// 导致 processStream 检测到 context canceled 并发送 EventError。
// 流的超时/取消由调用方（generateResponse → CollectStream）通过 ctx 管理。
func (r *RetryLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	release := r.acquire(ctx)
	defer release()
	streaming, ok := r.inner.(StreamingLLM)
	if !ok {
		return nil, fmt.Errorf("underlying LLM does not support streaming")
	}
	return retry.NewWithData[<-chan StreamEvent](
		r.retryOptions(ctx, "Retrying stream connection")...,
	).Do(func() (<-chan StreamEvent, error) {
		return streaming.GenerateStream(ctx, model, messages, tools, thinkingMode)
	})
}
