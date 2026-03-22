package agent

import (
	"strings"
	"testing"
	"time"
)

func TestGlobalMetricsInit(t *testing.T) {
	if GlobalMetrics == nil {
		t.Fatal("GlobalMetrics should be initialized")
	}
	if GlobalMetrics.StartTime.IsZero() {
		t.Error("StartTime should not be zero")
	}
}

func TestRecordConversation(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}
	m.RecordConversation(5, 10, 4, 2000, 500)

	if m.TotalConversations.Load() != 1 {
		t.Errorf("expected 1 conversation, got %d", m.TotalConversations.Load())
	}
	if m.TotalIterations.Load() != 5 {
		t.Errorf("expected 5 iterations, got %d", m.TotalIterations.Load())
	}
	if m.TotalToolCalls.Load() != 10 {
		t.Errorf("expected 10 tool calls, got %d", m.TotalToolCalls.Load())
	}
	// TotalLLMCalls 由 RecordConversation 统一计数
	if m.TotalLLMCalls.Load() != 4 {
		t.Errorf("expected 4 LLM calls, got %d", m.TotalLLMCalls.Load())
	}
	if m.TotalInputTokens.Load() != 2000 {
		t.Errorf("expected 2000 input tokens, got %d", m.TotalInputTokens.Load())
	}
	if m.TotalOutputTokens.Load() != 500 {
		t.Errorf("expected 500 output tokens, got %d", m.TotalOutputTokens.Load())
	}
}

func TestRecordConversation_Accumulates(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}
	m.RecordConversation(5, 10, 4, 2000, 500)
	m.RecordConversation(3, 8, 3, 1500, 300)

	if m.TotalConversations.Load() != 2 {
		t.Errorf("expected 2 conversations, got %d", m.TotalConversations.Load())
	}
	if m.TotalIterations.Load() != 8 {
		t.Errorf("expected 8 iterations, got %d", m.TotalIterations.Load())
	}
}

func TestSnapshot(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}
	m.RecordConversation(10, 20, 8, 5000, 1000)
	m.CompressEvents.Add(3)
	m.CompressTokensIn.Add(10000)
	m.CompressTokensOut.Add(6000)

	s := m.Snapshot()

	if s.TotalConversations != 1 {
		t.Errorf("expected 1, got %d", s.TotalConversations)
	}
	if s.TotalIterations != 10 {
		t.Errorf("expected 10, got %d", s.TotalIterations)
	}
	if s.CompressEvents != 3 {
		t.Errorf("expected 3, got %d", s.CompressEvents)
	}

	// CompressRatio = 6000 / 10000 = 0.6
	if s.CompressRatio < 0.59 || s.CompressRatio > 0.61 {
		t.Errorf("expected CompressRatio ~0.6, got %.4f", s.CompressRatio)
	}
}

func TestSnapshot_RecallRate(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}
	m.MaskedItems.Add(100)
	m.OffloadedItems.Add(50)
	m.OffloadedRecalls.Add(15)
	m.MaskedRecalls.Add(10)

	s := m.Snapshot()

	// totalEvictions = 150, totalRecalls = 25
	// recallRate = 25/150 = 0.1667
	if s.RecallRate < 0.16 || s.RecallRate > 0.17 {
		t.Errorf("expected RecallRate ~0.167, got %.4f", s.RecallRate)
	}
}

func TestSnapshot_NilMetrics(t *testing.T) {
	var m *AgentMetrics
	s := m.Snapshot()
	if s.TotalConversations != 0 {
		t.Error("nil metrics should return zero snapshot")
	}
}

func TestSnapshot_AvgTokensPerIter(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}
	m.RecordConversation(10, 0, 0, 5000, 1000)

	s := m.Snapshot()
	// AvgTokensPerIter = 5000 / 10 = 500
	if s.AvgTokensPerIter != 500.0 {
		t.Errorf("expected 500.0, got %.1f", s.AvgTokensPerIter)
	}
}

func TestFormatMarkdown(t *testing.T) {
	s := MetricsSnapshot{
		UptimeSeconds:      12300, // ~3h 25m
		TotalConversations: 42,
		TotalIterations:    186,
		TotalToolCalls:     312,
		TotalLLMCalls:      198,
		TotalInputTokens:   2_100_000,
		TotalOutputTokens:  180_000,
		MaskingEvents:      12,
		MaskedItems:        47,
		OffloadEvents:      8,
		OffloadedItems:     23,
		OffloadedRecalls:   15,
		MaskedRecalls:      10,
		CompressEvents:     6,
		CompressTokensIn:   2_100_000,
		CompressTokensOut:  1_400_000,
		ContextEditEvents:  3,
		SummaryRefines:     2,
		TotalToolErrors:    8,
		CompressRatio:      0.667,
	}

	md := s.FormatMarkdown()

	// 验证关键字段
	checks := []string{
		"📊",
		"3h 25m",
		"42",
		"186",
		"312",
		"198",
		"2.1M",
		"180.0K",
		"12",
		"47",
		"8",
		"23",
		"15",
		"6",
		"3",
		"2",
		"回调率",
	}
	for _, check := range checks {
		if !strings.Contains(md, check) {
			t.Errorf("markdown missing %q", check)
		}
	}
}

func TestFormatMarkdown_ZeroValues(t *testing.T) {
	s := MetricsSnapshot{}
	md := s.FormatMarkdown()

	if !strings.Contains(md, "0") {
		t.Error("zero-value markdown should contain zeros")
	}
	// 不应 panic
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		seconds  int64
		expected string
	}{
		{30, "30s"},
		{90, "1m 30s"},
		{120, "2m 0s"},
		{3661, "1h 1m"},
		{7200, "2h 0m"},
	}
	for _, tt := range tests {
		got := formatDuration(tt.seconds)
		if got != tt.expected {
			t.Errorf("formatDuration(%d) = %q, want %q", tt.seconds, got, tt.expected)
		}
	}
}

func TestFormatTokens(t *testing.T) {
	tests := []struct {
		tokens   int64
		expected string
	}{
		{500, "500"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{500000, "500.0K"},
		{1_000_000, "1.0M"},
		{2_100_000, "2.1M"},
	}
	for _, tt := range tests {
		got := formatTokens(tt.tokens)
		if got != tt.expected {
			t.Errorf("formatTokens(%d) = %q, want %q", tt.tokens, got, tt.expected)
		}
	}
}

func TestMetricsAtomicConcurrency(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}

	// 并发写入不应 panic
	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			m.TotalConversations.Add(1)
			m.TotalToolCalls.Add(1)
			m.TotalLLMCalls.Add(1)
			m.CompressEvents.Add(1)
			m.RecordConversation(1, 1, 1, 100, 50)
		}()
	}

	for i := 0; i < 100; i++ {
		<-done
	}

	// RecordConversation 每次加 1 conversations + 1 iterations + 1 tool calls + 1 llm calls
	// 直接 Add 每次加 1 conversations + 1 tool calls + 1 llm calls + 1 compress
	// 总计: conversations = 200, iterations = 100, tool calls = 200, llm calls = 200, compress = 100
	if m.TotalConversations.Load() != 200 {
		t.Errorf("expected 200 conversations, got %d", m.TotalConversations.Load())
	}
	if m.TotalIterations.Load() != 100 {
		t.Errorf("expected 100 iterations, got %d", m.TotalIterations.Load())
	}
	if m.TotalLLMCalls.Load() != 200 {
		t.Errorf("expected 200 LLM calls (100 direct + 100 via RecordConversation), got %d", m.TotalLLMCalls.Load())
	}
}

// testTime 返回一个固定的测试时间
func testTime() time.Time {
	return time.Now()
}
