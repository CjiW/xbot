package agent

import (
	"strings"
	"testing"
)

func TestNewRecallTracker(t *testing.T) {
	tracker := NewRecallTracker()
	if tracker == nil {
		t.Fatal("NewRecallTracker returned nil")
	}
	if len(tracker.hotItems) != 0 {
		t.Errorf("expected 0 hot items, got %d", len(tracker.hotItems))
	}
}

func TestRecordRecall_SingleItem(t *testing.T) {
	tracker := NewRecallTracker()
	tracker.RecordRecall("ol_abc123", "offload", "This is a test file content")

	items := tracker.GetHotItems(1)
	if len(items) != 1 {
		t.Fatalf("expected 1 hot item, got %d", len(items))
	}
	if items[0].Count != 1 {
		t.Errorf("expected count 1, got %d", items[0].Count)
	}
	if items[0].Hash == "" {
		t.Error("expected non-empty hash")
	}
}

func TestRecordRecall_SameItem(t *testing.T) {
	tracker := NewRecallTracker()

	// 同一 item 召回 3 次
	for i := 0; i < 3; i++ {
		tracker.RecordRecall("ol_abc123", "offload", "This is a test file content")
	}

	items := tracker.GetHotItems(3)
	if len(items) != 1 {
		t.Fatalf("expected 1 hot item with count >= 3, got %d", len(items))
	}
	if items[0].Count != 3 {
		t.Errorf("expected count 3, got %d", items[0].Count)
	}
}

func TestRecordRecall_DifferentItems(t *testing.T) {
	tracker := NewRecallTracker()

	// 不同 item 各召回 1 次
	tracker.RecordRecall("ol_abc123", "offload", "File A content")
	tracker.RecordRecall("ol_def456", "offload", "File B content")
	tracker.RecordRecall("mk_xyz789", "masked", "Masked result content")

	items := tracker.GetHotItems(1)
	if len(items) != 3 {
		t.Fatalf("expected 3 hot items, got %d", len(items))
	}
}

func TestRecordRecall_DifferentTypes(t *testing.T) {
	tracker := NewRecallTracker()

	// 相同 ID + content 但不同 type 应产生不同 hash
	tracker.RecordRecall("ol_abc", "offload", "same content")
	tracker.RecordRecall("ol_abc", "masked", "same content")

	items := tracker.GetHotItems(1)
	if len(items) != 2 {
		t.Fatalf("expected 2 hot items (different types), got %d", len(items))
	}
}

func TestRecordRecall_ContentTruncation(t *testing.T) {
	tracker := NewRecallTracker()

	longContent := strings.Repeat("x", 500)
	tracker.RecordRecall("ol_abc", "offload", longContent)

	items := tracker.GetHotItems(1)
	if len(items) != 1 {
		t.Fatalf("expected 1 hot item, got %d", len(items))
	}
	if len([]rune(items[0].Content)) > 200 {
		t.Errorf("expected content truncated to 200 runes, got %d", len([]rune(items[0].Content)))
	}
}

func TestRecordRecall_MaxHotItems(t *testing.T) {
	tracker := NewRecallTracker()
	tracker.maxHotItems = 5

	for i := 0; i < 10; i++ {
		tracker.RecordRecall("ol_item"+string(rune('A'+i)), "offload", "content")
	}

	items := tracker.GetHotItems(1)
	if len(items) > 5 {
		t.Errorf("expected at most 5 hot items, got %d", len(items))
	}
}

func TestGetHotItems_Threshold(t *testing.T) {
	tracker := NewRecallTracker()

	// item A: 3 次, item B: 2 次, item C: 1 次
	for i := 0; i < 3; i++ {
		tracker.RecordRecall("ol_aaa", "offload", "content A")
	}
	for i := 0; i < 2; i++ {
		tracker.RecordRecall("ol_bbb", "offload", "content B")
	}
	tracker.RecordRecall("ol_ccc", "offload", "content C")

	items3 := tracker.GetHotItems(3)
	if len(items3) != 1 {
		t.Errorf("expected 1 item with count >= 3, got %d", len(items3))
	}

	items2 := tracker.GetHotItems(2)
	if len(items2) != 2 {
		t.Errorf("expected 2 items with count >= 2, got %d", len(items2))
	}

	items1 := tracker.GetHotItems(1)
	if len(items1) != 3 {
		t.Errorf("expected 3 items with count >= 1, got %d", len(items1))
	}
}

func TestShouldRefine(t *testing.T) {
	tracker := NewRecallTracker()

	// 未达到阈值 → false
	if tracker.ShouldRefine(5) {
		t.Error("expected ShouldRefine=false when no hot items")
	}

	// 3 次召回 → true
	for i := 0; i < 3; i++ {
		tracker.RecordRecall("ol_aaa", "offload", "important content")
	}
	// 需要超出冷却期才能触发
	if !tracker.ShouldRefine(15) {
		t.Error("expected ShouldRefine=true with 3 recalls and iteration 15")
	}

	// 冷却期内 → false
	tracker.lastRefineIter = 10
	if tracker.ShouldRefine(5) {
		t.Error("expected ShouldRefine=false during cooldown")
	}
}

func TestShouldRefine_Cooldown(t *testing.T) {
	tracker := NewRecallTracker()

	for i := 0; i < 3; i++ {
		tracker.RecordRecall("ol_aaa", "offload", "important content")
	}

	// 第一次精化后，冷却期内不应再次触发
	tracker.MarkRefine(10)

	if tracker.ShouldRefine(15) {
		t.Error("expected ShouldRefine=false within cooldown (10 iterations)")
	}

	// 冷却期结束后应可以触发
	for i := 0; i < 3; i++ {
		tracker.RecordRecall("ol_bbb", "offload", "another important content")
	}

	if !tracker.ShouldRefine(25) {
		t.Error("expected ShouldRefine=true after cooldown expired")
	}
}

func TestMarkRefine(t *testing.T) {
	tracker := NewRecallTracker()

	for i := 0; i < 3; i++ {
		tracker.RecordRecall("ol_aaa", "offload", "important content")
	}

	// 精化后，高频项应被清零
	tracker.MarkRefine(10)

	items := tracker.GetHotItems(3)
	if len(items) != 0 {
		t.Errorf("expected 0 hot items after MarkRefine, got %d", len(items))
	}

	// 冷却期检查
	if tracker.ShouldRefine(12) {
		t.Error("expected ShouldRefine=false after MarkRefine")
	}
}

func TestGenerateRefinePrompt(t *testing.T) {
	tracker := NewRecallTracker()

	// 无高频项 → 空 prompt
	prompt := tracker.GenerateRefinePrompt()
	if prompt != "" {
		t.Errorf("expected empty prompt when no hot items, got: %q", prompt)
	}

	// 添加高频项
	for i := 0; i < 3; i++ {
		tracker.RecordRecall("ol_aaa", "offload", "config.yaml: port 8080, host localhost")
	}
	for i := 0; i < 5; i++ {
		tracker.RecordRecall("ol_bbb", "offload", "database password is secret123")
	}

	prompt = tracker.GenerateRefinePrompt()
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}

	if !strings.Contains(prompt, "系统提示") {
		t.Error("prompt should contain system hint")
	}
	if !strings.Contains(prompt, "config.yaml") {
		t.Error("prompt should contain content from item A")
	}
	if !strings.Contains(prompt, "secret123") {
		t.Error("prompt should contain content from item B")
	}
	if !strings.Contains(prompt, "召回 5 次") {
		t.Error("prompt should show count 5 for item B")
	}
}

func TestRecordRecall_NilTracker(t *testing.T) {
	var tracker *RecallTracker
	// 不应 panic
	tracker.RecordRecall("ol_aaa", "offload", "content")
	if tracker.ShouldRefine(10) {
		t.Error("nil tracker should not trigger refine")
	}
	if len(tracker.GetHotItems(1)) != 0 {
		t.Error("nil tracker should return empty items")
	}
	if tracker.GenerateRefinePrompt() != "" {
		t.Error("nil tracker should return empty prompt")
	}
}

func TestComputeHash(t *testing.T) {
	// 相同内容 → 相同 hash
	h1 := computeHash("hello world")
	h2 := computeHash("hello world")
	if h1 != h2 {
		t.Error("same content should produce same hash")
	}

	// 不同内容 → 不同 hash（大概率）
	h3 := computeHash("different content")
	if h1 == h3 {
		t.Error("different content should produce different hash")
	}

	// 长内容截断到 200 rune 后 hash
	short := strings.Repeat("a", 200)
	long := strings.Repeat("a", 300)
	h4 := computeHash(short)
	h5 := computeHash(long)
	if h4 != h5 {
		t.Error("content beyond 200 runes should be truncated before hashing")
	}
}
