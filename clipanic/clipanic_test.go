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

	done := make(chan struct{})
	Go("worker.loop", func() {
		defer close(done)
		panic("worker boom")
	})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for worker goroutine")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read panic log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "where=worker.loop") {
		t.Fatalf("expected worker where in panic log, got: %s", content)
	}
	if !strings.Contains(content, "panic=worker boom") {
		t.Fatalf("expected worker panic in panic log, got: %s", content)
	}
}
