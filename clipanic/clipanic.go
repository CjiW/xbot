package clipanic

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"xbot/config"
	log "xbot/logger"
)

var (
	configMu sync.RWMutex
	writeMu  sync.Mutex
	enabled  bool
	logPath  string
)

func DefaultLogPath() string {
	return filepath.Join(config.XbotHome(), "logs", "cli-panic.log")
}

func EnableFileLogging(path string) {
	if path == "" {
		path = DefaultLogPath()
	}
	configMu.Lock()
	defer configMu.Unlock()
	enabled = true
	logPath = path
}

func DisableFileLogging() {
	configMu.Lock()
	defer configMu.Unlock()
	enabled = false
	logPath = ""
}

func Recover(where string, msg any, repanic bool) {
	if r := recover(); r != nil {
		ReportWithStack(where, msg, r, debug.Stack())
		if repanic {
			panic(r)
		}
	}
}

func Report(where string, msg any, recovered any) {
	ReportWithStack(where, msg, recovered, debug.Stack())
}

func ReportWithStack(where string, msg any, recovered any, stack []byte) {
	msgLabel := formatMessage(msg)
	fields := log.Fields{"panic": recovered}
	if where != "" {
		fields["where"] = where
	}
	if msgLabel != "" {
		fields["msg"] = msgLabel
	}
	log.WithFields(fields).Error("CLI panic recovered")

	path, ok := currentLogPath()
	if !ok {
		return
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	var b strings.Builder
	fmt.Fprintf(&b, "\n==== %s panic ====\n", time.Now().Format(time.RFC3339))
	if where != "" {
		fmt.Fprintf(&b, "where=%s\n", where)
	}
	if msgLabel != "" {
		fmt.Fprintf(&b, "msg=%s\n", msgLabel)
	}
	fmt.Fprintf(&b, "panic=%v\n%s\n", recovered, stack)

	writeMu.Lock()
	defer writeMu.Unlock()
	_, _ = f.WriteString(b.String())
	_ = f.Sync()
}

func Go(where string, fn func()) {
	go func() {
		defer Recover(where, nil, false)
		fn()
	}()
}

func currentLogPath() (string, bool) {
	configMu.RLock()
	defer configMu.RUnlock()
	return logPath, enabled && logPath != ""
}

func formatMessage(msg any) string {
	switch v := msg.(type) {
	case nil:
		return ""
	case string:
		return v
	default:
		return fmt.Sprintf("%T", msg)
	}
}
