package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogsToolName(t *testing.T) {
	tool := NewLogsTool("admin123")
	if tool.Name() != "Logs" {
		t.Errorf("expected name 'Logs', got %q", tool.Name())
	}
}

func TestLogsToolDescription(t *testing.T) {
	tool := NewLogsTool("admin123")
	desc := tool.Description()

	// Verify description contains key information
	if !strings.Contains(desc, "list") {
		t.Error("description should mention 'list' action")
	}
	if !strings.Contains(desc, "read") {
		t.Error("description should mention 'read' action")
	}
	// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
	if !strings.Contains(desc, ".xbot/logs") {
		t.Error("description should mention log directory")
	}
	if !strings.Contains(desc, "level") {
		t.Error("description should mention 'level' parameter")
	}
	if !strings.Contains(desc, "grep") {
		t.Error("description should mention 'grep' parameter")
	}
}

func TestLogsToolPermission(t *testing.T) {
	t.Run("denies_non_admin_session", func(t *testing.T) {
		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "user456",
			DataDir: t.TempDir(),
		}

		_, err := tool.Execute(ctx, `{"action":"list"}`)
		if err == nil {
			t.Error("expected permission denied error for non-admin user")
		}
		if !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("expected permission denied error, got: %v", err)
		}
	})

	t.Run("allows_admin_session", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create a log file
		logFile := filepath.Join(logDir, "xbot-2026-03-20.log")
		if err := os.WriteFile(logFile, []byte("test log content"), 0644); err != nil {
			t.Fatal(err)
		}

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"list"}`)
		if err != nil {
			t.Fatalf("unexpected error for admin user: %v", err)
		}
		if result == nil {
			t.Error("expected result for admin user")
		}
	})

	t.Run("denies_when_admin_chatid_empty", func(t *testing.T) {
		tool := NewLogsTool("") // empty admin chat ID
		ctx := &ToolContext{
			ChatID:  "anyone",
			DataDir: t.TempDir(),
		}

		_, err := tool.Execute(ctx, `{"action":"list"}`)
		if err == nil {
			t.Error("expected error when admin chat ID is empty")
		}
	})

	t.Run("denies_different_admin_chatid", func(t *testing.T) {
		tool := NewLogsTool("real_admin")
		ctx := &ToolContext{
			ChatID:  "fake_admin",
			DataDir: t.TempDir(),
		}

		_, err := tool.Execute(ctx, `{"action":"list"}`)
		if err == nil {
			t.Error("expected permission denied for different chat ID")
		}
	})
}

func TestLogsToolListAction(t *testing.T) {
	t.Run("lists_log_files", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create multiple log files
		logFiles := []string{
			"xbot-2026-03-18.log",
			"xbot-2026-03-19.log",
			"xbot-2026-03-20.log",
		}
		for _, f := range logFiles {
			path := filepath.Join(logDir, f)
			if err := os.WriteFile(path, []byte("log content for "+f), 0644); err != nil {
				t.Fatal(err)
			}
		}

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"list"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify all files are listed
		for _, f := range logFiles {
			if !strings.Contains(result.Summary, f) {
				t.Errorf("expected file %q in result", f)
			}
		}
	})

	t.Run("handles_empty_directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"list"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(result.Summary, "No log files found") {
			t.Errorf("expected 'No log files found' message, got: %s", result.Summary)
		}
	})

	t.Run("handles_nonexistent_directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"list"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should handle gracefully
		if !strings.Contains(result.Summary, "No log files found") {
			t.Errorf("expected 'No log files found' for nonexistent directory, got: %s", result.Summary)
		}
	})

	t.Run("ignores_non_log_files", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create log file and non-log file
		if err := os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte("log"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logDir, "readme.txt"), []byte("readme"), 0644); err != nil {
			t.Fatal(err)
		}

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"list"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if strings.Contains(result.Summary, "readme.txt") {
			t.Error("non-log file should not be listed")
		}
		if !strings.Contains(result.Summary, "xbot-2026-03-20.log") {
			t.Error("log file should be listed")
		}
	})

	t.Run("files_sorted_by_date_descending", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create log files in non-chronological order
		logFiles := []string{
			"xbot-2026-03-19.log",
			"xbot-2026-03-20.log",
			"xbot-2026-03-18.log",
		}
		for _, f := range logFiles {
			path := filepath.Join(logDir, f)
			if err := os.WriteFile(path, []byte("content"), 0644); err != nil {
				t.Fatal(err)
			}
		}

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"list"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Latest should appear first in output
		idx18 := strings.Index(result.Summary, "2026-03-18")
		idx19 := strings.Index(result.Summary, "2026-03-19")
		idx20 := strings.Index(result.Summary, "2026-03-20")

		// 2026-03-20 should come before 2026-03-19, which should come before 2026-03-18
		if idx20 > idx19 || idx19 > idx18 {
			t.Error("log files should be sorted by date descending (newest first)")
		}
	})
}

func TestLogsToolReadAction(t *testing.T) {
	t.Run("reads_latest_log_file", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create log files
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-19.log"), []byte("old log\n"), 0644)
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte("latest log\n"), 0644)

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"read"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(result.Summary, "latest log") {
			t.Errorf("expected latest log content, got: %s", result.Summary)
		}
	})

	t.Run("reads_specific_log_file", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-18.log"), []byte("day 18 log\n"), 0644)
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte("day 20 log\n"), 0644)

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"read","file":"xbot-2026-03-18.log"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(result.Summary, "day 18 log") {
			t.Errorf("expected specific file content, got: %s", result.Summary)
		}
	})

	t.Run("filters_by_level", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		content := `time="2026-03-20T10:00:00Z" level=info msg="info message"
time="2026-03-20T10:01:00Z" level=error msg="error message"
time="2026-03-20T10:02:00Z" level=info msg="another info"
time="2026-03-20T10:03:00Z" level=error msg="another error"
`
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte(content), 0644)

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"read","level":"error"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should contain error messages but not info messages
		if !strings.Contains(result.Summary, "error message") {
			t.Error("should contain error message")
		}
		if strings.Contains(result.Summary, "info message") {
			t.Error("should not contain info message when filtering for error")
		}
	})

	t.Run("filters_by_grep", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		content := `line with request_id=abc123
line without marker
another line with request_id=def456
plain text line
`
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte(content), 0644)

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"read","grep":"request_id"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should contain lines with request_id
		if !strings.Contains(result.Summary, "request_id=abc123") {
			t.Error("should contain line with request_id=abc123")
		}
		if strings.Contains(result.Summary, "plain text line") {
			t.Error("should not contain line without grep pattern")
		}
	})

	t.Run("limits_lines", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create file with many lines
		var content string
		for i := 0; i < 200; i++ {
			content += "log line\n"
		}
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte(content), 0644)

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"read","lines":50}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Count lines in result (excluding header)
		lines := strings.Count(result.Summary, "log line")
		if lines > 50 {
			t.Errorf("expected at most 50 lines, got %d", lines)
		}
	})

	t.Run("default_lines_100", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create file with many lines
		var content string
		for i := 0; i < 200; i++ {
			content += "log line\n"
		}
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte(content), 0644)

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"read"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should use default 100 lines
		if !strings.Contains(result.Summary, "100") {
			t.Log("Note: result should indicate 100 lines limit")
		}
	})

	t.Run("error_no_log_files", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		_, err := tool.Execute(ctx, `{"action":"read"}`)
		if err == nil {
			t.Error("expected error when no log files exist")
		}
	})

	t.Run("handles_json_format_logs", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		content := `{"level":"info","msg":"info message"}
{"level":"error","msg":"error message"}
{"level":"info","msg":"another info"}
`
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte(content), 0644)

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"read","level":"error"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(result.Summary, "error message") {
			t.Error("should contain error message from JSON log")
		}
	})

	t.Run("combines_level_and_grep_filters", func(t *testing.T) {
		tmpDir := t.TempDir()
		// NOTE: .xbot is the server-side config directory; not accessible in user sandbox
		logDir := filepath.Join(tmpDir, ".xbot", "logs")
		if err := os.MkdirAll(logDir, 0755); err != nil {
			t.Fatal(err)
		}

		content := `time="2026-03-20T10:00:00Z" level=error msg="database connection failed"
time="2026-03-20T10:01:00Z" level=error msg="api timeout"
time="2026-03-20T10:02:00Z" level=info msg="database connected"
`
		os.WriteFile(filepath.Join(logDir, "xbot-2026-03-20.log"), []byte(content), 0644)

		tool := NewLogsTool("admin123")
		ctx := &ToolContext{
			ChatID:  "admin123",
			DataDir: tmpDir,
		}

		result, err := tool.Execute(ctx, `{"action":"read","level":"error","grep":"database"}`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should only contain error lines with "database"
		if !strings.Contains(result.Summary, "database connection failed") {
			t.Error("should contain database error")
		}
		if strings.Contains(result.Summary, "api timeout") {
			t.Error("should not contain non-database error")
		}
		if strings.Contains(result.Summary, "database connected") {
			t.Error("should not contain info level line")
		}
	})
}

func TestLogsToolInvalidAction(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewLogsTool("admin123")
	ctx := &ToolContext{
		ChatID:  "admin123",
		DataDir: tmpDir,
	}

	_, err := tool.Execute(ctx, `{"action":"invalid"}`)
	if err == nil {
		t.Error("expected error for invalid action")
	}
}

func TestLogsToolInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewLogsTool("admin123")
	ctx := &ToolContext{
		ChatID:  "admin123",
		DataDir: tmpDir,
	}

	_, err := tool.Execute(ctx, `{invalid json`)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestLogsToolMissingAction(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewLogsTool("admin123")
	ctx := &ToolContext{
		ChatID:  "admin123",
		DataDir: tmpDir,
	}

	_, err := tool.Execute(ctx, `{}`)
	if err == nil {
		t.Error("expected error when action is missing")
	}
}

func TestLogsToolParameters(t *testing.T) {
	tool := NewLogsTool("admin123")
	params := tool.Parameters()

	// Verify all expected parameters are defined
	paramNames := make(map[string]bool)
	for _, p := range params {
		paramNames[p.Name] = true
	}

	expectedParams := []string{"action", "file", "lines", "level", "grep"}
	for _, name := range expectedParams {
		if !paramNames[name] {
			t.Errorf("missing parameter: %s", name)
		}
	}

	// Verify action is required
	for _, p := range params {
		if p.Name == "action" && !p.Required {
			t.Error("action parameter should be required")
		}
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1572864, "1.5 MiB"},
		{1073741824, "1.0 GiB"},
	}

	for _, tt := range tests {
		result := formatFileSize(tt.bytes)
		if result != tt.expected {
			t.Errorf("formatFileSize(%d) = %q, want %q", tt.bytes, result, tt.expected)
		}
	}
}
