package logger

import (
	log "github.com/sirupsen/logrus"
)

// Fields 日志字段类型（logrus.Fields 别名）
type Fields = log.Fields

// Entry 日志条目类型
type Entry = log.Entry

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
