package tools

import (
	"testing"
)

func TestSubAgentTool_ParseInteractiveParams(t *testing.T) {
	tool := &SubAgentTool{}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:  "basic one-shot",
			input: `{"task":"review code","role":"code-reviewer"}`,
		},
		{
			name:  "interactive spawn",
			input: `{"task":"review code","role":"code-reviewer","interactive":true}`,
		},
		{
			name:  "action send",
			input: `{"task":"fix this","role":"writer","action":"send"}`,
		},
		{
			name:  "action unload",
			input: `{"task":"done","role":"writer","action":"unload"}`,
		},
		{
			name:    "missing task (non-unload)",
			input:   `{"role":"writer"}`,
			wantErr: true,
		},
		{
			name:    "missing role",
			input:   `{"task":"do something"}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			input:   `{not json}`,
			wantErr: true,
		},
		{
			name:    "action send without task",
			input:   `{"task":"","role":"writer","action":"send"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: Execute will fail at role lookup since we don't have agent roles files,
			// but parameter parsing errors should be caught first
			_, err := tool.Execute(nil, tt.input)
			if tt.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.wantErr {
				// Error is expected here since we have no Manager/roles set up,
				// but it should NOT be a parameter parsing error
				if err != nil {
					// Verify the error is not about missing params
					errStr := err.Error()
					if errStr == "task is required" || errStr == "role is required" ||
						errStr == "invalid parameters" {
						t.Fatalf("unexpected parameter error: %v", err)
					}
				}
			}
		})
	}
}

func TestSubAgentTool_NameAndDescription(t *testing.T) {
	tool := &SubAgentTool{}
	if tool.Name() != "SubAgent" {
		t.Errorf("Name() = %q, want %q", tool.Name(), "SubAgent")
	}
	desc := tool.Description()
	if desc == "" {
		t.Error("Description() should not be empty")
	}
	// Check that interactive mode is documented
	if !contains(desc, "interactive") {
		t.Error("Description should document interactive mode")
	}
}

func TestSubAgentTool_Parameters(t *testing.T) {
	tool := &SubAgentTool{}
	params := tool.Parameters()

	paramNames := make(map[string]bool)
	for _, p := range params {
		paramNames[p.Name] = true
	}

	requiredParams := []string{"task", "role"}
	for _, name := range requiredParams {
		if !paramNames[name] {
			t.Errorf("missing required parameter: %s", name)
		}
	}

	// Check interactive and action params exist
	if !paramNames["interactive"] {
		t.Error("missing interactive parameter")
	}
	if !paramNames["action"] {
		t.Error("missing action parameter")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
