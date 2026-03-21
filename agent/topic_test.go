package agent

import (
	"testing"
	"time"

	"xbot/llm"
)

// helper: create user message
func userMsg(content string) llm.ChatMessage {
	return llm.NewUserMessage(content)
}

// helper: create assistant message
func assistantMsg(content string) llm.ChatMessage {
	return llm.NewAssistantMessage(content)
}

// helper: create messages for a topic (user+assistant pairs)
func topicPair(userContent, assistantContent string) []llm.ChatMessage {
	return []llm.ChatMessage{
		userMsg(userContent),
		assistantMsg(assistantContent),
	}
}

// TestSingleTopicNoPartition 单话题不触发分区
func TestSingleTopicNoPartition(t *testing.T) {
	d := NewTopicDetector()

	// 构造 12 条消息，全部关于同一个话题（Go programming）
	var messages []llm.ChatMessage
	for i := 0; i < 6; i++ {
		messages = append(messages, topicPair(
			"Tell me about Go programming language features like goroutines and channels",
			"Go has great concurrency support with goroutines and channels for parallel execution",
		)...)
	}

	segments, _ := d.Detect(messages)

	if len(segments) != 1 {
		t.Errorf("expected 1 segment for single topic, got %d", len(segments))
	}
	if !segments[0].IsCurrent {
		t.Error("expected single segment to be marked as current")
	}
	if segments[0].StartIdx != 0 || segments[0].EndIdx != len(messages) {
		t.Errorf("expected segment to cover all messages [0,%d), got [%d,%d)",
			len(messages), segments[0].StartIdx, segments[0].EndIdx)
	}
}

// TestTopicSwitchEnglish 英文话题切换检测
func TestTopicSwitchEnglish(t *testing.T) {
	d := TopicDetector{
		CosineThreshold: 0.3,
		MinSegmentSize:  3,
	}

	var messages []llm.ChatMessage
	// Topic 1: Python programming (6 messages = 3 turns)
	for i := 0; i < 3; i++ {
		messages = append(messages, topicPair(
			"How do I use Python decorators and classes in my application",
			"Python decorators use the @ syntax and classes are defined with the class keyword",
		)...)
	}
	// Topic 2: Cooking recipes (8 messages = 4 turns) — completely different vocabulary
	for i := 0; i < 4; i++ {
		messages = append(messages, topicPair(
			"What is a good recipe for baking chocolate cake with flour and sugar",
			"Mix flour sugar eggs butter and bake at 350 degrees for delicious chocolate cake",
		)...)
	}

	segments, _ := d.Detect(messages)

	if len(segments) < 2 {
		t.Errorf("expected at least 2 segments for topic switch, got %d", len(segments))
	}

	// Verify segments cover the full range without gaps
	if segments[0].StartIdx != 0 {
		t.Errorf("first segment should start at 0, got %d", segments[0].StartIdx)
	}
	last := segments[len(segments)-1]
	if last.EndIdx != len(messages) {
		t.Errorf("last segment should end at %d, got %d", len(messages), last.EndIdx)
	}

	// Verify continuity
	for i := 1; i < len(segments); i++ {
		if segments[i].StartIdx != segments[i-1].EndIdx {
			t.Errorf("gap between segments %d and %d: %d != %d",
				i-1, i, segments[i-1].EndIdx, segments[i].StartIdx)
		}
	}

	// Only the last segment should be current
	for i, seg := range segments {
		if i == len(segments)-1 && !seg.IsCurrent {
			t.Errorf("last segment should be current")
		}
		if i < len(segments)-1 && seg.IsCurrent {
			t.Errorf("non-last segment %d should not be current", i)
		}
	}
}

// TestTopicSwitchCJK 中文/CJK 话题切换检测
func TestTopicSwitchCJK(t *testing.T) {
	d := TopicDetector{
		CosineThreshold: 0.3,
		MinSegmentSize:  3,
	}

	var messages []llm.ChatMessage
	// 话题1：编程开发（6条 = 3轮）
	for i := 0; i < 3; i++ {
		messages = append(messages, topicPair(
			"请帮我写一个Python函数来处理数据结构和算法问题",
			"好的我来帮你实现这个函数使用递归和动态规划的方法解决",
		)...)
	}
	// 话题2：美食烹饪（8条 = 4轮）—— 完全不同的中文词汇
	for i := 0; i < 4; i++ {
		messages = append(messages, topicPair(
			"请问红烧肉怎么做需要哪些食材和调料",
			"红烧肉需要五花肉酱油冰糖生姜料酒小火慢炖两个小时",
		)...)
	}

	segments, _ := d.Detect(messages)

	if len(segments) < 2 {
		t.Errorf("expected at least 2 segments for CJK topic switch, got %d", len(segments))
	}
}

// TestShortHistoryNoTrigger 短历史不触发（<10条）
func TestShortHistoryNoTrigger(t *testing.T) {
	d := NewTopicDetector()

	// 只有 8 条消息，低于 DefaultMinHistory(10)
	messages := []llm.ChatMessage{
		userMsg("Tell me about Python"),
		assistantMsg("Python is a great language"),
		userMsg("What about decorators"),
		assistantMsg("Decorators modify function behavior"),
		userMsg("Now tell me about cooking"),
		assistantMsg("Cooking is a great hobby"),
		userMsg("How to bake a cake"),
		assistantMsg("Mix flour and sugar"),
	}

	segments, _ := d.Detect(messages)

	if len(segments) != 1 {
		t.Errorf("expected 1 segment for short history (<10), got %d", len(segments))
	}
}

// TestFragmentNoTrigger 碎片片段不触发（MinSegmentSize=3）
func TestFragmentNoTrigger(t *testing.T) {
	d := TopicDetector{
		CosineThreshold: 0.3,
		MinSegmentSize:  5, // 设为 5，使得话题切换后的短片段被合并
	}

	var messages []llm.ChatMessage
	// Topic 1: long topic (8 messages = 4 turns)
	for i := 0; i < 4; i++ {
		messages = append(messages, topicPair(
			"Tell me about machine learning neural networks deep learning algorithms",
			"Machine learning uses neural networks with deep learning algorithms for training",
		)...)
	}
	// Topic 2: very short topic (2 messages = 1 turn) — should be merged
	messages = append(messages, topicPair(
		"How about cooking recipes",
		"Here is a great recipe for you",
	)...)
	// Topic 1 continues (4 more messages = 2 turns)
	for i := 0; i < 2; i++ {
		messages = append(messages, topicPair(
			"Back to neural networks and deep learning optimization",
			"Yes let's continue with gradient descent optimization techniques",
		)...)
	}

	segments, _ := d.Detect(messages)

	// The short cooking fragment should be merged into adjacent segments
	for _, seg := range segments {
		if seg.MessageCount < 5 {
			t.Errorf("expected no segment with <5 messages, but found segment [%d,%d) with %d messages",
				seg.StartIdx, seg.EndIdx, seg.MessageCount)
		}
	}

	// Total coverage should be complete
	totalMsgs := 0
	for _, seg := range segments {
		totalMsgs += seg.MessageCount
	}
	if totalMsgs != len(messages) {
		t.Errorf("segments cover %d messages but input has %d", totalMsgs, len(messages))
	}
}

// TestLargeConversationPerformance 大会话性能（100条 < 50ms）
func TestLargeConversationPerformance(t *testing.T) {
	d := NewTopicDetector()

	// Generate 100 messages (50 turns, 2 topics alternating)
	var messages []llm.ChatMessage
	for i := 0; i < 25; i++ {
		messages = append(messages, topicPair(
			"Tell me about software engineering best practices and design patterns",
			"Design patterns like singleton factory and observer are fundamental to software engineering",
		)...)
	}
	for i := 0; i < 25; i++ {
		messages = append(messages, topicPair(
			"What are some healthy cooking recipes with vegetables and grains",
			"Try roasting vegetables with olive oil and serving over brown rice for a healthy meal",
		)...)
	}

	if len(messages) != 100 {
		t.Fatalf("expected 100 messages, got %d", len(messages))
	}

	start := time.Now()
	segments, _ := d.Detect(messages)
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Errorf("Detect took %v, expected < 50ms", elapsed)
	}

	// Basic sanity check
	if len(segments) < 1 {
		t.Error("expected at least 1 segment")
	}

	// Verify full coverage
	totalMsgs := 0
	for _, seg := range segments {
		totalMsgs += seg.MessageCount
	}
	if totalMsgs != len(messages) {
		t.Errorf("segments cover %d messages but input has %d", totalMsgs, len(messages))
	}
}

// TestExtractKeywords 揋试关键词提取
func TestExtractKeywords(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		minCount int // expect at least this many keywords
		sampleKw string
	}{
		{
			name:     "English bigram",
			text:     "Go programming language is great for building software applications",
			minCount: 1,
			sampleKw: "go programming",
		},
		{
			name:     "CJK bigram",
			text:     "机器学习和深度学习是人工智能的重要分支",
			minCount: 1,
			sampleKw: "机器", // bigram: 机器, 器学, 学习, 习和, 和深, 深度, 度学, 学习, 习是, 是人, 人工, 工智, 智能, 能的, 的重, 重要, 要分, 分支
		},
		{
			name:     "Mixed content",
			text:     "使用Python编写机器学习代码",
			minCount: 1,
			sampleKw: "机器", // CJK bigrams: 使用, 编程, 程语, 语言, 言来, 来开, 开发, 发应, 应用, 用程, 程序
		},
		{
			name:     "Short CJK",
			text:     "你好",
			minCount: 0, // 只有2个字符，产生1个bigram
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kw := extractKeywords(tt.text)
			if len(kw) < tt.minCount {
				t.Errorf("expected at least %d keywords, got %d: %v", tt.minCount, len(kw), kw)
			}
			if tt.sampleKw != "" {
				found := false
				for _, k := range kw {
					if k == tt.sampleKw {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected keyword %q not found in %v", tt.sampleKw, kw)
				}
			}
		})
	}
}

// TestExtractCJKChars 测试 CJK 字符提取
func TestExtractCJKChars(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected int // expected number of bigrams
	}{
		{
			name:     "Pure Chinese",
			text:     "人工智能技术发展迅速",
			expected: 9, // 10 chars → 9 bigrams
		},
		{
			name:     "Mixed with English",
			text:     "使用Python编程语言来开发应用程序",
			expected: 11, // CJK seqs: 使用(2→1), 编程语言来开发应用程序(9→8), wait... 实际输出11: 使用, 编程, 程语, 语言, 言来, 来开, 开发, 发应, 应用, 用程, 程序
		},
		{
			name:     "Single CJK char",
			text:     "好",
			expected: 0, // need at least 2 consecutive CJK chars
		},
		{
			name:     "Two CJK chars",
			text:     "你好",
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bigrams := extractCJKChars(tt.text)
			if len(bigrams) != tt.expected {
				t.Errorf("expected %d bigrams, got %d: %v", tt.expected, len(bigrams), bigrams)
			}
		})
	}
}

// TestCosineSimilarity 测试余弦相似度
func TestCosineSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a        []string
		b        []string
		expected float64 // approximate
	}{
		{
			name:     "Identical",
			a:        []string{"hello world", "foo bar"},
			b:        []string{"hello world", "foo bar"},
			expected: 1.0,
		},
		{
			name:     "Empty both",
			a:        []string{},
			b:        []string{},
			expected: 1.0,
		},
		{
			name:     "Empty one side",
			a:        []string{"hello world"},
			b:        []string{},
			expected: 0.0,
		},
		{
			name:     "No overlap",
			a:        []string{"hello world", "foo bar"},
			b:        []string{"cat dog", "bird fish"},
			expected: 0.0,
		},
		{
			name:     "Partial overlap",
			a:        []string{"hello world", "foo bar", "cat dog"},
			b:        []string{"hello world", "foo bar", "bird fish"},
			expected: 0.67, // 2 out of 5 unique terms overlap; cos = 2/(√3*√3) ≈ 0.667
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sim := cosineSimilarity(tt.a, tt.b)
			diff := sim - tt.expected
			if diff < 0 {
				diff = -diff
			}
			if diff > 0.05 { // allow 5% tolerance
				t.Errorf("expected ~%.2f, got %.4f", tt.expected, sim)
			}
		})
	}
}

// TestGroupIntoTurns 测试轮次分组
func TestGroupIntoTurns(t *testing.T) {
	messages := []llm.ChatMessage{
		userMsg("hello"),
		assistantMsg("hi"),
		userMsg("question 1"),
		assistantMsg("answer 1"),
		userMsg("question 2"),
		assistantMsg("answer 2"),
	}

	turns := groupIntoTurns(messages)

	if len(turns) != 3 {
		t.Errorf("expected 3 turns, got %d", len(turns))
	}

	// Each turn should have 2 messages
	for i, turn := range turns {
		if len(turn) != 2 {
			t.Errorf("turn %d: expected 2 messages, got %d", i, len(turn))
		}
	}
}

// TestMergeShortSegments 测试短片段合并
func TestMergeShortSegments(t *testing.T) {
	segments := []TopicSegment{
		{StartIdx: 0, EndIdx: 6, MessageCount: 6},
		{StartIdx: 6, EndIdx: 8, MessageCount: 2},  // short
		{StartIdx: 8, EndIdx: 10, MessageCount: 2}, // short
		{StartIdx: 10, EndIdx: 18, MessageCount: 8},
	}

	merged := mergeShortSegments(segments, 3)

	if len(merged) != 2 {
		t.Errorf("expected 2 segments after merge, got %d", len(merged))
	}

	if merged[0].EndIdx != 10 {
		t.Errorf("expected first merged segment to end at 10, got %d", merged[0].EndIdx)
	}
	if merged[1].StartIdx != 10 {
		t.Errorf("expected second segment to start at 10, got %d", merged[1].StartIdx)
	}
}

// TestNewTopicDetectorDefaults 测试默认参数
func TestNewTopicDetectorDefaults(t *testing.T) {
	d := NewTopicDetector()

	if d.CosineThreshold != 0.3 {
		t.Errorf("expected CosineThreshold 0.3, got %.2f", d.CosineThreshold)
	}
	if d.MinSegmentSize != 3 {
		t.Errorf("expected MinSegmentSize 3, got %d", d.MinSegmentSize)
	}
}

// TestDetectEmptyMessages 测试空消息列表
func TestDetectEmptyMessages(t *testing.T) {
	d := NewTopicDetector()
	segments, _ := d.Detect(nil)

	if len(segments) != 1 {
		t.Errorf("expected 1 segment for empty input, got %d", len(segments))
	}
	if segments[0].MessageCount != 0 {
		t.Errorf("expected 0 messages, got %d", segments[0].MessageCount)
	}
}
