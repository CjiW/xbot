package agent

import (
	"math"
	"strings"
	"unicode"

	"github.com/google/uuid"

	"xbot/llm"
)

// TopicDetector 话题分区检测器（无状态纯算法对象，值接收者，天然线程安全）。
type TopicDetector struct {
	CosineThreshold float64 // 余弦相似度阈值，默认 0.3
	MinSegmentSize  int     // 最小话题片段大小（消息数），默认 3
}

// TopicSegment 检测到的话题分区。
type TopicSegment struct {
	ID           string   // 分区唯一标识
	StartIdx     int      // 起始消息索引（含）
	EndIdx       int      // 结束消息索引（不含，左闭右开）
	MessageCount int      // 片段内消息条数
	Keywords     []string // 该片段的关键词
	Summary      string   // 话题摘要（可选，当前为空）
	IsCurrent    bool     // 是否为当前（最新）话题
}

// NewTopicDetector 使用默认参数创建 TopicDetector。
func NewTopicDetector() TopicDetector {
	return TopicDetector{
		CosineThreshold: 0.3,
		MinSegmentSize:  3,
	}
}

// DefaultMinHistory 最小历史消息数，低于此值不触发话题分区。
const DefaultMinHistory = 10

// Detect 检测消息列表中的话题边界，返回话题分区列表。
// 防误判层：
//  1. 最小历史 > DefaultMinHistory（10条）
//  2. 最小片段 >= MinSegmentSize（3条）
//  3. 余弦相似度 < CosineThreshold（0.3）才切分
//  4. 新话题片段消息数 <= 2 时合并回上一个话题（避免碎片）
//  5. 检测失败时降级返回单个分区（全量保留）
func (d TopicDetector) Detect(messages []llm.ChatMessage) []TopicSegment {
	// Layer 1: 最小历史检查
	if len(messages) < DefaultMinHistory {
		return []TopicSegment{{
			ID:           uuid.New().String(),
			StartIdx:     0,
			EndIdx:       len(messages),
			MessageCount: len(messages),
			IsCurrent:    true,
		}}
	}

	// Layer 5: 安全兜底，Detect 内部 panic 时降级为单个分区
	defer func() {
		recover() // 由调用方通过返回值判断
	}()

	// 按对话轮次分组
	turns := groupIntoTurns(messages)
	if len(turns) < 2 {
		return []TopicSegment{{
			ID:           uuid.New().String(),
			StartIdx:     0,
			EndIdx:       len(messages),
			MessageCount: len(messages),
			IsCurrent:    true,
		}}
	}

	// 为每个轮次提取关键词
	turnKeywords := make([][]string, len(turns))
	for i, turn := range turns {
		var text strings.Builder
		for _, msg := range turn {
			if msg.Content != "" {
				text.WriteString(msg.Content)
				text.WriteString(" ")
			}
		}
		turnKeywords[i] = extractKeywords(text.String())
	}

	// 计算相邻轮次的相似度，找话题边界
	boundaries := make([]int, 0) // 轮次边界索引
	for i := 1; i < len(turns); i++ {
		sim := cosineSimilarity(turnKeywords[i-1], turnKeywords[i])
		if sim < d.CosineThreshold {
			boundaries = append(boundaries, i)
		}
	}

	// 将轮次边界映射回消息索引
	turnOffsets := make([]int, len(turns)+1)
	offset := 0
	for i, turn := range turns {
		turnOffsets[i] = offset
		offset += len(turn)
	}
	turnOffsets[len(turns)] = offset

	msgBoundaries := make([]int, 0, len(boundaries))
	for _, b := range boundaries {
		msgBoundaries = append(msgBoundaries, turnOffsets[b])
	}

	// Layer 2+4: 按边界切分，合并过短片段
	segments := splitByBoundaries(messages, msgBoundaries)
	segments = mergeShortSegments(segments, d.MinSegmentSize)

	// 为每个片段生成关键词（合并片段内所有轮次的关键词）
	for i := range segments {
		seg := &segments[i]
		kwSet := make(map[string]bool)
		for _, b := range boundaries {
			turnStartMsg := turnOffsets[b]
			turnEndMsg := turnOffsets[b+1]
			if turnStartMsg >= seg.StartIdx && turnEndMsg <= seg.EndIdx {
				bIdx := b
				if bIdx < len(turnKeywords) {
					for _, kw := range turnKeywords[bIdx] {
						kwSet[kw] = true
					}
				}
			}
		}
		// 也包含上一个边界到最后一个边界之间的所有轮次关键词
		for j, to := range turnOffsets {
			if j >= len(turns) {
				break
			}
			turnEnd := turnOffsets[j+1]
			if to >= seg.StartIdx && turnEnd <= seg.EndIdx {
				if j < len(turnKeywords) {
					for _, kw := range turnKeywords[j] {
						kwSet[kw] = true
					}
				}
			}
		}
		keywords := make([]string, 0, len(kwSet))
		for kw := range kwSet {
			keywords = append(keywords, kw)
		}
		seg.Keywords = keywords
	}

	// 标记最后一个片段为当前话题
	if len(segments) > 0 {
		segments[len(segments)-1].IsCurrent = true
	}

	return segments
}

// splitByBoundaries 按消息边界索引切分为话题片段。
func splitByBoundaries(messages []llm.ChatMessage, boundaries []int) []TopicSegment {
	if len(boundaries) == 0 {
		return []TopicSegment{{
			ID:           uuid.New().String(),
			StartIdx:     0,
			EndIdx:       len(messages),
			MessageCount: len(messages),
			IsCurrent:    true,
		}}
	}

	segments := make([]TopicSegment, 0, len(boundaries)+1)
	start := 0
	for _, b := range boundaries {
		if b <= start {
			continue // 跳过无效边界
		}
		segments = append(segments, TopicSegment{
			ID:           uuid.New().String(),
			StartIdx:     start,
			EndIdx:       b,
			MessageCount: b - start,
		})
		start = b
	}
	// 最后一个片段
	segments = append(segments, TopicSegment{
		ID:           uuid.New().String(),
		StartIdx:     start,
		EndIdx:       len(messages),
		MessageCount: len(messages) - start,
		IsCurrent:    true,
	})

	return segments
}

// mergeShortSegments 合并过短的话题片段（消息数 < minSize）到相邻片段。
func mergeShortSegments(segments []TopicSegment, minSize int) []TopicSegment {
	if len(segments) <= 1 || minSize <= 0 {
		return segments
	}

	merged := make([]TopicSegment, 0, len(segments))
	for i := 0; i < len(segments); i++ {
		seg := segments[i]
		// 如果当前片段过短，合并到上一个片段
		if seg.MessageCount < minSize && len(merged) > 0 {
			prev := &merged[len(merged)-1]
			prev.EndIdx = seg.EndIdx
			prev.MessageCount = prev.EndIdx - prev.StartIdx
			prev.IsCurrent = seg.IsCurrent
			continue
		}
		merged = append(merged, seg)
	}
	return merged
}

// groupIntoTurns 按对话轮次分组消息。
// 一个轮次 = 连续的 user 消息（可能含 assistant 回复和 tool 结果）。
// 以 user 消息作为新轮次的起点。
func groupIntoTurns(messages []llm.ChatMessage) [][]llm.ChatMessage {
	if len(messages) == 0 {
		return nil
	}

	var turns [][]llm.ChatMessage
	var currentTurn []llm.ChatMessage

	for i, msg := range messages {
		// user 消息开始新轮次（非首条消息时）
		if msg.Role == "user" && len(currentTurn) > 0 {
			turns = append(turns, currentTurn)
			currentTurn = nil
		}
		currentTurn = append(currentTurn, messages[i])
	}

	if len(currentTurn) > 0 {
		turns = append(turns, currentTurn)
	}

	return turns
}

// extractKeywords 从文本中提取轻量关键词（中英文 bigram）。
// 英文：小写化后按空格分词，提取 bigram。
// 中文：提取 CJK 字符 bigram。
func extractKeywords(text string) []string {
	var keywords []string

	// 提取英文 bigram
	words := strings.Fields(text)
	for i := 0; i < len(words)-1; i++ {
		w1 := strings.ToLower(normalizeWord(words[i]))
		w2 := strings.ToLower(normalizeWord(words[i+1]))
		if w1 != "" && w2 != "" && isAlphaWord(w1) && isAlphaWord(w2) {
			keywords = append(keywords, w1+" "+w2)
		}
	}

	// 提取 CJK bigram
	cjkBigrams := extractCJKChars(text)
	keywords = append(keywords, cjkBigrams...)

	return keywords
}

// extractCJKChars 提取连续 CJK 字符序列的 bigram。
func extractCJKChars(text string) []string {
	runes := []rune(text)
	var cjkSeqs []string // 收集连续 CJK 字符序列

	start := -1
	for i, r := range runes {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hangul, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hiragana, r) {
			if start == -1 {
				start = i
			}
		} else {
			if start != -1 && i-start >= 2 {
				cjkSeqs = append(cjkSeqs, string(runes[start:i]))
			}
			start = -1
		}
	}
	// 处理末尾序列
	if start != -1 && len(runes)-start >= 2 {
		cjkSeqs = append(cjkSeqs, string(runes[start:]))
	}

	var bigrams []string
	for _, seq := range cjkSeqs {
		seqRunes := []rune(seq)
		for i := 0; i < len(seqRunes)-1; i++ {
			bigrams = append(bigrams, string(seqRunes[i:i+2]))
		}
	}
	return bigrams
}

// normalizeWord 去除标点符号，保留字母数字。
func normalizeWord(word string) string {
	var b strings.Builder
	b.Grow(len(word))
	for _, r := range word {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isAlphaWord 判断是否为有效英文单词（至少1个字母）。
func isAlphaWord(word string) bool {
	for _, r := range word {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// cosineSimilarity 基于关键词频率向量的余弦相似度。
func cosineSimilarity(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0 // 两个空集视为完全相似
	}
	if len(a) == 0 || len(b) == 0 {
		return 0.0
	}

	// 构建频率向量
	freqA := buildFreqMap(a)
	freqB := buildFreqMap(b)

	// 收集所有唯一词
	allTerms := make(map[string]bool)
	for term := range freqA {
		allTerms[term] = true
	}
	for term := range freqB {
		allTerms[term] = true
	}

	// 计算点积和模
	var dotProduct, normA, normB float64
	for term := range allTerms {
		fa := freqA[term]
		fb := freqB[term]
		dotProduct += fa * fb
		normA += fa * fa
		normB += fb * fb
	}

	if normA == 0 || normB == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(normA) * math.Sqrt(normB))
}

// buildFreqMap 构建词频映射。
func buildFreqMap(keywords []string) map[string]float64 {
	freq := make(map[string]float64, len(keywords))
	for _, kw := range keywords {
		freq[kw]++
	}
	return freq
}
