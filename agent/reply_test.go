package agent

import (
	"strings"
	"testing"
)

func TestExtractFinalReply_ShortContent(t *testing.T) {
	input := "Hello, this is a short reply."
	got := ExtractFinalReply(input)
	if got != input {
		t.Errorf("short content should be returned as-is, got %q", got)
	}
}

func TestExtractFinalReply_NormalLongContent(t *testing.T) {
	// Build a content > 500 chars with 3 paragraphs where last paragraph >= 50 chars
	p1 := strings.Repeat("First paragraph background context line. ", 8) + "\n\n"
	p2 := strings.Repeat("Second paragraph detailed analysis content. ", 8) + "\n\n"
	p3 := "This is the final concluding paragraph with enough length to pass the fifty character threshold."
	input := p1 + p2 + p3

	if len(input) < 500 {
		t.Fatalf("test input must be >= 500 chars, got %d", len(input))
	}

	got := ExtractFinalReply(input)
	// New strategy: takes last 2-3 paragraphs (up to 2000 chars).
	// With 3 paragraphs totaling ~1012 chars (< 2000), all 3 are returned.
	want := strings.TrimSpace(input)
	if got != want {
		t.Errorf("expected all 3 paragraphs (within 2000 char limit), got %q", got)
	}
}

func TestExtractFinalReply_LastParagraphTooShort(t *testing.T) {
	p1 := strings.Repeat("First paragraph analysis content. ", 8) + "\n\n"
	p2 := strings.Repeat("Second paragraph detailed explanation. ", 8) + "\n\n"
	p3 := "OK."
	input := p1 + p2 + p3

	if len(input) < 500 {
		t.Fatalf("test input must be >= 500 chars, got %d", len(input))
	}

	got := ExtractFinalReply(input)
	// New strategy: takes last 2-3 paragraphs (up to 2000 chars).
	// All 3 paragraphs fit within 2000 chars, so all are returned.
	want := strings.TrimSpace(input)
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
