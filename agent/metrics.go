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

	// 压缩效率
	AvgTokensSavedPerCompress float64 // 每次压缩平均节省 token
	TokenSavingRate           float64 // token 节省率 (saved/in)

	// 记忆质量
	OffloadRecallRate float64 // offload 回调率
	MaskedRecallRate  float64 // masked 回调率
	SummaryRefineRate float64 // 摘要精化率 = SummaryRefines / CompressEvents
	ContextEditRate   float64 // 上下文编辑率 = ContextEditEvents / TotalConversations

	// 任务完成效果
	ToolSuccessRate     float64 // 工具成功率
	LLMSuccessRate      float64 // LLM 成功率
	AvgToolCallsPerConv float64 // 每对话平均工具调用数

	// 成本效率
	AvgTokensPerConv float64 // 每对话平均 token（输入+输出）
	OutputInputRatio float64 // 输出/输入比

	// 四层防御效能
	CombinedSavingRate float64 // 四层联合保存率 = 总节省 / 总输入
}

// GlobalMetrics 全局指标单例。
var GlobalMetrics *AgentMetrics

func init() {
	GlobalMetrics = &AgentMetrics{
		StartTime: time.Now(),
	}
}

// RecordConversation 记录一次对话完成。
// llmCalls 参数作为兜底校验：如果 engine.go 中的直接计数遗漏了某些路径，
// RecordConversation 仍能通过此参数补齐。
func (m *AgentMetrics) RecordConversation(iterations, toolCalls, llmCalls, inputTokens, outputTokens int) {
	m.TotalConversations.Add(1)
	m.TotalIterations.Add(int64(iterations))
	m.TotalToolCalls.Add(int64(toolCalls))
	m.TotalLLMCalls.Add(int64(llmCalls))
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

	// 压缩效率
	if s.CompressEvents > 0 {
		s.AvgTokensSavedPerCompress = float64(s.CompressTokensIn-s.CompressTokensOut) / float64(s.CompressEvents)
	}
	if s.CompressTokensIn > 0 {
		s.TokenSavingRate = float64(s.CompressTokensIn-s.CompressTokensOut) / float64(s.CompressTokensIn)
	}

	// 记忆质量
	if s.OffloadedItems > 0 {
		s.OffloadRecallRate = float64(s.OffloadedRecalls) / float64(s.OffloadedItems)
	}
	if s.MaskedItems > 0 {
		s.MaskedRecallRate = float64(s.MaskedRecalls) / float64(s.MaskedItems)
	}
	if s.CompressEvents > 0 {
		s.SummaryRefineRate = float64(s.SummaryRefines) / float64(s.CompressEvents)
	}
	if s.TotalConversations > 0 {
		s.ContextEditRate = float64(s.ContextEditEvents) / float64(s.TotalConversations)
	}

	// 任务完成效果
	if s.TotalToolCalls > 0 {
		s.ToolSuccessRate = float64(s.TotalToolCalls-s.TotalToolErrors) / float64(s.TotalToolCalls)
	}
	if s.TotalLLMCalls > 0 {
		s.LLMSuccessRate = float64(s.TotalLLMCalls-s.TotalLLMErrors) / float64(s.TotalLLMCalls)
	}
	if s.TotalConversations > 0 {
		s.AvgToolCallsPerConv = float64(s.TotalToolCalls) / float64(s.TotalConversations)
	}

	// 成本效率
	if s.TotalConversations > 0 {
		s.AvgTokensPerConv = float64(s.TotalInputTokens+s.TotalOutputTokens) / float64(s.TotalConversations)
	}
	if s.TotalInputTokens > 0 {
		s.OutputInputRatio = float64(s.TotalOutputTokens) / float64(s.TotalInputTokens)
	}

	// 四层防御效能
	if s.TotalInputTokens > 0 {
		s.CombinedSavingRate = float64(s.CompressTokensIn-s.CompressTokensOut) / float64(s.TotalInputTokens)
	}

	return s
}

// FormatMarkdown 将 MetricsSnapshot 格式化为飞书 markdown 卡片文本。
// 按能力维度分组为四段：运行概览、任务执行效果、记忆质量、压缩效率、四层防御效能。
func (s MetricsSnapshot) FormatMarkdown() string {
	var sb strings.Builder

	hasOverview := s.TotalConversations > 0 || s.TotalIterations > 0 || s.TotalToolCalls > 0
	hasMemory := s.MaskedItems > 0 || s.OffloadedItems > 0 || s.ContextEditEvents > 0 || s.CompressEvents > 0
	hasCompress := s.CompressEvents > 0
	hasDefense := s.MaskedItems > 0 || s.OffloadedItems > 0 || s.CompressTokensIn > 0

	// ── 运行概览 ──
	sb.WriteString("⏱️ **运行概览**\n──────────────\n")
	if hasOverview {
		fmt.Fprintf(&sb, "⏱️ 运行时长：%s\n", formatDuration(s.UptimeSeconds))
		fmt.Fprintf(&sb, "💬 对话：%d 次", s.TotalConversations)
		if s.TotalConversations > 0 {
			avgIter := float64(s.TotalIterations) / float64(s.TotalConversations)
			fmt.Fprintf(&sb, " | 🔄 平均迭代：%.1f 次/对话", avgIter)
		}
		sb.WriteString("\n")

		fmt.Fprintf(&sb, "🛠️ 工具调用：%d", s.TotalToolCalls)
		if s.TotalToolCalls > 0 {
			fmt.Fprintf(&sb, "（成功率 %.1f%%）", s.ToolSuccessRate*100)
		}
		fmt.Fprintf(&sb, " | 🤖 LLM：%d", s.TotalLLMCalls)
		if s.TotalLLMCalls > 0 {
			fmt.Fprintf(&sb, "（成功率 %.1f%%）", s.LLMSuccessRate*100)
		}
		sb.WriteString("\n")

		if s.TotalConversations > 0 {
			fmt.Fprintf(&sb, "💰 平均成本：%s tokens/对话", formatTokens(int64(s.AvgTokensPerConv)))
			if s.TotalInputTokens > 0 {
				fmt.Fprintf(&sb, " | 输出/输入比：%.1f%%", s.OutputInputRatio*100)
			}
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("暂无数据\n")
	}

	// ── 任务执行效果 ──
	sb.WriteString("\n🎯 **任务执行效果**\n──────────────\n")
	if s.TotalConversations > 0 {
		avgIter := float64(s.TotalIterations) / float64(s.TotalConversations)
		fmt.Fprintf(&sb, "📊 平均迭代：%.1f 次/对话\n", avgIter)

		if s.TotalToolCalls > 0 {
			fmt.Fprintf(&sb, "🛠️ 工具成功率：%.1f%%（%d 调用 / %d 错误）\n",
				s.ToolSuccessRate*100, s.TotalToolCalls, s.TotalToolErrors)
		} else {
			sb.WriteString("🛠️ 工具成功率：N/A（无调用）\n")
		}

		if s.TotalLLMCalls > 0 {
			fmt.Fprintf(&sb, "🤖 LLM 成功率：%.1f%%（%d 调用 / %d 错误）\n",
				s.LLMSuccessRate*100, s.TotalLLMCalls, s.TotalLLMErrors)
		} else {
			sb.WriteString("🤖 LLM 成功率：N/A（无调用）\n")
		}

		fmt.Fprintf(&sb, "🔧 每对话工具：%.1f 次\n", s.AvgToolCallsPerConv)
	} else {
		sb.WriteString("暂无数据\n")
	}

	// ── 记忆质量 ──
	sb.WriteString("\n🧠 **记忆质量**\n──────────────\n")
	if hasMemory {
		if s.MaskedItems > 0 {
			fmt.Fprintf(&sb, "🎭 Masking 回调率：%.1f%%（%d / %d）\n",
				s.MaskedRecallRate*100, s.MaskedRecalls, s.MaskedItems)
		}
		if s.OffloadedItems > 0 {
			fmt.Fprintf(&sb, "💾 Offload 回调率：%.1f%%（%d / %d）\n",
				s.OffloadRecallRate*100, s.OffloadedRecalls, s.OffloadedItems)
		}
		totalEvictions := s.MaskedItems + s.OffloadedItems
		totalRecalls := s.OffloadedRecalls + s.MaskedRecalls
		if totalEvictions > 0 {
			fmt.Fprintf(&sb, "📉 总回调率：%.1f%%（%d / %d）\n",
				s.RecallRate*100, totalRecalls, totalEvictions)
		}
		if s.TotalConversations > 0 {
			fmt.Fprintf(&sb, "✂️ 上下文编辑率：%.1f%%（%d / %d 对话）\n",
				s.ContextEditRate*100, s.ContextEditEvents, s.TotalConversations)
		}
		if s.CompressEvents > 0 {
			fmt.Fprintf(&sb, "🔍 摘要精化率：%.1f%%（%d / %d 压缩）← 精化率高说明压缩摘要质量差\n",
				s.SummaryRefineRate*100, s.SummaryRefines, s.CompressEvents)
		}
	} else {
		sb.WriteString("暂无数据\n")
	}

	// ── 压缩效率 ──
	sb.WriteString("\n📦 **压缩效率**\n──────────────\n")
	if hasCompress {
		saved := s.CompressTokensIn - s.CompressTokensOut
		fmt.Fprintf(&sb, "🧹 压缩：%d 次 | 节省 %s tokens（节省率 %.1f%%）\n",
			s.CompressEvents, formatTokens(saved), s.TokenSavingRate*100)
		fmt.Fprintf(&sb, "📊 每次平均节省：%s tokens\n",
			formatTokens(int64(s.AvgTokensSavedPerCompress)))
		fmt.Fprintf(&sb, "📐 输出/输入比：%.1f%%\n", s.CompressRatio*100)
	} else {
		sb.WriteString("暂无数据\n")
	}

	// ── 四层防御效能 ──
	sb.WriteString("\n🛡️ **四层防御效能**\n──────────────\n")
	if hasDefense {
		fmt.Fprintf(&sb, "🎭 Masking：遮蔽 %d 条（免费压缩）\n", s.MaskedItems)
		fmt.Fprintf(&sb, "💾 Offload：落盘 %d 条（磁盘保存）\n", s.OffloadedItems)
		if s.CompressTokensIn > 0 {
			saved := s.CompressTokensIn - s.CompressTokensOut
			fmt.Fprintf(&sb, "🧹 压缩：节省 %s tokens（节省率 %.1f%%）\n",
				formatTokens(saved), s.TokenSavingRate*100)
		} else {
			sb.WriteString("🧹 压缩：无数据\n")
		}
		fmt.Fprintf(&sb, "📈 联合保存率：%.1f%%（节省 %s / 总输入 %s）\n",
			s.CombinedSavingRate*100,
			formatTokens(s.CompressTokensIn-s.CompressTokensOut),
			formatTokens(s.TotalInputTokens))
	} else {
		sb.WriteString("暂无数据\n")
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
