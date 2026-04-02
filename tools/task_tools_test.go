package tools

import (
	"strings"
	"testing"
	"time"
)

// ==================== FormatBgTaskCompletion ====================

func TestFormatBgTaskCompletion_Basic(t *testing.T) {
	now := time.Now()
	finished := now.Add(30 * time.Second)
	task := &BackgroundTask{
		ID:         "abc123",
		Command:    "echo hello",
		Status:     BgTaskDone,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   0,
		Output:     "hello\n",
	}

	result := FormatBgTaskCompletion(task)

	if !strings.Contains(result, "abc123") {
		t.Error("missing task ID")
	}
	if !strings.Contains(result, "echo hello") {
		t.Error("missing command")
	}
	if !strings.Contains(result, "done") {
		t.Error("missing status")
	}
	if !strings.Contains(result, "Exit Code: 0") {
		t.Error("missing exit code")
	}
	if !strings.Contains(result, "hello") {
		t.Error("missing output")
	}
}

func TestFormatBgTaskCompletion_WithError(t *testing.T) {
	now := time.Now()
	finished := now.Add(5 * time.Second)
	task := &BackgroundTask{
		ID:         "err1",
		Command:    "false",
		Status:     BgTaskDone, // failed tasks also use BgTaskDone + Error field
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   1,
		Error:      "exit status 1",
		Output:     "",
	}

	result := FormatBgTaskCompletion(task)

	if !strings.Contains(result, "Error: exit status 1") {
		t.Error("missing error")
	}
	if !strings.Contains(result, "Exit Code: 1") {
		t.Error("missing exit code")
	}
	if !strings.Contains(result, "(no output)") {
		t.Error("should show no output hint")
	}
}

func TestFormatBgTaskCompletion_LargeOutputTruncated(t *testing.T) {
	now := time.Now()
	finished := now.Add(1 * time.Second)
	largeOutput := strings.Repeat("x", 3000) // > 2000 threshold
	task := &BackgroundTask{
		ID:         "big1",
		Command:    "cat large.log",
		Status:     BgTaskDone,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   0,
		Output:     largeOutput,
	}

	result := FormatBgTaskCompletion(task)

	if !strings.Contains(result, "truncated") {
		t.Error("should indicate truncation for large output")
	}
	if !strings.Contains(result, "3000") {
		t.Error("should show total size")
	}
	if !strings.Contains(result, "2000") {
		t.Error("should show truncated size")
	}
	// Result should be significantly shorter than original
	if len(result) > len(largeOutput) {
		t.Errorf("truncated result (%d) should be shorter than original (%d)", len(result), len(largeOutput))
	}
}

func TestFormatBgTaskCompletion_SmallOutputNotTruncated(t *testing.T) {
	now := time.Now()
	finished := now.Add(1 * time.Second)
	output := strings.Repeat("x", 500) // < 2000 threshold
	task := &BackgroundTask{
		ID:         "small1",
		Command:    "cat small.log",
		Status:     BgTaskDone,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   0,
		Output:     output,
	}

	result := FormatBgTaskCompletion(task)

	if strings.Contains(result, "truncated") {
		t.Error("should NOT truncate small output")
	}
	if !strings.Contains(result, output) {
		t.Error("should contain full output")
	}
}

func TestFormatBgTaskCompletion_NegativeExitCode(t *testing.T) {
	// Killed processes may have -1 exit code
	now := time.Now()
	finished := now.Add(1 * time.Second)
	task := &BackgroundTask{
		ID:         "kill1",
		Command:    "sleep 999",
		Status:     BgTaskDone,
		StartedAt:  now,
		FinishedAt: &finished,
		ExitCode:   -1,
		Error:      "signal: killed",
		Output:     "",
	}

	result := FormatBgTaskCompletion(task)

	// Negative exit code should not print "Exit Code: -1"
	if strings.Contains(result, "Exit Code: -1") {
		t.Error("should not show negative exit code")
	}
	if !strings.Contains(result, "signal: killed") {
		t.Error("missing error")
	}
}
