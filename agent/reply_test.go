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
	if got != p3 {
		t.Errorf("expected last paragraph, got %q", got)
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
	// p3 is < 50 chars and there are > 2 paragraphs, so it merges p2 + p3
	// paragraphs[-2] retains the trailing space from Repeat, last = trimmed p3
	// The final TrimSpace only trims outer whitespace, not the space within
	want := strings.Repeat("Second paragraph detailed explanation. ", 8) + "\n\nOK."
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
