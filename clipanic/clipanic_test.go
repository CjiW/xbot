package clipanic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoverWritesLogAndRepanics(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "cli-panic.log")
	EnableFileLogging(logPath)
	defer DisableFileLogging()

	defer func() {
		r := recover()
		if r != "boom" {
			t.Fatalf("expected repanic value boom, got %v", r)
		}

		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read panic log: %v", err)
		}
		content := string(data)
		if !strings.Contains(content, "where=main.main") {
			t.Fatalf("expected where in panic log, got: %s", content)
		}
		if !strings.Contains(content, "panic=boom") {
			t.Fatalf("expected panic value in panic log, got: %s", content)
		}
	}()

	func() {
		defer Recover("main.main", nil, true)
		panic("boom")
	}()
}

func TestRecoverWritesMessageType(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "cli-panic.log")
	EnableFileLogging(logPath)
	defer DisableFileLogging()

	type progressMsg struct{}

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected repanic")
			}
		}()
		func() {
			defer Recover("channel.cliModel.Update", progressMsg{}, true)
			panic("boom")
		}()
	}()

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read panic log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "msg=clipanic.progressMsg") {
		t.Fatalf("expected msg type in panic log, got: %s", content)
	}
}

func TestGoWritesLogAndSwallowsPanic(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "cli-panic.log")
	EnableFileLogging(logPath)
	defer DisableFileLogging()

	Go("worker.loop", func() {
		panic("worker boom")
	})

	// Go() launches a goroutine with defer Recover(...).
	// Recover writes the log synchronously under writeMu before the goroutine exits.
	// We can't observe goroutine completion directly, so poll for the log file.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		data, err := os.ReadFile(logPath)
		if err != nil {
			continue
		}
		content := string(data)
		if !strings.Contains(content, "where=worker.loop") {
			t.Fatalf("expected worker where in panic log, got: %s", content)
		}
		if !strings.Contains(content, "panic=worker boom") {
			t.Fatalf("expected worker panic in panic log, got: %s", content)
		}
		return // success
	}
	// Try one final read for a better error message
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("timed out: panic log file not created: %v", err)
	}
	t.Fatalf("timed out: log file exists but missing expected content: %s", string(data))
}
