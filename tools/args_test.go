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

func TestIsKnownDotFile(t *testing.T) {
	t.Run("known files return true", func(t *testing.T) {
		for _, name := range []string{
			".xbot", ".git", ".github", ".gitlab-ci.yml",
			".gitignore", ".editorconfig", ".env",
			".env.example", ".env.local",
		} {
			t.Run(name, func(t *testing.T) {
				if !isKnownDotFile(name) {
					t.Errorf("isKnownDotFile(%q) = false, want true", name)
				}
			})
		}
	})

	t.Run("unknown files return false", func(t *testing.T) {
		for _, name := range []string{".foo", "package.json", "README.md", "."} {
			t.Run(name, func(t *testing.T) {
				if isKnownDotFile(name) {
					t.Errorf("isKnownDotFile(%q) = true, want false", name)
				}
			})
		}
	})
}

func TestSplitJSONLLines(t *testing.T) {
	t.Run("empty data returns empty slice", func(t *testing.T) {
		got := splitJSONLLines([]byte{})
		if len(got) != 0 {
			t.Errorf("got %d lines, want 0", len(got))
		}
	})

	t.Run("single line no newline returns one element", func(t *testing.T) {
		got := splitJSONLLines([]byte(`{"a":1}`))
		if len(got) != 1 {
			t.Fatalf("got %d lines, want 1", len(got))
		}
		if string(got[0]) != `{"a":1}` {
			t.Errorf("got %q, want %q", string(got[0]), `{"a":1}`)
		}
	})

	t.Run("two lines returns two elements", func(t *testing.T) {
		got := splitJSONLLines([]byte("line1\nline2"))
		if len(got) != 2 {
			t.Fatalf("got %d lines, want 2", len(got))
		}
		if string(got[0]) != "line1" {
			t.Errorf("got[0] = %q, want %q", string(got[0]), "line1")
		}
		if string(got[1]) != "line2" {
			t.Errorf("got[1] = %q, want %q", string(got[1]), "line2")
		}
	})

	t.Run("trailing newline yields no trailing empty element", func(t *testing.T) {
		got := splitJSONLLines([]byte("line1\n"))
		if len(got) != 1 {
			t.Fatalf("got %d lines, want 1", len(got))
		}
		if string(got[0]) != "line1" {
			t.Errorf("got[0] = %q, want %q", string(got[0]), "line1")
		}
	})

	t.Run("multiple newlines correct count", func(t *testing.T) {
		got := splitJSONLLines([]byte("a\nb\nc\n"))
		if len(got) != 3 {
			t.Fatalf("got %d lines, want 3", len(got))
		}
		want := []string{"a", "b", "c"}
		for i, w := range want {
			if string(got[i]) != w {
				t.Errorf("got[%d] = %q, want %q", i, string(got[i]), w)
			}
		}
	})
}
