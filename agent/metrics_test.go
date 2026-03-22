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
		TotalLLMErrors:     0,

		// 计算指标（模拟 Snapshot() 的计算结果）
		AvgTokensPerIter:          float64(2_100_000) / float64(186),
		CompressRatio:             0.667,
		RecallRate:                25.0 / 70.0,
		AvgTokensSavedPerCompress: float64(2_100_000-1_400_000) / 6,
		TokenSavingRate:           float64(2_100_000-1_400_000) / float64(2_100_000),
		OffloadRecallRate:         15.0 / 23.0,
		MaskedRecallRate:          10.0 / 47.0,
		SummaryRefineRate:         2.0 / 6.0,
		ContextEditRate:           3.0 / 42.0,
		ToolSuccessRate:           float64(312-8) / float64(312),
		LLMSuccessRate:            1.0,
		AvgToolCallsPerConv:       312.0 / 42.0,
		AvgTokensPerConv:          float64(2_100_000+180_000) / 42.0,
		OutputInputRatio:          float64(180_000) / float64(2_100_000),
		CombinedSavingRate:        float64(2_100_000-1_400_000) / float64(2_100_000),
	}

	md := s.FormatMarkdown()

	// 验证五段式能力维度分组标题
	checks := []string{
		"⏱️ **运行概览**",
		"🎯 **任务执行效果**",
		"🧠 **记忆质量**",
		"📦 **压缩效率**",
		"🛡️ **四层防御效能**",
		"3h 25m",
		"42 次",
		"97.4%",
		"100.0%",
		"回调率",
		"精化率高说明压缩摘要质量差",
		"联合保存率",
	}
	for _, check := range checks {
		if !strings.Contains(md, check) {
			t.Errorf("markdown missing %q", check)
		}
	}

	// 验证不应出现旧格式的内容
	oldChecks := []string{
		"📊 **运行指标**",
		"📦 **上下文管理**",
	}
	for _, check := range oldChecks {
		if strings.Contains(md, check) {
			t.Errorf("markdown should NOT contain old format %q", check)
		}
	}
}

func TestFormatMarkdown_ZeroValues(t *testing.T) {
	s := MetricsSnapshot{}
	md := s.FormatMarkdown()

	// 零值时应显示"暂无数据"而非 panic
	if !strings.Contains(md, "暂无数据") {
		t.Error("zero-value markdown should contain '暂无数据'")
	}
	// 五个段落标题都应存在
	sections := []string{
		"⏱️ **运行概览**",
		"🎯 **任务执行效果**",
		"🧠 **记忆质量**",
		"📦 **压缩效率**",
		"🛡️ **四层防御效能**",
	}
	for _, sec := range sections {
		if !strings.Contains(md, sec) {
			t.Errorf("zero-value markdown should contain section %q", sec)
		}
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

// TestSnapshot_NewComputedMetrics 测试所有新增计算字段的正确性。
func TestSnapshot_NewComputedMetrics(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}

	// 1 conversation: 10 iters, 20 tools, 8 llm, 5000 in, 1000 out
	m.RecordConversation(10, 20, 8, 5000, 1000)
	m.CompressEvents.Add(3)
	m.CompressTokensIn.Add(10000)
	m.CompressTokensOut.Add(6000)
	m.MaskedItems.Add(47)
	m.MaskedRecalls.Add(10)
	m.OffloadedItems.Add(23)
	m.OffloadedRecalls.Add(15)
	m.ContextEditEvents.Add(3)
	m.SummaryRefines.Add(2)
	m.TotalToolErrors.Add(8)
	m.TotalLLMErrors.Add(0)

	s := m.Snapshot()

	// AvgTokensSavedPerCompress = (10000 - 6000) / 3 = 1333.33
	if s.AvgTokensSavedPerCompress < 1333 || s.AvgTokensSavedPerCompress > 1334 {
		t.Errorf("expected AvgTokensSavedPerCompress ~1333.33, got %.2f", s.AvgTokensSavedPerCompress)
	}

	// TokenSavingRate = (10000 - 6000) / 10000 = 0.4
	if s.TokenSavingRate != 0.4 {
		t.Errorf("expected TokenSavingRate 0.4, got %.4f", s.TokenSavingRate)
	}

	// OffloadRecallRate = 15 / 23 = 0.6522
	if s.OffloadRecallRate < 0.65 || s.OffloadRecallRate > 0.66 {
		t.Errorf("expected OffloadRecallRate ~0.652, got %.4f", s.OffloadRecallRate)
	}

	// MaskedRecallRate = 10 / 47 = 0.2128
	if s.MaskedRecallRate < 0.21 || s.MaskedRecallRate > 0.22 {
		t.Errorf("expected MaskedRecallRate ~0.213, got %.4f", s.MaskedRecallRate)
	}

	// SummaryRefineRate = 2 / 3 = 0.6667
	if s.SummaryRefineRate < 0.66 || s.SummaryRefineRate > 0.67 {
		t.Errorf("expected SummaryRefineRate ~0.667, got %.4f", s.SummaryRefineRate)
	}

	// ContextEditRate = 3 / 1 = 3.0
	if s.ContextEditRate != 3.0 {
		t.Errorf("expected ContextEditRate 3.0, got %.1f", s.ContextEditRate)
	}

	// ToolSuccessRate = (20 - 8) / 20 = 0.6
	if s.ToolSuccessRate != 0.6 {
		t.Errorf("expected ToolSuccessRate 0.6, got %.4f", s.ToolSuccessRate)
	}

	// LLMSuccessRate = (8 - 0) / 8 = 1.0
	if s.LLMSuccessRate != 1.0 {
		t.Errorf("expected LLMSuccessRate 1.0, got %.4f", s.LLMSuccessRate)
	}

	// AvgToolCallsPerConv = 20 / 1 = 20.0
	if s.AvgToolCallsPerConv != 20.0 {
		t.Errorf("expected AvgToolCallsPerConv 20.0, got %.1f", s.AvgToolCallsPerConv)
	}

	// AvgTokensPerConv = (5000 + 1000) / 1 = 6000.0
	if s.AvgTokensPerConv != 6000.0 {
		t.Errorf("expected AvgTokensPerConv 6000.0, got %.1f", s.AvgTokensPerConv)
	}

	// OutputInputRatio = 1000 / 5000 = 0.2
	if s.OutputInputRatio != 0.2 {
		t.Errorf("expected OutputInputRatio 0.2, got %.4f", s.OutputInputRatio)
	}

	// CombinedSavingRate = (10000 - 6000) / 5000 = 0.8
	if s.CombinedSavingRate != 0.8 {
		t.Errorf("expected CombinedSavingRate 0.8, got %.4f", s.CombinedSavingRate)
	}
}

// TestSnapshot_DivisionByZero 测试除数为 0 时所有比率字段保持零值。
func TestSnapshot_DivisionByZero(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}
	// 不调用 RecordConversation，所有计数器为 0
	s := m.Snapshot()

	// 所有比率字段应为零值
	rateFields := []struct {
		name  string
		value float64
	}{
		{"AvgTokensPerIter", s.AvgTokensPerIter},
		{"CompressRatio", s.CompressRatio},
		{"RecallRate", s.RecallRate},
		{"AvgTokensSavedPerCompress", s.AvgTokensSavedPerCompress},
		{"TokenSavingRate", s.TokenSavingRate},
		{"OffloadRecallRate", s.OffloadRecallRate},
		{"MaskedRecallRate", s.MaskedRecallRate},
		{"SummaryRefineRate", s.SummaryRefineRate},
		{"ContextEditRate", s.ContextEditRate},
		{"ToolSuccessRate", s.ToolSuccessRate},
		{"LLMSuccessRate", s.LLMSuccessRate},
		{"AvgToolCallsPerConv", s.AvgToolCallsPerConv},
		{"AvgTokensPerConv", s.AvgTokensPerConv},
		{"OutputInputRatio", s.OutputInputRatio},
		{"CombinedSavingRate", s.CombinedSavingRate},
	}
	for _, f := range rateFields {
		if f.value != 0 {
			t.Errorf("expected %s = 0 on zero input, got %.4f", f.name, f.value)
		}
	}
}

// TestSnapshot_PartialZeroDenominators 测试部分分母为零时的混合场景。
func TestSnapshot_PartialZeroDenominators(t *testing.T) {
	m := &AgentMetrics{StartTime: testTime()}

	// 有对话和 token，但压缩事件为 0
	m.RecordConversation(10, 20, 8, 5000, 1000)
	m.CompressTokensIn.Add(10000)
	m.CompressTokensOut.Add(6000)
	// CompressEvents = 0

	s := m.Snapshot()

	// CompressEvents = 0 → AvgTokensSavedPerCompress = 0
	if s.AvgTokensSavedPerCompress != 0 {
		t.Errorf("expected 0 when CompressEvents=0, got %.4f", s.AvgTokensSavedPerCompress)
	}

	// CompressTokensIn > 0 → TokenSavingRate = 0.4
	if s.TokenSavingRate != 0.4 {
		t.Errorf("expected TokenSavingRate 0.4, got %.4f", s.TokenSavingRate)
	}

	// TotalInputTokens > 0 → CombinedSavingRate = (10000-6000)/5000 = 0.8
	if s.CombinedSavingRate != 0.8 {
		t.Errorf("expected CombinedSavingRate 0.8, got %.4f", s.CombinedSavingRate)
	}

	// 无 mask/offload → OffloadRecallRate = 0, MaskedRecallRate = 0
	if s.OffloadRecallRate != 0 || s.MaskedRecallRate != 0 {
		t.Error("expected recall rates = 0 when no items evicted")
	}
}
