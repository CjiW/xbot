package agent

import (
	"math"

	"xbot/llm"
)

// ToolCallPattern 工具调用模式分类。
type ToolCallPattern int

const (
	PatternConversation ToolCallPattern = iota
	PatternReadHeavy
	PatternWriteHeavy
	PatternMixed
	PatternSubAgent
)

// TriggerInfo 智能压缩触发所需的信息快照。
type TriggerInfo struct {
	MaxTokens      int
	CurrentTokens  int
	IterationCount int
	ToolCallCount  int
	TokenHistory   []int
	GrowthRate     float64
	RecentTools    []string
	ToolPattern    ToolCallPattern
}

// TokenGrowthTracker 追踪 token 增长趋势的滑动窗口。
type TokenGrowthTracker struct {
	window  []tokenSnapshot
	maxSize int
}

type tokenSnapshot struct {
	iteration int
	tokens    int
}

// NewTokenGrowthTracker 创建增长追踪器，maxSize 默认 10。
func NewTokenGrowthTracker(maxSize int) *TokenGrowthTracker {
	if maxSize <= 0 {
		maxSize = 10
	}
	return &TokenGrowthTracker{
		window:  make([]tokenSnapshot, 0, maxSize),
		maxSize: maxSize,
	}
}

// Record 记录一次 token 快照，超过 maxSize 时丢弃最老的。
func (t *TokenGrowthTracker) Record(iteration, tokens int) {
	if len(t.window) >= t.maxSize {
		t.window = t.window[1:]
	}
	t.window = append(t.window, tokenSnapshot{iteration: iteration, tokens: tokens})
}

// GrowthRate 通过加权线性回归计算 token 增长斜率（最近数据权重更高）。
func (t *TokenGrowthTracker) GrowthRate() float64 {
	n := len(t.window)
	if n < 2 {
		return 0
	}

	var sumW, sumX, sumY, sumXY, sumX2 float64
	for i, s := range t.window {
		w := float64(i + 1) // 越新的权重越高
		x := float64(s.iteration)
		y := float64(s.tokens)
		sumW += w
		sumX += w * x
		sumY += w * y
		sumXY += w * x * y
		sumX2 += w * x * x
	}

	denom := sumW*sumX2 - sumX*sumX
	if denom == 0 {
		return 0
	}
	return (sumW*sumXY - sumX*sumY) / denom
}

// IsExponentialGrowth 检测是否存在指数增长（最近3个快照的加速比率 > 1.5）。
func (t *TokenGrowthTracker) IsExponentialGrowth() bool {
	n := len(t.window)
	if n < 3 {
		return false
	}

	last3 := t.window[n-3:]

	// 计算相邻快照的 token 增量
	deltas := make([]float64, 2)
	for i := 0; i < 2; i++ {
		deltas[i] = float64(last3[i+1].tokens - last3[i].tokens)
		if deltas[i] <= 0 {
			return false
		}
	}

	// 加速比率 = 后一个增量 / 前一个增量
	ratio := deltas[1] / deltas[0]
	return ratio > 1.5
}

// Snapshots 返回当前窗口中的所有快照。
func (t *TokenGrowthTracker) Snapshots() []tokenSnapshot {
	return t.window
}

// Reset 清空窗口。
func (t *TokenGrowthTracker) Reset() {
	t.window = t.window[:0]
}

// CompressCooldown 压缩冷却期管理器，防止短时间内重复压缩。
type CompressCooldown struct {
	lastCompressIteration int
	cooldownIterations    int // 默认 3
}

// NewCompressCooldown 创建冷却期管理器，iterations 默认 3。
func NewCompressCooldown(iterations int) *CompressCooldown {
	if iterations <= 0 {
		iterations = 3
	}
	return &CompressCooldown{
		cooldownIterations: iterations,
	}
}

// ShouldTrigger 判断当前迭代是否已过冷却期。
func (c *CompressCooldown) ShouldTrigger(currentIteration int) bool {
	if c.lastCompressIteration == 0 {
		return true
	}
	return currentIteration-c.lastCompressIteration >= c.cooldownIterations
}

// RecordCompress 记录一次压缩发生的迭代号。
func (c *CompressCooldown) RecordCompress(iteration int) {
	c.lastCompressIteration = iteration
}

// Reset 重置冷却状态。
func (c *CompressCooldown) Reset() {
	c.lastCompressIteration = 0
}

// calculateDynamicThreshold 计算三因子动态压缩阈值。
// 返回值范围 [0.5, 0.85]。
func calculateDynamicThreshold(info TriggerInfo) float64 {
	if info.MaxTokens == 0 {
		return 0.85
	}

	ratio := float64(info.CurrentTokens) / float64(info.MaxTokens)

	// 1. 阶段因子 (0.5-0.85)：根据上下文填充程度映射
	var phaseFactor float64
	switch {
	case ratio < 0.5:
		phaseFactor = 0.85
	case ratio < 0.7:
		// 0.5 → 0.85, 0.7 → 0.7
		phaseFactor = 0.85 + (0.70-0.85)*(ratio-0.5)/(0.7-0.5)
	case ratio < 0.9:
		// 0.7 → 0.7, 0.9 → 0.5
		phaseFactor = 0.70 + (0.50-0.70)*(ratio-0.7)/(0.9-0.7)
	default:
		phaseFactor = 0.5
	}

	// 2. 增长因子 (-0.05 ~ -0.15)
	var growthFactor float64
	switch {
	case info.GrowthRate > 5000:
		growthFactor = -0.15
	case info.GrowthRate > 2000:
		// 2000 → -0.05, 5000 → -0.15
		growthFactor = -0.05 + (-0.15+0.05)*(info.GrowthRate-2000)/(5000-2000)
	case info.GrowthRate > 0:
		growthFactor = -0.05 * (info.GrowthRate / 2000)
	}

	// 3. 模式因子 (-0.05 ~ +0.05)
	var patternFactor float64
	switch info.ToolPattern {
	case PatternReadHeavy:
		patternFactor = -0.05
	case PatternSubAgent:
		patternFactor = 0.05
	default:
		patternFactor = 0
	}

	threshold := phaseFactor + growthFactor + patternFactor
	return math.Max(0.5, math.Min(0.85, threshold))
}

// DetectToolPattern 根据最近工具调用列表检测使用模式。
// 基于频率占比分类：readRatio > 0.7 算 ReadHeavy，writeRatio > 0.7 算 WriteHeavy，subAgentRatio > 0.3 算 SubAgent。
func DetectToolPattern(recentTools []string) ToolCallPattern {
	if len(recentTools) == 0 {
		return PatternConversation
	}

	readTools := map[string]bool{
		"Read": true, "Grep": true, "Glob": true, "WebSearch": true,
		"Fetch": true,
	}
	writeTools := map[string]bool{
		"Edit": true, "Shell": true, "Write": true,
	}

	total := float64(len(recentTools))
	readCount := 0
	writeCount := 0
	subAgentCount := 0

	for _, tool := range recentTools {
		if readTools[tool] {
			readCount++
		}
		if writeTools[tool] {
			writeCount++
		}
		if tool == "SubAgent" {
			subAgentCount++
		}
	}

	readRatio := float64(readCount) / total
	writeRatio := float64(writeCount) / total
	subAgentRatio := float64(subAgentCount) / total

	if subAgentRatio > 0.3 {
		return PatternSubAgent
	}
	if readRatio > 0.7 {
		return PatternReadHeavy
	}
	if writeRatio > 0.7 {
		return PatternWriteHeavy
	}
	if readCount > 0 || writeCount > 0 {
		return PatternMixed
	}
	return PatternConversation
}

// BuildTriggerInfo 从运行时数据构建 TriggerInfo。
func BuildTriggerInfo(iteration int, messages []llm.ChatMessage, toolsUsed []string, provider *TriggerInfoProvider, cfg *ContextManagerConfig, model string) TriggerInfo {
	maxTokens := 100000 // 默认值
	if cfg != nil {
		maxTokens = cfg.MaxContextTokens
	}
	if maxTokens == 0 {
		maxTokens = 100000
	}
	info := TriggerInfo{
		MaxTokens:      maxTokens,
		IterationCount: iteration,
		ToolCallCount:  len(toolsUsed),
	}

	// 计算 CurrentTokens
	if msgTokens, err := llm.CountMessagesTokens(messages, model); err == nil {
		info.CurrentTokens = msgTokens
	}

	// 从 provider 获取增长数据
	if provider != nil && provider.GrowthTracker != nil {
		provider.GrowthTracker.Record(iteration, info.CurrentTokens)
		info.GrowthRate = provider.GrowthTracker.GrowthRate()
		snapshots := provider.GrowthTracker.Snapshots()
		info.TokenHistory = make([]int, len(snapshots))
		for i, s := range snapshots {
			info.TokenHistory[i] = s.tokens
		}
	}

	// 提取最近5个工具名
	recentCount := 5
	if len(toolsUsed) < recentCount {
		recentCount = len(toolsUsed)
	}
	info.RecentTools = make([]string, recentCount)
	copy(info.RecentTools, toolsUsed[len(toolsUsed)-recentCount:])

	// 检测工具模式
	info.ToolPattern = DetectToolPattern(info.RecentTools)

	return info
}
