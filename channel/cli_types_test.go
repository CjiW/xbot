// cli_types_test.go — Unit tests for truncateToWidth and hardWrapRunes.
//
// These tests verify that placeholder text is correctly truncated on narrow
// terminals and that CJK-aware hard wrapping works at character boundaries.

package channel

import (
	"testing"

	"github.com/mattn/go-runewidth"
)

// ---------------------------------------------------------------------------
// truncateToWidth
// ---------------------------------------------------------------------------

func TestTruncateToWidth_ShortString(t *testing.T) {
	got := truncateToWidth("hello", 10)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTruncateToWidth_ExactFit(t *testing.T) {
	got := truncateToWidth("hello", 5)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestTruncateToWidth_ASCII(t *testing.T) {
	got := truncateToWidth("hello world", 8)
	// "hello" = 5, "..." = 3, target = 5, so "hello..." = 8 cols
	if got != "hello..." {
		t.Errorf("expected %q, got %q", "hello...", got)
	}
	if runewidth.StringWidth(got) != 8 {
		t.Errorf("expected width 8, got %d", runewidth.StringWidth(got))
	}
}

func TestTruncateToWidth_CJK(t *testing.T) {
	// "你好世界" = 8 display columns (each CJK char = 2 cols)
	got := truncateToWidth("你好世界", 8)
	if got != "你好世界" {
		t.Errorf("expected %q, got %q", "你好世界", got)
	}
}

func TestTruncateToWidth_CJKTruncated(t *testing.T) {
	// "你好世界" = 8 cols, truncate to 6 → target = 6-3 = 3
	// 你(2) fits (2<=3), 好(2) → 4>3, so return "你..."
	got := truncateToWidth("你好世界", 6)
	if got != "你..." {
		t.Errorf("expected %q, got %q", "你...", got)
	}
	if w := runewidth.StringWidth(got); w > 6 {
		t.Errorf("expected width ≤ 6, got %d", w)
	}
}

func TestTruncateToWidth_CJKMixedASCII(t *testing.T) {
	// Typical placeholder on a very narrow terminal (width=12).
	got := truncateToWidth("Enter 发送 · Ctrl+J 换行 · /help", 12)
	if w := runewidth.StringWidth(got); w > 12 {
		t.Errorf("expected width ≤ 12, got %d for %q", w, got)
	}
	if got == "Enter 发送 · Ctrl+J 换行 · /help" {
		t.Error("expected truncation, got full string")
	}
}

func TestTruncateToWidth_VeryNarrow(t *testing.T) {
	// maxWidth = 2, ellipsis = 3, target = -1 → returns "..."[:2] = ".."
	got := truncateToWidth("hello", 2)
	if got != ".." {
		t.Errorf("expected %q, got %q", "..", got)
	}
}

func TestTruncateToWidth_WidthOne(t *testing.T) {
	got := truncateToWidth("hello", 1)
	if got != "." {
		t.Errorf("expected %q, got %q", ".", got)
	}
}

func TestTruncateToWidth_EmptyString(t *testing.T) {
	got := truncateToWidth("", 10)
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestTruncateToWidth_PlaceholderNarrowTerminal(t *testing.T) {
	// Simulates the real placeholder at various narrow terminal widths.
	ph := "Enter 发送 · Ctrl+J 换行 · /help"
	for _, tw := range []int{10, 14, 18, 22, 28, 40} {
		got := truncateToWidth(ph, tw)
		w := runewidth.StringWidth(got)
		if w > tw {
			t.Errorf("width=%d: truncated placeholder width %d exceeds %d", tw, w, tw)
		}
	}
}

// ---------------------------------------------------------------------------
// hardWrapRunes
// ---------------------------------------------------------------------------

func TestHardWrapRunes_ShortLine(t *testing.T) {
	got := hardWrapRunes("hello", 10)
	if got != "hello" {
		t.Errorf("expected %q, got %q", "hello", got)
	}
}

func TestHardWrapRunes_ASCIIWrap(t *testing.T) {
	got := hardWrapRunes("abcdefghij", 5)
	expected := "abcde\nfghij"
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestHardWrapRunes_CJKWrap(t *testing.T) {
	// "你好世界你好" = 12 cols, width=6 → 2 lines of 6 cols each
	got := hardWrapRunes("你好世界你好", 6)
	lines := splitLines(got)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	for i, line := range lines {
		w := runewidth.StringWidth(line)
		if w != 6 {
			t.Errorf("line %d: expected width 6, got %d (%q)", i, w, line)
		}
	}
}

func TestHardWrapRunes_CJKWithSpaces_NoWordWrap(t *testing.T) {
	// "你好abc 你好abc" — space should NOT be a wrap point.
	// 你(2)+好(2)+a(1)+b(1)+c(1)+ (1)+你(2) = 10 cols → fills exactly to width 10
	// 好(2) would make 12 > 10 → wrap
	input := "你好abc 你好abc"
	got := hardWrapRunes(input, 10)
	lines := splitLines(got)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	w1 := runewidth.StringWidth(lines[0])
	if w1 != 10 {
		t.Errorf("line 1: expected width 10 (filled to boundary), got %d (%q)", w1, lines[0])
	}
	// Space must stay on line 1 — no word-wrap at space
	if runewidth.StringWidth(lines[0]) < 10 && lines[0] == "你好abc" {
		t.Errorf("line 1 wrapped at space (word-wrap), expected hard-wrap: %q", lines[0])
	}
}

func TestHardWrapRunes_CJKWithMultipleSpaces(t *testing.T) {
	// "你好 世界 你好" = 2+2+1+2+1+1+2+2 = 13 cols
	// width = 6: 你(2)+好(2)+ (1) = 5, 世(2) → 7>6 wrap
	input := "你好 世界 你好"
	got := hardWrapRunes(input, 6)
	lines := splitLines(got)
	w1 := runewidth.StringWidth(lines[0])
	if w1 != 5 {
		t.Errorf("line 1: expected width 5, got %d (%q)", w1, lines[0])
	}
}

func TestHardWrapRunes_PureSpaces(t *testing.T) {
	got := hardWrapRunes("a b c d e", 3)
	lines := splitLines(got)
	for i, line := range lines {
		w := runewidth.StringWidth(line)
		if w > 3 {
			t.Errorf("line %d: width %d exceeds 3: %q", i, w, line)
		}
	}
}

func TestHardWrapRunes_DoubleWidthAtBoundary(t *testing.T) {
	// "abc好" = 3+2 = 5 cols, width = 4 → 好 wraps to line 2
	got := hardWrapRunes("abc好", 4)
	lines := splitLines(got)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "abc" {
		t.Errorf("line 1: expected %q, got %q", "abc", lines[0])
	}
	if lines[1] != "好" {
		t.Errorf("line 2: expected %q, got %q", "好", lines[1])
	}
}

// splitLines is a test helper — declared in cli_panel.go.
