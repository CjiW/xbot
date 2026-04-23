package tools

import (
	"testing"
)

func TestParseMentions(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{
			input:    "@agent:reviewer/r1 what do you think?",
			expected: []string{"agent:reviewer/r1"},
		},
		{
			input:    "@agent:reviewer/r1 @agent:tester/t1 please review",
			expected: []string{"agent:reviewer/r1", "agent:tester/t1"},
		},
		{
			input:    "No mentions here",
			expected: nil,
		},
		{
			input:    "@agent:reviewer/r1 @agent:reviewer/r1 duplicate",
			expected: []string{"agent:reviewer/r1"}, // dedup
		},
		{
			input:    "@agent:a/b-c@d @agent:x/y more text",
			expected: []string{"agent:a/b-c@d", "agent:x/y"},
		},
		{
			input:    "text @agent:reviewer/r1\nnext line @agent:tester/t2 end",
			expected: []string{"agent:reviewer/r1", "agent:tester/t2"},
		},
	}

	for _, tt := range tests {
		result := parseMentions(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseMentions(%q): expected %v, got %v", tt.input, tt.expected, result)
			continue
		}
		for i, addr := range result {
			if addr != tt.expected[i] {
				t.Errorf("parseMentions(%q)[%d]: expected %q, got %q", tt.input, i, tt.expected[i], addr)
			}
		}
	}
}

func TestParseAgentAddress(t *testing.T) {
	tests := []struct {
		addr     string
		wantRole string
		wantInst string
	}{
		{"agent:reviewer/r1", "reviewer", "r1"},
		{"agent:code-reviewer/fix-bug", "code-reviewer", "fix-bug"},
		{"agent:a/b/c", "a", "b/c"},
		{"agent:noslash", "", ""},
		{"agent:/onlyinstance", "", "onlyinstance"},
		{"feishu:ou_xxx", "", ""},
	}

	for _, tt := range tests {
		role, instance := parseAgentAddress(tt.addr)
		if role != tt.wantRole || instance != tt.wantInst {
			t.Errorf("parseAgentAddress(%q): expected (%q, %q), got (%q, %q)",
				tt.addr, tt.wantRole, tt.wantInst, role, instance)
		}
	}
}
