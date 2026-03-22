package agent

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// AgentMetrics Agent 运行指标（全局单例，进程重启归零）。
// 使用 atomic 操作保证并发安全且零锁。
type AgentMetrics struct {
	StartTime time.Time // 进程启动时间

	// === 对话指标 ===
	TotalConversations atomic.Int64 // 总对话数（每个用户消息算一次）
	TotalIterations    atomic.Int64 // 总 Agent 迭代数
	TotalToolCalls     atomic.Int64 // 总工具调用数
	TotalLLMCalls      atomic.Int64 // 总 LLM API 调用数
	TotalInputTokens   atomic.Int64 // 总输入 token 数
	TotalOutputTokens  atomic.Int64 // 总输出 token 数

	// === 上下文管理指标（核心 — 衡量四层防御效果） ===
	MaskingEvents     atomic.Int64 // Observation Masking 触发次数
	MaskedItems       atomic.Int64 // 被遮蔽的 tool result 数量
	OffloadEvents     atomic.Int64 // Offload 触发次数
	OffloadedItems    atomic.Int64 // 被落盘的 tool result 数量
	OffloadedRecalls  atomic.Int64 // offload_recall 被调用次数
	MaskedRecalls     atomic.Int64 // recall_masked 被调用次数
	CompressEvents    atomic.Int64 // 上下文压缩触发次数
	CompressTokensIn  atomic.Int64 // 压缩前 token 总量
	CompressTokensOut atomic.Int64 // 压缩后 token 总量
	ContextEditEvents atomic.Int64 // Context Editing 触发次数
	SummaryRefines    atomic.Int64 // 摘要精化触发次数

	// === 效率指标 ===
	TotalToolErrors atomic.Int64 // 工具执行错误次数
	TotalLLMErrors  atomic.Int64 // LLM 调用错误次数
}

// MetricsSnapshot 指标快照（用于 Settings 展示）。
type MetricsSnapshot struct {
	UptimeSeconds      int64
	TotalConversations int64
	TotalIterations    int64
	TotalToolCalls     int64
	TotalLLMCalls      int64
	TotalInputTokens   int64
	TotalOutputTokens  int64

	// 上下文管理
	MaskingEvents     int64
	MaskedItems       int64
	OffloadEvents     int64
	OffloadedItems    int64
	OffloadedRecalls  int64
	MaskedRecalls     int64
	CompressEvents    int64
	CompressTokensIn  int64
	CompressTokensOut int64
	ContextEditEvents int64
	SummaryRefines    int64

	// 效率
	TotalToolErrors int64
	TotalLLMErrors  int64

	// 计算指标
	AvgTokensPerIter float64 // 平均每次迭代输入 token 数
	CompressRatio    float64 // 总体压缩比 (out/in)
	RecallRate       float64 // 回调率 (recalls / offloads+maskings)
}

// GlobalMetrics 全局指标单例。
var GlobalMetrics *AgentMetrics

func init() {
	GlobalMetrics = &AgentMetrics{
		StartTime: time.Now(),
	}
}

// RecordConversation 记录一次对话完成。
func (m *AgentMetrics) RecordConversation(iterations, toolCalls, llmCalls, inputTokens, outputTokens int) {
	m.TotalConversations.Add(1)
	m.TotalIterations.Add(int64(iterations))
	m.TotalToolCalls.Add(int64(toolCalls))
	m.TotalInputTokens.Add(int64(inputTokens))
	m.TotalOutputTokens.Add(int64(outputTokens))
}

// Snapshot 获取指标快照（用于 Settings 展示）。
func (m *AgentMetrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}

	totalMasked := m.MaskedItems.Load()
	totalOffloaded := m.OffloadedItems.Load()
	totalRecalls := m.OffloadedRecalls.Load() + m.MaskedRecalls.Load()
	totalEvictions := totalMasked + totalOffloaded

	s := MetricsSnapshot{
		UptimeSeconds:      int64(time.Since(m.StartTime).Seconds()),
		TotalConversations: m.TotalConversations.Load(),
		TotalIterations:    m.TotalIterations.Load(),
		TotalToolCalls:     m.TotalToolCalls.Load(),
		TotalLLMCalls:      m.TotalLLMCalls.Load(),
		TotalInputTokens:   m.TotalInputTokens.Load(),
		TotalOutputTokens:  m.TotalOutputTokens.Load(),

		MaskingEvents:     m.MaskingEvents.Load(),
		MaskedItems:       totalMasked,
		OffloadEvents:     m.OffloadEvents.Load(),
		OffloadedItems:    totalOffloaded,
		OffloadedRecalls:  m.OffloadedRecalls.Load(),
		MaskedRecalls:     m.MaskedRecalls.Load(),
		CompressEvents:    m.CompressEvents.Load(),
		CompressTokensIn:  m.CompressTokensIn.Load(),
		CompressTokensOut: m.CompressTokensOut.Load(),
		ContextEditEvents: m.ContextEditEvents.Load(),
		SummaryRefines:    m.SummaryRefines.Load(),

		TotalToolErrors: m.TotalToolErrors.Load(),
		TotalLLMErrors:  m.TotalLLMErrors.Load(),
	}

	// 计算指标
	iters := m.TotalIterations.Load()
	if iters > 0 {
		s.AvgTokensPerIter = float64(s.TotalInputTokens) / float64(iters)
	}

	tokensIn := m.CompressTokensIn.Load()
	if tokensIn > 0 {
		s.CompressRatio = float64(m.CompressTokensOut.Load()) / float64(tokensIn)
	}

	if totalEvictions > 0 {
		s.RecallRate = float64(totalRecalls) / float64(totalEvictions)
	}

	return s
}

// FormatMarkdown 将 MetricsSnapshot 格式化为飞书 markdown 卡片文本。
func (s MetricsSnapshot) FormatMarkdown() string {
	var sb strings.Builder

	// 运行时长
	sb.WriteString("📊 **运行指标**\n──────────────\n")
	sb.WriteString(fmt.Sprintf("⏱️ 运行时长：%s\n", formatDuration(s.UptimeSeconds)))
	sb.WriteString(fmt.Sprintf("💬 对话次数：%d\n", s.TotalConversations))

	if s.TotalConversations > 0 {
		avgIter := float64(s.TotalIterations) / float64(s.TotalConversations)
		sb.WriteString(fmt.Sprintf("🔄 Agent 迭代：%d（平均 %.1f 次/对话）\n", s.TotalIterations, avgIter))
	} else {
		sb.WriteString(fmt.Sprintf("🔄 Agent 迭代：%d\n", s.TotalIterations))
	}

	sb.WriteString(fmt.Sprintf("🛠️ 工具调用：%d | ❌ 错误：%d\n", s.TotalToolCalls, s.TotalToolErrors))

	sb.WriteString(fmt.Sprintf("🤖 LLM 调用：%d", s.TotalLLMCalls))
	if s.TotalInputTokens > 0 {
		sb.WriteString(fmt.Sprintf(" | 输入 %s | 输出 %s",
			formatTokens(s.TotalInputTokens),
			formatTokens(s.TotalOutputTokens)))
	}
	sb.WriteString("\n")

	// 上下文管理
	sb.WriteString("\n📦 **上下文管理**\n──────────────\n")
	sb.WriteString(fmt.Sprintf("🎭 Masking：触发 %d 次 | 遮蔽 %d 条\n", s.MaskingEvents, s.MaskedItems))
	sb.WriteString(fmt.Sprintf("💾 Offload：触发 %d 次 | 落盘 %d 条 | 召回 %d 次\n",
		s.OffloadEvents, s.OffloadedItems, s.OffloadedRecalls))

	if s.CompressEvents > 0 {
		ratio := s.CompressRatio * 100
		sb.WriteString(fmt.Sprintf("🧹 压缩：触发 %d 次 | 总压缩比 %.0f%%（%s → %s tokens）\n",
			s.CompressEvents, ratio,
			formatTokens(s.CompressTokensIn),
			formatTokens(s.CompressTokensOut)))
	} else {
		sb.WriteString(fmt.Sprintf("🧹 压缩：触发 %d 次\n", s.CompressEvents))
	}

	sb.WriteString(fmt.Sprintf("✂️ Context Edit：%d 次\n", s.ContextEditEvents))
	sb.WriteString(fmt.Sprintf("🔍 摘要精化：%d 次\n", s.SummaryRefines))

	totalEvictions := s.MaskedItems + s.OffloadedItems
	totalRecalls := s.OffloadedRecalls + s.MaskedRecalls
	if totalEvictions > 0 {
		recallRate := float64(totalRecalls) / float64(totalEvictions) * 100
		sb.WriteString(fmt.Sprintf("📉 总回调率：%.1f%%（%d recalls / %d 遮蔽+落盘）\n",
			recallRate, totalRecalls, totalEvictions))
	}

	return sb.String()
}

// formatDuration 将秒数格式化为人类可读格式（如 "3h 25m"）。
func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	hours := seconds / 3600
	mins := (seconds % 3600) / 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}

// formatTokens 将 token 数格式化为 K/M 单位。
func formatTokens(tokens int64) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d", tokens)
	}
	if tokens < 1_000_000 {
		return fmt.Sprintf("%.1fK", float64(tokens)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
}
