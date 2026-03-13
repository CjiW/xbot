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

		// context 错误 — 不重试
		{"context.Canceled", context.Canceled, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false},
		{"wrapped context.Canceled", fmt.Errorf("call failed: %w", context.Canceled), false},
		{"wrapped context.DeadlineExceeded", fmt.Errorf("timeout: %w", context.DeadlineExceeded), false},
		{"string context canceled", errors.New("something context canceled here"), false},
		{"string context deadline exceeded", errors.New("context deadline exceeded"), false},

		// 网络错误 — 重试
		{"net.DNSError timeout", &net.DNSError{Err: "timeout", IsTimeout: true}, true},
		{"net.OpError", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},

		// HTTP 状态码 — OpenAI 格式: `POST "url": NNN StatusText`
		{"429 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 429 Too Many Requests`), true},
		{"500 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 500 Internal Server Error`), true},
		{"502 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 502 Bad Gateway`), true},
		{"503 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 503 Service Unavailable`), true},
		{"504 OpenAI", errors.New(`POST "https://api.openai.com/v1/chat": 504 Gateway Timeout`), true},

		// HTTP 状态码 — CodeBuddy 格式: `status=NNN`
		{"429 CodeBuddy", errors.New("CodeBuddy API error: status=429, body=rate limited"), true},
		{"502 CodeBuddy", errors.New("CodeBuddy API error: status=502, body=bad gateway"), true},

		// 不可重试的 4xx
		{"400 OpenAI", errors.New(`POST "url": 400 Bad Request`), false},
		{"401 OpenAI", errors.New(`POST "url": 401 Unauthorized`), false},
		{"403 OpenAI", errors.New(`POST "url": 403 Forbidden`), false},
		{"404 OpenAI", errors.New(`POST "url": 404 Not Found`), false},
		{"400 CodeBuddy", errors.New("CodeBuddy API error: status=400, body=invalid"), false},

		// 普通错误 — 不重试
		{"generic error", errors.New("something went wrong"), false},
		{"EOF", errors.New("unexpected EOF"), false},
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
	failCount int           // 前 N 次返回错误
	failErr   error         // 返回的错误
	calls     atomic.Int32  // 实际调用次数
	response  *LLMResponse  // 成功时返回的响应
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

func (m *failNLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition) (*LLMResponse, error) {
	n := int(m.calls.Add(1))
	if n <= m.failCount {
		return nil, m.failErr
	}
	return m.response, nil
}

func (m *failNLLM) ListModels() []string {
	return []string{"fail-n-mock"}
}

func (m *failNLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition) (<-chan StreamEvent, error) {
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

	resp, err := r.Generate(context.Background(), "test", nil, nil)
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

	resp, err := r.Generate(context.Background(), "test", nil, nil)
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

	_, err := r.Generate(context.Background(), "test", nil, nil)
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

	_, err := r.Generate(context.Background(), "test", nil, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if inner.calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (non-retryable should not retry)", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_ContextCanceled(t *testing.T) {
	// context 取消后应立即停止，不继续重试
	retryableErr := errors.New(`POST "url": 502 Bad Gateway`)
	inner := newFailNLLM(100, retryableErr)
	cfg := RetryConfig{Attempts: 5, Delay: 100 * time.Millisecond, MaxDelay: 1 * time.Second}
	r := NewRetryLLM(inner, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	// 50ms 后取消，应该来不及完成所有重试
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := r.Generate(ctx, "test", nil, nil)
	if err == nil {
		t.Fatal("expected error after context cancel")
	}
	// 不应该用完所有 5 次尝试
	if inner.calls.Load() >= 5 {
		t.Errorf("calls = %d, expected less than 5 (context should cancel early)", inner.calls.Load())
	}
}

func TestRetryLLM_Generate_NetworkError(t *testing.T) {
	// 网络错误可重试
	netErr := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
	inner := newFailNLLM(1, netErr)
	cfg := RetryConfig{Attempts: 3, Delay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
	r := NewRetryLLM(inner, cfg)

	resp, err := r.Generate(context.Background(), "test", nil, nil)
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

	ch, err := r.GenerateStream(context.Background(), "test", nil, nil)
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

	ch, err := r.GenerateStream(context.Background(), "test", nil, nil)
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

	_, err := r.GenerateStream(context.Background(), "test", nil, nil)
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
	if cfg.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", cfg.Attempts)
	}
	if cfg.Delay != 500*time.Millisecond {
		t.Errorf("Delay = %v, want 500ms", cfg.Delay)
	}
	if cfg.MaxDelay != 10*time.Second {
		t.Errorf("MaxDelay = %v, want 10s", cfg.MaxDelay)
	}
}

// ---------------------------------------------------------------------------
// 辅助类型
// ---------------------------------------------------------------------------

// nonStreamingLLM 只实现 LLM 接口，不实现 StreamingLLM
type nonStreamingLLM struct{}

func (n *nonStreamingLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition) (*LLMResponse, error) {
	return &LLMResponse{Content: "ok", FinishReason: FinishReasonStop}, nil
}

func (n *nonStreamingLLM) ListModels() []string {
	return []string{"non-streaming"}
}
