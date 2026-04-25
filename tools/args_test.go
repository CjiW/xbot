package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

type testArgs struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func TestParseToolArgs_ValidJSON(t *testing.T) {
	input := `{"name":"hello","count":42}`

	got, err := parseToolArgs[testArgs](input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "hello" {
		t.Errorf("Name = %q, want %q", got.Name, "hello")
	}
	if got.Count != 42 {
		t.Errorf("Count = %d, want %d", got.Count, 42)
	}
}

func TestParseToolArgs_InvalidJSON(t *testing.T) {
	input := "not json at all"

	got, err := parseToolArgs[testArgs](input)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got != nil {
		t.Errorf("expected nil result, got %+v", got)
	}
	if !strings.HasPrefix(err.Error(), "parse args: ") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "parse args: ")
	}
}

func TestParseToolArgs_EmptyObject(t *testing.T) {
	input := "{}"

	got, err := parseToolArgs[testArgs](input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil pointer, got nil")
	}
	var zero testArgs
	if *got != zero {
		t.Errorf("got %+v, want zero-value struct", got)
	}
}

func TestParseToolArgs_EmptyString(t *testing.T) {
	input := ""

	got, err := parseToolArgs[testArgs](input)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got != nil {
		t.Errorf("expected nil result, got %+v", got)
	}
	// Verify it wraps a json.SyntaxError
	var syntaxErr *json.SyntaxError
	if !strings.HasPrefix(err.Error(), "parse args: ") {
		t.Errorf("error = %q, want prefix %q", err.Error(), "parse args: ")
	}
	if !strings.Contains(err.Error(), "unexpected end") {
		t.Errorf("error = %q, want it to contain 'unexpected end'", err.Error())
	}
	_ = syntaxErr // suppress unused import
}
