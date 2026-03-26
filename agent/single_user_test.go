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
		got := a.NormalizeSenderID(tt.input)
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
		got := a.NormalizeSenderID(tt.input)
		if got != tt.want {
			t.Errorf("normalizeSenderID(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestWorkspaceRoot_SingleUser(t *testing.T) {
	a := &Agent{singleUser: true, workDir: "/work"}

	// Single-user mode: always returns workDir regardless of senderID
	tests := []struct {
		senderID string
		want     string
	}{
		{"ou_abc123", "/work"},
		{"default", "/work"},
		{"", "/work"},
	}

	for _, tt := range tests {
		got := a.workspaceRoot(tt.senderID)
		if got != tt.want {
			t.Errorf("workspaceRoot(%q) = %q, want %q", tt.senderID, got, tt.want)
		}
	}
}

func TestWorkspaceRoot_MultiUser(t *testing.T) {
	a := &Agent{singleUser: false, workDir: "/work"}

	// Multi-user mode: returns per-user workspace directory
	got := a.workspaceRoot("ou_abc123")
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	want := "/work/.xbot/users/ou_abc123/workspace"
	if got != want {
		t.Errorf("workspaceRoot(%q) = %q, want %q", "ou_abc123", got, want)
	}
}
