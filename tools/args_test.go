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

func TestValidateRunAsReason(t *testing.T) {
	tests := []struct {
		name    string
		runAs   string
		reason  string
		wantErr bool
	}{
		{
			name:    "both empty",
			runAs:   "",
			reason:  "",
			wantErr: false,
		},
		{
			name:    "both set",
			runAs:   "admin",
			reason:  "maintenance",
			wantErr: false,
		},
		{
			name:    "runAs set reason empty",
			runAs:   "admin",
			reason:  "",
			wantErr: true,
		},
		{
			name:    "runAs empty reason set",
			runAs:   "",
			reason:  "because",
			wantErr: true,
		},
		{
			name:    "both whitespace-only",
			runAs:   "  ",
			reason:  "\t",
			wantErr: false,
		},
		{
			name:    "runAs whitespace-only reason set",
			runAs:   "  ",
			reason:  "some reason",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateRunAsReason(tc.runAs, tc.reason)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateRunAsReason(%q, %q) = %v, wantErr %v", tc.runAs, tc.reason, err, tc.wantErr)
			}
		})
	}
}
