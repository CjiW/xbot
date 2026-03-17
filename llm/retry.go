package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	retry "github.com/avast/retry-go/v5"
	logrus "xbot/logger"
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
	Attempts uint          // 最大尝试次数（含首次），默认 3
	Delay    time.Duration // 初始延迟，默认 500ms
	MaxDelay time.Duration // 最大延迟，默认 10s
}

// DefaultRetryConfig 返回默认重试配置
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		Attempts: 3,
		Delay:    500 * time.Millisecond,
		MaxDelay: 10 * time.Second,
	}
}

// RetryLLM 为任意 LLM 实现提供重试能力的装饰器
type RetryLLM struct {
	inner  LLM
	config RetryConfig
}

// NewRetryLLM 创建重试包装器；inner 可选实现 StreamingLLM
func NewRetryLLM(inner LLM, cfg RetryConfig) *RetryLLM {
	return &RetryLLM{inner: inner, config: cfg}
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
	if !strings.Contains(msg, "400") {
		return false
	}
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
	return false
}

// isRetryableError 判断错误是否可重试
// 可重试：429、5xx、网络错误
// 不可重试：context 取消/超时、其他 4xx
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	// context 错误不重试
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := err.Error()
	// 网络层错误可重试
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// OpenAI SDK 错误格式: `POST "URL": NNN StatusText ...`
	// CodeBuddy 错误格式: `CodeBuddy API error: status=NNN, body=...`
	for _, code := range []string{"429", "500", "502", "503", "504"} {
		if strings.Contains(msg, ": "+code+" ") || // OpenAI
			strings.Contains(msg, "status="+code) { // CodeBuddy
			return true
		}
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
		retry.Context(ctx),
		retry.RetryIf(isRetryableError),
		retry.OnRetry(func(n uint, err error) {
			logrus.Ctx(ctx).WithFields(logrus.Fields{
				"attempt": n + 1,
				"max":     r.config.Attempts,
				"error":   err.Error(),
			}).Warn("[LLM] " + label)

			// 通知调用方（如 agent runLoop）以便向用户推送进度
			if notify := getRetryNotify(ctx); notify != nil {
				notify(n+1, r.config.Attempts, err)
			}
		}),
	}
}

// Generate 生成 LLM 响应，失败时按配置重试
func (r *RetryLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	return retry.NewWithData[*LLMResponse](
		r.retryOptions(ctx, "Retrying request")...,
	).Do(func() (*LLMResponse, error) {
		return r.inner.Generate(ctx, model, messages, tools, thinkingMode)
	})
}

// ListModels 获取可用模型列表（直接转发，不重试）
func (r *RetryLLM) ListModels() []string {
	return r.inner.ListModels()
}

// GenerateStream 仅在获取 channel 时重试，流开始后不重试
func (r *RetryLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
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
