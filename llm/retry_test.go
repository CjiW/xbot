package llm

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// isRetryableError 测试
// ---------------------------------------------------------------------------

func TestIsRetryableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		// nil
		{"nil error", nil, false},

		// context 错误
		{"context.Canceled", context.Canceled, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, true}, // 超时允许重试
		{"wrapped context.Canceled", fmt.Errorf("call failed: %w", context.Canceled), false},
		{"wrapped context.DeadlineExceeded", fmt.Errorf("timeout: %w", context.DeadlineExceeded), true}, // 超时允许重试
		{"string context canceled", errors.New("something context canceled here"), false},
		{"string context deadline exceeded", errors.New("context deadline exceeded"), false}, // 纯字符串不匹配 sentinel

		// 网络错误 — 重试
		{"net.DNSError timeout", &net.DNSError{Err: "timeout", IsTimeout: true}, true},
		{"net.OpError", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},

		// HTTP 状态码 — OpenAI 格式: `POST "url": NNN StatusText`
		{"429 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 429 Too Many Requests`), true},
		{"500 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 500 Internal Server Error`), true},
		{"502 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 502 Bad Gateway`), true},
		{"503 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 503 Service Unavailable`), true},
		{"504 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 504 Gateway Timeout`), true},

		// 不可重试的 4xx
		{"400 OpenAI", errors.New(`POST "url": 400 Bad Request`), false},
		{"401 OpenAI", errors.New(`POST "url": 401 Unauthorized`), false},
		{"403 OpenAI", errors.New(`POST "url": 403 Forbidden`), false},
		{"404 OpenAI", errors.New(`POST "url": 404 Not Found`), false},

		// 普通错误 — 不重试
		{"generic error", errors.New("something went wrong"), false},
		{"EOF", errors.New("unexpected EOF"), false},

		// B-05 修复：Anthropic SDK 错误格式: `anthropic API error: status=NNN, body=...`
		{"429 Anthropic", errors.New("anthropic API error: status=429, body={\"type\":\"error\"}"), true},
		{"500 Anthropic", errors.New("anthropic API error: status=500, body=internal error"), true},
		{"502 Anthropic", errors.New("anthropic API error: status=502, body=bad gateway"), true},
		{"503 Anthropic", errors.New("anthropic API error: status=503, body=overloaded"), true},
		{"400 Anthropic", errors.New("anthropic API error: status=400, body=bad request"), false},
		{"401 Anthropic", errors.New("anthropic API error: status=401, body=unauthorized"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryableError(tt.err)
			if got != tt.want {
				t.Errorf("isRetryableError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// failNLLM — 前 N 次调用失败，之后成功的 mock
// ---------------------------------------------------------------------------

type failNLLM struct {
	failCount int          // 前 N 次返回错误
	failErr   error        // 返回的错误
	calls     atomic.Int32 // 实际调用次数
	response  *LLMResponse // 成功时返回的响应
}

func newFailNLLM(failCount int, err error) *failNLLM {
	return &failNLLM{
		failCount: failCount,
		failErr:   err,
		response: &LLMResponse{
			Content:      "ok",
			FinishReason: FinishReasonStop,
			Usage:        TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
}

func (m *failNLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	n := int(m.calls.Add(1))
	if n <= m.failCount {
		return nil, m.failErr
	}
	return m.response, nil
}

func (m *failNLLM) ListModels() []string {
	return []string{"fail-n-mock"}
}

func (m *failNLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (
	<-chan StreamEvent, error) {
	n := int(m.calls.Add(1))
	if n <= m.failCount {
		return nil, m.failErr
	}
	ch := make(chan StreamEvent, 2)
	ch <- StreamEvent{Type: EventContent, Content: "ok"}
	ch <- StreamEvent{Type: EventDone, FinishReason: FinishReasonStop}
	close(ch)
	return ch, nil
}

// ---------------------------------------------------------------------------
// Generate 重试测试
// ---------------------------------------------------------------------------

func TestRetryLLM_Generate_SuccessOnFirstTry(t *testing.T) {
	inner := newFailNLLM(0, nil)
	r := NewRetryLLM(inner, DefaultRetryConfig())

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
	if inner.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_RetryThenSuccess(t *testing.T) {
	// 前 2 次返回 502，第 3 次成功
	retryableErr := errors.New(`POST "url": 502 Bad Gateway`)
	inner := newFailNLLM(2, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
	if inner.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_ExhaustedRetries(t *testing.T) {
	// 始终返回 429，3 次尝试全部失败
	retryableErr := errors.New(`POST "url": 429 Too Many Requests`)
	inner := newFailNLLM(100, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	_, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if inner.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_NonRetryableError(t *testing.T) {
	// 401 不可重试，应该只调用 1 次
	nonRetryableErr := errors.New(`POST "url": 401 Unauthorized`)
	inner := newFailNLLM(100, nonRetryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	_, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if inner.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (non-retryable should not retry)", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_ContextCanceled(t *testing.T) {
	// context.Canceled 应停止重试（isRetryableError 返回 false）
	inner := newFailNLLM(100, context.Canceled)
	cfg := RetryConfig{Attempts: 5, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}

	r := NewRetryLLM(inner, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	_, err := r.Generate(ctx, "test", nil, nil, "")
	if err == nil {
		t.Fatal("expected error after context cancel")
	}
	// context.Canceled 不可重试，应只调用 1 次
	if inner.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (context.Canceled is not retryable)", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_NetworkError(t *testing.T) {
	// 网络错误可重试
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	inner := newFailNLLM(1, netErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
	if inner.calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", inner.calls.Load())
	}
}

// ---------------------------------------------------------------------------
// GenerateStream 重试测试
// ---------------------------------------------------------------------------

func TestRetryLLM_GenerateStream_SuccessOnFirstTry(t *testing.T) {
	inner := newFailNLLM(0, nil)
	r := NewRetryLLM(inner, DefaultRetryConfig())

	ch, err := r.GenerateStream(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var events []StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) == 0 {
		t.Fatal("expected events, got none")
	}
}

func TestRetryLLM_GenerateStream_RetryConnection(t *testing.T) {
	// 前 1 次连接失败（返回 error），第 2 次成功
	retryableErr := errors.New(`POST "url": 503 Service Unavailable`)
	inner := newFailNLLM(1, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	ch, err := r.GenerateStream(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var gotContent bool
	for ev := range ch {
		if ev.Type == EventContent && ev.Content == "ok" {
			gotContent = true
		}
	}
	if !gotContent {
		t.Error("expected content event with 'ok'")
	}
}

func TestRetryLLM_GenerateStream_NonStreamingInner(t *testing.T) {
	// inner 不实现 StreamingLLM 时应返回错误
	inner := &nonStreamingLLM{}
	r := NewRetryLLM(inner, DefaultRetryConfig())

	_, err := r.GenerateStream(context.Background(), "test", nil, nil, "")
	if err == nil {
		t.Fatal("expected error for non-streaming LLM")
	}
	if err.Error() != "underlying LLM does not support streaming" {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListModels 测试（直接转发，不重试）
// ---------------------------------------------------------------------------

func TestRetryLLM_ListModels(t *testing.T) {
	inner := newFailNLLM(0, nil)
	r := NewRetryLLM(inner, DefaultRetryConfig())

	models := r.ListModels()
	if len(models) != 1 || models[0] != "fail-n-mock" {
		t.Errorf("ListModels() = %v, want [fail-n-mock]", models)
	}
}

// ---------------------------------------------------------------------------
// DefaultRetryConfig 测试
// ---------------------------------------------------------------------------

func TestDefaultRetryConfig(t *testing.T) {
	cfg := DefaultRetryConfig()
	if cfg.Attempts != 5 {
		t.Errorf("Attempts = %d, want 5", cfg.Attempts)
	}
	if cfg.Delay != 1*time.Second {
		t.Errorf("Delay = %v, want 1s", cfg.Delay)
	}
	if cfg.MaxDelay != 30*time.Second {
		t.Errorf("MaxDelay = %v, want 30s", cfg.MaxDelay)
	}
	if cfg.Timeout != 120*time.Second {
		t.Errorf("Timeout = %v, want 120s", cfg.Timeout)
	}
}

// ---------------------------------------------------------------------------
// 辅助类型
// ---------------------------------------------------------------------------

// nonStreamingLLM 只实现 LLM 接口，不实现 StreamingLLM
type nonStreamingLLM struct{}

func (n *nonStreamingLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok", FinishReason: FinishReasonStop}, nil
}

func (n *nonStreamingLLM) ListModels() []string {
	return []string{"non-streaming"}
}

// ---------------------------------------------------------------------------
// WithRetryNotify 回调测试
// ---------------------------------------------------------------------------

func TestRetryLLM_Generate_NotifiesOnRetry(t *testing.T) {
	// 前 2 次返回 502，第 3 次成功
	retryableErr := errors.New(`POST "url": 502 Bad Gateway`)
	inner := newFailNLLM(2, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	var notifications []struct {
		attempt, max uint
		err          error
	}
	ctx := WithRetryNotify(context.Background(), func(attempt, max uint, err error) {
		notifications = append(notifications, struct {
			attempt, max uint
			err          error
		}{attempt, max, err})
	})

	resp, err := r.Generate(ctx, "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}

	// 应该收到 2 次通知（第 1 次和第 2 次失败后各一次）
	if len(notifications) != 2 {
		t.Fatalf("notifications count = %d, want 2", len(notifications))
	}
	if notifications[0].attempt != 1 || notifications[0].max != 3 {
		t.Errorf("notification[0]: attempt=%d, max=%d, want 1, 3", notifications[0].attempt, notifications[0].max)
	}
	if notifications[1].attempt != 2 || notifications[1].max != 3 {
		t.Errorf("notification[1]: attempt=%d, max=%d, want 2, 3", notifications[1].attempt, notifications[1].max)
	}
}

func TestRetryLLM_Generate_NoNotifyWithoutCallback(t *testing.T) {
	// 没有注入回调时不应 panic
	retryableErr := errors.New(`POST "url": 502 Bad Gateway`)
	inner := newFailNLLM(1, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.Generate(context.Background(), "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
}

func TestRetryLLM_GenerateStream_NotifiesOnRetry(t *testing.T) {
	retryableErr := errors.New(`POST "url": 503 Service Unavailable`)
	inner := newFailNLLM(1, retryableErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	var notified atomic.Int32
	ctx := WithRetryNotify(context.Background(), func(attempt, max uint, err error) {
		notified.Add(1)
	})

	ch, err := r.GenerateStream(ctx, "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// drain channel
	for range ch {
	}

	if notified.Load() != 1 {
		t.Errorf("notified = %d, want 1", notified.Load())
	}
}

// ---------------------------------------------------------------------------
// 超时重试测试
// ---------------------------------------------------------------------------

func TestRetryLLM_Generate_TimeoutRetry(t *testing.T) {
	// 前 2 次返回 context.DeadlineExceeded，第 3 次成功
	inner := newFailNLLM(2, context.DeadlineExceeded)
	cfg := RetryConfig{
		Attempts: 5,
		Delay:    10 * time.Millisecond,
		MaxDelay: 50 * time.Millisecond,
		Timeout:  50 * time.Millisecond,
	}

	r := NewRetryLLM(inner, cfg)

	// 使用 context.WithTimeout 模拟调用方设置的超时
	parentCtx, parentCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer parentCancel()

	resp, err := r.Generate(parentCtx, "test", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content = %q, want %q", resp.Content, "ok")
	}
	// 应该调用了 3 次（前 2 次超时，第 3 次成功）
	if inner.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", inner.calls.Load())
	}
}

func TestRetryLLM_perAttemptCtx(t *testing.T) {
	t.Run("parent has deadline", func(t *testing.T) {
		r := NewRetryLLM(&nonStreamingLLM{}, RetryConfig{})
		parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		child, childCancel := r.perAttemptCtx(parent)
		defer childCancel()

		// child 应该有自己的 deadline（不继承 parent 的）
		if child == parent {
			t.Error("perAttemptCtx should create a new context")
		}
		childDeadline, ok := child.Deadline()
		if !ok {
			t.Fatal("child context should have a deadline")
		}
		parentDeadline, _ := parent.Deadline()
		// On Windows, time.Until() and WithTimeout can produce identical
		// deadlines due to lower timer precision. Only verify that the child
		// deadline is not before the parent (i.e. >=, not strictly >).
		if childDeadline.Before(parentDeadline) {
			t.Errorf("child deadline %v should not be before parent deadline %v", childDeadline, parentDeadline)
		}
	})

	t.Run("parent has no deadline", func(t *testing.T) {
		r := NewRetryLLM(&nonStreamingLLM{}, RetryConfig{})

		child, childCancel := r.perAttemptCtx(context.Background())
		defer childCancel()

		// 没有配置 Timeout 且 parent 无 deadline，应返回原 ctx
		if _, ok := child.Deadline(); ok {
			t.Error("child should not have a deadline when parent has none and Timeout is 0")
		}
	})

	t.Run("config Timeout takes priority", func(t *testing.T) {
		r := NewRetryLLM(&nonStreamingLLM{}, RetryConfig{
			Timeout: 2 * time.Second,
		})

		// parent 有 10 秒 deadline，但 config.Timeout = 2s 应优先
		parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		child, childCancel := r.perAttemptCtx(parent)
		defer childCancel()

		childDeadline, ok := child.Deadline()
		if !ok {
			t.Fatal("child should have a deadline")
		}
		remaining := time.Until(childDeadline)
		if remaining > 3*time.Second || remaining < time.Second {
			t.Errorf("child deadline should be ~2s, got %v", remaining)
		}
	})

	t.Run("parent already canceled", func(t *testing.T) {
		r := NewRetryLLM(&nonStreamingLLM{}, RetryConfig{})
		parent, cancel := context.WithCancel(context.Background())
		cancel()

		child, childCancel := r.perAttemptCtx(parent)
		defer childCancel()

		// 父 ctx 已取消，应返回父 ctx 本身
		if child != parent {
			t.Error("should return parent context when already canceled")
		}
	})
}
