package agent

import "testing"

func TestNormalizeSenderID_SingleUser(t *testing.T) {
	a := &Agent{singleUser: true}

	tests := []struct {
		input string
		want  string
	}{
		{"ou_abc123", "default"},
		{"user", "default"},
		{"", "default"},
		{"default", "default"},
	}

	for _, tt := range tests {
		got := a.normalizeSenderID(tt.input)
		if got != tt.want {
			t.Errorf("normalizeSenderID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNormalizeSenderID_MultiUser(t *testing.T) {
	a := &Agent{singleUser: false}

	tests := []struct {
		input string
		want  string
	}{
		{"ou_abc123", "ou_abc123"},
		{"user", "user"},
		{"", ""},
		{"default", "default"},
	}

	for _, tt := range tests {
		got := a.normalizeSenderID(tt.input)
		if got != tt.want {
			t.Errorf("normalizeSenderID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
