package tools

import (
	"encoding/json"
	"testing"
)

func TestDownloadFileTool_ParameterValidation(t *testing.T) {
	tool := NewDownloadFileTool("", "")

	tests := []struct {
		name    string
		input   map[string]string
		wantErr bool
		errSub  string
	}{
		{
			name:    "missing output_path",
			input:   map[string]string{"url": "https://example.com/file.pdf"},
			wantErr: true,
			errSub:  "output_path is required",
		},
		{
			name:    "missing url and feishu params",
			input:   map[string]string{"output_path": "out.pdf"},
			wantErr: true,
			errSub:  "must provide url",
		},
		{
			name:    "only message_id without file_key",
			input:   map[string]string{"message_id": "om_123", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "must provide url",
		},
		{
			name:    "only file_key without message_id",
			input:   map[string]string{"file_key": "fk", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "must provide url",
		},
		{
			name:    "invalid message_id chars",
			input:   map[string]string{"message_id": "om_123/bad", "file_key": "fk", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "invalid message_id",
		},
		{
			name:    "invalid file_key chars",
			input:   map[string]string{"message_id": "om_123", "file_key": "fk/bad", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "invalid file_key",
		},
		{
			name:    "valid feishu params but no feishu channel",
			input:   map[string]string{"message_id": "om_123", "file_key": "file_v3_abc", "output_path": "out.pdf"},
			wantErr: true,
			errSub:  "not supported for channel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON, _ := json.Marshal(tt.input)
			root := t.TempDir()
			ctx := &ToolContext{
				WorkspaceRoot: root,
				Channel:       "qq", // non-feishu channel
			}
			_, err := tool.Execute(ctx, string(inputJSON))
			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && tt.errSub != "" {
				if !contains(err.Error(), tt.errSub) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errSub)
				}
			}
		})
	}
}
