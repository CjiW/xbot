package agent

import (
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// RecallTracker 跟踪 LLM 的 recall 行为，检测摘要遗漏。
// 当同一内容被召回 ≥3 次时，说明压缩摘要遗漏了关键信息，需要精化回写。
type RecallTracker struct {
	mu             sync.Mutex
	recallCounts   map[string]int  // contentHash → recall count
	hotItems       []RecallHotItem // 高频召回项（按次数排序）
	maxHotItems    int             // 最多保留 50 个
	lastRefineIter atomic.Int32    // 上次精化时的迭代号（atomic 保证并发安全）
}

// RecallHotItem 表示一个高频召回的 item。
type RecallHotItem struct {
	Hash      string
	Content   string // 截断到 200 chars 的内容摘要
	Count     int
	FirstSeen time.Time
	LastSeen  time.Time
}

const (
	// defaultMaxHotItems 默认最大高频项数量
	defaultMaxHotItems = 50
	// refineThreshold 触发精化的最低召回次数
	refineThreshold = 3
	// refineCooldownIterations 精化冷却期（迭代数）
	refineCooldownIterations = 10
	// contentPreviewMaxLen 内容预览最大长度
	contentPreviewMaxLen = 200
)

// NewRecallTracker 创建 RecallTracker。
func NewRecallTracker() *RecallTracker {
	return &RecallTracker{
		recallCounts: make(map[string]int),
		maxHotItems:  defaultMaxHotItems,
	}
}

// computeHash 使用内容前 200 字符的 FNV-32 hash（简单去重，不需精确）。
func computeHash(content string) uint32 {
	preview := content
	runes := []rune(preview)
	if len(runes) > contentPreviewMaxLen {
		preview = string(runes[:contentPreviewMaxLen])
	}
	h := fnv.New32a()
	h.Write([]byte(preview))
	return h.Sum32()
}

// RecordRecall 记录一次 recall 事件（由 engine.go 在 offload_recall/recall_masked 工具调用后触发）。
// itemID: offload ID 或 masked ID
// contentType: "offload" 或 "masked"
// contentPreview: 召回内容的预览（用于 hash + 显示）
func (t *RecallTracker) RecordRecall(itemID string, contentType string, contentPreview string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	hash := computeHash(itemID + ":" + contentPreview)
	hashStr := contentType + ":" + uint32ToStr(hash)

	count := t.recallCounts[hashStr] + 1
	t.recallCounts[hashStr] = count

	now := time.Now()

	// 查找已有 hotItem 并更新
	found := false
	for i := range t.hotItems {
		if t.hotItems[i].Hash == hashStr {
			t.hotItems[i].Count = count
			t.hotItems[i].LastSeen = now
			if len(contentPreview) > contentPreviewMaxLen {
				t.hotItems[i].Content = string([]rune(contentPreview)[:contentPreviewMaxLen])
			} else {
				t.hotItems[i].Content = contentPreview
			}
			found = true
			break
		}
	}

	if !found {
		preview := contentPreview
		runes := []rune(preview)
		if len(runes) > contentPreviewMaxLen {
			preview = string(runes[:contentPreviewMaxLen])
		}
		t.hotItems = append(t.hotItems, RecallHotItem{
			Hash:      hashStr,
			Content:   preview,
			Count:     count,
			FirstSeen: now,
			LastSeen:  now,
		})
	}

	// 超限则淘汰最低频的项
	if len(t.hotItems) > t.maxHotItems {
		sort.Slice(t.hotItems, func(i, j int) bool {
			return t.hotItems[i].Count > t.hotItems[j].Count
		})
		t.hotItems = t.hotItems[:t.maxHotItems]
	}

	// 保持排序（按 count 降序）
	sort.Slice(t.hotItems, func(i, j int) bool {
		return t.hotItems[i].Count > t.hotItems[j].Count
	})
}

// GetHotItems 获取高频召回项（count >= threshold）。
func (t *RecallTracker) GetHotItems(threshold int) []RecallHotItem {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	var result []RecallHotItem
	for _, item := range t.hotItems {
		if item.Count >= threshold {
			result = append(result, item)
		}
	}
	return result
}

// ShouldRefine 判断是否需要精化摘要。
// 条件：存在 count >= 3 的高频项，且距上次精化超过 10 轮迭代。
func (t *RecallTracker) ShouldRefine(currentIteration int) bool {
	if t == nil {
		return false
	}
	// 冷却期内不触发
	if currentIteration-int(t.lastRefineIter.Load()) < refineCooldownIterations {
		return false
	}

	return len(t.GetHotItems(refineThreshold)) != 0
}

// MarkRefine 标记一次精化已完成（更新冷却计数器）。
func (t *RecallTracker) MarkRefine(currentIteration int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastRefineIter.Store(int32(currentIteration))
	// 清零已精化项的计数（避免重复触发）
	for i := range t.hotItems {
		if t.hotItems[i].Count >= refineThreshold {
			t.recallCounts[t.hotItems[i].Hash] = 0
			t.hotItems[i].Count = 0
		}
	}
	// 移除计数为 0 的项
	var filtered []RecallHotItem
	for _, item := range t.hotItems {
		if item.Count > 0 {
			filtered = append(filtered, item)
		}
	}
	t.hotItems = filtered
}

// GenerateRefinePrompt 生成精化 prompt，包含高频召回的关键信息。
// 返回一个 user message，告知 LLM 将这些关键信息融入当前上下文摘要中。
func (t *RecallTracker) GenerateRefinePrompt() string {
	if t == nil {
		return ""
	}

	hotItems := t.GetHotItems(refineThreshold)
	if len(hotItems) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("[系统提示：以下信息被频繁召回（≥3次），说明压缩摘要中遗漏了关键内容。请在后续工作中牢记这些信息，避免重复召回。]\n\n")

	for i, item := range hotItems {
		if i >= 10 { // 最多展示 10 项
			break
		}
		sb.WriteString(fmtRefineItem(item))
	}

	return sb.String()
}

// fmtRefinePrompt 格式化单个精化条目
func fmtRefineItem(item RecallHotItem) string {
	return fmt.Sprintf("- [召回 %d 次] %s\n", item.Count, item.Content)
}

// uint32ToStr 将 uint32 转为 hex 字符串
func uint32ToStr(h uint32) string {
	return strings.ToUpper(uint32ToHex(h))
}

func uint32ToHex(h uint32) string {
	const hex = "0123456789abcdef"
	b := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		b[i] = hex[h&0xf]
		h >>= 4
	}
	return string(b)
}
