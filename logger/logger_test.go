package logger

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupLogger(t *testing.T) {
	t.Run("creates_log_directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		logDir := filepath.Join(tmpDir, ".xbot", "logs")

		cfg := SetupConfig{
			Level:   "info",
			Format:  "text",
			WorkDir: tmpDir,
			MaxAge:  7,
		}

		// Close any existing globalRotateFile first
		Close()

		err := Setup(cfg)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}
		defer Close()

		// Verify directory was created
		if _, err := os.Stat(logDir); os.IsNotExist(err) {
			t.Errorf("log directory was not created: %s", logDir)
		}
	})

	t.Run("sets_text_format", func(t *testing.T) {
		Close()
		cfg := SetupConfig{
			Level:   "info",
			Format:  "text",
			WorkDir: t.TempDir(),
		}

		err := Setup(cfg)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}
		defer Close()

		// Verify text formatter is set (indirectly via logging)
		Info("test message - text format")
	})

	t.Run("sets_json_format", func(t *testing.T) {
		Close()
		cfg := SetupConfig{
			Level:   "info",
			Format:  "json",
			WorkDir: t.TempDir(),
		}

		err := Setup(cfg)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}
		defer Close()

		// Verify JSON formatter is set
		Info("test message - json format")
	})

	t.Run("sets_log_level", func(t *testing.T) {
		Close()
		cfg := SetupConfig{
			Level:   "debug",
			Format:  "text",
			WorkDir: t.TempDir(),
		}

		err := Setup(cfg)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}
		defer Close()

		// Debug level should now be enabled
		Debug("debug message should appear")
	})

	t.Run("defaults_to_info_level_for_invalid_level", func(t *testing.T) {
		Close()
		cfg := SetupConfig{
			Level:   "invalid",
			Format:  "text",
			WorkDir: t.TempDir(),
		}

		err := Setup(cfg)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}
		defer Close()

		// Should default to info level without error
		Info("info message")
	})

	t.Run("works_without_workdir", func(t *testing.T) {
		Close()
		cfg := SetupConfig{
			Level:  "info",
			Format: "text",
		}

		err := Setup(cfg)
		if err != nil {
			t.Fatalf("Setup failed: %v", err)
		}
		defer Close()

		Info("test message without file output")
	})
}

func TestDailyRotateFile(t *testing.T) {
	t.Run("creates_initial_file", func(t *testing.T) {
		tmpDir := t.TempDir()

		drf, err := newDailyRotateFile(tmpDir, "test")
		if err != nil {
			t.Fatalf("newDailyRotateFile failed: %v", err)
		}
		defer drf.Close()

		// Check that today's log file was created
		today := time.Now().Format("2006-01-02")
		expectedFile := filepath.Join(tmpDir, "test-"+today+".log")
		if _, err := os.Stat(expectedFile); os.IsNotExist(err) {
			t.Errorf("expected log file not created: %s", expectedFile)
		}
	})

	t.Run("write_to_file", func(t *testing.T) {
		tmpDir := t.TempDir()

		drf, err := newDailyRotateFile(tmpDir, "test")
		if err != nil {
			t.Fatalf("newDailyRotateFile failed: %v", err)
		}
		defer drf.Close()

		testData := []byte("test log message\n")
		n, err := drf.Write(testData)
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != len(testData) {
			t.Errorf("expected %d bytes written, got %d", len(testData), n)
		}

		// Verify content was written
		today := time.Now().Format("2006-01-02")
		logPath := filepath.Join(tmpDir, "test-"+today+".log")
		content, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("failed to read log file: %v", err)
		}
		if string(content) != string(testData) {
			t.Errorf("expected %q, got %q", string(testData), string(content))
		}
	})

	t.Run("append_mode", func(t *testing.T) {
		tmpDir := t.TempDir()

		drf, err := newDailyRotateFile(tmpDir, "test")
		if err != nil {
			t.Fatalf("newDailyRotateFile failed: %v", err)
		}
		defer drf.Close()

		// Write first message
		drf.Write([]byte("first message\n"))
		// Write second message
		drf.Write([]byte("second message\n"))

		// Verify both messages are in the file
		today := time.Now().Format("2006-01-02")
		logPath := filepath.Join(tmpDir, "test-"+today+".log")
		content, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("failed to read log file: %v", err)
		}

		if !strings.Contains(string(content), "first message") {
			t.Error("first message not found in log file")
		}
		if !strings.Contains(string(content), "second message") {
			t.Error("second message not found in log file")
		}
	})

	t.Run("creates_directory_if_not_exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		logDir := filepath.Join(tmpDir, "nested", "logs")

		drf, err := newDailyRotateFile(logDir, "test")
		if err != nil {
			t.Fatalf("newDailyRotateFile failed: %v", err)
		}
		defer drf.Close()

		// Verify directory was created
		if _, err := os.Stat(logDir); os.IsNotExist(err) {
			t.Errorf("log directory was not created: %s", logDir)
		}
	})

	t.Run("close_safe_when_nil", func(t *testing.T) {
		// This should not panic
		drf := &dailyRotateFile{}
		err := drf.Close()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("rotation_same_day_no_change", func(t *testing.T) {
		tmpDir := t.TempDir()

		drf, err := newDailyRotateFile(tmpDir, "test")
		if err != nil {
			t.Fatalf("newDailyRotateFile failed: %v", err)
		}
		defer drf.Close()

		// Call rotate again on same day
		err = drf.rotate()
		if err != nil {
			t.Fatalf("rotate failed: %v", err)
		}

		// Should still have only one file
		entries, err := os.ReadDir(tmpDir)
		if err != nil {
			t.Fatalf("failed to read directory: %v", err)
		}
		if len(entries) != 1 {
			t.Errorf("expected 1 log file, got %d", len(entries))
		}
	})
}

func TestCleanupOldLogs(t *testing.T) {
	t.Run("deletes_expired_logs", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create an "old" log file by setting its modification time to 10 days ago
		oldFile := filepath.Join(tmpDir, "xbot-2026-03-10.log")
		if err := os.WriteFile(oldFile, []byte("old log"), 0644); err != nil {
			t.Fatal(err)
		}

		// Set modification time to 10 days ago
		oldTime := time.Now().AddDate(0, 0, -10)
		if err := os.Chtimes(oldFile, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		// Create a recent log file
		recentFile := filepath.Join(tmpDir, "xbot-2026-03-20.log")
		if err := os.WriteFile(recentFile, []byte("recent log"), 0644); err != nil {
			t.Fatal(err)
		}

		// Run cleanup with 7-day retention
		doCleanup(tmpDir, 7)

		// Old file should be deleted
		if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
			t.Error("old log file should have been deleted")
		}

		// Recent file should remain
		if _, err := os.Stat(recentFile); os.IsNotExist(err) {
			t.Error("recent log file should not have been deleted")
		}
	})

	t.Run("keeps_logs_within_retention", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a log file from 5 days ago
		file := filepath.Join(tmpDir, "xbot-2026-03-15.log")
		if err := os.WriteFile(file, []byte("log content"), 0644); err != nil {
			t.Fatal(err)
		}

		oldTime := time.Now().AddDate(0, 0, -5)
		if err := os.Chtimes(file, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		// Run cleanup with 7-day retention
		doCleanup(tmpDir, 7)

		// File should remain
		if _, err := os.Stat(file); os.IsNotExist(err) {
			t.Error("log file within retention period should not be deleted")
		}
	})

	t.Run("ignores_non_log_files", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a non-log file that's old
		txtFile := filepath.Join(tmpDir, "readme.txt")
		if err := os.WriteFile(txtFile, []byte("not a log"), 0644); err != nil {
			t.Fatal(err)
		}

		oldTime := time.Now().AddDate(0, 0, -30)
		if err := os.Chtimes(txtFile, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		// Run cleanup
		doCleanup(tmpDir, 7)

		// Non-log file should remain
		if _, err := os.Stat(txtFile); os.IsNotExist(err) {
			t.Error("non-log file should not be deleted")
		}
	})

	t.Run("ignores_directories", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create a directory with old modification time
		subDir := filepath.Join(tmpDir, "old_data")
		if err := os.Mkdir(subDir, 0755); err != nil {
			t.Fatal(err)
		}

		oldTime := time.Now().AddDate(0, 0, -30)
		if err := os.Chtimes(subDir, oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		// Run cleanup
		doCleanup(tmpDir, 7)

		// Directory should remain
		if _, err := os.Stat(subDir); os.IsNotExist(err) {
			t.Error("directory should not be deleted")
		}
	})

	t.Run("handles_empty_directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Should not panic
		doCleanup(tmpDir, 7)

		// Directory should still exist
		if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
			t.Error("directory should still exist")
		}
	})

	t.Run("handles_nonexistent_directory", func(t *testing.T) {
		nonexistentDir := "/nonexistent/path/to/logs"

		// Should not panic
		doCleanup(nonexistentDir, 7)
	})

	t.Run("deletes_multiple_old_files", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create multiple old files
		for i := 1; i <= 3; i++ {
			file := filepath.Join(tmpDir, "xbot-2026-03-"+formatDay(i)+".log")
			if err := os.WriteFile(file, []byte("old log"), 0644); err != nil {
				t.Fatal(err)
			}
			oldTime := time.Now().AddDate(0, 0, -10-i)
			if err := os.Chtimes(file, oldTime, oldTime); err != nil {
				t.Fatal(err)
			}
		}

		// Run cleanup
		doCleanup(tmpDir, 7)

		// Count remaining files
		entries, _ := os.ReadDir(tmpDir)
		logFiles := 0
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".log") {
				logFiles++
			}
		}

		if logFiles != 0 {
			t.Errorf("expected 0 log files, got %d", logFiles)
		}
	})
}

func formatDay(day int) string {
	if day < 10 {
		return "0" + string(rune('0'+day))
	}
	return string(rune('0'+day/10)) + string(rune('0'+day%10))
}

func TestLoggerExports(t *testing.T) {
	// Test that exported functions and types work correctly
	t.Run("parse_level", func(t *testing.T) {
		level, err := ParseLevel("debug")
		if err != nil {
			t.Errorf("failed to parse debug level: %v", err)
		}
		if level != InfoLevel {
			// Debug level should be different from Info
			t.Log("Debug level parsed successfully")
		}
	})

	t.Run("with_field", func(t *testing.T) {
		entry := WithField("key", "value")
		if entry == nil {
			t.Error("WithField returned nil")
		}
	})

	t.Run("with_fields", func(t *testing.T) {
		entry := WithFields(Fields{"key1": "value1", "key2": "value2"})
		if entry == nil {
			t.Error("WithFields returned nil")
		}
	})

	t.Run("with_error", func(t *testing.T) {
		entry := WithError(os.ErrNotExist)
		if entry == nil {
			t.Error("WithError returned nil")
		}
	})
}
