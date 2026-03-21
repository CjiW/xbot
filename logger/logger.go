package logger

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// 类型别名
type Fields = log.Fields
type Entry = log.Entry
type Formatter = log.Formatter
type Level = log.Level

// 级别常量
var (
	InfoLevel = log.InfoLevel
)

// JSONFormatter JSON 格式化器
type JSONFormatter = log.JSONFormatter

// TextFormatter 文本格式化器
type TextFormatter = log.TextFormatter

// SetupConfig 日志配置
type SetupConfig struct {
	Level      string // 日志级别：debug, info, warn, error
	Format     string // 日志格式：text, json
	WorkDir    string // 工作目录（日志文件存放位置）
	MaxAge     int    // 日志保留天数（默认 7 天）
	MaxBackups int    // 保留的旧日志文件数量（默认 0，表示不限制）
}

// dailyRotateFile 支持按日轮转的日志文件写入器
type dailyRotateFile struct {
	mu          sync.Mutex
	baseDir     string   // 日志目录
	currentFile *os.File // 当前日志文件
	currentDate string   // 当前日期（YYYY-MM-DD）
	baseName    string   // 基础文件名（如 "xbot"）
}

// newDailyRotateFile 创建按日轮转的日志文件写入器
func newDailyRotateFile(dir, baseName string) (*dailyRotateFile, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}
	drf := &dailyRotateFile{
		baseDir:  dir,
		baseName: baseName,
	}
	if err := drf.rotate(); err != nil {
		return nil, err
	}
	return drf, nil
}

// Write 实现 io.Writer 接口
func (drf *dailyRotateFile) Write(p []byte) (n int, err error) {
	drf.mu.Lock()
	defer drf.mu.Unlock()

	// 检查是否需要轮转（日期变化）
	today := time.Now().Format("2006-01-02")
	if drf.currentDate != today {
		if err := drf.rotateLocked(); err != nil {
			return 0, err
		}
	}

	if drf.currentFile == nil {
		return 0, fmt.Errorf("log file not initialized")
	}

	return drf.currentFile.Write(p)
}

// Close 关闭当前日志文件
func (drf *dailyRotateFile) Close() error {
	drf.mu.Lock()
	defer drf.mu.Unlock()
	if drf.currentFile != nil {
		err := drf.currentFile.Close()
		drf.currentFile = nil
		return err
	}
	return nil
}

// rotate 执行日志轮转（需要持有锁）
func (drf *dailyRotateFile) rotate() error {
	drf.mu.Lock()
	defer drf.mu.Unlock()
	return drf.rotateLocked()
}

// rotateLocked 执行日志轮转（已持有锁）
func (drf *dailyRotateFile) rotateLocked() error {
	today := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("%s-%s.log", drf.baseName, today)
	logPath := filepath.Join(drf.baseDir, logFileName)

	// 如果日期没变且文件已打开，无需轮转
	if drf.currentDate == today && drf.currentFile != nil {
		return nil
	}

	// 关闭旧文件
	if drf.currentFile != nil {
		_ = drf.currentFile.Close()
	}

	// 打开新文件（追加模式）
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	drf.currentFile = f
	drf.currentDate = today
	return nil
}

// 全局日志文件写入器（用于 Close）
var globalRotateFile *dailyRotateFile
var globalRotateFileMu sync.Mutex // 保护 globalRotateFile 的并发写入（C-11）

// Setup 设置日志系统（文件输出 + 按日轮转 + 自动清理）
func Setup(cfg SetupConfig) error {
	// 设置日志级别
	level, err := log.ParseLevel(cfg.Level)
	if err != nil {
		level = log.InfoLevel
	}
	log.SetLevel(level)

	// 设置日志格式
	switch cfg.Format {
	case "json":
		log.SetFormatter(&log.JSONFormatter{})
	default:
		log.SetFormatter(&log.TextFormatter{
			FullTimestamp: true,
		})
	}

	// 如果指定了工作目录，启用文件输出
	if cfg.WorkDir != "" {
		logDir := filepath.Join(cfg.WorkDir, ".xbot", "logs")
		drf, err := newDailyRotateFile(logDir, "xbot")
		if err != nil {
			return fmt.Errorf("failed to create log rotator: %w", err)
		}

		// 同时输出到文件和标准输出（方便 docker logs 查看）
		log.SetOutput(io.MultiWriter(os.Stdout, drf))
		globalRotateFileMu.Lock()
		globalRotateFile = drf
		globalRotateFileMu.Unlock()

		// 启动后台清理旧日志
		maxAge := cfg.MaxAge
		if maxAge <= 0 {
			maxAge = 7 // 默认保留 7 天
		}
		go cleanupOldLogs(logDir, maxAge)
	}

	return nil
}

// Close 关闭日志系统（刷新缓冲区，关闭文件）
func Close() {
	globalRotateFileMu.Lock()
	drf := globalRotateFile
	globalRotateFile = nil
	globalRotateFileMu.Unlock()
	if drf != nil {
		_ = drf.Close()
	}
}

// cleanupOldLogs 清理过期的日志文件
func cleanupOldLogs(dir string, maxAge int) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	// 启动时执行一次
	doCleanup(dir, maxAge)

	for range ticker.C {
		doCleanup(dir, maxAge)
	}
}

// doCleanup 执行清理逻辑
func doCleanup(dir string, maxAge int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 使用 fmt.Fprintf(os.Stderr, ...) 避免递归调用日志系统
		fmt.Fprintf(os.Stderr, "[logger] failed to read log directory: %v\n", err)
		return
	}

	cutoff := time.Now().AddDate(0, 0, -maxAge)
	var deleted []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// 只处理 .log 文件
		if !strings.HasSuffix(name, ".log") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[logger] failed to get file info for %s: %v\n", name, err)
			continue
		}

		// 根据修改时间判断是否过期
		if info.ModTime().Before(cutoff) {
			filePath := filepath.Join(dir, name)
			if err := os.Remove(filePath); err != nil {
				fmt.Fprintf(os.Stderr, "[logger] failed to delete old log file %s: %v\n", name, err)
			} else {
				deleted = append(deleted, name)
			}
		}
	}

	// 按名称排序，保证输出稳定
	if len(deleted) > 0 {
		sort.Strings(deleted)
		fmt.Fprintf(os.Stderr, "[logger] cleaned up %d old log file(s): %s\n", len(deleted), strings.Join(deleted, ", "))
	}
}

// SetFormatter 设置日志格式
func SetFormatter(formatter Formatter) { log.SetFormatter(formatter) }

// SetLevel 设置日志级别
func SetLevel(level Level) { log.SetLevel(level) }

// ParseLevel 解析日志级别字符串
func ParseLevel(lvl string) (Level, error) { return log.ParseLevel(lvl) }

// WithField 创建带字段的日志条目
func WithField(key string, value any) *Entry {
	return log.WithField(key, value)
}

// WithFields 创建带多个字段的日志条目
func WithFields(fields Fields) *Entry {
	return log.WithFields(fields)
}

// WithError 创建带错误的日志条目
func WithError(err error) *Entry {
	return log.WithError(err)
}

// Debug 输出 Debug 级别日志
func Debug(args ...any) { log.Debug(args...) }

// Debugf 输出格式化 Debug 级别日志
func Debugf(format string, args ...any) { log.Debugf(format, args...) }

// Info 输出 Info 级别日志
func Info(args ...any) { log.Info(args...) }

// Infof 输出格式化 Info 级别日志
func Infof(format string, args ...any) { log.Infof(format, args...) }

// Warn 输出 Warn 级别日志
func Warn(args ...any) { log.Warn(args...) }

// Warnf 输出格式化 Warn 级别日志
func Warnf(format string, args ...any) { log.Warnf(format, args...) }

// Error 输出 Error 级别日志
func Error(args ...any) { log.Error(args...) }

// Errorf 输出格式化 Error 级别日志
func Errorf(format string, args ...any) { log.Errorf(format, args...) }

// Fatal 输出 Fatal 级别日志并退出
func Fatal(args ...any) { log.Fatal(args...) }

// Fatalf 输出格式化 Fatal 级别日志并退出
func Fatalf(format string, args ...any) { log.Fatalf(format, args...) }
