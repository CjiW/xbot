package llm

import (
	"testing"
)

func TestParseAnthropicThinking(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantNil  bool
		wantType string
		wantEff  string
	}{
		{
			name:    "empty string returns nil",
			input:   "",
			wantNil: true,
		},
		{
			name:    "disabled returns nil",
			input:   "disabled",
			wantNil: true,
		},
		{
			name:     "enabled returns type enabled",
			input:    "enabled",
			wantType: "enabled",
		},
		{
			name:     "adaptive returns type adaptive with high effort",
			input:    "adaptive",
			wantType: "adaptive",
			wantEff:  "high",
		},
		{
			name:     "JSON with budget_tokens",
			input:    `{"type": "enabled", "budget_tokens": 10000}`,
			wantType: "enabled",
		},
		{
			name:     "JSON adaptive with medium effort",
			input:    `{"type": "adaptive", "effort": "medium"}`,
			wantType: "adaptive",
			wantEff:  "medium",
		},
		{
			name:     "invalid JSON defaults to enabled",
			input:    `{invalid`,
			wantType: "enabled",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAnthropicThinking(tt.input)
			if tt.wantNil {
				if got != nil {
					t.Errorf("parseAnthropicThinking(%q) = %v, want nil", tt.input, got)
				}
				return
			}
			if got == nil {
				t.Errorf("parseAnthropicThinking(%q) = nil, want non-nil", tt.input)
				return
			}
			if got.Type != tt.wantType {
				t.Errorf("parseAnthropicThinking(%q).Type = %q, want %q", tt.input, got.Type, tt.wantType)
			}
			if tt.wantEff != "" && got.Effort != tt.wantEff {
				t.Errorf("parseAnthropicThinking(%q).Effort = %q, want %q", tt.input, got.Effort, tt.wantEff)
			}
		})
	}
}
